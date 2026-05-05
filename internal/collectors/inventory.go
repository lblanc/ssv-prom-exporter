// Package collectors holds the prometheus.Collector implementations.
//
// The inventory collector fetches SSV's /serverGroups, /servers, /pools and
// /virtualDisks list endpoints synchronously on every scrape and emits a
// flat set of gauges. State integers from the API are exposed as-is — the
// vendor enum mapping (e.g. "1 means online") is not documented in the
// REST help and is left to dashboard authors.
package collectors

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/lblanc/ssv-prom-exporter/internal/ssv"
)

const namespace = "ssv"

// Inventory implements prometheus.Collector.
type Inventory struct {
	client *ssv.Client
	log    *slog.Logger

	mu sync.Mutex // serialises scrapes against the same mgmt server

	up             *prometheus.Desc
	scrapeDuration *prometheus.Desc

	groupState       *prometheus.Desc
	groupStorageUsed *prometheus.Desc
	groupStorageMax  *prometheus.Desc
	groupCompliance  *prometheus.Desc
	groupExpiry      *prometheus.Desc

	serverState    *prometheus.Desc
	serverSupport  *prometheus.Desc
	serverPower    *prometheus.Desc
	serverCache    *prometheus.Desc
	serverDiag     *prometheus.Desc
	serverMaint    *prometheus.Desc
	serverInfo     *prometheus.Desc
	serverStorage  *prometheus.Desc
	serverMemTotal *prometheus.Desc
	serverMemAvail *prometheus.Desc

	poolStatus   *prometheus.Desc
	poolPresence *prometheus.Desc
	poolType     *prometheus.Desc
	poolChunk    *prometheus.Desc

	vdiskStatus  *prometheus.Desc
	vdiskSize    *prometheus.Desc
	vdiskType    *prometheus.Desc
	vdiskOffline *prometheus.Desc
}

func NewInventory(client *ssv.Client, log *slog.Logger) *Inventory {
	if log == nil {
		log = slog.Default()
	}
	groupLabels := []string{"group_id", "group"}
	serverLabels := []string{"server_id", "server", "group_id"}
	poolLabels := []string{"pool_id", "pool", "server_id"}
	vdiskLabels := []string{"vdisk_id", "vdisk"}

	return &Inventory{
		client: client,
		log:    log,

		up:             desc("up", "1 if the last SSV scrape succeeded, 0 otherwise.", nil),
		scrapeDuration: desc("scrape_duration_seconds", "Duration of the last SSV scrape, per collector.", []string{"collector"}),

		groupState:       desc("server_group_state", "Server group state (numeric, vendor-defined).", groupLabels),
		groupStorageUsed: desc("server_group_storage_used_bytes", "Storage used at the server-group scope.", groupLabels),
		groupStorageMax:  desc("server_group_storage_max_bytes", "Maximum storage allowed by the server-group license.", groupLabels),
		groupCompliance:  desc("server_group_out_of_compliance", "1 if the server group is flagged out of compliance.", groupLabels),
		groupExpiry:      desc("server_group_license_expires_seconds", "Server group license expiration time as a Unix timestamp.", groupLabels),

		serverState:    desc("server_state", "Server state (numeric, vendor-defined).", serverLabels),
		serverSupport:  desc("server_support_state", "Server support state (numeric, vendor-defined).", serverLabels),
		serverPower:    desc("server_power_state", "Server power state (numeric, vendor-defined).", serverLabels),
		serverCache:    desc("server_cache_state", "Server cache state (numeric, vendor-defined).", serverLabels),
		serverDiag:     desc("server_diagnostic_mode", "Server diagnostic mode (numeric, vendor-defined).", serverLabels),
		serverMaint:    desc("server_maintenance_mode", "1 if the server is in maintenance mode.", serverLabels),
		serverInfo:     desc("server_info", "Static server information (always 1).", []string{"server_id", "server", "host_name", "product_version", "product_build", "os_version"}),
		serverStorage:  desc("server_storage_used_bytes", "Storage used by this server.", serverLabels),
		serverMemTotal: desc("server_memory_total_bytes", "Total system memory on the server.", serverLabels),
		serverMemAvail: desc("server_memory_available_bytes", "Available system memory on the server.", serverLabels),

		poolStatus:   desc("pool_status", "Pool status (numeric, vendor-defined).", poolLabels),
		poolPresence: desc("pool_presence_status", "Pool presence status (numeric, vendor-defined).", poolLabels),
		poolType:     desc("pool_type", "Pool type (numeric, vendor-defined).", poolLabels),
		poolChunk:    desc("pool_chunk_size_bytes", "Pool allocation chunk size.", poolLabels),

		vdiskStatus:  desc("virtual_disk_status", "Virtual disk status (numeric, vendor-defined).", vdiskLabels),
		vdiskSize:    desc("virtual_disk_size_bytes", "Virtual disk size.", vdiskLabels),
		vdiskType:    desc("virtual_disk_type", "Virtual disk type (numeric, vendor-defined).", vdiskLabels),
		vdiskOffline: desc("virtual_disk_offline", "1 if the virtual disk is offline.", vdiskLabels),
	}
}

func desc(name, help string, labels []string) *prometheus.Desc {
	return prometheus.NewDesc(namespace+"_"+name, help, labels, nil)
}

