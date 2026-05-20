package promclip

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed templates/*.html
var tmplFS embed.FS

// Server is the prom-clip HTTP UI.
type Server struct {
	state *State
	log   *slog.Logger
	tmpl  *template.Template

	mu      sync.Mutex
	running map[string]context.CancelFunc

	// OutputDir is where exported .txt.gz files are written when the
	// user does not pass an absolute path. Created on demand.
	OutputDir string
}

// NewServer initializes the server with templates loaded.
func NewServer(state *State, log *slog.Logger, outputDir string) (*Server, error) {
	funcs := template.FuncMap{
		"fmtTime": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.Local().Format("2006-01-02 15:04:05")
		},
		"fmtTimePtr": func(t *time.Time) string {
			if t == nil || t.IsZero() {
				return ""
			}
			return t.Local().Format("2006-01-02 15:04:05")
		},
		"fmtBytes": func(n int64) string {
			const (
				kib = 1024
				mib = 1024 * kib
				gib = 1024 * mib
			)
			switch {
			case n >= gib:
				return fmt.Sprintf("%.2f GiB", float64(n)/gib)
			case n >= mib:
				return fmt.Sprintf("%.2f MiB", float64(n)/mib)
			case n >= kib:
				return fmt.Sprintf("%.2f KiB", float64(n)/kib)
			}
			return fmt.Sprintf("%d B", n)
		},
		"fmtDuration": func(start time.Time, end *time.Time) string {
			to := time.Now()
			if end != nil {
				to = *end
			}
			d := to.Sub(start).Round(time.Second)
			return d.String()
		},
	}
	t, err := template.New("").Funcs(funcs).ParseFS(tmplFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return &Server{
		state:     state,
		log:       log,
		tmpl:      t,
		running:   map[string]context.CancelFunc{},
		OutputDir: outputDir,
	}, nil
}

// Routes registers prom-clip's endpoints on mux.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("/", s.handleConnection)
	mux.HandleFunc("/test-source", s.handleTestSource)
	mux.HandleFunc("/test-target", s.handleTestTarget)
	mux.HandleFunc("/export", s.handleExport)
	mux.HandleFunc("/preview", s.handlePreview)
	mux.HandleFunc("/import", s.handleImport)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/status/", s.handleStatusDetail)
	mux.HandleFunc("/cancel/", s.handleCancel)
}

func (s *Server) handleConnection(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		side := r.FormValue("side")
		c := readConnForm(r, side)
		switch side {
		case "source":
			if err := s.state.SetLastSource(c); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		case "target":
			if err := s.state.SetLastTarget(c); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		http.Redirect(w, r, "/?saved="+side, http.StatusSeeOther)
		return
	}
	src, tgt := s.state.Snapshot()
	s.render(w, r, "connection.html", map[string]any{
		"Page":   "connection",
		"Source": src,
		"Target": tgt,
		"Saved":  r.URL.Query().Get("saved"),
	})
}

func (s *Server) handleTestSource(w http.ResponseWriter, r *http.Request) {
	s.handleTest(w, r, true)
}

func (s *Server) handleTestTarget(w http.ResponseWriter, r *http.Request) {
	s.handleTest(w, r, false)
}

func (s *Server) handleTest(w http.ResponseWriter, r *http.Request, isSource bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	side := "source"
	if !isSource {
		side = "target"
	}
	c := readConnForm(r, side)
	pc, err := NewPromClient(c)
	if err != nil {
		http.Error(w, "client init: "+err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	msg, err := pc.Ping(ctx)
	if err != nil {
		http.Error(w, "ping failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("OK — " + msg))
}

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	src, _ := s.state.Snapshot()
	pc, err := NewPromClient(src)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	from, to, err := parseTimeWindow(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	regex := strings.TrimSpace(r.FormValue("regex"))
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	names, err := pc.ListMetricNames(ctx, regex, from, to)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprintf(w, "%d metric(s) match:\n", len(names))
	for _, n := range names {
		_, _ = fmt.Fprintln(w, n)
	}
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		src, _ := s.state.Snapshot()
		s.render(w, r, "export.html", map[string]any{
			"Page":      "export",
			"Source":    src,
			"OutputDir": s.OutputDir,
			"Now":       time.Now(),
		})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	src, _ := s.state.Snapshot()
	from, to, err := parseTimeWindow(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	step, err := time.ParseDuration(strings.TrimSpace(r.FormValue("step")))
	if err != nil {
		http.Error(w, "bad step: "+err.Error(), http.StatusBadRequest)
		return
	}
	regex := strings.TrimSpace(r.FormValue("regex"))
	outName := strings.TrimSpace(r.FormValue("output"))
	if outName == "" {
		outName = fmt.Sprintf("prom-clip-%s.txt.gz", time.Now().UTC().Format("20060102-150405"))
	}
	outPath := outName
	if !filepath.IsAbs(outPath) {
		outPath = filepath.Join(s.OutputDir, outName)
	}
	pushS3 := r.FormValue("s3") != ""
	publicS3 := r.FormValue("s3_public") != ""

	id := newRunID()
	run := Run{
		ID:          id,
		Kind:        KindExport,
		Status:      StatusRunning,
		StartedAt:   time.Now(),
		SourceURL:   src.URL,
		From:        from,
		To:          to,
		Step:        step.String(),
		MetricRegex: regex,
		OutputPath:  outPath,
	}
	if err := s.state.AddRun(run); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.trackRun(id, cancel)
	go s.runExport(ctx, id, ExportRequest{
		Source: src, From: from, To: to, Step: step,
		MetricRegex: regex, OutputPath: outPath,
	}, pushS3, publicS3)

	http.Redirect(w, r, "/status/"+id, http.StatusSeeOther)
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		_, tgt := s.state.Snapshot()
		s.render(w, r, "import.html", map[string]any{
			"Page":      "import",
			"Target":    tgt,
			"OutputDir": s.OutputDir,
		})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, tgt := s.state.Snapshot()
	if tgt.URL == "" {
		http.Error(w, "target Prometheus is not configured (go to / first)", http.StatusBadRequest)
		return
	}
	input := strings.TrimSpace(r.FormValue("input"))
	if input == "" {
		http.Error(w, "input file required", http.StatusBadRequest)
		return
	}
	if !filepath.IsAbs(input) {
		input = filepath.Join(s.OutputDir, input)
	}
	batchSize, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("batch")))

	id := newRunID()
	run := Run{
		ID:        id,
		Kind:      KindImport,
		Status:    StatusRunning,
		StartedAt: time.Now(),
		TargetURL: tgt.URL,
		InputPath: input,
	}
	if err := s.state.AddRun(run); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.trackRun(id, cancel)
	go s.runImport(ctx, id, ImportRequest{Target: tgt, InputPath: input, BatchSeries: batchSize})

	http.Redirect(w, r, "/status/"+id, http.StatusSeeOther)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	runs := s.state.ListRuns()
	s.render(w, r, "status.html", map[string]any{
		"Page": "status",
		"Runs": runs,
	})
}

func (s *Server) handleStatusDetail(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/status/")
	if id == "" {
		http.Redirect(w, r, "/status", http.StatusSeeOther)
		return
	}
	run := s.state.GetRun(id)
	if run == nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, r, "status_detail.html", map[string]any{
		"Page": "status",
		"Run":  run,
	})
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/cancel/")
	s.mu.Lock()
	cancel, ok := s.running[id]
	s.mu.Unlock()
	if !ok {
		http.Redirect(w, r, "/status/"+id, http.StatusSeeOther)
		return
	}
	cancel()
	http.Redirect(w, r, "/status/"+id, http.StatusSeeOther)
}

func (s *Server) runExport(ctx context.Context, id string, req ExportRequest, pushS3, publicS3 bool) {
	defer s.untrackRun(id)
	log := s.log.With("run", id, "kind", "export")
	res, err := Export(ctx, log, req)
	now := time.Now()
	finalErr := ""
	status := StatusSuccess
	if err != nil {
		finalErr = err.Error()
		status = StatusFailed
		log.Error("export failed", "err", err)
	} else {
		log.Info("export done", "series", res.Series, "samples", res.Samples, "bytes", res.Bytes)
	}
	share := ""
	if err == nil && pushS3 {
		share, err = S3Upload(ctx, log, req.OutputPath, publicS3)
		if err != nil {
			log.Warn("s3 upload failed", "err", err)
			finalErr = "export OK but S3 upload failed: " + err.Error()
			// keep status = success; user still has the local file
		}
	}
	_ = s.state.UpdateRun(id, func(r *Run) {
		r.Status = status
		r.Error = finalErr
		r.FinishedAt = &now
		r.Series = res.Series
		r.Samples = res.Samples
		r.Bytes = res.Bytes
		r.ShareURL = share
	})
}

func (s *Server) runImport(ctx context.Context, id string, req ImportRequest) {
	defer s.untrackRun(id)
	log := s.log.With("run", id, "kind", "import")
	res, err := Import(ctx, log, req)
	now := time.Now()
	finalErr := ""
	status := StatusSuccess
	if err != nil {
		finalErr = err.Error()
		status = StatusFailed
		log.Error("import failed", "err", err)
	} else {
		log.Info("import done", "series", res.Series, "samples", res.Samples, "batches", res.Batches)
	}
	_ = s.state.UpdateRun(id, func(r *Run) {
		r.Status = status
		r.Error = finalErr
		r.FinishedAt = &now
		r.Series = res.Series
		r.Samples = res.Samples
	})
}

func (s *Server) trackRun(id string, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running[id] = cancel
}

func (s *Server) untrackRun(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, id)
}

func (s *Server) render(w http.ResponseWriter, _ *http.Request, name string, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		s.log.Error("template render", "name", name, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func readConnForm(r *http.Request, prefix string) Connection {
	return Connection{
		URL:      strings.TrimSpace(r.FormValue(prefix + "_url")),
		Username: strings.TrimSpace(r.FormValue(prefix + "_username")),
		Password: r.FormValue(prefix + "_password"),
		Insecure: r.FormValue(prefix+"_insecure") != "",
	}
}

func parseTimeWindow(r *http.Request) (time.Time, time.Time, error) {
	const layout = "2006-01-02T15:04"
	fromStr := strings.TrimSpace(r.FormValue("from"))
	toStr := strings.TrimSpace(r.FormValue("to"))
	if fromStr == "" || toStr == "" {
		return time.Time{}, time.Time{}, errors.New("from and to are required")
	}
	from, err := time.ParseInLocation(layout, fromStr, time.Local)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse from: %w", err)
	}
	to, err := time.ParseInLocation(layout, toStr, time.Local)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse to: %w", err)
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, errors.New("from must be < to")
	}
	return from, to, nil
}

func newRunID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102-150405-") + hex.EncodeToString(b[:])
}
