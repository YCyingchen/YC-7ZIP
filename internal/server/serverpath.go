package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ycyingchen/yc-7zip/internal/engine"
)

// resolveServerArchive validates a host path and resolves it to the file
// 7-Zip must actually open.
//
// When the user picks a part of a split archive — which is exactly what they
// will do, because the file manager shows every part — the entry point is
// volume 1, not the part they clicked. An incomplete series is refused here
// with the names of the missing parts rather than left for 7-Zip to report as
// a corrupt archive.
func (s *Server) resolveServerArchive(p string) (string, *engine.VolumeSet, error) {
	abs, err := s.validateServerPath(p, true)
	if err != nil {
		return "", nil, err
	}

	set, ok, err := engine.ResolveVolumes(abs)
	if err != nil {
		return "", nil, err
	}
	if !ok {
		return abs, nil, nil
	}
	if !set.Complete() {
		return "", set, badRequest("分卷不完整，缺少 %d 个分卷：%s",
			len(set.Missing), strings.Join(shortNames(set.Missing, 5), "、"))
	}
	if set.First == "" || !fileExists(set.First) {
		return "", set, badRequest("找不到第一卷，无法解压分卷压缩包")
	}
	return set.First, set, nil
}

// shortNames basenames a list and truncates it for a readable message.
func shortNames(paths []string, max int) []string {
	out := make([]string, 0, len(paths))
	for i, p := range paths {
		if i >= max {
			out = append(out, fmt.Sprintf("…（共 %d 个）", len(paths)))
			break
		}
		out = append(out, filepath.Base(p))
	}
	return out
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// validateServerPath checks a host path against the configured allow-roots.
// requireExist makes the existence check mandatory for inputs.
func (s *Server) validateServerPath(p string, requireExist bool) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", badRequest("路径为空")
	}
	if len(s.cfg.AllowRoots) == 0 {
		return "", forbidden("服务端未开放任何目录，请在启动参数中用 -allow-root 指定")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", badRequest("路径无效：%s", p)
	}
	abs = filepath.Clean(abs)
	if err := s.checkAllowed(abs); err != nil {
		return "", err
	}
	if requireExist {
		if _, err := os.Stat(abs); err != nil {
			return "", badRequest("路径不存在：%s", abs)
		}
	}
	return abs, nil
}

// validateServerOutputDir checks that a job may write into dir, creating it
// when asked. Writing outside the allow-roots is refused, so a request can
// never reach a directory the operator did not expose.
func (s *Server) validateServerOutputDir(dir string, create bool) (string, error) {
	abs, err := s.validateServerPath(dir, false)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(abs)
	switch {
	case err == nil && !st.IsDir():
		return "", badRequest("输出路径不是目录：%s", abs)
	case err == nil:
		return abs, nil
	case os.IsNotExist(err) && create:
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return "", fmt.Errorf("无法创建输出目录：%w", err)
		}
		return abs, nil
	default:
		return "", badRequest("输出目录不可用：%s", abs)
	}
}

// outputInsideSources reports whether writing into outDir would place the
// result inside one of the inputs.
//
// Compressing /data/photos to /data/photos/backup.7z makes 7-Zip read the
// directory it is simultaneously writing into, which yields an archive that
// contains a snapshot of itself. Refusing is the only correct answer.
func outputInsideSources(outDir string, sources []string) bool {
	out := filepath.Clean(outDir)
	for _, src := range sources {
		st, err := os.Stat(src)
		if err != nil || !st.IsDir() {
			continue
		}
		clean := filepath.Clean(src)
		if out == clean || strings.HasPrefix(out, clean+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// snapshotTopLevel lists the immediate child names of a directory.
func snapshotTopLevel(dir string) map[string]bool {
	seen := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return seen
	}
	for _, e := range entries {
		seen[e.Name()] = true
	}
	return seen
}

// diffTopLevel returns the entries present now but not in the snapshot, which
// is how an extraction reports what it actually created without having to
// predict 7-Zip's output.
func diffTopLevel(dir string, before map[string]bool) []string {
	var added []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !before[e.Name()] {
			added = append(added, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(added)
	return added
}

// inspectResponse describes a host path well enough for the UI to label it.
type inspectResponse struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	IsDir     bool   `json:"is_dir"`
	Size      int64  `json:"size"`
	Children  int    `json:"children,omitempty"`
	Format    string `json:"format,omitempty"`
	FormatTag string `json:"format_label,omitempty"`
	// Creatable reports whether the detected format can be produced.
	Creatable bool `json:"creatable"`
	// Volume is present only for split archive parts.
	Volume *volumeInfo `json:"volume,omitempty"`
	// SuggestedOutputDir is where a server-side job would write by default.
	SuggestedOutputDir string `json:"suggested_output_dir,omitempty"`
	// SuggestedSubdir is the alternative "extract into a folder named after the
	// archive" target, for archives that hold loose files rather than one
	// enclosing directory.
	SuggestedSubdir string `json:"suggested_subdir,omitempty"`
	// Reason explains why the path cannot be used, when that is the case.
	Reason string `json:"reason,omitempty"`
}

type volumeInfo struct {
	IsSet   bool     `json:"is_set"`
	Label   string   `json:"label"`
	First   string   `json:"first"`
	Count   int      `json:"count"`
	Missing []string `json:"missing,omitempty"`
	Scheme  string   `json:"scheme,omitempty"`
}

// handleInspect labels one host path: format, split volumes, and a sensible
// default output location. The UI calls it whenever the selection changes.
func (s *Server) handleInspect(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("path")
	abs, err := s.validateServerPath(raw, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	st, err := os.Stat(abs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "无法读取路径："+err.Error())
		return
	}

	resp := inspectResponse{Path: abs, Name: filepath.Base(abs), IsDir: st.IsDir(), Size: st.Size()}

	if st.IsDir() {
		if entries, err := os.ReadDir(abs); err == nil {
			resp.Children = len(entries)
		}
		resp.SuggestedOutputDir = abs
		writeJSON(w, http.StatusOK, resp)
		return
	}

	if format, ok := engine.ExtensionToFormat(filepath.Base(abs)); ok {
		resp.Format = format.ID
		resp.FormatTag = format.Label
		resp.Creatable = format.Capability == engine.CreateAndExtract
	}

	set, ok, err := engine.ResolveVolumes(abs)
	if err == nil && ok {
		resp.Volume = &volumeInfo{
			IsSet:   true,
			Label:   set.Label(),
			First:   set.First,
			Count:   len(set.Volumes),
			Missing: shortNames(set.Missing, 10),
			Scheme:  set.Scheme,
		}
		if !set.Complete() {
			resp.Reason = fmt.Sprintf("分卷不完整，缺少 %d 个分卷", len(set.Missing))
		}
	}

	// Extracting conventionally lands in the archive's own directory, which
	// avoids the "photos/photos" nesting that an archive holding a single
	// top-level folder would otherwise produce. The UI shows this path so the
	// user always sees where the result will go.
	base := filepath.Base(abs)
	if set != nil {
		base = filepath.Base(set.First)
	}
	stem := base
	if format, ok := engine.ExtensionToFormat(base); ok {
		stem = strings.TrimSuffix(base, format.Extension)
	}
	resp.SuggestedOutputDir = filepath.Dir(abs)
	resp.SuggestedSubdir = filepath.Join(filepath.Dir(abs), stem)

	writeJSON(w, http.StatusOK, resp)
}
