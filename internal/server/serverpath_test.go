package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ycyingchen/yc-7zip/internal/engine"
)

// makeSplitArchive writes a real split archive to disk and returns the
// directory holding it plus the part paths, in order.
func makeSplitArchive(t *testing.T, dir, name string, size int, volume string) []string {
	t.Helper()
	eng, err := engine.New()
	if err != nil {
		t.Skipf("7-Zip 不可用，跳过：%v", err)
	}
	work := filepath.Join(dir, "payload")
	out := filepath.Join(dir, "parts")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	// Incompressible so the split really happens. A patterned buffer would
	// squeeze below one volume and never split.
	blob := make([]byte, size)
	if _, err := rand.Read(blob); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "payload.bin"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	produced, err := eng.Compress(context.Background(), engine.CompressOptions{
		Format: "7z", Level: 1, VolumeSize: volume,
		WorkDir: work, Sources: []string{"payload.bin"},
		OutputName: name, OutputDir: out,
	}, nil)
	if err != nil {
		t.Fatalf("building the split fixture: %v", err)
	}
	if len(produced) < 2 {
		t.Fatalf("fixture did not split: %v", produced)
	}
	return produced
}

// TestServerSideCompressAndExtract drives the flow the NAS user actually
// wants: act on files that are already on the host, and write the result back
// to the host instead of streaming it to a browser.
func TestServerSideCompressAndExtract(t *testing.T) {
	shared := t.TempDir()
	// Source material on the host.
	srcDir := filepath.Join(shared, "photos")
	if err := os.MkdirAll(filepath.Join(srcDir, "相册"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "说明.txt"), []byte("hello nas"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "相册", "照片.txt"), []byte("中文名"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := newHarness(t, Config{AllowRoots: []string{shared}})
	outDir := filepath.Join(shared, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// --- compress, writing on the host ------------------------------------
	res, created := h.json("POST", "/api/jobs?kind=compress", nil)
	res.Body.Close()
	id, _ := created["id"].(string)

	res, run := h.json("POST", "/api/jobs/"+id+"/run", map[string]any{
		"source_paths": []string{srcDir},
		"output_mode":  "server",
		"output_dir":   outDir,
		"format":       "7z",
		"level":        5,
		"name":         "photos",
	})
	res.Body.Close()
	if res.StatusCode != 202 {
		t.Fatalf("run status = %d (%v)", res.StatusCode, run)
	}

	final := h.waitDone(id, 90*time.Second)
	if final["status"] != "done" {
		t.Fatalf("compress failed: %v", final["message"])
	}
	produced := filepath.Join(outDir, "photos.7z")
	if _, err := os.Stat(produced); err != nil {
		t.Fatalf("archive was not written to the host: %v", err)
	}
	paths, _ := final["server_paths"].([]any)
	if len(paths) != 1 || !strings.HasSuffix(paths[0].(string), "photos.7z") {
		t.Fatalf("server_paths = %v", paths)
	}
	if final["output_mode"] != "server" {
		t.Fatalf("output_mode = %v", final["output_mode"])
	}
	if final["total_bytes"] == float64(0) {
		t.Fatal("total_bytes should reflect the written archive")
	}

	// --- extract it again, also onto the host -----------------------------
	res, created2 := h.json("POST", "/api/jobs?kind=extract", nil)
	res.Body.Close()
	id2, _ := created2["id"].(string)

	extractDir := filepath.Join(shared, "restored")
	res, run2 := h.json("POST", "/api/jobs/"+id2+"/run", map[string]any{
		"source_paths": []string{produced},
		"output_mode":  "server",
		"output_dir":   extractDir,
	})
	res.Body.Close()
	if res.StatusCode != 202 {
		t.Fatalf("extract run status = %d (%v)", res.StatusCode, run2)
	}
	final2 := h.waitDone(id2, 90*time.Second)
	if final2["status"] != "done" {
		t.Fatalf("extract failed: %v", final2["message"])
	}

	got, err := os.ReadFile(filepath.Join(extractDir, "photos", "说明.txt"))
	if err != nil {
		t.Fatalf("restored file missing: %v", err)
	}
	if string(got) != "hello nas" {
		t.Fatalf("restored content = %q", got)
	}
	if _, err := os.Stat(filepath.Join(extractDir, "photos", "相册", "照片.txt")); err != nil {
		t.Errorf("unicode path not restored: %v", err)
	}

	restored, _ := final2["server_paths"].([]any)
	if len(restored) != 1 {
		t.Fatalf("expected one top-level restored entry, got %v", restored)
	}
}

// TestServerSideSplitVolumeExtraction covers the headline requirement: the
// user clicks a part of a split archive in their file manager, and the tool
// finds the rest and unpacks it.
func TestServerSideSplitVolumeExtraction(t *testing.T) {
	shared := t.TempDir()
	parts := makeSplitArchive(t, shared, "movie", 4<<20, "512k")
	if len(parts) < 3 {
		t.Fatalf("fixture produced %d parts, need at least 3", len(parts))
	}

	h := newHarness(t, Config{AllowRoots: []string{shared}})

	// Inspect a middle part: the API must report the series, not a lone file.
	res, info := h.json("GET", "/api/inspect?path="+parts[1], nil)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("inspect status = %d (%v)", res.StatusCode, info)
	}
	vol, _ := info["volume"].(map[string]any)
	if vol == nil || vol["is_set"] != true {
		t.Fatalf("inspect did not detect a volume set: %v", info)
	}
	if int(vol["count"].(float64)) != len(parts) {
		t.Fatalf("volume count = %v, want %d", vol["count"], len(parts))
	}
	if filepath.Base(vol["first"].(string)) != filepath.Base(parts[0]) {
		t.Fatalf("first = %v, want %v", vol["first"], parts[0])
	}

	// Now extract, again pointing at the middle part.
	outDir := filepath.Join(shared, "unpacked")
	res, created := h.json("POST", "/api/jobs?kind=extract", nil)
	res.Body.Close()
	id, _ := created["id"].(string)

	res, run := h.json("POST", "/api/jobs/"+id+"/run", map[string]any{
		"source_paths": []string{parts[1]},
		"output_mode":  "server",
		"output_dir":   outDir,
	})
	res.Body.Close()
	if res.StatusCode != 202 {
		t.Fatalf("run status = %d (%v)", res.StatusCode, run)
	}
	final := h.waitDone(id, 120*time.Second)
	if final["status"] != "done" {
		t.Fatalf("split extraction failed: %v", final["message"])
	}
	if final["volume_label"] == "" || final["volume_label"] == nil {
		t.Fatal("the job should report which volume series it used")
	}

	st, err := os.Stat(filepath.Join(outDir, "payload.bin"))
	if err != nil {
		t.Fatalf("payload not extracted from the split archive: %v", err)
	}
	if st.Size() != 4<<20 {
		t.Fatalf("payload size = %d, want %d", st.Size(), 4<<20)
	}
}

// TestServerSideSplitVolumeMissingPart names the absent parts instead of
// letting 7-Zip report a corrupt archive.
func TestServerSideSplitVolumeMissingPart(t *testing.T) {
	shared := t.TempDir()
	parts := makeSplitArchive(t, shared, "broken", 4<<20, "512k")
	if len(parts) < 3 {
		t.Skipf("fixture produced %d parts, need 3 to punch a hole", len(parts))
	}
	if err := os.Remove(parts[1]); err != nil {
		t.Fatal(err)
	}

	h := newHarness(t, Config{AllowRoots: []string{shared}})
	res, created := h.json("POST", "/api/jobs?kind=extract", nil)
	res.Body.Close()
	id, _ := created["id"].(string)

	res, run := h.json("POST", "/api/jobs/"+id+"/run", map[string]any{
		"source_paths": []string{parts[0]},
		"output_mode":  "server",
		"output_dir":   filepath.Join(shared, "dest"),
	})
	res.Body.Close()
	if res.StatusCode != 400 {
		t.Fatalf("incomplete series should be refused with 400, got %d (%v)", res.StatusCode, run)
	}
	msg, _ := run["error"].(string)
	if !strings.Contains(msg, "分卷") {
		t.Fatalf("error should mention the missing volumes, got %q", msg)
	}
}

// TestServerSideGuards covers the boundaries that keep a request from reaching
// anything the operator did not expose.
func TestServerSideGuards(t *testing.T) {
	shared := t.TempDir()
	srcDir := filepath.Join(shared, "data")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()

	h := newHarness(t, Config{AllowRoots: []string{shared}})

	t.Run("source outside the allowed roots", func(t *testing.T) {
		res, created := h.json("POST", "/api/jobs?kind=compress", nil)
		res.Body.Close()
		id, _ := created["id"].(string)
		res, run := h.json("POST", "/api/jobs/"+id+"/run", map[string]any{
			"source_paths": []string{filepath.Join(outside, "secret.txt")},
			"output_mode":  "server",
			"output_dir":   filepath.Join(shared, "out"),
		})
		res.Body.Close()
		if res.StatusCode != 400 && res.StatusCode != 403 {
			t.Fatalf("out-of-root source should be refused, got %d (%v)", res.StatusCode, run)
		}
	})

	t.Run("output outside the allowed roots", func(t *testing.T) {
		res, created := h.json("POST", "/api/jobs?kind=compress", nil)
		res.Body.Close()
		id, _ := created["id"].(string)
		res, run := h.json("POST", "/api/jobs/"+id+"/run", map[string]any{
			"source_paths": []string{srcDir},
			"output_mode":  "server",
			"output_dir":   filepath.Join(outside, "escaped"),
		})
		res.Body.Close()
		if res.StatusCode != 400 && res.StatusCode != 403 {
			t.Fatalf("out-of-root output should be refused, got %d (%v)", res.StatusCode, run)
		}
		if _, err := os.Stat(filepath.Join(outside, "escaped")); err == nil {
			t.Fatal("the refused output directory should not have been created")
		}
	})

	t.Run("output inside the source directory", func(t *testing.T) {
		res, created := h.json("POST", "/api/jobs?kind=compress", nil)
		res.Body.Close()
		id, _ := created["id"].(string)
		res, run := h.json("POST", "/api/jobs/"+id+"/run", map[string]any{
			"source_paths": []string{srcDir},
			"output_mode":  "server",
			"output_dir":   srcDir,
			"name":         "self",
		})
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatalf("self-containing output should be refused, got %d (%v)", res.StatusCode, run)
		}
		msg, _ := run["error"].(string)
		if !strings.Contains(msg, "自我包含") {
			t.Fatalf("expected a self-containment error, got %q", msg)
		}
	})

	t.Run("several archives at once", func(t *testing.T) {
		res, created := h.json("POST", "/api/jobs?kind=extract", nil)
		res.Body.Close()
		id, _ := created["id"].(string)
		res, run := h.json("POST", "/api/jobs/"+id+"/run", map[string]any{
			"source_paths": []string{srcDir, srcDir},
		})
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatalf("two sources for one extract should be refused, got %d (%v)", res.StatusCode, run)
		}
	})
}

