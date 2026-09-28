package server

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// makeJPEG writes a solid-colour JPEG and returns its path.
func makeJPEG(t *testing.T, path string, w, h int, orientation int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	if orientation > 1 {
		data = insertEXIFOrientation(t, data, orientation)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// insertEXIFOrientation splices a minimal APP1/Exif segment carrying just the
// orientation tag right after SOI, which is what a camera writes.
func insertEXIFOrientation(t *testing.T, jpegData []byte, orientation int) []byte {
	t.Helper()

	tiff := &bytes.Buffer{}
	tiff.WriteString("MM") // big endian
	binary.Write(tiff, binary.BigEndian, uint16(42))
	binary.Write(tiff, binary.BigEndian, uint32(8)) // IFD0 紧跟在 TIFF 头后
	binary.Write(tiff, binary.BigEndian, uint16(1)) // 一个条目
	binary.Write(tiff, binary.BigEndian, uint16(0x0112))
	binary.Write(tiff, binary.BigEndian, uint16(3)) // SHORT
	binary.Write(tiff, binary.BigEndian, uint32(1))
	binary.Write(tiff, binary.BigEndian, uint16(orientation))
	binary.Write(tiff, binary.BigEndian, uint16(0)) // 补齐 4 字节
	binary.Write(tiff, binary.BigEndian, uint32(0)) // 无下一个 IFD

	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	seg := make([]byte, 0, len(payload)+4)
	seg = append(seg, 0xFF, 0xE1)
	seg = binary.BigEndian.AppendUint16(seg, uint16(len(payload)+2))
	seg = append(seg, payload...)

	out := make([]byte, 0, len(jpegData)+len(seg))
	out = append(out, jpegData[:2]...) // SOI
	out = append(out, seg...)
	out = append(out, jpegData[2:]...)
	return out
}

func TestParseExifOrientation(t *testing.T) {
	for _, want := range []int{1, 3, 6, 8} {
		path := filepath.Join(t.TempDir(), "o.jpg")
		makeJPEG(t, path, 8, 8, want)
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		got := readJPEGOrientation(f)
		f.Close()
		if got != want {
			t.Errorf("orientation = %d, want %d", got, want)
		}
	}

	// 普通 JPEG 没有 EXIF，应当返回 1 而不是报错
	plain := filepath.Join(t.TempDir(), "plain.jpg")
	makeJPEG(t, plain, 8, 8, 1)
	f, _ := os.Open(plain)
	defer f.Close()
	if got := readJPEGOrientation(f); got != 1 {
		t.Errorf("plain jpeg orientation = %d, want 1", got)
	}
}

func TestBoxDownscale(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1000, 400))

	// 横图缩到 320 宽，比例保持
	got := boxDownscale(src, 320, 320)
	if b := got.Bounds(); b.Dx() != 320 || b.Dy() != 128 {
		t.Fatalf("bounds = %dx%d, want 320x128", b.Dx(), b.Dy())
	}

	// 已经足够小的图原样返回，不放大
	small := image.NewRGBA(image.Rect(0, 0, 100, 80))
	if boxDownscale(small, 320, 320) != image.Image(small) {
		t.Fatal("a small image should be returned untouched")
	}

	// 竖图受限的是高度
	tall := image.NewRGBA(image.Rect(0, 0, 400, 1000))
	if b := boxDownscale(tall, 320, 320).Bounds(); b.Dx() != 128 || b.Dy() != 320 {
		t.Fatalf("tall bounds = %dx%d, want 128x320", b.Dx(), b.Dy())
	}
}

