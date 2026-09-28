package server

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// 自更新：下载新版本、校验、原子替换自身。
//
// 这个能力只在「裸二进制」部署下有意义——容器里换了文件，重启就会回到镜像里
// 那一份；fpk 部署由飞牛应用中心负责升级。所以先识别部署形态，叫不出来的
// 路径就不假装能做。
//
// 安全上有一条硬规则：**在通过结构校验之前，绝不执行上传来的文件**。
// 本地更新没有远端校验文件可比，唯一能信任的就是"它是一个本架构的 ELF
// 且自称的版本号合法"，所以结构校验必须先于任何 exec。

const (
	// updateMaxPackageBytes 限制下载/上传的发布包大小。
	updateMaxPackageBytes = 256 << 20
	// updateProbeTimeout 限制探测新版二进制的时间。
	updateProbeTimeout = 20 * time.Second
	// updateProbeOutputCap 限制探测时的输出量，避免被测程序刷爆内存。
	updateProbeOutputCap = 64 << 10
	// updateTarMemberCap 限制从发布包里解出的单个成员大小。
	updateTarMemberCap = 200 << 20
)

// errTooLarge 表示读到了容量上限。
var errTooLarge = errors.New("数据超过允许的大小上限")

// UpdateStatus 是给界面看的更新状态。
type UpdateStatus struct {
	Current   string `json:"current"`
	Channel   string `json:"channel"`
	RepoURL   string `json:"repo_url,omitempty"`
	Method    string `json:"method"` // binary | container | package
	Latest    string `json:"latest,omitempty"`
	HasUpdate bool   `json:"has_update"`

	Notes       string `json:"notes,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
	AssetName   string `json:"asset_name,omitempty"`

	// Source 说明这次结果来自哪条渠道，SourceURL 是那一条的具体地址。
	// 配了多条源时界面必须能说清"是谁答的"，否则用户没法判断该信哪条。
	Source    string `json:"source,omitempty"`
	SourceURL string `json:"source_url,omitempty"`
	// Sources 是本次逐条源的检查结果。失败的那几条也要报出来：只说"检查失败"
	// 会让人以为所有渠道都断了，而实际往往只是其中一条连不上。
	Sources []SourceOutcome `json:"sources,omitempty"`
	// SourceOptions 是配置好的渠道清单，界面用它渲染选择器。
	SourceOptions []SourceOption `json:"source_options,omitempty"`

	// CanSelfUpdate 为 false 时 Reason 说明为什么，以及该怎么做。
	CanSelfUpdate bool   `json:"can_self_update"`
	Reason        string `json:"reason,omitempty"`

	CheckedAt string `json:"checked_at,omitempty"`
	Error     string `json:"error,omitempty"`
}

// SourceOutcome 是一条更新源本次检查的结果。
type SourceOutcome struct {
	Kind    string `json:"kind"`
	URL     string `json:"url,omitempty"`
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

// SourceOption 是一条可选的更新渠道。
type SourceOption struct {
	Kind string `json:"kind"`
	URL  string `json:"url,omitempty"`
}

// stagedAsset 是自更新要下载的东西：包地址、校验方式、摘要。
type stagedAsset struct {
	url         string
	checksumURL string
	sha256      string
}

// updateState 缓存最近一次检查结果。
type updateState struct {
	mu        sync.Mutex
	status    UpdateStatus
	checkedAt time.Time
	// asset 只在锁内读写：以前 checksumURL 写在锁外，而且跨次检查不重置，
	// 会出现"用上一个版本的校验文件"这种怪事。
	asset stagedAsset
}

// applyMu 串行化安装。两个并发请求共用同一个暂存文件名会互相踩，
// 表现为其中一个莫名其妙地报"替换失败"。
var applyMu sync.Mutex

// deploymentMethod 判断当前是怎么部署的。
//
// 只看 /.dockerenv 不够：containerd / k8s / podman 都不建这个文件。
// 再补两条通行的判据，免得在容器里误判成裸二进制、让用户以为能自更新
// （容器里换了文件，重新调度就没了）。
func deploymentMethod() string {
	if os.Getenv("TRIM_APPDEST") != "" || os.Getenv("TRIM_APPNAME") != "" {
		return "package"
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "container"
	}
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return "container"
	}
	if raw, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		text := string(raw)
		for _, marker := range []string{"docker", "kubepods", "containerd", "libpod", "lxc"} {
			if strings.Contains(text, marker) {
				return "container"
			}
		}
	}
	return "binary"
}

// selfUpdateReason 返回不能自更新的原因；能自更新时返回空串。
func selfUpdateReason() string {
	switch deploymentMethod() {
	case "container":
		return "这是容器部署：替换容器内的文件会在重新调度后回到镜像里的版本。" +
			"请更新镜像标签后 docker compose pull && docker compose up -d"
	case "package":
		return "这是飞牛应用部署：升级由应用中心负责，请安装新版本的 .fpk"
	}
	self, err := os.Executable()
	if err != nil {
		return "无法确定自身路径：" + err.Error()
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return "无法解析自身路径：" + err.Error()
	}
	dir := filepath.Dir(self)
	probe := filepath.Join(dir, ".yc7zip-write-probe")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		return fmt.Sprintf("程序所在目录不可写（%s）：把程序换到有写权限的位置，或用包管理器升级", dir)
	}
	_ = os.Remove(probe)
	return ""
}

// fillDerived 补齐不参与缓存、每次都要现算的字段。
func (s *Server) fillDerived(status *UpdateStatus) {
	status.Current = s.cfg.Version
	status.Channel = s.cfg.Channel
	status.RepoURL = s.cfg.RepoURL
	status.Method = deploymentMethod()
	status.Reason = selfUpdateReason()
	status.CanSelfUpdate = status.Reason == ""

	// 渠道清单每次都现算：它来自启动参数，不是某次检查的产物。
	// 缓存的 status 里带上它，界面就不必为了渲染选择器再取一次。
	status.SourceOptions = nil
	for _, src := range s.sources {
		status.SourceOptions = append(status.SourceOptions, SourceOption{Kind: src.kind, URL: src.displayURL()})
	}
}

// handleUpdateStatus 返回缓存的更新状态，不触网，界面可以随时调。
func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	s.updates.mu.Lock()
	status := s.updates.status
	checked := s.updates.checkedAt
	s.updates.mu.Unlock()

	s.fillDerived(&status)
	if !checked.IsZero() {
		status.CheckedAt = checked.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, status)
}

// releaseInfo 是 GitHub Releases API 里我们用得到的字段。
type releaseInfo struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	Prerelease  bool   `json:"prerelease"`
	Draft       bool   `json:"draft"`
	PublishedAt string `json:"published_at"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

// defaultUpdateSources 是没显式配置时的渠道顺序。
//
// 自建源放在前面：它是我们自己发布的那份产物（飞牛 NAS 上的 lucky 文件服务），
// 在受限网络里比 api.github.com 可靠得多。GitHub 留在后面兜底——仓库被封、
// 自建源没同步、或者用户就想从官方渠道拿，都还有一条路。多源是为了可用性，
// 不是放宽校验：每条路的包都要过 SHA256。
var defaultUpdateSources = []string{"https://yc7zip.202693.xyz/", "github"}

const (
	sourceSelf   = "self"
	sourceGitHub = "github"
	// selfManifestName 是自建源目录里的清单文件名，由发布脚本写入。
	selfManifestName = "update.json"
	// selfChecksumName 是自建源目录里的校验文件，清单里没写摘要时回退到它。
	selfChecksumName = "SHA256SUMS.txt"
)

// updateSource 是一条更新渠道。
//
// 两种渠道的产物形态本来就不同：自建源是一个静态目录（读 update.json），
// GitHub 是 Releases API。所以按渠道分别取值，而不是硬凑一个中间格式——
// 后者要维护一层转换，还会把 GitHub 的 tag / prerelease 语义压平丢掉。
type updateSource struct {
	kind string
	base string // self：目录基址（末尾无斜杠）；github：可选的 owner/repo 覆盖
}

func (u updateSource) displayURL() string {
	if u.kind == sourceGitHub {
		if u.base != "" {
			return "https://github.com/" + u.base
		}
		return ""
	}
	return u.base
}

// label 是拼错误文案时用的渠道名。界面上的渠道名由前端按语言渲染，
// 但错误串是服务端拼的，这里只用中文。
func (u updateSource) label() string {
	if u.kind == sourceGitHub {
		return "GitHub"
	}
	return "自建源（" + u.base + "）"
}

// parseUpdateSources 把 -update-sources 的取值解析成渠道列表，顺序即尝试顺序。
//
// 每项要么是 github / github:owner/repo，要么是一个自建源基址。
// 认不出的项直接报错而不是跳过：静默忽略会让"换了源却没生效"变成一件
// 只能靠翻日志才发现的事。
func parseUpdateSources(items []string) ([]updateSource, error) {
	var out []updateSource
	for _, raw := range items {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		switch {
		case item == sourceGitHub:
			out = append(out, updateSource{kind: sourceGitHub})
		case strings.HasPrefix(item, sourceGitHub+":"):
			repo := strings.Trim(strings.TrimSpace(strings.TrimPrefix(item, sourceGitHub+":")), "/")
			if !strings.Contains(repo, "/") {
				return nil, fmt.Errorf("GitHub 源要写成 github:owner/repo，收到 %q", item)
			}
			out = append(out, updateSource{kind: sourceGitHub, base: repo})
		case strings.Contains(item, "://"):
			base := strings.TrimSuffix(item, "/")
			parsed, err := url.Parse(base)
			if err != nil || parsed.Host == "" {
				return nil, fmt.Errorf("自建源地址无效：%q", item)
			}
			// 只认 http(s)：别的协议到 fetch 那一步才会以一句不知所云的
			// 传输错误失败，不如在配置阶段就说清楚。
			if parsed.Scheme != "http" && parsed.Scheme != "https" {
				return nil, fmt.Errorf("自建源只支持 http(s) 地址：%q", item)
			}
			out = append(out, updateSource{kind: sourceSelf, base: base})
		default:
			return nil, fmt.Errorf(
				"无法识别的更新源 %q：应填 github、github:owner/repo，或一个 http(s) 目录地址", item)
		}
	}
	return out, nil
}

// updateManifest 是自建源目录里的 update.json，由发布脚本写入。
//
// 之所以要这个文件而不是让客户端去拼：VERSION + CHANGELOG.md + 列举目录
// 三样凑出来的信息既不够（拿不到包摘要）也不稳（文件名规则散在客户端里，
// 改一次命名就要跟着升级解析）。一个文件把该说的都说全。
type updateManifest struct {
	Version string        `json:"version"`
	Channel string        `json:"channel"`
	Date    string        `json:"date"`
	Notes   string        `json:"notes"`
	Assets  []updateAsset `json:"assets"`
}

// updateAsset 是自建源里的一个产物。
type updateAsset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// checkOne 按渠道取一次检查结果。
func (s *Server) checkOne(ctx context.Context, src updateSource) (checkedSource, error) {
	if src.kind == sourceGitHub {
		return s.checkGitHub(ctx, src)
	}
	return s.checkSelfHosted(ctx, src)
}

// checkSelfHosted 读自建源目录里的 update.json。
func (s *Server) checkSelfHosted(ctx context.Context, src updateSource) (checkedSource, error) {
	var out checkedSource
	endpoint := src.base + "/" + selfManifestName

	body, err := s.fetch(ctx, endpoint, "application/json")
	if err != nil {
		return out, err
	}
	defer body.Close()

	var manifest updateManifest
	if err := json.NewDecoder(io.LimitReader(body, 4<<20)).Decode(&manifest); err != nil {
		return out, fmt.Errorf("解析 %s 失败：%w", endpoint, err)
	}

	version := strings.TrimSpace(manifest.Version)
	if !isVersionTag(version) {
		return out, fmt.Errorf("自建源报出的版本 %q 不是本项目的版本号格式", version)
	}
	// 通道不符不算故障，只是"这条源上没有我要的版本"。
	if manifest.Channel != "" && s.cfg.Channel != "test" && manifest.Channel != s.cfg.Channel {
		return out, fmt.Errorf("自建源上只有「%s」通道的 %s", manifest.Channel, version)
	}

	want := fmt.Sprintf("yc-7zip-linux-%s.tar.gz", runtime.GOARCH)
	for _, asset := range manifest.Assets {
		if asset.Name != want {
			continue
		}
		out.status.Latest = version
		out.status.HasUpdate = version > s.cfg.Version
		out.status.Notes = manifest.Notes
		out.status.PublishedAt = manifest.Date
		out.status.AssetName = asset.Name
		out.status.DownloadURL = src.base + "/" + asset.Name
		out.asset = stagedAsset{url: out.status.DownloadURL, sha256: asset.SHA256}
		if asset.SHA256 == "" {
			// 老清单没有摘要时的退路，和 GitHub 那条一样走 SHA256SUMS.txt
			out.asset.checksumURL = src.base + "/" + selfChecksumName
		}
		return out, nil
	}
	return out, fmt.Errorf("自建源上没有 %s 架构的包（%s）", runtime.GOARCH, want)
}

// checkGitHub 走 Releases API。
func (s *Server) checkGitHub(ctx context.Context, src updateSource) (checkedSource, error) {
	var out checkedSource

	repoPath := src.base
	if repoPath == "" {
		repoPath = repoAPI(s.cfg.RepoURL)
	}
	if repoPath == "" {
		return out, fmt.Errorf("没有配置仓库地址，无法从 GitHub 检查更新")
	}

	endpoint := fmt.Sprintf("%s/repos/%s/releases?per_page=20", s.githubAPI, repoPath)
	body, err := s.fetch(ctx, endpoint, "application/vnd.github+json")
	if err != nil {
		return out, err
	}
	defer body.Close()

	var releases []releaseInfo
	if err := json.NewDecoder(io.LimitReader(body, 4<<20)).Decode(&releases); err != nil {
		return out, fmt.Errorf("解析发布列表失败：%w", err)
	}

	channel := s.cfg.Channel
	best := releaseInfo{}
	bestTag := ""
	for _, rel := range releases {
		if rel.Draft {
			continue
		}
		// 正式通道只认正式版；测试通道两者都认
		if channel != "test" && rel.Prerelease {
			continue
		}
		tag := strings.TrimSpace(rel.TagName)
		if !isVersionTag(tag) {
			continue
		}
		if tag > bestTag {
			bestTag, best = tag, rel
		}
	}

	if bestTag == "" {
		return out, fmt.Errorf("没有找到适用于「%s」通道的发布", channel)
	}

	out.status.Latest = bestTag
	out.status.HasUpdate = bestTag > s.cfg.Version
	out.status.Notes = best.Body
	out.status.PublishedAt = best.PublishedAt

	// 找当前架构的包与校验文件
	want := fmt.Sprintf("yc-7zip-%s-linux-%s.tar.gz", bestTag, runtime.GOARCH)
	for _, asset := range best.Assets {
		switch asset.Name {
		case want:
			out.status.AssetName = asset.Name
			out.status.DownloadURL = asset.BrowserDownloadURL
		case "SHA256SUMS.txt":
			out.asset.checksumURL = asset.BrowserDownloadURL
		}
	}
	if out.status.DownloadURL == "" {
		return out, fmt.Errorf("该发布里没有 %s 架构的包", runtime.GOARCH)
	}
	out.asset.url = out.status.DownloadURL
	return out, nil
}

// checkedSource 是一条源给出的检查结果：给界面看的 status，加上自更新要用的地址。
type checkedSource struct {
	status UpdateStatus
	asset  stagedAsset
}

// handleUpdateCheck 主动查询最新版本。
//
// source 查询参数可以把这次检查限定在某一条渠道上（self / github），
// 界面上的选择器就用它。留空或 auto 表示按配置顺序问所有源。
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	status, err := s.checkForUpdate(ctx, strings.TrimSpace(r.URL.Query().Get("source")))
	if err != nil {
		status.Error = err.Error()
		// 网络不通不算服务端错误，交给界面提示即可
		writeJSON(w, http.StatusOK, status)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// checkForUpdate 问每一条源，取其中最新的那个版本。
//
// 并发问而不是串行：自建源通常秒回，api.github.com 在受限网络里要等几十秒
// 才超时，串行等于让最慢的那条拖住整个结果。多源是为了可用性，
// 但也不该因此把一次"检查更新"变成一次长等待。
//
// 版本号是 zip<YYMM>.<NNN>，位数固定且补零，所以字符串比较就是版本比较——
// 不需要解析，也就不会在解析上出错。
func (s *Server) checkForUpdate(ctx context.Context, only string) (UpdateStatus, error) {
	status := UpdateStatus{}
	s.fillDerived(&status)

	sources := s.sources
	if only != "" && only != "auto" {
		var kept []updateSource
		for _, src := range sources {
			if src.kind == only {
				kept = append(kept, src)
			}
		}
		if len(kept) == 0 {
			return status, fmt.Errorf("没有名为 %q 的更新源", only)
		}
		sources = kept
	}

	type answer struct {
		src updateSource
		got checkedSource
		err error
	}
	answers := make([]answer, len(sources))
	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Add(1)
		go func(i int, src updateSource) {
			defer wg.Done()
			got, err := s.checkOne(ctx, src)
			answers[i] = answer{src: src, got: got, err: err}
		}(i, src)
	}
	wg.Wait()

	// 逐条记录结果，顺序与配置一致，界面照着渲染即可
	best := -1
	var failures []string
	for i, a := range answers {
		outcome := SourceOutcome{Kind: a.src.kind, URL: a.src.displayURL()}
		if a.err != nil {
			outcome.Error = a.err.Error()
			failures = append(failures, a.src.label()+"："+a.err.Error())
		} else {
			outcome.OK = true
			outcome.Version = a.got.status.Latest
			// 多源之间会互相落后：取版本最高的那条，而不是"第一条能答的"。
			// 否则一条还没同步完的源会把新版盖掉，看起来像"检查不到更新"。
			if best < 0 || a.got.status.Latest > answers[best].got.status.Latest {
				best = i
			}
		}
		status.Sources = append(status.Sources, outcome)
	}

	if best < 0 {
		err := fmt.Errorf("所有更新源都不可用：%s", strings.Join(failures, "；"))
		s.storeCheck(&status, stagedAsset{})
		return status, err
	}

	winner := answers[best]
	status.Latest = winner.got.status.Latest
	status.HasUpdate = winner.got.status.Latest > s.cfg.Version
	status.Notes = winner.got.status.Notes
	status.PublishedAt = winner.got.status.PublishedAt
	status.AssetName = winner.got.status.AssetName
	status.DownloadURL = winner.got.status.DownloadURL
	status.Source = winner.src.kind
	status.SourceURL = winner.src.displayURL()

	// 每次检查都重新记录，不留上一次的地址
	s.storeCheck(&status, winner.got.asset)
	return status, nil
}

func (s *Server) storeCheck(status *UpdateStatus, asset stagedAsset) {
	s.updates.mu.Lock()
	defer s.updates.mu.Unlock()
	s.updates.status = *status
	s.updates.checkedAt = time.Now()
	s.updates.asset = asset
}

// repoAPI 从仓库地址里取出 owner/name。
func repoAPI(repoURL string) string {
	trimmed := strings.TrimSuffix(strings.TrimSpace(repoURL), ".git")
	trimmed = strings.TrimSuffix(trimmed, "/")
	if i := strings.Index(trimmed, "github.com/"); i >= 0 {
		rest := trimmed[i+len("github.com/"):]
		parts := strings.Split(rest, "/")
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1]
		}
	}
	return ""
}

