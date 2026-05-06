// Package ssv is a small HTTP client for DataCore SANsymphony's REST API.
//
// SSV's API enforces three non-obvious conventions:
//   - Authentication is session-based: POST /sessions with a non-standard
//     "Basic <user> <pass>" (literal, NOT base64) returns a token; every
//     subsequent request carries "Token <token>" (NOT "Bearer").
//   - A mandatory "ServerHost" header on every request, naming the
//     management server. Missing it returns HTTP 400 ErrorCode 9.
//   - Errors are returned as a JSON fault body with ErrorCode +
//     ErrorMessage fields; the structured form lands in HTTPError.
//
// The client also supports endpoint failover: it carries a list of
// {baseURL, serverHost} pairs (the user-provided primary plus any
// backups discovered from /servers) and falls through them in order
// on transient errors (network failures, 5xx responses). When every
// endpoint fails transiently, the call is retried with exponential
// backoff (capped) honoring ctx.Done(). Each endpoint owns its own
// session token, since /sessions is scoped to (REST host, ServerHost).
package ssv

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	apiPathV1  = "/RestService/rest.svc/1.0"
	clientName = "ssv-prom-exporter"
)

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

	// Retries is the number of additional attempts after the initial one
	// when every configured endpoint fails transiently. Total attempts =
	// Retries + 1. Defaults to 2.
	Retries int

	// RetryBaseDelay is the first backoff sleep between retries; each
	// subsequent retry doubles it (capped at RetryMaxDelay) with up to
	// 50% added jitter. Defaults to 200ms.
	RetryBaseDelay time.Duration

	// RetryMaxDelay caps the per-sleep backoff. Defaults to 2s.
	RetryMaxDelay time.Duration
}

// HTTPError is returned by the client when SSV responds with a 4xx/5xx
// status. Carrying the code lets callers (and the failover logic)
// distinguish "configuration" errors (4xx) from "transient" ones (5xx).
//
// When the response body is a JSON fault (ErrorCode + ErrorMessage),
// the parsed values are exposed via Code and Message; Body always
// holds the raw response for diagnostics.
type HTTPError struct {
	StatusCode int
	Path       string // includes verb for non-GET, e.g. "POST sessions"
	Body       string

	Code    int    // ErrorCode from the JSON fault, 0 if absent
	Message string // ErrorMessage from the JSON fault, "" if absent
}

func (e *HTTPError) Error() string {
	detail := e.Body
	switch {
	case e.Message != "" && e.Code != 0:
		detail = fmt.Sprintf("ErrorCode %d: %s", e.Code, e.Message)
	case e.Message != "":
		detail = e.Message
	}
	return fmt.Sprintf("ssv: %s: HTTP %d: %s", e.Path, e.StatusCode, detail)
}

// newHTTPError builds an HTTPError, parsing the body for the JSON fault
// shape SSV uses ({"ErrorCode": ..., "ErrorMessage": "..."}). Failure
// to decode is silently ignored — the raw body always remains
// available.
func newHTTPError(path string, statusCode int, body []byte) *HTTPError {
	e := &HTTPError{
		StatusCode: statusCode,
		Path:       path,
		Body:       strings.TrimSpace(string(body)),
	}
	var f struct {
		ErrorCode    int    `json:"ErrorCode"`
		ErrorMessage string `json:"ErrorMessage"`
	}
	if err := json.Unmarshal(body, &f); err == nil {
		e.Code = f.ErrorCode
		e.Message = f.ErrorMessage
	}
	return e
}

// endpoint is one (baseURL, ServerHost-header, session-token) triple.
// Each endpoint owns its own token because /sessions is scoped to the
// (REST host, ServerHost) pair: the IIS bridge does not share state
// across server-group hosts.
type endpoint struct {
	baseURL    string
	serverHost string

	mu    sync.Mutex // guards token
	token string
}

