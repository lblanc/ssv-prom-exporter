package collectors

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/lblanc/ssv-prom-exporter/internal/ssv"
)

// Performance fans out one /performance/{id} call per server, pool and
// virtual disk through a bounded worker pool, then flat-maps the
// returned counter dictionaries onto Prometheus metrics.
//
// Counters are emitted with prometheus.CounterValue (cumulative integers
// suitable for rate()), and capacity / cache snapshots as gauges.
type Performance struct {
	client  *ssv.Client
	log     *slog.Logger
	workers int

	serverMappings []perfMapping
	poolMappings   []perfMapping
	vdiskMappings  []perfMapping
}

// perfMapping links a SSV counter name to the Prometheus desc and value
// type used to emit it.
type perfMapping struct {
	key     string
	desc    *prometheus.Desc
	valType prometheus.ValueType
}

func NewPerformance(client *ssv.Client, log *slog.Logger, workers int) *Performance {
	if log == nil {
		log = slog.Default()
	}
	if workers <= 0 {
		workers = 8
	}
	serverLabels := []string{"server_id", "server"}
	poolLabels := []string{"pool_id", "pool", "server_id"}
	vdiskLabels := []string{"vdisk_id", "vdisk"}

	c := &Performance{
		client:  client,
		log:     log,
		workers: workers,
	}
	c.serverMappings = []perfMapping{
		{"TotalBytesRead", desc("server_read_bytes_total", "Cumulative bytes read by this DataCore server.", serverLabels), prometheus.CounterValue},
		{"TotalBytesWritten", desc("server_write_bytes_total", "Cumulative bytes written by this DataCore server.", serverLabels), prometheus.CounterValue},
		{"TotalReads", desc("server_read_ops_total", "Cumulative read operations on this DataCore server.", serverLabels), prometheus.CounterValue},
		{"TotalWrites", desc("server_write_ops_total", "Cumulative write operations on this DataCore server.", serverLabels), prometheus.CounterValue},
		{"CacheReadHits", desc("server_cache_read_hits_total", "Cumulative cache read hits.", serverLabels), prometheus.CounterValue},
		{"CacheReadMisses", desc("server_cache_read_misses_total", "Cumulative cache read misses.", serverLabels), prometheus.CounterValue},
		{"CacheWriteHits", desc("server_cache_write_hits_total", "Cumulative cache write hits.", serverLabels), prometheus.CounterValue},
		{"CacheWriteMisses", desc("server_cache_write_misses_total", "Cumulative cache write misses.", serverLabels), prometheus.CounterValue},
		{"CacheSize", desc("server_cache_size_bytes", "Configured cache size on this server.", serverLabels), prometheus.GaugeValue},
		{"FreeCache", desc("server_cache_free_bytes", "Free cache space on this server.", serverLabels), prometheus.GaugeValue},
	}
	c.poolMappings = []perfMapping{
		{"TotalBytesRead", desc("pool_read_bytes_total", "Cumulative bytes read from this pool.", poolLabels), prometheus.CounterValue},
		{"TotalBytesWritten", desc("pool_write_bytes_total", "Cumulative bytes written to this pool.", poolLabels), prometheus.CounterValue},
		{"TotalReads", desc("pool_read_ops_total", "Cumulative read operations on this pool.", poolLabels), prometheus.CounterValue},
		{"TotalWrites", desc("pool_write_ops_total", "Cumulative write operations on this pool.", poolLabels), prometheus.CounterValue},
		{"BytesTotal", desc("pool_capacity_bytes", "Total capacity of this pool.", poolLabels), prometheus.GaugeValue},
		{"BytesAllocated", desc("pool_used_bytes", "Allocated bytes in this pool.", poolLabels), prometheus.GaugeValue},
		{"BytesAvailable", desc("pool_available_bytes", "Available bytes in this pool.", poolLabels), prometheus.GaugeValue},
		{"BytesReserved", desc("pool_reserved_bytes", "Reserved bytes in this pool.", poolLabels), prometheus.GaugeValue},
		{"BytesInReclamation", desc("pool_reclamation_bytes", "Bytes currently being reclaimed in this pool.", poolLabels), prometheus.GaugeValue},
		{"BytesOverSubscribed", desc("pool_oversubscribed_bytes", "Over-subscribed bytes in this pool.", poolLabels), prometheus.GaugeValue},
	}
	c.vdiskMappings = []perfMapping{
		{"TotalBytesRead", desc("virtual_disk_read_bytes_total", "Cumulative bytes read from this virtual disk.", vdiskLabels), prometheus.CounterValue},
		{"TotalBytesWritten", desc("virtual_disk_write_bytes_total", "Cumulative bytes written to this virtual disk.", vdiskLabels), prometheus.CounterValue},
		{"TotalReads", desc("virtual_disk_read_ops_total", "Cumulative read operations on this virtual disk.", vdiskLabels), prometheus.CounterValue},
		{"TotalWrites", desc("virtual_disk_write_ops_total", "Cumulative write operations on this virtual disk.", vdiskLabels), prometheus.CounterValue},
		{"CacheReadHits", desc("virtual_disk_cache_read_hits_total", "Cumulative cache read hits for this virtual disk.", vdiskLabels), prometheus.CounterValue},
		{"CacheReadMisses", desc("virtual_disk_cache_read_misses_total", "Cumulative cache read misses for this virtual disk.", vdiskLabels), prometheus.CounterValue},
		{"CacheWriteHits", desc("virtual_disk_cache_write_hits_total", "Cumulative cache write hits for this virtual disk.", vdiskLabels), prometheus.CounterValue},
		{"CacheWriteMisses", desc("virtual_disk_cache_write_misses_total", "Cumulative cache write misses for this virtual disk.", vdiskLabels), prometheus.CounterValue},
	}
	return c
}

