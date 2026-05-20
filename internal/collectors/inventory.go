// Package collectors holds the SSV-specific Prometheus collectors.
//
// Each collector implements the Child interface defined in scrape.go;
// the Scrape wrapper is what actually registers with prometheus.Registry.
//
// State integers from the SSV API are exposed as-is — the vendor enum
// mapping is not documented in the REST help and is left to dashboard
// authors.
package collectors

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/lblanc/ssv-prom-exporter/internal/ssv"
)

// Inventory fetches /serverGroups, /servers, /pools and /virtualDisks
// list endpoints synchronously on every scrape and emits a flat set of
// gauges describing topology and capacity.
type Inventory struct {
	client *ssv.Client
	log    *slog.Logger

	mu sync.Mutex // serialises scrapes against the same mgmt server

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

	hostState      *prometheus.Desc
	hostConnState  *prometheus.Desc
	hostMaint      *prometheus.Desc
	hostType       *prometheus.Desc
	hostInfo       *prometheus.Desc

	portConnected *prometheus.Desc
	portRole      *prometheus.Desc
	portInfo      *prometheus.Desc

	pdiskStatus *prometheus.Desc
	pdiskSize   *prometheus.Desc
	pdiskFree   *prometheus.Desc
	pdiskPool   *prometheus.Desc
	pdiskInfo   *prometheus.Desc
}

func NewInventory(client *ssv.Client, log *slog.Logger) *Inventory {
	if log == nil {
		log = slog.Default()
	}
	groupLabels := []string{"group_id", "group"}
	serverLabels := []string{"server_id", "server", "group_id"}
	poolLabels := []string{"pool_id", "pool", "server_id"}
	vdiskLabels := []string{"vdisk_id", "vdisk"}
	hostLabels := []string{"host_id", "host"}
	portLabels := []string{"port_id", "port", "host_id", "host"}
	pdiskLabels := []string{"disk_id", "disk", "host_id", "pool", "tier"}
	pdiskPoolLabels := []string{"disk_id", "disk", "host_id", "pool_id", "pool", "tier"}

	return &Inventory{
		client: client,
		log:    log,

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
		serverInfo:     desc("server_info", "Static server information (always 1).", []string{"server_id", "server", "host_name", "product_name", "product_version", "product_build", "os_version"}),
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

		hostState:     desc("host_state", "Host state (numeric, vendor-defined).", hostLabels),
		hostConnState: desc("host_connection_state", "Host connection state (numeric, vendor-defined).", hostLabels),
		hostMaint:     desc("host_maintenance_mode", "1 if the host is in maintenance mode.", hostLabels),
		hostType:      desc("host_type", "Host type (numeric, vendor-defined).", hostLabels),
		hostInfo:      desc("host_info", "Static host information (always 1).", []string{"host_id", "host", "host_name", "description", "version"}),

		portConnected: desc("port_connected", "1 if the SCSI/iSCSI port reports as connected.", portLabels),
		portRole:      desc("port_role_capability", "Port role bitmap (vendor-defined; mixes front-end / mirror / back-end roles).", portLabels),
		portInfo:      desc("port_info", "Static port information (always 1).", []string{"port_id", "port", "host_id", "host", "port_name", "alias", "port_type", "port_mode"}),

		pdiskStatus: desc("physical_disk_status", "Physical disk status (numeric, vendor-defined). Only Type==4 pool-member disks are exposed.", pdiskLabels),
		pdiskSize:   desc("physical_disk_size_bytes", "Physical disk capacity.", pdiskLabels),
		pdiskFree:   desc("physical_disk_free_bytes", "Physical disk free space.", pdiskLabels),
		pdiskPool:   desc("physical_disk_pool", "Maps a physical disk to its pool and tier (always 1). Joinable on disk_id with the perf metrics.", pdiskPoolLabels),
		pdiskInfo:   desc("physical_disk_info", "Static physical disk information (always 1).", []string{"disk_id", "disk", "host_id", "vendor", "product", "revision", "is_solid_state", "bus_type"}),
	}
}

func (c *Inventory) Name() string { return "inventory" }

func (c *Inventory) Describe(ch chan<- *prometheus.Desc) {
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
	ch <- c.hostState
	ch <- c.hostConnState
	ch <- c.hostMaint
	ch <- c.hostType
	ch <- c.hostInfo
	ch <- c.portConnected
	ch <- c.portRole
	ch <- c.portInfo
	ch <- c.pdiskStatus
	ch <- c.pdiskSize
	ch <- c.pdiskFree
	ch <- c.pdiskPool
	ch <- c.pdiskInfo
}

