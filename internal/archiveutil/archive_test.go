package archiveutil

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"zipmanga/internal/comicfmt"
	"zipmanga/internal/db"
	"zipmanga/internal/mounts"
)

func TestPageImagesZIPReadsRequestedPagesOnce(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "sample.cbz")
	if err := writeTestZIP(archivePath, map[string][]byte{
		"001.jpg": []byte("page-1"),
		"002.jpg": []byte("page-2"),
		"003.jpg": []byte("page-3"),
	}); err != nil {
		t.Fatalf("writeTestZIP failed: %v", err)
	}

	provider, err := mounts.NewLocalProvider(db.Mount{
		ID:       1,
		Name:     "local",
		Type:     db.MountTypeLocal,
		RootPath: root,
	})
	if err != nil {
		t.Fatalf("NewLocalProvider failed: %v", err)
	}

	pages, err := PageImages(provider, "sample.cbz", comicfmt.FormatZIP, []int{2, 1, 1}, 1<<20)
	if err != nil {
		t.Fatalf("PageImages failed: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("len(pages)=%d want 2", len(pages))
	}
	if got := string(pages[1].Data); got != "page-2" {
		t.Fatalf("page 1 data=%q want page-2", got)
	}
	if got := string(pages[2].Data); got != "page-3" {
		t.Fatalf("page 2 data=%q want page-3", got)
	}
}

func writeTestZIP(path string, files map[string][]byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		data := files[name]
		w, err := zw.Create(name)
		if err != nil {
			_ = zw.Close()
			return err
		}
		if _, err := bytes.NewBuffer(data).WriteTo(w); err != nil {
			_ = zw.Close()
			return err
		}
	}
	return zw.Close()
}
