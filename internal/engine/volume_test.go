package engine

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// touch creates an empty placeholder file, which is enough to exercise the
// naming-scheme logic without writing real archive data.
func touch(t *testing.T, dir, name string) string {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

func baseName(p string) string { return filepath.Base(p) }

func TestResolveVolumes(t *testing.T) {
	cases := []struct {
		name       string
		files      []string
		selected   string
		scheme     string
		first      string
		count      int
		missing    []string
		notAVolume bool
	}{
		{
			name:     "7z numeric suffix",
			files:    []string{"movie.7z.001", "movie.7z.002", "movie.7z.003"},
			selected: "movie.7z.001",
			scheme:   "NNN", first: "movie.7z.001", count: 3,
		},
		{
			name:     "tar.gz numeric suffix",
			files:    []string{"backup.tar.gz.001", "backup.tar.gz.002"},
			selected: "backup.tar.gz.002",
			scheme:   "NNN", first: "backup.tar.gz.001", count: 2,
		},
		{
			name:     "numeric gap is reported",
			files:    []string{"movie.7z.001", "movie.7z.003"},
			selected: "movie.7z.001",
			scheme:   "NNN", first: "movie.7z.001", count: 2,
			missing: []string{"movie.7z.002"},
		},
		{
			name:     "numbered from the middle still points at volume 1",
			files:    []string{"movie.7z.001", "movie.7z.002", "movie.7z.003"},
			selected: "movie.7z.003",
			scheme:   "NNN", first: "movie.7z.001", count: 3,
		},
		{
			name:     "modern rar partN",
			files:    []string{"series.part1.rar", "series.part2.rar", "series.part3.rar"},
			selected: "series.part1.rar",
			scheme:   "partN.rar", first: "series.part1.rar", count: 3,
		},
		{
			name:     "modern rar keeps two digit padding",
			files:    []string{"series.part01.rar", "series.part02.rar"},
			selected: "series.part02.rar",
			scheme:   "partN.rar", first: "series.part01.rar", count: 2,
		},
		{
			name:     "modern rar missing middle part",
			files:    []string{"series.part1.rar", "series.part3.rar"},
			selected: "series.part1.rar",
			scheme:   "partN.rar", first: "series.part1.rar", count: 2,
			missing: []string{"series.part2.rar"},
		},
		{
			name:     "legacy rar from the .rar",
			files:    []string{"old.rar", "old.r00", "old.r01"},
			selected: "old.rar",
			scheme:   "rNN", first: "old.rar", count: 3,
		},
		{
			name:     "legacy rar from an .rNN part",
			files:    []string{"old.rar", "old.r00", "old.r01"},
			selected: "old.r01",
			scheme:   "rNN", first: "old.rar", count: 3,
		},
		{
			name:     "legacy rar missing the leading .rar",
			files:    []string{"old.r00", "old.r01"},
			selected: "old.r00",
			scheme:   "rNN", first: "old.rar", count: 2,
			missing: []string{"old.rar"},
		},
		{
			name:     "split zip from the .zip",
			files:    []string{"pack.zip", "pack.z01", "pack.z02"},
			selected: "pack.zip",
			scheme:   "zNN", first: "pack.zip", count: 3,
		},
		{
			name:       "a plain archive is not a volume set",
			files:      []string{"plain.7z"},
			selected:   "plain.7z",
			notAVolume: true,
		},
		{
			name:       "a dated file name is not mistaken for a volume",
			files:      []string{"report.2024"},
			selected:   "report.2024",
			notAVolume: true,
		},
		{
			name:       "an ordinary file is untouched",
			files:      []string{"notes.txt"},
			selected:   "notes.txt",
			notAVolume: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tc.files {
				touch(t, dir, f)
			}
			selected := filepath.Join(dir, tc.selected)

			set, ok, err := ResolveVolumes(selected)
			if err != nil {
				t.Fatal(err)
			}
			if tc.notAVolume {
				if ok {
					t.Fatalf("expected not a volume set, got %+v", set)
				}
				return
			}
			if !ok {
				t.Fatal("expected a volume set")
			}
			if set.Scheme != tc.scheme {
				t.Errorf("scheme = %q, want %q", set.Scheme, tc.scheme)
			}
			if baseName(set.First) != tc.first {
				t.Errorf("first = %q, want %q", baseName(set.First), tc.first)
			}
			if len(set.Volumes) != tc.count {
				t.Errorf("volumes = %d, want %d (%v)", len(set.Volumes), tc.count, names(set.Volumes))
			}
			gotMissing := names(set.Missing)
			if strings.Join(gotMissing, ",") != strings.Join(tc.missing, ",") {
				t.Errorf("missing = %v, want %v", gotMissing, tc.missing)
			}
			if len(tc.missing) == 0 && !set.Complete() {
				t.Errorf("expected a complete set, missing %v", gotMissing)
			}
		})
	}
}

