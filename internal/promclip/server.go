package promclip

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"os"
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

	// KeepExports caps the number of .txt.gz files retained in
	// OutputDir. 0 disables pruning. Set by the entrypoint from the
	// -keep-exports flag; runExport sweeps after every successful run.
	KeepExports int

	// Ephemeral is true when prom-clip runs without a persistent
	// state-dir. State stays in memory and export files are removed
	// after the browser downloads them. The entrypoint sets this.
	Ephemeral bool
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
	mux.HandleFunc("/test", s.handleTest)
	mux.HandleFunc("/export", s.handleExport)
	mux.HandleFunc("/preview", s.handlePreview)
	mux.HandleFunc("/import", s.handleImport)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/status/", s.handleStatusDetail)
	mux.HandleFunc("/cancel/", s.handleCancel)
	mux.HandleFunc("/download/", s.handleDownload)
	mux.HandleFunc("/browse", s.handleBrowse)
	mux.HandleFunc("/settings/s3", s.handleS3)
	mux.HandleFunc("/settings/s3/test", s.handleS3Test)
}

func (s *Server) handleS3(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		t := readS3Form(r)
		if err := s.state.SetS3(t); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/settings/s3?saved=1", http.StatusSeeOther)
		return
	}
	s.render(w, r, "s3.html", map[string]any{
		"Page":  "s3",
		"S3":    s.state.S3Snapshot(),
		"Saved": r.URL.Query().Get("saved") != "",
	})
}

func (s *Server) handleS3Test(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	t := readS3Form(r)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	msg, err := S3Test(ctx, t)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(msg))
}

func readS3Form(r *http.Request) S3Target {
	return S3Target{
		Endpoint:      strings.TrimSpace(r.FormValue("endpoint")),
		Region:        strings.TrimSpace(r.FormValue("region")),
		Bucket:        strings.TrimSpace(r.FormValue("bucket")),
		Prefix:        strings.TrimSpace(r.FormValue("prefix")),
		AccessKey:     strings.TrimSpace(r.FormValue("access_key")),
		SecretKey:     r.FormValue("secret_key"),
		UseSSL:        r.FormValue("use_ssl") != "",
		PathStyle:     r.FormValue("path_style") != "",
		Public:        r.FormValue("public") != "",
		PublicBaseURL: strings.TrimSpace(r.FormValue("public_base_url")),
	}
}

// handleBrowse lists a server-side directory as JSON. Used by the
// folder/file picker modal in the UI. The path is taken from the query
// string (?path=...). Empty path means: list the synthetic root (Windows
// drives) or "/" on Linux.
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	res, err := Browse(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(res)
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
		c := readConnForm(r)
		if err := s.state.SetLastConnection(c); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/?saved=1", http.StatusSeeOther)
		return
	}
	s.render(w, r, "connection.html", map[string]any{
		"Page":       "connection",
		"Connection": s.state.Snapshot(),
		"Saved":      r.URL.Query().Get("saved") != "",
	})
}

