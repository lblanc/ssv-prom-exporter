package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lblanc/ssv-prom-exporter/internal/promclip"
)

// runExportCLI runs a one-shot export from the command line. No web
// server, no state directory, no port — the caller passes all inputs
// as flags and we write the OpenMetrics file to -out.
func runExportCLI(args []string) int {
	fs := flag.NewFlagSet("prom-clip export", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: prom-clip export [options]")
		fmt.Fprintln(fs.Output(), "")
		fmt.Fprintln(fs.Output(), "Export a time window from a source Prometheus to an OpenMetrics .txt.gz file.")
		fmt.Fprintln(fs.Output(), "")
		fmt.Fprintln(fs.Output(), "Options:")
		fs.PrintDefaults()
		fmt.Fprintln(fs.Output(), "")
		fmt.Fprintln(fs.Output(), "Time formats for -from / -to:")
		fmt.Fprintln(fs.Output(), "  RFC3339 absolute   2026-05-21T09:00:00Z")
		fmt.Fprintln(fs.Output(), "  relative duration  -1h, -30m, -2h30m  (interpreted as now+duration)")
		fmt.Fprintln(fs.Output(), "  literal            now")
	}

	var (
		src      = fs.String("src", "", "Source Prometheus URL (required)")
		user     = fs.String("src-user", "", "Source basic-auth username (optional)")
		pass     = fs.String("src-pass", "", "Source basic-auth password (optional)")
		insecure = fs.Bool("src-insecure", false, "Skip TLS verification on the source")
		fromArg  = fs.String("from", "-1h", "Start of the window (RFC3339, -DUR, or 'now')")
		toArg    = fs.String("to", "now", "End of the window (RFC3339, -DUR, or 'now')")
		step     = fs.Duration("step", 15*time.Second, "Sampling step (e.g. 15s, 1m)")
		metric   = fs.String("metric", "", "Regex matching metric names (empty = all)")
		out      = fs.String("out", "", "Output .txt.gz path (required)")
		verbose  = fs.Bool("verbose", false, "Verbose logging")
	)
	_ = fs.Parse(args)

	if *src == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "error: -src and -out are required")
		fs.Usage()
		return 2
	}
	from, err := parseTimeFlag(*fromArg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: -from:", err)
		return 2
	}
	to, err := parseTimeFlag(*toArg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: -to:", err)
		return 2
	}

	log := cliLogger(*verbose)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	res, err := promclip.Export(ctx, log, promclip.ExportRequest{
		Source: promclip.Connection{
			URL:      *src,
			Username: *user,
			Password: *pass,
			Insecure: *insecure,
		},
		From:        from,
		To:          to,
		Step:        *step,
		MetricRegex: *metric,
		OutputPath:  *out,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "export failed:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "export ok: %d series, %d samples, %d bytes -> %s\n",
		res.Series, res.Samples, res.Bytes, *out)
	return 0
}

// runImportCLI replays a previously-exported .txt.gz into a target
// Prometheus via remote-write. Same constraints as the UI mode: the
// target needs --web.enable-remote-write-receiver AND an OOO window.
func runImportCLI(args []string) int {
	fs := flag.NewFlagSet("prom-clip import", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: prom-clip import [options] -dst URL -in FILE")
		fmt.Fprintln(fs.Output(), "")
		fmt.Fprintln(fs.Output(), "Replay an OpenMetrics export into a target Prometheus via remote-write.")
		fmt.Fprintln(fs.Output(), "The target Prometheus needs --web.enable-remote-write-receiver AND")
		fmt.Fprintln(fs.Output(), "storage.tsdb.out_of_order_time_window set wide enough to cover the export.")
		fmt.Fprintln(fs.Output(), "")
		fmt.Fprintln(fs.Output(), "Options:")
		fs.PrintDefaults()
	}

	var (
		dst      = fs.String("dst", "", "Target Prometheus URL (required)")
		user     = fs.String("dst-user", "", "Target basic-auth username (optional)")
		pass     = fs.String("dst-pass", "", "Target basic-auth password (optional)")
		insecure = fs.Bool("dst-insecure", false, "Skip TLS verification on the target")
		in       = fs.String("in", "", "Input .txt.gz path (required)")
		batch    = fs.Int("batch", 500, "Series per remote-write batch")
		group    = fs.String("group", "", "Override (or add) the 'group' label on every imported series")
		verbose  = fs.Bool("verbose", false, "Verbose logging")
	)
	_ = fs.Parse(args)

	if *dst == "" || *in == "" {
		fmt.Fprintln(os.Stderr, "error: -dst and -in are required")
		fs.Usage()
		return 2
	}

	log := cliLogger(*verbose)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	res, err := promclip.Import(ctx, log, promclip.ImportRequest{
		Target: promclip.Connection{
			URL:      *dst,
			Username: *user,
			Password: *pass,
			Insecure: *insecure,
		},
		InputPath:     *in,
		BatchSeries:   *batch,
		OverrideGroup: *group,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "import failed:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "import ok: %d series, %d samples, %d batches\n",
		res.Series, res.Samples, res.Batches)
	return 0
}

// parseTimeFlag accepts RFC3339, a Go duration (interpreted as now+d,
// so "-1h" means "1 hour ago"), or the literal "now".
func parseTimeFlag(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "now" {
		return time.Now(), nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("expected RFC3339 (2026-05-21T09:00:00Z), duration (-1h), or 'now', got %q", s)
}

// cliLogger returns a stderr text logger. Default level is INFO so the
// user sees per-step progress (matched metrics, batch counters). -verbose
// drops to DEBUG; INFO/WARN/ERROR remain in both modes.
func cliLogger(verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}