func (c *Inventory) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.up
	ch <- c.scrapeDuration
	ch <- c.groupState
	ch <- c.groupStorageUsed
	ch <- c.groupStorageMax
	ch <- c.groupCompliance
	ch <- c.groupExpiry
	ch <- c.serverState
	ch <- c.serverSupport
	ch <- c.serverPower
	ch <- c.serverCache
	ch <- c.serverDiag
	ch <- c.serverMaint
	ch <- c.serverInfo
	ch <- c.serverStorage
	ch <- c.serverMemTotal
	ch <- c.serverMemAvail
	ch <- c.poolStatus
	ch <- c.poolPresence
	ch <- c.poolType
	ch <- c.poolChunk
	ch <- c.vdiskStatus
	ch <- c.vdiskSize
	ch <- c.vdiskType
	ch <- c.vdiskOffline
}

func (c *Inventory) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	groups, gerr := c.client.ServerGroups(ctx)
	servers, serr := c.client.Servers(ctx)
	pools, perr := c.client.Pools(ctx)
	vdisks, verr := c.client.VirtualDisks(ctx)

	ok := 1.0
	for _, e := range []error{gerr, serr, perr, verr} {
		if e != nil {
			c.log.Error("ssv inventory scrape error", "err", e)
			ok = 0
		}
	}
	ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, ok)
	ch <- prometheus.MustNewConstMetric(c.scrapeDuration, prometheus.GaugeValue, time.Since(start).Seconds(), "inventory")

	for _, g := range groups {
		labels := []string{g.ID, g.Caption}
		ch <- prometheus.MustNewConstMetric(c.groupState, prometheus.GaugeValue, float64(g.State), labels...)
		ch <- prometheus.MustNewConstMetric(c.groupStorageUsed, prometheus.GaugeValue, float64(g.StorageUsed.Value), labels...)
		ch <- prometheus.MustNewConstMetric(c.groupStorageMax, prometheus.GaugeValue, float64(g.MaxStorage.Value), labels...)
		ch <- prometheus.MustNewConstMetric(c.groupCompliance, prometheus.GaugeValue, btof(g.OutOfCompliance), labels...)
		if !g.NextExpirationDate.IsZero() {
			ch <- prometheus.MustNewConstMetric(c.groupExpiry, prometheus.GaugeValue, float64(g.NextExpirationDate.Unix()), labels...)
		}
	}

	for _, s := range servers {
		labels := []string{s.ID, s.Caption, s.GroupID}
		ch <- prometheus.MustNewConstMetric(c.serverState, prometheus.GaugeValue, float64(s.State), labels...)
		ch <- prometheus.MustNewConstMetric(c.serverSupport, prometheus.GaugeValue, float64(s.SupportState), labels...)
		ch <- prometheus.MustNewConstMetric(c.serverPower, prometheus.GaugeValue, float64(s.PowerState), labels...)
		ch <- prometheus.MustNewConstMetric(c.serverCache, prometheus.GaugeValue, float64(s.CacheState), labels...)
		ch <- prometheus.MustNewConstMetric(c.serverDiag, prometheus.GaugeValue, float64(s.DiagnosticMode), labels...)
		ch <- prometheus.MustNewConstMetric(c.serverMaint, prometheus.GaugeValue, btof(s.MaintenanceModeEnabled), labels...)
		ch <- prometheus.MustNewConstMetric(c.serverStorage, prometheus.GaugeValue, float64(s.StorageUsed.Value), labels...)
		ch <- prometheus.MustNewConstMetric(c.serverMemTotal, prometheus.GaugeValue, float64(s.TotalSystemMemory.Value), labels...)
		ch <- prometheus.MustNewConstMetric(c.serverMemAvail, prometheus.GaugeValue, float64(s.AvailableSystemMemory.Value), labels...)
		ch <- prometheus.MustNewConstMetric(c.serverInfo, prometheus.GaugeValue, 1, s.ID, s.Caption, s.HostName, s.ProductVersion, s.ProductBuild, s.OsVersion)
	}

	for _, p := range pools {
		labels := []string{p.ID, p.Caption, p.ServerID}
		ch <- prometheus.MustNewConstMetric(c.poolStatus, prometheus.GaugeValue, float64(p.PoolStatus), labels...)
		ch <- prometheus.MustNewConstMetric(c.poolPresence, prometheus.GaugeValue, float64(p.PresenceStatus), labels...)
		ch <- prometheus.MustNewConstMetric(c.poolType, prometheus.GaugeValue, float64(p.Type), labels...)
		ch <- prometheus.MustNewConstMetric(c.poolChunk, prometheus.GaugeValue, float64(p.ChunkSize.Value), labels...)
	}

	for _, v := range vdisks {
		labels := []string{v.ID, v.Caption}
		ch <- prometheus.MustNewConstMetric(c.vdiskStatus, prometheus.GaugeValue, float64(v.DiskStatus), labels...)
		ch <- prometheus.MustNewConstMetric(c.vdiskSize, prometheus.GaugeValue, float64(v.Size.Value), labels...)
		ch <- prometheus.MustNewConstMetric(c.vdiskType, prometheus.GaugeValue, float64(v.Type), labels...)
		ch <- prometheus.MustNewConstMetric(c.vdiskOffline, prometheus.GaugeValue, btof(v.Offline), labels...)
	}
}

func btof(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
