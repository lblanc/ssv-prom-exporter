package promclip

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/golang/snappy"
)

// ImportRequest describes one replay run.
type ImportRequest struct {
	Target    Connection
	InputPath string // .txt.gz produced by Export (or any compatible OpenMetrics file)
	// BatchSeries caps the number of series per WriteRequest sent on the
	// wire. Defaults to 500 if unset; large batches reduce HTTP overhead
	// but Prometheus has a 32 MiB hard cap on snappy-decompressed bodies.
	BatchSeries int
	// OverrideGroup, when non-empty, replaces (or adds) the "group" label
	// on every imported series. Useful when re-injecting a customer's
	// export into a local lab to retag everything under a new group.
	// Note: collapsing distinct group values into one merges series that
	// were previously distinguished only by group.
	OverrideGroup string
}

// ImportResult is the post-flight summary.
type ImportResult struct {
	Series  int
	Samples int64
	Batches int
}

// Import streams an OpenMetrics file into the target Prometheus via
// remote-write. The target must run with
// `--web.enable-remote-write-receiver`.
func Import(ctx context.Context, log *slog.Logger, req ImportRequest) (ImportResult, error) {
	if req.InputPath == "" {
		return ImportResult{}, fmt.Errorf("InputPath required")
	}
	batchSize := req.BatchSeries
	if batchSize <= 0 {
		batchSize = 500
	}

	f, err := os.Open(req.InputPath)
	if err != nil {
		return ImportResult{}, fmt.Errorf("open input: %w", err)
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(strings.ToLower(req.InputPath), ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return ImportResult{}, fmt.Errorf("gzip: %w", err)
		}
		defer gz.Close()
		r = gz
	}

	endpoint := strings.TrimRight(req.Target.URL, "/") + "/api/v1/write"
	httpc := &http.Client{
		Timeout:   2 * time.Minute,
		Transport: newRoundTripper(req.Target),
	}

	var (
		res    ImportResult
		batch  []PBTimeSeries
		seen   = map[string]int{} // canonical key -> index in current batch
		allKey = map[string]struct{}{}
	)

	send := func() error {
		if len(batch) == 0 {
			return nil
		}
		raw := MarshalWriteRequest(batch)
		compressed := snappy.Encode(nil, raw)
		hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(compressed))
		if err != nil {
			return err
		}
		hreq.Header.Set("Content-Type", "application/x-protobuf")
		hreq.Header.Set("Content-Encoding", "snappy")
		hreq.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
		hreq.Header.Set("User-Agent", "prom-clip/dev")
		resp, err := httpc.Do(hreq)
		if err != nil {
			return fmt.Errorf("POST %s: %w", endpoint, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return fmt.Errorf("remote-write HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		res.Batches++
		batch = batch[:0]
		seen = map[string]int{}
		return nil
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if line == "# EOF" {
			break
		}
		if strings.HasPrefix(line, "#") {
			// TYPE / HELP / UNIT — ignored on replay (Prometheus reconstructs
			// the type from the samples; metadata isn't carried over remote
			// write 1.0 anyway).
			continue
		}

		name, labels, val, ts, perr := parseSampleLine(line)
		if perr != nil {
			log.Warn("import: skip line", "err", perr, "line", line)
			continue
		}
		if req.OverrideGroup != "" {
			labels = upsertLabel(labels, "group", req.OverrideGroup)
		}

		pbLabels := make([]PBLabel, 0, len(labels)+1)
		pbLabels = append(pbLabels, PBLabel{Name: "__name__", Value: name})
		pbLabels = append(pbLabels, labels...)
		key := canonicalKey(pbLabels)

		if _, ever := allKey[key]; !ever {
			allKey[key] = struct{}{}
			res.Series++
		}
		if idx, ok := seen[key]; ok {
			batch[idx].Samples = append(batch[idx].Samples, PBSample{Value: val, TimestampMs: ts})
		} else {
			seen[key] = len(batch)
			batch = append(batch, PBTimeSeries{
				Labels:  pbLabels,
				Samples: []PBSample{{Value: val, TimestampMs: ts}},
			})
		}
		res.Samples++

		if len(batch) >= batchSize {
			if err := send(); err != nil {
				return res, err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return res, fmt.Errorf("scan: %w", err)
	}
	if err := send(); err != nil {
		return res, err
	}
	return res, nil
}

// parseSampleLine parses a single OpenMetrics sample line of the form:
//
//	metric_name{l1="v1",l2="v2",...} <value> <timestamp_seconds>
//	metric_name <value> <timestamp_seconds>
//
// Returns metric name, label slice (excluding __name__), value, and the
// timestamp converted to milliseconds.
func parseSampleLine(line string) (name string, labels []PBLabel, value float64, tsMs int64, err error) {
	var rest string
	if br := strings.IndexByte(line, '{'); br >= 0 {
		closeIdx := strings.LastIndexByte(line, '}')
		if closeIdx < br {
			return "", nil, 0, 0, fmt.Errorf("malformed labels")
		}
		name = line[:br]
		labels, err = parseLabelBlock(line[br+1 : closeIdx])
		if err != nil {
			return "", nil, 0, 0, err
		}
		rest = strings.TrimLeft(line[closeIdx+1:], " ")
	} else {
		sp := strings.IndexByte(line, ' ')
		if sp < 0 {
			return "", nil, 0, 0, fmt.Errorf("no value")
		}
		name = line[:sp]
		rest = strings.TrimLeft(line[sp+1:], " ")
	}

	fields := strings.Fields(rest)
	if len(fields) < 1 {
		return "", nil, 0, 0, fmt.Errorf("missing value")
	}
	value, err = parseSampleFloat(fields[0])
	if err != nil {
		return "", nil, 0, 0, err
	}
	if len(fields) >= 2 {
		f, perr := strconv.ParseFloat(fields[1], 64)
		if perr != nil {
			return "", nil, 0, 0, fmt.Errorf("parse timestamp: %w", perr)
		}
		tsMs = int64(f * 1000.0)
	} else {
		tsMs = time.Now().UnixMilli()
	}
	return name, labels, value, tsMs, nil
}

func parseSampleFloat(s string) (float64, error) {
	switch s {
	case "NaN":
		return math.NaN(), nil
	case "+Inf":
		return math.Inf(1), nil
	case "-Inf":
		return math.Inf(-1), nil
	}
	return strconv.ParseFloat(s, 64)
}

func parseLabelBlock(s string) ([]PBLabel, error) {
	var labels []PBLabel
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ',' || s[i] == ' ') {
			i++
		}
		if i >= len(s) {
			break
		}
		eq := strings.IndexByte(s[i:], '=')
		if eq < 0 {
			return nil, fmt.Errorf("label without '=' in %q", s[i:])
		}
		name := s[i : i+eq]
		i += eq + 1
		if i >= len(s) || s[i] != '"' {
			return nil, fmt.Errorf("label value not quoted at %q", s[i:])
		}
		i++
		var v strings.Builder
		for i < len(s) {
			c := s[i]
			if c == '\\' && i+1 < len(s) {
				switch s[i+1] {
				case '\\':
					v.WriteByte('\\')
				case '"':
					v.WriteByte('"')
				case 'n':
					v.WriteByte('\n')
				default:
					v.WriteByte(s[i+1])
				}
				i += 2
				continue
			}
			if c == '"' {
				i++
				break
			}
			v.WriteByte(c)
			i++
		}
		labels = append(labels, PBLabel{Name: name, Value: v.String()})
	}
	return labels, nil
}

// upsertLabel replaces the value of an existing label or appends a new
// one. The slice is mutated in place when the label already exists; a
// new slice with one extra entry is returned otherwise.
func upsertLabel(labels []PBLabel, name, value string) []PBLabel {
	for i := range labels {
		if labels[i].Name == name {
			labels[i].Value = value
			return labels
		}
	}
	return append(labels, PBLabel{Name: name, Value: value})
}

func canonicalKey(labels []PBLabel) string {
	var b strings.Builder
	for _, l := range labels {
		b.WriteString(l.Name)
		b.WriteByte('=')
		b.WriteString(l.Value)
		b.WriteByte(0)
	}
	return b.String()
}
