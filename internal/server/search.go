package server

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 递归搜索的上限。
//
// 这些数字都是"给一个坏情况兜底"用的，不是性能调优：搜索是在一个 NAS 的用户
// 目录里跑，正常几万个文件也就几十毫秒。真正要防的是有人把允许根设成 / 然后
// 搜 "a"——那会扫遍整台机器。所以四道闸：结果条数、扫描条数、时间预算、以及
// 请求被取消（前端每次新的按键都会 abort 上一个请求）。
const (
	searchDefaultLimit = 200
	searchMaxLimit     = 1000
	searchMaxScanned   = 400000
	searchBudget       = 8 * time.Second
)

// searchEntry is one hit from /api/search.
//
// Dir 与 Rel 是给界面用的：搜索结果来自不同的子目录，只显示一个文件名根本
// 分不清是哪一份，所以两个都要给（Dir 用来"跳到它所在的目录"，Rel 用来显示
// "它在搜索根的哪一层"）。
type searchEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Dir   string `json:"dir"`
	Rel   string `json:"rel"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// handleSearch powers the "search for a file" folder-tree walk.
//
// 受与浏览完全相同的边界约束：只在允许清单内搜索，越界时走 writeEngineError
// 的 403，而且**不回带路径**（理由同 checkAllowed）。
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if len(s.cfg.AllowRoots) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": false,
			"roots":   []string{},
			"entries": []searchEntry{},
		})
		return
	}

	// path 留空表示"在所有可用目录里搜"，正好对上界面停在「可用目录」那一层的
	// 情况；否则就在这一棵子树里搜。
	requested := strings.TrimSpace(r.URL.Query().Get("path"))
	var roots []string
	base := ""
	if requested == "" {
		roots = s.searchRoots()
	} else {
		abs, err := s.validateServerPath(requested, true)
		if err != nil {
			writeEngineError(w, err)
			return
		}
		st, err := os.Stat(abs)
		if err != nil {
			writeError(w, http.StatusBadRequest, "无法读取目录："+err.Error())
			return
		}
		if !st.IsDir() {
			writeError(w, http.StatusBadRequest, "只能在一个目录里搜索")
			return
		}
		roots = []string{abs}
		base = abs
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := searchLimit(r)

	entries := make([]searchEntry, 0, 32)
	resp := map[string]any{
		"enabled": true,
		"path":    base,
		"query":   query,
		"entries": entries,
		"roots":   s.browseRoots(),
	}
	// 空查询不报错，直接回空：界面在用户清空输入框时也会走到这里。
	tokens := searchTokens(query)
	if len(tokens) == 0 {
		resp["truncated"] = false
		resp["scanned"] = 0
		writeJSON(w, http.StatusOK, resp)
		return
	}

	hits, scanned, truncated := s.searchTrees(r, roots, tokens, limit)
	resp["entries"] = hits
	resp["scanned"] = scanned
	resp["truncated"] = truncated
	writeJSON(w, http.StatusOK, resp)
}

// searchRoots 是"在所有可用目录里搜"时的起点：与浏览界面看到的根一致，
// 不存在的目录直接跳过（与 browseRoots 同一套判据）。
func (s *Server) searchRoots() []string {
	out := make([]string, 0, len(s.cfg.AllowRoots))
	seen := map[string]bool{}
	for _, root := range s.cfg.AllowRoots {
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		if seen[abs] || !s.isAllowed(abs) {
			continue
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
	}
	return out
}

// searchTrees walks each root and collects matching entries.
//
// 返回 (命中, 扫描过的条目数, 是否被上限截断)。所有上限都是"到点就停"，
// 而不是先算完再截断——在 / 上搜索必须能被立刻叫停。
func (s *Server) searchTrees(r *http.Request, roots []string, tokens []string, limit int) ([]searchEntry, int, bool) {
	hits := make([]searchEntry, 0, 32)
	scanned := 0
	truncated := false
	deadline := time.Now().Add(searchBudget)
	ctx := r.Context()

	for _, root := range roots {
		if truncated {
			break
		}
		walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				truncated = true
				return fs.SkipAll
			}
			if err != nil {
				// 读不了的目录跳过而不是整棵树失败：NAS 上到处是没权限的系统目录。
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if p == root {
				return nil
			}
			name := d.Name()
			// 隐藏项与飞牛自己的目录都不进结果——列表里看不见的东西，
			// 搜索里露出来就等于绕过了那条规则。
			if strings.HasPrefix(name, ".") || isInternalEntry(name, filepath.Dir(p)) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			scanned++
			if scanned > searchMaxScanned || time.Now().After(deadline) {
				truncated = true
				return fs.SkipAll
			}
			if !matchAllTokens(strings.ToLower(name), tokens) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				rel = name
			}
			hits = append(hits, searchEntry{
				Name:  name,
				Path:  p,
				Dir:   filepath.Dir(p),
				Rel:   filepath.ToSlash(rel),
				IsDir: d.IsDir(),
				Size:  info.Size(),
			})
			if len(hits) >= limit {
				truncated = true
				return fs.SkipAll
			}
			return nil
		})
		_ = walkErr
	}
	return hits, scanned, truncated
}

// searchTokens 把查询串切成小写词。
//
// 多个词是"与"的关系：搜 "2024 报告" 只出名字里同时含这两个词的文件。空查询
// 切出零个词，调用方据此直接返回空结果。
func searchTokens(query string) []string {
	fields := strings.Fields(strings.ToLower(query))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// matchAllTokens 要求每个词都是文件名的子串。
func matchAllTokens(lowerName string, tokens []string) bool {
	for _, tok := range tokens {
		if !strings.Contains(lowerName, tok) {
			return false
		}
	}
	return len(tokens) > 0
}

// searchLimit 读 limit 参数并夹到上限之间。
func searchLimit(r *http.Request) int {
	limit := searchDefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > searchMaxLimit {
		limit = searchMaxLimit
	}
	return limit
}
