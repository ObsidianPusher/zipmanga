package cache

import (
	"os"
	"path/filepath"
	"testing"

	"zipmanga/internal/db"
)

func TestPageCacheReapsOldestBySize(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "test.sqlite"), nil)
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	cacheDir := filepath.Join(t.TempDir(), "cache")
	pc, err := NewPageCache(store, cacheDir, 20)
	if err != nil {
		t.Fatalf("NewPageCache failed: %v", err)
	}

	oldPath, err := pc.EnsureObject("page:raw:test:old", func() (CacheObject, error) {
		return CacheObject{Data: []byte("0123456789abcdef"), Ext: ".jpg"}, nil
	})
	if err != nil {
		t.Fatalf("EnsureObject old failed: %v", err)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("old cache file missing: %v", err)
	}

	newPath, err := pc.EnsureObject("page:raw:test:new", func() (CacheObject, error) {
		return CacheObject{Data: []byte("fedcba9876543210"), Ext: ".jpg"}, nil
	})
	if err != nil {
		t.Fatalf("EnsureObject new failed: %v", err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("new cache file missing: %v", err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("expected old cache file to be reaped, got err=%v", err)
	}
}
