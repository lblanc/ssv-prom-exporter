package collectors

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/lblanc/ssv-prom-exporter/internal/ssv"
)

// Health fetches per-resource monitor states from /monitors and the
// active alerts from /alerts.
type Health struct {
	client *ssv.Client
	log    *slog.Logger
	mu     sync.Mutex

	monitorState *prometheus.Desc
	alertsTotal  *prometheus.Desc
	alertInfo    *prometheus.Desc
	alertAge     *prometheus.Desc
}

func NewHealth(client *ssv.Client, log *slog.Logger) *Health {
	if log == nil {
		log = slog.Default()
	}
	alertLabels := []string{"alert_id", "machine_id", "machine", "level", "high_priority", "needs_ack", "caller", "message"}
	return &Health{
		client: client,
		log:    log,
		monitorState: desc("monitor_state",
			"Monitor state (numeric, vendor-defined).",
			[]string{"monitor_id", "template", "target_id", "caption"}),
		alertsTotal: desc("alerts_total", "Number of active alerts.", nil),
		alertInfo:   desc("alert_info", "Per-alert info gauge (always 1). All useful fields are exposed as labels for table panels.", alertLabels),
		alertAge:    desc("alert_age_seconds", "Age of the alert (now - timestamp).", []string{"alert_id"}),
	}
}

func (c *Health) Name() string { return "health" }

func (c *Health) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.monitorState
	ch <- c.alertsTotal
	ch <- c.alertInfo
	ch <- c.alertAge
}

func (c *Health) CollectMetrics(ctx context.Context, ch chan<- prometheus.Metric) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	monitors, mErr := c.client.Monitors(ctx)
	alerts, aErr := c.client.Alerts(ctx)

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

	ch <- prometheus.MustNewConstMetric(c.alertsTotal, prometheus.GaugeValue, float64(len(alerts)))

	for _, a := range alerts {
		// SSV's composite alert ID is rendered as "<machine>:<seq>" so
		// each alert series is uniquely identified by a single label.
		alertID := a.ID.MachineID + ":" + strconv.FormatInt(a.ID.SequenceNumber, 10)
		ch <- prometheus.MustNewConstMetric(c.alertInfo, prometheus.GaugeValue, 1,
			alertID,
			a.ID.MachineID,
			a.MachineName,
			strconv.Itoa(a.Level),
			boolToStr(a.HighPriority),
			boolToStr(a.NeedsAcknowledge),
			a.Caller,
			a.MessageText,
		)
		if !a.TimeStamp.IsZero() {
			age := time.Since(a.TimeStamp.Time).Seconds()
			if age < 0 {
				age = 0
			}
			ch <- prometheus.MustNewConstMetric(c.alertAge, prometheus.GaugeValue, age, alertID)
		}
	}

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
