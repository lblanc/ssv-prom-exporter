// Package ssv is a small HTTP client for DataCore SANsymphony's REST API.
//
// SSV's API enforces two non-obvious conventions:
//   - HTTP Basic auth using a Windows account on the management server.
//   - A mandatory "ServerHost" header on every request, naming the
//     management server. Missing it returns HTTP 400 ErrorCode 9.
//
// The client also supports endpoint failover: it carries a list of
// {baseURL, serverHost} pairs (the user-provided primary plus any
// backups discovered from /servers) and falls through them in order
// on transient errors (network failures, 5xx responses).
package ssv

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const apiPathV1 = "/RestService/rest.svc/1.0"

// Config holds the inputs needed to talk to a SSV management server.
type Config struct {
	BaseURL    string
	Username   string
	Password   string
	ServerHost string        // mandatory ServerHost header value; defaults to host of BaseURL
	Insecure   bool          // skip TLS verification (SSV ships self-signed certs)
	Timeout    time.Duration // per-request timeout; defaults to 15s
	Logger     *slog.Logger  // optional; defaults to slog.Default()

	// BackupCIDRs restricts which discovered IPs are accepted as failover
	// backups. SSV's /servers reports every IP bound on a node (mgmt,
	// iSCSI, mirror, IPv6 link-local) and only the management network
	// usually accepts REST. If empty AND the primary URL is an IPv4, this
	// defaults to that IP's /24 — works for the typical "all mgmt IPs in
	// one VLAN" deployment. To disable filtering, pass {"0.0.0.0/0"}.
	BackupCIDRs []string
}

// HTTPError is returned by the client when SSV responds with a 4xx/5xx
// status. Carrying the code lets callers (and the failover logic)
// distinguish "configuration" errors (4xx) from "transient" ones (5xx).
type HTTPError struct {
	StatusCode int
	Path       string
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("ssv: GET %s: HTTP %d: %s", e.Path, e.StatusCode, e.Body)
}

// endpoint is one (baseURL, ServerHost-header) pair.
type endpoint struct {
	baseURL    string
	serverHost string
}

// preferredTTL is how long the client keeps preferring a backup after a
// failover. Once it expires, the next call starts from the primary again
// to detect recovery. Five minutes balances "don't hammer a flaky primary"
// with "notice when it comes back".
const preferredTTL = 5 * time.Minute

// dialTimeout caps how long we wait for a TCP connect to any single
// endpoint. Short enough that a dead primary fails over quickly, long
// enough to ride out brief network jitter.
const dialTimeout = 3 * time.Second

// Client wraps http.Client with the SSV REST conventions baked in.
type Client struct {
	cfg     Config
	http    *http.Client
	log     *slog.Logger
	allowed []*net.IPNet // CIDRs accepted as failover backups; nil means accept any IPv4

	mu           sync.RWMutex
	endpoints    []endpoint // primary first, backups after
	preferredIdx int        // index of the last-known-good endpoint
	preferredTs  time.Time  // when preferredIdx was last set
}

func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("ssv: BaseURL is required")
	}
	if cfg.Username == "" || cfg.Password == "" {
		return nil, fmt.Errorf("ssv: username and password are required")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.ServerHost == "" {
		cfg.ServerHost = hostOf(cfg.BaseURL)
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	cidrs := cfg.BackupCIDRs
	if len(cidrs) == 0 {
		if ip := net.ParseIP(hostOf(cfg.BaseURL)); ip != nil && ip.To4() != nil {
			cidrs = []string{ip.To4().String() + "/24"}
		}
	}
	allowed, err := parseCIDRs(cidrs)
	if err != nil {
		return nil, err
	}
	return &Client{
		cfg:     cfg,
		log:     log,
		allowed: allowed,
		http: &http.Client{
			Timeout: cfg.Timeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.Insecure},
				DialContext: (&net.Dialer{
					Timeout:   dialTimeout,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				TLSHandshakeTimeout:   dialTimeout,
				ResponseHeaderTimeout: 10 * time.Second,
				MaxIdleConns:          10,
				IdleConnTimeout:       60 * time.Second,
			},
		},
		endpoints: []endpoint{{baseURL: cfg.BaseURL, serverHost: cfg.ServerHost}},
	}, nil
}

// SetBackups replaces the backup endpoint list. The primary (from cfg)
// remains first; usable IPs from ips are appended as backups. IPs that
// match the primary host (or each other), or that aren't usable IPv4
// addresses, are skipped. Logs once at INFO when the resulting list
// differs from the previous one.
func (c *Client) SetBackups(ips []string) {
	primary := endpoint{baseURL: c.cfg.BaseURL, serverHost: c.cfg.ServerHost}
	primaryHost := hostOf(c.cfg.BaseURL)

	eps := []endpoint{primary}
	seen := map[string]bool{primaryHost: true}

	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		if !c.acceptIP(ip) || seen[ip] {
			continue
		}
		seen[ip] = true
		bu, ok := withHost(c.cfg.BaseURL, ip)
		if !ok {
			continue
		}
		eps = append(eps, endpoint{baseURL: bu, serverHost: ip})
	}

	c.mu.Lock()
	changed := !endpointsEqual(c.endpoints, eps)
	c.endpoints = eps
	if c.preferredIdx >= len(eps) {
		c.preferredIdx = 0
	}
	c.mu.Unlock()

	if changed {
		urls := make([]string, len(eps))
		for i, e := range eps {
			urls[i] = e.baseURL
		}
		c.log.Info("ssv: endpoint list updated", "count", len(eps), "endpoints", urls)
	}
}

