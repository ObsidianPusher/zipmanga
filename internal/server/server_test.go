package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"zipmanga/internal/auth"
	"zipmanga/internal/db"
)

func TestNormalizeEnhanceModeCanonicalizesLegacyAliases(t *testing.T) {
	cases := map[string]string{
		"":           "level1",
		"off":        "off",
		"level1":     "level1",
		"balanced":   "level1",
		"clarity":    "level1",
		"level2":     "level2",
		"quality":    "level2",
		"ai-2x":      "level2",
		"unexpected": "level1",
	}
	for input, want := range cases {
		if got := normalizeEnhanceMode(input); got != want {
			t.Fatalf("normalizeEnhanceMode(%q)=%q want %q", input, got, want)
		}
	}
}

func TestDefaultReaderSettingsUseLevel1Enhance(t *testing.T) {
	got := defaultReaderSettings(7)
	if got.EnhanceZip != "level1" {
		t.Fatalf("EnhanceZip=%q want level1", got.EnhanceZip)
	}
	if got.EnhancePDF != "level1" {
		t.Fatalf("EnhancePDF=%q want level1", got.EnhancePDF)
	}
}

func TestAuthRequiredRefreshesSessionCookieWhenSessionExtended(t *testing.T) {
	store := openServerTestStore(t)
	authSvc := auth.NewService(store, 2*time.Hour)

	user, err := store.CreateUser("tester", "hash", false)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	token := "cookie-refresh"
	originalExpires := time.Now().Add(15 * time.Minute)
	if err := store.CreateSession(token, user.ID, originalExpires); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	srv := &Server{
		auth:       authSvc,
		sessionTTL: 2 * time.Hour,
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token, Path: "/"})

	handlerCalled := false
	srv.authRequired(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusNoContent)
	})(rr, req)

	if !handlerCalled {
		t.Fatal("expected wrapped handler to be called")
	}
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d want=%d", rr.Code, http.StatusNoContent)
	}

	resp := rr.Result()
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected refreshed session cookie")
	}

	var sessionCookie *http.Cookie
	for _, cookie := range cookies {
		if cookie.Name == sessionCookieName {
			sessionCookie = cookie
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected zm_session cookie to be refreshed")
	}
	if !sessionCookie.Expires.After(originalExpires.Add(90 * time.Minute)) {
		t.Fatalf("cookie expires=%v was not extended from original=%v", sessionCookie.Expires, originalExpires)
	}
}

func openServerTestStore(t *testing.T) *db.Store {
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
