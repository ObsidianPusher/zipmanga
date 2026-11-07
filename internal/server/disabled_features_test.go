package server

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"zipmanga/internal/db"
	"zipmanga/internal/mounts"
)

func TestRemoteMountRequestsRejected(t *testing.T) {
	for _, kind := range []string{"smb", "webdav"} {
		body := fmt.Sprintf(`{"name":"remote","type":%q,"root_path":"/","smb_host":"localhost","smb_share":"books","webdav_url":"http://localhost/books"}`, kind)
		if _, err := decodeMountRequest(httptest.NewRequest(http.MethodPost, "/api/mounts", strings.NewReader(body))); err == nil {
			t.Errorf("creation accepted disabled mount %s", kind)
		}
		if _, _, err := decodeMountBrowseRequest(httptest.NewRequest(http.MethodPost, "/api/mounts/browse", strings.NewReader(strings.ReplaceAll(strings.ReplaceAll(body, `"name":"remote",`, ""), `"root_path":"/",`, "")))); err == nil {
			t.Errorf("browsing accepted disabled mount %s", kind)
		}
	}
}

func TestPDFReadingEndpointsRejectExistingRecords(t *testing.T) {
	store := openServerTestStore(t)
	mt, err := store.CreateMount(db.Mount{Name: "local", Type: db.MountTypeLocal, RootPath: t.TempDir(), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertComicForMount(mt.ID, db.Comic{Path: "book.pdf", Title: "PDF", PageCount: 2}); err != nil {
		t.Fatal(err)
	}
	books, err := store.ListComicsByMount(mt.ID)
	if err != nil || len(books) != 1 {
		t.Fatalf("books=%v err=%v", books, err)
	}
	srv := &Server{store: store, mountManager: mounts.NewManager(store)}
	defer srv.mountManager.Close()
	for name, handler := range map[string]http.HandlerFunc{
		"detail": srv.handleAPIComicDetail,
		"page":   srv.handleAPIComicPage,
		"file":   srv.handleAPIComicFile,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.SetPathValue("id", strconv.FormatInt(books[0].ID, 10))
			req.SetPathValue("index", "0")
			req = withUserCtx(req, db.User{ID: 1})
			rec := httptest.NewRecorder()
			handler(rec, req)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unsupported comic format") {
				t.Fatalf("PDF must be rejected before reading: status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAppOnlyOffersLocalMountsWithoutPDFJS(t *testing.T) {
	_, tpl, _, _, err := loadWebUI("")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := tpl.Execute(&output, map[string]any{"Title": "Settings", "BaseHref": "/", "IsAdmin": true, "View": "settings"}); err != nil {
		t.Fatal(err)
	}
	html := output.String()
	selectHTML := regexp.MustCompile(`(?s)<select[^>]*id="mountType"[^>]*>(.*?)</select>`).FindStringSubmatch(html)
	if len(selectHTML) != 2 {
		t.Fatal("mount type selector missing")
	}
	options := regexp.MustCompile(`<option\b[^>]*value="([^"]+)"`).FindAllStringSubmatch(selectHTML[1], -1)
	if len(options) != 1 || options[0][1] != "local" {
		t.Errorf("mount options=%v", options)
	}
	if regexp.MustCompile(`<script\b[^>]*src="[^"]*pdfjs/`).MatchString(html) {
		t.Fatal("PDF.js is still loaded")
	}
}
