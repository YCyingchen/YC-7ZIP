package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ycyingchen/yc-7zip/internal/engine"
	"github.com/ycyingchen/yc-7zip/internal/job"
)

type harness struct {
	t      *testing.T
	server *httptest.Server
	srv    *Server
	jobs   *job.Manager
	root   string
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	eng, err := engine.New()
	if err != nil {
		t.Skipf("7-Zip 不可用，跳过：%v", err)
	}
	root := t.TempDir()
	jobs, err := job.NewManager(filepath.Join(root, "jobs"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxUpload == 0 {
		cfg.MaxUpload = 64 << 20
	}
	if cfg.JobTTL == 0 {
		cfg.JobTTL = time.Hour
	}
	if cfg.Version == "" {
		cfg.Version = "test"
	}
	srv := New(cfg, eng, jobs, os.DirFS("../.."))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &harness{t: t, server: ts, srv: srv, jobs: jobs, root: root}
}

func (h *harness) do(method, path string, body io.Reader, ctype string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(method, h.server.URL+path, body)
	if err != nil {
		h.t.Fatal(err)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	return res
}

func (h *harness) json(method, path string, payload any) (*http.Response, map[string]any) {
	h.t.Helper()
	var body io.Reader
	if payload != nil {
		buf, err := json.Marshal(payload)
		if err != nil {
			h.t.Fatal(err)
		}
		body = bytes.NewReader(buf)
	}
	res := h.do(method, path, body, "application/json")
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return res, out
}

// upload sends one multipart body carrying the given files.
func (h *harness) upload(jobID string, files map[string]string) *http.Response {
	h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, content := range files {
		if err := mw.WriteField("relpath", name); err != nil {
			h.t.Fatal(err)
		}
		part, err := mw.CreateFormFile("files", filepath.Base(name))
		if err != nil {
			h.t.Fatal(err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			h.t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		h.t.Fatal(err)
	}
	return h.do("POST", "/api/jobs/"+jobID+"/upload", &buf, mw.FormDataContentType())
}

// waitDone polls a job until it leaves the running state.
func (h *harness) waitDone(jobID string, limit time.Duration) map[string]any {
	h.t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		res, body := h.json("GET", "/api/jobs/"+jobID, nil)
		res.Body.Close()
		switch body["status"] {
		case "done", "error", "cancelled":
			return body
		}
		time.Sleep(150 * time.Millisecond)
	}
	h.t.Fatalf("job %s did not finish within %s", jobID, limit)
	return nil
}

func TestHealthAndFormats(t *testing.T) {
	h := newHarness(t, Config{})

	res, body := h.json("GET", "/api/health", nil)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", res.StatusCode)
	}
	if body["ok"] != true {
		t.Fatalf("health ok = %v", body["ok"])
	}

	res, body = h.json("GET", "/api/formats", nil)
	res.Body.Close()
	list, ok := body["formats"].([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("formats missing: %v", body)
	}
}

// TestCompressFlow drives the full user path through HTTP: create, upload, run,
// poll, download.
func TestCompressFlow(t *testing.T) {
	h := newHarness(t, Config{})

	res, created := h.json("POST", "/api/jobs?kind=compress", nil)
	res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d (%v)", res.StatusCode, created)
	}
	id, _ := created["id"].(string)
	if len(id) != 32 {
		t.Fatalf("unexpected job id %q", id)
	}

	up := h.upload(id, map[string]string{
		"docs/readme.txt": "hello world",
		"docs/深空.txt":     "深入",
	})
	up.Body.Close()
	if up.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d", up.StatusCode)
	}

	res, run := h.json("POST", "/api/jobs/"+id+"/run", map[string]any{
		"format": "7z", "level": 5, "name": "bundle",
	})
	res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("run status = %d (%v)", res.StatusCode, run)
	}

	final := h.waitDone(id, 60*time.Second)
	if final["status"] != "done" {
		t.Fatalf("job failed: %v", final["message"])
	}
	files, _ := final["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("expected one output file, got %v", files)
	}
	first, _ := files[0].(map[string]any)
	if first["name"] != "bundle.7z" {
		t.Fatalf("unexpected output name %v", first["name"])
	}

	dl := h.do("GET", "/api/jobs/"+id+"/download-all", nil, "")
	defer dl.Body.Close()
	if dl.StatusCode != http.StatusOK {
		t.Fatalf("download status = %d", dl.StatusCode)
	}
	data, _ := io.ReadAll(dl.Body)
	if len(data) == 0 {
		t.Fatal("downloaded archive is empty")
	}
	// A real 7z file starts with the format signature 37 7A BC AF 27 1C.
	if !bytes.HasPrefix(data, []byte{0x37, 0x7a, 0xbc, 0xaf, 0x27, 0x1c}) {
		t.Fatalf("download is not a 7z archive: % x", data[:8])
	}
}

// TestExtractFlow packs a tree, subscribes it as an extract job, and verifies
// the result set comes back.
func TestExtractFlow(t *testing.T) {
	h := newHarness(t, Config{})

	archive := makeArchive(t, map[string]string{
		"a.txt":     "alpha",
		"b/c.txt":   "gamma",
		"b/d/e.txt": "epsilon",
	})

	res, created := h.json("POST", "/api/jobs?kind=extract", nil)
	res.Body.Close()
	id, _ := created["id"].(string)

	if err := h.uploadRaw(id, "bundle.7z", archive); err != nil {
		t.Fatal(err)
	}

	res, preview := h.json("POST", "/api/jobs/"+id+"/preview", map[string]any{})
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("preview status = %d (%v)", res.StatusCode, preview)
	}
	entries, _ := preview["entries"].([]any)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d (%v)", len(entries), entries)
	}

	res, run := h.json("POST", "/api/jobs/"+id+"/run", map[string]any{"preserve_paths": true})
	res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("run status = %d (%v)", res.StatusCode, run)
	}

	final := h.waitDone(id, 60*time.Second)
	if final["status"] != "done" {
		t.Fatalf("job failed: %v", final["message"])
	}
	files, _ := final["files"].([]any)
	if len(files) != 3 {
		t.Fatalf("expected 3 output files, got %d", len(files))
	}

	// Multiple outputs are bundled into a zip for the single download button.
	dl := h.do("GET", "/api/jobs/"+id+"/download-all", nil, "")
	defer dl.Body.Close()
	if dl.StatusCode != http.StatusOK {
		t.Fatalf("download-all status = %d", dl.StatusCode)
	}
	raw, _ := io.ReadAll(dl.Body)
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("download-all is not a zip: %v", err)
	}
	if len(zr.File) != 3 {
		t.Fatalf("zip holds %d entries, want 3", len(zr.File))
	}
}