func (ep *endpoint) key() string { return ep.baseURL + "|" + ep.serverHost }

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
	endpoints    []*endpoint // primary first, backups after
	preferredIdx int         // index of the last-known-good endpoint
	preferredTs  time.Time   // when preferredIdx was last set
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
	if cfg.Retries < 0 {
		cfg.Retries = 0
	}
	if cfg.RetryBaseDelay <= 0 {
		cfg.RetryBaseDelay = 200 * time.Millisecond
	}
	if cfg.RetryMaxDelay <= 0 {
		cfg.RetryMaxDelay = 2 * time.Second
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
		endpoints: []*endpoint{{baseURL: cfg.BaseURL, serverHost: cfg.ServerHost}},
	}, nil
}

// SetBackups replaces the backup endpoint list. The primary (from cfg)
// remains first; usable IPs from ips are appended as backups. IPs that
// match the primary host (or each other), or that aren't usable IPv4
// addresses, are skipped. Endpoints whose (baseURL, serverHost) pair
// already existed keep their session token, so SetBackups does not
// trigger a re-auth on every inventory refresh.
func (c *Client) SetBackups(ips []string) {
	primaryHost := hostOf(c.cfg.BaseURL)

	c.mu.Lock()
	defer c.mu.Unlock()

	// Index existing endpoints by (baseURL, serverHost) so we can carry
	// session tokens across the rebuild. New entries get fresh ones.
	existing := make(map[string]*endpoint, len(c.endpoints))
	for _, ep := range c.endpoints {
		existing[ep.key()] = ep
	}
	pick := func(baseURL, serverHost string) *endpoint {
		key := baseURL + "|" + serverHost
		if ep, ok := existing[key]; ok {
			return ep
		}
		return &endpoint{baseURL: baseURL, serverHost: serverHost}
	}

	eps := []*endpoint{pick(c.cfg.BaseURL, c.cfg.ServerHost)}
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
		eps = append(eps, pick(bu, ip))
	}

	changed := !endpointsEqual(c.endpoints, eps)
	c.endpoints = eps
	if c.preferredIdx >= len(eps) {
		c.preferredIdx = 0
	}
	if changed {
		urls := make([]string, len(eps))
		for i, e := range eps {
			urls[i] = e.baseURL
		}
		c.log.Info("ssv: endpoint list updated", "count", len(eps), "endpoints", urls)
	}
}

