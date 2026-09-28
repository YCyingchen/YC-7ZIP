package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Entry is one member of an archive.
type Entry struct {
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	PackedSize int64     `json:"packed_size"`
	Modified   time.Time `json:"modified,omitempty"`
	IsDir      bool      `json:"is_dir"`
	Encrypted  bool      `json:"encrypted"`
	CRC        string    `json:"crc,omitempty"`
	Method     string    `json:"method,omitempty"`
}

// ArchiveInfo is the result of reading an archive's table of contents.
type ArchiveInfo struct {
	Format       string  `json:"format"`
	PhysicalSize int64   `json:"physical_size"`
	Entries      []Entry `json:"entries"`
	// NeedsPassword reports that names or contents are encrypted and the
	// supplied password (if any) did not unlock the archive.
	NeedsPassword bool `json:"needs_password"`
	// HeaderEncrypted reports that even the member names are hidden.
	HeaderEncrypted bool `json:"header_encrypted"`
	// TotalSize is the sum of uncompressed member sizes, excluding
	// directories.
	TotalSize int64 `json:"total_size"`
}

// kvSeparator is the " = " that joins a key and its value in -slt output.
const kvSeparator = " = "

// List reads an archive's table of contents.
func (e *Engine) List(ctx context.Context, archive, password string, onProgress ProgressFunc) (*ArchiveInfo, error) {
	args := []string{"l", "-slt"}
	if password != "" {
		args = append(args, "-p"+password)
	}
	args = append(args, archive)

	raw, err := e.capture(ctx, args...)
	if err != nil {
		lower := strings.ToLower(raw)
		if isPasswordFailure(lower) {
			if password == "" {
				// The header itself is encrypted and nobody has tried a
				// password yet: report the requirement so the UI can prompt.
				return &ArchiveInfo{
					Format:          strings.TrimPrefix(strings.ToLower(filepath.Ext(archive)), "."),
					NeedsPassword:   true,
					HeaderEncrypted: true,
				}, nil
			}
			// A password was supplied and still failed, so it is wrong. This
			// must be an error: reporting "needs password" here would let a
			// job start and die later instead of telling the caller now.
			return nil, fmt.Errorf("%w：密码不正确", ErrPasswordRequired)
		}
		return nil, fmt.Errorf("%s", summarizeDiag(raw))
	}

	info := parseSlt(raw)
	info.Format = inferFormat(archive, info.Format)
	for _, entry := range info.Entries {
		if entry.Encrypted {
			info.NeedsPassword = password == ""
			break
		}
	}
	return info, nil
}

func isPasswordFailure(lower string) bool {
	for _, marker := range []string{
		"wrong password", "password is incorrect", "can not open encrypted archive",
		"cannot open encrypted archive", "enter password", "encrypted archive",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// capture runs 7-Zip and returns its stdout, folding stderr into the returned
// string so callers can classify the failure.
func (e *Engine) capture(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"-sccUTF-8"}, args...)
	cmd := execCommand(ctx, e.BinPath, full...)
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	combined := out.String()
	if errBuf.Len() > 0 {
		combined += "\n" + errBuf.String()
	}
	return combined, err
}

// parseSlt turns `7z l -slt` output into structured data. The listing is a
// sequence of "Key = Value" blocks separated by blank lines; everything before
// the "----------" divider describes the archive itself.
func parseSlt(out string) *ArchiveInfo {
	info := &ArchiveInfo{}
	scanner := bufio.NewScanner(strings.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)

	inEntries := false
	current := map[string]string{}

	flush := func() {
		if len(current) == 0 {
			return
		}
		if !inEntries {
			if v, ok := current["Physical Size"]; ok {
				info.PhysicalSize, _ = strconv.ParseInt(v, 10, 64)
			}
			if v, ok := current["Type"]; ok && info.Format == "" {
				info.Format = strings.ToLower(v)
			}
			current = map[string]string{}
			return
		}
		memberPath := current["Path"]
		entry := Entry{
			Path:       memberPath,
			Size:       parseInt(current["Size"]),
			PackedSize: parseInt(current["Packed Size"]),
			IsDir: strings.TrimSpace(current["Folder"]) == "+" ||
				strings.Contains(current["Attributes"], "D") ||
				strings.HasSuffix(memberPath, "/"),
			Encrypted: strings.TrimSpace(current["Encrypted"]) == "+",
			CRC:       strings.TrimSpace(current["CRC"]),
			Method:    strings.TrimSpace(current["Method"]),
		}
		if modified, ok := current["Modified"]; ok {
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(modified), time.Local); err == nil {
				entry.Modified = t
			}
		}
		// The archive's own root folder is reported with an empty or
		// dot-only path; skip it so the UI does not show a phantom row.
		if entry.Path != "" && entry.Path != "." {
			info.Entries = append(info.Entries, entry)
			if !entry.IsDir {
				info.TotalSize += entry.Size
			}
		}
		current = map[string]string{}
	}

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "----------") {
			flush()
			inEntries = true
			continue
		}
		if trimmed == "" {
			flush()
			continue
		}
		idx := strings.Index(line, " = ")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		value := line[idx+len(kvSeparator):]
		current[key] = value
	}
	flush()
	return info
}

