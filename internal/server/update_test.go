package server

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ycyingchen/yc-7zip/internal/engine"
	"github.com/ycyingchen/yc-7zip/internal/job"
)

// 更新检查只涉及网络与文件，不碰 7-Zip。所以这里另起一个不依赖 7-Zip 的
// 服务端，而不是复用 newHarness——否则本机没装 7-Zip 时整块会被跳过，
// 而这些恰恰是"装不装 7-Zip 都应该正确"的逻辑。
func newUpdateServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	root := t.TempDir()
	jobs, err := job.NewManager(filepath.Join(root, "jobs"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version == "" {
		cfg.Version = "zip2609.002"
	}
	if cfg.Channel == "" {
		cfg.Channel = "test"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = root
	}
	return New(cfg, &engine.Engine{}, jobs, os.DirFS("../.."))
}

// 两个渠道的包名规则本来就不同：自建源目录里用的是不带版本号的稳定名
// （下载页链接不会随版本失效），而 Release 附件带版本号。
func archPackageName() string {
	return fmt.Sprintf("yc-7zip-linux-%s.tar.gz", runtime.GOARCH)
}

func githubPackageName(tag string) string {
	return fmt.Sprintf("yc-7zip-%s-linux-%s.tar.gz", tag, runtime.GOARCH)
}

// staticServer 起一个只按路径返回固定内容的小服务，用来冒充自建源。
func staticServer(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for name, body := range files {
		content := body
		mux.HandleFunc("/"+name, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, content)
		})
	}
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// failingServer 起一个一律 500 的服务，用来冒充坏掉的源。
func failingServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// manifestFor 造一份自建源清单，只带当前架构的那个包。
func manifestFor(t *testing.T, version, channel, sha string) string {
	t.Helper()
	raw, err := json.Marshal(updateManifest{
		Version: version,
		Channel: channel,
		Date:    "2026-09-30",
		Notes:   "更新说明",
		Assets:  []updateAsset{{Name: archPackageName(), Size: 12, SHA256: sha}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// githubServer 假造 Releases API。
//
// 用原始 JSON 而不是构造 releaseInfo：结构体标签写错时构造出来的东西照样
// 能编过，只有真过一遍解码才能发现字段名对不上。
func githubServer(t *testing.T, releases string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, releases)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// releaseJSON 造一条 Releases API 的返回。
func releaseJSON(tag, body string, assets map[string]string) string {
	type asset struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	}
	items := make([]asset, 0, len(assets))
	for name, url := range assets {
		items = append(items, asset{Name: name, URL: url})
	}
	raw, err := json.Marshal([]map[string]any{{
		"tag_name":     tag,
		"body":         body,
		"published_at": "2026-09-30T01:02:03Z",
		"prerelease":   false,
		"assets":       items,
	}})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func TestParseUpdateSources(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		want    []updateSource
		wantErr bool
	}{
		{name: "github", in: []string{"github"}, want: []updateSource{{kind: sourceGitHub}}},
		{name: "github 带仓库", in: []string{"github:YCyingchen/YC-7ZIP"},
			want: []updateSource{{kind: sourceGitHub, base: "YCyingchen/YC-7ZIP"}}},
		{name: "自建源去掉末尾斜杠", in: []string{"https://yc7zip.202693.xyz/"},
			want: []updateSource{{kind: sourceSelf, base: "https://yc7zip.202693.xyz"}}},
		{name: "顺序即优先级", in: []string{"https://a.example/", "github"},
			want: []updateSource{
				{kind: sourceSelf, base: "https://a.example"},
				{kind: sourceGitHub},
			}},
		{name: "空项被忽略", in: []string{"", "   ", "github"},
			want: []updateSource{{kind: sourceGitHub}}},
		{name: "github 缺仓库名", in: []string{"github:noslash"}, wantErr: true},
		{name: "非 http(s) 协议", in: []string{"ftp://a.example/x"}, wantErr: true},
		{name: "认不出的写法", in: []string{"yc7zip.202693.xyz"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseUpdateSources(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("该报错却通过了：%+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("不该报错：%v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("解析结果不对：得到 %+v，期望 %+v", got, tc.want)
			}
		})
	}
}

func TestSelfHostedSourceFindsUpdate(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	ts := staticServer(t, map[string]string{"update.json": manifestFor(t, "zip2609.005", "test", sha)})
	srv := newUpdateServer(t, Config{UpdateSources: []string{ts.URL + "/"}})

	status, err := srv.checkForUpdate(context.Background(), "")
	if err != nil {
		t.Fatalf("检查失败：%v", err)
	}
	if !status.HasUpdate || status.Latest != "zip2609.005" {
		t.Fatalf("应发现 zip2609.005：latest=%q has_update=%v", status.Latest, status.HasUpdate)
	}
	if status.Source != sourceSelf {
		t.Fatalf("结果应来自自建源，实际是 %q", status.Source)
	}
	if want := ts.URL + "/" + archPackageName(); status.DownloadURL != want {
		t.Fatalf("下载地址不对：%q，期望 %q", status.DownloadURL, want)
	}
	if status.Notes != "更新说明" {
		t.Fatalf("更新说明没带出来：%q", status.Notes)
	}

	// 摘要要一路带到安装那一步，否则"有校验"只是纸面上的
	srv.updates.mu.Lock()
	staged := srv.updates.asset
	srv.updates.mu.Unlock()
	if staged.sha256 != sha {
		t.Fatalf("暂存的摘要不对：%q", staged.sha256)
	}
	if staged.url != status.DownloadURL {
		t.Fatalf("暂存的包地址不对：%q", staged.url)
	}
}

func TestSelfHostedSourceWithoutChecksumFallsBackToSumsFile(t *testing.T) {
	ts := staticServer(t, map[string]string{"update.json": manifestFor(t, "zip2609.005", "test", "")})
	srv := newUpdateServer(t, Config{UpdateSources: []string{ts.URL + "/"}})

	if _, err := srv.checkForUpdate(context.Background(), ""); err != nil {
		t.Fatalf("检查失败：%v", err)
	}
	srv.updates.mu.Lock()
	staged := srv.updates.asset
	srv.updates.mu.Unlock()

	want := ts.URL + "/" + selfChecksumName
	if staged.checksumURL != want {
		t.Fatalf("清单没有摘要时应回退到校验文件 %q，实际 %q", want, staged.checksumURL)
	}
}

func TestExpectedChecksumReadsSumsFile(t *testing.T) {
	name := archPackageName()
	sum := strings.Repeat("cd", 32)
	// sha256sum 写出来的路径带 "./" 前缀，这是真实形态，要能认
	ts := staticServer(t, map[string]string{
		"SHA256SUMS.txt": sum + "  ./" + name + "\n" + strings.Repeat("ef", 32) + "  ./other.tar.gz\n",
	})
	srv := newUpdateServer(t, Config{})

	got, err := srv.expectedChecksum(context.Background(), ts.URL+"/"+selfChecksumName, name)
	if err != nil {
		t.Fatalf("读校验文件失败：%v", err)
	}
	if got != sum {
		t.Fatalf("取到的摘要不对：%q，期望 %q", got, sum)
	}
}

func TestGitHubSourceFindsUpdate(t *testing.T) {
	name := githubPackageName("zip2609.004")
	releases := releaseJSON("zip2609.004", "来自 GitHub 的说明", map[string]string{
		name:             "https://example.invalid/" + name,
		"SHA256SUMS.txt": "https://example.invalid/SHA256SUMS.txt",
	})
	ts := githubServer(t, releases)
	srv := newUpdateServer(t, Config{
		RepoURL:       "https://github.com/YCyingchen/YC-7ZIP",
		UpdateSources: []string{"github"},
	})
	srv.githubAPI = ts.URL

	status, err := srv.checkForUpdate(context.Background(), "")
	if err != nil {
		t.Fatalf("检查失败：%v", err)
	}
	if status.Source != sourceGitHub {
		t.Fatalf("结果应来自 GitHub，实际是 %q", status.Source)
	}
	if status.Latest != "zip2609.004" || !status.HasUpdate {
		t.Fatalf("版本判定不对：latest=%q has_update=%v", status.Latest, status.HasUpdate)
	}
	if status.Notes != "来自 GitHub 的说明" {
		t.Fatalf("发布说明没带出来：%q", status.Notes)
	}

	srv.updates.mu.Lock()
	staged := srv.updates.asset
	srv.updates.mu.Unlock()
	if staged.checksumURL != "https://example.invalid/SHA256SUMS.txt" {
		t.Fatalf("校验文件地址不对：%q", staged.checksumURL)
	}
}

// 发布里没有校验文件时必须拒绝安装。
// 这条不是假想：在线更新的包是要替换本机可执行文件的，没有校验就等于
// 把这件事交给任何一个能应答那个地址的东西。
func TestOnlineUpdateWithoutChecksumIsRefused(t *testing.T) {
	name := githubPackageName("zip2609.004")
	ts := githubServer(t, releaseJSON("zip2609.004", "", map[string]string{name: "https://example.invalid/" + name}))
	pkg := staticServer(t, map[string]string{name: "not really a package"})

	srv := newUpdateServer(t, Config{
		RepoURL:       "https://github.com/YCyingchen/YC-7ZIP",
		UpdateSources: []string{"github"},
	})
	srv.githubAPI = ts.URL
	if _, err := srv.checkForUpdate(context.Background(), ""); err != nil {
		t.Fatalf("检查失败：%v", err)
	}

	srv.updates.mu.Lock()
	staged := srv.updates.asset
	srv.updates.mu.Unlock()
	staged.url = pkg.URL + "/" + name

	if _, err := srv.installFromURL(context.Background(), staged, "zip2609.004"); err == nil {
		t.Fatal("没有校验值却装上了")
	} else if !strings.Contains(err.Error(), "校验值") {
		t.Fatalf("报错应说明缺校验值，实际：%v", err)
	}
}

// 摘要对得上时应当走过校验这一步。这里故意给一个不是发布包的文件，
// 断言它是因为"不是 gzip 包"而失败——如果卡在别处，说明校验路径本身有问题，
// 而"校验永远不过"会让人把防护关掉。
func TestOnlineUpdateAcceptsMatchingChecksum(t *testing.T) {
	const payload = "still not a tar.gz"
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))

	pkg := staticServer(t, map[string]string{archPackageName(): payload})
	ts := staticServer(t, map[string]string{"update.json": manifestFor(t, "zip2609.005", "test", sum)})

	srv := newUpdateServer(t, Config{UpdateSources: []string{ts.URL + "/"}})
	if _, err := srv.checkForUpdate(context.Background(), ""); err != nil {
		t.Fatalf("检查失败：%v", err)
	}

	srv.updates.mu.Lock()
	staged := srv.updates.asset
	srv.updates.mu.Unlock()
	staged.url = pkg.URL + "/" + archPackageName()

	_, err := srv.installFromURL(context.Background(), staged, "zip2609.005")
	if err == nil {
		t.Fatal("一个不是发布包的东西被装上了")
	}
	if !strings.Contains(err.Error(), "gzip") {
		t.Fatalf("应当是卡在解包而不是校验：%v", err)
	}
}

// writeTarGz 打一个只含单个成员的 tar.gz。
func writeTarGz(t *testing.T, path, member, src string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: member, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

// 解包之后、探测版本之前必须已经有执行位。
//
// 这条是实跑一次在线更新才发现的：os.CreateTemp 建出来的是 0600，而
// probeVersion 要真的执行它，于是每次都死在
// "fork/exec ...: permission denied"——而那个报错看起来像"包里东西坏了"，
// 会把人带到完全错误的方向去。
//
// 拿测试二进制自己当发布包里的 yc7zip：它是本架构的合法 ELF，被问 -version
// 时会因为不认识这个参数而退出，正好把"执行成功但读不到版本号"（期望）与
// "根本执行不了"（缺陷）区分开。
func TestStagedBinaryIsExecutableBeforeProbe(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("执行位是 Linux 上的概念，这条断言在别的平台没有意义")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(t.TempDir(), "yc-7zip-test.tar.gz")
	writeTarGz(t, pkg, "yc7zip", self)

	srv := newUpdateServer(t, Config{})
	_, err = srv.installFromFile(pkg, filepath.Base(pkg), "")
	if err == nil {
		t.Fatal("一个不认识 -version 的程序不该被当成发布包装上")
	}
	if strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("解包后没有执行位，探测阶段就失败了：%v", err)
	}
	if !strings.Contains(err.Error(), "执行失败") {
		t.Fatalf("应当是执行成功但读不到版本号：%v", err)
	}
}

func TestFallsBackToGitHubWhenSelfSourceFails(t *testing.T) {
	name := githubPackageName("zip2609.004")
	gh := githubServer(t, releaseJSON("zip2609.004", "", map[string]string{
		name:             "https://example.invalid/" + name,
		"SHA256SUMS.txt": "https://example.invalid/SHA256SUMS.txt",
	}))
	bad := failingServer(t)

	srv := newUpdateServer(t, Config{
		RepoURL:       "https://github.com/YCyingchen/YC-7ZIP",
		UpdateSources: []string{bad.URL + "/", "github"},
	})
	srv.githubAPI = gh.URL

	status, err := srv.checkForUpdate(context.Background(), "")
	if err != nil {
		t.Fatalf("一条源坏了不该让整次检查失败：%v", err)
	}
	if status.Source != sourceGitHub {
		t.Fatalf("应回落到 GitHub，实际是 %q", status.Source)
	}
	// 坏掉的那条要报出来，否则用户看到"检查失败"会以为所有渠道都断了
	if len(status.Sources) != 2 {
		t.Fatalf("应逐条报告结果，实际 %d 条", len(status.Sources))
	}
	if status.Sources[0].OK || status.Sources[0].Error == "" {
		t.Fatalf("自建源的失败应被记录：%+v", status.Sources[0])
	}
	if !status.Sources[1].OK || status.Sources[1].Version != "zip2609.004" {
		t.Fatalf("GitHub 那条应成功：%+v", status.Sources[1])
	}
}

func TestAllSourcesFailingIsReported(t *testing.T) {
	first, second := failingServer(t), failingServer(t)
	srv := newUpdateServer(t, Config{UpdateSources: []string{first.URL + "/", second.URL + "/"}})

	status, err := srv.checkForUpdate(context.Background(), "")
	if err == nil {
		t.Fatal("所有源都坏了却没报错")
	}
	if !strings.Contains(err.Error(), "所有更新源都不可用") {
		t.Fatalf("报错应说明是全部源的问题，实际：%v", err)
	}
	// 两条都要点名，否则用户不知道该去修哪一条
	for _, src := range []*httptest.Server{first, second} {
		if !strings.Contains(err.Error(), src.URL) {
			t.Fatalf("报错里缺少 %s：%v", src.URL, err)
		}
	}
	if len(status.Sources) != 2 || status.Sources[0].OK || status.Sources[1].OK {
		t.Fatalf("逐条结果不对：%+v", status.Sources)
	}
}

// 多源之间会互相落后，取版本最高的那条而不是"第一条能答的"，
// 否则一条还没同步完的源会把新版盖掉，表现为"检查不到更新"。
func TestPicksHighestVersionAcrossSources(t *testing.T) {
	older := staticServer(t, map[string]string{
		"update.json": manifestFor(t, "zip2609.003", "test", strings.Repeat("ab", 32)),
	})
	newer := staticServer(t, map[string]string{
		"update.json": manifestFor(t, "zip2609.006", "test", strings.Repeat("cd", 32)),
	})
	srv := newUpdateServer(t, Config{UpdateSources: []string{older.URL + "/", newer.URL + "/"}})

	status, err := srv.checkForUpdate(context.Background(), "")
	if err != nil {
		t.Fatalf("检查失败：%v", err)
	}
	if status.Latest != "zip2609.006" {
		t.Fatalf("应取更高的 zip2609.006，实际 %q", status.Latest)
	}
	if status.SourceURL != newer.URL {
		t.Fatalf("结果应记在较新的那条源上，实际 %q", status.SourceURL)
	}
}

func TestCheckCanBeLimitedToOneSource(t *testing.T) {
	self := staticServer(t, map[string]string{
		"update.json": manifestFor(t, "zip2609.009", "test", strings.Repeat("ab", 32)),
	})
	gh := githubServer(t, releaseJSON("zip2609.004", "", map[string]string{
		githubPackageName("zip2609.004"): "https://example.invalid/pkg",
		"SHA256SUMS.txt":                 "https://example.invalid/SHA256SUMS.txt",
	}))
	srv := newUpdateServer(t, Config{
		RepoURL:       "https://github.com/YCyingchen/YC-7ZIP",
		UpdateSources: []string{self.URL + "/", "github"},
	})
	srv.githubAPI = gh.URL

	status, err := srv.checkForUpdate(context.Background(), "github")
	if err != nil {
		t.Fatalf("检查失败：%v", err)
	}
	if len(status.Sources) != 1 || status.Sources[0].Kind != sourceGitHub {
		t.Fatalf("限定渠道后只该问那一条：%+v", status.Sources)
	}
	if status.Latest != "zip2609.004" {
		t.Fatalf("应是 GitHub 上的版本，实际 %q", status.Latest)
	}

	if _, err := srv.checkForUpdate(context.Background(), "nope"); err == nil {
		t.Fatal("指定了不存在的渠道却没报错")
	}
}

func TestStatusExposesConfiguredSources(t *testing.T) {
	srv := newUpdateServer(t, Config{UpdateSources: []string{"https://a.example/", "github"}})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	res, err := http.Get(ts.URL + "/api/update")
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	defer res.Body.Close()

	var status UpdateStatus
	if err := json.NewDecoder(res.Body).Decode(&status); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(status.SourceOptions) != 2 {
		t.Fatalf("应列出两条渠道：%+v", status.SourceOptions)
	}
	if status.SourceOptions[0].Kind != sourceSelf || status.SourceOptions[0].URL != "https://a.example" {
		t.Fatalf("第一条应是自建源：%+v", status.SourceOptions[0])
	}
	if status.SourceOptions[1].Kind != sourceGitHub {
		t.Fatalf("第二条应是 GitHub：%+v", status.SourceOptions[1])
	}
}

// 没配 -update-sources 时要落到默认渠道：先自建源，再 GitHub。
// 界面靠这个列表渲染选择器，配错了会直接影响能不能手动换渠道。
func TestDefaultSourcesIncludeSelfHostedAndGitHub(t *testing.T) {
	srv := newUpdateServer(t, Config{})
	if len(srv.sources) != 2 {
		t.Fatalf("默认应有两条渠道：%+v", srv.sources)
	}
	if srv.sources[0].kind != sourceSelf || srv.sources[0].base != "https://yc7zip.202693.xyz" {
		t.Fatalf("第一条默认应是自建源：%+v", srv.sources[0])
	}
	if srv.sources[1].kind != sourceGitHub {
		t.Fatalf("第二条默认应是 GitHub：%+v", srv.sources[1])
	}
}
