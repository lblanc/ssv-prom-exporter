package ssv

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// withSessions wraps h so POST /sessions is auto-handled with a fixed
// token. Tests that don't care about auth can drop their handler in
// and stay focused on the resource path.
func withSessions(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == apiPathV1+"/sessions" && r.Method == http.MethodPost {
			var body struct {
				Operation string
				Token     string
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			switch body.Operation {
			case "OpenSession":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"Token":"tok-abc"}`))
			case "CloseSession":
				w.WriteHeader(http.StatusOK)
			default:
				http.Error(w, "bad sessions op", http.StatusBadRequest)
			}
			return
		}
		h(w, r)
	}
}

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
	srv := httptest.NewServer(withSessions(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
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
	srv := httptest.NewServer(withSessions(func(w http.ResponseWriter, r *http.Request) {
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
	srv := httptest.NewServer(withSessions(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "forbidden", http.StatusForbidden)
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
	srv := httptest.NewServer(withSessions(func(w http.ResponseWriter, r *http.Request) {
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

// TestSession_OpenedOnceReusedAcrossCalls verifies the cached token is
// reused: only one OpenSession even though we issue multiple GETs.
func TestSession_OpenedOnceReusedAcrossCalls(t *testing.T) {
	var opens, closes, gets atomic.Int32
	var lastAuth atomic.Value // string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == apiPathV1+"/sessions" && r.Method == http.MethodPost {
			var b struct{ Operation string }
			_ = json.NewDecoder(r.Body).Decode(&b)
			switch b.Operation {
			case "OpenSession":
				opens.Add(1)
				if got := r.Header.Get("Authorization"); got != "Basic u p" {
					t.Errorf("OpenSession auth = %q, want %q (literal Basic, NOT base64)", got, "Basic u p")
				}
				_, _ = w.Write([]byte(`{"Token":"sess-1"}`))
			case "CloseSession":
				closes.Add(1)
				w.WriteHeader(http.StatusOK)
			}
			return
		}
		gets.Add(1)
		lastAuth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, 0)
	for i := 0; i < 5; i++ {
		if _, err := c.GetRaw(context.Background(), "serverGroups"); err != nil {
			t.Fatalf("GetRaw[%d]: %v", i, err)
		}
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("OpenSession count = %d, want 1 (token must be cached)", got)
	}
	if got := gets.Load(); got != 5 {
		t.Fatalf("GET count = %d, want 5", got)
	}
	if a := lastAuth.Load(); a != "Token sess-1" {
		t.Fatalf("GET auth = %q, want %q", a, "Token sess-1")
	}

	c.Close(context.Background())
	if got := closes.Load(); got != 1 {
		t.Fatalf("CloseSession count = %d, want 1 after Client.Close()", got)
	}
}

// TestSession_ReauthsOn401 verifies a 401 triggers a single reauth +
// retry, transparent to the caller.
func TestSession_ReauthsOn401(t *testing.T) {
	var opens atomic.Int32
	var gets atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == apiPathV1+"/sessions" && r.Method == http.MethodPost {
			var b struct{ Operation string }
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b.Operation == "OpenSession" {
				n := opens.Add(1)
				_, _ = w.Write([]byte(`{"Token":"tok-` + itoa(int(n)) + `"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		n := gets.Add(1)
		if n == 1 {
			http.Error(w, `{"ErrorCode":7,"ErrorMessage":"token expired"}`, http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, 0)
	body, err := c.GetRaw(context.Background(), "serverGroups")
	if err != nil {
		t.Fatalf("GetRaw: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("body: %q", body)
	}
	if got := opens.Load(); got != 2 {
		t.Fatalf("OpenSession count = %d, want 2 (initial + reauth)", got)
	}
	if got := gets.Load(); got != 2 {
		t.Fatalf("GET count = %d, want 2 (failed + retried)", got)
	}
}

// TestSession_Persistent401IsPropagated guarantees we don't loop on a
// truly bad credentials situation: two 401s in a row → caller sees the
// error.
func TestSession_Persistent401IsPropagated(t *testing.T) {
	srv := httptest.NewServer(withSessions(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"ErrorCode":3,"ErrorMessage":"invalid credentials"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, 0)
	_, err := c.GetRaw(context.Background(), "serverGroups")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var herr *HTTPError
	if !errors.As(err, &herr) {
		t.Fatalf("err = %v, want HTTPError", err)
	}
	if herr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("StatusCode = %d, want 401", herr.StatusCode)
	}
}

// TestHTTPError_ParsesJsonFault verifies that ErrorCode/ErrorMessage
// from a JSON fault body land on HTTPError.
func TestHTTPError_ParsesJsonFault(t *testing.T) {
	srv := httptest.NewServer(withSessions(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"ErrorCode":9,"ErrorMessage":"No ServerHost header was passed."}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, 0)
	_, err := c.GetRaw(context.Background(), "serverGroups")
	if err == nil {
		t.Fatal("expected error")
	}
	var herr *HTTPError
	if !errors.As(err, &herr) {
		t.Fatalf("err = %v, want HTTPError", err)
	}
	if herr.Code != 9 {
		t.Errorf("Code = %d, want 9", herr.Code)
	}
	if !strings.Contains(herr.Message, "ServerHost") {
		t.Errorf("Message = %q, want it to contain 'ServerHost'", herr.Message)
	}
	if !strings.Contains(herr.Error(), "ErrorCode 9") {
		t.Errorf("Error() = %q, want it to mention ErrorCode 9", herr.Error())
	}
}

// TestNullCounterMap_SkipsListedCounters checks that counters flagged
// in NullCounterMap are dropped, not emitted as zero.
func TestNullCounterMap_SkipsListedCounters(t *testing.T) {
	srv := httptest.NewServer(withSessions(func(w http.ResponseWriter, r *http.Request) {
		// Single perf snapshot with a NullCounterMap object form.
		_, _ = io.WriteString(w, `[{
			"CollectionTime":"/Date(1700000000000)/",
			"NullCounterMap":{"FlagAsNull":true,"NotNull":false},
			"FlagAsNull":0,
			"NotNull":42,
			"Other":7
		}]`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, 0)
	cm, err := c.Performance(context.Background(), "obj-1")
	if err != nil {
		t.Fatalf("Performance: %v", err)
	}
	if _, present := cm["FlagAsNull"]; present {
		t.Errorf("FlagAsNull should have been skipped (NullCounterMap=true)")
	}
	if v := cm["NotNull"]; v != 42 {
		t.Errorf("NotNull = %d, want 42", v)
	}
	if v := cm["Other"]; v != 7 {
		t.Errorf("Other = %d, want 7", v)
	}
	if _, present := cm["NullCounterMap"]; present {
		t.Errorf("NullCounterMap key should not appear in the output")
	}
	if _, present := cm["CollectionTime"]; present {
		t.Errorf("CollectionTime key should not appear in the output")
	}
}

// itoa avoids pulling strconv solely for the reauth test.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
