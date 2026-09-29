package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// searchResponse 是 /api/search 的响应，测试里按字段取值。
type searchResponse struct {
	Enabled   bool          `json:"enabled"`
	Path      string        `json:"path"`
	Query     string        `json:"query"`
	Entries   []searchEntry `json:"entries"`
	Truncated bool          `json:"truncated"`
	Scanned   int           `json:"scanned"`
}

// searchFixture 造一棵有层次的目录树：跨子目录的同名文件、隐藏目录、
// 飞牛自己的目录、以及大小写不同的英文名——搜索的几条规则都要在这里能验。
func searchFixture(t *testing.T) string {
	t.Helper()
	shared := t.TempDir()
	for name, body := range map[string]string{
		"1000/报告2024.pdf":             "a",
		"1000/子目录/报告2024-附录.pdf":      "b",
		"1000/子目录/other.txt":          "c",
		"1000/Photos/IMG_001.JPG":     "d",
		"1000/@appdata/hidden-报告.pdf": "e",
		"1000/.cache/hidden-报告.pdf":   "f",
		"1000/thumb/hidden-报告.pdf":    "g",
		"空间4/录像2024.mp4":              "h",
	} {
		full := filepath.Join(shared, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return shared
}

// doSearch 打一次 /api/search，同时回原始响应体——越界用例要断言"里面没有那个路径"。
func doSearch(t *testing.T, ts *httptest.Server, dir, query, extra string) (int, searchResponse, string) {
	t.Helper()
	target := ts.URL + "/api/search?path=" + url.QueryEscape(dir) + "&q=" + url.QueryEscape(query) + extra
	res, err := http.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body searchResponse
	_ = json.Unmarshal(raw, &body)
	return res.StatusCode, body, string(raw)
}

func relNames(entries []searchEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Rel)
	}
	return out
}

func hasRel(entries []searchEntry, rel string) bool {
	for _, e := range entries {
		if e.Rel == rel {
			return true
		}
	}
	return false
}

// 搜索要能穿到子目录里，这是它存在的理由：列表只列一层，找文件全靠点。
func TestSearchFindsNestedEntries(t *testing.T) {
	shared := searchFixture(t)
	srv := newUpdateServer(t, Config{AllowRoots: []string{shared}})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	code, body, raw := doSearch(t, ts, shared, "报告", "")
	if code != http.StatusOK {
		t.Fatalf("状态码 %d", code)
	}
	if !body.Enabled {
		t.Fatal("启用了允许目录时应当 enabled=true")
	}
	if body.Path != shared {
		t.Fatalf("回带搜索根 %q，期望 %q", body.Path, shared)
	}
	// 命中的正好是两个：顶层那个 + 子目录里那个（@appdata/.cache/thumb 里的都不算）
	if len(body.Entries) != 2 {
		t.Fatalf("应当正好命中 2 个：%v（%s）", relNames(body.Entries), raw)
	}
	if !hasRel(body.Entries, "1000/报告2024.pdf") || !hasRel(body.Entries, "1000/子目录/报告2024-附录.pdf") {
		t.Fatalf("命中不对：%v", relNames(body.Entries))
	}
	if body.Scanned < 8 {
		t.Fatalf("扫描计数看着不对：%d", body.Scanned)
	}
}

// 隐藏目录与飞牛自己的目录不进结果：列表里看不见的东西，搜索里露出来等于绕过了那条规则。
func TestSearchSkipsHiddenAndInternal(t *testing.T) {
	shared := searchFixture(t)
	srv := newUpdateServer(t, Config{AllowRoots: []string{shared}})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	_, body, _ := doSearch(t, ts, shared, "hidden", "")
	if len(body.Entries) != 0 {
		t.Fatalf("隐藏目录里的文件不该出现：%v", relNames(body.Entries))
	}

	_, body, raw := doSearch(t, ts, shared, "报告", "")
	if len(body.Entries) != 2 {
		t.Fatalf("被藏起来的目录不该贡献命中：%v", relNames(body.Entries))
	}
	for _, leak := range []string{"@appdata", ".cache", "thumb"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("响应里出现了 %s：%s", leak, raw)
		}
	}
}

