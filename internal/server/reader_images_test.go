package server

import (
	"archive/zip"
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"zipmanga/internal/cache"
	"zipmanga/internal/comicfmt"
	"zipmanga/internal/db"
	"zipmanga/internal/mounts"
)

func TestReaderOriginalPreservesArchiveBytes(t *testing.T) {
	dir := t.TempDir()
	var source bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{R: 123, A: 255})
	if err := png.Encode(&source, img); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	entry, _ := zw.Create("001.png")
	_, _ = entry.Write(source.Bytes())
	_ = zw.Close()
	if err := os.WriteFile(filepath.Join(dir, "book.cbz"), archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	provider, err := mounts.NewLocalProvider(db.Mount{ID: 1, RootPath: dir})
	if err != nil {
		t.Fatal(err)
	}
	pc, err := cache.NewPageCache(openServerTestStore(t), t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{pageCache: pc}
	path, err := srv.ensureOriginalComicPageCache(db.Comic{ID: 1, Path: "book.cbz"}, provider, comicfmt.FormatZIP, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, source.Bytes()) {
		t.Fatal("original image was transcoded")
	}
	_, err = srv.ensureOriginalComicPageCache(db.Comic{ID: 1, Path: "book.cbz"}, provider, comicfmt.FormatZIP, 1)
	if code, _ := readerPageError(err); code != "page_out_of_range" {
		t.Fatalf("code=%s err=%v", code, err)
	}
}

func TestReaderErrorsDoNotInferEOF(t *testing.T) {
	for _, err := range []error{errors.New("connection reset"), errors.New("invalid image"), os.ErrNotExist} {
		code, status := readerPageError(err)
		if code == "page_out_of_range" || status == 404 {
			t.Fatalf("transient error classified as EOF: %v", err)
		}
	}
}

func TestReaderWidthAndPrefetchBounded(t *testing.T) {
	if readerDisplayWidth(7000, "economy") != 1280 {
		t.Fatal("economy must cap transfer width")
	}
	if readerDisplayWidth(1001, "standard") != 1280 {
		t.Fatal("nearby widths should share bucket")
	}
	got := readerPrefetchIndexes(3, 10, 2, "backward")
	if len(got) != 2 || got[0] != 2 || got[1] != 1 {
		t.Fatalf("wrong backward indexes: %v", got)
	}
	got = readerPrefetchIndexes(0, 0, 1000, "backward")
	if len(got) != 0 {
		t.Fatal(got)
	}
}

func TestReaderPageHTTPQualityCacheAndErrors(t *testing.T) {
	store := openServerTestStore(t)
	dir := t.TempDir()
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 1800, 100))); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	entry, _ := zw.Create("001.png")
	_, _ = entry.Write(imageData.Bytes())
	_ = zw.Close()
	if err := os.WriteFile(filepath.Join(dir, "book.cbz"), archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	mt, err := store.CreateMount(db.Mount{Name: "reader", Type: db.MountTypeLocal, RootPath: dir, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertComicForMount(mt.ID, db.Comic{Path: "book.cbz", Title: "Book", PageCount: 2}); err != nil {
		t.Fatal(err)
	}
	comics, err := store.ListComicsByMount(mt.ID)
	if err != nil || len(comics) != 1 {
		t.Fatalf("comics=%v err=%v", comics, err)
	}
	pc, err := cache.NewPageCache(store, t.TempDir(), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: store, mountManager: mounts.NewManager(store), pageCache: pc, pageLoadQueues: make(map[string]*comicPageLoadQueue), pageLoadSem: make(chan struct{}, 2)}
	request := func(query, index string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/?"+query, nil)
		req.SetPathValue("id", strconv.FormatInt(comics[0].ID, 10))
		req.SetPathValue("index", index)
		rec := httptest.NewRecorder()
		srv.handleAPIComicPage(rec, req)
		return rec
	}
	first := request("quality=economy&enhance=off&w=7000", "0")
	if first.Code != 200 {
		t.Fatalf("status=%d body=%s", first.Code, first.Body.String())
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(first.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 1280 {
		t.Fatalf("economy width=%d", cfg.Width)
	}
	second := request("quality=economy&enhance=off&w=7000", "0")
	if second.Code != 200 || !strings.Contains(second.Header().Get("Server-Timing"), `cache;desc="true"`) {
		t.Fatalf("display cache miss: %s", second.Header().Get("Server-Timing"))
	}
	original := request("quality=original&enhance=level2&w=640", "0")
	if original.Code != 200 || !bytes.Equal(original.Body.Bytes(), imageData.Bytes()) {
		t.Fatalf("original image changed status=%d", original.Code)
	}
	eof := request("quality=original", "1")
	if eof.Code != 404 || !strings.Contains(eof.Body.String(), "page_out_of_range") {
		t.Fatalf("EOF: %d %s", eof.Code, eof.Body.String())
	}
	if err := os.Remove(filepath.Join(dir, "book.cbz")); err != nil {
		t.Fatal(err)
	}
	unavailable := request("quality=original", "2")
	if unavailable.Code != 502 || strings.Contains(unavailable.Body.String(), "page_out_of_range") {
		t.Fatalf("missing source inferred EOF: %d %s", unavailable.Code, unavailable.Body.String())
	}
	cached := request("quality=original", "0")
	if cached.Code != 200 || !bytes.Equal(cached.Body.Bytes(), imageData.Bytes()) {
		t.Fatal("original cache not reused")
	}
}
