package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/ycyingchen/yc-7zip/internal/engine"
	"github.com/ycyingchen/yc-7zip/internal/job"
)

// handleHealth reports liveness plus the capabilities the UI adapts to.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"version":        s.cfg.Version,
		"uptime_seconds": int(time.Since(s.start).Seconds()),
		"engine": map[string]any{
			"path":        s.engine.BinPath,
			"version":     s.engine.Version,
			"unrar":       s.engine.Unrar,
			"create":      creatableFormats(),
			"extract_all": true,
		},
		"limits": map[string]any{
			"max_upload_bytes": s.cfg.MaxUpload,
			"job_ttl_seconds":  int(s.cfg.JobTTL.Seconds()),
			"threads":          runtime.NumCPU(),
		},
		"auth_enabled": s.cfg.Auth != "",
		"allow_roots":  s.cfg.AllowRoots,
	})
}

func creatableFormats() []string {
	var out []string
	for _, f := range engine.Formats {
		if f.Capability == engine.CreateAndExtract {
			out = append(out, f.ID)
		}
	}
	return out
}

// handleFormats returns the format registry.
func (s *Server) handleFormats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"formats": engine.Formats})
}

// handleCreateJob reserves a workspace.
func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	kind := job.Kind(r.URL.Query().Get("kind"))
	if kind != job.KindCompress && kind != job.KindExtract {
		writeError(w, http.StatusBadRequest, "kind 必须是 compress 或 extract")
		return
	}
	j, err := s.jobs.Create(kind)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法创建工作区："+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, j)
}

// handleJobStatus is polled by the UI for progress.
func (s *Server) handleJobStatus(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "任务不存在或已过期")
		return
	}
	writeJSON(w, http.StatusOK, j)
}

// handleDeleteJob drops the job and frees its disk space.
func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	if !s.jobs.Delete(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "任务不存在")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCancelJob stops a running job.
func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.jobs.Get(id); !ok {
		writeError(w, http.StatusNotFound, "任务不存在")
		return
	}
	if !s.jobs.Cancel(id) {
		writeError(w, http.StatusConflict, "任务已结束，无法取消")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// runRequest is the body of POST /api/jobs/{id}/run.
type runRequest struct {
	// SourcePaths switches the input from uploaded files to paths that already
	// exist on the host, which is the primary mode for a NAS deployment. Each
	// path must fall inside an allowed root.
	SourcePaths []string `json:"source_paths"`

	// OutputMode is "download" (default) or "server".
	OutputMode string `json:"output_mode"`
	// OutputDir is the host directory a server-side job writes into.
	OutputDir string `json:"output_dir"`

	// Compression options.
	Format       string `json:"format"`
	Level        int    `json:"level"`
	Password     string `json:"password"`
	EncryptNames bool   `json:"encrypt_names"`
	VolumeSize   string `json:"volume_size"`
	Name         string `json:"name"`
	Threads      int    `json:"threads"`
	Solid        bool   `json:"solid"`

	// Extraction options.
	Selected      []string `json:"selected"`
	PreservePaths *bool    `json:"preserve_paths"`
	Overwrite     *bool    `json:"overwrite"`
}

// handleRun validates a request and starts the operation asynchronously.
func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "任务不存在或已过期")
		return
	}
	if j.Status == job.StatusRunning {
		writeError(w, http.StatusConflict, "任务正在执行中")
		return
	}

	var req runRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误："+err.Error())
		return
	}

	switch j.Kind {
	case job.KindCompress:
		if err := s.runCompress(j, &req); err != nil {
			// Validation problems are reported synchronously so the UI can
			// point at the offending field.
			writeEngineError(w, err)
			return
		}
	case job.KindExtract:
		if err := s.runExtract(j, &req); err != nil {
			writeEngineError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusAccepted, j)
}