func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c := readConnForm(r)
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
	conn := s.state.Snapshot()
	pc, err := NewPromClient(conn)
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
		s.render(w, r, "export.html", map[string]any{
			"Page":       "export",
			"Connection": s.state.Snapshot(),
			"OutputDir":  s.OutputDir,
			"Ephemeral":  s.Ephemeral,
			"Now":        time.Now(),
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
	conn := s.state.Snapshot()
	if conn.URL == "" {
		http.Error(w, "Prometheus is not configured (go to / first)", http.StatusBadRequest)
		return
	}
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
	outFolder := strings.TrimSpace(r.FormValue("output_folder"))
	if outFolder == "" {
		outFolder = s.OutputDir
	}
	outPath := outName
	switch {
	case filepath.IsAbs(outPath):
		// Absolute name wins over the folder field.
	case strings.ContainsAny(outName, `/\`):
		// Name embeds a path separator: treat as relative to folder.
		outPath = filepath.Join(outFolder, outName)
	default:
		outPath = filepath.Join(outFolder, outName)
	}
	pushS3 := r.FormValue("s3") != ""
	if pushS3 {
		t := s.state.S3Snapshot()
		if t.Bucket == "" || t.Endpoint == "" {
			http.Error(w, "S3 push requested but no S3 target is configured (Settings → S3 target)", http.StatusBadRequest)
			return
		}
	}

	id := newRunID()
	run := Run{
		ID:          id,
		Kind:        KindExport,
		Status:      StatusRunning,
		StartedAt:   time.Now(),
		SourceURL:   conn.URL,
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
		Source: conn, From: from, To: to, Step: step,
		MetricRegex: regex, OutputPath: outPath,
	}, pushS3, s.state.S3Snapshot())

	http.Redirect(w, r, "/status/"+id, http.StatusSeeOther)
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.render(w, r, "import.html", map[string]any{
			"Page":       "import",
			"Connection": s.state.Snapshot(),
			"OutputDir":  s.OutputDir,
		})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	// 32 MiB in-memory then disk-spool — Import streams via os.Open below,
	// so very large uploads don't need to be held in RAM.
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "parse upload: "+err.Error(), http.StatusBadRequest)
		return
	}
	conn := s.state.Snapshot()
	if conn.URL == "" {
		http.Error(w, "Prometheus is not configured (go to / first)", http.StatusBadRequest)
		return
	}
	input, err := s.resolveImportInput(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	batchSize, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("batch")))
	groupOverride := strings.TrimSpace(r.FormValue("group_override"))

	id := newRunID()
	run := Run{
		ID:        id,
		Kind:      KindImport,
		Status:    StatusRunning,
		StartedAt: time.Now(),
		TargetURL: conn.URL,
		InputPath: input,
	}
	if err := s.state.AddRun(run); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.trackRun(id, cancel)
	go s.runImport(ctx, id, ImportRequest{
		Target: conn, InputPath: input, BatchSeries: batchSize,
		OverrideGroup: groupOverride,
	})

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

func (s *Server) runExport(ctx context.Context, id string, req ExportRequest, pushS3 bool, s3target S3Target) {
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
		share, err = S3Upload(ctx, log, req.OutputPath, s3target)
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
	if status == StatusSuccess && s.KeepExports > 0 && s.OutputDir != "" {
		if n, err := PruneExports(s.OutputDir, s.KeepExports); err != nil {
			log.Warn("prune exports", "err", err, "removed", n)
		} else if n > 0 {
			log.Info("pruned old exports", "removed", n, "keep", s.KeepExports)
		}
	}
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

// resolveImportInput returns the path the import worker should open.
// Priority: a non-empty multipart "file" upload (streamed to
// <OutputDir>/uploads/<stamp>-<sanitized-name>); otherwise the
// server-side "input" path field (resolved relative to OutputDir).
func (s *Server) resolveImportInput(r *http.Request) (string, error) {
	if r.MultipartForm != nil {
		if headers := r.MultipartForm.File["file"]; len(headers) > 0 && headers[0].Size > 0 {
			fh := headers[0]
			src, err := fh.Open()
			if err != nil {
				return "", fmt.Errorf("open upload: %w", err)
			}
			defer src.Close()
			uploadsDir := filepath.Join(s.OutputDir, "uploads")
			if err := os.MkdirAll(uploadsDir, 0o700); err != nil {
				return "", fmt.Errorf("uploads dir: %w", err)
			}
			name := sanitizeUploadName(fh.Filename)
			dstPath := filepath.Join(uploadsDir, time.Now().UTC().Format("20060102-150405-")+name)
			dst, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return "", fmt.Errorf("create upload dest: %w", err)
			}
			if _, err := io.Copy(dst, src); err != nil {
				_ = dst.Close()
				_ = os.Remove(dstPath)
				return "", fmt.Errorf("save upload: %w", err)
			}
			if err := dst.Close(); err != nil {
				return "", fmt.Errorf("save upload: %w", err)
			}
			return dstPath, nil
		}
	}
	input := strings.TrimSpace(r.FormValue("input"))
	if input == "" {
		return "", errors.New("either upload a file or provide a server-side path")
	}
	if !filepath.IsAbs(input) {
		input = filepath.Join(s.OutputDir, input)
	}
	return input, nil
}

// sanitizeUploadName strips path separators and replaces unsafe runes
// from a user-supplied filename so it can be appended to OutputDir
// without escaping it. Falls back to "upload" if the input collapses
// to empty.
func sanitizeUploadName(name string) string {
	// Discard any directory component the browser might send (older
	// browsers prepended one).
	name = filepath.Base(filepath.ToSlash(name))
	name = filepath.Base(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	clean := strings.Trim(b.String(), ".")
	if clean == "" {
		clean = "upload"
	}
	if len(clean) > 200 {
		clean = clean[:200]
	}
	return clean
}

// handleDownload streams the output file of a successful export back to
// the browser as an attachment, so the user gets a native "Save as"
// dialog. Refuses anything outside OutputDir (path-traversal guard).
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/download/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	run := s.state.GetRun(id)
	if run == nil || run.Kind != KindExport || run.OutputPath == "" {
		http.NotFound(w, r)
		return
	}
	abs, err := filepath.Abs(run.OutputPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	root, err := filepath.Abs(s.OutputDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		http.Error(w, "file is outside the managed output directory", http.StatusForbidden)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "output file no longer exists on disk", http.StatusGone)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	name := filepath.Base(abs)
	ct := "application/octet-stream"
	switch {
	case strings.HasSuffix(strings.ToLower(name), ".gz"):
		ct = "application/gzip"
	case strings.HasSuffix(strings.ToLower(name), ".txt"):
		ct = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	http.ServeContent(w, r, name, st.ModTime(), f)

	// In ephemeral mode the file lives in os.TempDir() only as long
	// as the user needs to fetch it once. Close before Remove because
	// Windows refuses to delete an open file.
	if s.Ephemeral {
		_ = f.Close()
		if err := os.Remove(abs); err == nil {
			s.log.Info("ephemeral export removed after download", "path", abs)
			_ = s.state.UpdateRun(id, func(r *Run) {
				r.OutputPath = ""
			})
		} else {
			s.log.Warn("ephemeral export remove failed", "path", abs, "err", err)
		}
	}
}

func readConnForm(r *http.Request) Connection {
	return Connection{
		URL:      strings.TrimSpace(r.FormValue("url")),
		Username: strings.TrimSpace(r.FormValue("username")),
		Password: r.FormValue("password"),
		Insecure: r.FormValue("insecure") != "",
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