// isVersionTag 判断标签是不是我们的版本号格式（zip + 4 数字 + . + 3 数字）。
// 位数固定且补零，所以字典序等于数值序。
func isVersionTag(tag string) bool {
	if len(tag) != len("zip2609.001") || !strings.HasPrefix(tag, "zip") {
		return false
	}
	for i := 0; i < len(tag); i++ {
		ch := tag[i]
		switch {
		case i < 3:
			continue
		case i < 7:
			if ch < '0' || ch > '9' {
				return false
			}
		case i == 7:
			if ch != '.' {
				return false
			}
		default:
			if ch < '0' || ch > '9' {
				return false
			}
		}
	}
	return true
}

// fetch 发一个 GET，按需走代理。GitHub 在部分网络下直连不通，所以代理要能配。
func (s *Server) fetch(ctx context.Context, endpoint, accept string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "yc7zip/"+s.cfg.Version)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	if s.cfg.Proxy != "" {
		proxyURL, err := url.Parse(s.cfg.Proxy)
		if err != nil {
			return nil, fmt.Errorf("代理地址无效：%w", err)
		}
		client.Transport = &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败（若在受限网络，请用 -proxy 指定代理）：%w", err)
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("接口返回 %d", res.StatusCode)
	}
	return res.Body, nil
}

