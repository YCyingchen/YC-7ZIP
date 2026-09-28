package server

import (
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // 注册解码器，image.Decode 依赖
	"image/jpeg"
	_ "image/png" // 注册解码器
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Media handling for the gallery view.
//
// The server stays deliberately thin: it only decodes what the Go standard
// library already understands (JPEG, PNG, GIF) and hands everything else
// straight to the browser, which decodes far more formats than any Go
// dependency worth carrying — WebP, AVIF, and HEIC on Safari. Browsers also
// apply EXIF orientation themselves, so only the resized path has to rotate.

const (
	// thumbMaxSourcePixels bounds the work one thumbnail can cost. 60 MP
	// covers phone panoramas while refusing decompression bombs.
	thumbMaxSourcePixels = 60_000_000
	// thumbMaxSourceBytes bounds how much of a file we read to build a
	// thumbnail, so a huge TIFF cannot stall the request.
	thumbMaxSourceBytes = 96 << 20
	// thumbQuality is the JPEG quality of generated thumbnails.
	thumbQuality = 82
)

// imageExtensions may be shown as images and can get a generated thumbnail.
var imageExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".jpe": true, ".png": true, ".gif": true,
	".webp": true, ".bmp": true, ".avif": true, ".heic": true, ".heif": true,
	".tif": true, ".tiff": true, ".svg": true,
}

// videoExtensions are served for inline playback so the browser can paint a
// first frame and support seeking.
var videoExtensions = map[string]bool{
	".mp4": true, ".m4v": true, ".mov": true, ".webm": true, ".mkv": true,
	".avi": true, ".wmv": true, ".flv": true, ".ts": true, ".mpg": true, ".mpeg": true,
}

// IsMediaPath reports whether a path is something the gallery can show.
func IsMediaPath(name string) (isImage bool, isVideo bool) {
	ext := strings.ToLower(filepath.Ext(name))
	return imageExtensions[ext], videoExtensions[ext]
}

// rawURL builds a link to the original file, keeping the mount point the app
// is served under so redirects still work behind the fnOS gateway.
func (s *Server) rawURL(abs string) string {
	return strings.TrimSuffix(s.cfg.BasePath, "/") + "/api/raw?path=" + url.QueryEscape(abs)
}

// handleRaw serves an original media file inline, with Range support so video
// seeking works.
//
// Only media extensions are served. The picker already lets the user read any
// allowed file, and keeping this endpoint media-only stops it from becoming a
// general file-download URL.
func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	abs, err := s.validateServerPath(r.URL.Query().Get("path"), true)
	if err != nil {
		// 走统一的错误映射：越界是 403，路径不存在是 400
		writeEngineError(w, err)
		return
	}
	st, err := os.Stat(abs)
	if err != nil || st.IsDir() {
		writeError(w, http.StatusNotFound, "文件不存在")
		return
	}

	isImage, isVideo := IsMediaPath(abs)
	if !isImage && !isVideo {
		writeError(w, http.StatusUnsupportedMediaType, "该文件不是图片或视频")
		return
	}

	f, err := os.Open(abs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法打开文件："+err.Error())
		return
	}
	defer f.Close()

	ctype := mime.TypeByExtension(strings.ToLower(filepath.Ext(abs)))
	if ctype == "" {
		if isVideo {
			ctype = "video/mp4"
		} else {
			ctype = "application/octet-stream"
		}
	}
	w.Header().Set("Content-Type", ctype)
	// inline 而不是 attachment：这是给 img/video 直接引用的
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	// ServeContent 负责 Range、If-Modified-Since 与 206
	http.ServeContent(w, r, filepath.Base(abs), st.ModTime(), f)
}

// handleThumb returns a downscaled preview.
//
// Formats the standard library cannot decode are redirected to the raw file
// instead of failing, because the browser will scale them itself.
func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	abs, err := s.validateServerPath(r.URL.Query().Get("path"), true)
	if err != nil {
		writeEngineError(w, err)
		return
	}

	isImage, isVideo := IsMediaPath(abs)
	if !isImage {
		// 视频交给浏览器的 <video preload="metadata"> 自己出首帧，不占服务端
		if isVideo {
			http.Redirect(w, r, s.rawURL(abs), http.StatusFound)
			return
		}
		writeError(w, http.StatusUnsupportedMediaType, "该文件不是图片或视频")
		return
	}

	st, err := os.Stat(abs)
	if err != nil || st.IsDir() {
		writeError(w, http.StatusNotFound, "文件不存在")
		return
	}

	width := parseThumbWidth(r.URL.Query().Get("w"))
	// ETag 把尺寸也算进去，换尺寸不会命中旧缓存
	etag := fmt.Sprintf(`W/"%x-%d-%d-%d"`, fnv64(abs), st.ModTime().Unix(), st.Size(), width)
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	if !decodableImage(abs) || st.Size() > thumbMaxSourceBytes {
		http.Redirect(w, r, s.rawURL(abs), http.StatusFound)
		return
	}

	f, err := os.Open(abs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法打开文件："+err.Error())
		return
	}
	defer f.Close()

	// 先探一次 EXIF 方向，再回到文件开头正式解码
	orientation := 1
	if isJPEG(abs) {
		orientation = readJPEGOrientation(io.LimitReader(f, thumbMaxSourceBytes))
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	cfg, _, err := image.DecodeConfig(io.LimitReader(f, thumbMaxSourceBytes))
	if err != nil {
		http.Redirect(w, r, s.rawURL(abs), http.StatusFound)
		return
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > thumbMaxSourcePixels {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("图片尺寸过大（%d×%d），无法生成缩略图", cfg.Width, cfg.Height))
		return
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	src, _, err := image.Decode(io.LimitReader(f, thumbMaxSourceBytes))
	if err != nil {
		http.Redirect(w, r, s.rawURL(abs), http.StatusFound)
		return
	}

	thumb := applyOrientation(boxDownscale(src, width, width), orientation)

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("ETag", etag)
	_ = jpeg.Encode(w, thumb, &jpeg.Options{Quality: thumbQuality})
}

func isJPEG(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".jpe":
		return true
	default:
		return false
	}
}

