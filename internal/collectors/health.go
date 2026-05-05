package collectors

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/lblanc/ssv-prom-exporter/internal/ssv"
)

// Health fetches per-resource monitor states from /monitors and the
// active alert count from /alerts.
type Health struct {
	client *ssv.Client
	log    *slog.Logger
	mu     sync.Mutex

	monitorState *prometheus.Desc
	alertsTotal  *prometheus.Desc
}

func NewHealth(client *ssv.Client, log *slog.Logger) *Health {
	if log == nil {
		log = slog.Default()
	}
	return &Health{
		client: client,
		log:    log,
		monitorState: desc("monitor_state",
			"Monitor state (numeric, vendor-defined).",
			[]string{"monitor_id", "template", "target_id", "caption"}),
		alertsTotal: desc("alerts_total", "Number of active alerts.", nil),
	}
}

func (c *Health) Name() string { return "health" }

func (c *Health) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.monitorState
	ch <- c.alertsTotal
}

func (c *Health) CollectMetrics(ctx context.Context, ch chan<- prometheus.Metric) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	monitors, mErr := c.client.Monitors(ctx)
	alertCount, aErr := c.client.AlertsCount(ctx)

	ok := true
	for _, e := range []error{mErr, aErr} {
		if e != nil {
			c.log.Error("ssv health scrape error", "err", e)
			ok = false
		}
	}

	for _, m := range monitors {
		if m.Internal {
			continue
		}
		ch <- prometheus.MustNewConstMetric(c.monitorState, prometheus.GaugeValue, float64(m.State),
			m.ID, shortTemplate(m.TemplateID), m.MonitoredObject, m.Caption)
	}

	ch <- prometheus.MustNewConstMetric(c.alertsTotal, prometheus.GaugeValue, float64(alertCount))

	return ok
}

// shortTemplate trims SSV's TemplateId down to the class name.
//
//	T(DataCore.Executive.Controller.PhysicalDiskStateMonitor<5470A365-...>)
//	  -> PhysicalDiskStateMonitor
//	T(DataCore.Executive.Controller.RisingThresholdPerfMonitor`1[T]<...>BusyCount)
//	  -> RisingThresholdPerfMonitor
//
// The discriminating detail (e.g. "BusyCount", "Storage latency") stays
// in the Caption / MessageText fields exposed elsewhere.
func shortTemplate(s string) string {
	s = strings.TrimPrefix(s, "T(")
	s = strings.TrimSuffix(s, ")")
	if i := strings.Index(s, "<"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, "`"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return s
}
