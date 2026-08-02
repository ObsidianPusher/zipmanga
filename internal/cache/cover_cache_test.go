package cache

import (
	"path/filepath"
	"testing"

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