func parseThumbWidth(raw string) int {
	w, err := strconv.Atoi(raw)
	if err != nil || w <= 0 {
		return 320
	}
	if w > 1024 {
		w = 1024
	}
	if w < 64 {
		w = 64
	}
	return w
}

// decodableImage reports whether the standard library can decode this format,
// which decides between a real thumbnail and a hand-off to the browser.
func decodableImage(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".jpe", ".png", ".gif":
		return true
	default:
		return false
	}
}

// boxDownscale averages source pixels into the destination, which is the right
// filter for shrinking and needs no external dependency. Go's standard library
// ships no resizer at all, and one box pass beats pulling in x/image here.
func boxDownscale(src image.Image, maxW, maxH int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw == 0 || sh == 0 {
		return src
	}
	if sw <= maxW && sh <= maxH {
		return src
	}

	scale := float64(maxW) / float64(sw)
	if h := float64(maxH) / float64(sh); h < scale {
		scale = h
	}
	dw := int(float64(sw) * scale)
	dh := int(float64(sh) * scale)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		y0 := b.Min.Y + y*sh/dh
		y1 := b.Min.Y + (y+1)*sh/dh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < dw; x++ {
			x0 := b.Min.X + x*sw/dw
			x1 := b.Min.X + (x+1)*sw/dw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, a, n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					cr, cg, cb, ca := src.At(xx, yy).RGBA()
					r += uint64(cr)
					g += uint64(cg)
					bl += uint64(cb)
					a += uint64(ca)
					n++
				}
			}
			if n == 0 {
				continue
			}
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8(r / n >> 8),
				G: uint8(g / n >> 8),
				B: uint8(bl / n >> 8),
				A: uint8(a / n >> 8),
			})
		}
	}
	return dst
}

// applyOrientation rotates a decoded image according to its EXIF orientation.
//
// Phone photos almost always carry a rotation tag; without this the gallery
// would show a wall of sideways pictures, which is the most visible way a
// photo browser can look broken.
func applyOrientation(src image.Image, orientation int) image.Image {
	if orientation <= 1 || orientation > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	swap := orientation >= 5 // 5-8 交换宽高

	var dst *image.RGBA
	if swap {
		dst = image.NewRGBA(image.Rect(0, 0, h, w))
	} else {
		dst = image.NewRGBA(image.Rect(0, 0, w, h))
	}

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var nx, ny int
			switch orientation {
			case 2:
				nx, ny = w-1-x, y
			case 3:
				nx, ny = w-1-x, h-1-y
			case 4:
				nx, ny = x, h-1-y
			case 5:
				nx, ny = y, x
			case 6:
				nx, ny = h-1-y, x
			case 7:
				nx, ny = h-1-y, w-1-x
			case 8:
				nx, ny = y, w-1-x
			default:
				nx, ny = x, y
			}
			dst.Set(nx, ny, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// readJPEGOrientation pulls tag 0x0112 out of the APP1/Exif segment.
func readJPEGOrientation(r io.Reader) int {
	data, err := io.ReadAll(io.LimitReader(r, 256<<10))
	if err != nil || len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		if marker == 0xDA { // 到了图像数据
			return 1
		}
		// 段边界（SOI/EOI/RST）没有长度字段
		if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		segLen := int(data[i+2])<<8 | int(data[i+3])
		if segLen < 2 || i+2+segLen > len(data) {
			return 1
		}
		if marker == 0xE1 { // APP1
			if o := parseExifOrientation(data[i+4 : i+2+segLen]); o != 1 {
				return o
			}
		}
		i += 2 + segLen
	}
	return 1
}

// parseExifOrientation reads the orientation tag out of an APP1 payload.
func parseExifOrientation(seg []byte) int {
	if len(seg) < 14 || string(seg[:6]) != "Exif\x00\x00" {
		return 1
	}
	tiff := seg[6:]
	var bigEndian bool
	switch {
	case tiff[0] == 'M' && tiff[1] == 'M':
		bigEndian = true
	case tiff[0] == 'I' && tiff[1] == 'I':
		bigEndian = false
	default:
		return 1
	}
	u16 := func(b []byte) int {
		if bigEndian {
			return int(b[0])<<8 | int(b[1])
		}
		return int(b[1])<<8 | int(b[0])
	}
	u32 := func(b []byte) uint32 {
		if bigEndian {
			return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
		}
		return uint32(b[3])<<24 | uint32(b[2])<<16 | uint32(b[1])<<8 | uint32(b[0])
	}

	if len(tiff) < 8 {
		return 1
	}
	ifdOffset := int(u32(tiff[4:8]))
	if ifdOffset < 8 || ifdOffset+2 > len(tiff) {
		return 1
	}
	count := u16(tiff[ifdOffset : ifdOffset+2])
	entry := ifdOffset + 2
	for n := 0; n < count; n++ {
		if entry+12 > len(tiff) {
			return 1
		}
		if u16(tiff[entry:entry+2]) == 0x0112 {
			return u16(tiff[entry+8 : entry+10])
		}
		entry += 12
	}
	return 1
}

// fnv64 hashes a string for ETag generation.
func fnv64(s string) uint64 {
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)
	h := uint64(offset)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime
	}
	return h
}