func endpointsEqual(a, b []endpoint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Endpoints returns a snapshot of the currently-configured endpoints
// (primary first, backups after). Useful for diagnostics.
func (c *Client) Endpoints() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.endpoints))
	for _, ep := range c.endpoints {
		out = append(out, ep.baseURL)
	}
	return out
}

// GetRaw fetches the given resource path (relative to the API root) and
// returns the raw JSON response body. On a transient failure (network
// error or HTTP 5xx) it falls through to the next endpoint, in an order
// that starts from the last-known-good endpoint until preferredTTL
// elapses, then from primary again.
func (c *Client) GetRaw(ctx context.Context, path string) ([]byte, error) {
	eps, order := c.tryOrder()

	var lastErr error
	for _, idx := range order {
		ep := eps[idx]
		body, err := c.getOne(ctx, ep, path)
		if err == nil {
			c.markPreferred(idx)
			return body, nil
		}
		if !isTransient(err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("ssv: all endpoints failed: %w", lastErr)
}

// tryOrder returns a snapshot of the endpoint list and the index order
// in which to try them. The order starts from the last-known-good
// endpoint (within preferredTTL) and wraps around; outside the TTL it
// starts from the primary so recovery is detected.
func (c *Client) tryOrder() ([]endpoint, []int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	eps := make([]endpoint, len(c.endpoints))
	copy(eps, c.endpoints)

	start := 0
	if c.preferredIdx > 0 && c.preferredIdx < len(eps) && time.Since(c.preferredTs) < preferredTTL {
		start = c.preferredIdx
	}
	order := make([]int, len(eps))
	for i := range eps {
		order[i] = (start + i) % len(eps)
	}
	return eps, order
}

// markPreferred refreshes the last-known-good index. The TTL timestamp is
// always bumped, so a stable backup keeps being preferred indefinitely.
// State transitions (primary <-> backup, backup A -> backup B) are logged
// once at the moment they happen.
func (c *Client) markPreferred(idx int) {
	c.mu.Lock()
	prev := c.preferredIdx
	c.preferredIdx = idx
	c.preferredTs = time.Now()
	transition := prev != idx
	var primaryURL, fromURL, toURL string
	if transition {
		primaryURL = c.endpoints[0].baseURL
		fromURL = c.endpoints[prev].baseURL
		toURL = c.endpoints[idx].baseURL
	}
	c.mu.Unlock()

	if !transition {
		return
	}
	switch {
	case prev == 0 && idx != 0:
		c.log.Warn("ssv failover: primary unreachable, using backup",
			"primary", primaryURL, "backup", toURL)
	case prev != 0 && idx == 0:
		c.log.Info("ssv: primary endpoint reachable again",
			"primary", primaryURL)
	default:
		c.log.Info("ssv: switched between backup endpoints",
			"from", fromURL, "to", toURL)
	}
}

func (c *Client) getOne(ctx context.Context, ep endpoint, path string) ([]byte, error) {
	u := strings.TrimRight(ep.baseURL, "/") + apiPathV1 + "/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	req.Header.Set("ServerHost", ep.serverHost)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ssv: GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ssv: GET %s: %w", path, err)
	}
	if resp.StatusCode >= 400 {
		return nil, &HTTPError{StatusCode: resp.StatusCode, Path: path, Body: strings.TrimSpace(string(body))}
	}
	return body, nil
}