// TestDownloadRejectsTraversal checks the path guard on the download endpoint.
func TestDownloadRejectsTraversal(t *testing.T) {
	h := newHarness(t, Config{})

	res, created := h.json("POST", "/api/jobs?kind=compress", nil)
	res.Body.Close()
	id, _ := created["id"].(string)
	up := h.upload(id, map[string]string{"a.txt": "a"})
	up.Body.Close()
	res, _ = h.json("POST", "/api/jobs/"+id+"/run", map[string]any{"format": "zip", "name": "x"})
	res.Body.Close()
	h.waitDone(id, 60*time.Second)

	for _, bad := range []string{"../../etc/passwd", "..%2f..%2fetc%2fpasswd", "/etc/passwd", "a/../../../b"} {
		dl := h.do("GET", "/api/jobs/"+id+"/download?file="+bad, nil, "")
		dl.Body.Close()
		if dl.StatusCode == http.StatusOK {
			t.Errorf("download of %q should have been rejected, got %d", bad, dl.StatusCode)
		}
	}
}

// TestUploadRejectsTraversal checks the path guard on the upload endpoint.
func TestUploadRejectsTraversal(t *testing.T) {
	h := newHarness(t, Config{})

	res, created := h.json("POST", "/api/jobs?kind=compress", nil)
	res.Body.Close()
	id, _ := created["id"].(string)

	up := h.upload(id, map[string]string{"../../evil.txt": "nope"})
	up.Body.Close()
	if up.StatusCode != http.StatusBadRequest {
		t.Fatalf("upload of a traversing name should be rejected, got %d", up.StatusCode)
	}
}

