// Package ssv is a small HTTP client for DataCore SANsymphony's REST API.
//
// SSV's API enforces two non-obvious conventions:
//   - HTTP Basic auth using a Windows account on the management server.
//   - A mandatory "ServerHost" header on every request, naming the
//     management server. Missing it returns HTTP 400 ErrorCode 9.
//
// This package handles both, plus SSV's .NET-style date format.
package ssv

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
}

// Client wraps http.Client with the SSV REST conventions baked in.
type Client struct {
	cfg  Config
	http *http.Client
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
		host := strings.TrimPrefix(strings.TrimPrefix(cfg.BaseURL, "https://"), "http://")
		cfg.ServerHost = strings.SplitN(host, "/", 2)[0]
	}
	return &Client{
		cfg: cfg,
		http: &http.Client{
			Timeout: cfg.Timeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.Insecure},
			},
		},
	}, nil
}

// GetRaw fetches the given resource path (relative to the API root) and
// returns the raw JSON response body.
func (c *Client) GetRaw(ctx context.Context, path string) ([]byte, error) {
	u := strings.TrimRight(c.cfg.BaseURL, "/") + apiPathV1 + "/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	req.Header.Set("ServerHost", c.cfg.ServerHost)
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
		return nil, fmt.Errorf("ssv: GET %s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
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
