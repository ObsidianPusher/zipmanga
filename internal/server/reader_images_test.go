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
	"zipmanga/internal/imgcodec"
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

func TestReaderDisplayCacheSurvivesRawCacheEviction(t *testing.T) {
	store := openServerTestStore(t)
	dir := t.TempDir()
	raw, err := imgcodec.EncodeImageLossyWebP(image.NewNRGBA(image.Rect(0, 0, 32, 48)))
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	entry, err := zw.Create("001.webp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dir, "book.cbz")
	if err := os.WriteFile(archivePath, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	mt, err := store.CreateMount(db.Mount{Name: "local", Type: db.MountTypeLocal, RootPath: dir, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertComicForMount(mt.ID, db.Comic{Path: "book.cbz", Title: "WebP", PageCount: 2}); err != nil {
		t.Fatal(err)
	}
	books, err := store.ListComicsByMount(mt.ID)
	if err != nil || len(books) != 1 {
		t.Fatalf("books=%v err=%v", books, err)
	}
	pc, err := cache.NewPageCache(store, t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: store, mountManager: mounts.NewManager(store), pageCache: pc, pageLoadQueues: make(map[string]*comicPageLoadQueue), pageLoadSem: make(chan struct{}, 2)}
	defer srv.mountManager.Close()
	request := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/?"+query, nil)
		req.SetPathValue("id", strconv.FormatInt(books[0].ID, 10))
		req.SetPathValue("index", "0")
		rec := httptest.NewRecorder()
		srv.handleAPIComicPage(rec, req)
		return rec
	}
	query := "quality=economy&enhance=level1&w=640"
	first := request(query)
	if first.Code != 200 || !bytes.Equal(first.Body.Bytes(), raw) {
		t.Fatalf("first request: status=%d body=%s", first.Code, first.Body.String())
	}
	rawPath, ok, err := pc.GetPathIfExists(rawComicPageCacheKey(books[0], comicfmt.FormatZIP, 0))
	if err != nil || !ok {
		t.Fatalf("raw cache missing: %v", err)
	}
	if err := os.Remove(rawPath); err != nil {
		t.Fatal(err)
	}
	// A cached display must not reopen the ZIP even when its raw cache was evicted.
	if err := os.Remove(archivePath); err != nil {
		t.Fatal(err)
	}
	cached := request(query)
	if cached.Code != 200 || !bytes.Equal(cached.Body.Bytes(), raw) || !strings.Contains(cached.Header().Get("Server-Timing"), `cache;desc="true"`) {
		t.Fatalf("display cache not reused: status=%d timing=%s body=%s", cached.Code, cached.Header().Get("Server-Timing"), cached.Body.String())
	}
	for _, query := range []string{"quality=original", "quality=economy&enhance=level1&w=960", "quality=economy&enhance=off&w=640", "quality=standard&enhance=level1&w=640"} {
		if rec := request(query); rec.Code != 502 {
			t.Errorf("different display variant must not reuse cached image: query=%s status=%d", query, rec.Code)
		}
	}
}

func TestReaderWebPQuality(t *testing.T) {
	store := openServerTestStore(t)
	dir := t.TempDir()
	img := image.NewNRGBA(image.Rect(0, 0, 1280, 1920))
	for y := 0; y < 1920; y++ {
		for x := 0; x < 1280; x++ {
			v := uint8(245)
			if x%17 == 0 || y%19 == 0 {
				v = 30
			}
			img.SetNRGBA(x, y, color.NRGBA{R: v, G: v, B: v, A: 255})
		}
	}
	raw, err := imgcodec.EncodeImageLossyWebP(img)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dir, "book.cbz")
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	entry, err := zw.Create("001.webp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	mt, err := store.CreateMount(db.Mount{Name: "webp", Type: db.MountTypeLocal, RootPath: dir, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertComicForMount(mt.ID, db.Comic{Path: "book.cbz", Title: "WebP", PageCount: 2}); err != nil {
		t.Fatal(err)
	}
	books, err := store.ListComicsByMount(mt.ID)
	if err != nil || len(books) != 1 {
		t.Fatalf("books=%v err=%v", books, err)
	}
	pc, err := cache.NewPageCache(store, t.TempDir(), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: store, mountManager: mounts.NewManager(store), pageCache: pc, pageLoadQueues: make(map[string]*comicPageLoadQueue), pageLoadSem: make(chan struct{}, 2)}
	defer srv.mountManager.Close()
	request := func(quality string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/?quality="+quality+"&enhance=off&w=640", nil)
		req.SetPathValue("id", strconv.FormatInt(books[0].ID, 10))
		req.SetPathValue("index", "0")
		rec := httptest.NewRecorder()
		srv.handleAPIComicPage(rec, req)
		return rec
	}
	t.Run("standard preserves original WebP", func(t *testing.T) {
		rec := request("standard")
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/webp" || !bytes.Equal(rec.Body.Bytes(), raw) {
			t.Fatalf("standard must preserve WebP bytes: status=%d type=%s bytes=%d want=%d", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len(), len(raw))
		}
	})
	t.Run("economy serves a cached smaller WebP", func(t *testing.T) {
		rec := request("economy")
		cfg, format, err := image.DecodeConfig(bytes.NewReader(rec.Body.Bytes()))
		if rec.Code != 200 || err != nil || format != "webp" || rec.Header().Get("Content-Type") != "image/webp" || cfg.Width != 640 || cfg.Height != 960 {
			t.Fatalf("economy must resize as WebP: status=%d type=%s format=%s size=%dx%d err=%v", rec.Code, rec.Header().Get("Content-Type"), format, cfg.Width, cfg.Height, err)
		}
		if rec.Body.Len() >= len(raw) {
			t.Fatalf("economy grew: got=%d source=%d", rec.Body.Len(), len(raw))
		}
		if err := os.Remove(archivePath); err != nil {
			t.Fatal(err)
		}
		cached := request("economy")
		if cached.Code != 200 || !bytes.Equal(cached.Body.Bytes(), rec.Body.Bytes()) || !strings.Contains(cached.Header().Get("Server-Timing"), `cache;desc="true"`) {
			t.Fatalf("economy cache not reused: status=%d timing=%s", cached.Code, cached.Header().Get("Server-Timing"))
		}
		t.Logf("source=%d bytes; economy=%d bytes", len(raw), rec.Body.Len())
	})
}
