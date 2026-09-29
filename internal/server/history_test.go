package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ycyingchen/yc-7zip/internal/job"
)

func TestHistoryStoreRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	h := newHistoryStore(dataDir)
	if got := len(h.list()); got != 0 {
		t.Fatalf("新库应为空，得到 %d 条", got)
	}

	product := filepath.Join(dataDir, "packed.7z")
	if err := os.WriteFile(product, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.add(HistoryEntry{
		ID: "job-1", Kind: "compress", Status: "done",
		StartedAt: time.Now().Add(-time.Second), FinishedAt: time.Now(), DurationMS: 1200,
		Sources:    []string{"/vol1/1000/a.txt"},
		OutputMode: "server", OutputDir: dataDir,
		Outputs: []HistoryFile{{Name: "packed.7z", Path: product, Size: 7}},
	})

	list := h.list()
	if len(list) != 1 {
		t.Fatalf("应有 1 条，得到 %d", len(list))
	}
	if list[0].Missing {
		t.Fatal("产物还在磁盘上，不该标 missing")
	}
	if list[0].DurationMS != 1200 || list[0].OutputDir != dataDir || list[0].Sources[0] != "/vol1/1000/a.txt" {
		t.Fatalf("往返后字段对不上：%+v", list[0])
	}

	// 新起的进程（新的 store）要能读回同一条：历史是落盘的，不是内存里的。
	again := newHistoryStore(dataDir)
	if got := len(again.list()); got != 1 {
		t.Fatalf("重新加载后应有 1 条，得到 %d", got)
	}
}

func TestHistoryStoreLimit(t *testing.T) {
	dataDir := t.TempDir()
	h := newHistoryStore(dataDir)
	for i := 0; i < historyLimit+7; i++ {
		h.add(HistoryEntry{ID: "job-" + strconv.Itoa(i), Kind: "extract", Status: "done"})
	}
	list := h.list()
	if len(list) != historyLimit {
		t.Fatalf("应截断到 %d 条，得到 %d", historyLimit, len(list))
	}
	if list[0].ID != "job-"+strconv.Itoa(historyLimit+6) {
		t.Fatalf("最新的应排在最前，得到 %s", list[0].ID)
	}
}

func TestHistoryStoreMarksMissing(t *testing.T) {
	dataDir := t.TempDir()
	h := newHistoryStore(dataDir)

	gone := filepath.Join(dataDir, "gone.7z")
	h.add(HistoryEntry{ID: "a", Status: "done", Outputs: []HistoryFile{{Name: "gone.7z", Path: gone}}})
	h.add(HistoryEntry{ID: "b", Status: "done"})

	// 最新的排在最前，所以按 id 找，不能按下标。
	list := h.list()
	byID := map[string]HistoryEntry{}
	for _, e := range list {
		byID[e.ID] = e
	}
	if !byID["a"].Missing {
		t.Fatal("产物不在磁盘上，应标 missing")
	}
	if byID["b"].Missing {
		t.Fatal("没有产物的记录不该被当成 missing")
	}
}

func TestHistoryStoreRemoveAndClear(t *testing.T) {
	h := newHistoryStore(t.TempDir())
	h.add(HistoryEntry{ID: "a", Status: "done"})
	h.add(HistoryEntry{ID: "b", Status: "error"})

	if h.remove("不存在") {
		t.Fatal("删不存在的记录应返回 false")
	}
	if !h.remove("a") {
		t.Fatal("删存在的记录应返回 true")
	}
	if got := len(h.list()); got != 1 {
		t.Fatalf("应剩 1 条，得到 %d", got)
	}
	if n := h.clear(); n != 1 {
		t.Fatalf("清空应返回 1，得到 %d", n)
	}
	if got := len(h.list()); got != 0 {
		t.Fatalf("清空后应为空，得到 %d", got)
	}
}

