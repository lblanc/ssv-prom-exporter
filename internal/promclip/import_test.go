package promclip

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang/snappy"
)

func TestUpsertLabel(t *testing.T) {
	cases := []struct {
		name   string
		in     []PBLabel
		key    string
		val    string
		expect []PBLabel
	}{
		{
			name:   "appends when missing",
			in:     []PBLabel{{Name: "instance", Value: "x"}},
			key:    "group", val: "G1",
			expect: []PBLabel{{Name: "instance", Value: "x"}, {Name: "group", Value: "G1"}},
		},
		{
			name:   "replaces existing in place",
			in:     []PBLabel{{Name: "group", Value: "OLD"}, {Name: "instance", Value: "x"}},
			key:    "group", val: "NEW",
			expect: []PBLabel{{Name: "group", Value: "NEW"}, {Name: "instance", Value: "x"}},
		},
		{
			name:   "empty input",
			in:     nil,
			key:    "group", val: "G",
			expect: []PBLabel{{Name: "group", Value: "G"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := upsertLabel(tc.in, tc.key, tc.val)
			if len(got) != len(tc.expect) {
				t.Fatalf("len: got %d want %d", len(got), len(tc.expect))
			}
			for i := range got {
				if got[i] != tc.expect[i] {
					t.Errorf("[%d] got %+v want %+v", i, got[i], tc.expect[i])
				}
			}
		})
	}
}

// TestImport_OverrideGroup feeds Import a tiny OpenMetrics file with
// group="ORIG", points it at an httptest server that captures the
// remote-write payload, and asserts the label was rewritten to
// group="NEW" before being sent over the wire. The check is a
// substring scan of the snappy-decompressed protobuf, which is
// adequate because PBLabel values are encoded as raw UTF-8 in the
// wire format used by protowrite.go.
func TestImport_OverrideGroup(t *testing.T) {
	dir := t.TempDir()
	gzPath := filepath.Join(dir, "in.txt.gz")
	f, err := os.Create(gzPath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	body := "# TYPE up gauge\n" +
		`up{group="ORIG",instance="x"} 1 1700000000` + "\n" +
		"# EOF\n"
	if _, err := io.WriteString(gz, body); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/write" {
			http.NotFound(w, r)
			return
		}
		buf, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		captured = buf
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	res, err := Import(context.Background(), log, ImportRequest{
		Target:        Connection{URL: srv.URL},
		InputPath:     gzPath,
		OverrideGroup: "NEW",
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Series != 1 || res.Samples != 1 {
		t.Errorf("counters: series=%d samples=%d (want 1/1)", res.Series, res.Samples)
	}
	if len(captured) == 0 {
		t.Fatal("server captured no body")
	}
	decoded, err := snappy.Decode(nil, captured)
	if err != nil {
		t.Fatalf("snappy decode: %v", err)
	}
	if !bytes.Contains(decoded, []byte("NEW")) {
		t.Errorf("payload does not contain 'NEW': %q", trim(decoded))
	}
	if bytes.Contains(decoded, []byte("ORIG")) {
		t.Errorf("payload still contains 'ORIG' — override did not stick: %q", trim(decoded))
	}
}

func trim(b []byte) string {
	s := string(b)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return strings.ReplaceAll(s, "\n", "\\n")
}