func parseInt(s string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func inferFormat(archive, fallback string) string {
	if f, ok := ExtensionToFormat(filepath.Base(archive)); ok {
		return f.ID
	}
	if fallback == "" {
		return "unknown"
	}
	return fallback
}

// ExtractOptions controls a single extraction.
type ExtractOptions struct {
	// Archive is an absolute path to the source archive.
	Archive string
	// OutputDir is an absolute path that must already exist.
	OutputDir string
	// Password unlocks encrypted archives.
	Password string
	// Selected limits extraction to these member paths. Empty means all.
	Selected []string
	// PreservePaths keeps the archive's directory structure. When false all
	// members are flattened into OutputDir.
	PreservePaths bool
	// Overwrite replaces existing files.
	Overwrite bool
}

// Extract unpacks an archive into OutputDir.
//
// Every member name is validated first: a name that is absolute or contains a
// ".." segment is rejected, because 7-Zip's own sanitisation has historically
// lagged behind the extraction it performs and this is the one place where a
// malicious archive could reach outside the job sandbox.
func (e *Engine) Extract(ctx context.Context, opts ExtractOptions, onProgress ProgressFunc) (*ArchiveInfo, error) {
	info, err := e.List(ctx, opts.Archive, opts.Password, nil)
	if err != nil {
		return nil, err
	}
	if info.NeedsPassword && opts.Password == "" {
		return info, ErrPasswordRequired
	}

	for _, entry := range info.Entries {
		if UnsafePath(entry.Path) {
			return info, fmt.Errorf("%w：%s", ErrUnsafeEntry, entry.Path)
		}
	}

	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return nil, err
	}

	// 7-Zip spells "extract without paths" as the command `e`; there is no
	// switch form, and passing `-e` is a command line error.
	command := "x"
	if !opts.PreservePaths {
		command = "e"
	}

	args := []string{command}
	if opts.Password != "" {
		args = append(args, "-p"+opts.Password)
	}
	args = append(args, "-o"+opts.OutputDir)
	if !opts.Overwrite {
		args = append(args, "-aos")
	}
	// 7-Zip syntax is "x [switches] archive [names...]". The listfile must come
	// after the archive, and no "--" may appear: it would turn every following
	// switch into a literal file name.
	args = append(args, "-scsUTF-8", opts.Archive)
	if len(opts.Selected) > 0 {
		listFile, cleanup, err := writeListFile(opts.OutputDir, opts.Selected)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		args = append(args, "@"+listFile)
	}

	if err := e.run(ctx, "", onProgress, args...); err != nil {
		return info, err
	}
	return info, nil
}

// CompressOptions controls a single compression.
type CompressOptions struct {
	// Format is a registry ID such as "7z" or "zip".
	Format string
	// Level is 0-9, or -1 for the format default.
	Level int
	// Password encrypts the payload. Empty disables encryption.
	Password string
	// EncryptNames also hides member names (7z only, -mhe=on).
	EncryptNames bool
	// VolumeSize splits the output, e.g. "100m" or "2g". Empty means no split.
	VolumeSize string
	// WorkDir is the directory holding the payload; 7-Zip runs with this as
	// its working directory so stored paths stay relative and clean.
	WorkDir string
	// Sources are member paths relative to WorkDir.
	Sources []string
	// OutputName is the base file name without extension.
	OutputName string
	// OutputDir receives the produced archive and must be outside WorkDir.
	OutputDir string
	// Threads is the 7-Zip worker count; 0 means "let 7-Zip decide".
	Threads int
	// Solid enables solid blocks where the format supports it.
	Solid bool
	// Overwrite replaces an existing archive of the same name. Nothing is
	// deleted unless this is set, so a name collision is a clear error rather
	// than silent data loss.
	Overwrite bool
}

// singleFileCompressors cannot hold more than one member; anything richer is
// first wrapped in a tar stream.
var singleFileCompressors = map[string]string{
	"gzip": "tar.gz", "bzip2": "tar.bz2", "xz": "tar.xz",
	"zstd": "tar.zst", "lzma": "tar.lzma",
}