// TestExtractRefusesUnsafeEntries is the end-to-end zip-slip regression test:
// the archive is well formed but a member tries to climb out. 7-Zip itself
// sanitises such names on extraction, and this asserts YC-7ZIP refuses them
// up front rather than relying on that.
func TestExtractRefusesUnsafeEntries(t *testing.T) {
	for _, evil := range []string{"../evil.txt", "../../deep.txt", "/absolute.txt", "a/../../b.txt"} {
		t.Run(evil, func(t *testing.T) {
			h := newHarness(t, Config{})

			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			members := map[string]string{"good.txt": "fine", evil: "escaped"}
			for name, content := range members {
				w, err := zw.Create(name)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := w.Write([]byte(content)); err != nil {
					t.Fatal(err)
				}
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}

			res, created := h.json("POST", "/api/jobs?kind=extract", nil)
			res.Body.Close()
			id, _ := created["id"].(string)
			if err := h.uploadRaw(id, "evil.zip", buf.Bytes()); err != nil {
				t.Fatal(err)
			}

			res, run := h.json("POST", "/api/jobs/"+id+"/run", map[string]any{})
			res.Body.Close()
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("unsafe archive should be refused at run time, got %d (%v)", res.StatusCode, run)
			}
			msg, _ := run["error"].(string)
			if !strings.Contains(msg, "不安全") {
				t.Fatalf("expected an unsafe-path error, got %q", msg)
			}
		})
	}
}

// TestAuthGate verifies the optional basic auth boundary.
func TestAuthGate(t *testing.T) {
	h := newHarness(t, Config{Auth: "admin:secret"})

	res, _ := h.json("GET", "/api/formats", nil)
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request should be 401, got %d", res.StatusCode)
	}

	// /api/health stays open for container probes.
	res, _ = h.json("GET", "/api/health", nil)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("health should stay open, got %d", res.StatusCode)
	}

	req, _ := http.NewRequest("GET", h.server.URL+"/api/formats", nil)
	req.SetBasicAuth("admin", "secret")
	ok, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("authenticated request should succeed, got %d", ok.StatusCode)
	}

	req, _ = http.NewRequest("GET", h.server.URL+"/api/formats", nil)
	req.SetBasicAuth("admin", "wrong")
	bad, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password should be 401, got %d", bad.StatusCode)
	}
}

// TestUploadLimit verifies the size ceiling produces 413 rather than a
// truncated archive.
func TestUploadLimit(t *testing.T) {
	h := newHarness(t, Config{MaxUpload: 1024})

	res, created := h.json("POST", "/api/jobs?kind=compress", nil)
	res.Body.Close()
	id, _ := created["id"].(string)

	up := h.upload(id, map[string]string{"big.bin": strings.Repeat("x", 8192)})
	up.Body.Close()
	if up.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload should be 413, got %d", up.StatusCode)
	}
}

// TestBrowseDisabledByDefault confirms the server-side file picker is inert
// unless the operator opts in.
func TestBrowseDisabledByDefault(t *testing.T) {
	h := newHarness(t, Config{})
	res, body := h.json("GET", "/api/browse", nil)
	res.Body.Close()
	if body["enabled"] != false {
		t.Fatalf("browse should be disabled by default: %v", body)
	}
}

