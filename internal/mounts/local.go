package mounts

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"zipmanga/internal/db"
)

type LocalProvider struct {
	mount db.Mount
	root  string
}

func NewLocalProvider(m db.Mount) (*LocalProvider, error) {
	root, err := canonicalizeLocalRoot(m.RootPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("local root not accessible: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("local root is not directory: %s", root)
	}
	return &LocalProvider{mount: m, root: root}, nil
}

func canonicalizeLocalRoot(raw string) (string, error) {
	root := filepath.Clean(filepath.FromSlash(strings.TrimSpace(raw)))
	if root == "" || root == "." {
		return "", fmt.Errorf("local root path is required")
	}
	if !filepath.IsAbs(root) {
		abs, err := filepath.Abs(root)
		if err != nil {
			return "", fmt.Errorf("resolve absolute path failed: %w", err)
		}
		root = abs
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil && strings.TrimSpace(resolved) != "" {
		root = filepath.Clean(resolved)
	}
	return root, nil
}

func (p *LocalProvider) ID() int64 { return p.mount.ID }

func (p *LocalProvider) Name() string { return p.mount.Name }

func (p *LocalProvider) Type() db.MountType { return p.mount.Type }

func (p *LocalProvider) ListDir(rel string) ([]FileEntry, error) {
	full, err := p.resolve(rel)
	if err != nil {
		return nil, err
	}

	dirs, err := os.ReadDir(full)
	if err != nil {
		return nil, err
	}

	entries := make([]FileEntry, 0, len(dirs))
	for _, d := range dirs {
		info, err := d.Info()
		if err != nil {
			continue
		}
		relPath := filepath.ToSlash(filepath.Join(cleanRel(rel), d.Name()))
		if relPath == "." {
			relPath = d.Name()
		}
		entries = append(entries, FileEntry{
			Path:    strings.TrimPrefix(relPath, "./"),
			Name:    d.Name(),
			IsDir:   d.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	filtered := make([]FileEntry, 0, len(entries))
	for _, entry := range entries {
		if ShouldHideEntry(entry) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered, nil
}

func (p *LocalProvider) Open(rel string) (io.ReadCloser, error) {
	full, err := p.resolve(rel)
	if err != nil {
		return nil, err
	}
	return os.Open(full)
}

func (p *LocalProvider) OpenReaderAt(rel string) (ReadAtCloser, int64, error) {
	full, err := p.resolve(rel)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}

func (p *LocalProvider) Close() error { return nil }

func (p *LocalProvider) resolve(rel string) (string, error) {
	clean := cleanRel(rel)
	if clean == "." {
		clean = ""
	}
	full := filepath.Clean(filepath.Join(p.root, filepath.FromSlash(clean)))
	rootWithSep := p.root + string(filepath.Separator)
	if full != p.root && !strings.HasPrefix(full, rootWithSep) {
		return "", fmt.Errorf("path escapes root: %s", rel)
	}
	return full, nil
}

func cleanRel(rel string) string {
	r := strings.TrimSpace(rel)
	if r == "" {
		return "."
	}
	r = strings.TrimPrefix(filepath.ToSlash(r), "/")
	r = filepath.Clean(r)
	if r == "" {
		return "."
	}
	return filepath.ToSlash(r)
}
