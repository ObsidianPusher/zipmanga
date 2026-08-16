package server

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"zipmanga/internal/db"
)

type archiveTaskStatus string

const (
	archiveTaskQueued    archiveTaskStatus = "queued"
	archiveTaskRunning   archiveTaskStatus = "running"
	archiveTaskSucceeded archiveTaskStatus = "succeeded"
	archiveTaskFailed    archiveTaskStatus = "failed"
)

type archiveTaskLogLine struct {
	Seq       uint64    `json:"seq"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type archiveWorkflowTask struct {
	ID            string            `json:"id"`
	Action        string            `json:"action"`
	MountID       int64             `json:"mount_id"`
	MountName     string            `json:"mount_name"`
	Status        archiveTaskStatus `json:"status"`
	SourcePath    string            `json:"source_path,omitempty"`
	OutputRoot    string            `json:"output_root,omitempty"`
	WebPQuality   int64             `json:"webp_quality,omitempty"`
	WebPMethod    int64             `json:"webp_method,omitempty"`
	ProgressTotal int               `json:"progress_total"`
	ProgressDone  int               `json:"progress_done"`
	CurrentItem   string            `json:"current_item,omitempty"`
	CurrentPhase  string            `json:"current_phase,omitempty"`

	CreatedAt  time.Time `json:"created_at"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`

	Summary archiveWorkflowStats        `json:"summary"`
	Roots   []archiveWorkflowRootResult `json:"roots"`
	Logs    []archiveTaskLogLine        `json:"logs"`
	LastSeq uint64                      `json:"last_seq"`
	Error   string                      `json:"error,omitempty"`
}

func (s *Server) handleAPIAdminArchiveWorkflowCreateTask(w http.ResponseWriter, r *http.Request) {
	req, err := s.decodeArchiveTaskCreateRequest(r)
	if err != nil {
		if strings.HasPrefix(err.Error(), "internal:") {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": strings.TrimPrefix(err.Error(), "internal: ")})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	task := s.createArchiveTask(req)
	go s.runArchiveTask(task.ID, req)

	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":   true,
		"task": archiveTaskBriefDTO(task),
	})
}

func (s *Server) handleAPIAdminArchiveWorkflowListTasks(w http.ResponseWriter, r *http.Request) {
	limit := parseBoundedInt(strings.TrimSpace(r.URL.Query().Get("limit")), 30, 1, 200)
	items := s.listArchiveTaskBrief(limit)
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
	})
}

func (s *Server) handleAPIAdminArchiveWorkflowGetTask(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid task id"})
		return
	}
	afterSeq, _ := strconv.ParseUint(strings.TrimSpace(r.URL.Query().Get("after_seq")), 10, 64)
	task, logs, ok := s.getArchiveTaskSnapshot(id, afterSeq)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "task not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"task": task,
		"logs": logs,
	})
}

type archiveTaskCreateRequest struct {
	Action      string
	Mount       db.Mount
	Password    string
	SourcePath  string
	OutputRoot  string
	WebPQuality int64
	WebPMethod  int64
}

func (s *Server) decodeArchiveTaskCreateRequest(r *http.Request) (archiveTaskCreateRequest, error) {
	var req struct {
		Action      string `json:"action"`
		MountID     int64  `json:"mount_id"`
		Password    string `json:"password"`
		SourcePath  string `json:"source_path"`
		OutputRoot  string `json:"output_root"`
		WebPQuality *int64 `json:"webp_quality"`
		WebPMethod  *int64 `json:"webp_method"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return archiveTaskCreateRequest{}, err
	}

	action := strings.ToLower(strings.TrimSpace(req.Action))
	switch action {
	case archiveActionCollect, archiveActionExtract, archiveActionOrganize, archiveActionRepack:
	default:
		return archiveTaskCreateRequest{}, errors.New("unsupported action")
	}
	if req.MountID <= 0 {
		return archiveTaskCreateRequest{}, errors.New("mount_id must be positive")
	}

	mount, err := s.store.GetMount(req.MountID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return archiveTaskCreateRequest{}, errors.New("mount not found")
		}
		return archiveTaskCreateRequest{}, fmt.Errorf("internal: query mount failed: %w", err)
	}
	if mount.Type != db.MountTypeLocal {
		return archiveTaskCreateRequest{}, errors.New("archive workflow only supports local mounts")
	}
	out := archiveTaskCreateRequest{
		Action:      action,
		Mount:       mount,
		Password:    req.Password,
		SourcePath:  strings.TrimSpace(req.SourcePath),
		OutputRoot:  strings.TrimSpace(req.OutputRoot),
		WebPQuality: s.currentRuntimeConfig().WebPQuality,
		WebPMethod:  s.currentRuntimeConfig().WebPMethod,
	}
	if req.WebPQuality != nil {
		out.WebPQuality = *req.WebPQuality
	}
	if req.WebPMethod != nil {
		out.WebPMethod = *req.WebPMethod
	}
	return out, nil
}

