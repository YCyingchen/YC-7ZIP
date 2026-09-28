package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrPasswordRequired is returned when an archive is encrypted and no (or a
// wrong) password was supplied.
var ErrPasswordRequired = errors.New("存档已加密，需要正确的密码")

// ErrUnsafeEntry is returned when an archive tries to write outside the
// extraction root.
var ErrUnsafeEntry = errors.New("存档中包含不安全的路径，已拒绝解压")

// Progress is a single progress sample. Percent is -1 while the total size is
// still unknown (7-Zip reports this during the scanning phase).
type Progress struct {
	Percent float64
	Stage   string
}

// ProgressFunc receives progress samples. It must not block for long: 7-Zip
// stops emitting while the callback runs.
type ProgressFunc func(Progress)

// Engine owns the 7-Zip process surface.
type Engine struct {
	// BinPath is the resolved 7-Zip executable.
	BinPath string
	// Version is the reported version string, e.g. "24.07".
	Version string
	// Unrar is an optional unrar executable used for RAR archives whose
	// features 7-Zip rejects (rare, but seen with some RAR5 revisions).
	Unrar string

	// mu serialises probeOnly calls that lazily populate the cache.
	mu sync.Mutex
	// encryptedProbe caches whether an archive needs a password, keyed by
	// path+mtime so a repeated listing does not re-run the probe.
	encryptedProbe map[string]bool
}

// New locates a usable 7-Zip binary and returns a ready engine.
func New() (*Engine, error) {
	path, err := find7Zip()
	if err != nil {
		return nil, err
	}
	e := &Engine{BinPath: path, encryptedProbe: map[string]bool{}}
	if v, err := e.probeVersion(context.Background()); err == nil {
		e.Version = v
	}
	e.Unrar = findBinary("unrar")
	return e, nil
}

// find7Zip looks for an official 7-Zip executable in the places a user is
// actually likely to have one, in decreasing order of preference.
func find7Zip() (string, error) {
	if env := os.Getenv("YC7ZIP_7Z"); env != "" {
		if st, err := os.Stat(env); err == nil && !st.IsDir() {
			return env, nil
		}
	}

	var names []string
	if runtime.GOOS == "windows" {
		names = []string{"7zz.exe", "7za.exe", "7z.exe"}
	} else {
		names = []string{"7zz", "7za", "7z"}
	}

	// 1. Next to our own executable: this is how the released archives and
	//    the container image ship the binary.
	if self, err := os.Executable(); err == nil {
		dir := filepath.Dir(self)
		for _, candidate := range []string{dir, filepath.Join(dir, "7z"), filepath.Join(dir, "bin")} {
			for _, name := range names {
				full := filepath.Join(candidate, name)
				if st, err := os.Stat(full); err == nil && !st.IsDir() {
					return full, nil
				}
			}
		}
	}

	// 2. Anywhere on PATH.
	for _, name := range names {
		if p := findBinary(name); p != "" {
			return p, nil
		}
	}

	// 3. Well known install locations.
	var guesses []string
	switch runtime.GOOS {
	case "windows":
		guesses = []string{
			`C:\Program Files\7-Zip\7z.exe`,
			`C:\Program Files (x86)\7-Zip\7z.exe`,
		}
	case "darwin":
		guesses = []string{
			"/opt/homebrew/bin/7zz", "/usr/local/bin/7zz",
			"/Applications/Keka.app/Contents/Resources/keka7z",
		}
	default:
		guesses = []string{"/usr/lib/p7zip/7z", "/usr/local/bin/7zz", "/snap/bin/7z", "/usr/bin/7zz"}
	}
	for _, g := range guesses {
		if st, err := os.Stat(g); err == nil && !st.IsDir() {
			return g, nil
		}
	}

	return "", errors.New("未找到 7-Zip 可执行文件：请安装 7-Zip（或 p7zip / 7zz），" +
		"或设置环境变量 YC7ZIP_7Z 指向它")
}

// findBinary is exec.LookPath with the caller's own directory fallback.
func findBinary(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(name, ".exe") {
		if p, err := exec.LookPath(name + ".exe"); err == nil {
			return p
		}
	}
	return ""
}

var versionRe = regexp.MustCompile(`(?m)^7-Zip(?: \(z\))?.*?([0-9]+\.[0-9]+)`)

func (e *Engine) probeVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, e.BinPath).Output()
	if err != nil {
		// 7-Zip exits non-zero when invoked with no arguments; the banner on
		// stdout is still valid.
		var ee *exec.ExitError
		if !errors.As(err, &ee) || len(out) == 0 {
			return "", err
		}
	}
	if m := versionRe.FindSubmatch(out); len(m) == 2 {
		return string(m[1]), nil
	}
	return "unknown", nil
}

// progressRe matches a percentage sample. It is deliberately unanchored:
// depending on the platform and whether stdout is a terminal, 7-Zip separates
// its in-place redraws with "\r", with backspaces, or with nothing at all, so
// the samples cannot be relied on to start a line.
var progressRe = regexp.MustCompile(`([0-9]{1,3})%`)