// TestInspectDirectory checks the labelling the UI relies on before it runs.
func TestInspectDirectory(t *testing.T) {
	shared := t.TempDir()
	dir := filepath.Join(shared, "folder")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := newHarness(t, Config{AllowRoots: []string{shared}})

	res, info := h.json("GET", "/api/inspect?path="+dir, nil)
	res.Body.Close()
	if info["is_dir"] != true {
		t.Fatalf("is_dir = %v", info["is_dir"])
	}
	if int(info["children"].(float64)) != 3 {
		t.Fatalf("children = %v, want 3", info["children"])
	}
	if info["suggested_output_dir"] != dir {
		t.Fatalf("suggested_output_dir = %v, want %v", info["suggested_output_dir"], dir)
	}
}

// TestInspectSingleArchive checks format detection and the default extraction
// target for a plain archive.
func TestInspectSingleArchive(t *testing.T) {
	shared := t.TempDir()
	archive := makeArchiveAt(t, shared, "solo.7z")
	h := newHarness(t, Config{AllowRoots: []string{shared}})

	res, info := h.json("GET", "/api/inspect?path="+archive, nil)
	res.Body.Close()
	if info["format"] != "7z" {
		t.Fatalf("format = %v, want 7z", info["format"])
	}
	if info["creatable"] != true {
		t.Fatalf("7z should be creatable: %v", info)
	}
	if _, hasVolume := info["volume"]; hasVolume {
		t.Fatalf("a single archive should not report a volume set: %v", info)
	}
	want := shared
	if info["suggested_output_dir"] != want {
		t.Fatalf("suggested_output_dir = %v, want %v", info["suggested_output_dir"], want)
	}
	if info["suggested_subdir"] != filepath.Join(shared, "solo") {
		t.Fatalf("suggested_subdir = %v, want %v", info["suggested_subdir"], filepath.Join(shared, "solo"))
	}
}

