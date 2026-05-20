// Package main is the entrypoint for prom-clip — a small web tool that
// exports a time-window of Prometheus data to OpenMetrics (.txt.gz)
// and replays it into a target Prometheus instance via remote-write.
//
// Typical usage:
//
//	prom-clip -listen :8088
//	# then open http://localhost:8088
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lblanc/ssv-prom-exporter/internal/promclip"
)

var version = "dev"

func main() {
	var (
		listen    = flag.String("listen", ":8088", "HTTP listen address")
		stateDir  = flag.String("state-dir", promclip.DefaultStateDir(), "Directory for state.json and exported files")
		outputDir = flag.String("output-dir", "", "Where exported .txt.gz files are written (default: <state-dir>/exports)")
		showVer   = flag.Bool("version", false, "Print version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println(version)
		return
	}

	if err := os.MkdirAll(*stateDir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "state dir:", err)
		os.Exit(1)
	}
	outDir := *outputDir
	if outDir == "" {
		outDir = filepath.Join(*stateDir, "exports")
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "output dir:", err)
		os.Exit(1)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	log = log.With("svc", "prom-clip")

	st, err := promclip.LoadState(filepath.Join(*stateDir, "state.json"))
	if err != nil {
		log.Error("load state", "err", err)
		os.Exit(1)
	}

	srv, err := promclip.NewServer(st, log, outDir)
	if err != nil {
		log.Error("server init", "err", err)
		os.Exit(1)
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
	log.Info("prom-clip listening", "addr", *listen, "state_dir", *stateDir, "output_dir", outDir, "version", version)

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
