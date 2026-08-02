package auth

import (
	"testing"
	"time"

	"zipmanga/internal/db"
)

func TestAuthByTokenWithRefreshExtendsSessionToFullTTL(t *testing.T) {
	store := openTestStore(t)
	svc := NewService(store, 2*time.Hour)

	user, err := store.CreateUser("tester", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	token := "token-refresh"
	originalExpires := time.Now().Add(20 * time.Minute)
	if err := store.CreateSession(token, user.ID, originalExpires); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	_, renewedExpires, renewed, err := svc.AuthByTokenWithRefresh(token)
	if err != nil {
		t.Fatalf("AuthByTokenWithRefresh failed: %v", err)
	}
	if !renewed {
		t.Fatal("expected session to be renewed")
	}
	if !renewedExpires.After(originalExpires.Add(90 * time.Minute)) {
		t.Fatalf("renewed expires_at=%v was not extended to full ttl beyond original=%v", renewedExpires, originalExpires)
	}

	session, err := store.GetSessionWithUser(token)
	if err != nil {
		t.Fatalf("GetSessionWithUser failed: %v", err)
	}
	if !session.ExpiresAt.Equal(renewedExpires) {
		t.Fatalf("stored expires_at=%v want=%v", session.ExpiresAt, renewedExpires)
	}
}

func openTestStore(t *testing.T) *db.Store {
	t.Helper()

	store, err := db.Open(t.TempDir()+"/test.sqlite", nil)
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}
