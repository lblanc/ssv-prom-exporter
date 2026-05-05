// Package main is the entrypoint for ssv-prom-exporter.
//
// The same binary supports four modes, picked from flags + runtime:
//   -ping               one-shot probe of /serverGroups (prints JSON, exits)
//   -install            register as a Windows service (then exit)
//   -uninstall          remove the Windows service (then exit)
//   -listen :N          run the Prometheus HTTP exporter (console or service)
//
// Service vs console is auto-detected via svc.IsService(): the SCM
// launches the binary the same way as the console; we just need to know
// whether to talk to the SCM status channel or to handle SIGINT.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/lblanc/ssv-prom-exporter/internal/collectors"
	"github.com/lblanc/ssv-prom-exporter/internal/ssv"
	"github.com/lblanc/ssv-prom-exporter/internal/svc"
)

var version = "dev"

func main() {
	var (
		baseURL     = flag.String("url", os.Getenv("SSV_URL"), "SSV REST base URL, e.g. https://10.0.0.1")
		user        = flag.String("user", os.Getenv("SSV_USER"), "SSV username")
		pass        = flag.String("pass", os.Getenv("SSV_PASS"), "SSV password")
		serverHost  = flag.String("host", os.Getenv("SSV_HOST"), "Value of the ServerHost header (defaults to host of -url)")
		insecure    = flag.Bool("insecure", true, "Skip TLS verification (SSV mgmt servers typically use self-signed certs)")
		bases       = flag.String("bases", os.Getenv("SSV_BASES"), "Comma-separated list of backup IPs to fall through to if the primary -url fails. Discovered IPs from /servers replace this list on every successful inventory scrape.")
		backupCIDRs = flag.String("backup-cidrs", os.Getenv("SSV_BACKUP_CIDRS"), "Comma-separated CIDRs to filter discovered backup IPs (e.g. 10.0.0.0/24). Defaults to the primary's /24 if -url is an IPv4. Pass 0.0.0.0/0 to disable filtering.")
		ping        = flag.Bool("ping", false, "Probe /serverGroups and print the response, then exit")
		listen      = flag.String("listen", "", "Run as Prometheus exporter, listen on this address (e.g. :9876)")
		perfWorkers = flag.Int("perf-workers", 8, "Number of concurrent /performance/{id} calls during a scrape")
		install     = flag.Bool("install", false, "Install as a Windows service and exit (Windows only)")
		uninstall   = flag.Bool("uninstall", false, "Uninstall the Windows service and exit (Windows only)")
		svcName     = flag.String("svc-name", "ssv-prom-exporter", "Windows service name")
		svcDisplay  = flag.String("svc-display", "DataCore SANsymphony Prometheus Exporter", "Windows service display name")
		svcDesc     = flag.String("svc-description", "Exposes DataCore SANsymphony metrics for Prometheus.", "Windows service description")
		showVer     = flag.Bool("version", false, "Print version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println(version)
		return
	}

	svcCfg := svc.Config{Name: *svcName, DisplayName: *svcDisplay, Description: *svcDesc}

	// Pick a logger up front. Service mode swaps it to the Event Log
	// once we know we were launched by the SCM.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if svc.IsService() {
		if h, err := svc.EventLogHandler(*svcName); err == nil {
			log = slog.New(h)
		}
	}

	if *install {
		if err := installService(svcCfg); err != nil {
			fmt.Fprintln(os.Stderr, "install:", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "service %q installed\n", svcCfg.Name)
		fmt.Fprintln(os.Stderr, "note: command-line args (including -pass) are stored in the SCM ImagePath and readable via `sc qc`. Use env-based config or tighten service ACLs for production.")
		return
	}
	if *uninstall {
		if err := svc.Uninstall(svcCfg.Name); err != nil {
			fmt.Fprintln(os.Stderr, "uninstall:", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "service %q uninstalled\n", svcCfg.Name)
		return
	}

	if *baseURL == "" || *user == "" || *pass == "" {
		fmt.Fprintln(os.Stderr, "missing required arguments (-url/-user/-pass or SSV_URL/SSV_USER/SSV_PASS)")
		os.Exit(2)
	}

	cfg := ssv.Config{
		BaseURL:    *baseURL,
		Username:   *user,
		Password:   *pass,
		ServerHost: *serverHost,
		Insecure:   *insecure,
		Logger:     log,
	}
	if *backupCIDRs != "" {
		cfg.BackupCIDRs = strings.Split(*backupCIDRs, ",")
	}
	client, err := ssv.New(cfg)
	if err != nil {
		log.Error("client init", "err", err)
		os.Exit(1)
	}
	if *bases != "" {
		client.SetBackups(strings.Split(*bases, ","))
		log.Info("static backups configured", "endpoints", client.Endpoints())
	}

	if *ping {
		runPing(client)
		return
	}

	if *listen == "" {
		fmt.Fprintln(os.Stderr, "no action selected (try -ping, -listen, -install, -uninstall, or -version)")
		os.Exit(2)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewScrape(log, 30*time.Second,
		collectors.NewInventory(client, log),
		collectors.NewHealth(client, log),
		collectors.NewPerformance(client, log, *perfWorkers),
	))

	runFn := func(ctx context.Context) error {
		return serveExporter(ctx, log, *listen, reg)
	}

	if svc.IsService() {
		if err := svc.Run(svcCfg, log, runFn); err != nil {
			log.Error("service run failed", "err", err)
			os.Exit(1)
		}
		return
	}

	// Console mode: handle Ctrl-C / SIGTERM gracefully.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runFn(ctx); err != nil {
		log.Error("exporter exited with error", "err", err)
		os.Exit(1)
	}
}

// serveExporter starts the HTTP server for /metrics and shuts it down
// gracefully when ctx is cancelled.
func serveExporter(ctx context.Context, log *slog.Logger, addr string, reg *prometheus.Registry) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, "ssv-prom-exporter %s\n\nGET /metrics for Prometheus exposition.\n", version)
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()

	log.Info("starting exporter", "addr", addr)
	select {
	case <-ctx.Done():
		log.Info("shutdown requested")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("http shutdown: %w", err)
		}
		return nil
	case err := <-serveErr:
		if err == nil || err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

// installService computes the absolute path of the running binary and
// the arg list to bake into the SCM, then delegates to svc.Install.
//
// Args are taken from the flags the user explicitly set (flag.Visit),
// minus the service-management ones — so on every service start the
// binary is invoked with the same -url / -user / -pass / -listen / etc.
// the operator picked at install time.
func installService(cfg svc.Config) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable path: %w", err)
	}

	skip := map[string]bool{"install": true, "uninstall": true, "ping": true, "version": true}
	var args []string
	flag.Visit(func(f *flag.Flag) {
		if skip[f.Name] {
			return
		}
		args = append(args, "-"+f.Name+"="+f.Value.String())
	})
	return svc.Install(cfg, exe, args)
}

func runPing(client *ssv.Client) {
	body, err := client.GetRaw(context.Background(), "serverGroups")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	var pretty any
	if err := json.Unmarshal(body, &pretty); err != nil {
		fmt.Println(string(body))
		return
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(pretty)
}
