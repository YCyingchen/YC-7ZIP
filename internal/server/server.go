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
	// DataDir is where persistent server-side state lives (UI settings,
	// wallpaper). It must be writable.
	DataDir string
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
	// Channel is the release channel this build came from: "test" or "stable".
	// It decides which releases count as an update and which image tag to use.
	Channel string
	// UpdateSources 是检查更新时要问的渠道，顺序即尝试顺序。每项要么是一个
	// 自建源目录地址（读它下面的 update.json），要么是 github（可写成
	// github:owner/repo）。留空则用 defaultUpdateSources。
	UpdateSources []string
	// Proxy is an optional outbound HTTP proxy for reaching GitHub, which is
	// not directly reachable from every network.
	Proxy string
	// Changelog is the embedded CHANGELOG.md, served to the in-app settings
	// panel so upgrade notes are readable without network access.
	Changelog string
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

	// updates caches the last update check.
	updates *updateState
	// sources 是解析后的更新渠道，顺序即尝试顺序。
	sources []updateSource
	// githubAPI 是 GitHub 那一条的 API 基址。留成字段是为了让测试能把它
	// 指到 httptest 上，不必为可测性把一个只测试用的开关放进 Config。
	githubAPI string
	// restart lets the process exit for a restart after a self-update; set by
	// the caller so tests can observe it instead of dying.
	restart func()

	// uiSettings persists the wallpaper and how strongly to render it.
	uiSettings *uiStore
	// history 记录已经结束的压缩/解压，供界面回看。
	history *historyStore
	// notifications 保存任务结束时要推送到哪些渠道。
	notifications *notifyStore
	// qqBind 管着 QQ 官方机器人扫码绑定的会话（见 qqbot_bind.go）。
	qqBind *qqBindStore
}

// New builds a server. webFS must contain index.html at its root.
func New(cfg Config, eng *engine.Engine, jobs *job.Manager, webFS fs.FS) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &Server{
		cfg:           cfg,
		engine:        eng,
		jobs:          jobs,
		log:           cfg.Logger,
		webFS:         webFS,
		mux:           http.NewServeMux(),
		start:         time.Now(),
		updates:       &updateState{},
		githubAPI:     "https://api.github.com",
		uiSettings:    newUIStore(cfg.DataDir),
		history:       newHistoryStore(cfg.DataDir),
		notifications: newNotifyStore(cfg.DataDir),
		qqBind:        newQQBindStore(),
	}
	s.sources = s.resolveSources()
	s.routes()
	return s
}

// resolveSources 解析更新源配置。
//
// 配错了只告警并回退到默认渠道，不让服务起不来：更新源是辅助能力，
// 为一条写错的启动参数拒绝启动，代价远大于收益。但告警必须留着——
// 否则"换了源却没生效"会变成一件只能靠翻日志才发现的事。
func (s *Server) resolveSources() []updateSource {
	parsed, err := parseUpdateSources(s.cfg.UpdateSources)
	if err != nil {
		s.log.Warn("更新源配置无法识别，改用默认渠道", "error", err)
		parsed = nil
	}
	if len(parsed) == 0 {
		fallback, _ := parseUpdateSources(defaultUpdateSources)
		parsed = fallback
	}
	for _, src := range parsed {
		if src.kind == sourceSelf && !strings.HasPrefix(src.base, "https://") {
			s.log.Warn("自建更新源不是 https：摘要与包可能一起被改写，校验只剩防传输损坏的作用",
				"source", src.base)
		}
	}
	return parsed
}

// SetRestart installs the hook used after a self-update. When unset the
// process exits, which is what a service manager needs to pick up the new
// binary.
func (s *Server) SetRestart(fn func()) { s.restart = fn }

// Handler returns the composed HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.withRecovery(s.withLogging(s.withCSRFGuard(s.withAuth(s.withBasePath(s.mux)))))
}