// runCompress validates options and launches the packer.
func (s *Server) runCompress(j *job.Job, req *runRequest) error {
	if req.Format == "" {
		req.Format = "7z"
	}
	format, ok := engine.FormatByID(req.Format)
	if !ok {
		return badRequest("不支持的压缩格式：%s", req.Format)
	}
	if format.Capability != engine.CreateAndExtract {
		return badRequest("%s 只能解压，无法创建", format.Label)
	}
	if req.Password != "" && !format.SupportsPassword {
		// Silently dropping a password would be worse than refusing: the user
		// would ship plaintext believing it was encrypted.
		return badRequest("%s 不支持加密，请改用 7z 或 ZIP", format.Label)
	}
	if req.Level < -1 || req.Level > 9 {
		return badRequest("压缩级别必须在 0-9 之间")
	}
	if req.VolumeSize != "" && !format.SupportsVolume {
		return badRequest("%s 不支持分卷", format.Label)
	}
	if req.VolumeSize != "" {
		if _, err := parseVolumeSize(req.VolumeSize); err != nil {
			return badRequest("%s", err.Error())
		}
	}

	var (
		workDir string
		sources []string
		err     error
	)
	if len(req.SourcePaths) > 0 {
		workDir, sources, err = s.resolveServerSources(req.SourcePaths)
	} else {
		workDir = j.InDir
		sources, err = topLevelEntries(j.InDir)
	}
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return badRequest("没有可压缩的文件，请先选择文件")
	}

	mode := job.OutputMode(req.OutputMode)
	if mode == "" {
		mode = job.OutputDownload
	}
	outputDir := j.OutDir
	switch mode {
	case job.OutputDownload:
		if err := resetDir(outputDir); err != nil {
			return err
		}
	case job.OutputServer:
		outputDir, err = s.validateServerOutputDir(req.OutputDir, true)
		if err != nil {
			return err
		}
		// 7-Zip would otherwise read the directory it is writing into and
		// archive a snapshot of its own output.
		absSources := make([]string, 0, len(sources))
		for _, name := range sources {
			absSources = append(absSources, filepath.Join(workDir, name))
		}
		if outputInsideSources(outputDir, absSources) {
			return badRequest("输出目录在待压缩目录内部，会造成自我包含，请换一个输出目录")
		}
	default:
		return badRequest("未知的输出方式：%s", req.OutputMode)
	}

	name := sanitizeOutputName(req.Name)
	if name == "" {
		name = defaultArchiveName(sources)
	}

	overwrite := true
	if req.Overwrite != nil {
		overwrite = *req.Overwrite
	}

	opts := engine.CompressOptions{
		Format:       format.ID,
		Level:        req.Level,
		Password:     req.Password,
		EncryptNames: req.EncryptNames,
		VolumeSize:   req.VolumeSize,
		WorkDir:      workDir,
		Sources:      sources,
		OutputName:   name,
		OutputDir:    outputDir,
		Threads:      req.Threads,
		Solid:        req.Solid,
		Overwrite:    overwrite,
	}

	s.jobs.Update(j.ID, func(jb *job.Job) {
		jb.OutputMode = mode
		jb.OutputDir = outputDir
		jb.SourceNames = trimNames(displayNames(workDir, sources), 50)
	})

	s.launch(j, func(ctx context.Context) error {
		out, err := s.engine.Compress(ctx, opts, progressBridge(s.jobs, j.ID))
		if err != nil {
			return err
		}
		s.recordServerOutput(j.ID, out)
		return nil
	})
	return nil
}

