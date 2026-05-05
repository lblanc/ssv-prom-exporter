// Package main is the entrypoint for ssv-prom-exporter.
//
// For v0 the binary only supports a -ping mode that probes the SSV mgmt
// server's REST API by fetching /serverGroups. The Prometheus collector
// surface comes in subsequent commits.
package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var version = "dev"

func main() {
	var (
		baseURL    = flag.String("url", os.Getenv("SSV_URL"), "SSV REST base URL, e.g. https://10.0.0.1")
		user       = flag.String("user", os.Getenv("SSV_USER"), "SSV username")
		pass       = flag.String("pass", os.Getenv("SSV_PASS"), "SSV password")
		serverHost = flag.String("host", os.Getenv("SSV_HOST"), "Value of the ServerHost header (defaults to host of -url)")
		insecure   = flag.Bool("insecure", true, "Skip TLS verification (SSV mgmt servers typically use self-signed certs)")
		ping       = flag.Bool("ping", false, "Probe /serverGroups and print the response")
		showVer    = flag.Bool("version", false, "Print version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println(version)
		return
	}

	if !*ping {
		fmt.Fprintln(os.Stderr, "no action selected (try -ping or -version)")
		os.Exit(2)
	}

	if *baseURL == "" || *user == "" || *pass == "" {
		fmt.Fprintln(os.Stderr, "missing required arguments (-url/-user/-pass or SSV_URL/SSV_USER/SSV_PASS)")
		os.Exit(2)
	}

	host := *serverHost
	if host == "" {
		host = strings.TrimPrefix(strings.TrimPrefix(*baseURL, "https://"), "http://")
		host = strings.SplitN(host, "/", 2)[0]
	}

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: *insecure},
		},
	}

	url := strings.TrimRight(*baseURL, "/") + "/RestService/rest.svc/1.0/serverGroups"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		fail(err)
	}
	req.SetBasicAuth(*user, *pass)
	req.Header.Set("ServerHost", host)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		fail(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fail(err)
	}

	if resp.StatusCode >= 400 {
		fmt.Fprintf(os.Stderr, "HTTP %d: %s\n", resp.StatusCode, body)
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

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