// Compress builds an archive and returns the produced file paths (more than
// one when volume splitting is on), sorted in volume order.
func (e *Engine) Compress(ctx context.Context, opts CompressOptions, onProgress ProgressFunc) ([]string, error) {
	format, ok := FormatByID(opts.Format)
	if !ok {
		return nil, fmt.Errorf("不支持的压缩格式：%s", opts.Format)
	}
	if format.Capability != CreateAndExtract {
		return nil, fmt.Errorf("%s 不支持创建（仅可解压）", format.Label)
	}
	if len(opts.Sources) == 0 {
		return nil, errors.New("没有选择要压缩的文件")
	}

	needTar := false
	if tarSuffix, isSingle := singleFileCompressors[format.ID]; isSingle {
		// A directory source, or more than one source, has to be tarred first.
		if len(opts.Sources) > 1 {
			needTar = true
		} else if st, err := os.Stat(filepath.Join(opts.WorkDir, opts.Sources[0])); err == nil && st.IsDir() {
			needTar = true
		}
		_ = tarSuffix
	}

	if needTar {
		return e.compressViaTar(ctx, opts, format, onProgress)
	}
	return e.compressDirect(ctx, opts, format, onProgress)
}

// clearExistingTargets removes an archive of the same name, or refuses when the
// caller did not ask to overwrite.
//
// This is not merely polite: 7-Zip answers a pre-existing split archive with
// "Updating for multivolume archives is not implemented" and produces nothing,
// so a second run with the same output name would fail forever. For a single
// file archive 7-Zip would instead *update* the old archive, which is equally
// not what "create this archive" means.
func (e *Engine) clearExistingTargets(opts CompressOptions, format Format) error {
	candidates, err := existingOutputs(opts.OutputDir, opts.OutputName, format.Extension)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return nil
	}
	if !opts.Overwrite {
		return fmt.Errorf("目标已存在：%s。勾选“覆盖同名文件”后可替换",
			strings.Join(shortBaseNames(candidates, 4), "、"))
	}
	for _, path := range candidates {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("无法替换已存在的输出文件 %s：%w", filepath.Base(path), err)
		}
	}
	return nil
}

var volumeSuffixRe = regexp.MustCompile(`\.\d{3,4}$`)

