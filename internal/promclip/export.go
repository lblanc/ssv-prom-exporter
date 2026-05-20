package promclip

import (
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/prometheus/common/model"
)

// ExportRequest describes one export run.
type ExportRequest struct {
	Source      Connection
	From, To    time.Time
	Step        time.Duration
	MetricRegex string // optional; empty means "all metrics"
	OutputPath  string // .txt.gz path
}

// ExportResult is the post-flight summary.
type ExportResult struct {
	Series  int
	Samples int64
	Bytes   int64
}

// Export queries the source Prometheus over [From, To] at Step and
// writes all matching series as a gzipped OpenMetrics file at
// OutputPath. The format is replayable into another Prometheus via
// remote-write (see Import) or via `promtool tsdb create-blocks-from
// openmetrics`.
func Export(ctx context.Context, log *slog.Logger, req ExportRequest) (ExportResult, error) {
	if req.OutputPath == "" {
		return ExportResult{}, fmt.Errorf("OutputPath required")
	}
	if req.Step <= 0 {
		return ExportResult{}, fmt.Errorf("Step must be > 0")
	}
	if !req.From.Before(req.To) {
		return ExportResult{}, fmt.Errorf("From must be < To")
	}

	pc, err := NewPromClient(req.Source)
	if err != nil {
		return ExportResult{}, err
	}

	names, err := pc.ListMetricNames(ctx, req.MetricRegex, req.From, req.To)
	if err != nil {
		return ExportResult{}, err
	}
	log.Info("export: matched metrics", "count", len(names))

	f, err := os.Create(req.OutputPath)
	if err != nil {
		return ExportResult{}, fmt.Errorf("create output: %w", err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	w := bufio.NewWriterSize(gz, 64*1024)

	var res ExportResult
	for _, name := range names {
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		default:
		}
		series, err := pc.QueryRange(ctx, name, req.From, req.To, req.Step)
		if err != nil {
			log.Warn("export: query failed", "metric", name, "err", err)
			continue
		}
		if len(series) == 0 {
			continue
		}
		meta, _ := pc.Metadata(ctx, name) // best-effort
		if err := writeMetric(w, name, meta, series); err != nil {
			return res, err
		}
		res.Series += len(series)
		for _, s := range series {
			res.Samples += int64(len(s.Samples))
		}
	}

	if _, err := w.WriteString("# EOF\n"); err != nil {
		return res, err
	}
	if err := w.Flush(); err != nil {
		return res, err
	}
	if err := gz.Close(); err != nil {
		return res, err
	}
	if err := f.Sync(); err != nil {
		return res, err
	}
	if st, err := f.Stat(); err == nil {
		res.Bytes = st.Size()
	}
	return res, nil
}

// writeMetric emits one metric block (TYPE/HELP/UNIT + every sample of
// every series), in deterministic order.
func writeMetric(w *bufio.Writer, name string, meta MetricMeta, series []RangeSeries) error {
	// Header lines. TYPE first per OpenMetrics convention.
	mtype := normalizeType(meta.Type)
	if _, err := fmt.Fprintf(w, "# TYPE %s %s\n", name, mtype); err != nil {
		return err
	}
	if meta.Unit != "" {
		if _, err := fmt.Fprintf(w, "# UNIT %s %s\n", name, escapeHelp(meta.Unit)); err != nil {
			return err
		}
	}
	if meta.Help != "" {
		if _, err := fmt.Fprintf(w, "# HELP %s %s\n", name, escapeHelp(meta.Help)); err != nil {
			return err
		}
	}

	// Sort series by their (canonicalized) label set so the output is
	// stable across runs against an unchanged Prometheus.
	sort.SliceStable(series, func(i, j int) bool {
		return canonicalLabels(series[i].Labels) < canonicalLabels(series[j].Labels)
	})

	for _, s := range series {
		// Samples are already ordered by timestamp in Prometheus's response,
		// but enforce it to keep diffs stable.
		samples := append([]model.SamplePair(nil), s.Samples...)
		sort.SliceStable(samples, func(i, j int) bool {
			return samples[i].Timestamp < samples[j].Timestamp
		})
		labels := formatLabels(name, s.Labels)
		for _, sp := range samples {
			// OpenMetrics timestamps are seconds with optional fraction.
			tsSec := float64(sp.Timestamp) / 1000.0
			val := float64(sp.Value)
			if _, err := fmt.Fprintf(w, "%s %s %s\n",
				labels, formatFloat(val), formatTimestamp(tsSec)); err != nil {
				return err
			}
		}
	}
	return nil
}

// formatLabels renders `name{l1="v1",l2="v2",...}`. The __name__ label
// is consumed as the metric name, never emitted as a label.
func formatLabels(name string, labels model.Metric) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		if string(k) == "__name__" {
			continue
		}
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return name
	}
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(escapeLabelValue(string(labels[model.LabelName(k)])))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// canonicalLabels returns a stable string key for ordering.
func canonicalLabels(labels model.Metric) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(string(labels[model.LabelName(k)]))
		b.WriteByte(0)
	}
	return b.String()
}

func escapeLabelValue(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
	)
	return r.Replace(s)
}

func escapeHelp(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		"\n", `\n`,
	)
	return r.Replace(s)
}

func normalizeType(t string) string {
	if t == "" {
		return "unknown"
	}
	// Prometheus returns lowercase types; OpenMetrics accepts them as-is
	// (counter, gauge, histogram, summary, info, stateset, gaugehistogram,
	// unknown).
	return t
}

// formatFloat renders a float64 in a form Prometheus parses back losslessly.
func formatFloat(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	// 'g' picks the shortest of %e/%f that round-trips a float64.
	return fmt.Sprintf("%g", v)
}

func formatTimestamp(sec float64) string {
	return fmt.Sprintf("%g", sec)
}
