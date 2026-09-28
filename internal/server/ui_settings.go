package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 界面设置：壁纸与它的呈现强度。
//
// 存服务端而不是浏览器本地，原因很实际：飞牛里这个应用是嵌在 iframe 里的，
// 换台设备、清一次站点数据就没了；存在服务端则一次设置处处生效。

const (
	// wallpaperMaxBytes 限制壁纸大小。4K 的 JPEG 一般 2–6 MB，
	// 给到 24 MB 足够，同时挡住"误传一个 200 MB 的原图"。
	wallpaperMaxBytes = 24 << 20
	// uiDirName 是数据目录下的子目录名。
	uiDirName = "ui"
)

// UISettings 是界面可调项。
type UISettings struct {
	// Wallpaper 关闭后保留文件，下次打开不用重新选。
	Wallpaper bool `json:"wallpaper"`
	// Dim 是叠加的暗化程度（0-90），保证浅色壁纸上文字仍然可读。
	Dim int `json:"dim"`
	// Blur 是背景模糊半径（0-40 px）。
	Blur int `json:"blur"`
	// Fit 是铺满方式：cover / contain / tile。
	Fit string `json:"fit"`
	// PanelAlpha 是操作面板的透明程度（0-90 百分比）。0 表示不透明，
	// 与不设壁纸时的观感一致；调高壁纸会透出来，文字可读性则更依赖上面的暗化。
	PanelAlpha int `json:"panel_alpha"`
	// HasWallpaper 由服务端填，告诉界面有没有可用的图。
	HasWallpaper bool   `json:"has_wallpaper"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

func defaultSettings() UISettings {
	// PanelAlpha 默认 8：这是有壁纸时原本就有的那点"略透"，作为默认值保留下来，
	// 免得升级之后观感突变。想要完全不透明就把它拉到 0。
	return UISettings{Wallpaper: false, Dim: 45, Blur: 0, Fit: "cover", PanelAlpha: 8}
}

// uiStore 负责读写设置与壁纸文件。
type uiStore struct {
	dir string

	mu       sync.Mutex
	settings UISettings
	// wallpaperName 是当前壁纸文件名，为空表示没有
	wallpaperName string
}

func newUIStore(dataDir string) *uiStore {
	dir := filepath.Join(dataDir, uiDirName)
	store := &uiStore{dir: dir, settings: defaultSettings()}
	store.load()
	return store
}

// load 读取磁盘上的设置；任何问题都退回默认值，界面不该因为设置坏了打不开。
func (u *uiStore) load() {
	if u.dir == "" {
		return
	}
	if raw, err := os.ReadFile(filepath.Join(u.dir, "settings.json")); err == nil {
		var s UISettings
		if json.Unmarshal(raw, &s) == nil {
			s = sanitizeSettings(s)
			u.settings = s
		}
	}
	u.wallpaperName = u.findWallpaper()
	u.settings.HasWallpaper = u.wallpaperName != ""
}

// findWallpaper 找出已有的壁纸文件（扩展名按内容变，所以按前缀找）。
func (u *uiStore) findWallpaper() string {
	entries, err := os.ReadDir(u.dir)
	if err != nil {
		return ""
	}
	var found []string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && strings.HasPrefix(name, "wallpaper.") {
			found = append(found, name)
		}
	}
	if len(found) == 0 {
		return ""
	}
	sort.Strings(found)
	return found[0]
}

func sanitizeSettings(s UISettings) UISettings {
	if s.Dim < 0 {
		s.Dim = 0
	}
	if s.Dim > 90 {
		s.Dim = 90
	}
	if s.Blur < 0 {
		s.Blur = 0
	}
	if s.Blur > 40 {
		s.Blur = 40
	}
	switch s.Fit {
	case "cover", "contain", "tile":
	default:
		s.Fit = "cover"
	}
	// 上限 90 而不是 100：全透明之后面板就只剩文字浮在照片上，
	// 那不是"透明化"，是把界面弄坏。
	if s.PanelAlpha < 0 {
		s.PanelAlpha = 0
	}
	if s.PanelAlpha > 90 {
		s.PanelAlpha = 90
	}
	return s
}

// snapshot 返回当前设置（含 HasWallpaper）。
func (u *uiStore) snapshot() UISettings {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := u.settings
	out.HasWallpaper = u.wallpaperName != ""
	return out
}

// save 落盘设置。
func (u *uiStore) save(s UISettings) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	s = sanitizeSettings(s)
	s.HasWallpaper = u.wallpaperName != ""
	s.UpdatedAt = time.Now().Format(time.RFC3339)
	u.settings = s

	if u.dir == "" {
		return nil
	}
	if err := os.MkdirAll(u.dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(u.dir, "settings.json"), raw, 0o644)
}

// setWallpaper 保存壁纸（内容已由调用方校验过类型）。
func (u *uiStore) setWallpaper(ext string, content io.Reader) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.dir == "" {
		return errors.New("没有可用的数据目录")
	}
	if err := os.MkdirAll(u.dir, 0o755); err != nil {
		return err
	}

	// 先删旧的：换扩展名时不留孤儿文件
	u.removeWallpaperLocked()

	name := "wallpaper" + ext
	path := filepath.Join(u.dir, name)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	written, err := io.Copy(f, io.LimitReader(content, wallpaperMaxBytes+1))
	if cerr := f.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if written > wallpaperMaxBytes {
		_ = os.Remove(tmp)
		return fmt.Errorf("壁纸超过 %d MB", wallpaperMaxBytes>>20)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	u.wallpaperName = name
	return nil
}

func (u *uiStore) removeWallpaper() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.removeWallpaperLocked()
}

func (u *uiStore) removeWallpaperLocked() {
	if u.wallpaperName != "" {
		_ = os.Remove(filepath.Join(u.dir, u.wallpaperName))
		u.wallpaperName = ""
	}
	// 兜底：清掉任何残留的 wallpaper.*
	if entries, err := os.ReadDir(u.dir); err == nil {
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "wallpaper.") {
				_ = os.Remove(filepath.Join(u.dir, entry.Name()))
			}
		}
	}
}

// openWallpaper 打开当前壁纸文件。
func (u *uiStore) openWallpaper() (*os.File, string, error) {
	u.mu.Lock()
	name := u.wallpaperName
	u.mu.Unlock()
	if name == "" {
		return nil, "", errors.New("还没有设置壁纸")
	}
	f, err := os.Open(filepath.Join(u.dir, name))
	if err != nil {
		return nil, "", err
	}
	return f, name, nil
}

// ---------------------------------------------------------------- 处理器

// handleChangelog 返回嵌入的 CHANGELOG.md。
//
// 嵌进二进制是为了应用内就能看更新日志，不依赖网络——NAS 常常待在内网里，
// 而"这个版本改了什么"恰恰是升级前最想确认的事。
func (s *Server) handleChangelog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":  s.cfg.Version,
		"channel":  s.cfg.Channel,
		"markdown": s.cfg.Changelog,
	})
}

func (s *Server) handleUISettingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.uiSettings.snapshot())
}

func (s *Server) handleUISettingsPut(w http.ResponseWriter, r *http.Request) {
	var body UISettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误："+err.Error())
		return
	}
	// HasWallpaper 由服务端说了算，不受客户端影响
	if err := s.uiSettings.save(body); err != nil {
		writeError(w, http.StatusInternalServerError, "保存设置失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.uiSettings.snapshot())
}

// handleWallpaperGet 返回壁纸原图。图片不大，交给浏览器缓存。
func (s *Server) handleWallpaperGet(w http.ResponseWriter, r *http.Request) {
	f, name, err := s.uiSettings.openWallpaper()
	if err != nil {
		writeError(w, http.StatusNotFound, "还没有设置壁纸")
		return
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	etag := fmt.Sprintf(`W/"%x-%d"`, fnv64(name), st.ModTime().Unix())
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	// 壁纸是用户提供的字节，明确禁止嗅探，避免浏览器把它当别的类型处理
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, st.ModTime(), f)
}

// handleWallpaperPost 设置壁纸：直接上传一张图片。
func (s *Server) handleWallpaperPost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, wallpaperMaxBytes+(1<<20))
	file, header, err := r.FormFile("image")
	if err != nil {
		writeError(w, http.StatusBadRequest, "请用 image 字段上传图片："+err.Error())
		return
	}
	defer file.Close()

	ext, err := sniffImageExt(file)
	if err != nil {
		writeError(w, http.StatusUnsupportedMediaType, err.Error())
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.uiSettings.setWallpaper(ext, file); err != nil {
		writeError(w, http.StatusInternalServerError, "保存壁纸失败："+err.Error())
		return
	}
	s.log.Info("壁纸已更新", "from", header.Filename, "ext", ext)
	s.enableWallpaper()
	writeJSON(w, http.StatusOK, s.uiSettings.snapshot())
}

func (s *Server) handleWallpaperDelete(w http.ResponseWriter, r *http.Request) {
	s.uiSettings.removeWallpaper()
	settings := s.uiSettings.snapshot()
	settings.Wallpaper = false
	if err := s.uiSettings.save(settings); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.uiSettings.snapshot())
}

// enableWallpaper 让"设好了图却没打开开关"这种状态自动纠正。
func (s *Server) enableWallpaper() {
	settings := s.uiSettings.snapshot()
	if settings.Wallpaper {
		return
	}
	settings.Wallpaper = true
	_ = s.uiSettings.save(settings)
}

// sniffImageExt 按文件头判断图片类型。
//
// 不看扩展名：用户从 NAS 上挑的图可能叫 .jpeg 而实际是 PNG，
// 也可能压根改过名。类型错了浏览器就不会渲染，白白让人以为没生效。
func sniffImageExt(r io.Reader) (string, error) {
	head := make([]byte, 16)
	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", err
	}
	head = head[:n]

	switch {
	case len(head) >= 3 && head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF:
		return ".jpg", nil
	case len(head) >= 8 && string(head[:8]) == "\x89PNG\r\n\x1a\n":
		return ".png", nil
	case len(head) >= 6 && (string(head[:6]) == "GIF87a" || string(head[:6]) == "GIF89a"):
		return ".gif", nil
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return ".webp", nil
	case len(head) >= 12 && string(head[4:12]) == "ftypavif":
		return ".avif", nil
	}
	return "", errors.New("只支持 JPEG / PNG / GIF / WebP / AVIF 图片")
}
