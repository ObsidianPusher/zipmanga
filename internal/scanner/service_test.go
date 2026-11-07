package scanner

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"testing"
	"time"

	"zipmanga/internal/db"
	"zipmanga/internal/mounts"
)

type fakeProvider struct {
	dirs    map[string][]mounts.FileEntry
	dirErrs map[string]error
}

func (f fakeProvider) ID() int64 { return 1 }

func (f fakeProvider) Name() string { return "fake" }

func (f fakeProvider) Type() db.MountType { return db.MountTypeLocal }

func (f fakeProvider) ListDir(rel string) ([]mounts.FileEntry, error) {
	if err, ok := f.dirErrs[rel]; ok {
		return nil, err
	}
	return f.dirs[rel], nil
}

func (f fakeProvider) Open(rel string) (io.ReadCloser, error) { return nil, io.EOF }

func (f fakeProvider) OpenReaderAt(rel string) (mounts.ReadAtCloser, int64, error) {
	return nil, 0, mounts.ErrReaderAtUnsupported
}

func (f fakeProvider) Close() error { return nil }

func TestIsComicName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{name: "a.zip", want: true},
		{name: "a.cbz", want: true},
		{name: "a.rar", want: false},
		{name: "a.cbr", want: false},
		{name: "a.7z", want: false},
		{name: "a.cb7", want: false},
		{name: "a.pdf", want: false},
		{name: "a.PDF", want: false},
		{name: "a.jpg", want: false},
	}
	for _, tc := range cases {
		if got := isComicName(tc.name); got != tc.want {
			t.Fatalf("isComicName(%q)=%v want=%v", tc.name, got, tc.want)
		}
	}
}

func TestNormalizedEntryPathFallback(t *testing.T) {
	e := mounts.FileEntry{
		Path:  "broken/path/noext",
		Name:  "sample.cbz",
		IsDir: false,
	}
	got := normalizedEntryPath("books", e)
	if got != "books/sample.cbz" {
		t.Fatalf("normalizedEntryPath fallback mismatch: %q", got)
	}
}

func TestListComicFilesRecursive(t *testing.T) {
	now := time.Now()
	p := fakeProvider{
		dirs: map[string][]mounts.FileEntry{
			"": {
				{Path: "series", Name: "series", IsDir: true, ModTime: now},
				{Path: "root.pdf", Name: "root.pdf", IsDir: false, ModTime: now},
			},
			"series": {
				{Path: "series/v1.zip", Name: "v1.zip", IsDir: false, ModTime: now},
				{Path: "series/misc", Name: "misc", IsDir: true, ModTime: now},
			},
			"series/misc": {
				{Path: "", Name: "deep.PDF", IsDir: false, ModTime: now},
			},
		},
	}
	files, err := listComicFiles(context.Background(), p, nil)
	if err != nil {
		t.Fatalf("listComicFiles err: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("listComicFiles count=%d want=1", len(files))
	}
}

func TestListComicFilesSkipSubDirNotExist(t *testing.T) {
	now := time.Now()
	p := fakeProvider{
		dirs: map[string][]mounts.FileEntry{
			"": {
				{Path: "2018 合集", Name: "2018 合集", IsDir: true, ModTime: now},
				{Path: "ok.zip", Name: "ok.zip", IsDir: false, ModTime: now},
			},
		},
		dirErrs: map[string]error{
			"2018 合集": os.ErrNotExist,
		},
	}
	files, err := listComicFiles(context.Background(), p, nil)
	if err != nil {
		t.Fatalf("listComicFiles err: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("listComicFiles count=%d want=1", len(files))
	}
	if got := files[0].Path; got != "ok.zip" {
		t.Fatalf("listComicFiles path=%q want=ok.zip", got)
	}
}

func TestListComicFilesRootListError(t *testing.T) {
	p := fakeProvider{
		dirs: map[string][]mounts.FileEntry{},
		dirErrs: map[string]error{
			"": fs.ErrNotExist,
		},
	}
	_, err := listComicFiles(context.Background(), p, nil)
	if err == nil {
		t.Fatal("listComicFiles expected error, got nil")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("listComicFiles err=%v, expected not-exist", err)
	}
}

func TestListComicFilesPreserveDirTrailingSpace(t *testing.T) {
	now := time.Now()
	p := fakeProvider{
		dirs: map[string][]mounts.FileEntry{
			"": {
				{Path: "2018 合集 ", Name: "2018 合集 ", IsDir: true, ModTime: now},
			},
			"2018 合集 ": {
				{Path: "2018 合集 /第1话.cbz", Name: "第1话.cbz", IsDir: false, ModTime: now},
			},
		},
	}
	files, err := listComicFiles(context.Background(), p, nil)
	if err != nil {
		t.Fatalf("listComicFiles err: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("listComicFiles count=%d want=1", len(files))
	}
	if got := files[0].Path; got != "2018 合集 /第1话.cbz" {
		t.Fatalf("listComicFiles path=%q want=%q", got, "2018 合集 /第1话.cbz")
	}
}
