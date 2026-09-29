package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/ycyingchen/yc-7zip/internal/job"
)

const (
	// historyDirName 是数据目录下存放历史的子目录。
	historyDirName = "history"
	// historyFileName 是历史文件名。
	historyFileName = "history.json"
	// historyLimit 是保留条数上限。够回溯，但必须有上限：一个"每次操作都追加"
	// 的文件没有上限，迟早会变成打开面板就卡住的原因。
	historyLimit = 200
	// historyMaxOutputs 限制单条记录里保存的产物条数，避免"解压出一个十万文件
	// 的目录"把历史文件本身撑大。超出的只记条数，不记名字。
	historyMaxOutputs = 50
)

// HistoryFile 是历史条目里的一个产物。
type HistoryFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	// Path 是服务端路径。"下载到本机"的产物落在任务工作区里，任务 TTL 一到
	// 就会被清掉，所以这个路径随时可能失效。
	Path string `json:"path,omitempty"`
	// Missing 表示记下这条时路径还在、现在磁盘上已经没有了。
	Missing bool `json:"missing,omitempty"`
}

// HistoryEntry 是一次压缩/解压的存档。
//
// 它是"给人看"的记录，不是任务快照：任务对象（job.Job）里的工作区路径、
// 上传临时目录都会过期，那些字段不往这里抄。
type HistoryEntry struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`   // compress / extract
	Status     string    `json:"status"` // done / error / cancelled
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	DurationMS int64     `json:"duration_ms"`

	// Sources 是来源的显示名（NAS 上是完整路径，上传模式是文件名）。
	Sources []string `json:"sources,omitempty"`

	OutputMode string        `json:"output_mode,omitempty"` // server / download
	OutputDir  string        `json:"output_dir,omitempty"`
	Outputs    []HistoryFile `json:"outputs,omitempty"`
	OutputNote string        `json:"output_note,omitempty"` // 产物被截断时的说明
	TotalBytes int64         `json:"total_bytes,omitempty"`

	VolumeLabel string `json:"volume_label,omitempty"`
	Message     string `json:"message,omitempty"`

	// Missing 表示这条记录的产物在磁盘上已经找不到了。
	Missing bool `json:"missing,omitempty"`
}

// historyStore 负责历史记录的读写。
type historyStore struct {
	dir string

	mu      sync.Mutex
	entries []HistoryEntry
}

func newHistoryStore(dataDir string) *historyStore {
	h := &historyStore{}
	if dataDir != "" {
		h.dir = filepath.Join(dataDir, historyDirName)
	}
	h.load()
	return h
}

// load 读取磁盘上的历史；坏了就当成空历史，界面不该因为记录文件损坏打不开。
func (h *historyStore) load() {
	if h.dir == "" {
		return
	}
	raw, err := os.ReadFile(filepath.Join(h.dir, historyFileName))
	if err != nil {
		return
	}
	var file struct {
		Entries []HistoryEntry `json:"entries"`
	}
	if json.Unmarshal(raw, &file) != nil {
		return
	}
	if len(file.Entries) > historyLimit {
		file.Entries = file.Entries[:historyLimit]
	}
	h.entries = file.Entries
}

// add 记录一条，最新的排在前面。
func (h *historyStore) add(e HistoryEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// 同一个任务只会有一条：任务结束时才记，但取消与失败可能前后脚都到。
	for i := range h.entries {
		if h.entries[i].ID == e.ID {
			h.entries = append(h.entries[:i], h.entries[i+1:]...)
			break
		}
	}
	h.entries = append([]HistoryEntry{e}, h.entries...)
	if len(h.entries) > historyLimit {
		h.entries = h.entries[:historyLimit]
	}
	h.saveLocked()
}

// list 返回最新在前的副本，并标注产物是否还在磁盘上。
func (h *historyStore) list() []HistoryEntry {
	h.mu.Lock()
	defer h.mu.Unlock()

	out := make([]HistoryEntry, len(h.entries))
	copy(out, h.entries)
	for i := range out {
		out[i].Missing = annotateMissing(&out[i])
	}
	return out
}

// annotateMissing 标出已经不存在的产物，返回"全都找不到了"。
func annotateMissing(e *HistoryEntry) bool {
	if len(e.Outputs) == 0 {
		return false
	}
	missing := 0
	for i := range e.Outputs {
		p := e.Outputs[i].Path
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			e.Outputs[i].Missing = true
			missing++
		}
	}
	return missing == len(e.Outputs)
}

// remove 删除一条。
func (h *historyStore) remove(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	for i := range h.entries {
		if h.entries[i].ID == id {
			h.entries = append(h.entries[:i], h.entries[i+1:]...)
			h.saveLocked()
			return true
		}
	}
	return false
}

// clear 清空。
func (h *historyStore) clear() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	n := len(h.entries)
	h.entries = nil
	h.saveLocked()
	return n
}

// saveLocked 落盘。调用方持锁。先写临时文件再改名，中途断电不会留下半个 JSON。
func (h *historyStore) saveLocked() {
	if h.dir == "" {
		return
	}
	if err := os.MkdirAll(h.dir, 0o755); err != nil {
		return
	}
	raw, err := json.MarshalIndent(struct {
		Entries []HistoryEntry `json:"entries"`
	}{Entries: h.entries}, "", "  ")
	if err != nil {
		return
	}
	tmp := filepath.Join(h.dir, historyFileName+".tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, filepath.Join(h.dir, historyFileName))
}

// recordHistory 把一个已结束的任务写进历史。
//
// 挂在 launch 的收尾处，所以只看得到终态；还在跑的任务不该出现在历史里。
func (s *Server) recordHistory(id string) {
	if s.history == nil {
		return
	}
	j, ok := s.jobs.Get(id)
	if !ok {
		return
	}
	switch j.Status {
	case job.StatusDone, job.StatusError, job.StatusCancelled:
	default:
		return
	}
	s.history.add(historyEntryFromJob(j, time.Now()))
}

// historyEntryFromJob 把任务翻成一条历史。
func historyEntryFromJob(j *job.Job, now time.Time) HistoryEntry {
	e := HistoryEntry{
		ID:          j.ID,
		Kind:        string(j.Kind),
		Status:      string(j.Status),
		StartedAt:   j.CreatedAt,
		FinishedAt:  now,
		Sources:     append([]string(nil), j.SourceNames...),
		OutputMode:  string(j.OutputMode),
		OutputDir:   j.OutputDir,
		TotalBytes:  j.TotalBytes,
		VolumeLabel: j.VolumeLabel,
		Message:     j.Message,
	}
	e.DurationMS = e.FinishedAt.Sub(e.StartedAt).Milliseconds()
	if e.DurationMS < 0 {
		e.DurationMS = 0
	}

	// 写入 NAS 的任务把产物记在 ServerPaths；下载模式记在 Files（工作区里）。
	paths := j.ServerPaths
	for _, p := range paths {
		if len(e.Outputs) >= historyMaxOutputs {
			break
		}
		f := HistoryFile{Name: filepath.Base(p), Path: p}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			f.Size = st.Size()
		}
		e.Outputs = append(e.Outputs, f)
	}
	if len(paths) > historyMaxOutputs {
		e.OutputNote = "共 " + strconv.Itoa(len(paths)) + " 项"
	}
	for _, f := range j.Files {
		if len(e.Outputs) >= historyMaxOutputs {
			break
		}
		e.Outputs = append(e.Outputs, HistoryFile{Name: f.Name, Size: f.Size, Path: f.Path})
	}
	if len(j.Files) > historyMaxOutputs && e.OutputNote == "" {
		e.OutputNote = "共 " + strconv.Itoa(len(j.Files)) + " 项"
	}
	return e
}

// handleHistoryList 返回历史记录。
func (s *Server) handleHistoryList(w http.ResponseWriter, r *http.Request) {
	entries := []HistoryEntry{}
	if s.history != nil {
		entries = s.history.list()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"limit":   historyLimit,
	})
}

// handleHistoryDelete 删除一条记录。删的是记录，不动磁盘上的产物。
func (s *Server) handleHistoryDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.history == nil || !s.history.remove(id) {
		writeError(w, http.StatusNotFound, "记录不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleHistoryClear 清空历史。同样只删记录。
func (s *Server) handleHistoryClear(w http.ResponseWriter, r *http.Request) {
	removed := 0
	if s.history != nil {
		removed = s.history.clear()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed})
}