func (s *Server) createArchiveTask(req archiveTaskCreateRequest) archiveWorkflowTask {
	s.archiveTaskMu.Lock()
	defer s.archiveTaskMu.Unlock()

	s.archiveTaskSeq++
	id := fmt.Sprintf("awt_%d_%04d", time.Now().Unix(), s.archiveTaskSeq%10000)
	now := time.Now()
	task := archiveWorkflowTask{
		ID:          id,
		Action:      req.Action,
		MountID:     req.Mount.ID,
		MountName:   req.Mount.Name,
		Status:      archiveTaskQueued,
		SourcePath:  req.SourcePath,
		OutputRoot:  req.OutputRoot,
		WebPQuality: req.WebPQuality,
		WebPMethod:  req.WebPMethod,
		CreatedAt:   now,
		Roots:       make([]archiveWorkflowRootResult, 0, 4),
		Logs:        make([]archiveTaskLogLine, 0, 128),
	}
	s.archiveTasks[id] = &task
	s.archiveTaskOrder = append(s.archiveTaskOrder, id)
	s.pruneArchiveTasksLocked()
	return task
}

func (s *Server) runArchiveTask(taskID string, req archiveTaskCreateRequest) {
	mount := req.Mount
	maskPassword := func(v string) string {
		if strings.TrimSpace(v) == "" {
			return "(empty)"
		}
		return "******"
	}

	s.setArchiveTaskRunning(taskID)
	s.appendArchiveTaskLog(taskID, "info", "$ archive-workflow %s --mount %d --password %s", s.archiveTaskAction(taskID), mount.ID, maskPassword(req.Password))

	roots := mount.RootPaths
	if len(roots) == 0 && strings.TrimSpace(mount.RootPath) != "" {
		roots = []string{mount.RootPath}
	}
	roots = uniqueStrings(roots)
	if len(roots) == 0 {
		s.finishArchiveTask(taskID, archiveTaskFailed, archiveWorkflowStats{Failed: 1}, nil, "mount has no root paths")
		return
	}

	summary := archiveWorkflowStats{}
	results := make([]archiveWorkflowRootResult, 0, len(roots))
	taskAction := s.archiveTaskAction(taskID)
	for _, root := range roots {
		normalized, err := canonicalizeLocalRootPath(root)
		if err != nil {
			msg := fmt.Sprintf("root=%s invalid: %v", root, err)
			s.appendArchiveTaskLog(taskID, "error", msg)
			res := archiveWorkflowRootResult{
				RootPath: root,
				Stats: archiveWorkflowStats{
					Failed: 1,
				},
				Errors: []string{msg},
			}
			results = append(results, res)
			addArchiveStats(&summary, res.Stats)
			continue
		}

		s.appendArchiveTaskLog(taskID, "info", "$ run %s --root %q", taskAction, normalized)
		stats, errs, runErr := runArchiveWorkflowOnRoot(taskAction, normalized, req.Password)
		if runErr != nil {
			errs = append(errs, runErr.Error())
			stats.Failed++
		}
		sort.Strings(errs)
		results = append(results, archiveWorkflowRootResult{
			RootPath: normalized,
			Stats:    stats,
			Errors:   errs,
		})
		addArchiveStats(&summary, stats)

		s.appendArchiveTaskLog(taskID, "info", "root=%q done: scanned=%d matched=%d moved=%d extracted=%d kept=%d deleted=%d zipped=%d skipped=%d failed=%d",
			normalized, stats.Scanned, stats.Matched, stats.Moved, stats.Extracted, stats.Kept, stats.Deleted, stats.Zipped, stats.Skipped, stats.Failed)
		for _, item := range errs {
			s.appendArchiveTaskLog(taskID, "error", "%s", item)
		}
	}

	finalStatus := archiveTaskSucceeded
	finalError := ""
	if summary.Failed > 0 {
		finalStatus = archiveTaskFailed
		finalError = fmt.Sprintf("workflow finished with %d failures", summary.Failed)
	}
	s.finishArchiveTask(taskID, finalStatus, summary, results, finalError)
}

