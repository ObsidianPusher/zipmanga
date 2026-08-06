package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"zipmanga/internal/db"
)

type webpTaskStatus string

const (
	webpTaskQueued    webpTaskStatus = "queued"
	webpTaskRunning   webpTaskStatus = "running"
	webpTaskSucceeded webpTaskStatus = "succeeded"
	webpTaskFailed    webpTaskStatus = "failed"
	webpTaskCanceled  webpTaskStatus = "canceled"
)

type webpTaskLogLine struct {
	Seq       uint64    `json:"seq"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type webpTask struct {
	ID                  string
	SourceRootPath      string
	SourcePath          string
	OutputRootPath      string
	OutputDirPath       string
	Quality             int64
	Method              int64
	Concurrency         int64
	Status              webpTaskStatus
	ProgressTotal       int
	ProgressDone        int
	PartialArchives     int
	PendingArchives     int
	CurrentArchive      string
	CurrentArchiveDone  int
	CurrentArchiveTotal int
	CurrentPhase        string
	CurrentItem         string
	CancelRequested     bool
	LastSeq             uint64
	Summary             archiveWorkflowStats
	Error               string
	CreatedAt           time.Time
	StartedAt           time.Time
	FinishedAt          time.Time
	UpdatedAt           time.Time
}

type webpTaskRun struct {
	mu              sync.RWMutex
	cancelRequested bool
}

type webpTaskDiskSnapshot struct {
	TotalArchives       int
	DoneArchives        int
	PartialArchives     int
	PendingArchives     int
	CurrentArchive      string
	CurrentArchiveDone  int
	CurrentArchiveTotal int
	Summary             archiveWorkflowStats
}

type webpTaskStateGuard struct {
	mu          sync.Mutex
	lastPersist time.Time
}

func (g *webpTaskStateGuard) update(persist func() error, force bool, mutate func()) error {
	if g == nil {
		if mutate != nil {
			mutate()
		}
		if persist != nil {
			return persist()
		}
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if mutate != nil {
		mutate()
	}
	if !force && !g.lastPersist.IsZero() && time.Since(g.lastPersist) < 750*time.Millisecond {
		return nil
	}
	if persist != nil {
		if err := persist(); err != nil {
			return err
		}
	}
	g.lastPersist = time.Now()
	return nil
}

func (r *webpTaskRun) requestCancel() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancelRequested = true
}

func (r *webpTaskRun) isCancelRequested() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cancelRequested
}

func (s *Server) handleAPIAdminBrowseRootDirectoryDirs(w http.ResponseWriter, r *http.Request) {
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid root directory id"})
		return
	}
	root, err := s.store.GetRootDirectory(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "root directory not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	var req struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	currentPath := strings.TrimSpace(req.Path)
	if currentPath == "" {
		currentPath = root.Path
	}
	targetPath, err := validateRootDirectoryBrowsePath(root, currentPath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	items, err := browseLocalDirectories(targetPath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"current_path": targetPath,
		"items":        items,
	})
}

func validateRootDirectoryBrowsePath(root db.RootDirectory, rawPath string) (string, error) {
	rootPath, err := canonicalizeLocalRootPath(root.Path)
	if err != nil {
		return "", err
	}
	target, err := canonicalizeLocalRootPath(rawPath)
	if err != nil {
		return "", err
	}
	rootPrefix := rootPath + string(filepath.Separator)
	if target != rootPath && !strings.HasPrefix(target, rootPrefix) {
		return "", fmt.Errorf("path is outside root directory")
	}
	return target, nil
}

func browseLocalDirectories(rootPath string) ([]map[string]any, error) {
	entries, err := os.ReadDir(rootPath)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		item := map[string]any{
			"name": entry.Name(),
			"path": filepath.Join(rootPath, entry.Name()),
		}
		if mountsShouldSkipDirName(entry.Name()) {
			continue
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(fmt.Sprint(out[i]["name"])) < strings.ToLower(fmt.Sprint(out[j]["name"]))
	})
	if len(out) > 2000 {
		out = out[:2000]
	}
	return out, nil
}

func (s *Server) recoverWebPTasks() {
	items, err := s.listPersistedWebPTasksByStatus("", 200)
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("webp task recovery load warning: %v", err)
		}
		return
	}
	activeSeen := false
	for _, task := range items {
		if task.Status != webpTaskQueued && task.Status != webpTaskRunning {
			continue
		}
		if activeSeen {
			if s.logger != nil {
				s.logger.Printf("skip extra recovered webp task %s status=%s", task.ID, task.Status)
			}
			continue
		}
		if task.CancelRequested {
			task.Status = webpTaskCanceled
			task.CurrentArchive = ""
			task.CurrentPhase = "已取消"
			task.CurrentItem = ""
			task.Error = "task canceled before recovery"
			task.FinishedAt = time.Now()
			task.UpdatedAt = task.FinishedAt
			if err := s.persistWebPTask(&task); err != nil && s.logger != nil {
				s.logger.Printf("persist canceled recovered webp task %s failed: %v", task.ID, err)
			}
			continue
		}
		activeSeen = true
		s.startWebPTaskRun(task.ID)
	}
}

func (s *Server) maybeRefreshPersistedWebPTask(task *webpTask) {
	if task == nil {
		return
	}
	if task.Status != webpTaskQueued && task.Status != webpTaskRunning {
		return
	}
	if s.hasActiveWebPTaskRun(task.ID) {
		return
	}
	if !task.CancelRequested && !task.UpdatedAt.IsZero() && time.Since(task.UpdatedAt) < 10*time.Second {
		return
	}
	_ = s.refreshWebPTaskSnapshot(task, true)
}

func (s *Server) startWebPTaskRun(taskID string) bool {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return false
	}
	s.webpTaskMu.Lock()
	if _, ok := s.webpTaskRuns[taskID]; ok {
		s.webpTaskMu.Unlock()
		return false
	}
	s.webpTaskRuns[taskID] = &webpTaskRun{}
	s.webpTaskMu.Unlock()

	go s.runPersistedWebPTask(taskID)
	return true
}

func (s *Server) finishWebPTaskRun(taskID string) {
	s.webpTaskMu.Lock()
	defer s.webpTaskMu.Unlock()
	delete(s.webpTaskRuns, taskID)
}

func (s *Server) requestWebPTaskCancel(taskID string) {
	s.webpTaskMu.Lock()
	defer s.webpTaskMu.Unlock()
	if run, ok := s.webpTaskRuns[taskID]; ok && run != nil {
		run.requestCancel()
	}
}

func (s *Server) hasActiveWebPTaskRun(taskID string) bool {
	s.webpTaskMu.Lock()
	defer s.webpTaskMu.Unlock()
	_, ok := s.webpTaskRuns[taskID]
	return ok
}

func (s *Server) hasActiveWebPTask(taskID string) bool {
	return s.hasActiveWebPTaskRun(taskID)
}

func (s *Server) isWebPTaskCancelRequested(taskID string) bool {
	s.webpTaskMu.Lock()
	run, ok := s.webpTaskRuns[taskID]
	s.webpTaskMu.Unlock()
	if ok && run != nil && run.isCancelRequested() {
		return true
	}

	task, err := s.getPersistedWebPTask(taskID)
	if err != nil {
		return false
	}
	return task.CancelRequested
}

func (s *Server) refreshWebPTaskSnapshot(task *webpTask, force bool) error {
	if task == nil {
		return errors.New("task is nil")
	}
	if !force {
		if task.Status != webpTaskQueued && task.Status != webpTaskRunning {
			return nil
		}
		if time.Since(task.UpdatedAt) < 8*time.Second && !task.CancelRequested {
			return nil
		}
	}

	snapshot, err := s.buildWebPTaskDiskSnapshot(*task)
	if err != nil {
		return err
	}
	task.ProgressTotal = snapshot.TotalArchives
	task.ProgressDone = snapshot.DoneArchives
	task.PartialArchives = snapshot.PartialArchives
	task.PendingArchives = snapshot.PendingArchives
	task.CurrentArchiveDone = snapshot.CurrentArchiveDone
	task.CurrentArchiveTotal = snapshot.CurrentArchiveTotal
	task.Summary = snapshot.Summary

	if strings.TrimSpace(snapshot.CurrentArchive) != "" {
		task.CurrentArchive = snapshot.CurrentArchive
		task.CurrentItem = snapshot.CurrentArchive
	}
	switch {
	case task.CancelRequested && task.Status != webpTaskCanceled:
		task.CurrentPhase = "取消中"
	case snapshot.CurrentArchive != "" && snapshot.CurrentArchiveTotal > 0:
		task.CurrentPhase = fmt.Sprintf("处理压缩包 %s · 图片 %d/%d", snapshot.CurrentArchive, snapshot.CurrentArchiveDone, snapshot.CurrentArchiveTotal)
	case snapshot.DoneArchives >= snapshot.TotalArchives && snapshot.TotalArchives > 0:
		task.CurrentPhase = "已完成"
	case snapshot.TotalArchives == 0:
		task.CurrentPhase = "扫描完成"
	default:
		task.CurrentPhase = fmt.Sprintf("已完成 %d/%d，部分完成 %d，待处理 %d", snapshot.DoneArchives, snapshot.TotalArchives, snapshot.PartialArchives, snapshot.PendingArchives)
	}
	task.UpdatedAt = time.Now()
	return s.persistWebPTask(task)
}

func (s *Server) buildWebPTaskDiskSnapshot(task webpTask) (webpTaskDiskSnapshot, error) {
	jobs, stats, err := scanWebPArchiveJobs(task.SourcePath, task.OutputDirPath, nil, nil)
	if err != nil {
		return webpTaskDiskSnapshot{}, err
	}

	snapshot := webpTaskDiskSnapshot{
		TotalArchives:   len(jobs),
		PendingArchives: len(jobs),
		Summary:         stats,
	}
	for _, job := range jobs {
		manifest, complete, manifestErr := s.matchCompletedWebPArchiveManifest(job, task.Quality, task.Method)
		if manifestErr == nil && complete {
			snapshot.DoneArchives++
			snapshot.PendingArchives--
			snapshot.Summary.Moved++
			snapshot.Summary.Zipped++
			snapshot.Summary.Kept += manifest.KeptEntries
			snapshot.Summary.Extracted += manifest.ConvertedEntries
			continue
		}
		if manifestErr != nil {
			snapshot.Summary.Failed++
			continue
		}
		finalZipExists, existsErr := pathExists(job.FinalZip)
		if existsErr != nil {
			snapshot.Summary.Failed++
			continue
		}
		if finalZipExists {
			snapshot.PartialArchives++
			snapshot.PendingArchives--
			snapshot.Summary.Partial++
			if snapshot.CurrentArchive == "" {
				snapshot.CurrentArchive = job.RelPath
			}
		}
	}
	if snapshot.PendingArchives < 0 {
		snapshot.PendingArchives = 0
	}
	snapshot.Summary.Pending = snapshot.PendingArchives
	return snapshot, nil
}

func (s *Server) handleAPIAdminListWebPTasks(w http.ResponseWriter, r *http.Request) {
	running, err := s.getPersistedRunningWebPTask()
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			running = webpTask{}
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
	}
	items, err := s.listPersistedWebPTasksByStatuses([]webpTaskStatus{webpTaskSucceeded, webpTaskFailed}, 2)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if running.ID != "" {
		s.maybeRefreshPersistedWebPTask(&running)
	}
	for i := range items {
		s.maybeRefreshPersistedWebPTask(&items[i])
	}
	outItems := make([]map[string]any, 0, len(items))
	for _, item := range items {
		outItems = append(outItems, webpTaskDTO(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"running": func() any {
			if running.ID == "" {
				return nil
			}
			return webpTaskDTO(running)
		}(),
		"items": outItems,
	})
}

func (s *Server) handleAPIAdminGetWebPTask(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(r.PathValue("id"))
	if taskID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid task id"})
		return
	}
	task, err := s.getPersistedWebPTask(taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "task not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.maybeRefreshPersistedWebPTask(&task)
	includeLogs := parseBoolQueryParam(r.URL.Query().Get("include_logs"))
	resp := map[string]any{"task": webpTaskDTO(task)}
	if includeLogs {
		afterSeq, _ := strconv.ParseUint(strings.TrimSpace(r.URL.Query().Get("after_seq")), 10, 64)
		logs, err := s.listPersistedWebPTaskLogsAfter(taskID, afterSeq)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		resp["logs"] = logs
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAPIAdminCreateWebPTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceRootID    int64  `json:"source_root_id"`
		SourcePath      string `json:"source_path"`
		OutputRootID    int64  `json:"output_root_id"`
		WebPQuality     *int64 `json:"webp_quality"`
		WebPMethod      *int64 `json:"webp_method"`
		WebPConcurrency *int64 `json:"webp_concurrency"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if req.SourceRootID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "source_root_id must be positive"})
		return
	}
	if req.OutputRootID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "output_root_id must be positive"})
		return
	}

	sourceRoot, err := s.store.GetRootDirectory(req.SourceRootID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "source root directory not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	outputRoot, err := s.store.GetRootDirectory(req.OutputRootID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "output root directory not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	sourcePath, err := validateRootDirectoryBrowsePath(sourceRoot, req.SourcePath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	outputRootPath, err := canonicalizeLocalRootPath(outputRoot.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	runtimeCfg := s.currentRuntimeConfig()
	task := webpTask{
		ID:             fmt.Sprintf("wpt_%d_%06d", time.Now().Unix(), time.Now().UnixNano()%1000000),
		SourceRootPath: sourceRoot.Path,
		SourcePath:     sourcePath,
		OutputRootPath: outputRootPath,
		OutputDirPath:  filepath.Join(outputRootPath, sanitizeDirectoryName(filepath.Base(sourcePath))+"-webp"),
		Quality:        runtimeCfg.WebPQuality,
		Method:         runtimeCfg.WebPMethod,
		Concurrency:    runtimeCfg.WebPConcurrency,
		Status:         webpTaskQueued,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
		Summary:        archiveWorkflowStats{},
	}
	if req.WebPQuality != nil {
		task.Quality = *req.WebPQuality
	}
	if req.WebPMethod != nil {
		task.Method = *req.WebPMethod
	}
	if req.WebPConcurrency != nil {
		task.Concurrency = *req.WebPConcurrency
	}
	if task.Quality < 1 || task.Quality > 100 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "webp_quality must be between 1 and 100"})
		return
	}
	if task.Method < 0 || task.Method > 6 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "webp_method must be between 0 and 6"})
		return
	}
	if task.Concurrency < 1 || task.Concurrency > 16 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "webp_concurrency must be between 1 and 16"})
		return
	}

	if err := ensureDirExists(task.SourcePath); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("invalid source path: %v", err)})
		return
	}
	if err := ensureDirExists(task.OutputRootPath); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("invalid output root: %v", err)})
		return
	}
	s.webpTaskMu.Lock()
	if len(s.webpTaskRuns) > 0 {
		s.webpTaskMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"error": "another webp task is already running"})
		return
	}
	if activeTask, err := s.getPersistedRunningWebPTask(); err == nil {
		s.webpTaskMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"error": fmt.Sprintf("webp task %s is already running", activeTask.ID)})
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		s.webpTaskMu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := s.insertWebPTask(&task); err != nil {
		s.webpTaskMu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.webpTaskMu.Unlock()
	s.startWebPTaskRun(task.ID)

	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":   true,
		"task": webpTaskDTO(task),
	})
}

