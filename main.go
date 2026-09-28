// Command yc7zip serves the YC-7ZIP web interface: a self-hosted archive
// manager for 7z, zip, rar, tar and the rest of the 7-Zip format family.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ycyingchen/yc-7zip/internal/engine"
	"github.com/ycyingchen/yc-7zip/internal/job"
	"github.com/ycyingchen/yc-7zip/internal/server"
)

// version is injected at build time with -ldflags "-X main.version=...".
var version = "dev"

// channel 由构建注入（test / stable），决定检查更新时认哪些发布。
var channel = "stable"

// defaultRepoURL is where bug reports go; override with -repo when running a fork.
const defaultRepoURL = "https://github.com/YCyingchen/YC-7ZIP"

// stringList collects a repeatable flag.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if !filepath.IsAbs(v) {
		abs, err := filepath.Abs(v)
		if err != nil {
			return err
		}
		v = abs
	}
	*s = append(*s, filepath.Clean(v))
	return nil
}

func main() {
	os.Exit(run())
}

func run() int {
	fs := flag.NewFlagSet("yc7zip", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `YC-7ZIP %s — 网页版压缩/解压工具

用法:
  yc7zip [选项]

选项:
`, version)
		fs.PrintDefaults()
		fmt.Fprint(fs.Output(), `
示例:
  yc7zip                                  监听 :8080，数据存放在系统临时目录
  yc7zip -addr :9090 -data /var/lib/yc7zip
  yc7zip -auth admin:secret -max-upload 20G
  yc7zip -allow-root /vol1/1000/共享 -allow-root /vol5/1000/空间4
`)
	}

	var (
		addr       = fs.String("addr", envOr("YC7ZIP_ADDR", ":8080"), "HTTP 监听地址，留空则只监听 unix socket")
		socketPath = fs.String("socket", envOr("YC7ZIP_SOCKET", ""), "额外监听一个 unix socket（飞牛网关用），例如 /trim/app.sock")
		basePath   = fs.String("base-path", envOr("YC7ZIP_BASE_PATH", ""), "URL 前缀，例如 /app/yc7zip（经飞牛网关访问时使用）")
		dataDir    = fs.String("data", envOr("YC7ZIP_DATA", ""), "任务数据目录（默认为系统临时目录下的 yc7zip）")
		auth       = fs.String("auth", envOr("YC7ZIP_AUTH", ""), "HTTP Basic 认证，格式 user:password，留空则不启用")
		maxUpload  = fs.String("max-upload", envOr("YC7ZIP_MAX_UPLOAD", "4G"), "单个任务的上传总量上限，例如 4G / 500M，0 表示不限制")
		jobTTL     = fs.Duration("job-ttl", 2*time.Hour, "任务结果保留时长，超时后自动清理")
		repoURL    = fs.String("repo", envOr("YC7ZIP_REPO", defaultRepoURL), "项目仓库地址，显示在界面上作为反馈入口")
		showVer    = fs.Bool("version", false, "打印版本后退出")
		showBuild  = fs.Bool("build-info", false, "打印版本与发布通道后退出")
		proxyURL   = fs.String("proxy", envOr("YC7ZIP_PROXY", ""), "检查更新用的出站代理，例如 http://192.168.1.8:7890")
		quiet      = fs.Bool("quiet", false, "只输出错误日志")
		allowRoots stringList
	)
	fs.Var(&allowRoots, "allow-root", "允许作为压缩源的服务器目录，可重复指定（默认关闭该功能）")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}
	if *showVer {
		// 只输出版本号：更新流程会用 -version 探测包内程序，
		// 多输出一个词就会让那个解析出错。
		fmt.Printf("YC-7ZIP %s\n", version)
		return 0
	}
	if *showBuild {
		// 版本 + 通道。用来核对构建时确实把 -X main.channel= 注进去了——
		// 只注入版本、漏了通道的话，二进制会永远以为自己在 stable 上，
		// 而这种缺失在运行时不报错，只能靠构建后核对。
		fmt.Printf("version=%s channel=%s\n", version, channel)
		return 0
	}

	// Comma separated form for container and package deployments, where a
	// repeatable flag is awkward to express in a compose file.
	if len(allowRoots) == 0 {
		if env := os.Getenv("YC7ZIP_ALLOW_ROOTS"); env != "" {
			for _, part := range strings.Split(env, ",") {
				if err := allowRoots.Set(part); err != nil {
					fmt.Fprintf(os.Stderr, "YC7ZIP_ALLOW_ROOTS 中的路径无效：%s\n", part)
					return 2
				}
			}
		}
	}

	level := slog.LevelInfo
	if *quiet {
		level = slog.LevelError
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	maxBytes, err := parseSize(*maxUpload)
	if err != nil {
		logger.Error("无法解析 -max-upload", "value", *maxUpload, "error", err)
		return 2
	}

	root := *dataDir
	if root == "" {
		root = filepath.Join(os.TempDir(), "yc7zip")
	}
	jobs, err := job.NewManager(root, *jobTTL)
	if err != nil {
		logger.Error("无法初始化数据目录", "path", root, "error", err)
		return 1
	}

	// A missing 7-Zip is not fatal: the UI still loads and /api/health reports
	// the problem, which beats a process that refuses to boot and leaves the
	// operator guessing.
	eng, engErr := engine.New()
	if engErr != nil {
		logger.Error("7-Zip 不可用，压缩与解压功能将不可用", "error", engErr)
		eng = &engine.Engine{}
	} else {
		logger.Info("已找到 7-Zip", "path", eng.BinPath, "version", eng.Version)
	}

	cfg := server.Config{
		Addr:       *addr,
		Auth:       *auth,
		MaxUpload:  maxBytes,
		JobTTL:     *jobTTL,
		Version:    version,
		AllowRoots: allowRoots,
		BasePath:   *basePath,
		RepoURL:    *repoURL,
		Channel:    channel,
		Proxy:      *proxyURL,
		DataDir:    root,
		Changelog:  string(changelogMD),
		Logger:     logger,
	}
	srv := server.New(cfg, eng, jobs, webFS)

	httpServer := &http.Server{
		Handler: srv.Handler(),
		// Long timeouts: a big archive may take many minutes to upload or a
		// large listing many seconds to produce.
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       120 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return context.Background() },
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)

	// A unix socket is how the fnOS app gateway reaches a container app: the
	// gateway dials the socket and forwards the original path, so the app has
	// to create it where the package directory is mounted.
	if *socketPath != "" {
		if err := serveUnixSocket(httpServer, *socketPath, logger, errCh); err != nil {
			logger.Error("无法监听 unix socket", "path", *socketPath, "error", err)
			return 1
		}
	}

	if *addr != "" {
		httpServer.Addr = *addr
		go func() {
			logger.Info("YC-7ZIP 已启动",
				"version", version,
				"addr", *addr,
				"socket", *socketPath,
				"base_path", *basePath,
				"data", root,
				"auth", cfg.Auth != "",
				"allow_roots", len(cfg.AllowRoots),
			)
			if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
	} else if *socketPath == "" {
		logger.Error("既没有 -addr 也没有 -socket，没有可用的监听方式")
		return 2
	}

	select {
	case err := <-errCh:
		logger.Error("HTTP 服务异常退出", "error", err)
		return 1
	case <-ctx.Done():
		logger.Info("收到退出信号，正在关闭…")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("关闭超时", "error", err)
	}
	if *socketPath != "" {
		_ = os.Remove(*socketPath)
	}
	return 0
}

