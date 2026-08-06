package server

import (
	"archive/zip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"zipmanga/internal/config"
	"zipmanga/internal/db"
)

func TestCancelWebPTaskMarksInactiveTaskCanceled(t *testing.T) {
	store := openServerTestStore(t)
	srv := &Server{
		store:        store,
		webpTaskRuns: map[string]*webpTaskRun{},
	}

	now := time.Now()
	task := webpTask{
		ID:             "task_cancel_test",
		SourcePath:     filepath.Join(t.TempDir(), "source"),
		OutputRootPath: filepath.Join(t.TempDir(), "output"),
		OutputDirPath:  filepath.Join(t.TempDir(), "output", "source-webp"),
		Quality:        75,
		Method:         0,
		Concurrency:    2,
		Status:         webpTaskRunning,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := srv.insertWebPTask(&task); err != nil {
		t.Fatalf("insert task failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/admin/webp/tasks/"+task.ID+"/cancel", nil)
	req.SetPathValue("id", task.ID)
	rr := httptest.NewRecorder()

	srv.handleAPIAdminCancelWebPTask(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	got, err := srv.getPersistedWebPTask(task.ID)
	if err != nil {
		t.Fatalf("getPersistedWebPTask failed: %v", err)
	}
	if !got.CancelRequested {
		t.Fatal("cancel_requested not persisted")
	}
	if got.Status != webpTaskCanceled {
		t.Fatalf("status=%q want canceled", got.Status)
	}
	if got.Error == "" {
		t.Fatal("expected cancel error to be recorded")
	}
}

func TestListWebPTasksSeparatesRunningAndRecentSucceededOrFailed(t *testing.T) {
	store := openServerTestStore(t)
	srv := &Server{store: store}

	now := time.Now()
	baseDir := t.TempDir()
	running := webpTask{
		ID:             "task_running",
		SourcePath:     filepath.Join(baseDir, "source"),
		OutputRootPath: filepath.Join(baseDir, "output"),
		OutputDirPath:  filepath.Join(baseDir, "output", "source-webp"),
		Quality:        75,
		Method:         0,
		Concurrency:    2,
		Status:         webpTaskRunning,
		CreatedAt:      now.Add(-10 * time.Minute),
		UpdatedAt:      now,
	}
	succeededA := webpTask{
		ID:             "task_succeeded_a",
		SourcePath:     filepath.Join(baseDir, "source-s1"),
		OutputRootPath: filepath.Join(baseDir, "output"),
		OutputDirPath:  filepath.Join(baseDir, "output", "source-s1-webp"),
		Quality:        75,
		Method:         0,
		Concurrency:    2,
		Status:         webpTaskSucceeded,
		CreatedAt:      now.Add(-2 * time.Minute),
		UpdatedAt:      now.Add(-2 * time.Minute),
		FinishedAt:     now.Add(-2 * time.Minute),
	}
	succeededB := webpTask{
		ID:             "task_succeeded_b",
		SourcePath:     filepath.Join(baseDir, "source-s2"),
		OutputRootPath: filepath.Join(baseDir, "output"),
		OutputDirPath:  filepath.Join(baseDir, "output", "source-s2-webp"),
		Quality:        75,
		Method:         0,
		Concurrency:    2,
		Status:         webpTaskSucceeded,
		CreatedAt:      now.Add(-3 * time.Minute),
		UpdatedAt:      now.Add(-3 * time.Minute),
		FinishedAt:     now.Add(-3 * time.Minute),
	}
	failedA := webpTask{
		ID:             "task_failed_a",
		SourcePath:     filepath.Join(baseDir, "source-f1"),
		OutputRootPath: filepath.Join(baseDir, "output"),
		OutputDirPath:  filepath.Join(baseDir, "output", "source-f1-webp"),
		Quality:        75,
		Method:         0,
		Concurrency:    2,
		Status:         webpTaskFailed,
		CreatedAt:      now.Add(-1 * time.Minute),
		UpdatedAt:      now.Add(-1 * time.Minute),
		FinishedAt:     now.Add(-1 * time.Minute),
	}
	canceledC := webpTask{
		ID:             "task_canceled_c",
		SourcePath:     filepath.Join(baseDir, "source-c"),
		OutputRootPath: filepath.Join(baseDir, "output"),
		OutputDirPath:  filepath.Join(baseDir, "output", "source-c-webp"),
		Quality:        75,
		Method:         0,
		Concurrency:    2,
		Status:         webpTaskCanceled,
		CreatedAt:      now.Add(-5 * time.Minute),
		UpdatedAt:      now.Add(-5 * time.Minute),
		FinishedAt:     now.Add(-5 * time.Minute),
	}
	for i, task := range []*webpTask{&running, &succeededA, &succeededB, &failedA, &canceledC} {
		if err := srv.insertWebPTask(task); err != nil {
			t.Fatalf("insert task %d failed: %v", i, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/webp/tasks", nil)
	rr := httptest.NewRecorder()
	srv.handleAPIAdminListWebPTasks(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var resp struct {
		Running map[string]any   `json:"running"`
		Items   []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	if got := stringValue(resp.Running, "id"); got != "task_running" {
		t.Fatalf("running task id=%q want=%q", got, "task_running")
	}
	if len(resp.Items) != 2 {
		t.Fatalf("items len=%d want=2", len(resp.Items))
	}
	if got := stringValue(resp.Items[0], "id"); got != "task_failed_a" {
		t.Fatalf("items[0].id=%q want=%q", got, "task_failed_a")
	}
	if got := stringValue(resp.Items[1], "id"); got != "task_succeeded_a" {
		t.Fatalf("items[1].id=%q want=%q", got, "task_succeeded_a")
	}
	for i, item := range resp.Items {
		status := stringValue(item, "status")
		if strings.EqualFold(status, string(webpTaskRunning)) || strings.EqualFold(status, string(webpTaskQueued)) {
			t.Fatalf("items[%d] should not include active task status=%q", i, status)
		}
		if !strings.EqualFold(status, string(webpTaskSucceeded)) && !strings.EqualFold(status, string(webpTaskFailed)) {
			t.Fatalf("items[%d] status=%q want succeeded or failed", i, status)
		}
	}
}

func TestCreateWebPTaskRejectsWhenAnotherTaskIsRunning(t *testing.T) {
	store := openServerTestStore(t)
	srv := &Server{store: store, runtimeCfg: config.Default().Runtime}

	sourceRoot, err := store.CreateRootDirectory("source-root", t.TempDir())
	if err != nil {
		t.Fatalf("CreateRootDirectory source failed: %v", err)
	}
	outputRoot, err := store.CreateRootDirectory("output-root", t.TempDir())
	if err != nil {
		t.Fatalf("CreateRootDirectory output failed: %v", err)
	}
	sourcePath := filepath.Join(sourceRoot.Path, "books")
	if err := os.MkdirAll(sourcePath, 0o755); err != nil {
		t.Fatalf("mkdir source path failed: %v", err)
	}

	active := webpTask{
		ID:             "task_running_conflict",
		SourcePath:     sourcePath,
		OutputRootPath: outputRoot.Path,
		OutputDirPath:  filepath.Join(outputRoot.Path, "books-webp"),
		Quality:        75,
		Method:         0,
		Concurrency:    2,
		Status:         webpTaskRunning,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := srv.insertWebPTask(&active); err != nil {
		t.Fatalf("insert active task failed: %v", err)
	}

	body := strings.NewReader(`{"source_root_id":` + strconv.FormatInt(sourceRoot.ID, 10) + `,"source_path":` + strconv.Quote(sourcePath) + `,"output_root_id":` + strconv.FormatInt(outputRoot.ID, 10) + `}`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/webp/tasks", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	srv.handleAPIAdminCreateWebPTask(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d want=%d body=%s", rr.Code, http.StatusConflict, rr.Body.String())
	}
}

func TestGetWebPTaskOmitsLogsByDefault(t *testing.T) {
	store := openServerTestStore(t)
	srv := &Server{store: store}

	now := time.Now()
	task := webpTask{
		ID:             "task_without_logs",
		SourcePath:     filepath.Join(t.TempDir(), "source"),
		OutputRootPath: filepath.Join(t.TempDir(), "output"),
		OutputDirPath:  filepath.Join(t.TempDir(), "output", "source-webp"),
		Quality:        75,
		Method:         0,
		Concurrency:    2,
		Status:         webpTaskCanceled,
		CreatedAt:      now,
		UpdatedAt:      now,
		FinishedAt:     now,
	}
	if err := srv.insertWebPTask(&task); err != nil {
		t.Fatalf("insert task failed: %v", err)
	}
	if err := srv.appendPersistedWebPTaskLog(&task, "info", "hello"); err != nil {
		t.Fatalf("appendPersistedWebPTaskLog failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/webp/tasks/"+task.ID, nil)
	req.SetPathValue("id", task.ID)
	rr := httptest.NewRecorder()

	srv.handleAPIAdminGetWebPTask(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	if _, exists := resp["logs"]; exists {
		t.Fatal("logs should be omitted by default")
	}
}

func TestCleanupWebPTaskWorkDirsRemovesResidualWorkDirs(t *testing.T) {
	store := openServerTestStore(t)
	srv := &Server{store: store}

	sourceRoot := t.TempDir()
	outputRoot := t.TempDir()
	outputDir := filepath.Join(outputRoot, "series-webp")
	sourceArchiveA := filepath.Join(sourceRoot, "a.cbz")
	sourceArchiveB := filepath.Join(sourceRoot, "nested", "b.cbz")
	if err := os.MkdirAll(filepath.Dir(sourceArchiveB), 0o755); err != nil {
		t.Fatalf("mkdir source dir failed: %v", err)
	}
	if err := writeTestZipArchive(sourceArchiveA, map[string]string{"page1.jpg": "a"}); err != nil {
		t.Fatalf("write source archive A failed: %v", err)
	}
	if err := writeTestZipArchive(sourceArchiveB, map[string]string{"page1.jpg": "b"}); err != nil {
		t.Fatalf("write source archive B failed: %v", err)
	}

	workDirA := filepath.Join(outputDir, "a.zip.work")
	workDirB := filepath.Join(outputDir, "nested", "b.zip.work")
	for _, dir := range []string{workDirA, workDirB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir work dir %s failed: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "stale.webp"), []byte("stale"), 0o644); err != nil {
			t.Fatalf("write stale file in %s failed: %v", dir, err)
		}
	}

	task := webpTask{
		ID:             "task_cleanup_workdirs",
		SourcePath:     sourceRoot,
		OutputRootPath: outputRoot,
		OutputDirPath:  outputDir,
		Quality:        75,
		Method:         0,
		Concurrency:    2,
		Status:         webpTaskFailed,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := srv.cleanupWebPTaskWorkDirs(&task); err != nil {
		t.Fatalf("cleanupWebPTaskWorkDirs failed: %v", err)
	}

	for _, dir := range []string{workDirA, workDirB} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("work dir %s should be removed, stat err=%v", dir, err)
		}
	}
}

func TestBuildWebPTaskDiskSnapshotUsesManifestWithoutArchiveRescan(t *testing.T) {
	store := openServerTestStore(t)
	srv := &Server{store: store}

	sourceRoot := t.TempDir()
	outputRoot := t.TempDir()
	sourceArchive := filepath.Join(sourceRoot, "series.cbz")
	outputArchive := filepath.Join(outputRoot, "series-webp", "series.zip")
	if err := writeTestZipArchive(sourceArchive, map[string]string{
		"page1.jpg": "jpg-1",
		"page2.png": "png-2",
	}); err != nil {
		t.Fatalf("write source archive failed: %v", err)
	}
	if err := writeTestZipArchive(outputArchive, map[string]string{
		"page1.webp": "webp-1",
		"page2.webp": "webp-2",
	}); err != nil {
		t.Fatalf("write output archive failed: %v", err)
	}

	sourceInfo, err := os.Stat(sourceArchive)
	if err != nil {
		t.Fatalf("stat source failed: %v", err)
	}
	outputInfo, err := os.Stat(outputArchive)
	if err != nil {
		t.Fatalf("stat output failed: %v", err)
	}
	if err := store.UpsertWebPArchiveManifest(db.WebPArchiveManifest{
		SourcePath:       sourceArchive,
		OutputZipPath:    outputArchive,
		Quality:          75,
		Method:           0,
		SourceMtimeUnix:  sourceInfo.ModTime().UnixNano(),
		SourceSize:       sourceInfo.Size(),
		SourceEntryCount: 2,
		ConvertedEntries: 2,
		KeptEntries:      0,
		OutputSize:       outputInfo.Size(),
		CompletedAt:      time.Now(),
	}); err != nil {
		t.Fatalf("UpsertWebPArchiveManifest failed: %v", err)
	}

	task := webpTask{
		SourcePath:     sourceRoot,
		OutputRootPath: outputRoot,
		OutputDirPath:  filepath.Join(outputRoot, "series-webp"),
		Quality:        75,
		Method:         0,
	}
	snapshot, err := srv.buildWebPTaskDiskSnapshot(task)
	if err != nil {
		t.Fatalf("buildWebPTaskDiskSnapshot failed: %v", err)
	}
	if snapshot.TotalArchives != 1 || snapshot.DoneArchives != 1 {
		t.Fatalf("snapshot archive counts=%+v", snapshot)
	}
	if snapshot.PendingArchives != 0 || snapshot.PartialArchives != 0 {
		t.Fatalf("snapshot pending/partial=%+v", snapshot)
	}
	if snapshot.Summary.Extracted != 2 || snapshot.Summary.Kept != 0 {
		t.Fatalf("snapshot summary=%+v", snapshot.Summary)
	}
}

func writeTestZipArchive(path string, files map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()

	writer := zip.NewWriter(out)
	for name, body := range files {
		entry, err := writer.Create(name)
		if err != nil {
			_ = writer.Close()
			return err
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			_ = writer.Close()
			return err
		}
	}
	return writer.Close()
}

func stringValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
