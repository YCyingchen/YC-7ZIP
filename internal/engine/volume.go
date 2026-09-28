package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// VolumeSet describes a split (multi-volume) archive series.
//
// Users meet four naming schemes in the wild and all four have to work,
// because the user's instinct is to double-click whatever file they can see:
//
//	name.7z.001  name.7z.002   …  7-Zip, zip, tar.gz and friends
//	name.part1.rar …              modern RAR
//	name.rar  name.r00  name.r01  legacy RAR
//	name.zip  name.z01  name.z02  zip -s
//
// Only the first volume is a valid entry point, so the tool always resolves the
// series and points 7-Zip at volume 1 no matter which part the user selected.
type VolumeSet struct {
	// First is the absolute path of volume 1: what 7-Zip must be told to read.
	First string
	// Selected is the volume the user actually picked.
	Selected string
	// Volumes lists every present part, in order starting from volume 1.
	Volumes []string
	// Missing lists expected part names that are absent, which is the usual
	// reason a split archive refuses to open.
	Missing []string
	// Scheme is the naming convention that was recognised.
	Scheme string
}

// Complete reports whether every expected part is present.
func (v *VolumeSet) Complete() bool { return len(v.Missing) == 0 }

// Label renders a short human readable description, e.g.
// "name.7z.001 … name.7z.005（共 5 卷）".
func (v *VolumeSet) Label() string {
	if len(v.Volumes) == 0 {
		return ""
	}
	first := filepath.Base(v.Volumes[0])
	last := filepath.Base(v.Volumes[len(v.Volumes)-1])
	if first == last {
		return fmt.Sprintf("%s（共 1 卷）", first)
	}
	return fmt.Sprintf("%s … %s（共 %d 卷）", first, last, len(v.Volumes))
}

var (
	// name.7z.001 — three or four digit sequence number.
	numericVolumeRe = regexp.MustCompile(`^(.+)\.(\d{3,4})$`)
	// name.part1.rar / name.part01.rar
	partRarRe = regexp.MustCompile(`(?i)^(.+)\.part(\d{1,3})\.rar$`)
	// name.r00 / name.r01 (legacy RAR continuation parts)
	legacyRarRe = regexp.MustCompile(`(?i)^(.+)\.r(\d{2,3})$`)
	// name.z01 / name.z02 (split zip continuation parts)
	splitZipRe = regexp.MustCompile(`(?i)^(.+)\.z(\d{2,3})$`)
)

// ResolveVolumes inspects path and, when it belongs to a split archive series,
// returns the resolved set. A plain archive yields ok == false.
//
// The caller gets an error only for genuinely broken input (an unreadable
// directory); an incomplete series is reported through VolumeSet.Missing so the
// UI can name the parts that are absent.
func ResolveVolumes(path string) (*VolumeSet, bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, false, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, false, err
	}
	if st.IsDir() {
		return nil, false, nil
	}

	dir := filepath.Dir(abs)
	name := filepath.Base(abs)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false, err
	}
	present := make(map[string]bool, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			present[e.Name()] = true
		}
	}

	join := func(n string) string { return filepath.Join(dir, n) }

	// 1. name.partN.rar — modern RAR.
	if m := partRarRe.FindStringSubmatch(name); m != nil {
		base := m[1]
		re := regexp.MustCompile(`(?i)^` + regexp.QuoteMeta(base) + `\.part(\d{1,3})\.rar$`)
		names, nums := collectNumbered(present, re)
		if len(names) > 0 {
			width := padWidth(nums)
			missing := expectedNames(nums, func(n int) string {
				return fmt.Sprintf("%s.part%0*d.rar", base, width, n)
			})
			return buildSet(dir, name, "partN.rar", names, missing, join), true, nil
		}
	}

	// 2. name.rNN — legacy RAR. Volume 1 is the sibling name.rar.
	if m := legacyRarRe.FindStringSubmatch(name); m != nil {
		base := m[1]
		re := regexp.MustCompile(`(?i)^` + regexp.QuoteMeta(base) + `\.r(\d{2,3})$`)
		names, _ := collectNumbered(present, re)
		first := base + ".rar"
		// Volume 1 is the .rar whether or not it is present: pointing 7-Zip at
		// an .rNN part cannot work, so an absent .rar must surface as missing
		// rather than silently becoming the entry point.
		vols := []string{}
		if present[first] {
			vols = append(vols, join(first))
		}
		for _, n := range names {
			vols = append(vols, join(n))
		}
		if len(vols) > 0 {
			missing := []string{}
			if !present[first] {
				missing = append(missing, first)
			}
			return &VolumeSet{
				First:    join(first),
				Selected: abs,
				Volumes:  vols,
				Missing:  missing,
				Scheme:   "rNN",
			}, true, nil
		}
	}

	// 3. name.rar with a matching name.r00 — legacy RAR seen from volume 1.
	if strings.EqualFold(filepath.Ext(name), ".rar") {
		base := name[:len(name)-4]
		re := regexp.MustCompile(`(?i)^` + regexp.QuoteMeta(base) + `\.r(\d{2,3})$`)
		if names, _ := collectNumbered(present, re); len(names) > 0 {
			vols := []string{abs}
			for _, n := range names {
				vols = append(vols, join(n))
			}
			return &VolumeSet{
				First:    abs,
				Selected: abs,
				Volumes:  vols,
				Scheme:   "rNN",
			}, true, nil
		}
	}

	// 4. name.zip with a matching name.zNN — split zip seen from volume 1.
	if strings.EqualFold(filepath.Ext(name), ".zip") {
		base := name[:len(name)-4]
		re := regexp.MustCompile(`(?i)^` + regexp.QuoteMeta(base) + `\.z(\d{2,3})$`)
		if names, _ := collectNumbered(present, re); len(names) > 0 {
			vols := []string{abs}
			for _, n := range names {
				vols = append(vols, join(n))
			}
			return &VolumeSet{
				First:    abs,
				Selected: abs,
				Volumes:  vols,
				Scheme:   "zNN",
			}, true, nil
		}
	}

	// 5. name.zNN — split zip. Volume 1 is the sibling name.zip.
	if m := splitZipRe.FindStringSubmatch(name); m != nil {
		base := m[1]
		re := regexp.MustCompile(`(?i)^` + regexp.QuoteMeta(base) + `\.z(\d{2,3})$`)
		names, _ := collectNumbered(present, re)
		first := base + ".zip"
		vols := []string{}
		if present[first] {
			vols = append(vols, join(first))
		}
		for _, n := range names {
			vols = append(vols, join(n))
		}
		if len(vols) > 0 {
			missing := []string{}
			if !present[first] {
				missing = append(missing, first)
			}
			return &VolumeSet{
				First:    join(first),
				Selected: abs,
				Volumes:  vols,
				Missing:  missing,
				Scheme:   "zNN",
			}, true, nil
		}
	}

	// 6. name.<ext>.NNN — the 7-Zip / zip / tar scheme.
	if m := numericVolumeRe.FindStringSubmatch(name); m != nil {
		base := m[1]
		// Require the stem to look like an archive. Without this, an ordinary
		// file such as "report.2024" would be mistaken for volume 0024 of a
		// series that does not exist.
		if _, isArchive := ExtensionToFormat(base); isArchive {
			re := regexp.MustCompile(`^` + regexp.QuoteMeta(base) + `\.(\d{3,4})$`)
			names, nums := collectNumbered(present, re)
			if len(names) > 0 {
				width := padWidth(nums)
				if width < 3 {
					width = 3
				}
				missing := expectedNames(nums, func(n int) string {
					return fmt.Sprintf("%s.%0*d", base, width, n)
				})
				return buildSet(dir, name, "NNN", names, missing, join), true, nil
			}
		}
	}

	return nil, false, nil
}