func TestApplyOrientationSwapsSides(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 100, 50))
	// 6 与 8 是 90 度旋转，宽高应当交换
	for _, o := range []int{5, 6, 7, 8} {
		b := applyOrientation(src, o).Bounds()
		if b.Dx() != 50 || b.Dy() != 100 {
			t.Errorf("orientation %d bounds = %dx%d, want 50x100", o, b.Dx(), b.Dy())
		}
	}
	// 2/3/4 只是翻转，尺寸不变
	for _, o := range []int{2, 3, 4} {
		b := applyOrientation(src, o).Bounds()
		if b.Dx() != 100 || b.Dy() != 50 {
			t.Errorf("orientation %d bounds = %dx%d, want 100x50", o, b.Dx(), b.Dy())
		}
	}
	if applyOrientation(src, 1) != image.Image(src) {
		t.Error("orientation 1 should be a no-op")
	}
}

// TestThumbEndpoint covers the happy path: a real JPEG comes back smaller.
func TestThumbEndpoint(t *testing.T) {
	shared := t.TempDir()
	makeJPEG(t, filepath.Join(shared, "photo.jpg"), 1600, 1200, 1)
	h := newHarness(t, Config{AllowRoots: []string{shared}})

	res := h.do("GET", "/api/thumb?path="+urlEnc(filepath.Join(shared, "photo.jpg"))+"&w=200", nil, "")
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("thumb status = %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("content-type = %q", ct)
	}
	cfg, format, err := image.DecodeConfig(res.Body)
	if err != nil {
		t.Fatalf("returned bytes are not an image: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("format = %q", format)
	}
	if cfg.Width != 200 {
		t.Fatalf("thumb width = %d, want 200", cfg.Width)
	}
	if cfg.Height != 150 {
		t.Fatalf("thumb height = %d, want 150", cfg.Height)
	}
	if res.Header.Get("ETag") == "" {
		t.Error("a thumbnail should carry an ETag")
	}
}

// TestThumbRespectsExifOrientation is the visible-symptom regression test: a
// phone photo shot in portrait and tagged "rotate 90" must come back portrait.
//
// Thumbnails are fitted into a W×W box, so a landscape source yields W wide and
// a portrait source yields W tall. Rotation and uniform scaling commute, so the
// observable effect of honouring the tag is that the aspect ratio flips.
func TestThumbRespectsExifOrientation(t *testing.T) {
	shared := t.TempDir()
	makeJPEG(t, filepath.Join(shared, "landscape.jpg"), 1600, 800, 1)
	makeJPEG(t, filepath.Join(shared, "rotated.jpg"), 1600, 800, 6)
	h := newHarness(t, Config{AllowRoots: []string{shared}})

	thumbSize := func(name string) (int, int) {
		res := h.do("GET", "/api/thumb?path="+urlEnc(filepath.Join(shared, name))+"&w=200", nil, "")
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: status = %d", name, res.StatusCode)
		}
		cfg, _, err := image.DecodeConfig(res.Body)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return cfg.Width, cfg.Height
	}

	w, hgt := thumbSize("landscape.jpg")
	if w != 200 || hgt != 100 {
		t.Fatalf("landscape thumb = %dx%d, want 200x100", w, hgt)
	}

	w, hgt = thumbSize("rotated.jpg")
	if w != 100 || hgt != 200 {
		t.Fatalf("rotated thumb = %dx%d, want 100x200 (portrait, orientation applied)", w, hgt)
	}
}

// TestThumbRedirectsForBrowserOnlyFormats checks the hand-off path: the server
// must not fail on formats it cannot decode, it must point at the original.
func TestThumbRedirectsForBrowserOnlyFormats(t *testing.T) {
	shared := t.TempDir()
	if err := os.WriteFile(filepath.Join(shared, "anim.webp"), []byte("RIFF....WEBP"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, Config{AllowRoots: []string{shared}})

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	req, _ := http.NewRequest("GET", h.server.URL+"/api/thumb?path="+urlEnc(filepath.Join(shared, "anim.webp")), nil)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302 to the raw file", res.StatusCode)
	}
	if loc := res.Header.Get("Location"); loc == "" || !bytes.Contains([]byte(loc), []byte("/api/raw")) {
		t.Fatalf("location = %q, want an /api/raw link", loc)
	}
}