// TestBrowseAllowRoots checks the opt-in path and its boundary.
func TestBrowseAllowRoots(t *testing.T) {
	shared := t.TempDir()
	if err := os.MkdirAll(filepath.Join(shared, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, Config{AllowRoots: []string{shared}})

	res, body := h.json("GET", "/api/browse", nil)
	res.Body.Close()
	if body["enabled"] != true {
		t.Fatalf("browse should be enabled: %v", body)
	}

	res, body = h.json("GET", "/api/browse?path="+shared, nil)
	res.Body.Close()
	entries, _ := body["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %v", entries)
	}

	// 越界的目录既不列出来、也不说"这个路径不在允许范围内"——
	// 后者等于告诉任何能打开页面的人 NAS 上有哪些路径。
	// 这里用一个**平行的**目录而不是父目录：父目录正好是允许根路径的前缀，
	// 响应里出现它的名字是正常的，拿它做断言会误报。
	outside := t.TempDir()
	defer os.RemoveAll(outside)
	res, body = h.json("GET", "/api/browse?path="+outside, nil)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("越界浏览应当退回根视图，得到 %d", res.StatusCode)
	}
	if body["path"] != "" {
		t.Fatalf("退回根视图时 path 应为空，得到 %v", body["path"])
	}
	roots, _ := body["roots"].([]any)
	if len(roots) != 1 {
		t.Fatalf("应当给出允许的根目录，得到 %v", body["roots"])
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), filepath.Base(outside)) {
		t.Fatalf("响应里不该出现越界的那个路径：%s", raw)
	}
}

// TestStaticServesIndex ensures the embedded front end is reachable.
func TestStaticServesIndex(t *testing.T) {
	h := newHarness(t, Config{})
	res := h.do("GET", "/", nil, "")
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d", res.StatusCode)
	}
	raw, _ := io.ReadAll(res.Body)
	if !bytes.Contains(raw, []byte("YC-7ZIP")) {
		t.Fatal("index.html does not mention YC-7ZIP")
	}

	css := h.do("GET", "/assets/style.css", nil, "")
	css.Body.Close()
	if css.StatusCode != http.StatusOK {
		t.Fatalf("GET /assets/style.css = %d", css.StatusCode)
	}
}

// TestStaticAssetsAreRevalidated is the regression test for a stale-cache bug
// that produced a mixed-version page: a fresh index.html against a cached
// app.js from the previous release. It surfaced as "the list/gallery buttons do
// nothing" and a filter dropdown rendering a raw i18n key instead of its label.
//
// Assets are embedded in the binary, so their URL never changes while their
// content does. They must not carry a long max-age — they must revalidate.
func TestStaticAssetsAreRevalidated(t *testing.T) {
	h := newHarness(t, Config{Version: "zip2609.001"})

	res := h.do("GET", "/assets/app.js", nil, "")
	_, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("assets/app.js = %d", res.StatusCode)
	}

	etag := res.Header.Get("ETag")
	if etag == "" {
		t.Fatal("a static asset must carry an ETag so it can be revalidated")
	}
	cacheControl := res.Header.Get("Cache-Control")
	if !strings.Contains(cacheControl, "no-cache") {
		t.Fatalf("Cache-Control = %q, want no-cache so the browser revalidates", cacheControl)
	}
	if strings.Contains(cacheControl, "max-age=") && !strings.Contains(cacheControl, "max-age=0") {
		t.Fatalf("Cache-Control = %q must not let a stale asset be reused", cacheControl)
	}

	// 带上 If-None-Match 应当拿到 304 而不是重新传输
	req, _ := http.NewRequest("GET", h.server.URL+"/assets/app.js", nil)
	req.Header.Set("If-None-Match", etag)
	again, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	again.Body.Close()
	if again.StatusCode != http.StatusNotModified {
		t.Fatalf("revalidation = %d, want 304", again.StatusCode)
	}

	// index.html 给资源 URL 打了版本号，升级后旧缓存不会被命中
	index := h.do("GET", "/", nil, "")
	raw, _ := io.ReadAll(index.Body)
	index.Body.Close()
	if !bytes.Contains(raw, []byte("assets/app.js?v=zip2609.001")) {
		t.Fatal("index.html should stamp asset URLs with the version to bust old caches")
	}
	if !bytes.Contains(raw, []byte("assets/style.css?v=zip2609.001")) {
		t.Fatal("style.css should be stamped too")
	}

	// 带着查询串的资源依然要能取到
	asset := h.do("GET", "/assets/app.js?v=zip2609.001", nil, "")
	asset.Body.Close()
	if asset.StatusCode != http.StatusOK {
		t.Fatalf("stamped asset URL = %d", asset.StatusCode)
	}
}

