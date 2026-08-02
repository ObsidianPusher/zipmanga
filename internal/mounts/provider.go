package mounts

import (
	"errors"
	"io"
	"path"
	"strings"
	"time"

	"zipmanga/internal/db"
)

var ErrReaderAtUnsupported = errors.New("readerAt is unsupported for this mount")

type ReadAtCloser interface {
	io.ReaderAt
	io.Closer
}

type FileEntry struct {
	Path    string
	Name    string
	IsDir   bool
	Size    int64
	ModTime time.Time
}

type Provider interface {
	ID() int64
	Name() string
	Type() db.MountType
	ListDir(rel string) ([]FileEntry, error)
	Open(rel string) (io.ReadCloser, error)
	OpenReaderAt(rel string) (ReadAtCloser, int64, error)
	Close() error
}

func ShouldHideEntry(entry FileEntry) bool {
	base := strings.TrimSpace(entry.Name)
	if base == "" {
		base = strings.TrimSpace(path.Base(strings.ReplaceAll(entry.Path, "\\", "/")))
	}
	if base == "" || base == "." || base == ".." {
		return false
	}
	if strings.HasPrefix(base, ".") {
		return true
	}
	switch strings.ToLower(base) {
	case "@eadir", ".recycle", "$recycle.bin":
		return true
	default:
		return false
	}
}
