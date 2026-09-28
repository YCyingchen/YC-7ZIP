package engine

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestEngine skips the test when no 7-Zip is installed, so the suite stays
// runnable on a bare machine while still exercising the real engine wherever
// one exists.
func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	eng, err := New()
	if err != nil {
		t.Skipf("7-Zip 不可用，跳过：%v", err)
	}
	return eng
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCompressExtractRoundTrip covers the path a real user takes: pack a tree,
// read its index back, unpack it, and confirm the bytes survived.
func TestCompressExtractRoundTrip(t *testing.T) {
	eng := newTestEngine(t)
	ctx := context.Background()

	base := t.TempDir()
	work := filepath.Join(base, "in")
	out := filepath.Join(base, "out")
	writeTree(t, work, map[string]string{
		"readme.txt":              "hello yc-7zip",
		"nested/深空/资料.txt":        "中文文件名测试",
		"nested/data.bin":         strings.Repeat("A", 4096),
		"nested/emoji-🚀-file.txt": "rocket",
	})
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, format := range []string{"7z", "zip", "tar"} {
		t.Run(format, func(t *testing.T) {
			sources, err := os.ReadDir(work)
			if err != nil {
				t.Fatal(err)
			}
			names := make([]string, 0, len(sources))
			for _, s := range sources {
				names = append(names, s.Name())
			}

			formatOut := filepath.Join(out, format)
			if err := os.MkdirAll(formatOut, 0o755); err != nil {
				t.Fatal(err)
			}

			produced, err := eng.Compress(ctx, CompressOptions{
				Format:     format,
				Level:      5,
				WorkDir:    work,
				Sources:    names,
				OutputName: "bundle",
				OutputDir:  formatOut,
			}, nil)
			if err != nil {
				t.Fatalf("Compress: %v", err)
			}
			if len(produced) != 1 {
				t.Fatalf("expected 1 output, got %d (%v)", len(produced), produced)
			}

			info, err := eng.List(ctx, produced[0], "", nil)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(info.Entries) == 0 {
				t.Fatal("List returned no entries")
			}
			if info.TotalSize == 0 {
				t.Fatal("List reported a zero total size")
			}

			dest := filepath.Join(base, "unpacked-"+format)
			if _, err := eng.Extract(ctx, ExtractOptions{
				Archive:       produced[0],
				OutputDir:     dest,
				PreservePaths: true,
				Overwrite:     true,
			}, nil); err != nil {
				t.Fatalf("Extract: %v", err)
			}

			got, err := os.ReadFile(filepath.Join(dest, "readme.txt"))
			if err != nil {
				t.Fatalf("readme.txt missing after extract: %v", err)
			}
			if string(got) != "hello yc-7zip" {
				t.Fatalf("content mismatch: %q", got)
			}

			// Non-ASCII member names are the classic place an archive tool
			// silently mangles data, so assert on them explicitly.
			if _, err := os.Stat(filepath.Join(dest, "nested", "深空", "资料.txt")); err != nil {
				t.Errorf("unicode path missing: %v", err)
			}
		})
	}
}

// TestEncryptedArchiveRequiresPassword verifies both halves of the password
// contract: listing without one reports the requirement, and the right
// password produces a readable result.
func TestEncryptedArchiveRequiresPassword(t *testing.T) {
	eng := newTestEngine(t)
	ctx := context.Background()

	base := t.TempDir()
	work := filepath.Join(base, "in")
	out := filepath.Join(base, "out")
	writeTree(t, work, map[string]string{"secret.txt": "top secret"})
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}

	produced, err := eng.Compress(ctx, CompressOptions{
		Format:       "7z",
		Level:        5,
		Password:     "s3cr3t",
		EncryptNames: true,
		WorkDir:      work,
		Sources:      []string{"secret.txt"},
		OutputName:   "locked",
		OutputDir:    out,
	}, nil)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}

	info, err := eng.List(ctx, produced[0], "", nil)
	if err != nil {
		t.Fatalf("List without password should not error: %v", err)
	}
	if !info.NeedsPassword {
		t.Fatal("NeedsPassword should be true for an encrypted archive")
	}

	if _, err := eng.Extract(ctx, ExtractOptions{
		Archive:   produced[0],
		OutputDir: filepath.Join(base, "nope"),
	}, nil); !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("expected ErrPasswordRequired, got %v", err)
	}

	dest := filepath.Join(base, "unpacked")
	if _, err := eng.Extract(ctx, ExtractOptions{
		Archive:       produced[0],
		OutputDir:     dest,
		Password:      "s3cr3t",
		PreservePaths: true,
		Overwrite:     true,
	}, nil); err != nil {
		t.Fatalf("Extract with correct password: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "secret.txt"))
	if err != nil || string(got) != "top secret" {
		t.Fatalf("decrypted content mismatch: %q err=%v", got, err)
	}
}

// TestWrongPasswordIsReportedAsSuch guards the error classification the HTTP
// layer relies on to show a "wrong password" prompt instead of a 500.
func TestWrongPasswordIsReportedAsSuch(t *testing.T) {
	eng := newTestEngine(t)
	ctx := context.Background()

	base := t.TempDir()
	work := filepath.Join(base, "in")
	out := filepath.Join(base, "out")
	writeTree(t, work, map[string]string{"a.txt": "a"})
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}

	produced, err := eng.Compress(ctx, CompressOptions{
		Format: "zip", Level: 5, Password: "right-one",
		WorkDir: work, Sources: []string{"a.txt"},
		OutputName: "locked", OutputDir: out,
	}, nil)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}

	_, err = eng.Extract(ctx, ExtractOptions{
		Archive:   produced[0],
		OutputDir: filepath.Join(base, "out2"),
		Password:  "wrong-one",
	}, nil)
	if err == nil {
		t.Fatal("expected an error for a wrong password")
	}
	if !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("wrong password should map to ErrPasswordRequired, got %v", err)
	}
}