// TestRawEndpoint covers inline serving and Range support, which is what makes
// video seeking work.
func TestRawEndpoint(t *testing.T) {
	shared := t.TempDir()
	body := []byte("0123456789abcdefghij")
	if err := os.WriteFile(filepath.Join(shared, "clip.mp4"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, Config{AllowRoots: []string{shared}})

	res := h.do("GET", "/api/raw?path="+urlEnc(filepath.Join(shared, "clip.mp4")), nil, "")
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("raw status = %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "video/mp4" {
		t.Fatalf("content-type = %q, want video/mp4", ct)
	}
	if cd := res.Header.Get("Content-Disposition"); cd != "inline" {
		t.Fatalf("content-disposition = %q, want inline", cd)
	}
	if got := res.Header.Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("accept-ranges = %q, want bytes", got)
	}

	// Range 请求应当拿到 206 与正确的片段
	req, _ := http.NewRequest("GET", h.server.URL+"/api/raw?path="+urlEnc(filepath.Join(shared, "clip.mp4")), nil)
	req.Header.Set("Range", "bytes=5-9")
	part, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer part.Body.Close()
	if part.StatusCode != http.StatusPartialContent {
		t.Fatalf("range status = %d, want 206", part.StatusCode)
	}
	buf := make([]byte, 16)
	n, _ := part.Body.Read(buf)
	if string(buf[:n]) != "56789" {
		t.Fatalf("range body = %q, want 56789", buf[:n])
	}
}

// TestMediaEndpointGuards covers the boundaries: non-media files, missing
// files, and paths outside the allow-roots.
func TestMediaEndpointGuards(t *testing.T) {
	shared := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(shared, "notes.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, Config{AllowRoots: []string{shared}})

	cases := []struct {
		name string
		url  string
		want int
	}{
		{"非媒体文件不给原文件", "/api/raw?path=" + urlEnc(filepath.Join(shared, "notes.txt")), http.StatusUnsupportedMediaType},
		{"不存在的文件", "/api/raw?path=" + urlEnc(filepath.Join(shared, "nope.mp4")), http.StatusBadRequest},
		{"越界路径", "/api/raw?path=" + urlEnc(filepath.Join(outside, "secret.jpg")), http.StatusForbidden},
		{"缩略图越界路径", "/api/thumb?path=" + urlEnc(filepath.Join(outside, "secret.jpg")), http.StatusForbidden},
		{"缩略图非媒体", "/api/thumb?path=" + urlEnc(filepath.Join(shared, "notes.txt")), http.StatusUnsupportedMediaType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := h.do("GET", tc.url, nil, "")
			res.Body.Close()
			if res.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.want)
			}
		})
	}
}

// TestParseThumbWidth checks the clamps that keep a client from asking for a
// full-size decode.
func TestParseThumbWidth(t *testing.T) {
	cases := map[string]int{"": 320, "0": 320, "-5": 320, "abc": 320, "1": 64, "200": 200, "99999": 1024}
	for in, want := range cases {
		if got := parseThumbWidth(in); got != want {
			t.Errorf("parseThumbWidth(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestIsMediaPath(t *testing.T) {
	img, vid := IsMediaPath("a.JPG")
	if !img || vid {
		t.Fatal("JPG should be an image")
	}
	img, vid = IsMediaPath("a.mp4")
	if img || !vid {
		t.Fatal("mp4 should be a video")
	}
	img, vid = IsMediaPath("a.7z")
	if img || vid {
		t.Fatal("7z is neither")
	}
}

// urlEnc percent-encodes a path for use in a query string.
func urlEnc(p string) string {
	var b bytes.Buffer
	for _, r := range []byte(p) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '/', r == '-', r == '_', r == '.', r == '~':
			b.WriteByte(r)
		default:
			const hex = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hex[r>>4])
			b.WriteByte(hex[r&0x0f])
		}
	}
	return b.String()
}