func (s *Server) setArchiveTaskRunning(taskID string) {
	s.archiveTaskMu.Lock()
	defer s.archiveTaskMu.Unlock()
	task, ok := s.archiveTasks[taskID]
	if !ok {
		return
	}
	task.Status = archiveTaskRunning
	task.StartedAt = time.Now()
	task.FinishedAt = time.Time{}
}

func (s *Server) finishArchiveTask(taskID string, status archiveTaskStatus, summary archiveWorkflowStats, roots []archiveWorkflowRootResult, finalErr string) {
	s.archiveTaskMu.Lock()
	defer s.archiveTaskMu.Unlock()
	task, ok := s.archiveTasks[taskID]
	if !ok {
		return
	}
	task.Status = status
	task.Summary = summary
	task.Error = strings.TrimSpace(finalErr)
	task.FinishedAt = time.Now()
	task.CurrentPhase = ""
	task.Roots = append([]archiveWorkflowRootResult(nil), roots...)
}

func (s *Server) updateArchiveTaskProgress(taskID string, total, done int, currentItem, currentPhase string, summary archiveWorkflowStats) {
	s.archiveTaskMu.Lock()
	defer s.archiveTaskMu.Unlock()
	task, ok := s.archiveTasks[taskID]
	if !ok {
		return
	}
	if total >= 0 {
		task.ProgressTotal = total
	}
	if done >= 0 {
		task.ProgressDone = done
	}
	task.CurrentItem = strings.TrimSpace(currentItem)
	task.CurrentPhase = strings.TrimSpace(currentPhase)
	task.Summary = summary
}

func (s *Server) appendArchiveTaskLog(taskID, level, format string, args ...any) {
	message := strings.TrimSpace(fmt.Sprintf(format, args...))
	if message == "" {
		return
	}
	if level == "" {
		level = "info"
	}

	s.archiveTaskMu.Lock()
	defer s.archiveTaskMu.Unlock()
	task, ok := s.archiveTasks[taskID]
	if !ok {
		return
	}
	task.LastSeq++
	task.Logs = append(task.Logs, archiveTaskLogLine{
		Seq:       task.LastSeq,
		Level:     level,
		Message:   message,
		CreatedAt: time.Now(),
	})
	if len(task.Logs) > 8000 {
		task.Logs = append([]archiveTaskLogLine(nil), task.Logs[len(task.Logs)-8000:]...)
	}
}

func (s *Server) archiveTaskAction(taskID string) string {
	s.archiveTaskMu.RLock()
	defer s.archiveTaskMu.RUnlock()
	task, ok := s.archiveTasks[taskID]
	if !ok {
		return ""
	}
	return task.Action
}

