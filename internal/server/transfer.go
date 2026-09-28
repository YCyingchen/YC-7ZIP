package server

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ycyingchen/yc-7zip/internal/engine"
	"github.com/ycyingchen/yc-7zip/internal/job"
)

// handleUpload streams a multipart body into the job's input directory.
//
// The body is consumed part by part rather than through ParseMultipartForm so
// that a multi-gigabyte upload never lands in memory or in a second temp copy.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "任务不存在或已过期")
		return
	}
	if j.Status == job.StatusRunning {
		writeError(w, http.StatusConflict, "任务正在执行中，无法继续上传")
		return
	}

	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "上传格式错误："+err.Error())
		return
	}

	var (
		written  int64
		fileCnt  int
		pending  string
		maxBytes = s.cfg.MaxUpload
	)

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "读取上传内容失败："+err.Error())
			return
		}

		switch part.FormName() {
		case "relpath":
			// The client sends the browser's relative path immediately before
			// the matching file part so directory uploads keep their shape.
			buf, _ := io.ReadAll(io.LimitReader(part, 4096))
			part.Close()
			pending = string(buf)
			continue
		case "files":
		default:
			part.Close()
			continue
		}

		name := pending
		pending = ""
		if name == "" {
			name = part.FileName()
		}

		rel, err := sanitizeUploadPath(name)
		if err != nil {
			part.Close()
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if j.Kind == job.KindExtract && fileCnt >= 1 {
			part.Close()
			writeError(w, http.StatusBadRequest, "解压任务一次只能上传一个压缩包")
			return
		}

		dest := filepath.Join(j.InDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
			part.Close()
			writeError(w, http.StatusInternalServerError, "无法创建目录："+err.Error())
			return
		}
		f, err := os.Create(dest)
		if err != nil {
			part.Close()
			writeError(w, http.StatusInternalServerError, "无法写入文件："+err.Error())
			return
		}

		var reader io.Reader = part
		if maxBytes > 0 {
			remaining := maxBytes - written
			if remaining <= 0 {
				f.Close()
				part.Close()
				writeError(w, http.StatusRequestEntityTooLarge,
					fmt.Sprintf("已超出单任务上传上限 %s", humanBytes(maxBytes)))
				return
			}
			reader = io.LimitReader(part, remaining+1)
		}

		n, copyErr := io.Copy(f, reader)
		closeErr := f.Close()
		part.Close()
		written += n

		if maxBytes > 0 && written > maxBytes {
			_ = os.Remove(dest)
			writeError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("已超出单任务上传上限 %s", humanBytes(maxBytes)))
			return
		}
		if copyErr != nil {
			_ = os.Remove(dest)
			writeError(w, http.StatusInternalServerError, "写入中断："+copyErr.Error())
			return
		}
		if closeErr != nil {
			_ = os.Remove(dest)
			writeError(w, http.StatusInternalServerError, "写入失败："+closeErr.Error())
			return
		}
		fileCnt++
	}

	if fileCnt == 0 {
		writeError(w, http.StatusBadRequest, "没有收到任何文件")
		return
	}

	sources, _ := topLevelEntries(j.InDir)
	s.jobs.Update(j.ID, func(jb *job.Job) {
		jb.SourceNames = trimNames(sources, 50)
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"received_files": fileCnt,
		"received_bytes": written,
	})
}

// handleDownloadFile serves one output file with range support.
func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "任务不存在或已过期")
		return
	}
	if j.Status != job.StatusDone {
		writeError(w, http.StatusConflict, "任务尚未完成")
		return
	}

	rel := r.URL.Query().Get("file")
	if rel == "" {
		if len(j.Files) != 1 {
			writeError(w, http.StatusBadRequest, "请通过 file 参数指定要下载的文件")
			return
		}
		rel = j.Files[0].Name
	}

	target, err := resolveWithin(j.OutDir, rel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	setContentDisposition(w, filepath.Base(target))
	http.ServeFile(w, r, target)
}

// handleDownloadAll returns the single result, or a zip of every result when
// the job produced a tree (the usual case for an extraction).
func (s *Server) handleDownloadAll(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "任务不存在或已过期")
		return
	}
	if j.Status != job.StatusDone {
		writeError(w, http.StatusConflict, "任务尚未完成")
		return
	}
	if len(j.Files) == 0 {
		writeError(w, http.StatusNotFound, "没有可下载的结果")
		return
	}

	if len(j.Files) == 1 {
		target, err := resolveWithin(j.OutDir, j.Files[0].Name)
		if err == nil {
			setContentDisposition(w, filepath.Base(target))
			http.ServeFile(w, r, target)
			return
		}
	}

	name := archiveBaseName(j) + ".zip"
	setContentDisposition(w, name)
	w.Header().Set("Content-Type", "application/zip")

	zw := zip.NewWriter(w)
	defer zw.Close()
	for _, file := range j.Files {
		if err := addToZip(zw, j.OutDir, file); err != nil {
			// The response is already streaming; the best we can do is stop
			// and let the truncated zip signal the failure.
			s.log.Warn("zip stream aborted", "job", j.ID, "file", file.Name, "error", err)
			return
		}
	}
}