// runExtract validates options and launches the unpacker.
//
// The archive index is read synchronously, before the job is handed to the
// background worker. That is what lets a traversal attempt, a bad password or
// an incomplete volume series come back as a real HTTP error instead of a job
// that silently dies after the caller has already been told 202.
func (s *Server) runExtract(j *job.Job, req *runRequest) error {
	var (
		archivePath string
		set         *engine.VolumeSet
		err         error
	)
	if len(req.SourcePaths) > 0 {
		if len(req.SourcePaths) != 1 {
			return badRequest("一次只能解压一个压缩包（分卷会自动收集）")
		}
		archivePath, set, err = s.resolveServerArchive(req.SourcePaths[0])
	} else {
		archivePath, err = s.singleArchive(j)
	}
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	info, err := s.engine.List(ctx, archivePath, req.Password, nil)
	if err != nil {
		return err
	}
	if info.NeedsPassword && req.Password == "" {
		return engine.ErrPasswordRequired
	}
	for _, entry := range info.Entries {
		if engine.UnsafePath(entry.Path) {
			return fmt.Errorf("%w：%s", engine.ErrUnsafeEntry, entry.Path)
		}
	}
	// Formats without header encryption list their members even when locked,
	// so verify the password against one member now rather than letting the
	// job start and fail a minute later.
	if encrypted := engine.EncryptedMembers(info); len(encrypted) > 0 && req.Password != "" {
		if err := s.engine.CheckPassword(ctx, archivePath, req.Password, encrypted[0]); err != nil {
			return err
		}
	}

	preserve := true
	if req.PreservePaths != nil {
		preserve = *req.PreservePaths
	}
	overwrite := true
	if req.Overwrite != nil {
		overwrite = *req.Overwrite
	}

	mode := job.OutputMode(req.OutputMode)
	if mode == "" {
		mode = job.OutputDownload
	}
	outputDir := j.OutDir
	var before map[string]bool
	switch mode {
	case job.OutputDownload:
		if err := resetDir(outputDir); err != nil {
			return err
		}
	case job.OutputServer:
		outputDir, err = s.validateServerOutputDir(req.OutputDir, true)
		if err != nil {
			return err
		}
		// An extraction reports what it created by diffing the directory, so
		// pre-existing neighbours are not mistaken for its own output.
		before = snapshotTopLevel(outputDir)
	default:
		return badRequest("未知的输出方式：%s", req.OutputMode)
	}

	opts := engine.ExtractOptions{
		Archive:       archivePath,
		OutputDir:     outputDir,
		Password:      req.Password,
		Selected:      req.Selected,
		PreservePaths: preserve,
		Overwrite:     overwrite,
	}

	sourceLabel := filepath.Base(archivePath)
	volumeLabel := ""
	if set != nil {
		volumeLabel = set.Label()
	}
	s.jobs.Update(j.ID, func(jb *job.Job) {
		jb.Archive = info
		jb.OutputMode = mode
		jb.OutputDir = outputDir
		jb.VolumeLabel = volumeLabel
		jb.SourceNames = []string{sourceLabel}
	})

	s.launch(j, func(ctx context.Context) error {
		if _, err := s.engine.Extract(ctx, opts, progressBridge(s.jobs, j.ID)); err != nil {
			return err
		}
		if mode == job.OutputServer {
			s.recordServerOutput(j.ID, diffTopLevel(outputDir, before))
		}
		return nil
	})
	return nil
}

// recordServerOutput stores the host paths a server-side job produced and
// keeps TotalBytes meaningful for the UI.
func (s *Server) recordServerOutput(id string, paths []string) {
	var total int64
	for _, p := range paths {
		size := dirOrFileSize(p)
		total += size
	}
	s.jobs.Update(id, func(j *job.Job) {
		j.ServerPaths = paths
		j.TotalBytes = total
	})
}

