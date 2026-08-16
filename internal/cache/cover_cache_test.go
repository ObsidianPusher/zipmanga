package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"zipmanga/internal/db"
)

func TestEnsureObjectPreservesRequestedExtension(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "test.sqlite"), nil)
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	cacheDir := filepath.Join(t.TempDir(), "cache")
	cc, err := NewCoverCache(store, cacheDir, 32)
	if err != nil {
		t.Fatalf("NewCoverCache failed: %v", err)
	}

	path, err := cc.EnsureObject("page:test:png", func() (CacheObject, error) {
		return CacheObject{Data: []byte("pngdata"), Ext: ".png"}, nil
	})
	if err != nil {
		t.Fatalf("EnsureObject failed: %v", err)
	}
	if filepath.Ext(path) != ".png" {
		t.Fatalf("cached extension=%q want .png", filepath.Ext(path))
	}
}

func TestCoverCacheReapDoesNotDeletePageCacheEntries(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "test.sqlite"), nil)
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	cacheDir := filepath.Join(t.TempDir(), "cache")
	cc, err := NewCoverCache(store, cacheDir, 1)
	if err != nil {
		t.Fatalf("NewCoverCache failed: %v", err)
	}

	pagePath := filepath.Join(cacheDir, "pages", "aa", "page.webp")
	if err := os.MkdirAll(filepath.Dir(pagePath), 0o755); err != nil {
		t.Fatalf("MkdirAll page failed: %v", err)
	}
	if err := os.WriteFile(pagePath, []byte("page-bytes"), 0o644); err != nil {
		t.Fatalf("WriteFile page failed: %v", err)
	}
	now := time.Now().Unix()
	if err := store.UpsertCacheEntry(db.CacheEntry{
		Key:        "page:raw:test",
		RelPath:    "pages/aa/page.webp",
		Size:       int64(len("page-bytes")),
		LastAccess: now - 120,
		CreatedAt:  now - 120,
	}); err != nil {
		t.Fatalf("UpsertCacheEntry page failed: %v", err)
	}

	firstCover, err := cc.EnsureObject("cover:test:1", func() (CacheObject, error) {
		return CacheObject{Data: []byte("cover-1"), Ext: ".jpg"}, nil
	})
	if err != nil {
		t.Fatalf("EnsureObject first cover failed: %v", err)
	}

	secondCover, err := cc.EnsureObject("cover:test:2", func() (CacheObject, error) {
		return CacheObject{Data: []byte("cover-2"), Ext: ".jpg"}, nil
	})
	if err != nil {
		t.Fatalf("EnsureObject second cover failed: %v", err)
	}

	if _, err := os.Stat(secondCover); err != nil {
		t.Fatalf("second cover missing: %v", err)
	}
	if _, err := os.Stat(pagePath); err != nil {
		t.Fatalf("page cache entry should be preserved, stat err=%v", err)
	}
	if _, err := os.Stat(firstCover); !os.IsNotExist(err) {
		t.Fatalf("expected oldest cover to be reaped, got err=%v", err)
	}
}
