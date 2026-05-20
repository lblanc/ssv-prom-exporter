package promclip

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

// PromClient wraps the official Prometheus API client with the small
// surface prom-clip needs: ping, list metrics matching a regex, range
// queries, metric metadata.
type PromClient struct {
	api  v1.API
	conn Connection
}

// NewPromClient builds a client for the given connection. Basic auth is
// applied via a RoundTripper that injects Authorization on each request.
func NewPromClient(c Connection) (*PromClient, error) {
	if c.URL == "" {
		return nil, errors.New("prometheus URL is empty")
	}
	rt := newRoundTripper(c)
	cli, err := promapi.NewClient(promapi.Config{
		Address:      c.URL,
		RoundTripper: rt,
	})
	if err != nil {
		return nil, fmt.Errorf("prom client: %w", err)
	}
	return &PromClient{api: v1.NewAPI(cli), conn: c}, nil
}

// newRoundTripper builds an http.RoundTripper with a sensible base
// transport, optional InsecureSkipVerify, and optional Basic auth.
func newRoundTripper(c Connection) http.RoundTripper {
	base := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
	if c.Insecure {
		base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	if c.Username == "" && c.Password == "" {
		return base
	}
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(c.Username+":"+c.Password))
	return &authedTransport{base: base, auth: auth}
}

type authedTransport struct {
	base http.RoundTripper
	auth string
}

func (t *authedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone so we don't mutate the caller's request.
	r := req.Clone(req.Context())
	if r.Header.Get("Authorization") == "" {
		r.Header.Set("Authorization", t.auth)
	}
	return t.base.RoundTrip(r)
}

// Ping checks reachability by issuing a trivial instant query (`vector(1)`).
// Returns a friendly description of what was reached.
func (p *PromClient) Ping(ctx context.Context) (string, error) {
	if _, _, err := p.api.Query(ctx, "vector(1)", time.Now()); err != nil {
		return "", err
	}
	if bi, err := p.api.Buildinfo(ctx); err == nil {
		return fmt.Sprintf("Prometheus %s (revision %s)", bi.Version, bi.Revision), nil
	}
	return "reachable", nil
}

// ListMetricNames returns the deduplicated set of metric names present
// within [from, to] that match the optional regex. Implemented via
// /api/v1/label/values?label=__name__ with a series-selector match.
//
// matchRegex may be empty (returns all metric names). Matches are
// anchored on both ends by Prometheus convention.
func (p *PromClient) ListMetricNames(ctx context.Context, matchRegex string, from, to time.Time) ([]string, error) {
	var matches []string
	if matchRegex != "" {
		matches = []string{fmt.Sprintf(`{__name__=~%q}`, matchRegex)}
	}
	vals, _, err := p.api.LabelValues(ctx, "__name__", matches, from, to)
	if err != nil {
		return nil, fmt.Errorf("label values: %w", err)
	}
	names := make([]string, 0, len(vals))
	for _, v := range vals {
		names = append(names, string(v))
	}
	sort.Strings(names)
	return names, nil
}

// RangeSeries is one time-series with the samples returned by
// /api/v1/query_range over the requested window.
type RangeSeries struct {
	Labels  model.Metric
	Samples []model.SamplePair
}

// QueryRange runs an instant query expression over [from, to] at the
// given step. Useful expressions: a bare metric name (returns all
// series), or a PromQL selector. The returned slice is empty if no
// series matched.
func (p *PromClient) QueryRange(ctx context.Context, query string, from, to time.Time, step time.Duration) ([]RangeSeries, error) {
	val, _, err := p.api.QueryRange(ctx, query, v1.Range{Start: from, End: to, Step: step})
	if err != nil {
		return nil, fmt.Errorf("query_range %q: %w", query, err)
	}
	matrix, ok := val.(model.Matrix)
	if !ok {
		// Empty selectors may return an empty Vector or Matrix.
		return nil, fmt.Errorf("unexpected response shape: %T", val)
	}
	out := make([]RangeSeries, 0, len(matrix))
	for _, ss := range matrix {
		out = append(out, RangeSeries{
			Labels:  ss.Metric,
			Samples: ss.Values,
		})
	}
	return out, nil
}

// MetricMeta is the trimmed metadata prom-clip cares about. Type is
// kept as a plain string ("counter" / "gauge" / "histogram" / "summary"
// / "info" / "stateset" / "unknown") to match OpenMetrics's type names.
type MetricMeta struct {
	Type string
	Help string
	Unit string
}

// Metadata fetches the metric metadata via /api/v1/metadata. The
// Prometheus API returns multiple entries per metric (one per target);
// we keep the first.
func (p *PromClient) Metadata(ctx context.Context, metric string) (MetricMeta, error) {
	m, err := p.api.Metadata(ctx, metric, "1")
	if err != nil {
		return MetricMeta{}, fmt.Errorf("metadata: %w", err)
	}
	if md, ok := m[metric]; ok && len(md) > 0 {
		return MetricMeta{
			Type: string(md[0].Type),
			Help: md[0].Help,
			Unit: md[0].Unit,
		}, nil
	}
	return MetricMeta{}, nil
}