// TestVolumeSplit produces a split archive and confirms every part lands on
// disk, which is what the download endpoint enumerates.
func TestVolumeSplit(t *testing.T) {
	eng := newTestEngine(t)
	ctx := context.Background()

	base := t.TempDir()
	work := filepath.Join(base, "in")
	out := filepath.Join(base, "out")
	// Incompressible content: a repeating pattern would squeeze below one
	// volume and the split would never be exercised.
	blob := make([]byte, 512<<10)
	if _, err := rand.Read(blob); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "big.bin"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}

	produced, err := eng.Compress(ctx, CompressOptions{
		Format: "7z", Level: 1, VolumeSize: "64k",
		WorkDir: work, Sources: []string{"big.bin"},
		OutputName: "split", OutputDir: out,
	}, nil)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if len(produced) < 2 {
		t.Fatalf("expected multiple volumes, got %v", produced)
	}
	for _, p := range produced {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("volume missing: %v", err)
		}
	}
}

// TestOnlyExtractFormatsRefuseCreation ensures the registry gate is enforced
// in the engine and not merely in the UI.
func TestOnlyExtractFormatsRefuseCreation(t *testing.T) {
	eng := newTestEngine(t)
	base := t.TempDir()
	work := filepath.Join(base, "in")
	writeTree(t, work, map[string]string{"a.txt": "a"})

	if _, err := eng.Compress(context.Background(), CompressOptions{
		Format: "rar", WorkDir: work, Sources: []string{"a.txt"},
		OutputName: "x", OutputDir: base,
	}, nil); err == nil {
		t.Fatal("creating a RAR must fail")
	}
}

// TestUnsafePath is the security regression test for archive path traversal.
func TestUnsafePath(t *testing.T) {
	unsafe := []string{
		"../escape.txt",
		"../../etc/passwd",
		"a/../../b",
		"/absolute/path",
		"\\windows\\system32\\evil.dll",
		"..\\windows\\evil",
		"C:/Windows/system32/evil.exe",
		"",
		"a\x00b",
	}
	for _, name := range unsafe {
		if !UnsafePath(name) {
			t.Errorf("UnsafePath(%q) = false, want true", name)
		}
	}
	safe := []string{
		"file.txt",
		"dir/file.txt",
		"深空/资料.txt",
		"a.b.c/d.txt",
		"..hidden",
		"...",
	}
	for _, name := range safe {
		if UnsafePath(name) {
			t.Errorf("UnsafePath(%q) = true, want false", name)
		}
	}
}

// TestExtensionToFormat covers the suffix mapping the upload path uses.
func TestExtensionToFormat(t *testing.T) {
	cases := map[string]string{
		"a.7z": "7z", "a.ZIP": "zip", "a.rar": "rar", "a.tar.gz": "gzip",
		"a.tgz": "gzip", "a.tar.xz": "xz", "a.txz": "xz", "a.zst": "zstd",
		"a.iso": "iso",
	}
	for name, want := range cases {
		got, ok := ExtensionToFormat(name)
		if !ok {
			t.Errorf("ExtensionToFormat(%q) not found", name)
			continue
		}
		if got.ID != want {
			t.Errorf("ExtensionToFormat(%q) = %q, want %q", name, got.ID, want)
		}
	}
}

// TestProgressIsReported confirms the -bsp1 parser actually surfaces samples.
//
// This is not cosmetic: 7-Zip separates progress samples with a bare carriage
// return, so a plain line scanner silently sees one huge token and the UI's
// progress bar never moves. The payload is incompressible and large enough that
// 7-Zip must emit more than one sample.
func TestProgressIsReported(t *testing.T) {
	eng := newTestEngine(t)
	base := t.TempDir()
	work := filepath.Join(base, "in")
	out := filepath.Join(base, "out")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.bin", "b.bin"} {
		blob := make([]byte, 24<<20)
		if _, err := rand.Read(blob); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(work, name), blob, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var (
		mu     sync.Mutex
		seen   int
		maxPct float64
		minPct = 101.0
	)

	_, err := eng.Compress(context.Background(), CompressOptions{
		Format: "7z", Level: 9,
		WorkDir: work, Sources: []string{"a.bin", "b.bin"},
		OutputName: "progress", OutputDir: out,
	}, func(p Progress) {
		mu.Lock()
		defer mu.Unlock()
		seen++
		if p.Percent > maxPct {
			maxPct = p.Percent
		}
		if p.Percent < minPct {
			minPct = p.Percent
		}
	})
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if seen == 0 {
		t.Fatal("no progress samples were parsed: the \\r separated stream is not being split")
	}
	if maxPct < 1 {
		t.Fatalf("progress never advanced past %.1f%% (saw %d samples)", maxPct, seen)
	}
}