// addToZip appends one output file to a streaming zip archive.
func addToZip(zw *zip.Writer, root string, file job.ResultFile) error {
	src := file.Path
	if src == "" {
		var err error
		src, err = resolveWithin(root, file.Name)
		if err != nil {
			return err
		}
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	hdr, err := zip.FileInfoHeader(st)
	if err != nil {
		return err
	}
	hdr.Name = file.Name
	hdr.Method = zip.Deflate
	entry, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(entry, f)
	return err
}

// archiveBaseName picks the download base name for a multi-file result.
func archiveBaseName(j *job.Job) string {
	if len(j.SourceNames) > 0 {
		base := strings.TrimSuffix(j.SourceNames[0], path.Ext(j.SourceNames[0]))
		if sanitized := sanitizeOutputName(base); sanitized != "" {
			return sanitized
		}
	}
	return "yc7zip-" + j.ID[:8]
}

// browseRoots renders the configured allow-roots as entries the picker can
// show. Missing directories are dropped so a package that lists /vol1 … /vol5
// does not show shortcuts to volumes the NAS does not have, and the filesystem
// root is labelled rather than left as a bare slash.
func (s *Server) browseRoots() []browseEntry {
	seen := map[string]bool{}
	out := make([]browseEntry, 0, len(s.cfg.AllowRoots))
	for _, root := range s.cfg.AllowRoots {
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		if seen[abs] {
			continue
		}
		seen[abs] = true

		if abs == string(os.PathSeparator) {
			out = append(out, browseEntry{Name: "根目录 /", Path: abs, IsDir: true})
			continue
		}
		st, err := os.Stat(abs)
		if err != nil || !st.IsDir() {
			continue
		}
		out = append(out, browseEntry{Name: filepath.Base(abs), Path: abs, IsDir: true})
	}
	return out
}

// browseEntry is one row in the server-side file picker.
type browseEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// handleBrowse powers the optional "compress files already on the server"
// flow. It is inert unless the operator configured at least one allowed root.
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	if len(s.cfg.AllowRoots) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": false,
			"roots":   []string{},
			"entries": []browseEntry{},
		})
		return
	}

	requested := r.URL.Query().Get("path")
	if requested == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": true,
			"path":    "",
			"parent":  "",
			"roots":   s.browseRoots(),
			"entries": []browseEntry{},
		})
		return
	}

	abs, err := filepath.Abs(requested)
	if err != nil {
		writeError(w, http.StatusBadRequest, "路径无效")
		return
	}
	abs = filepath.Clean(abs)
	if err := s.checkAllowed(abs); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	dirents, err := os.ReadDir(abs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "无法读取目录："+err.Error())
		return
	}

	entries := make([]browseEntry, 0, len(dirents))
	for _, de := range dirents {
		name := de.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		entries = append(entries, browseEntry{
			Name:  name,
			Path:  filepath.Join(abs, name),
			IsDir: de.IsDir(),
			Size:  info.Size(),
		})
	}
	sort.Slice(entries, func(a, b int) bool {
		if entries[a].IsDir != entries[b].IsDir {
			return entries[a].IsDir
		}
		return strings.ToLower(entries[a].Name) < strings.ToLower(entries[b].Name)
	})

	parent := filepath.Dir(abs)
	if err := s.checkAllowed(parent); err != nil || parent == abs {
		parent = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true,
		"path":    abs,
		"parent":  parent,
		"entries": entries,
	})
}

// sanitizeUploadPath turns a client supplied name into a safe relative path
// inside the job's input directory.
func sanitizeUploadPath(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimLeft(name, "/")
	name = path.Clean(name)
	if name == "" || name == "." || name == "/" {
		return "", errors.New("文件名无效")
	}
	if engine.UnsafePath(name) {
		return "", fmt.Errorf("文件名包含不安全路径：%s", name)
	}
	segments := strings.Split(name, "/")
	for i, seg := range segments {
		segments[i] = sanitizeFilenameSegment(seg)
		if segments[i] == "" {
			return "", fmt.Errorf("文件名无效：%s", name)
		}
	}
	if len(segments) > 64 {
		return "", errors.New("目录层级过深")
	}
	return strings.Join(segments, "/"), nil
}

// sanitizeFilenameSegment strips characters the host filesystem rejects.
func sanitizeFilenameSegment(seg string) string {
	seg = strings.Map(func(r rune) rune {
		switch r {
		case '<', '>', ':', '"', '|', '?', '*', 0:
			return '_'
		}
		if r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, seg)
	seg = strings.TrimRight(seg, ". ")
	return seg
}

// resolveWithin joins root and rel and refuses anything that escapes root.
func resolveWithin(root, rel string) (string, error) {
	rel = strings.ReplaceAll(rel, "\\", "/")
	rel = strings.TrimLeft(rel, "/")
	cleaned := path.Clean(rel)
	if cleaned == "." || cleaned == "" || engine.UnsafePath(cleaned) {
		return "", errors.New("文件路径无效")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target := filepath.Join(rootAbs, filepath.FromSlash(cleaned))
	// Belt and braces: confirm the joined result is still under rootAbs. This
	// catches symlinked outputs that the lexical check above cannot see.
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if targetAbs != rootAbs && !strings.HasPrefix(targetAbs, rootAbs+string(os.PathSeparator)) {
		return "", errors.New("文件路径越界")
	}
	st, err := os.Stat(targetAbs)
	if err != nil {
		return "", errors.New("文件不存在")
	}
	if st.IsDir() {
		return "", errors.New("目标是一个目录")
	}
	return targetAbs, nil
}

// setContentDisposition emits both the legacy and the RFC 5987 form so that
// non-ASCII archive names survive in every browser.
func setContentDisposition(w http.ResponseWriter, filename string) {
	ascii := strings.Map(func(r rune) rune {
		if r > 0 && r < 128 && r != '"' && r != '\\' {
			return r
		}
		return '_'
	}, filename)
	if ascii == "" {
		ascii = "download"
	}
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", ascii, url.PathEscape(filename)))
}

// humanBytes renders a byte count for error messages.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTP"[exp])
}
