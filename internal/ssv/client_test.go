package ssv

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient builds a Client pointing at srv with disabled CIDR
// filtering and short backoff so tests stay fast.
func newTestClient(t *testing.T, srv *httptest.Server, retries int) *Client {
	t.Helper()
	c, err := New(Config{
		BaseURL:        srv.URL,
		Username:       "u",
		Password:       "p",
		BackupCIDRs:    []string{"0.0.0.0/0"},
		Retries:        retries,
		RetryBaseDelay: 1 * time.Millisecond,
		RetryMaxDelay:  5 * time.Millisecond,
		Timeout:        2 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestGetRaw_TransientThenSuccess(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, 3)
	body, err := c.GetRaw(context.Background(), "serverGroups")
	if err != nil {
		t.Fatalf("GetRaw: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("body: %q", body)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
}

func TestGetRaw_ExhaustsRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, 2)
	if _, err := c.GetRaw(context.Background(), "serverGroups"); err == nil {
		t.Fatal("expected error, got nil")
	}
	// Retries=2 → 3 attempts total (1 initial + 2 retries), each pass
	// only has one endpoint configured.
	if got := calls.Load(); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
}

func TestGetRaw_4xxShortCircuits(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, 5)
	_, err := c.GetRaw(context.Background(), "serverGroups")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1 (4xx must not retry)", got)
	}
}

func TestGetRaw_RetryHonorsContext(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	// Long base delay so the second backoff is guaranteed to outlive ctx.
	c, err := New(Config{
		BaseURL:        srv.URL,
		Username:       "u",
		Password:       "p",
		BackupCIDRs:    []string{"0.0.0.0/0"},
		Retries:        5,
		RetryBaseDelay: 200 * time.Millisecond,
		RetryMaxDelay:  1 * time.Second,
		Timeout:        2 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = c.GetRaw(ctx, "serverGroups")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from cancelled ctx, got nil")
	}
	// We allow a generous upper bound to dodge CI flake.
	if elapsed > 500*time.Millisecond {
		t.Fatalf("elapsed = %v, expected ctx cancel to abort early", elapsed)
	}
}

func TestBackoffDelay_CapsAndJitter(t *testing.T) {
	base := 100 * time.Millisecond
	max := 500 * time.Millisecond
	for _, attempt := range []int{0, 1, 2, 3, 10, 62} {
		d := backoffDelay(base, max, attempt)
		if d <= 0 {
			t.Fatalf("attempt=%d: delay=%v must be > 0", attempt, d)
		}
		if d > max+max/2 {
			t.Fatalf("attempt=%d: delay=%v exceeds cap+jitter (%v)", attempt, d, max+max/2)
		}
	}
}
