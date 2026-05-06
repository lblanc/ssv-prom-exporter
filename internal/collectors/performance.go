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
	hostMappings   []perfMapping
	portMappings   []perfMapping
	pdiskMappings  []perfMapping
}

// perfMapping links a SSV counter name to the Prometheus desc and value
// type used to emit it.
//
// scale converts the raw int64 value before emission (e.g. 1e-3 for
// SSV's millisecond timers, which Prometheus convention exposes as
// seconds). zero scale is treated as 1.0.
//
// extraLabels are appended to the per-object label values supplied at
// emit time. This lets a single Desc (with one extra label such as
// "class") fan out into several mappings, each pinned to a fixed value.
type perfMapping struct {
	key         string
	desc        *prometheus.Desc
	valType     prometheus.ValueType
	scale       float64
	extraLabels []string
}

// timeScale converts SSV's millisecond timers to Prometheus' canonical
// seconds. Verified empirically against PSP 20: average IO time per
// op falls in the 0.6–3 range, matching SSD/cache latencies in ms.
const timeScale = 1e-3

func NewPerformance(client *ssv.Client, log *slog.Logger, workers int) *Performance {
	if log == nil {
		log = slog.Default()
	}
	if workers <= 0 {
		workers = 8
	}
	serverLabels := []string{"server_id", "server"}
	serverClassLabels := append(append([]string{}, serverLabels...), "class")
	poolLabels := []string{"pool_id", "pool", "server_id"}
	vdiskLabels := []string{"vdisk_id", "vdisk"}
	hostLabels := []string{"host_id", "host"}
	portLabels := []string{"port_id", "port", "host_id"}
	pdiskLabels := []string{"disk_id", "disk", "host_id"}

	c := &Performance{
		client:  client,
		log:     log,
		workers: workers,
	}
	// Per-class shared descs, fanned out below by class label.
	classOps := desc("server_class_io_operations_total", "Cumulative IO operations on this DataCore server, broken down by IO pipeline class.", serverClassLabels)
	classTime := desc("server_class_io_time_seconds_total", "Cumulative time spent on IO operations on this DataCore server, broken down by IO pipeline class.", serverClassLabels)
	classMaxTime := desc("server_class_io_max_time_seconds", "Peak IO duration recently observed on this DataCore server, broken down by IO pipeline class.", serverClassLabels)
	type cls struct{ label, prefix string }
	for _, k := range []cls{
		{"front_end_target", "FrontEndTarget"},
		{"mirror_target", "MirrorTarget"},
		{"physical_disk", "PhysicalDisk"},
		{"pool", "Pool"},
		{"target", "Target"},
	} {
		c.serverMappings = append(c.serverMappings,
			perfMapping{key: k.prefix + "Operations", desc: classOps, valType: prometheus.CounterValue, extraLabels: []string{k.label}},
			perfMapping{key: k.prefix + "TotalOperationsTime", desc: classTime, valType: prometheus.CounterValue, scale: timeScale, extraLabels: []string{k.label}},
			perfMapping{key: k.prefix + "MaxIOTime", desc: classMaxTime, valType: prometheus.GaugeValue, scale: timeScale, extraLabels: []string{k.label}},
		)
	}
	c.serverMappings = append(c.serverMappings,
		perfMapping{key: "TotalBytesRead", desc: desc("server_read_bytes_total", "Cumulative bytes read by this DataCore server.", serverLabels), valType: prometheus.CounterValue},
		perfMapping{key: "TotalBytesWritten", desc: desc("server_write_bytes_total", "Cumulative bytes written by this DataCore server.", serverLabels), valType: prometheus.CounterValue},
		perfMapping{key: "TotalReads", desc: desc("server_read_ops_total", "Cumulative read operations on this DataCore server.", serverLabels), valType: prometheus.CounterValue},
		perfMapping{key: "TotalWrites", desc: desc("server_write_ops_total", "Cumulative write operations on this DataCore server.", serverLabels), valType: prometheus.CounterValue},
		perfMapping{key: "CacheReadHits", desc: desc("server_cache_read_hits_total", "Cumulative cache read hits.", serverLabels), valType: prometheus.CounterValue},
		perfMapping{key: "CacheReadMisses", desc: desc("server_cache_read_misses_total", "Cumulative cache read misses.", serverLabels), valType: prometheus.CounterValue},
		perfMapping{key: "CacheWriteHits", desc: desc("server_cache_write_hits_total", "Cumulative cache write hits.", serverLabels), valType: prometheus.CounterValue},
		perfMapping{key: "CacheWriteMisses", desc: desc("server_cache_write_misses_total", "Cumulative cache write misses.", serverLabels), valType: prometheus.CounterValue},
		perfMapping{key: "CacheSize", desc: desc("server_cache_size_bytes", "Configured cache size on this server.", serverLabels), valType: prometheus.GaugeValue},
		perfMapping{key: "FreeCache", desc: desc("server_cache_free_bytes", "Free cache space on this server.", serverLabels), valType: prometheus.GaugeValue},
	)
	c.poolMappings = []perfMapping{
		{key: "TotalBytesRead", desc: desc("pool_read_bytes_total", "Cumulative bytes read from this pool.", poolLabels), valType: prometheus.CounterValue},
		{key: "TotalBytesWritten", desc: desc("pool_write_bytes_total", "Cumulative bytes written to this pool.", poolLabels), valType: prometheus.CounterValue},
		{key: "TotalReads", desc: desc("pool_read_ops_total", "Cumulative read operations on this pool.", poolLabels), valType: prometheus.CounterValue},
		{key: "TotalWrites", desc: desc("pool_write_ops_total", "Cumulative write operations on this pool.", poolLabels), valType: prometheus.CounterValue},
		{key: "TotalReadTime", desc: desc("pool_read_time_seconds_total", "Cumulative time spent on read operations on this pool.", poolLabels), valType: prometheus.CounterValue, scale: timeScale},
		{key: "TotalWriteTime", desc: desc("pool_write_time_seconds_total", "Cumulative time spent on write operations on this pool.", poolLabels), valType: prometheus.CounterValue, scale: timeScale},
		{key: "TotalOperationsTime", desc: desc("pool_io_time_seconds_total", "Cumulative time spent on IO operations on this pool.", poolLabels), valType: prometheus.CounterValue, scale: timeScale},
		{key: "MaxReadTime", desc: desc("pool_read_max_time_seconds", "Peak read duration recently observed on this pool.", poolLabels), valType: prometheus.GaugeValue, scale: timeScale},
		{key: "MaxWriteTime", desc: desc("pool_write_max_time_seconds", "Peak write duration recently observed on this pool.", poolLabels), valType: prometheus.GaugeValue, scale: timeScale},
		{key: "MaxReadWriteTime", desc: desc("pool_io_max_time_seconds", "Peak IO duration recently observed on this pool.", poolLabels), valType: prometheus.GaugeValue, scale: timeScale},
		{key: "BytesTotal", desc: desc("pool_capacity_bytes", "Total capacity of this pool.", poolLabels), valType: prometheus.GaugeValue},
		{key: "BytesAllocated", desc: desc("pool_used_bytes", "Allocated bytes in this pool.", poolLabels), valType: prometheus.GaugeValue},
		{key: "BytesAvailable", desc: desc("pool_available_bytes", "Available bytes in this pool.", poolLabels), valType: prometheus.GaugeValue},
		{key: "BytesReserved", desc: desc("pool_reserved_bytes", "Reserved bytes in this pool.", poolLabels), valType: prometheus.GaugeValue},
		{key: "BytesInReclamation", desc: desc("pool_reclamation_bytes", "Bytes currently being reclaimed in this pool.", poolLabels), valType: prometheus.GaugeValue},
		{key: "BytesOverSubscribed", desc: desc("pool_oversubscribed_bytes", "Over-subscribed bytes in this pool.", poolLabels), valType: prometheus.GaugeValue},
	}
	c.vdiskMappings = []perfMapping{
		{key: "TotalBytesRead", desc: desc("virtual_disk_read_bytes_total", "Cumulative bytes read from this virtual disk.", vdiskLabels), valType: prometheus.CounterValue},
		{key: "TotalBytesWritten", desc: desc("virtual_disk_write_bytes_total", "Cumulative bytes written to this virtual disk.", vdiskLabels), valType: prometheus.CounterValue},
		{key: "TotalReads", desc: desc("virtual_disk_read_ops_total", "Cumulative read operations on this virtual disk.", vdiskLabels), valType: prometheus.CounterValue},
		{key: "TotalWrites", desc: desc("virtual_disk_write_ops_total", "Cumulative write operations on this virtual disk.", vdiskLabels), valType: prometheus.CounterValue},
		{key: "CacheReadHits", desc: desc("virtual_disk_cache_read_hits_total", "Cumulative cache read hits for this virtual disk.", vdiskLabels), valType: prometheus.CounterValue},
		{key: "CacheReadMisses", desc: desc("virtual_disk_cache_read_misses_total", "Cumulative cache read misses for this virtual disk.", vdiskLabels), valType: prometheus.CounterValue},
		{key: "CacheWriteHits", desc: desc("virtual_disk_cache_write_hits_total", "Cumulative cache write hits for this virtual disk.", vdiskLabels), valType: prometheus.CounterValue},
		{key: "CacheWriteMisses", desc: desc("virtual_disk_cache_write_misses_total", "Cumulative cache write misses for this virtual disk.", vdiskLabels), valType: prometheus.CounterValue},
		{key: "TotalOperationsTime", desc: desc("virtual_disk_io_time_seconds_total", "Cumulative time spent on IO operations on this virtual disk.", vdiskLabels), valType: prometheus.CounterValue, scale: timeScale},
		{key: "MaxReadWriteTime", desc: desc("virtual_disk_io_max_time_seconds", "Peak IO duration recently observed on this virtual disk.", vdiskLabels), valType: prometheus.GaugeValue, scale: timeScale},
	}
	c.hostMappings = []perfMapping{
		{key: "TotalBytesRead", desc: desc("host_read_bytes_total", "Cumulative bytes read by this SAN client.", hostLabels), valType: prometheus.CounterValue},
		{key: "TotalBytesWritten", desc: desc("host_write_bytes_total", "Cumulative bytes written by this SAN client.", hostLabels), valType: prometheus.CounterValue},
		{key: "TotalReads", desc: desc("host_read_ops_total", "Cumulative read operations issued by this SAN client.", hostLabels), valType: prometheus.CounterValue},
		{key: "TotalWrites", desc: desc("host_write_ops_total", "Cumulative write operations issued by this SAN client.", hostLabels), valType: prometheus.CounterValue},
		{key: "TotalBytesProvisioned", desc: desc("host_provisioned_bytes", "Provisioned (mapped) capacity exposed to this SAN client.", hostLabels), valType: prometheus.GaugeValue},
		{key: "MaxReadSize", desc: desc("host_max_read_size_bytes", "Peak read IO size observed for this SAN client.", hostLabels), valType: prometheus.GaugeValue},
		{key: "MaxWriteSize", desc: desc("host_max_write_size_bytes", "Peak write IO size observed for this SAN client.", hostLabels), valType: prometheus.GaugeValue},
		{key: "MaxOperationSize", desc: desc("host_max_op_size_bytes", "Peak IO size (read or write) observed for this SAN client.", hostLabels), valType: prometheus.GaugeValue},
	}
	c.portMappings = []perfMapping{
		// Aggregate IO (sum of initiator + target).
		{key: "TotalReads", desc: desc("port_read_ops_total", "Cumulative read operations on this SCSI/iSCSI port.", portLabels), valType: prometheus.CounterValue},
		{key: "TotalWrites", desc: desc("port_write_ops_total", "Cumulative write operations on this SCSI/iSCSI port.", portLabels), valType: prometheus.CounterValue},
		{key: "TotalBytesRead", desc: desc("port_read_bytes_total", "Cumulative bytes read on this SCSI/iSCSI port.", portLabels), valType: prometheus.CounterValue},
		{key: "TotalBytesWritten", desc: desc("port_write_bytes_total", "Cumulative bytes written on this SCSI/iSCSI port.", portLabels), valType: prometheus.CounterValue},
		{key: "TotalPendingCommands", desc: desc("port_pending_commands", "Pending commands currently queued on this port.", portLabels), valType: prometheus.GaugeValue},
		// Target side (port acting as iSCSI/FC target — mostly the SDS server's front-end ports).
		{key: "TargetOperations", desc: desc("port_target_ops_total", "Cumulative operations on this port acting as target.", portLabels), valType: prometheus.CounterValue},
		{key: "TargetBytesTransferred", desc: desc("port_target_bytes_total", "Cumulative bytes transferred on this port acting as target.", portLabels), valType: prometheus.CounterValue},
		{key: "TargetTotalOperationsTime", desc: desc("port_target_io_time_seconds_total", "Cumulative time spent on target IO operations.", portLabels), valType: prometheus.CounterValue, scale: timeScale},
		{key: "TargetMaxIOTime", desc: desc("port_target_io_max_time_seconds", "Peak target IO duration recently observed.", portLabels), valType: prometheus.GaugeValue, scale: timeScale},
		// Initiator side (port acting as initiator — back-end / mirror traffic).
		{key: "InitiatorOperations", desc: desc("port_initiator_ops_total", "Cumulative operations on this port acting as initiator.", portLabels), valType: prometheus.CounterValue},
		{key: "InitiatorBytesTransferred", desc: desc("port_initiator_bytes_total", "Cumulative bytes transferred on this port acting as initiator.", portLabels), valType: prometheus.CounterValue},
		// Link-layer error counters (FC/iSCSI plumbing).
		{key: "BusyCount", desc: desc("port_busy_total", "Cumulative count of port-busy events.", portLabels), valType: prometheus.CounterValue},
		{key: "InvalidCrcCount", desc: desc("port_invalid_crc_total", "Cumulative invalid-CRC frames.", portLabels), valType: prometheus.CounterValue},
		{key: "LinkFailureCount", desc: desc("port_link_failure_total", "Cumulative link-failure events.", portLabels), valType: prometheus.CounterValue},
		{key: "LossOfSignalCount", desc: desc("port_loss_of_signal_total", "Cumulative loss-of-signal events.", portLabels), valType: prometheus.CounterValue},
		{key: "LossOfSyncCount", desc: desc("port_loss_of_sync_total", "Cumulative loss-of-sync events.", portLabels), valType: prometheus.CounterValue},
	}
	c.pdiskMappings = []perfMapping{
		// Note the API spelling here: physical-disk perf uses
		// `Total{Reads,Writes}Time` (with the 's'), unlike pool perf
		// which uses `Total{Read,Write}Time`. Easy to mis-copy.
		{key: "TotalReads", desc: desc("physical_disk_read_ops_total", "Cumulative read operations on this physical disk.", pdiskLabels), valType: prometheus.CounterValue},
		{key: "TotalWrites", desc: desc("physical_disk_write_ops_total", "Cumulative write operations on this physical disk.", pdiskLabels), valType: prometheus.CounterValue},
		{key: "TotalBytesRead", desc: desc("physical_disk_read_bytes_total", "Cumulative bytes read from this physical disk.", pdiskLabels), valType: prometheus.CounterValue},
		{key: "TotalBytesWritten", desc: desc("physical_disk_write_bytes_total", "Cumulative bytes written to this physical disk.", pdiskLabels), valType: prometheus.CounterValue},
		{key: "TotalReadsTime", desc: desc("physical_disk_read_time_seconds_total", "Cumulative time spent on read operations on this physical disk.", pdiskLabels), valType: prometheus.CounterValue, scale: timeScale},
		{key: "TotalWritesTime", desc: desc("physical_disk_write_time_seconds_total", "Cumulative time spent on write operations on this physical disk.", pdiskLabels), valType: prometheus.CounterValue, scale: timeScale},
		{key: "TotalOperationsTime", desc: desc("physical_disk_io_time_seconds_total", "Cumulative time spent on IO operations on this physical disk.", pdiskLabels), valType: prometheus.CounterValue, scale: timeScale},
		{key: "MaxReadTime", desc: desc("physical_disk_read_max_time_seconds", "Peak read duration recently observed on this physical disk.", pdiskLabels), valType: prometheus.GaugeValue, scale: timeScale},
		{key: "MaxWriteTime", desc: desc("physical_disk_write_max_time_seconds", "Peak write duration recently observed on this physical disk.", pdiskLabels), valType: prometheus.GaugeValue, scale: timeScale},
		{key: "MaxReadWriteTime", desc: desc("physical_disk_io_max_time_seconds", "Peak IO duration recently observed on this physical disk.", pdiskLabels), valType: prometheus.GaugeValue, scale: timeScale},
		{key: "AverageQueueLength", desc: desc("physical_disk_avg_queue_length", "Average queue length on this physical disk (gauge, vendor-defined).", pdiskLabels), valType: prometheus.GaugeValue},
		{key: "TotalPendingCommands", desc: desc("physical_disk_pending_commands", "Pending commands currently queued on this physical disk.", pdiskLabels), valType: prometheus.GaugeValue},
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
	for _, m := range c.hostMappings {
		ch <- m.desc
	}
	for _, m := range c.portMappings {
		ch <- m.desc
	}
	for _, m := range c.pdiskMappings {
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
	hosts, hErr := c.client.Hosts(ctx)
	ports, ptErr := c.client.Ports(ctx)
	pdisks, pdErr := c.client.PhysicalDisks(ctx)
	if err := errors.Join(sErr, pErr, vErr, hErr, ptErr, pdErr); err != nil {
		c.log.Error("ssv perf: inventory lookup failed", "err", err)
		return false
	}

	jobs := make([]perfJob, 0, len(servers)+len(pools)+len(vdisks)+len(hosts)+len(ports)+len(pdisks))
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
	for _, h := range hosts {
		if h.Internal {
			continue
		}
		jobs = append(jobs, perfJob{
			id:       h.ID,
			mappings: c.hostMappings,
			labels:   []string{h.ID, h.Caption},
		})
	}
	for _, p := range ports {
		if p.Internal {
			continue
		}
		jobs = append(jobs, perfJob{
			id:       p.ID,
			mappings: c.portMappings,
			labels:   []string{p.ID, p.Caption, p.HostID},
		})
	}
	for _, d := range pdisks {
		// Mirror inventory.go: only Type==4 disks are pool members and
		// have meaningful perf — the others (mirror pseudo-disks,
		// system disks, client virtual disks) report mostly zeros.
		if d.Type != 4 || d.Internal {
			continue
		}
		jobs = append(jobs, perfJob{
			id:       d.ID,
			mappings: c.pdiskMappings,
			labels:   []string{d.ID, d.Caption, d.HostID},
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
		scale := mp.scale
		if scale == 0 {
			scale = 1
		}
		val := float64(v) * scale
		if len(mp.extraLabels) == 0 {
			ch <- prometheus.MustNewConstMetric(mp.desc, mp.valType, val, labels...)
			continue
		}
		full := make([]string, 0, len(labels)+len(mp.extraLabels))
		full = append(full, labels...)
		full = append(full, mp.extraLabels...)
		ch <- prometheus.MustNewConstMetric(mp.desc, mp.valType, val, full...)
	}
}