// existingOutputs lists files in dir that a run with this base name would
// write: "<name><ext>", its split parts, and the tar-based "<name>.tar<ext>"
// equivalents. Anything unrelated is deliberately left alone.
func existingOutputs(dir, name, ext string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if isTargetName(entry.Name(), name, ext) {
			out = append(out, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// isTargetName reports whether file is an archive this run would produce for
// the given base name.
func isTargetName(file, name, ext string) bool {
	for _, prefix := range []string{name + ext, name + ".tar" + ext} {
		if file == prefix {
			return true
		}
		if strings.HasPrefix(file, prefix) && volumeSuffixRe.MatchString(file) {
			return true
		}
	}
	return false
}

func shortBaseNames(paths []string, max int) []string {
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

func (e *Engine) compressDirect(ctx context.Context, opts CompressOptions, format Format, onProgress ProgressFunc) ([]string, error) {
	if err := e.clearExistingTargets(opts, format); err != nil {
		return nil, err
	}
	outPath := filepath.Join(opts.OutputDir, opts.OutputName+format.Extension)

	listFile, cleanup, err := writeListFile(opts.OutputDir, opts.Sources)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	args := []string{"a", "-t" + format.ID}
	args = append(args, compressionArgs(opts, format)...)
	args = append(args, "-scsUTF-8", outPath, "@"+listFile)

	if err := e.run(ctx, opts.WorkDir, onProgress, args...); err != nil {
		return nil, err
	}
	return e.collectOutputs(opts.OutputDir, opts.OutputName)
}

// compressViaTar implements the two step pipeline for the single member
// compressors: build a tar stream, then squeeze it.
func (e *Engine) compressViaTar(ctx context.Context, opts CompressOptions, format Format, onProgress ProgressFunc) ([]string, error) {
	if err := e.clearExistingTargets(opts, format); err != nil {
		return nil, err
	}
	tarPath := filepath.Join(opts.OutputDir, opts.OutputName+".tar")

	listFile, cleanup, err := writeListFile(opts.OutputDir, opts.Sources)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	tarArgs := []string{"a", "-ttar", "-scsUTF-8", tarPath, "@" + listFile}

	if err := e.run(ctx, opts.WorkDir, func(p Progress) {
		if onProgress != nil {
			// Tar is the first half of the work.
			onProgress(Progress{Percent: p.Percent / 2, Stage: "packing"})
		}
	}, tarArgs...); err != nil {
		_ = os.Remove(tarPath)
		return nil, err
	}
	defer os.Remove(tarPath)

	outPath := filepath.Join(opts.OutputDir, opts.OutputName+".tar"+format.Extension)
	args := []string{"a", "-t" + format.ID}
	args = append(args, compressionArgs(opts, format)...)
	args = append(args, outPath, tarPath)

	if err := e.run(ctx, opts.WorkDir, func(p Progress) {
		if onProgress != nil {
			onProgress(Progress{Percent: 50 + p.Percent/2, Stage: "compressing"})
		}
	}, args...); err != nil {
		return nil, err
	}
	return e.collectOutputs(opts.OutputDir, opts.OutputName+".tar")
}

// compressionArgs translates the options into 7-Zip switches.
func compressionArgs(opts CompressOptions, format Format) []string {
	var args []string
	if format.SupportsLevel && opts.Level >= 0 {
		args = append(args, "-mx="+strconv.Itoa(opts.Level))
	}
	if opts.Threads > 0 {
		args = append(args, "-mmt="+strconv.Itoa(opts.Threads))
	}
	if opts.Password != "" && format.SupportsPassword {
		args = append(args, "-p"+opts.Password)
		switch format.ID {
		case "7z":
			// 7-Zip always uses AES-256 for 7z encryption and rejects an
			// explicit -m0=AES256, so only the header switch is set here.
			// -mhe=on extends encryption to the header, hiding member names.
			if opts.EncryptNames {
				args = append(args, "-mhe=on")
			} else {
				args = append(args, "-mhe=off")
			}
		case "zip":
			// ZIP defaults to the broken ZipCrypto scheme; AES-256 is the
			// only defensible choice for a new archive.
			args = append(args, "-mem=AES256")
		}
	}
	if format.SupportsVolume && opts.VolumeSize != "" {
		args = append(args, "-v"+opts.VolumeSize)
	}
	if format.ID == "7z" && opts.Solid {
		args = append(args, "-ms=on")
	}
	return args
}

// collectOutputs lists the archive files produced, covering split volumes such
// as name.7z.001 by matching the base name prefix.
func (e *Engine) collectOutputs(dir, baseName string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == baseName || strings.HasPrefix(name, baseName+".") {
			out = append(out, filepath.Join(dir, name))
		}
	}
	if len(out) == 0 {
		return nil, errors.New("压缩未产生任何输出文件")
	}
	sort.Strings(out)
	return out, nil
}

// writeListFile writes a 7-Zip @listfile holding one relative member path per
// line. The file lives outside WorkDir so it cannot be picked up as payload.
func writeListFile(dir string, names []string) (string, func(), error) {
	f, err := os.CreateTemp(dir, ".yc7zip-list-*.txt")
	if err != nil {
		return "", func() {}, err
	}
	w := bufio.NewWriter(f)
	for _, name := range names {
		// A newline inside a name would silently fork the entry; such names
		// cannot occur on the platforms we support, so reject them loudly.
		if strings.ContainsAny(name, "\r\n") {
			f.Close()
			os.Remove(f.Name())
			return "", func() {}, fmt.Errorf("文件名包含换行符，无法处理：%q", name)
		}
		fmt.Fprintln(w, name)
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", func() {}, err
	}
	return f.Name(), func() { _ = os.Remove(f.Name()) }, nil
}

// UnsafePath reports whether a member name would escape an extraction root.
func UnsafePath(name string) bool {
	if name == "" {
		return true
	}
	if strings.ContainsRune(name, 0) {
		return true
	}
	normalised := strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(normalised, "/") {
		return true
	}
	// Windows drive-absolute names such as "C:/x".
	if len(normalised) >= 2 && normalised[1] == ':' {
		return true
	}
	cleaned := path.Clean(normalised)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return true
	}
	for _, segment := range strings.Split(normalised, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

// Test reports whether the engine can actually invoke 7-Zip.
func (e *Engine) Test(ctx context.Context) error {
	if e.BinPath == "" {
		return errors.New("7-Zip 路径为空")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return execCommand(ctx, e.BinPath).Run()
}

// CheckPassword decrypts one member to prove a password is correct, without
// writing anything to disk.
//
// Formats without header encryption (notably ZIP) still list their member
// names when locked, so a listing cannot tell a right password from a wrong
// one. Verifying a single member up front turns "job accepted, then failed"
// into an immediate, specific error.
func (e *Engine) CheckPassword(ctx context.Context, archive, password, member string) error {
	args := []string{"t"}
	if password != "" {
		args = append(args, "-p"+password)
	}
	args = append(args, "-scsUTF-8", archive)
	if member != "" {
		args = append(args, member)
	}
	return e.run(ctx, "", nil, args...)
}

// EncryptedMembers returns the encrypted non-directory members, which is what
// CheckPassword needs for a targeted test.
func EncryptedMembers(info *ArchiveInfo) []string {
	if info == nil {
		return nil
	}
	var out []string
	for _, entry := range info.Entries {
		if entry.Encrypted && !entry.IsDir {
			out = append(out, entry.Path)
		}
	}
	return out
}