// 大小写不敏感、多个词是"与"的关系。
func TestSearchMatchingRules(t *testing.T) {
	shared := searchFixture(t)
	srv := newUpdateServer(t, Config{AllowRoots: []string{shared}})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	_, body, _ := doSearch(t, ts, shared, "img_001", "")
	if !hasRel(body.Entries, "1000/Photos/IMG_001.JPG") {
		t.Fatalf("小写查询没命中大写文件名：%v", relNames(body.Entries))
	}

	_, body, _ = doSearch(t, ts, shared, "报告 附录", "")
	if len(body.Entries) != 1 || body.Entries[0].Rel != "1000/子目录/报告2024-附录.pdf" {
		t.Fatalf("多词应当是与的关系：%v", relNames(body.Entries))
	}

	// 目录也能被搜到（按目录名），并且标记成目录
	_, body, _ = doSearch(t, ts, shared, "子目录", "")
	if len(body.Entries) != 1 || !body.Entries[0].IsDir {
		t.Fatalf("应当命中目录并标记 is_dir：%v", relNames(body.Entries))
	}
	if body.Entries[0].Dir != filepath.Join(shared, "1000") {
		t.Fatalf("dir 字段应当是它的父目录：%q", body.Entries[0].Dir)
	}
}

// 在子树里搜：受与浏览相同的边界约束，rel 相对的是这一棵子树。
func TestSearchRespectsSubtreeAndLimit(t *testing.T) {
	shared := searchFixture(t)
	sub := filepath.Join(shared, "1000")
	srv := newUpdateServer(t, Config{AllowRoots: []string{shared}})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	_, body, _ := doSearch(t, ts, sub, "报告", "")
	if body.Path != sub {
		t.Fatalf("回带的搜索根应为 %q，实际 %q", sub, body.Path)
	}
	if !hasRel(body.Entries, "报告2024.pdf") {
		t.Fatalf("rel 应当相对搜索根：%v", relNames(body.Entries))
	}

	// 只取一条：要带截断标记，否则界面会把"只找到一条"当成事实
	code, limited, _ := doSearch(t, ts, shared, "报告", "&limit=1")
	if code != http.StatusOK || len(limited.Entries) != 1 {
		t.Fatalf("limit=1 应当只回一条，实际 %v", relNames(limited.Entries))
	}
	if !limited.Truncated {
		t.Fatal("还有更多命中时应当标记 truncated")
	}
	if limited.Scanned == 0 {
		t.Fatal("应当报告扫描过的条目数")
	}
}

// 空查询回空而不是报错：界面清空输入框时走的就是这条路。
func TestSearchEmptyQuery(t *testing.T) {
	shared := searchFixture(t)
	srv := newUpdateServer(t, Config{AllowRoots: []string{shared}})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	code, body, _ := doSearch(t, ts, shared, "   ", "")
	if code != http.StatusOK || len(body.Entries) != 0 {
		t.Fatalf("空查询应当回空：%d %v", code, relNames(body.Entries))
	}
}

// 越界：403，而且响应里不能出现那个路径——理由与 checkAllowed 相同。
func TestSearchDeniesOutsideRoots(t *testing.T) {
	shared := searchFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "报告.pdf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newUpdateServer(t, Config{AllowRoots: []string{shared}})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	code, body, raw := doSearch(t, ts, outside, "报告", "")
	if code != http.StatusForbidden {
		t.Fatalf("越界应当 403，实际 %d（%s）", code, raw)
	}
	if strings.Contains(raw, outside) {
		t.Fatalf("响应里不该回带越界路径：%s", raw)
	}
	if len(body.Entries) != 0 {
		t.Fatalf("越界不该给结果：%v", relNames(body.Entries))
	}
}

// 没配允许目录时搜索是关闭的（和浏览一致），而不是"搜整台机器"。
func TestSearchDisabledWithoutRoots(t *testing.T) {
	srv := newUpdateServer(t, Config{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	code, body, _ := doSearch(t, ts, "", "报告", "")
	if code != http.StatusOK || body.Enabled {
		t.Fatalf("没配允许目录时应 enabled=false：%d %+v", code, body)
	}
}

// path 留空 = 在所有可用目录里搜，正好对上界面停在「可用目录」那一层的情况。
func TestSearchWithoutPathCoversAllRoots(t *testing.T) {
	shared := searchFixture(t)
	second := t.TempDir()
	if err := os.WriteFile(filepath.Join(second, "报告-另一卷.pdf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newUpdateServer(t, Config{AllowRoots: []string{shared, second}})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	_, body, _ := doSearch(t, ts, "", "报告", "")
	if body.Path != "" {
		t.Fatalf("全局搜索时 path 应当为空，实际 %q", body.Path)
	}
	var foundSecond bool
	for _, e := range body.Entries {
		if e.Name == "报告-另一卷.pdf" {
			foundSecond = true
		}
	}
	if !foundSecond {
		t.Fatalf("第二个允许目录也该被搜到：%v", relNames(body.Entries))
	}
}