// makeArchiveAt writes a small plain archive next to the caller's directory.
func makeArchiveAt(t *testing.T, dir, name string) string {
	t.Helper()
	eng, err := engine.New()
	if err != nil {
		t.Skipf("7-Zip 不可用，跳过：%v", err)
	}
	work := filepath.Join(dir, "src-"+name)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "only.txt"), []byte("only"), 0o644); err != nil {
		t.Fatal(err)
	}
	produced, err := eng.Compress(context.Background(), engine.CompressOptions{
		Format: "7z", Level: 5, WorkDir: work, Sources: []string{"only.txt"},
		OutputName: strings.TrimSuffix(name, ".7z"), OutputDir: dir,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return produced[0]
}

// TestPreviewFromServerPath checks that the index can be read straight from a
// host file, which is what the context-menu flow does before showing options.
func TestPreviewFromServerPath(t *testing.T) {
	shared := t.TempDir()
	archive := makeArchiveAt(t, shared, "peek.7z")
	h := newHarness(t, Config{AllowRoots: []string{shared}})

	res, created := h.json("POST", "/api/jobs?kind=extract", nil)
	res.Body.Close()
	id, _ := created["id"].(string)

	res, preview := h.json("POST", "/api/jobs/"+id+"/preview", map[string]any{"path": archive})
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("preview status = %d (%v)", res.StatusCode, preview)
	}
	entries, _ := preview["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %v", entries)
	}
}