// serveUnixSocket makes the server also answer on a unix socket.
//
// The socket is created world-writable because the fnOS gateway runs as a
// different user than the package, and a stale socket left behind by a crashed
// previous run must be cleared first or the bind fails.
func serveUnixSocket(srv *http.Server, path string, logger *slog.Logger, errCh chan<- error) error {
	if st, err := os.Stat(path); err == nil && st.Mode()&os.ModeSocket != 0 {
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o666); err != nil {
		logger.Warn("无法修改 socket 权限", "path", path, "error", err)
	}
	go func() {
		logger.Info("已监听 unix socket", "path", path)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// parseSize accepts "4G", "500M", "1024", "0" and returns bytes.
func parseSize(v string) (int64, error) {
	v = strings.TrimSpace(strings.ToUpper(v))
	if v == "" || v == "0" {
		return 0, nil
	}
	multiplier := int64(1)
	switch v[len(v)-1] {
	case 'K':
		multiplier, v = 1<<10, v[:len(v)-1]
	case 'M':
		multiplier, v = 1<<20, v[:len(v)-1]
	case 'G':
		multiplier, v = 1<<30, v[:len(v)-1]
	case 'T':
		multiplier, v = 1<<40, v[:len(v)-1]
	case 'B':
		v = v[:len(v)-1]
	}
	var n int64
	if v == "" {
		return 0, fmt.Errorf("缺少数值")
	}
	for _, ch := range v {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("非法字符 %q", ch)
		}
		n = n*10 + int64(ch-'0')
	}
	return n * multiplier, nil
}
