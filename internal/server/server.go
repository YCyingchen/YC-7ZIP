// Package server exposes YC-7ZIP over HTTP.
package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/ycyingchen/yc-7zip/internal/engine"
	"github.com/ycyingchen/yc-7zip/internal/job"
)

// Config is the runtime configuration.
type Config struct {
	// Addr is the listen address, e.g. ":8080".
	Addr string
	// Auth, when set as "user:pass", enables HTTP basic authentication.
	Auth string
	// MaxUpload caps a single upload in bytes.
	MaxUpload int64
	// JobTTL is how long an idle workspace survives.
	JobTTL time.Duration
	// Version is stamped into responses.
	Version string
	// AllowRoots lists absolute directories the server may read from, letting
	// a NAS user compress files that are already on disk instead of uploading
	// them again. Empty disables the feature entirely.
	AllowRoots []string
	// BasePath is a URL prefix the app is served under, e.g. "/app/yc7zip".
	// The fnOS app gateway does NOT strip its prefix — verified against a
	// shipped app, which serves its own HTML under /app/<name>/ — so the
	// server has to strip it itself.
	BasePath string
	// RepoURL is the project's public repository, surfaced in the UI so users
	// have somewhere to send bug reports.
	RepoURL string
	// Logger receives structured request logs.
	Logger *slog.Logger
}

// Server wires the engine, the job manager and the router together.
type Server struct {
	cfg    Config
	engine *engine.Engine
	jobs   *job.Manager
	log    *slog.Logger
	webFS  fs.FS
	mux    *http.ServeMux
	start  time.Time
}

// New builds a server. webFS must contain index.html at its root.
func New(cfg Config, eng *engine.Engine, jobs *job.Manager, webFS fs.FS) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &Server{
		cfg:    cfg,
		engine: eng,
		jobs:   jobs,
		log:    cfg.Logger,
		webFS:  webFS,
		mux:    http.NewServeMux(),
		start:  time.Now(),
	}
	s.routes()
	return s
}

// Handler returns the composed HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.withRecovery(s.withLogging(s.withAuth(s.withBasePath(s.mux))))
}

// healthPath is exempt from both the auth gate and the base-path strip, so a
// container runtime or an uptime monitor can always probe it at a fixed path
// without knowing how the app is mounted.
const healthPath = "/api/health"

// withBasePath strips the configured prefix so the router can stay unaware of
// where the app is mounted.
//
// A request for the bare prefix is redirected to add the trailing slash, which
// is what makes the front end's relative URLs ("assets/app.js", "api/health")
// resolve correctly whether the app is reached directly on its port or through
// the fnOS gateway.
func (s *Server) withBasePath(next http.Handler) http.Handler {
	prefix := strings.TrimSuffix(strings.TrimSpace(s.cfg.BasePath), "/")
	if prefix == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Liveness is always reachable at the same path, prefixed or not.
		if r.URL.Path == healthPath {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == prefix {
			http.Redirect(w, r, prefix+"/", http.StatusTemporaryRedirect)
			return
		}
		if !strings.HasPrefix(r.URL.Path, prefix+"/") {
			// Outside the mount point: nothing here belongs to the app.
			http.NotFound(w, r)
			return
		}
		stripped := r.Clone(r.Context())
		stripped.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
		next.ServeHTTP(w, stripped)
	})
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/formats", s.handleFormats)

	s.mux.HandleFunc("POST /api/jobs", s.handleCreateJob)
	s.mux.HandleFunc("GET /api/jobs/{id}", s.handleJobStatus)
	s.mux.HandleFunc("DELETE /api/jobs/{id}", s.handleDeleteJob)
	s.mux.HandleFunc("POST /api/jobs/{id}/cancel", s.handleCancelJob)
	s.mux.HandleFunc("POST /api/jobs/{id}/upload", s.handleUpload)
	s.mux.HandleFunc("POST /api/jobs/{id}/run", s.handleRun)
	s.mux.HandleFunc("POST /api/jobs/{id}/preview", s.handlePreview)
	s.mux.HandleFunc("GET /api/jobs/{id}/download", s.handleDownloadFile)
	s.mux.HandleFunc("GET /api/jobs/{id}/download-all", s.handleDownloadAll)

	s.mux.HandleFunc("GET /api/browse", s.handleBrowse)
	s.mux.HandleFunc("GET /api/inspect", s.handleInspect)
	s.mux.HandleFunc("GET /api/thumb", s.handleThumb)
	s.mux.HandleFunc("GET /api/raw", s.handleRaw)

	s.mux.HandleFunc("GET /", s.handleStatic)
}

// ---------------------------------------------------------------- middleware