// TestCommonDir is the regression test for the bug that broke multi-select:
// splitting and rejoining path components dropped the leading separator, so
// the derived working directory came back relative and every later Rel call
// failed with "can't make X relative to Y".
func TestCommonDir(t *testing.T) {
	cases := []struct {
		name string
		dirs []string
		want string
	}{
		{"single directory", []string{"/data/photos"}, "/data/photos"},
		{"same directory twice", []string{"/data/src", "/data/src"}, "/data/src"},
		{"siblings share the parent", []string{"/data/x", "/data/y"}, "/data"},
		{"cousins climb two levels", []string{"/a/b/c", "/a/b/d"}, "/a/b"},
		{"deep divergence", []string{"/a/b/c", "/a/x/y"}, "/a"},
		{"no shared parent below root", []string{"/a", "/b"}, "/"},
		{"unicode components", []string{"/vol5/1000/空间4/YC-7ZIP/_uitest/src",
			"/vol5/1000/空间4/YC-7ZIP/_uitest/src"}, "/vol5/1000/空间4/YC-7ZIP/_uitest/src"},
		{"unicode divergence", []string{"/vol5/1000/空间4/YC-7ZIP/_uitest/src",
			"/vol5/1000/空间4/YC-7ZIP/_uitest/out"}, "/vol5/1000/空间4/YC-7ZIP/_uitest"},
		{"empty input", nil, string(os.PathSeparator)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := commonDir(tc.dirs)
			if got != tc.want {
				t.Fatalf("commonDir(%v) = %q, want %q", tc.dirs, got, tc.want)
			}
			if len(tc.dirs) > 0 && !filepath.IsAbs(got) {
				t.Fatalf("commonDir returned a relative path: %q", got)
			}
		})
	}
}

// TestResolveServerSourcesRelativePaths proves the derived working directory
// and member names are usable, which is what the multi-select flow needs.
func TestResolveServerSourcesRelativePaths(t *testing.T) {
	shared := t.TempDir()
	src := filepath.Join(shared, "src")
	if err := os.MkdirAll(filepath.Join(src, "相册"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"说明.txt", "相册/照片.txt", "blob.bin"} {
		full := filepath.Join(src, filepath.FromSlash(p))
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	h := newHarness(t, Config{AllowRoots: []string{shared}})
	paths := []string{
		filepath.Join(src, "说明.txt"),
		filepath.Join(src, "相册"),
		filepath.Join(src, "blob.bin"),
	}
	wd, names, err := h.srv.resolveServerSources(paths)
	if err != nil {
		t.Fatalf("resolveServerSources: %v", err)
	}
	if !filepath.IsAbs(wd) {
		t.Fatalf("working directory is not absolute: %q", wd)
	}
	if wd != src {
		t.Fatalf("working directory = %q, want %q", wd, src)
	}
	want := []string{"说明.txt", "相册", "blob.bin"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("member names = %v, want %v", names, want)
	}
	// Every derived name must resolve back to the file it came from.
	for i, name := range names {
		if _, err := os.Stat(filepath.Join(wd, name)); err != nil {
			t.Errorf("derived name %q does not resolve: %v", name, err)
		}
		if filepath.Join(wd, name) != paths[i] {
			t.Errorf("derived name %q maps to the wrong file", name)
		}
	}
}

// TestDownloadModeStillWorks guards against the server-side path quietly
// breaking the original upload-to-download flow.
func TestDownloadModeStillWorks(t *testing.T) {
	h := newHarness(t, Config{})

	res, created := h.json("POST", "/api/jobs?kind=compress", nil)
	res.Body.Close()
	id, _ := created["id"].(string)
	up := h.upload(id, map[string]string{"a.txt": "hello"})
	up.Body.Close()
	res, _ = h.json("POST", "/api/jobs/"+id+"/run", map[string]any{"format": "zip", "name": "dl"})
	res.Body.Close()
	final := h.waitDone(id, 60*time.Second)
	if final["status"] != "done" {
		t.Fatalf("download-mode compress failed: %v", final["message"])
	}
	if final["output_mode"] != "download" {
		t.Fatalf("output_mode = %v, want download", final["output_mode"])
	}
	dl := h.do("GET", "/api/jobs/"+id+"/download-all", nil, "")
	defer dl.Body.Close()
	raw, _ := io.ReadAll(dl.Body)
	if !bytes.HasPrefix(raw, []byte("PK")) {
		t.Fatalf("download is not a zip: % x", raw[:4])
	}
}