// handleUpdateApply 执行在线更新：下载 -> 校验 -> 替换 -> 原地重启。
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if reason := selfUpdateReason(); reason != "" {
		writeError(w, http.StatusConflict, reason)
		return
	}

	s.updates.mu.Lock()
	staged := s.updates.asset
	latest := s.updates.status.Latest
	s.updates.mu.Unlock()

	if staged.url == "" {
		writeError(w, http.StatusBadRequest, "还没有检查过更新，请先检查")
		return
	}
	if !isVersionTag(latest) || latest <= s.cfg.Version {
		writeError(w, http.StatusBadRequest, "没有比当前更新的版本")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	result, err := s.installFromURL(ctx, staged, latest)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
	s.scheduleRestart()
}

// handleUpdateUpload 用本地文件更新。
//
// 只接受发布包（.tar.gz），不接受裸二进制：本地更新没有远端校验文件可比，
// 唯一能信任的是"它是本架构的 ELF 且自称版本更高"。裸二进制少一层"这确实是
// 我们发布的包"的结构约束，没有必要为省一步而放宽。
func (s *Server) handleUpdateUpload(w http.ResponseWriter, r *http.Request) {
	if reason := selfUpdateReason(); reason != "" {
		writeError(w, http.StatusConflict, reason)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, updateMaxPackageBytes+(1<<20))
	file, header, err := r.FormFile("package")
	if err != nil {
		writeError(w, http.StatusBadRequest, "请用 package 字段上传发布包："+err.Error())
		return
	}
	defer file.Close()

	name := filepath.Base(header.Filename)
	if !strings.HasSuffix(name, ".tar.gz") && !strings.HasSuffix(name, ".tgz") {
		writeError(w, http.StatusBadRequest,
			"本地更新只接受 .tar.gz 发布包（Release 里的 yc-7zip-*-linux-*.tar.gz）")
		return
	}

	temp, err := os.CreateTemp("", "yc7zip-upload-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.Remove(temp.Name())
	defer temp.Close()

	written, err := io.Copy(temp, io.LimitReader(file, updateMaxPackageBytes+1))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "接收文件失败："+err.Error())
		return
	}
	if written > updateMaxPackageBytes {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("发布包超过 %d MB", updateMaxPackageBytes>>20))
		return
	}

	// wantVersion 留空：版本以包内二进制自报的为准，但必须格式合法且高于当前
	result, err := s.installFromFile(temp.Name(), name, "")
	if err != nil {
		writeEngineError(w, err)
		return
	}
	result["received_bytes"] = written
	writeJSON(w, http.StatusOK, result)
	s.scheduleRestart()
}