// Get fetches the given resource path and decodes the JSON response into out.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	body, err := c.GetRaw(ctx, path)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("ssv: decode %s: %w", path, err)
	}
	return nil
}

func (c *Client) ServerGroups(ctx context.Context) ([]ServerGroup, error) {
	var v []ServerGroup
	return v, c.Get(ctx, "serverGroups", &v)
}

func (c *Client) Servers(ctx context.Context) ([]Server, error) {
	var v []Server
	return v, c.Get(ctx, "servers", &v)
}

func (c *Client) Pools(ctx context.Context) ([]Pool, error) {
	var v []Pool
	return v, c.Get(ctx, "pools", &v)
}

func (c *Client) VirtualDisks(ctx context.Context) ([]VirtualDisk, error) {
	var v []VirtualDisk
	return v, c.Get(ctx, "virtualDisks", &v)
}

func (c *Client) Monitors(ctx context.Context) ([]Monitor, error) {
	var v []Monitor
	return v, c.Get(ctx, "monitors", &v)
}

// AlertsCount returns the number of items in /alerts. The alert payload
// shape is not yet modelled because the lab consistently returns []; the
// count alone is enough to drive an alert on "alerts > 0".
func (c *Client) AlertsCount(ctx context.Context) (int, error) {
	var v []json.RawMessage
	if err := c.Get(ctx, "alerts", &v); err != nil {
		return 0, err
	}
	return len(v), nil
}

// CounterMap is a flat name → integer mapping for SSV's
// /performance/{id} responses. Each object type (server, pool,
// virtualDisk, etc.) exposes a different set of counters; callers
// look up the keys they care about and skip the rest.
type CounterMap map[string]int64

// Performance fetches the performance counter snapshot for a single
// instance. The SSV REST endpoint always returns an array of exactly
// one snapshot; this function unwraps it. CollectionTime and other
// non-numeric fields are dropped from the returned map.
func (c *Client) Performance(ctx context.Context, id string) (CounterMap, error) {
	body, err := c.GetRaw(ctx, "performance/"+url.PathEscape(id))
	if err != nil {
		return nil, err
	}
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(body, &arr); err != nil {
		return nil, fmt.Errorf("ssv: decode performance/%s: %w", id, err)
	}
	if len(arr) == 0 {
		return CounterMap{}, nil
	}
	out := make(CounterMap, len(arr[0]))
	for k, v := range arr[0] {
		var n int64
		if err := json.Unmarshal(v, &n); err == nil {
			out[k] = n
		}
	}
	return out, nil
}

// hostOf extracts the host (without scheme or path) from a URL string.
func hostOf(u string) string {
	if parsed, err := url.Parse(u); err == nil && parsed.Hostname() != "" {
		return parsed.Hostname()
	}
	s := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	s = strings.SplitN(s, "/", 2)[0]
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	return s
}

// withHost returns u with its hostname replaced by host (port preserved).
// Returns false if u cannot be parsed.
func withHost(u, host string) (string, bool) {
	parsed, err := url.Parse(u)
	if err != nil {
		return "", false
	}
	if port := parsed.Port(); port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	} else {
		parsed.Host = host
	}
	return parsed.String(), true
}

// acceptIP reports whether s is a usable IPv4 address that also falls
// within the configured BackupCIDRs (if any). IPv6 is excluded for v0;
// many SSV deployments only bind REST on IPv4.
func (c *Client) acceptIP(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	if len(c.allowed) == 0 {
		return true
	}
	for _, n := range c.allowed {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func parseCIDRs(in []string) ([]*net.IPNet, error) {
	out := make([]*net.IPNet, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, fmt.Errorf("ssv: invalid CIDR %q: %w", s, err)
		}
		out = append(out, n)
	}
	return out, nil
}

// isTransient classifies an error as a candidate for failover: network
// errors, context deadlines exceeded mid-request, and HTTP 5xx. Auth
// failures, missing-header errors, decode errors and other 4xx are NOT
// transient — falling through to a backup would just hide a config bug.
func isTransient(err error) bool {
	var herr *HTTPError
	if errors.As(err, &herr) {
		return herr.StatusCode >= 500
	}
	// Anything not a 4xx HTTPError is treated as transient: net errors,
	// timeouts, TLS handshake failures, EOF mid-body, etc.
	return true
}
