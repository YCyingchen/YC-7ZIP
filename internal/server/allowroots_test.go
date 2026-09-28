package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 允许清单之外的路径要"看不见"：既不列出来，也不靠一句带路径的报错把
// NAS 上有什么告诉对方——上架之后这个页面是这台 NAS 的所有用户都能打开的。
func TestPathsOutsideAllowRootsAreHidden(t *testing.T) {
	base := t.TempDir()
	allowed := filepath.Join(base, "允许区")
	outside := filepath.Join(base, "不该看见的目录")
	for _, dir := range []string{filepath.Join(allowed, "子目录"), outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	srv := newUpdateServer(t, Config{AllowRoots: []string{allowed}})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	get := func(path string) (int, string) {
		t.Helper()
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, string(raw)
	}

	t.Run("清单内的目录照常列出", func(t *testing.T) {
		code, body := get("/api/browse?path=" + url.QueryEscape(allowed))
		if code != http.StatusOK {
			t.Fatalf("状态码 %d：%s", code, body)
		}
		if !strings.Contains(body, "子目录") {
			t.Fatalf("应当列出子目录：%s", body)
		}
	})

	t.Run("清单外的目录退回根视图，且不出现那个路径", func(t *testing.T) {
		code, body := get("/api/browse?path=" + url.QueryEscape(outside))
		if code != http.StatusOK {
			t.Fatalf("应当退回根视图而不是报错，得到 %d：%s", code, body)
		}
		if strings.Contains(body, "不该看见的目录") {
			t.Fatalf("响应里不该出现那个路径：%s", body)
		}
		if !strings.Contains(body, filepath.Base(allowed)) {
			t.Fatalf("应当给出允许的根目录：%s", body)
		}
	})

	t.Run("操作类接口拒绝时也不回路径", func(t *testing.T) {
		code, body := get("/api/inspect?path=" + url.QueryEscape(filepath.Join(outside, "x.zip")))
		if code != http.StatusForbidden {
			t.Fatalf("应当 403，得到 %d：%s", code, body)
		}
		if strings.Contains(body, "不该看见的目录") {
			t.Fatalf("报错里不该带路径：%s", body)
		}
	})
}
