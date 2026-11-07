package mounts

import (
	"strings"
	"testing"
	"zipmanga/internal/db"
)

func TestNewProviderRejectsRemoteMounts(t *testing.T) {
	for _, kind := range []db.MountType{db.MountTypeSMB, db.MountTypeWebDAV} {
		for _, roots := range [][]string{{"/"}, {"/one", "/two"}} {
			p, err := NewProvider(db.Mount{Type: kind, RootPaths: roots})
			if err == nil && p != nil {
				p.Close()
				t.Fatalf("remote mount %s was enabled", kind)
			}
			if err == nil || !strings.Contains(err.Error(), "unsupported mount type") {
				t.Fatalf("%s must be rejected before provider initialization: %v", kind, err)
			}
		}
	}
}

func TestNewProviderStillSupportsLocalMounts(t *testing.T) {
	for _, roots := range [][]string{{t.TempDir()}, {t.TempDir(), t.TempDir()}} {
		p, err := NewProvider(db.Mount{Type: db.MountTypeLocal, RootPaths: roots})
		if err != nil {
			t.Fatal(err)
		}
		if p.Type() != db.MountTypeLocal {
			t.Fatalf("type=%s", p.Type())
		}
		p.Close()
	}
}