func endpointsEqual(a, b []*endpoint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].baseURL != b[i].baseURL || a[i].serverHost != b[i].serverHost {
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

// Close terminates every active session. Best-effort: errors per
// endpoint are logged at debug and do not abort the loop, since the
// process is shutting down. Safe to call multiple times.
func (c *Client) Close(ctx context.Context) {
	c.mu.RLock()
	eps := make([]*endpoint, len(c.endpoints))
	copy(eps, c.endpoints)
	c.mu.RUnlock()
	for _, ep := range eps {
		if err := c.closeSession(ctx, ep); err != nil {
			c.log.Debug("ssv: close session", "endpoint", ep.baseURL, "err", err)
		}
	}
}

// GetRaw fetches the given resource path (relative to the API root) and
// returns the raw JSON response body. On a transient failure (network
// error or HTTP 5xx) it falls through to the next endpoint, in an order
// that starts from the last-known-good endpoint until preferredTTL
// elapses, then from primary again.
//
// If every endpoint fails transiently in one pass, the call is retried
// up to cfg.Retries additional times with exponential backoff (capped
// at cfg.RetryMaxDelay) and ±50% jitter, honoring ctx cancellation.
// Non-transient errors (4xx, decode failures) short-circuit the retry
// loop because failing over or retrying would just hide a config bug.
func (c *Client) GetRaw(ctx context.Context, path string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.cfg.Retries; attempt++ {
		body, err := c.tryAllEndpoints(ctx, path)
		if err == nil {
			return body, nil
		}
		if !isTransient(err) {
			return nil, err
		}
		lastErr = err
		if attempt == c.cfg.Retries {
			break
		}
		delay := backoffDelay(c.cfg.RetryBaseDelay, c.cfg.RetryMaxDelay, attempt)
		c.log.Debug("ssv: retry after transient failure",
			"path", path, "attempt", attempt+1, "of", c.cfg.Retries+1, "sleep", delay, "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil, lastErr
}

// tryAllEndpoints walks the endpoint list once, in tryOrder, and
// returns the first successful body. Non-transient errors (4xx, decode)
// short-circuit. If every endpoint fails transiently it returns a
// wrapped "all endpoints failed" error so the retry loop can decide
// whether to back off.
func (c *Client) tryAllEndpoints(ctx context.Context, path string) ([]byte, error) {
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

// backoffDelay returns base * 2^attempt, capped at maxDelay, with up to
// 50% added jitter to avoid thundering-herd retries when several scrapes
// hit a flapping mgmt server simultaneously.
func backoffDelay(base, maxDelay time.Duration, attempt int) time.Duration {
	d := base << attempt
	if d <= 0 || d > maxDelay { // overflow or cap
		d = maxDelay
	}
	jitter := time.Duration(rand.Int63n(int64(d/2 + 1)))
	return d + jitter
}

// tryOrder returns a snapshot of the endpoint list and the index order
// in which to try them. The order starts from the last-known-good
// endpoint (within preferredTTL) and wraps around; outside the TTL it
// starts from the primary so recovery is detected.
func (c *Client) tryOrder() ([]*endpoint, []int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	eps := make([]*endpoint, len(c.endpoints))
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

// getOne issues one GET against ep. It transparently re-authenticates
// once on HTTP 401 (treated as "token expired"); a second 401 is
// returned to the caller as a non-transient error.
func (c *Client) getOne(ctx context.Context, ep *endpoint, path string) ([]byte, error) {
	body, err := c.doGet(ctx, ep, path)
	if !isUnauthorized(err) {
		return body, err
	}
	c.invalidateToken(ep)
	return c.doGet(ctx, ep, path)
}

// doGet performs a single authenticated GET, opening a session if the
// endpoint does not yet have a token.
func (c *Client) doGet(ctx context.Context, ep *endpoint, path string) ([]byte, error) {
	tok, err := c.ensureToken(ctx, ep)
	if err != nil {
		return nil, err
	}
	u := strings.TrimRight(ep.baseURL, "/") + apiPathV1 + "/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	// Non-standard: literal "Token <token>", NOT "Bearer".
	req.Header.Set("Authorization", "Token "+tok)
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
		return nil, newHTTPError(path, resp.StatusCode, body)
	}
	return body, nil
}

// ensureToken returns the endpoint's current token, opening a session
// first if needed. Per-endpoint mutex serialises concurrent goroutines
// so we never fire two parallel OpenSession calls for the same target.
func (c *Client) ensureToken(ctx context.Context, ep *endpoint) (string, error) {
	ep.mu.Lock()
	defer ep.mu.Unlock()
	if ep.token != "" {
		return ep.token, nil
	}
	tok, err := c.openSession(ctx, ep)
	if err != nil {
		return "", err
	}
	ep.token = tok
	return tok, nil
}

// invalidateToken drops the cached token. Next call on this endpoint
// will reopen a session.
func (c *Client) invalidateToken(ep *endpoint) {
	ep.mu.Lock()
	ep.token = ""
	ep.mu.Unlock()
}

// openSession exchanges credentials for a session token. The
// "Authorization: Basic <user> <pass>" header is intentionally NOT
// base64-encoded — the SSV REST app expects the literal three-token
// form. Returns the token on success.
func (c *Client) openSession(ctx context.Context, ep *endpoint) (string, error) {
	const path = "POST sessions"
	u := strings.TrimRight(ep.baseURL, "/") + apiPathV1 + "/sessions"
	body, _ := json.Marshal(map[string]string{
		"Operation": "OpenSession",
		"Client":    clientName,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("ServerHost", ep.serverHost)
	req.Header.Set("Authorization", "Basic "+c.cfg.Username+" "+c.cfg.Password)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("ssv: open session: %w", err)
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("ssv: open session: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", newHTTPError(path, resp.StatusCode, rb)
	}
	var out struct {
		Token string `json:"Token"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return "", fmt.Errorf("ssv: decode session: %w", err)
	}
	if out.Token == "" {
		return "", fmt.Errorf("ssv: open session: empty token in response")
	}
	return out.Token, nil
}

// closeSession terminates the endpoint's session, if any. The token is
// cleared up-front so concurrent callers don't try to reuse a token
// we're about to invalidate server-side.
func (c *Client) closeSession(ctx context.Context, ep *endpoint) error {
	ep.mu.Lock()
	tok := ep.token
	ep.token = ""
	ep.mu.Unlock()
	if tok == "" {
		return nil
	}

	u := strings.TrimRight(ep.baseURL, "/") + apiPathV1 + "/sessions"
	body, _ := json.Marshal(map[string]string{
		"Operation": "CloseSession",
		"Token":     tok,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("ServerHost", ep.serverHost)
	req.Header.Set("Authorization", "Token "+tok)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
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

func (c *Client) Hosts(ctx context.Context) ([]Host, error) {
	var v []Host
	return v, c.Get(ctx, "hosts", &v)
}

func (c *Client) Ports(ctx context.Context) ([]Port, error) {
	var v []Port
	return v, c.Get(ctx, "ports", &v)
}

func (c *Client) PhysicalDisks(ctx context.Context) ([]PhysicalDisk, error) {
	var v []PhysicalDisk
	return v, c.Get(ctx, "physicalDisks", &v)
}

func (c *Client) PoolMembers(ctx context.Context) ([]PoolMember, error) {
	var v []PoolMember
	return v, c.Get(ctx, "poolMembers", &v)
}

func (c *Client) Monitors(ctx context.Context) ([]Monitor, error) {
	var v []Monitor
	return v, c.Get(ctx, "monitors", &v)
}

// Alerts returns the typed list of active alerts from /alerts.
func (c *Client) Alerts(ctx context.Context) ([]Alert, error) {
	var v []Alert
	return v, c.Get(ctx, "alerts", &v)
}

// CounterMap is a flat name → integer mapping for SSV's
// /performance/{id} responses. Each object type (server, pool,
// virtualDisk, etc.) exposes a different set of counters; callers
// look up the keys they care about and skip the rest.
type CounterMap map[string]int64

// Performance fetches the performance counter snapshot for a single
// instance. The SSV REST endpoint always returns an array of exactly
// one snapshot; this function unwraps it.
//
// Counters listed in NullCounterMap (vendor bitmap of unavailable
// counters) are skipped, so callers don't see them as zero. Non-numeric
// fields (CollectionTime, NullCounterMap itself) are dropped.
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
	raw := arr[0]
	nulls := decodeNullCounterMap(raw["NullCounterMap"])

	out := make(CounterMap, len(raw))
	for k, v := range raw {
		if k == "NullCounterMap" || k == "CollectionTime" {
			continue
		}
		if nulls[k] {
			continue
		}
		var n int64
		if err := json.Unmarshal(v, &n); err == nil {
			out[k] = n
		}
	}
	return out, nil
}

// decodeNullCounterMap reads SSV's NullCounterMap field, which marks
// counters as unavailable so the client can skip them rather than
// report them as zero. Two shapes have been observed across PSP
// versions: a JSON object {name: bool} and a JSON array of names.
// Anything we can't decode is treated as "no nulls signalled" — the
// caller still skips counters that don't decode as int64, so we never
// emit garbage.
func decodeNullCounterMap(raw json.RawMessage) map[string]bool {
	if len(raw) == 0 {
		return nil
	}
	var asMap map[string]bool
	if err := json.Unmarshal(raw, &asMap); err == nil {
		return asMap
	}
	var asArr []string
	if err := json.Unmarshal(raw, &asArr); err == nil {
		out := make(map[string]bool, len(asArr))
		for _, k := range asArr {
			out[k] = true
		}
		return out
	}
	return nil
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

// isUnauthorized reports whether err is an HTTPError carrying a 401.
// Used by getOne to trigger a single re-auth before propagating.
func isUnauthorized(err error) bool {
	var herr *HTTPError
	if errors.As(err, &herr) {
		return herr.StatusCode == http.StatusUnauthorized
	}
	return false
}