// installResult 是安装结果，返回给界面显示。
type installResult map[string]any

// installFromURL 下载发布包并安装。
//
// 在线更新**必须有校验来源**：这条路上的包是从网上拿的，没有校验就等于把
// "替换本机可执行文件"这件事交给任何一个能应答那个地址的东西。宁可装不上。
// 本地更新不走这里——它没有远端摘要可比，改用 ELF 结构 + 版本号约束。
//
// 摘要值的可信度取决于它从哪来：GitHub 那条走 TLS，摘要与包来自同一个已认证
// 来源；自建源若配成明文 http，摘要和包可以一起被改写，校验就只剩"防传输损坏"
// 的作用。所以自建源应当用 https（启动时会对 http 源告警）。
func (s *Server) installFromURL(ctx context.Context, staged stagedAsset, wantVersion string) (installResult, error) {
	body, err := s.fetch(ctx, staged.url, "application/octet-stream")
	if err != nil {
		return nil, err
	}
	defer body.Close()

	temp, err := os.CreateTemp("", "yc7zip-download-*.tar.gz")
	if err != nil {
		return nil, err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()

	written, err := io.Copy(temp, &cappedReader{
		r:         body,
		remaining: updateMaxPackageBytes,
		what:      "发布包",
	})
	if err != nil {
		return nil, fmt.Errorf("下载失败：%w", err)
	}

	name := filepath.Base(staged.url)
	expected := staged.sha256
	if expected == "" && staged.checksumURL != "" {
		sum, err := s.expectedChecksum(ctx, staged.checksumURL, name)
		if err != nil {
			return nil, err
		}
		expected = sum
	}
	if expected == "" {
		return nil, fmt.Errorf("该更新源没有提供 %s 的校验值，拒绝安装", name)
	}

	actual, err := fileSHA256(temp.Name())
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(expected, actual) {
		return nil, fmt.Errorf("SHA256 校验不通过：期望 %s，实际 %s", expected, actual)
	}

	result, err := s.installFromFile(temp.Name(), name, wantVersion)
	if err != nil {
		return nil, err
	}
	result["downloaded_bytes"] = written
	result["sha256"] = actual
	return result, nil
}

// expectedChecksum 从 SHA256SUMS.txt 里取指定文件的摘要。
func (s *Server) expectedChecksum(ctx context.Context, checksumURL, name string) (string, error) {
	body, err := s.fetch(ctx, checksumURL, "text/plain")
	if err != nil {
		return "", fmt.Errorf("取校验文件失败：%w", err)
	}
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "./") == name {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("校验文件里没有 %s", name)
}

// installFromFile 校验并替换自身。
//
// 顺序刻意设计成"越晚破坏越好"，而且**在结构校验通过之前绝不执行文件**：
//  1. 解到唯一的临时文件（不执行）
//  2. 结构校验：ELF + 架构匹配
//  3. 才允许执行它问版本，并限制时间与输出量
//  4. 版本必须是合法格式且严格高于当前
//  5. 设好执行位、备份旧的、原子改名
//
// 任何一步失败都不会动到正在运行的程序。
func (s *Server) installFromFile(pkgPath, name, wantVersion string) (installResult, error) {
	applyMu.Lock()
	defer applyMu.Unlock()

	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(self)

	// 每个请求用独立的暂存名：共用一个名字时，两个并发安装会互相删掉对方
	// 刚 rename 走的文件，表现为其中一个报"替换失败"。
	stagedFile, err := os.CreateTemp(dir, ".yc7zip-new-*")
	if err != nil {
		return nil, fmt.Errorf("无法在程序目录创建临时文件：%w", err)
	}
	staged := stagedFile.Name()
	stagedFile.Close()
	defer os.Remove(staged)

	if err := extractBinary(pkgPath, name, staged); err != nil {
		return nil, err
	}

	// 结构校验：先确认它是个本架构的 ELF，再考虑执行
	if err := checkELFArch(staged); err != nil {
		return nil, err
	}

	reported, err := probeVersion(staged)
	if err != nil {
		return nil, err
	}
	if !isVersionTag(reported) {
		return nil, fmt.Errorf("包内程序报出的版本 %q 不是本项目的版本号格式", reported)
	}
	if wantVersion != "" && reported != wantVersion {
		return nil, fmt.Errorf("包内程序版本 %q 与发布标签 %q 不符", reported, wantVersion)
	}
	if reported <= s.cfg.Version {
		return nil, fmt.Errorf("不更新：%s 不高于当前版本 %s", reported, s.cfg.Version)
	}

	// 执行位在改名之前设好：改名后再 chmod 也会失败的话，
	// 就已经把程序换成了一份不可执行的，那才是真的把服务弄坏。
	if err := os.Chmod(staged, 0o755); err != nil {
		return nil, fmt.Errorf("设置执行位失败：%w", err)
	}

	backup := self + ".bak"
	if err := copyFile(self, backup); err != nil {
		return nil, fmt.Errorf("备份当前程序失败：%w", err)
	}

	// 同一目录内 rename，原子
	if err := os.Rename(staged, self); err != nil {
		return nil, fmt.Errorf("替换失败：%w", err)
	}

	return installResult{
		"installed": reported,
		"previous":  s.cfg.Version,
		"backup":    backup,
		"path":      self,
	}, nil
}

// checkELFArch 确认文件是本机架构的 Linux 可执行文件。
//
// 这一步不执行文件，纯读头部。它挡掉"架构不对""根本不是可执行文件"
// 这两类，也让"上传一个脚本来冒充"在触发 exec 之前就被拒。
func checkELFArch(path string) error {
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("不是有效的 Linux 可执行文件（ELF）：%w", err)
	}
	defer f.Close()

	if f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN {
		return fmt.Errorf("ELF 类型不是可执行文件：%s", f.Type)
	}
	want, ok := map[string]elf.Machine{
		"amd64": elf.EM_X86_64,
		"arm64": elf.EM_AARCH64,
		"arm":   elf.EM_ARM,
		"386":   elf.EM_386,
	}[runtime.GOARCH]
	if !ok {
		return nil
	}
	if f.Machine != want {
		return fmt.Errorf("架构不匹配：包内是 %s，本机是 %s", f.Machine, want)
	}
	return nil
}

