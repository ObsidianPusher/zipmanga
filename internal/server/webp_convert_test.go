package server

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"zipmanga/internal/imgcodec"
)

type webpTestFile struct {
	name string
	data []byte
}

func writeWebPTestZIP(t *testing.T, filename string, files ...webpTestFile) {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, file := range files {
		entry, err := w.Create(file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(file.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}

func webpTestPNG(t *testing.T, width int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, 3))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Build a stored RAR4 fixture with real header and file checksums. This keeps
// compatibility tests independent of external rar executables and downloads.
func writeWebPTestRAR(t *testing.T, filename string, files ...webpTestFile) {
	t.Helper()
	var archive bytes.Buffer
	archive.WriteString("Rar!\x1a\x07\x00")
	writeBlock := func(kind byte, flags uint16, payload []byte) {
		header := make([]byte, 7+len(payload))
		header[2] = kind
		binary.LittleEndian.PutUint16(header[3:], flags)
		binary.LittleEndian.PutUint16(header[5:], uint16(len(header)))
		copy(header[7:], payload)
		binary.LittleEndian.PutUint16(header, uint16(crc32.ChecksumIEEE(header[2:])))
		archive.Write(header)
	}
	writeBlock(0x73, 0, make([]byte, 6))
	for _, file := range files {
		header := make([]byte, 25+len(file.name))
		binary.LittleEndian.PutUint32(header, uint32(len(file.data)))
		binary.LittleEndian.PutUint32(header[4:], uint32(len(file.data)))
		header[8] = 3 // Unix host OS
		binary.LittleEndian.PutUint32(header[9:], crc32.ChecksumIEEE(file.data))
		header[17], header[18] = 20, 0x30 // RAR 2.0, stored
		binary.LittleEndian.PutUint16(header[19:], uint16(len(file.name)))
		binary.LittleEndian.PutUint32(header[21:], 0600)
		copy(header[25:], file.name)
		writeBlock(0x74, 0x8000, header)
		archive.Write(file.data)
	}
	writeBlock(0x7b, 0, nil)
	if err := os.WriteFile(filename, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestWebPRARConversionCompatibility(t *testing.T) {
	dir := t.TempDir()
	source, output := filepath.Join(dir, "book.cbr"), filepath.Join(dir, "out.zip")
	original := []byte("opaque AVIF image data")
	writeWebPTestRAR(t, source,
		webpTestFile{"__MACOSX/._001.png", []byte("AppleDouble metadata")},
		webpTestFile{"001.png", webpTestPNG(t, 4)},
		webpTestFile{"._002.avif", []byte("AppleDouble metadata")},
		webpTestFile{"002.avif", original},
	)
	entries, err := scanWebPArchiveEntries(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	stats := archiveWorkflowStats{}
	if err := writeWebPArchiveOutput(source, output, entries, imgcodec.LossyWebPEncodeOptions{}, 2, nil, nil, &stats); err != nil {
		t.Fatal(err)
	}
	r, err := zip.OpenReader(output)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if len(r.File) != 2 || r.File[0].Name != "001.webp" || r.File[1].Name != "002.avif" || stats.Extracted != 1 || stats.Kept != 1 {
		t.Fatalf("incorrect pages or stats: files=%v stats=%+v", r.File, stats)
	}
	f, err := r.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := image.Decode(f)
	_ = f.Close()
	if err != nil || img.Bounds().Dx() != 4 {
		t.Fatalf("invalid converted page: %v", err)
	}
	f, err = r.File[1].Open()
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("AVIF page modified: %v", err)
	}
}

func TestWebPConversionIgnoresMacMetadataWithoutShiftingPages(t *testing.T) {
	dir := t.TempDir()
	source, output := filepath.Join(dir, "book.cbz"), filepath.Join(dir, "out.zip")
	writeWebPTestZIP(t, source,
		webpTestFile{"__MACOSX/book/._001.png", []byte("AppleDouble metadata")},
		webpTestFile{"book/001.png", webpTestPNG(t, 4)},
		webpTestFile{"book/._002.png", []byte("AppleDouble metadata")},
		webpTestFile{"book/002.png", webpTestPNG(t, 7)},
	)
	entries, err := scanWebPArchiveEntries(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	stats := archiveWorkflowStats{}
	if err := writeWebPArchiveOutput(source, output, entries, imgcodec.LossyWebPEncodeOptions{}, 2, nil, nil, &stats); err != nil {
		t.Fatalf("metadata must not fail a readable comic: %v", err)
	}
	r, err := zip.OpenReader(output)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if len(r.File) != 2 || stats.Extracted != 2 || stats.Kept != 0 {
		t.Fatalf("pages=%d stats=%+v", len(r.File), stats)
	}
	for i, want := range []struct {
		name  string
		width int
	}{{"book/001.webp", 4}, {"book/002.webp", 7}} {
		f, err := r.File[i].Open()
		if err != nil {
			t.Fatal(err)
		}
		img, _, err := image.Decode(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if r.File[i].Name != want.name || img.Bounds().Dx() != want.width {
			t.Fatalf("page %d: name=%s bounds=%v", i, r.File[i].Name, img.Bounds())
		}
	}
}

func TestWebPConversionPreservesAVIFBytesAndExtension(t *testing.T) {
	dir := t.TempDir()
	source, output := filepath.Join(dir, "book.cbz"), filepath.Join(dir, "out.zip")
	// AVIF is opaque to this converter: retain every byte, as for existing WebP.
	original := []byte("\x00\x00\x00\x20ftypavif\x00\x00\x00\x00avifmif1miafMA1B")
	writeWebPTestZIP(t, source, webpTestFile{"001.AVIF", original}, webpTestFile{"002.png", webpTestPNG(t, 5)})
	entries, err := scanWebPArchiveEntries(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	stats := archiveWorkflowStats{}
	if err := writeWebPArchiveOutput(source, output, entries, imgcodec.LossyWebPEncodeOptions{}, 2, nil, nil, &stats); err != nil {
		t.Fatalf("unsupported AVIF must be retained without losing pages: %v", err)
	}
	r, err := zip.OpenReader(output)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if len(r.File) != 2 || r.File[0].Name != "001.AVIF" || stats.Kept != 1 || stats.Extracted != 1 {
		t.Fatalf("unexpected output or stats: files=%v stats=%+v", r.File, stats)
	}
	f, err := r.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("AVIF modified: %v", err)
	}
	complete, err := isWebPArchiveOutputComplete(source, output)
	if err != nil || !complete {
		t.Fatalf("completed output not recognized: complete=%v err=%v", complete, err)
	}
}

func TestWebPConversionErrorNamesPageAndKeepsExistingOutput(t *testing.T) {
	dir := t.TempDir()
	source, output := filepath.Join(dir, "book.cbz"), filepath.Join(dir, "out.zip")
	writeWebPTestZIP(t, source, webpTestFile{"chapter/003.jpg", []byte("broken image")})
	existing := []byte("existing complete output")
	if err := os.WriteFile(output, existing, 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := scanWebPArchiveEntries(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = writeWebPArchiveOutput(source, output, entries, imgcodec.LossyWebPEncodeOptions{}, 2, nil, nil, nil)
	if !errors.Is(err, image.ErrFormat) || !strings.Contains(err.Error(), "chapter/003.jpg") {
		t.Fatalf("missing page and underlying error: %v", err)
	}
	got, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(got, existing) {
		t.Fatalf("existing output modified: %v", err)
	}
	if _, err := os.Stat(output + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary output left behind: %v", err)
	}
}

func TestWebPTaskSummaryExposesFailureWithoutLogs(t *testing.T) {
	source, output := t.TempDir(), t.TempDir()
	writeWebPTestZIP(t, filepath.Join(source, "bad.cbz"), webpTestFile{"003.jpg", []byte("broken image")})
	srv := &Server{store: openServerTestStore(t)}
	now := time.Now()
	task := webpTask{ID: "conversion_failure", SourcePath: source, OutputRootPath: output, Quality: 75, Concurrency: 1, Status: webpTaskRunning, CreatedAt: now, UpdatedAt: now}
	if err := srv.insertWebPTask(&task); err != nil {
		t.Fatal(err)
	}
	status, message := srv.executeWebPTask(&task)
	if status != webpTaskFailed || task.Summary.Failed != 1 {
		t.Fatalf("status=%s stats=%+v", status, task.Summary)
	}
	for _, want := range []string{"bad.cbz", "003.jpg", "unknown format"} {
		if !strings.Contains(message, want) {
			t.Fatalf("summary missing %q: %s", want, message)
		}
	}
	task.Status, task.Error = status, message
	if err := srv.persistWebPTask(&task); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/webp/tasks/"+task.ID, nil)
	req.SetPathValue("id", task.ID)
	rr := httptest.NewRecorder()
	srv.handleAPIAdminGetWebPTask(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "bad.cbz") || strings.Contains(rr.Body.String(), `"logs"`) {
		t.Fatalf("failure must be visible without loading logs: %d %s", rr.Code, rr.Body.String())
	}
}

func TestWebPTaskDetailReturnsBoundedHistoricalFailureExamples(t *testing.T) {
	srv := &Server{store: openServerTestStore(t)}
	now := time.Now()
	task := webpTask{ID: "historical_failures", SourcePath: t.TempDir(), OutputRootPath: t.TempDir(), Quality: 75, Concurrency: 1, Status: webpTaskFailed, Error: "webp task finished with 300 failures", CreatedAt: now, UpdatedAt: now, FinishedAt: now}
	if err := srv.insertWebPTask(&task); err != nil {
		t.Fatal(err)
	}
	if err := srv.appendPersistedWebPTaskLog(&task, "info", "large progress log"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := srv.appendPersistedWebPTaskLog(&task, "error", "bad-%d.cbz: image decode failed", i); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/webp/tasks/"+task.ID, nil)
	req.SetPathValue("id", task.ID)
	rr := httptest.NewRecorder()
	srv.handleAPIAdminGetWebPTask(rr, req)
	var resp struct {
		Task struct {
			FailureExamples []string `json:"failure_examples"`
		}
		Logs json.RawMessage `json:"logs"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if rr.Code != http.StatusOK || len(resp.Task.FailureExamples) != 3 || len(resp.Logs) != 0 {
		t.Fatalf("must return at most three errors without full logs: %d %s", rr.Code, rr.Body.String())
	}
	for i, example := range resp.Task.FailureExamples {
		if example != fmt.Sprintf("bad-%d.cbz: image decode failed", i) {
			t.Fatalf("unexpected failure example %q", example)
		}
	}
}