func names(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, filepath.Base(p))
	}
	return out
}

// TestSplitVolumeExtraction is the end-to-end proof that a real multi-volume
// archive produced by 7-Zip can be read back. This is the behaviour the user
// asked for explicitly, and it is the one thing a naive implementation gets
// wrong by pointing 7-Zip at the wrong part.
func TestSplitVolumeExtraction(t *testing.T) {
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
	// Incompressible payload so the split actually happens.
	blob := make([]byte, 3<<20)
	if _, err := rand.Read(blob); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "payload.bin"), blob, 0o644); err != nil {
		t.Fatal(err)
	}

	produced, err := eng.Compress(ctx, CompressOptions{
		Format: "7z", Level: 1, VolumeSize: "512k",
		WorkDir: work, Sources: []string{"payload.bin"},
		OutputName: "split", OutputDir: out,
	}, nil)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if len(produced) < 2 {
		t.Fatalf("expected a split archive, got %v", produced)
	}

	// Point at a middle volume on purpose: the resolver must still use 001.
	set, ok, err := ResolveVolumes(produced[1])
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("a split archive was not recognised as a volume set")
	}
	if baseName(set.First) != "split.7z.001" {
		t.Fatalf("first volume = %q, want split.7z.001", baseName(set.First))
	}
	if !set.Complete() {
		t.Fatalf("set reported incomplete: %v", names(set.Missing))
	}
	if len(set.Volumes) != len(produced) {
		t.Fatalf("resolved %d volumes, expected %d", len(set.Volumes), len(produced))
	}

	// Listing and extracting must both work from the resolved first volume.
	info, err := eng.List(ctx, set.First, "", nil)
	if err != nil {
		t.Fatalf("List from volume 1: %v", err)
	}
	if len(info.Entries) == 0 {
		t.Fatal("no entries listed from volume 1")
	}

	dest := filepath.Join(base, "unpacked")
	if _, err := eng.Extract(ctx, ExtractOptions{
		Archive: set.First, OutputDir: dest, PreservePaths: true, Overwrite: true,
	}, nil); err != nil {
		t.Fatalf("Extract from volume 1: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "payload.bin"))
	if err != nil {
		t.Fatalf("extracted payload missing: %v", err)
	}
	if len(got) != len(blob) {
		t.Fatalf("payload size = %d, want %d", len(got), len(blob))
	}
	for i := range blob {
		if got[i] != blob[i] {
			t.Fatalf("payload differs at byte %d", i)
		}
	}
}

// TestSplitVolumeMissingPartFails confirms an incomplete series produces a
// clear error rather than a confusing 7-Zip message.
func TestSplitVolumeMissingPartFails(t *testing.T) {
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
	produced, err := eng.Compress(ctx, CompressOptions{
		Format: "7z", Level: 1, VolumeSize: "512k",
		WorkDir: work, Sources: []string{"payload.bin"},
		OutputName: "broken", OutputDir: out,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(produced) < 3 {
		t.Skipf("need at least 3 volumes to punch a hole, got %d", len(produced))
	}
	// Remove a middle volume, the usual shape of an unfinished download.
	if err := os.Remove(produced[1]); err != nil {
		t.Fatal(err)
	}

	set, ok, err := ResolveVolumes(produced[0])
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected a volume set")
	}
	if set.Complete() {
		t.Fatal("set should be reported incomplete after removing a part")
	}
	if len(set.Missing) == 0 {
		t.Fatal("the missing part was not identified")
	}
}
