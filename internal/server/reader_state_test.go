package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"zipmanga/internal/db"
)

func readerFixture(t *testing.T) (*Server, db.User, []db.Comic) {
	t.Helper()
	s := openServerTestStore(t)
	u, _ := s.CreateUser("reader", "hash", false)
	m, _ := s.CreateMount(db.Mount{Name: "local", Type: db.MountTypeLocal, RootPath: "/manga", Enabled: true})
	for _, p := range []string{"series/Volume 1.zip", "series/Volume 2.zip", "series/Volume 10.zip", "other/Volume 3.zip"} {
		if err := s.UpsertComicForMount(m.ID, db.Comic{Path: p, Title: p, PageCount: 20}); err != nil {
			t.Fatal(err)
		}
	}
	books, err := s.ListComicsByMount(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{store: s}, u, books
}

func TestReaderStateHTTPValidationAndConflict(t *testing.T) {
	s, u, books := readerFixture(t)
	event := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	body := fmt.Sprintf(`{"settings":{"mode":"scroll","fit":"width","quality":"standard","enhance":"off","spreadDirection":"ltr","spread":"auto"},"last_page":8,"total_pages":20,"page_offset":0.25,"updated_at":%q}`, event)
	send := func(payload string, user bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(payload))
		r.SetPathValue("id", fmt.Sprint(books[0].ID))
		if user {
			r = withUserCtx(r, u)
		}
		w := httptest.NewRecorder()
		s.handleAPIUpdateReaderState(w, r)
		return w
	}
	if w := send(body, false); w.Code != 401 {
		t.Fatalf("auth status=%d", w.Code)
	}
	if w := send(strings.Replace(body, `"page_offset":0.25`, `"page_offset":2`, 1), true); w.Code != 400 {
		t.Fatalf("offset status=%d %s", w.Code, w.Body)
	}
	if w := send(body, true); w.Code != 200 {
		t.Fatalf("save status=%d %s", w.Code, w.Body)
	}
	if w := send(body, true); w.Code != 409 || !strings.Contains(w.Body.String(), `"last_page":8`) {
		t.Fatalf("conflict status=%d %s", w.Code, w.Body)
	}
}

func TestNeighborsNaturalOrderAndDirectoryBoundary(t *testing.T) {
	s, u, books := readerFixture(t)
	var target db.Comic
	for _, b := range books {
		if b.Path == "series/Volume 2.zip" {
			target = b
		}
	}
	r := withUserCtx(httptest.NewRequest(http.MethodGet, "/", nil), u)
	r.SetPathValue("id", fmt.Sprint(target.ID))
	w := httptest.NewRecorder()
	s.handleAPIComicNeighbors(w, r)
	var got struct {
		Previous struct {
			Path string `json:"path"`
		} `json:"previous"`
		Next struct {
			Path string `json:"path"`
		} `json:"next"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Previous.Path != "series/Volume 1.zip" || got.Next.Path != "series/Volume 10.zip" {
		t.Fatalf("neighbors=%s", w.Body)
	}
}

func TestHistoryPaginationClampsPageAndReturnsTotals(t *testing.T) {
	s, u, books := readerFixture(t)
	for _, b := range books {
		if _, err := s.store.UpsertReadingProgress(u.ID, b.ID, "", 1, 20); err != nil {
			t.Fatal(err)
		}
	}
	r := withUserCtx(httptest.NewRequest(http.MethodGet, "/api/history?page=99&page_size=3", nil), u)
	w := httptest.NewRecorder()
	if !s.handleAPIHistoryPaged(w, r) {
		t.Fatal("not handled")
	}
	var got struct {
		Page       int   `json:"page"`
		Total      int   `json:"total"`
		TotalPages int   `json:"total_pages"`
		Items      []any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Page != 2 || got.Total != 4 || got.TotalPages != 2 || len(got.Items) != 1 {
		t.Fatalf("pagination=%s", w.Body)
	}
}

func TestEmptyReaderStateHistoryAndNeighbor(t *testing.T) {
	s, u, books := readerFixture(t)
	r := withUserCtx(httptest.NewRequest(http.MethodGet, "/", nil), u)
	r.SetPathValue("id", fmt.Sprint(books[0].ID))
	w := httptest.NewRecorder()
	s.handleAPIGetReaderState(w, r)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"state":null}` {
		t.Fatalf("new state=%s", w.Body)
	}
	r = withUserCtx(httptest.NewRequest(http.MethodGet, "/api/history?page=8&page_size=24", nil), u)
	w = httptest.NewRecorder()
	s.handleAPIHistoryPaged(w, r)
	var history struct {
		Page  int   `json:"page"`
		Total int   `json:"total"`
		Pages int   `json:"total_pages"`
		Items []any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if history.Page != 1 || history.Total != 0 || history.Pages != 1 || history.Items == nil || len(history.Items) != 0 {
		t.Fatalf("empty history=%s", w.Body)
	}
	for _, book := range books {
		if book.Path == "other/Volume 3.zip" {
			r = withUserCtx(httptest.NewRequest(http.MethodGet, "/", nil), u)
			r.SetPathValue("id", fmt.Sprint(book.ID))
			w = httptest.NewRecorder()
			s.handleAPIComicNeighbors(w, r)
			if strings.TrimSpace(w.Body.String()) != `{"next":null,"previous":null}` {
				t.Fatalf("isolated neighbors=%s", w.Body)
			}
		}
	}
}

func TestReaderDefaultsPersistAllDisplayOptions(t *testing.T) {
	s, u, _ := readerFixture(t)
	r := withUserCtx(httptest.NewRequest(http.MethodPut, "/api/me/settings", strings.NewReader(`{"mode":"scroll","fit":"original","quality":"original","spread":"double","cover_offset":false,"enhance_zip":"off","enhance_pdf":"off","spread_direction":"ltr","mobile_compact":true}`)), u)
	w := httptest.NewRecorder()
	s.handleAPIUpdateMyReaderSettings(w, r)
	if w.Code != 200 {
		t.Fatalf("save %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	s.handleAPIGetMyReaderSettings(w, withUserCtx(httptest.NewRequest(http.MethodGet, "/api/me/settings", nil), u))
	var got struct {
		Settings map[string]any `json:"settings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"fit": "original", "quality": "original", "spread": "double", "cover_offset": false} {
		if got.Settings[key] != want {
			t.Errorf("%s=%v want %v", key, got.Settings[key], want)
		}
	}
}