// buildSet assembles a VolumeSet for the schemes whose volume 1 is simply the
// lowest numbered part.
func buildSet(
	dir, selected, scheme string,
	names []string,
	missing []string,
	join func(string) string,
) *VolumeSet {
	vols := make([]string, 0, len(names))
	for _, n := range names {
		vols = append(vols, join(n))
	}
	missingPaths := make([]string, 0, len(missing))
	for _, n := range missing {
		missingPaths = append(missingPaths, join(n))
	}
	// Volume 1 is the first present part; when part 001 itself is absent the
	// series is broken and complete() will say so.
	first := ""
	if len(vols) > 0 {
		first = vols[0]
	}
	return &VolumeSet{
		First:    first,
		Selected: filepath.Join(dir, selected),
		Volumes:  vols,
		Missing:  missingPaths,
		Scheme:   scheme,
	}
}

// collectNumbered returns the directory entries matching re, ordered by the
// numeric capture group, along with the parsed numbers.
func collectNumbered(present map[string]bool, re *regexp.Regexp) ([]string, []int) {
	type item struct {
		name string
		num  int
	}
	var items []item
	for name := range present {
		m := re.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		num, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		items = append(items, item{name: name, num: num})
	}
	sort.Slice(items, func(a, b int) bool { return items[a].num < items[b].num })

	names := make([]string, 0, len(items))
	nums := make([]int, 0, len(items))
	for _, it := range items {
		names = append(names, it.name)
		nums = append(nums, it.num)
	}
	return names, nums
}

// expectedNames reports the parts that should exist between volume 1 and the
// highest observed number but do not. nameFor renders the file name of part n
// using the padding width actually observed on disk, so the reported name is
// the one the user has to go and find.
func expectedNames(nums []int, nameFor func(int) string) []string {
	if len(nums) == 0 {
		return nil
	}
	have := make(map[int]bool, len(nums))
	for _, n := range nums {
		have[n] = true
	}
	// The series always starts at 1. A gap at the top is undetectable, but a
	// hole in the middle — the usual symptom of an unfinished download — is.
	var missing []string
	for n := 1; n <= nums[len(nums)-1]; n++ {
		if !have[n] {
			missing = append(missing, nameFor(n))
		}
	}
	return missing
}

// padWidth returns the digit width used by the observed numbers, so a series
// written as part01 keeps its two digit padding in the message.
func padWidth(nums []int) int {
	width := 1
	for _, n := range nums {
		if w := len(strconv.Itoa(n)); w > width {
			width = w
		}
	}
	return width
}

func firstOr(list []string, fallback string) string {
	if len(list) > 0 {
		return list[0]
	}
	return fallback
}

// DescribeVolumes renders the parts of a series for a log line or an error.
func DescribeVolumes(v *VolumeSet) string {
	if v == nil {
		return ""
	}
	parts := make([]string, 0, len(v.Volumes))
	for _, p := range v.Volumes {
		parts = append(parts, filepath.Base(p))
	}
	return strings.Join(parts, ", ")
}
