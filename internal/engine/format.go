// Package engine wraps the 7-Zip command line tool that powers YC-7ZIP.
//
// The wrapper deliberately keeps a single dependency: an official 7-Zip
// executable (7zz / 7za / 7z.exe). 7-Zip already reads and writes every format
// YC-7ZIP advertises, including RAR extraction, so no third party archive
// library is bundled and no archive data ever leaves the host.
package engine

import "strings"

// Capability describes what can be done with a container format.
type Capability int

const (
	// ExtractOnly formats can be opened but never produced (RAR is the
	// canonical example: the RAR compressor is proprietary).
	ExtractOnly Capability = iota
	// CreateAndExtract formats can be produced and opened.
	CreateAndExtract
)

// Format is one entry of the format registry shown in the UI.
type Format struct {
	// ID is the 7-Zip type switch value, e.g. "7z", "zip", "tar".
	ID string `json:"id"`
	// Label is the human readable name.
	Label string `json:"label"`
	// Extension is the file name suffix, including the leading dot.
	Extension string `json:"extension"`
	// Capability tells the UI which direction is legal.
	Capability Capability `json:"capability"`
	// SupportsPassword marks formats with a real encryption implementation.
	SupportsPassword bool `json:"supports_password"`
	// SupportsVolume marks formats that can be split into fixed volumes.
	SupportsVolume bool `json:"supports_volume"`
	// SupportsLevel marks formats where -mx is meaningful.
	SupportsLevel bool `json:"supports_level"`
	// MIME is used for download responses.
	MIME string `json:"-"`
	// Note is an optional hint rendered under the format tile.
	Note string `json:"note,omitempty"`
}

// Formats is the registry the API exposes. Ordering is intentional: the most
// common choices for a NAS user come first.
var Formats = []Format{
	{
		ID: "7z", Label: "7-Zip", Extension: ".7z",
		Capability:       CreateAndExtract,
		SupportsPassword: true, SupportsVolume: true, SupportsLevel: true,
		MIME: "application/x-7z-compressed",
		Note: "压缩率最高，支持 AES-256 加密与文件名加密",
	},
	{
		ID: "zip", Label: "ZIP", Extension: ".zip",
		Capability:       CreateAndExtract,
		SupportsPassword: true, SupportsVolume: true, SupportsLevel: true,
		MIME: "application/zip",
		Note: "Windows / macOS 原生支持，兼容性最好",
	},
	{
		ID: "tar", Label: "TAR", Extension: ".tar",
		Capability:       CreateAndExtract,
		SupportsPassword: false, SupportsVolume: false, SupportsLevel: false,
		MIME: "application/x-tar",
		Note: "仅打包不压缩，保留 Unix 权限位",
	},
	{
		ID: "gzip", Label: "GZIP", Extension: ".gz",
		Capability:       CreateAndExtract,
		SupportsPassword: false, SupportsVolume: false, SupportsLevel: true,
		MIME: "application/gzip",
		Note: "单文件压缩，常用于 .tar.gz",
	},
	{
		ID: "bzip2", Label: "BZIP2", Extension: ".bz2",
		Capability:       CreateAndExtract,
		SupportsPassword: false, SupportsVolume: false, SupportsLevel: true,
		MIME: "application/x-bzip2",
		Note: "老牌高压缩率算法，速度较慢",
	},
	{
		ID: "xz", Label: "XZ", Extension: ".xz",
		Capability:       CreateAndExtract,
		SupportsPassword: false, SupportsVolume: false, SupportsLevel: true,
		MIME: "application/x-xz",
		Note: "Linux 发行版常用，压缩率高",
	},
	{
		ID: "zstd", Label: "Zstandard", Extension: ".zst",
		Capability:       CreateAndExtract,
		SupportsPassword: false, SupportsVolume: false, SupportsLevel: true,
		MIME: "application/zstd",
		Note: "现代算法，速度与压缩率兼顾",
	},
	{
		ID: "rar", Label: "RAR", Extension: ".rar",
		Capability:       ExtractOnly,
		SupportsPassword: false, SupportsVolume: false, SupportsLevel: false,
		MIME: "application/vnd.rar",
		Note: "仅可解压：RAR 压缩算法为私有授权，7-Zip 无法生成",
	},
	{
		ID: "iso", Label: "ISO", Extension: ".iso",
		Capability: ExtractOnly,
		MIME:       "application/x-iso9660-image",
		Note:       "光盘镜像，仅可解压",
	},
	{
		ID: "cab", Label: "CAB", Extension: ".cab",
		Capability: ExtractOnly,
		MIME:       "application/vnd.ms-cab-compressed",
		Note:       "Windows 安装包容器，仅可解压",
	},
	{
		ID: "wim", Label: "WIM", Extension: ".wim",
		Capability: ExtractOnly,
		MIME:       "application/x-ms-wim",
		Note:       "Windows 映像，仅可解压",
	},
	{
		ID: "cpio", Label: "CPIO", Extension: ".cpio",
		Capability: ExtractOnly,
		MIME:       "application/x-cpio",
		Note:       "initramfs 容器，仅可解压",
	},
}

// FormatByID returns the registered format, or false when unknown.
func FormatByID(id string) (Format, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, f := range Formats {
		if f.ID == id {
			return f, true
		}
	}
	return Format{}, false
}

// ExtensionToFormat maps a file suffix onto a registry entry. It handles the
// compound suffixes that users actually meet, e.g. ".tar.gz" must resolve to
// the GZIP decoder even though the logical container is a tar stream.
func ExtensionToFormat(name string) (Format, bool) {
	lower := strings.ToLower(name)
	for _, f := range Formats {
		if strings.HasSuffix(lower, f.Extension) {
			return f, true
		}
	}
	// Long tail of readable-only containers we accept but do not advertise.
	extra := map[string]string{
		".tgz": "gzip", ".tbz": "bzip2", ".tbz2": "bzip2", ".txz": "xz",
		".tzst": "zstd", ".lz": "lzma", ".lzma": "lzma", ".z": "compress",
		".rar5": "rar", ".arj": "arj", ".lzh": "lzh", ".chm": "chm",
		".msi": "msi", ".deb": "deb", ".rpm": "rpm", ".dmg": "dmg",
		".img": "img", ".vhd": "vhd", ".squashfs": "squashfs", ".apk": "apk",
	}
	for suffix, id := range extra {
		if strings.HasSuffix(lower, suffix) {
			return Format{ID: id, Label: strings.ToUpper(strings.TrimPrefix(suffix, ".")),
				Extension: suffix, Capability: ExtractOnly, MIME: "application/octet-stream",
				Note: "仅可解压"}, true
		}
	}
	return Format{}, false
}