func (c *Inventory) CollectMetrics(ctx context.Context, ch chan<- prometheus.Metric) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	groups, gerr := c.client.ServerGroups(ctx)
	servers, serr := c.client.Servers(ctx)
	pools, perr := c.client.Pools(ctx)
	vdisks, verr := c.client.VirtualDisks(ctx)
	hosts, herr := c.client.Hosts(ctx)
	ports, prerr := c.client.Ports(ctx)
	pdisks, pderr := c.client.PhysicalDisks(ctx)
	pmembers, pmerr := c.client.PoolMembers(ctx)

	ok := true
	for _, e := range []error{gerr, serr, perr, verr, herr, prerr, pderr, pmerr} {
		if e != nil {
			c.log.Error("ssv inventory scrape error", "err", e)
			ok = false
		}
	}

	// Drop servers that belong to a remote (federated) SSV group: their
	// /performance endpoint returns no usable data and descriptive
	// fields are empty stubs, so they only add noise to the dashboards
	// and the failover IP pool.
	if gerr == nil && serr == nil {
		var localGroupID string
		for _, g := range groups {
			if g.OurGroup {
				localGroupID = g.ID
				break
			}
		}
		if localGroupID != "" {
			kept := servers[:0]
			for _, s := range servers {
				if s.GroupID == localGroupID {
					kept = append(kept, s)
				}
			}
			servers = kept
		}
	}

	// Refresh the client's failover backup list with all IPs reported by
	// the servers in the group. The client de-dups against the primary
	// host and filters out unusable IPs (loopback, link-local, IPv6).
	if serr == nil {
		var ips []string
		for _, s := range servers {
			ips = append(ips, s.IpAddresses...)
		}
		c.client.SetBackups(ips)
	}

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
		ch <- prometheus.MustNewConstMetric(c.serverInfo, prometheus.GaugeValue, 1, s.ID, s.Caption, s.HostName, s.ProductName, s.ProductVersion, s.ProductBuild, s.OsVersion)
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

	for _, h := range hosts {
		if h.Internal {
			// SSV creates internal pseudo-hosts for its own bookkeeping —
			// skip them so dashboards focus on real SAN clients.
			continue
		}
		labels := []string{h.ID, h.Caption}
		ch <- prometheus.MustNewConstMetric(c.hostState, prometheus.GaugeValue, float64(h.State), labels...)
		ch <- prometheus.MustNewConstMetric(c.hostConnState, prometheus.GaugeValue, float64(h.ConnectionState), labels...)
		ch <- prometheus.MustNewConstMetric(c.hostMaint, prometheus.GaugeValue, btof(h.InMaintenanceMode), labels...)
		ch <- prometheus.MustNewConstMetric(c.hostType, prometheus.GaugeValue, float64(h.Type), labels...)
		ch <- prometheus.MustNewConstMetric(c.hostInfo, prometheus.GaugeValue, 1, h.ID, h.Caption, h.HostName, h.Description, h.Version)
	}

	// Unified host_id -> caption map. Covers external SAN clients
	// (from /hosts) AND the SDS servers themselves (from /servers,
	// because SDS-owned ports like "SDS122_FE1" carry the server's
	// own UUID as HostId). Without the server side, those ports
	// would render with an empty `host` label.
	captionByHostID := make(map[string]string, len(hosts)+len(servers))
	for _, h := range hosts {
		captionByHostID[h.ID] = h.Caption
	}
	for _, s := range servers {
		captionByHostID[s.ID] = s.Caption
	}

	for _, p := range ports {
		if p.Internal {
			continue
		}
		hostCaption := captionByHostID[p.HostID]
		labels := []string{p.ID, p.Caption, p.HostID, hostCaption}
		ch <- prometheus.MustNewConstMetric(c.portConnected, prometheus.GaugeValue, btof(p.Connected), labels...)
		ch <- prometheus.MustNewConstMetric(c.portRole, prometheus.GaugeValue, float64(p.RoleCapability), labels...)
		ch <- prometheus.MustNewConstMetric(c.portInfo, prometheus.GaugeValue, 1,
			p.ID, p.Caption, p.HostID, hostCaption, p.PortName, p.Alias,
			itoa(p.PortType), itoa(p.PortMode),
		)
	}

	// Build pool/poolMember lookup tables once so each Type==4 disk can
	// be tagged with its pool and tier in one place.
	poolByID := make(map[string]ssv.Pool, len(pools))
	for _, p := range pools {
		poolByID[p.ID] = p
	}
	memberByID := make(map[string]ssv.PoolMember, len(pmembers))
	for _, m := range pmembers {
		memberByID[m.ID] = m
	}

	for _, d := range pdisks {
		// Type==4 is "real spinning rust / SSD / NVMe attached to a SDS
		// server". The other types (0=system, 6=mirror pseudo-disk,
		// 7=client virtual disk) are not pool members and have no
		// useful metrics for this dashboard.
		if d.Type != 4 || d.Internal {
			continue
		}
		// Resolve pool and tier up front so they can be carried as
		// labels on every per-disk metric. This duplicates what
		// ssv_physical_disk_pool exposes as a relation, but having
		// `pool` on the perf metrics directly lets Grafana ad-hoc
		// label filters (e.g. `pool=xxx`) match these series too.
		var poolID, poolCaption, tier string
		if m, ok := memberByID[d.PoolMemberID]; ok {
			poolID = m.DiskPoolID
			tier = itoa(m.DiskTier)
			if p, ok := poolByID[poolID]; ok {
				poolCaption = p.Caption
			}
		}
		labels := []string{d.ID, d.Caption, d.HostID, poolCaption, tier}
		ch <- prometheus.MustNewConstMetric(c.pdiskStatus, prometheus.GaugeValue, float64(d.DiskStatus), labels...)
		ch <- prometheus.MustNewConstMetric(c.pdiskSize, prometheus.GaugeValue, float64(d.Size.Value), labels...)
		ch <- prometheus.MustNewConstMetric(c.pdiskFree, prometheus.GaugeValue, float64(d.FreeSpace.Value), labels...)
		ch <- prometheus.MustNewConstMetric(c.pdiskInfo, prometheus.GaugeValue, 1,
			d.ID, d.Caption, d.HostID,
			strings.TrimSpace(d.InquiryData.Vendor),
			strings.TrimSpace(d.InquiryData.Product),
			strings.TrimSpace(d.InquiryData.Revision),
			boolToStr(d.IsSolidState),
			itoa(d.BusType),
		)
		if poolID != "" {
			ch <- prometheus.MustNewConstMetric(c.pdiskPool, prometheus.GaugeValue, 1,
				d.ID, d.Caption, d.HostID, poolID, poolCaption, tier,
			)
		}
	}

	return ok
}
