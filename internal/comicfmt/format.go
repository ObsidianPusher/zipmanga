package comicfmt

import (
	"path"
	"path/filepath"
	"strings"
)

type Format string

const (
	FormatUnknown Format = "unknown"
	FormatZIP     Format = "zip"
	FormatRAR     Format = "rar"
	Format7Z      Format = "7z"
	FormatPDF     Format = "pdf"
)

func Detect(path string) Format {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(path)))
	switch ext {
	case ".zip", ".cbz":
		return FormatZIP
	case ".pdf":
		return FormatPDF
	default:
		return FormatUnknown
	}
}

func IsArchive(format Format) bool {
	switch format {
	case FormatZIP:
		return true
	default:
		return false
	}
}

func IsSupportedPath(path string) bool {
	switch Detect(path) {
	case FormatZIP:
		return true
	default:
		return false
	}
}

func FileNameKey(p string) string {
	base := path.Base(strings.ReplaceAll(p, "\\", "/"))
	switch base {
	case "", ".", "/":
		return ""
	}
	return strings.ToLower(base)
}