func (s *Server) handleAPIAdminCancelWebPTask(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(r.PathValue("id"))
	if taskID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid task id"})
		return
	}
	task, err := s.getPersistedWebPTask(taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "task not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if task.Status != webpTaskQueued && task.Status != webpTaskRunning {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "task is not running"})
		return
	}
	task.CancelRequested = true
	task.CurrentPhase = "取消中"
	if strings.TrimSpace(task.CurrentItem) == "" {
		task.CurrentItem = ""
	}
	task.UpdatedAt = time.Now()
	if !s.hasActiveWebPTask(taskID) {
		task.Status = webpTaskCanceled
		task.CurrentArchive = ""
		task.CurrentItem = ""
		task.Error = "task canceled by user"
		task.FinishedAt = time.Now()
	}
	if err := s.persistWebPTask(&task); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.requestWebPTaskCancel(taskID)
	_ = s.appendPersistedWebPTaskLog(&task, "warn", "取消请求已提交")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task": webpTaskDTO(task)})
}

func (s *Server) runPersistedWebPTask(taskID string) {
	defer s.finishWebPTaskRun(taskID)

	task, err := s.getPersistedWebPTask(taskID)
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("load webp task %s failed: %v", taskID, err)
		}
		return
	}
	if task.CancelRequested {
		task.Status = webpTaskCanceled
		task.CurrentArchive = ""
		task.CurrentPhase = "已取消"
		task.CurrentItem = ""
		task.Error = "task canceled before start"
		task.FinishedAt = time.Now()
		task.UpdatedAt = task.FinishedAt
		_ = s.persistWebPTask(&task)
		return
	}

	now := time.Now()
	task.Status = webpTaskRunning
	task.Error = ""
	if task.StartedAt.IsZero() {
		task.StartedAt = now
	}
	task.FinishedAt = time.Time{}
	task.UpdatedAt = now
	if err := s.persistWebPTask(&task); err != nil {
		if s.logger != nil {
			s.logger.Printf("persist start webp task %s failed: %v", taskID, err)
		}
		return
	}
	_ = s.appendPersistedWebPTaskLog(&task, "info", "$ webp-task run --source %q --output-root %q --quality %d --method %d --concurrency %d",
		task.SourcePath, task.OutputRootPath, task.Quality, task.Method, task.Concurrency)

	finalStatus, finalErr := s.executeWebPTask(&task)
	task.Status = finalStatus
	task.Error = strings.TrimSpace(finalErr)
	task.CurrentArchive = ""
	task.CurrentItem = ""
	if finalStatus == webpTaskSucceeded {
		task.CurrentPhase = "已完成"
	} else if finalStatus == webpTaskCanceled {
		task.CurrentPhase = "已取消"
	} else {
		task.CurrentPhase = "执行失败"
	}
	task.FinishedAt = time.Now()
	task.UpdatedAt = task.FinishedAt
	switch finalStatus {
	case webpTaskSucceeded:
		_ = s.appendPersistedWebPTaskLog(&task, "info", "任务完成: done=%d total=%d converted=%d kept=%d failed=%d", task.ProgressDone, task.ProgressTotal, task.Summary.Extracted, task.Summary.Kept, task.Summary.Failed)
	case webpTaskCanceled:
		_ = s.appendPersistedWebPTaskLog(&task, "warn", "任务已取消: done=%d total=%d", task.ProgressDone, task.ProgressTotal)
	default:
		_ = s.appendPersistedWebPTaskLog(&task, "error", "任务失败: %s", task.Error)
	}
	if cleanupErr := s.cleanupWebPTaskWorkDirs(&task); cleanupErr != nil {
		if s.logger != nil {
			s.logger.Printf("cleanup webp work dirs for task %s failed: %v", task.ID, cleanupErr)
		}
		_ = s.appendPersistedWebPTaskLog(&task, "warn", "清理遗留 work 目录失败: %v", cleanupErr)
	}
	if err := s.persistWebPTask(&task); err != nil && s.logger != nil {
		s.logger.Printf("persist finish webp task %s failed: %v", task.ID, err)
	}
}