// extractBinary 从发布包里取出 yc7zip，写到 dest。
func extractBinary(pkgPath, name, dest string) error {
	if !strings.HasSuffix(name, ".tar.gz") && !strings.HasSuffix(name, ".tgz") {
		return fmt.Errorf("只接受 .tar.gz 发布包，收到 %q", name)
	}

	f, err := os.Open(pkgPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("不是有效的 gzip 包：%w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("读取归档失败：%w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// 只认 basename，天然挡掉 ../ 这类路径穿越
		if filepath.Base(hdr.Name) != "yc7zip" {
			continue
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		// 用会报错的限量读，而不是 LimitReader：后者到上限只返回 EOF，
		// 于是超限的成员会被静默截断，然后拿去执行。
		_, err = io.Copy(out, &cappedReader{r: tr, remaining: updateTarMemberCap, what: "包内成员"})
		if cerr := out.Close(); cerr != nil && err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("发布包里没有 yc7zip")
}

// probeVersion 执行一个二进制问它的版本。只应在结构校验之后调用。
func probeVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), updateProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "-version")
	stdout, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("包内程序执行失败（退出码 %d）：%s",
				exit.ExitCode(), firstLineOf(string(exit.Stderr)))
		}
		return "", fmt.Errorf("无法执行包内程序：%w", err)
	}
	if len(stdout) > updateProbeOutputCap {
		stdout = stdout[:updateProbeOutputCap]
	}
	fields := strings.Fields(strings.TrimSpace(string(stdout)))
	if len(fields) == 0 {
		return "", fmt.Errorf("包内程序没有输出版本号")
	}
	return fields[len(fields)-1], nil
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return strings.TrimSpace(s)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// cappedReader 读满上限后返回错误，而不是像 LimitReader 那样静默 EOF。
// "超限就截断" 在这里是危险的：截断的文件接着会被当成新版本执行。
type cappedReader struct {
	r         io.Reader
	remaining int64
	what      string
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, fmt.Errorf("%s超过 %d MB：%w", c.what, updateMaxPackageBytes>>20, errTooLarge)
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.r.Read(p)
	c.remaining -= int64(n)
	return n, err
}

// scheduleRestart 换上新版本并原地重启。
//
// 不能只 os.Exit：裸二进制部署由 deploy/start.sh 用 setsid --fork 拉起，
// 没有 supervisor 会把它重新拉起来，退出等于服务停摆。
// exec 换掉进程映像则 PID 不变，重新监听后即可继续服务。
func (s *Server) scheduleRestart() {
	go func() {
		time.Sleep(1200 * time.Millisecond)

		if s.restart != nil {
			s.restart()
			return
		}
		self, err := os.Executable()
		if err == nil {
			self, err = filepath.EvalSymlinks(self)
		}
		if err == nil {
			s.log.Warn("更新完成，原地重启到新版本", "path", self)
			if err := reexec(self); err == nil {
				return
			} else {
				s.log.Error("原地重启失败，交由外部重启", "error", err)
			}
		}
		s.log.Warn("进程即将退出，需要外部重新启动")
		os.Exit(0)
	}()
}