// TestOverwriteExistingArchive is the regression test for a failure that made
// every repeat run impossible: 7-Zip answers "Updating for multivolume
// archives is not implemented" when the first part of a split archive already
// exists, and silently *updates* a single-file archive instead of replacing it.
func TestOverwriteExistingArchive(t *testing.T) {
	eng := newTestEngine(t)
	ctx := context.Background()

	base := t.TempDir()
	work := filepath.Join(base, "in")
	out := filepath.Join(base, "out")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	blob := make([]byte, 3<<20)
	if _, err := rand.Read(blob); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "payload.bin"), blob, 0o644); err != nil {
		t.Fatal(err)
	}

	runs := []struct {
		name      string
		volume    string
		format    string
		extension string
	}{
		{name: "split", volume: "512k", format: "7z", extension: ".7z"},
		{name: "single", volume: "", format: "zip", extension: ".zip"},
	}

	for _, rc := range runs {
		t.Run(rc.name, func(t *testing.T) {
			opts := CompressOptions{
				Format: rc.format, Level: 1, VolumeSize: rc.volume,
				WorkDir: work, Sources: []string{"payload.bin"},
				OutputName: rc.name, OutputDir: out,
			}

			first, err := eng.Compress(ctx, opts, nil)
			if err != nil {
				t.Fatalf("first run: %v", err)
			}
			if len(first) == 0 {
				t.Fatal("first run produced nothing")
			}

			// Without permission to overwrite, the collision must be reported.
			_, err = eng.Compress(ctx, opts, nil)
			if err == nil {
				t.Fatal("second run should have refused to overwrite")
			}
			if !strings.Contains(err.Error(), "覆盖同名文件") {
				t.Fatalf("error should explain the collision, got %q", err)
			}

			// With it, the result must be a fresh archive of the same shape.
			opts.Overwrite = true
			second, err := eng.Compress(ctx, opts, nil)
			if err != nil {
				t.Fatalf("overwriting run: %v", err)
			}
			if len(second) != len(first) {
				t.Fatalf("after overwrite produced %d parts, want %d", len(second), len(first))
			}
			for _, p := range second {
				st, err := os.Stat(p)
				if err != nil {
					t.Errorf("missing output %s: %v", filepath.Base(p), err)
					continue
				}
				if st.Size() == 0 {
					t.Errorf("output %s is empty", filepath.Base(p))
				}
			}
		})
	}
}

// TestClearExistingTargetsLeavesNeighboursAlone guards the blast radius: only
// archives this run would produce may be removed.
func TestClearExistingTargetsLeavesNeighboursAlone(t *testing.T) {
	dir := t.TempDir()
	keep := []string{"photos.txt", "photos.zip.bak", "photos-2024.7z", "other.7z.001", "photos.tar"}
	for _, name := range keep {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	targets := []string{"photos.7z", "photos.7z.001", "photos.7z.002"}
	for _, name := range targets {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := existingOutputs(dir, "photos", ".7z")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(targets) {
		t.Fatalf("existingOutputs = %v, want exactly %v", names(got), targets)
	}
	for _, p := range got {
		if !strings.HasPrefix(baseName(p), "photos.7z") {
			t.Errorf("unexpected match %s", baseName(p))
		}
	}
	for _, name := range keep {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("neighbour %s should not have been considered an output", name)
		}
	}
}

// TestSummarizeDiag checks that a bare "Error:" label does not become the whole
// user-visible message.
func TestSummarizeDiag(t *testing.T) {
	cases := []struct {
		diag string
		want string
	}{
		{"\nError:\nUpdating for multivolume archives is not implemented\n", "Updating for multivolume archives is not implemented"},
		{"ERROR: Wrong password\n", "Wrong password"},
		{"", ""},
		{"\n\n", ""},
	}
	for _, tc := range cases {
		if got := summarizeDiag(tc.diag); got != tc.want {
			t.Errorf("summarizeDiag(%q) = %q, want %q", tc.diag, got, tc.want)
		}
	}
}

// TestListUnsupportedArchive checks the error path for garbage input.
func TestListUnsupportedArchive(t *testing.T) {
	eng := newTestEngine(t)
	base := t.TempDir()
	junk := filepath.Join(base, "not-an-archive.7z")
	if err := os.WriteFile(junk, []byte("this is definitely not an archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := eng.List(ctx, junk, "", nil); err == nil {
		t.Fatal("listing garbage should fail")
	}
}