func TestParseVolumeSize(t *testing.T) {
	ok := map[string]string{"100m": "100m", "2G": "2g", "512k": "512k", "1t": "1t", "256mb": "256m"}
	for in, want := range ok {
		got, err := parseVolumeSize(in)
		if err != nil {
			t.Errorf("parseVolumeSize(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseVolumeSize(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"abc", "12x", "0m", "-5m", "m"} {
		if _, err := parseVolumeSize(bad); err == nil {
			t.Errorf("parseVolumeSize(%q) should fail", bad)
		}
	}
}

func TestSanitizeOutputName(t *testing.T) {
	cases := map[string]string{
		"../evil":    "evil",
		"a/b/c":      "c",
		"name.7z":    "name",
		`bad:<>|?*`:  "bad______",
		"  spaced  ": "spaced",
		"":           "",
	}
	for in, want := range cases {
		if got := sanitizeOutputName(in); got != want {
			t.Errorf("sanitizeOutputName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeUploadPath(t *testing.T) {
	good := map[string]string{
		"a.txt":           "a.txt",
		"dir/a.txt":       "dir/a.txt",
		"./dir/a.txt":     "dir/a.txt",
		"深空/资料.txt":       "深空/资料.txt",
		"/leading/a.txt":  "leading/a.txt",
		"back\\slash.txt": "back/slash.txt",
	}
	for in, want := range good {
		got, err := sanitizeUploadPath(in)
		if err != nil {
			t.Errorf("sanitizeUploadPath(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("sanitizeUploadPath(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", ".", "..", "../a", "a/../../b", "/", "C:/x"} {
		if _, err := sanitizeUploadPath(bad); err == nil {
			t.Errorf("sanitizeUploadPath(%q) should fail", bad)
		}
	}
}

// uploadRaw posts a single binary file as an extract job's archive.
func (h *harness) uploadRaw(jobID, name string, content []byte) error {
	h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("files", name)
	if err != nil {
		return err
	}
	if _, err := part.Write(content); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	res := h.do("POST", "/api/jobs/"+jobID+"/upload", &buf, mw.FormDataContentType())
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(res.Body)
		return &httpError{status: res.StatusCode, body: string(raw)}
	}
	return nil
}

type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string {
	return "unexpected status " + http.StatusText(e.status) + ": " + e.body
}

// makeArchive builds a real 7z archive on disk using the engine itself, so the
// test data is exactly what a user would upload.
func makeArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	eng, err := engine.New()
	if err != nil {
		t.Skipf("7-Zip 不可用，跳过：%v", err)
	}
	base := t.TempDir()
	work := filepath.Join(base, "in")
	out := filepath.Join(base, "out")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(files))
	for name, content := range files {
		full := filepath.Join(work, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	produced, err := eng.Compress(context.Background(), engine.CompressOptions{
		Format: "7z", Level: 5, WorkDir: work, Sources: names,
		OutputName: "bundle", OutputDir: out,
	}, nil)
	if err != nil {
		t.Fatalf("building the fixture archive: %v", err)
	}
	raw, err := os.ReadFile(produced[0])
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
