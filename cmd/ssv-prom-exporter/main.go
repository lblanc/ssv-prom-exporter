// Package main is the entrypoint for ssv-prom-exporter.
//
// Two modes:
//   -ping       one-shot probe of /serverGroups (prints JSON, exits)
//   -listen :N  starts the Prometheus HTTP exporter on :N
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/lblanc/ssv-prom-exporter/internal/collectors"
	"github.com/lblanc/ssv-prom-exporter/internal/ssv"
)

var version = "dev"

func main() {
	var (
		baseURL    = flag.String("url", os.Getenv("SSV_URL"), "SSV REST base URL, e.g. https://10.0.0.1")
		user       = flag.String("user", os.Getenv("SSV_USER"), "SSV username")
		pass       = flag.String("pass", os.Getenv("SSV_PASS"), "SSV password")
		serverHost = flag.String("host", os.Getenv("SSV_HOST"), "Value of the ServerHost header (defaults to host of -url)")
		insecure   = flag.Bool("insecure", true, "Skip TLS verification (SSV mgmt servers typically use self-signed certs)")
		bases       = flag.String("bases", os.Getenv("SSV_BASES"), "Comma-separated list of backup IPs to fall through to if the primary -url fails. Discovered IPs from /servers replace this list on every successful inventory scrape.")
		backupCIDRs = flag.String("backup-cidrs", os.Getenv("SSV_BACKUP_CIDRS"), "Comma-separated CIDRs to filter discovered backup IPs (e.g. 10.0.0.0/24). Defaults to the primary's /24 if -url is an IPv4. Pass 0.0.0.0/0 to disable filtering.")
		ping       = flag.Bool("ping", false, "Probe /serverGroups and print the response, then exit")
		listen     = flag.String("listen", "", "Run as Prometheus exporter, listen on this address (e.g. :9876)")
		showVer    = flag.Bool("version", false, "Print version and exit")
	)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if *showVer {
		fmt.Println(version)
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
		fmt.Fprintln(os.Stderr, "no action selected (try -ping, -listen, or -version)")
		os.Exit(2)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewScrape(log, 30*time.Second,
		collectors.NewInventory(client, log),
		collectors.NewHealth(client, log),
	))

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
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Info("starting exporter", "addr", *listen, "target", *baseURL)
	if err := srv.ListenAndServe(); err != nil {
		log.Error("listen failed", "err", err)
		os.Exit(1)
	}
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