func TestHistoryEntryFromJob(t *testing.T) {
	dir := t.TempDir()
	product := filepath.Join(dir, "out.7z")
	if err := os.WriteFile(product, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 目录也当成一项产物：只记名字，不进去数文件（解压出十万个文件时那是白跑）。
	sub := filepath.Join(dir, "解压结果")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	start := time.Now().Add(-3 * time.Second)
	j := &job.Job{
		ID: "j1", Kind: job.KindCompress, Status: job.StatusDone,
		CreatedAt: start, UpdatedAt: time.Now(),
		SourceNames: []string{"/vol1/1000/a.txt", "/vol1/1000/b.txt"},
		OutputMode:  job.OutputServer, OutputDir: dir,
		ServerPaths: []string{product, sub},
		TotalBytes:  5, VolumeLabel: "100M",
	}

	e := historyEntryFromJob(j, time.Now())
	if e.Kind != "compress" || e.Status != "done" {
		t.Fatalf("种类/状态没翻译对：%+v", e)
	}
	if len(e.Outputs) != 2 {
		t.Fatalf("应有 2 项产物，得到 %d", len(e.Outputs))
	}
	if e.Outputs[0].Size != 5 || e.Outputs[1].Name != "解压结果" {
		t.Fatalf("产物记录不对：%+v", e.Outputs)
	}
	if e.DurationMS < 2900 {
		t.Fatalf("用时应约 3 秒，得到 %d ms", e.DurationMS)
	}
	if e.TotalBytes != 5 || e.VolumeLabel != "100M" {
		t.Fatalf("汇总字段丢了：%+v", e)
	}
}

// newHistoryTestServer 起一个只测历史接口的服务端。
// engine 传 nil：这些接口不碰 7-Zip，测试不必要求机器上装了它。
func newHistoryTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	root := t.TempDir()
	jobs, err := job.NewManager(filepath.Join(root, "jobs"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		DataDir: t.TempDir(), MaxUpload: 1 << 20, JobTTL: time.Hour, Version: "test",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	// engine 传 nil：历史接口不碰 7-Zip，测试也就不必要求机器上装了它。
	srv := New(cfg, nil, jobs, os.DirFS("../.."))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

func fetchHistory(t *testing.T, ts *httptest.Server) []HistoryEntry {
	t.Helper()
	res, err := http.Get(ts.URL + "/api/history")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/history = %d，想要 200", res.StatusCode)
	}
	var out struct {
		Entries []HistoryEntry `json:"entries"`
		Limit   int            `json:"limit"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Limit != historyLimit {
		t.Fatalf("返回的上限应是 %d，得到 %d", historyLimit, out.Limit)
	}
	return out.Entries
}

func callHistory(t *testing.T, method, url string) int {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	return res.StatusCode
}

func TestHistoryAPI(t *testing.T) {
	srv, ts := newHistoryTestServer(t)

	if got := fetchHistory(t, ts); len(got) != 0 {
		t.Fatalf("一开始不该有记录，得到 %d 条", len(got))
	}

	// 直接往库里塞记录：这里测的是接口，任务生命周期另有测试。
	srv.history.add(HistoryEntry{ID: "h1", Kind: "extract", Status: "done", FinishedAt: time.Now()})
	if got := fetchHistory(t, ts); len(got) != 1 || got[0].ID != "h1" {
		t.Fatalf("应有 1 条 h1，得到 %+v", got)
	}

	if code := callHistory(t, http.MethodDelete, ts.URL+"/api/history/找不到"); code != http.StatusNotFound {
		t.Fatalf("删不存在的记录应 404，得到 %d", code)
	}
	if code := callHistory(t, http.MethodDelete, ts.URL+"/api/history/h1"); code != http.StatusOK {
		t.Fatalf("删记录应 200，得到 %d", code)
	}
	if got := fetchHistory(t, ts); len(got) != 0 {
		t.Fatalf("删完应为空，得到 %d 条", len(got))
	}

	srv.history.add(HistoryEntry{ID: "h2", Status: "done"})
	srv.history.add(HistoryEntry{ID: "h3", Status: "done"})
	if code := callHistory(t, http.MethodDelete, ts.URL+"/api/history"); code != http.StatusOK {
		t.Fatalf("清空应 200，得到 %d", code)
	}
	if got := fetchHistory(t, ts); len(got) != 0 {
		t.Fatalf("清空后应为空，得到 %d 条", len(got))
	}
}