// dirOrFileSize totals a path, walking directories.
func dirOrFileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	if !st.IsDir() {
		return st.Size()
	}
	var total int64
	_ = filepath.Walk(p, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// displayNames renders sources as they should appear in the UI, which for
// host paths is the full path rather than the relative member name.
func displayNames(workDir string, sources []string) []string {
	out := make([]string, 0, len(sources))
	for _, name := range sources {
		if filepath.IsAbs(name) {
			out = append(out, name)
			continue
		}
		out = append(out, filepath.Join(workDir, name))
	}
	return out
}

// handlePreview lists an archive's contents so the UI can offer selection.
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "任务不存在或已过期")
		return
	}
	if j.Kind != job.KindExtract {
		writeError(w, http.StatusBadRequest, "仅解压任务支持预览")
		return
	}
	var body struct {
		Password string `json:"password"`
		// Path reads a server-side archive instead of an uploaded one.
		Path string `json:"path"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)

	var (
		archivePath string
		set         *engine.VolumeSet
		err         error
	)
	if body.Path != "" {
		archivePath, set, err = s.resolveServerArchive(body.Path)
	} else {
		archivePath, err = s.singleArchive(j)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	info, err := s.engine.List(ctx, archivePath, body.Password, nil)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	s.jobs.Update(j.ID, func(jb *job.Job) {
		jb.Archive = info
		jb.SourceNames = []string{filepath.Base(archivePath)}
		if set != nil {
			jb.VolumeLabel = set.Label()
		}
	})
	writeJSON(w, http.StatusOK, info)
}

// launch runs fn in the background under a cancellable context and records the
// terminal state.
func (s *Server) launch(j *job.Job, fn func(context.Context) error) {
	// The context outlives the HTTP request on purpose: the client polls for
	// status rather than holding the connection open for a long pack.
	ctx, cancel := context.WithCancel(context.Background())
	s.jobs.Begin(j.ID, cancel)

	go func() {
		defer cancel()
		err := fn(ctx)
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return // Cancel already recorded the state.
			}
			s.log.Warn("job failed", "job", j.ID, "error", err)
			s.jobs.Fail(j.ID, err)
			return
		}
		// A server-side job reports what it wrote through ServerPaths, so
		// scanning the (unused) workspace would only ever report nothing.
		if jb, ok := s.jobs.Get(j.ID); !ok || jb.OutputMode != job.OutputServer {
			if err := s.jobs.CollectOutputs(j.ID); err != nil {
				s.jobs.Fail(j.ID, err)
				return
			}
		}
		s.jobs.Finish(j.ID)
	}()
}

// progressBridge converts engine progress into job updates.
func progressBridge(m *job.Manager, id string) engine.ProgressFunc {
	var lastStored time.Time
	return func(p engine.Progress) {
		// 7-Zip emits progress far faster than a browser needs; throttling
		// keeps the job map from becoming a contention point.
		if time.Since(lastStored) < 250*time.Millisecond && p.Percent < 100 {
			return
		}
		lastStored = time.Now()
		m.Update(id, func(j *job.Job) {
			j.Progress = p.Percent
			if p.Stage != "" {
				j.Stage = p.Stage
			}
		})
	}
}

// singleArchive returns the one archive inside an extract job's input dir.
func (s *Server) singleArchive(j *job.Job) (string, error) {
	var found []string
	err := filepath.Walk(j.InDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		found = append(found, p)
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(found) == 0 {
		return "", errors.New("请先上传一个压缩包")
	}
	if len(found) > 1 {
		return "", errors.New("一次只能解压一个压缩包，请重新创建任务")
	}
	return found[0], nil
}

// resetDir empties a directory and recreates it.
//
// A retry reuses the same job workspace, so leftovers from the previous attempt
// would otherwise be reported as part of the new result. Files are unlinked
// rather than the whole tree removed so the path itself stays stable.
func resetDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(dir, 0o750)
		}
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return fmt.Errorf("无法清理工作目录：%w", err)
		}
	}
	return nil
}

// topLevelEntries lists the immediate children of dir, which are exactly the
// members to store at the archive root.
func topLevelEntries(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		// Skip the private scratch files the server writes next to uploads.
		if strings.HasPrefix(name, ".yc7zip-") {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// resolveServerSources validates host paths against the allowed roots and
// derives the working directory that keeps stored paths relative.
func (s *Server) resolveServerSources(paths []string) (string, []string, error) {
	if len(s.cfg.AllowRoots) == 0 {
		return "", nil, forbidden("服务器端未开放任何目录，请改用上传文件")
	}
	var cleaned []string
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", nil, badRequest("路径无效：%s", p)
		}
		abs = filepath.Clean(abs)
		if err := s.checkAllowed(abs); err != nil {
			return "", nil, err
		}
		if _, err := os.Stat(abs); err != nil {
			return "", nil, badRequest("路径不存在：%s", abs)
		}
		cleaned = append(cleaned, abs)
	}
	// The working directory is the common parent of the sources, so the stored
	// member names stay short and relative while still keeping a selected
	// folder as a folder inside the archive.
	parents := make([]string, 0, len(cleaned))
	for _, p := range cleaned {
		parents = append(parents, filepath.Dir(p))
	}
	workDir := commonDir(parents)

	rel := make([]string, 0, len(cleaned))
	for _, p := range cleaned {
		r, err := filepath.Rel(workDir, p)
		if err != nil {
			return "", nil, badRequest("无法确定这些路径的公共父目录：%s", strings.Join(shortNames(cleaned, 3), "、"))
		}
		rel = append(rel, r)
	}
	return workDir, rel, nil
}

// checkAllowed enforces the allow-root boundary.
func (s *Server) checkAllowed(abs string) error {
	for _, root := range s.cfg.AllowRoots {
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		rootAbs = filepath.Clean(rootAbs)
		if abs == rootAbs || strings.HasPrefix(abs, rootAbs+string(os.PathSeparator)) {
			return nil
		}
	}
	return forbidden("路径不在允许范围内：%s", abs)
}

// commonDir returns the deepest directory that contains every path in dirs.
//
// It is built on filepath.Rel rather than on string splitting: splitting and
// rejoining silently drops the leading separator, which turns an absolute
// working directory into a relative one and makes every later Rel call fail.
func commonDir(dirs []string) string {
	if len(dirs) == 0 {
		return string(os.PathSeparator)
	}
	current := filepath.Clean(dirs[0])
	for _, dir := range dirs[1:] {
		target := filepath.Clean(dir)
		for {
			rel, err := filepath.Rel(current, target)
			// rel == "." means same path; anything not starting with ".."
			// means target is inside current.
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				break
			}
			parent := filepath.Dir(current)
			if parent == current {
				break // reached the root and it still does not contain target
			}
			current = parent
		}
	}
	return current
}

// parseVolumeSize accepts 7-Zip volume sizes such as "100m", "2g" and "512k".
func parseVolumeSize(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "", nil
	}
	last := v[len(v)-1]
	digits := v
	unit := ""
	if last == 'b' && len(v) >= 2 {
		// "100mb" style suffixes are common in the UI.
		unit = string(v[len(v)-2])
		digits = v[:len(v)-2]
	} else if last < '0' || last > '9' {
		unit = string(last)
		digits = v[:len(v)-1]
	}
	switch unit {
	case "", "b", "k", "m", "g", "t":
	default:
		return "", fmt.Errorf("分卷大小单位无效：%s（可用 k / m / g）", v)
	}
	if digits == "" {
		return "", fmt.Errorf("分卷大小无效：%s", v)
	}
	var n int64
	for _, ch := range digits {
		if ch < '0' || ch > '9' {
			return "", fmt.Errorf("分卷大小无效：%s", v)
		}
		n = n*10 + int64(ch-'0')
	}
	if n <= 0 {
		return "", errors.New("分卷大小必须大于 0")
	}
	return digits + unit, nil
}

// sanitizeOutputName keeps an archive base name safe for the filesystem.
func sanitizeOutputName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = strings.TrimSuffix(name, path.Ext(name))
	// Strip characters that are illegal in file names on any supported OS.
	name = strings.Map(func(r rune) rune {
		switch r {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*', 0:
			return '_'
		}
		if r < 0x20 {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	if name == "" || name == "." || name == ".." {
		return ""
	}
	if len(name) > 100 {
		name = name[:100]
	}
	return name
}

// defaultArchiveName derives a sensible base name from the payload.
func defaultArchiveName(sources []string) string {
	switch {
	case len(sources) == 1:
		base := path.Base(strings.ReplaceAll(sources[0], "\\", "/"))
		if base != "" && base != "." && base != "/" {
			return sanitizeOutputName(base)
		}
	case len(sources) > 1:
		return sanitizeOutputName(fmt.Sprintf("archive-%d-items", len(sources)))
	}
	return "archive"
}

func trimNames(names []string, max int) []string {
	if len(names) <= max {
		return names
	}
	out := append([]string{}, names[:max]...)
	return append(out, fmt.Sprintf("… 共 %d 项", len(names)))
}