// withAuth enforces basic authentication when a credential is configured.
func (s *Server) withAuth(next http.Handler) http.Handler {
	if s.cfg.Auth == "" {
		return next
	}
	user, pass, _ := strings.Cut(s.cfg.Auth, ":")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The health endpoint stays open so container orchestrators and the
		// fnOS status hook can probe without credentials.
		if r.URL.Path == healthPath {
			next.ServeHTTP(w, r)
			return
		}
		gotUser, gotPass, ok := r.BasicAuth()
		// Constant time comparison on both halves; a length mismatch must not
		// short-circuit differently from a value mismatch.
		userOK := subtle.ConstantTimeCompare([]byte(gotUser), []byte(user)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(gotPass), []byte(pass)) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="YC-7ZIP"`)
			writeError(w, http.StatusUnauthorized, "需要登录")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.code,
			"duration_ms", time.Since(started).Milliseconds(),
		)
	})
}

func (s *Server) withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic", "path", r.URL.Path, "panic", rec)
				writeError(w, http.StatusInternalServerError, "服务器内部错误")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ------------------------------------------------------------------- static

// handleStatic serves the embedded front end with an SPA fallback.
//
// Assets are compiled into the binary, so their URL stays the same while their
// content changes with every release. A long max-age therefore produces a
// mixed-version page — a stale app.js against a fresh index.html — which shows
// up as baffling breakage ("the buttons do nothing", "the label shows a raw
// key"). They are served with an ETag and no-cache instead: the browser keeps a
// copy but revalidates, and unchanged files cost a 304.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	upath := path.Clean("/" + r.URL.Path)
	if strings.HasPrefix(upath, "/api/") {
		writeError(w, http.StatusNotFound, "接口不存在")
		return
	}
	name := strings.TrimPrefix(upath, "/")
	if name == "" {
		name = "index.html"
	}

	data, err := fs.ReadFile(s.webFS, name)
	if err != nil {
		// Unknown path: hand back the shell so client side routing keeps
		// working, but only for paths that look like navigation.
		index, indexErr := fs.ReadFile(s.webFS, "index.html")
		if indexErr != nil {
			writeError(w, http.StatusNotFound, "资源不存在")
			return
		}
		s.serveIndex(w, index)
		return
	}

	if strings.HasSuffix(name, ".html") {
		s.serveIndex(w, data)
		return
	}

	etag := fmt.Sprintf(`"%x"`, fnv64(string(data)))
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if ctype := mime.TypeByExtension(path.Ext(name)); ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

// serveIndex returns the shell with the version stamped onto its asset URLs.
//
// The query string makes each release a new URL, so a browser still holding a
// copy cached under the old URL fetches the matching script rather than mixing
// versions. Without it, upgrading would leave existing users broken until their
// cache expired.
func (s *Server) serveIndex(w http.ResponseWriter, data []byte) {
	html := string(data)
	stamp := "?v=" + url.QueryEscape(s.cfg.Version)
	for _, asset := range []string{"assets/style.css", "assets/app.js", "assets/images/icon.png"} {
		html = strings.ReplaceAll(html, `"`+asset+`"`, `"`+asset+stamp+`"`)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(html))
}

// ----------------------------------------------------------------- utilities

func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(payload)
}

// apiError is the uniform error body.
type apiError struct {
	Error string `json:"error"`
	// PasswordRequired lets the UI switch to the password prompt instead of
	// showing a raw failure.
	PasswordRequired bool `json:"password_required,omitempty"`
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, apiError{Error: msg})
}

// requestError carries the HTTP status that belongs to a failure the caller
// caused, so a validation problem does not surface as a generic 500.
type requestError struct {
	err    error
	status int
}

func (e *requestError) Error() string { return e.err.Error() }
func (e *requestError) Unwrap() error { return e.err }

// badRequest marks invalid input.
func badRequest(format string, args ...any) error {
	return &requestError{err: fmt.Errorf(format, args...), status: http.StatusBadRequest}
}

// forbidden marks an input that is well formed but outside what the operator
// exposed: an authorisation boundary rather than a syntax error.
func forbidden(format string, args ...any) error {
	return &requestError{err: fmt.Errorf(format, args...), status: http.StatusForbidden}
}

// writeEngineError maps engine sentinels and caller-input failures onto HTTP
// status codes, falling back to 500 for genuine server faults.
func writeEngineError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, engine.ErrPasswordRequired):
		writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error(), PasswordRequired: true})
	case errors.Is(err, engine.ErrUnsafeEntry):
		writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
	default:
		var reqErr *requestError
		if errors.As(err, &reqErr) {
			writeJSON(w, reqErr.status, apiError{Error: reqErr.err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
	}
}