func (c *Performance) Name() string { return "performance" }

func (c *Performance) Describe(ch chan<- *prometheus.Desc) {
	for _, m := range c.serverMappings {
		ch <- m.desc
	}
	for _, m := range c.poolMappings {
		ch <- m.desc
	}
	for _, m := range c.vdiskMappings {
		ch <- m.desc
	}
}

// perfJob is one /performance/{id} call to make.
type perfJob struct {
	id       string
	mappings []perfMapping
	labels   []string
}

func (c *Performance) CollectMetrics(ctx context.Context, ch chan<- prometheus.Metric) bool {
	// Inventory lookup. Errors here return early — without IDs we have
	// nothing to emit.
	servers, sErr := c.client.Servers(ctx)
	pools, pErr := c.client.Pools(ctx)
	vdisks, vErr := c.client.VirtualDisks(ctx)
	if err := errors.Join(sErr, pErr, vErr); err != nil {
		c.log.Error("ssv perf: inventory lookup failed", "err", err)
		return false
	}

	jobs := make([]perfJob, 0, len(servers)+len(pools)+len(vdisks))
	for _, s := range servers {
		jobs = append(jobs, perfJob{
			id:       s.ID,
			mappings: c.serverMappings,
			labels:   []string{s.ID, s.Caption},
		})
	}
	for _, p := range pools {
		jobs = append(jobs, perfJob{
			id:       p.ID,
			mappings: c.poolMappings,
			labels:   []string{p.ID, p.Caption, p.ServerID},
		})
	}
	for _, v := range vdisks {
		jobs = append(jobs, perfJob{
			id:       v.ID,
			mappings: c.vdiskMappings,
			labels:   []string{v.ID, v.Caption},
		})
	}

	sem := make(chan struct{}, c.workers)
	var wg sync.WaitGroup
	var failures int
	var failuresMu sync.Mutex

	for _, j := range jobs {
		sem <- struct{}{}
		wg.Add(1)
		go func(j perfJob) {
			defer func() { <-sem; wg.Done() }()

			counters, err := c.client.Performance(ctx, j.id)
			if err != nil {
				c.log.Error("ssv perf: scrape failed", "id", j.id, "err", err)
				failuresMu.Lock()
				failures++
				failuresMu.Unlock()
				return
			}
			emitFromMap(ch, j.mappings, counters, j.labels)
		}(j)
	}
	wg.Wait()

	return failures == 0
}

// emitFromMap walks the mapping table and emits a metric for each
// counter the snapshot actually contains. Missing keys are skipped
// silently — different SSV versions may add or remove counters and we
// don't want partial data to drop a healthy scrape.
func emitFromMap(ch chan<- prometheus.Metric, mappings []perfMapping, m ssv.CounterMap, labels []string) {
	for _, mp := range mappings {
		v, ok := m[mp.key]
		if !ok {
			continue
		}
		ch <- prometheus.MustNewConstMetric(mp.desc, mp.valType, float64(v), labels...)
	}
}