// run executes 7-Zip, forwarding progress samples and returning the combined
// diagnostic output when the process fails.
func (e *Engine) run(ctx context.Context, dir string, onProgress ProgressFunc, args ...string) error {
	// -bsp1 routes percentage reporting to stdout so it is separable from the
	//   human readable stream.
	// -bso0 silences the normal listing output, which we never display.
	// -bse2 keeps diagnostics on stderr where they can be collected
	//   separately from the progress percentages.
	// -sccUTF-8 forces UTF-8 console output so non-ASCII member names survive
	//   on Windows hosts whose ANSI code page is not UTF-8.
	full := append([]string{"-bsp1", "-bso0", "-bse2", "-sccUTF-8", "-y"}, args...)
	cmd := execCommand(ctx, e.BinPath, full...)
	cmd.Dir = dir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// Read the progress stream in fixed chunks and pick the last percentage out
	// of each one. This is bounded no matter how long the job runs or how the
	// samples are separated, which a line scanner cannot promise.
	go func() {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		tail := make([]byte, 0, 128)
		for {
			n, readErr := stdout.Read(buf)
			if n > 0 {
				chunk := make([]byte, 0, len(tail)+n)
				chunk = append(chunk, tail...)
				chunk = append(chunk, buf[:n]...)

				if onProgress != nil {
					if matches := progressRe.FindAllSubmatch(chunk, -1); len(matches) > 0 {
						last := matches[len(matches)-1][1]
						if pct, convErr := strconv.ParseFloat(string(last), 64); convErr == nil && pct <= 100 {
							onProgress(Progress{Percent: pct, Stage: "processing"})
						}
					}
				}

				// Keep a short tail so a sample split across two reads is
				// still seen, without growing without bound.
				const tailLen = 64
				if len(chunk) > tailLen {
					tail = append(tail[:0], chunk[len(chunk)-tailLen:]...)
				} else {
					tail = append(tail[:0], chunk...)
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	// Diagnostics only matter when the run fails, so cap what we retain.
	var diag strings.Builder
	go func() {
		defer wg.Done()
		raw, _ := io.ReadAll(io.LimitReader(stderr, 256*1024))
		for _, line := range strings.FieldsFunc(string(raw), func(r rune) bool {
			return r == '\r' || r == '\n'
		}) {
			line = strings.TrimSpace(line)
			// Drop blank filler and the periodic percentage lines; what is
			// left is the actual message.
			if line == "" || progressRe.MatchString(line) && !strings.Contains(line, "ERROR") {
				continue
			}
			diag.WriteString(line)
			diag.WriteByte('\n')
		}
	}()

	wg.Wait()
	err = cmd.Wait()
	if err != nil {
		return wrapExitError(err, diag.String())
	}
	return nil
}

// diagLabelRe matches the prefixes 7-Zip puts in front of a real message, such
// as "ERROR:" or "System ERROR:". Stripping them leaves something a user can
// act on; a line that is nothing but a label becomes empty and is dropped.
var diagLabelRe = regexp.MustCompile(`(?i)^\s*(system\s+error|command\s+line\s+error|error|warning)\s*:\s*`)

// summarizeDiag picks the most informative line out of 7-Zip's stderr.
//
// Taking the literal first line is not good enough: 7-Zip frequently leads with
// a bare "Error:" and puts the real message on the next line, which would leave
// the user staring at a message that says nothing.
func summarizeDiag(diag string) string {
	var lines []string
	for _, raw := range strings.Split(diag, "\n") {
		line := strings.TrimSpace(diagLabelRe.ReplaceAllString(strings.TrimSpace(raw), ""))
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	if len(lines) == 1 {
		return lines[0]
	}
	return lines[0] + "（" + lines[len(lines)-1] + "）"
}

// classify maps 7-Zip's exit codes and messages onto the engine's sentinel
// errors so the HTTP layer can pick a proper status code.
func wrapExitError(err error, diag string) error {
	lower := strings.ToLower(diag)
	switch {
	case strings.Contains(lower, "wrong password"),
		strings.Contains(lower, "password is incorrect"),
		strings.Contains(lower, "can not open encrypted archive"),
		strings.Contains(lower, "cannot open encrypted archive"):
		return fmt.Errorf("%w (%s)", ErrPasswordRequired, summarizeDiag(diag))
	case strings.Contains(lower, "updating for multivolume archives is not implemented"):
		return errors.New("目标分卷归档已存在，7-Zip 无法更新分卷，请勾选“覆盖同名文件”或换一个输出文件名")
	case strings.Contains(lower, "is not supported archive"),
		strings.Contains(lower, "cannot open the file as"),
		strings.Contains(lower, "unsupported method"):
		return fmt.Errorf("无法识别的存档格式或使用了不支持的算法：%s", summarizeDiag(diag))
	}
	msg := summarizeDiag(diag)
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("7-Zip 执行失败：%s", msg)
}

// copyFileSize returns the size of path, or 0 when unavailable.
func copyFileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

// drain discards a reader, used where 7-Zip output is known to be unneeded.
func drain(r io.Reader) { _, _ = io.Copy(io.Discard, r) }