func (s *Server) listArchiveTaskBrief(limit int) []map[string]any {
	s.archiveTaskMu.RLock()
	defer s.archiveTaskMu.RUnlock()

	if limit <= 0 {
		limit = 30
	}
	out := make([]map[string]any, 0, limit)
	for i := len(s.archiveTaskOrder) - 1; i >= 0 && len(out) < limit; i-- {
		id := s.archiveTaskOrder[i]
		task, ok := s.archiveTasks[id]
		if !ok {
			continue
		}
		out = append(out, archiveTaskBriefDTO(*task))
	}
	return out
}

func archiveTaskBriefDTO(task archiveWorkflowTask) map[string]any {
	return map[string]any{
		"id":             task.ID,
		"action":         task.Action,
		"mount_id":       task.MountID,
		"mount_name":     task.MountName,
		"source_path":    task.SourcePath,
		"output_root":    task.OutputRoot,
		"webp_quality":   task.WebPQuality,
		"webp_method":    task.WebPMethod,
		"status":         task.Status,
		"created_at":     task.CreatedAt,
		"started_at":     task.StartedAt,
		"finished_at":    task.FinishedAt,
		"progress_total": task.ProgressTotal,
		"progress_done":  task.ProgressDone,
		"current_item":   task.CurrentItem,
		"current_phase":  task.CurrentPhase,
		"summary":        task.Summary,
		"error":          task.Error,
		"last_seq":       task.LastSeq,
	}
}

func (s *Server) getArchiveTaskSnapshot(taskID string, afterSeq uint64) (map[string]any, []archiveTaskLogLine, bool) {
	s.archiveTaskMu.RLock()
	defer s.archiveTaskMu.RUnlock()

	task, ok := s.archiveTasks[taskID]
	if !ok {
		return nil, nil, false
	}
	logs := make([]archiveTaskLogLine, 0)
	if afterSeq == 0 {
		logs = append(logs, task.Logs...)
	} else {
		for _, line := range task.Logs {
			if line.Seq > afterSeq {
				logs = append(logs, line)
			}
		}
	}

	taskDTO := map[string]any{
		"id":             task.ID,
		"action":         task.Action,
		"mount_id":       task.MountID,
		"mount_name":     task.MountName,
		"source_path":    task.SourcePath,
		"output_root":    task.OutputRoot,
		"webp_quality":   task.WebPQuality,
		"webp_method":    task.WebPMethod,
		"status":         task.Status,
		"created_at":     task.CreatedAt,
		"started_at":     task.StartedAt,
		"finished_at":    task.FinishedAt,
		"progress_total": task.ProgressTotal,
		"progress_done":  task.ProgressDone,
		"current_item":   task.CurrentItem,
		"current_phase":  task.CurrentPhase,
		"summary":        task.Summary,
		"roots":          append([]archiveWorkflowRootResult(nil), task.Roots...),
		"error":          task.Error,
		"last_seq":       task.LastSeq,
	}
	return taskDTO, logs, true
}

func (s *Server) pruneArchiveTasksLocked() {
	if s.archiveTaskLimit <= 0 {
		s.archiveTaskLimit = 120
	}
	if len(s.archiveTaskOrder) <= s.archiveTaskLimit {
		return
	}

	next := make([]string, 0, len(s.archiveTaskOrder))
	for _, id := range s.archiveTaskOrder {
		task, ok := s.archiveTasks[id]
		if !ok {
			continue
		}
		if len(next) >= s.archiveTaskLimit && task.Status != archiveTaskRunning && task.Status != archiveTaskQueued {
			delete(s.archiveTasks, id)
			continue
		}
		next = append(next, id)
	}
	if len(next) > s.archiveTaskLimit {
		trimmed := next[len(next)-s.archiveTaskLimit:]
		keep := make(map[string]struct{}, len(trimmed))
		for _, id := range trimmed {
			keep[id] = struct{}{}
		}
		for id := range s.archiveTasks {
			if _, ok := keep[id]; !ok {
				delete(s.archiveTasks, id)
			}
		}
		next = trimmed
	}
	s.archiveTaskOrder = next
}