func (s *Server) insertWebPTask(task *webpTask) error {
	if task == nil {
		return errors.New("task is nil")
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now()
	}
	if task.UpdatedAt.IsZero() {
		task.UpdatedAt = task.CreatedAt
	}
	summaryJSON, err := json.Marshal(task.Summary)
	if err != nil {
		return err
	}
	_, err = s.store.DB().Exec(`
		INSERT INTO webp_tasks (
			id, source_root_path, source_path, output_root_path, output_dir_path,
			quality, method, concurrency, status,
			total_archives, done_archives, partial_archives, pending_archives,
			current_archive, current_archive_done, current_archive_total, current_phase, current_item,
			cancel_requested, last_seq, summary_json, error,
			created_at, started_at, finished_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		task.ID, strings.TrimSpace(task.SourceRootPath), strings.TrimSpace(task.SourcePath), strings.TrimSpace(task.OutputRootPath), strings.TrimSpace(task.OutputDirPath),
		task.Quality, task.Method, task.Concurrency, string(task.Status),
		task.ProgressTotal, task.ProgressDone, task.PartialArchives, task.PendingArchives,
		strings.TrimSpace(task.CurrentArchive), task.CurrentArchiveDone, task.CurrentArchiveTotal, strings.TrimSpace(task.CurrentPhase), strings.TrimSpace(task.CurrentItem),
		webpTaskBoolToInt(task.CancelRequested), task.LastSeq, string(summaryJSON), strings.TrimSpace(task.Error),
		task.CreatedAt.UTC().Format(time.RFC3339Nano), formatOptionalTime(task.StartedAt), formatOptionalTime(task.FinishedAt), task.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *Server) persistWebPTask(task *webpTask) error {
	if task == nil {
		return errors.New("task is nil")
	}
	if task.UpdatedAt.IsZero() {
		task.UpdatedAt = time.Now()
	}
	summaryJSON, err := json.Marshal(task.Summary)
	if err != nil {
		return err
	}
	_, err = s.store.DB().Exec(`
		UPDATE webp_tasks
		SET
			source_root_path = ?,
			source_path = ?,
			output_root_path = ?,
			output_dir_path = ?,
			quality = ?,
			method = ?,
			concurrency = ?,
			status = ?,
			total_archives = ?,
			done_archives = ?,
			partial_archives = ?,
			pending_archives = ?,
			current_archive = ?,
			current_archive_done = ?,
			current_archive_total = ?,
			current_phase = ?,
			current_item = ?,
			cancel_requested = ?,
			last_seq = ?,
			summary_json = ?,
			error = ?,
			created_at = ?,
			started_at = ?,
			finished_at = ?,
			updated_at = ?
		WHERE id = ?
	`,
		strings.TrimSpace(task.SourceRootPath), strings.TrimSpace(task.SourcePath), strings.TrimSpace(task.OutputRootPath), strings.TrimSpace(task.OutputDirPath),
		task.Quality, task.Method, task.Concurrency, string(task.Status),
		task.ProgressTotal, task.ProgressDone, task.PartialArchives, task.PendingArchives,
		strings.TrimSpace(task.CurrentArchive), task.CurrentArchiveDone, task.CurrentArchiveTotal, strings.TrimSpace(task.CurrentPhase), strings.TrimSpace(task.CurrentItem),
		webpTaskBoolToInt(task.CancelRequested), task.LastSeq, string(summaryJSON), strings.TrimSpace(task.Error),
		task.CreatedAt.UTC().Format(time.RFC3339Nano), formatOptionalTime(task.StartedAt), formatOptionalTime(task.FinishedAt), task.UpdatedAt.UTC().Format(time.RFC3339Nano),
		task.ID,
	)
	return err
}

func (s *Server) getPersistedWebPTask(taskID string) (webpTask, error) {
	row := s.store.DB().QueryRow(`
		SELECT
			id, source_root_path, source_path, output_root_path, output_dir_path,
			quality, method, concurrency, status,
			total_archives, done_archives, partial_archives, pending_archives,
			current_archive, current_archive_done, current_archive_total, current_phase, current_item,
			cancel_requested, last_seq, summary_json, error,
			created_at, started_at, finished_at, updated_at
		FROM webp_tasks
		WHERE id = ?
	`, taskID)
	return scanWebPTask(row)
}

func (s *Server) getPersistedRunningWebPTask() (webpTask, error) {
	row := s.store.DB().QueryRow(`
		SELECT
			id, source_root_path, source_path, output_root_path, output_dir_path,
			quality, method, concurrency, status,
			total_archives, done_archives, partial_archives, pending_archives,
			current_archive, current_archive_done, current_archive_total, current_phase, current_item,
			cancel_requested, last_seq, summary_json, error,
			created_at, started_at, finished_at, updated_at
		FROM webp_tasks
		WHERE status IN ('queued', 'running')
		ORDER BY CASE status WHEN 'running' THEN 0 ELSE 1 END, created_at DESC
		LIMIT 1
	`)
	return scanWebPTask(row)
}

func (s *Server) listPersistedWebPTasksByStatus(status string, limit int) ([]webpTask, error) {
	if limit <= 0 {
		limit = 2
	}
	query := `
		SELECT
			id, source_root_path, source_path, output_root_path, output_dir_path,
			quality, method, concurrency, status,
			total_archives, done_archives, partial_archives, pending_archives,
			current_archive, current_archive_done, current_archive_total, current_phase, current_item,
			cancel_requested, last_seq, summary_json, error,
			created_at, started_at, finished_at, updated_at
		FROM webp_tasks
	`
	args := make([]any, 0, 2)
	switch strings.TrimSpace(status) {
	case "":
		query += "WHERE status IN ('queued', 'running', 'succeeded', 'failed', 'canceled')\nORDER BY CASE status WHEN 'running' THEN 0 WHEN 'queued' THEN 1 WHEN 'canceled' THEN 2 WHEN 'failed' THEN 3 ELSE 4 END, created_at DESC\nLIMIT ?"
		args = append(args, limit)
	default:
		query += "WHERE status = ?\nORDER BY created_at DESC\nLIMIT ?"
		args = append(args, status, limit)
	}
	rows, err := s.store.DB().Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]webpTask, 0, limit)
	for rows.Next() {
		task, err := scanWebPTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Server) listPersistedWebPTasksByStatuses(statuses []webpTaskStatus, limit int) ([]webpTask, error) {
	if limit <= 0 {
		limit = 2
	}
	filtered := make([]string, 0, len(statuses))
	for _, status := range statuses {
		trimmed := strings.TrimSpace(string(status))
		if trimmed == "" {
			continue
		}
		filtered = append(filtered, trimmed)
	}
	if len(filtered) == 0 {
		return []webpTask{}, nil
	}

	placeholders := make([]string, 0, len(filtered))
	args := make([]any, 0, len(filtered)+1)
	for _, status := range filtered {
		placeholders = append(placeholders, "?")
		args = append(args, status)
	}
	args = append(args, limit)

	query := fmt.Sprintf(`
		SELECT
			id, source_root_path, source_path, output_root_path, output_dir_path,
			quality, method, concurrency, status,
			total_archives, done_archives, partial_archives, pending_archives,
			current_archive, current_archive_done, current_archive_total, current_phase, current_item,
			cancel_requested, last_seq, summary_json, error,
			created_at, started_at, finished_at, updated_at
		FROM webp_tasks
		WHERE status IN (%s)
		ORDER BY created_at DESC
		LIMIT ?
	`, strings.Join(placeholders, ", "))

	rows, err := s.store.DB().Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]webpTask, 0, limit)
	for rows.Next() {
		task, err := scanWebPTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Server) listPersistedWebPTaskLogsAfter(taskID string, afterSeq uint64) ([]webpTaskLogLine, error) {
	rows, err := s.store.DB().Query(`
		SELECT seq, level, message, created_at
		FROM webp_task_logs
		WHERE task_id = ? AND seq > ?
		ORDER BY seq ASC
	`, taskID, afterSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]webpTaskLogLine, 0, 64)
	for rows.Next() {
		var (
			line       webpTaskLogLine
			createdRaw string
		)
		if err := rows.Scan(&line.Seq, &line.Level, &line.Message, &createdRaw); err != nil {
			return nil, err
		}
		line.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw)
		if err != nil {
			return nil, err
		}
		out = append(out, line)
	}
	return out, rows.Err()
}

func (s *Server) appendPersistedWebPTaskLog(task *webpTask, level, format string, args ...any) error {
	if task == nil {
		return errors.New("task is nil")
	}
	message := strings.TrimSpace(fmt.Sprintf(format, args...))
	if message == "" {
		return nil
	}
	if level == "" {
		level = "info"
	}
	now := time.Now()
	task.LastSeq++
	task.UpdatedAt = now

	tx, err := s.store.DB().Begin()
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if _, err := tx.Exec(`
		INSERT INTO webp_task_logs (task_id, seq, level, message, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, task.ID, task.LastSeq, level, message, now.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE webp_tasks
		SET last_seq = ?, updated_at = ?
		WHERE id = ?
	`, task.LastSeq, task.UpdatedAt.UTC().Format(time.RFC3339Nano), task.ID); err != nil {
		return err
	}
	if task.LastSeq > 8000 {
		if _, err := tx.Exec(`
			DELETE FROM webp_task_logs
			WHERE task_id = ? AND seq <= ?
		`, task.ID, task.LastSeq-8000); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func scanWebPTask(scanner interface{ Scan(dest ...any) error }) (webpTask, error) {
	var (
		task        webpTask
		statusRaw   string
		summaryRaw  string
		cancelFlag  int
		createdRaw  string
		startedRaw  string
		finishedRaw string
		updatedRaw  string
	)
	if err := scanner.Scan(
		&task.ID, &task.SourceRootPath, &task.SourcePath, &task.OutputRootPath, &task.OutputDirPath,
		&task.Quality, &task.Method, &task.Concurrency, &statusRaw,
		&task.ProgressTotal, &task.ProgressDone, &task.PartialArchives, &task.PendingArchives,
		&task.CurrentArchive, &task.CurrentArchiveDone, &task.CurrentArchiveTotal, &task.CurrentPhase, &task.CurrentItem,
		&cancelFlag, &task.LastSeq, &summaryRaw, &task.Error,
		&createdRaw, &startedRaw, &finishedRaw, &updatedRaw,
	); err != nil {
		return webpTask{}, err
	}
	task.Status = webpTaskStatus(strings.TrimSpace(statusRaw))
	task.CancelRequested = webpTaskIntToBool(cancelFlag)
	if strings.TrimSpace(summaryRaw) != "" {
		if err := json.Unmarshal([]byte(summaryRaw), &task.Summary); err != nil {
			return webpTask{}, err
		}
	}
	var err error
	task.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return webpTask{}, err
	}
	task.StartedAt, err = parseOptionalTime(startedRaw)
	if err != nil {
		return webpTask{}, err
	}
	task.FinishedAt, err = parseOptionalTime(finishedRaw)
	if err != nil {
		return webpTask{}, err
	}
	task.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedRaw)
	if err != nil {
		return webpTask{}, err
	}
	return task, nil
}

func webpTaskDTO(task webpTask) map[string]any {
	out := map[string]any{
		"id":                    task.ID,
		"source_root_path":      task.SourceRootPath,
		"source_path":           task.SourcePath,
		"output_root":           task.OutputRootPath,
		"output_dir":            task.OutputDirPath,
		"webp_quality":          task.Quality,
		"webp_method":           task.Method,
		"webp_concurrency":      task.Concurrency,
		"status":                task.Status,
		"progress_total":        task.ProgressTotal,
		"progress_done":         task.ProgressDone,
		"partial_archives":      task.PartialArchives,
		"pending_archives":      task.PendingArchives,
		"current_archive":       task.CurrentArchive,
		"current_archive_done":  task.CurrentArchiveDone,
		"current_archive_total": task.CurrentArchiveTotal,
		"current_phase":         task.CurrentPhase,
		"current_item":          task.CurrentItem,
		"cancel_requested":      task.CancelRequested,
		"summary":               task.Summary,
		"error":                 task.Error,
		"last_seq":              task.LastSeq,
		"created_at":            task.CreatedAt,
	}
	if !task.StartedAt.IsZero() {
		out["started_at"] = task.StartedAt
	} else {
		out["started_at"] = nil
	}
	if !task.FinishedAt.IsZero() {
		out["finished_at"] = task.FinishedAt
	} else {
		out["finished_at"] = nil
	}
	if !task.UpdatedAt.IsZero() {
		out["updated_at"] = task.UpdatedAt
	} else {
		out["updated_at"] = nil
	}
	return out
}

func formatOptionalTime(v time.Time) string {
	if v.IsZero() {
		return ""
	}
	return v.UTC().Format(time.RFC3339Nano)
}

func parseOptionalTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, raw)
}

func webpTaskBoolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func webpTaskIntToBool(v int) bool {
	return v != 0
}

func parseBoolQueryParam(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
