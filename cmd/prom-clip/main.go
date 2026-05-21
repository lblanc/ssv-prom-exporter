// Package main is the entrypoint for prom-clip — a small tool that
// exports a time-window of Prometheus data to OpenMetrics (.txt.gz)
// and replays it into a target Prometheus via remote-write.
//
// Two modes:
//
//	prom-clip                          # web UI on http://127.0.0.1:8088
//	prom-clip export -src ... -out ... # one-shot CLI, no server, no state
//	prom-clip import -dst ... -in  ... # one-shot CLI, no server, no state
//
// Pass -h to either subcommand for its flag list. UI mode listens on
// loopback by default (no Windows Firewall prompt); pass
// -listen 0.0.0.0:8088 to expose, or -open=false to suppress the
// browser auto-open.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/lblanc/ssv-prom-exporter/internal/promclip"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "export":
			os.Exit(runExportCLI(os.Args[2:]))
		case "import":
			os.Exit(runImportCLI(os.Args[2:]))
		}
	}
	var (
		listen      = flag.String("listen", "127.0.0.1:8088", "HTTP listen address (loopback by default; pass 0.0.0.0:8088 to expose)")
		stateDir    = flag.String("state-dir", "", "Persist state.json and accumulate exports here. Default empty = ephemeral: state in RAM, exports written to the OS temp dir and removed after the browser downloads them.")
		outputDir   = flag.String("output-dir", "", "Override where export .txt.gz files are written (default: <state-dir>/exports in persistent mode, or the OS temp dir in ephemeral mode)")
		keepExports = flag.Int("keep-exports", 20, "Persistent mode only — keep this many newest *.txt.gz files (0 disables pruning). Ignored in ephemeral mode.")
		openUI      = flag.Bool("open", true, "Open the UI in the default browser on startup")
		showVer     = flag.Bool("version", false, "Print version and exit")
	)
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "Usage:")
		fmt.Fprintln(flag.CommandLine.Output(), "  prom-clip                          # web UI on http://127.0.0.1:8088")
		fmt.Fprintln(flag.CommandLine.Output(), "  prom-clip export -src ... -out ... # one-shot CLI export")
		fmt.Fprintln(flag.CommandLine.Output(), "  prom-clip import -dst ... -in  ... # one-shot CLI import")
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "Web UI mode flags:")
		flag.PrintDefaults()
		fmt.Fprintln(flag.CommandLine.Output(), "")
		fmt.Fprintln(flag.CommandLine.Output(), "Run `prom-clip export -h` or `prom-clip import -h` for subcommand options.")
	}
	flag.Parse()

	if *showVer {
		fmt.Println(version)
		return
	}

	ephemeral := *stateDir == ""

	// In ephemeral mode we don't touch the filesystem for state, and
	// exports go to the OS temp dir (deleted post-download). In
	// persistent mode we create the directory tree once, up front.
	var (
		outDir    string
		statePath string
	)
	if ephemeral {
		outDir = os.TempDir()
		if *outputDir != "" {
			// Explicit override still honored, but we don't create it
			// silently — fail fast if the path doesn't exist.
			outDir = *outputDir
			if _, err := os.Stat(outDir); err != nil {
				fmt.Fprintln(os.Stderr, "output dir:", err)
				os.Exit(1)
			}
		}
	} else {
		if err := os.MkdirAll(*stateDir, 0o700); err != nil {
			fmt.Fprintln(os.Stderr, "state dir:", err)
			os.Exit(1)
		}
		outDir = *outputDir
		if outDir == "" {
			outDir = filepath.Join(*stateDir, "exports")
		}
		if err := os.MkdirAll(outDir, 0o700); err != nil {
			fmt.Fprintln(os.Stderr, "output dir:", err)
			os.Exit(1)
		}
		statePath = filepath.Join(*stateDir, "state.json")
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	log = log.With("svc", "prom-clip")

	st, err := promclip.LoadState(statePath)
	if err != nil {
		log.Error("load state", "err", err)
		os.Exit(1)
	}

	srv, err := promclip.NewServer(st, log, outDir)
	if err != nil {
		log.Error("server init", "err", err)
		os.Exit(1)
	}
	srv.Ephemeral = ephemeral
	if !ephemeral {
		srv.KeepExports = *keepExports
		if n, err := promclip.PruneExports(outDir, *keepExports); err != nil {
			log.Warn("initial prune exports", "err", err, "removed", n)
		} else if n > 0 {
			log.Info("pruned old exports at startup", "removed", n, "keep", *keepExports)
		}
	}

	mux := http.NewServeMux()
	srv.Routes(mux)

	httpSrv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	printStartupBanner(version, browserURL(*listen), *stateDir, outDir, ephemeral)

	if *openUI {
		url := browserURL(*listen)
		go func() {
			if err := waitListening(ctx, *listen, 3*time.Second); err != nil {
				log.Warn("browser open skipped: server not ready", "err", err)
				return
			}
			if err := openBrowser(url); err != nil {
				log.Warn("browser open failed", "url", url, "err", err)
			}
		}()
	}

	select {
	case <-ctx.Done():
		log.Info("shutdown requested")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Error("http shutdown", "err", err)
			os.Exit(1)
		}
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			log.Error("http server", "err", err)
			os.Exit(1)
		}
	}
}

// printStartupBanner emits a multi-line banner to stderr so the user
// can see at a glance where the UI is reachable and where (if anywhere)
// data will be persisted. Goes through fmt rather than slog because a
// structured log line would scatter these facts across key=value pairs
// that get lost in a noisy terminal.
func printStartupBanner(version, url, stateDir, outDir string, ephemeral bool) {
	bar := strings.Repeat("=", 64)
	fmt.Fprintln(os.Stderr, bar)
	fmt.Fprintf(os.Stderr, " prom-clip %s ready\n", version)
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "   web UI    : %s\n", url)
	if ephemeral {
		fmt.Fprintln(os.Stderr, "   mode      : ephemeral (no state on disk)")
		fmt.Fprintf(os.Stderr, "   exports   : streamed to the browser, then removed from %s\n", outDir)
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, " Pass -state-dir <path> to persist state and keep exports across runs.")
	} else {
		fmt.Fprintf(os.Stderr, "   state dir : %s\n", stateDir)
		fmt.Fprintf(os.Stderr, "   exports   : %s\n", outDir)
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, " Delete the state dir to wipe history.")
	}
	fmt.Fprintln(os.Stderr, " Press Ctrl-C to stop.")
	fmt.Fprintln(os.Stderr, bar)
}

func browserURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://localhost" + listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func waitListening(ctx context.Context, listen string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	target := listen
	if h, p, err := net.SplitHostPort(listen); err == nil {
		if h == "" || h == "0.0.0.0" || h == "::" {
			h = "127.0.0.1"
		}
		target = net.JoinHostPort(h, p)
	}
	for time.Now().Before(deadline) {
		dialer := net.Dialer{Timeout: 200 * time.Millisecond}
		conn, err := dialer.DialContext(ctx, "tcp", target)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return fmt.Errorf("timeout waiting for %s", target)
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