// withCSRFGuard rejects cross-origin state changes.
//
// 这层检查是为 Basic 认证准备的：multipart 表单属于 CORS 的"简单请求"：
// 浏览器会把缓存的 Basic 凭据自动带上，且不触发预检。于是"管理员访问了一个
// 恶意页面"就等于把更新接口交了出去——而更新接口能替换可执行文件。
//
// 判据按顺序：
//  1. 有 Sec-Fetch-Site 就只认它。这个头由浏览器自己填，页面脚本改不了，
//     也不受反向代理影响，是这里最可靠的一手信息。
//  2. 没有 Origin：非浏览器客户端（curl、脚本），放行。
//  3. Origin 与本请求同源（认 Host，也认正经代理会带的 X-Forwarded-Host）。
//  4. 应用自己没配 Basic 认证：这层检查防的就是"浏览器自动带上的缓存凭据"，
//     没有凭据就没有这个威胁；飞牛应用包部署正属于这种（访问由网关的登录态管）。
//  5. 其余一律拒绝，并把现场写进日志。
//
// 第 1 步之前是用 Origin 与 Host 硬比的，结果在网关后面必然误判：
// 网关会把 Host 改写成自己的名字（飞牛的应用网关就是），而 Origin 仍是浏览器
// 看到的那个地址，于是网关里"点任何按钮都报拒绝跨站请求"。第 5 步的日志就是为了
// 让这类误判一眼能看清——当时只回了一句 Origin，Host 是什么全靠猜。
func (s *Server) withCSRFGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}

		if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
			switch site {
			case "same-origin", "same-site", "none":
				next.ServeHTTP(w, r)
			default:
				s.rejectCSRF(w, r, "Sec-Fetch-Site="+site)
			}
			return
		}

		origin := r.Header.Get("Origin")
		if origin == "" || sameOrigin(origin, r) || s.cfg.Auth == "" {
			next.ServeHTTP(w, r)
			return
		}
		s.rejectCSRF(w, r, "Origin="+origin)
	})
}

// sameOrigin 判断 Origin 是否与本请求同源。
//
// 还认 X-Forwarded-Host：正经的反向代理会把原始 Host 放在那里。
func sameOrigin(origin string, r *http.Request) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	if strings.EqualFold(parsed.Host, r.Host) {
		return true
	}
	// 可能是逗号分隔的一串（多级代理），取第一个
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		if first := strings.TrimSpace(strings.Split(fwd, ",")[0]); first != "" {
			return strings.EqualFold(parsed.Host, first)
		}
	}
	return false
}

// rejectCSRF 拒绝一次跨站请求，并把判断依据留在日志里。
func (s *Server) rejectCSRF(w http.ResponseWriter, r *http.Request, why string) {
	s.log.Warn("拒绝跨站请求",
		"method", r.Method,
		"path", r.URL.Path,
		"reason", why,
		"host", r.Host,
		"origin", r.Header.Get("Origin"),
		"forwarded_host", r.Header.Get("X-Forwarded-Host"),
		"sec_fetch_site", r.Header.Get("Sec-Fetch-Site"),
	)
	writeError(w, http.StatusForbidden, "拒绝跨站请求："+why)
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
	s.mux.HandleFunc("GET /api/search", s.handleSearch)
	s.mux.HandleFunc("GET /api/inspect", s.handleInspect)
	s.mux.HandleFunc("GET /api/thumb", s.handleThumb)
	s.mux.HandleFunc("GET /api/raw", s.handleRaw)

	// 更新
	s.mux.HandleFunc("GET /api/update", s.handleUpdateStatus)
	s.mux.HandleFunc("POST /api/update/check", s.handleUpdateCheck)
	s.mux.HandleFunc("POST /api/update/apply", s.handleUpdateApply)
	s.mux.HandleFunc("POST /api/update/upload", s.handleUpdateUpload)

	// 界面设置与壁纸
	s.mux.HandleFunc("GET /api/changelog", s.handleChangelog)

	// 历史记录：只读回看 + 删除单条 + 清空。删记录不动磁盘上的产物。
	s.mux.HandleFunc("GET /api/history", s.handleHistoryList)
	s.mux.HandleFunc("DELETE /api/history", s.handleHistoryClear)
	s.mux.HandleFunc("DELETE /api/history/{id}", s.handleHistoryDelete)
	s.mux.HandleFunc("GET /api/ui-settings", s.handleUISettingsGet)
	s.mux.HandleFunc("PUT /api/ui-settings", s.handleUISettingsPut)
	// 通知：渠道配置 + 试发。任务结束时按这里的开关推送（见 notify.go）。
	s.mux.HandleFunc("GET /api/notifications", s.handleNotifyGet)
	s.mux.HandleFunc("PUT /api/notifications", s.handleNotifyPut)
	s.mux.HandleFunc("POST /api/notifications/test", s.handleNotifyTest)
	// QQ 机器人扫码绑定：浏览器只跟这两个接口打交道，与官方的往返在服务端完成。
	s.mux.HandleFunc("POST /api/notifications/qq/start", s.handleQQBindStart)
	s.mux.HandleFunc("GET /api/notifications/qq/poll", s.handleQQBindPoll)

	s.mux.HandleFunc("GET /api/wallpaper", s.handleWallpaperGet)
	s.mux.HandleFunc("POST /api/wallpaper", s.handleWallpaperPost)
	s.mux.HandleFunc("DELETE /api/wallpaper", s.handleWallpaperDelete)

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
