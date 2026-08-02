package server

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"zipmanga/internal/db"
)

const (
	nameFormatPreviewTTL = 2 * time.Hour
)

type nameFormatPlanItem struct {
	RootPath string `json:"root_path"`
	OldAbs   string `json:"old_abs"`
	NewAbs   string `json:"new_abs"`
	OldName  string `json:"old_name"`
	NewName  string `json:"new_name"`
	IsDir    bool   `json:"is_dir"`
}

type nameFormatPreview struct {
	ID        string
	MountID   int64
	MountName string
	CreatedAt time.Time
	Items     []nameFormatPlanItem
	Errors    []string
}

func (s *Server) handleAPIAdminNameFormatPreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MountID     int64 `json:"mount_id"`
		SampleLimit int   `json:"sample_limit"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if req.MountID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "mount_id must be positive"})
		return
	}

	mount, err := s.store.GetMount(req.MountID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "mount not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if mount.Type != db.MountTypeLocal {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "name format only supports local mounts"})
		return
	}

	roots := mount.RootPaths
	if len(roots) == 0 && strings.TrimSpace(mount.RootPath) != "" {
		roots = []string{mount.RootPath}
	}
	roots = uniqueStrings(roots)
	if len(roots) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "mount has no root paths"})
		return
	}

	allItems := make([]nameFormatPlanItem, 0, 1024)
	allErrs := make([]string, 0, 64)
	for _, root := range roots {
		normalized, err := canonicalizeLocalRootPath(root)
		if err != nil {
			allErrs = append(allErrs, fmt.Sprintf("root %s invalid: %v", root, err))
			continue
		}
		items, errs, planErr := buildNameFormatPlanForRoot(normalized)
		allItems = append(allItems, items...)
		allErrs = append(allErrs, errs...)
		if planErr != nil {
			allErrs = append(allErrs, fmt.Sprintf("scan root %s failed: %v", normalized, planErr))
		}
	}
	sort.SliceStable(allItems, func(i, j int) bool {
		if allItems[i].RootPath == allItems[j].RootPath {
			return allItems[i].OldAbs < allItems[j].OldAbs
		}
		return allItems[i].RootPath < allItems[j].RootPath
	})
	sort.Strings(allErrs)

	preview := s.createNameFormatPreview(mount, allItems, allErrs)
	sampleLimit := req.SampleLimit
	if sampleLimit <= 0 {
		sampleLimit = 300
	}
	if sampleLimit > 3000 {
		sampleLimit = 3000
	}
	sample, truncated := previewDiffSample(preview.Items, sampleLimit)
	resp := map[string]any{
		"ok":            true,
		"preview_id":    preview.ID,
		"mount_id":      preview.MountID,
		"mount_name":    preview.MountName,
		"created_at":    preview.CreatedAt,
		"total_changes": len(preview.Items),
		"sample_items":  sample,
		"truncated":     truncated,
		"errors":        preview.Errors,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAPIAdminNameFormatApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PreviewID string `json:"preview_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	previewID := strings.TrimSpace(req.PreviewID)
	if previewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "preview_id is required"})
		return
	}

	preview, ok := s.popNameFormatPreview(previewID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "preview not found or expired"})
		return
	}

	mount, err := s.store.GetMount(preview.MountID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "mount not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if mount.Type != db.MountTypeLocal {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "name format only supports local mounts"})
		return
	}

	task := s.createArchiveTask(archiveActionFormatNames, mount)
	go s.runNameFormatTask(task.ID, previewID, preview)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":         true,
		"preview_id": previewID,
		"task":       archiveTaskBriefDTO(task),
	})
}

func (s *Server) runNameFormatTask(taskID, previewID string, preview *nameFormatPreview) {
	if preview == nil {
		s.finishArchiveTask(taskID, archiveTaskFailed, archiveWorkflowStats{Failed: 1}, nil, "preview is nil")
		return
	}
	s.setArchiveTaskRunning(taskID)
	s.appendArchiveTaskLog(taskID, "info", "$ name-format apply --preview %s --changes %d", previewID, len(preview.Items))
	for _, msg := range preview.Errors {
		s.appendArchiveTaskLog(taskID, "warn", msg)
	}

	items := append([]nameFormatPlanItem(nil), preview.Items...)
	sort.SliceStable(items, func(i, j int) bool {
		di := pathDepth(items[i].OldAbs)
		dj := pathDepth(items[j].OldAbs)
		if di != dj {
			return di > dj
		}
		if items[i].IsDir != items[j].IsDir {
			return !items[i].IsDir
		}
		return items[i].OldAbs < items[j].OldAbs
	})

	summary := archiveWorkflowStats{
		Matched: len(items),
	}
	rootMap := make(map[string]*archiveWorkflowRootResult, 8)
	getRoot := func(root string) *archiveWorkflowRootResult {
		key := filepath.Clean(root)
		if item, ok := rootMap[key]; ok {
			return item
		}
		next := &archiveWorkflowRootResult{
			RootPath: key,
			Stats:    archiveWorkflowStats{},
			Errors:   make([]string, 0, 8),
		}
		rootMap[key] = next
		return next
	}
	for _, item := range items {
		root := getRoot(item.RootPath)
		summary.Scanned++
		root.Stats.Scanned++
		root.Stats.Matched++

		oldPath := filepath.Clean(item.OldAbs)
		target := filepath.Clean(item.NewAbs)
		if samePath(oldPath, target) {
			summary.Skipped++
			root.Stats.Skipped++
			continue
		}

		exists, err := pathExists(oldPath)
		if err != nil {
			summary.Failed++
			root.Stats.Failed++
			msg := fmt.Sprintf("stat %s failed: %v", oldPath, err)
			root.Errors = append(root.Errors, msg)
			s.appendArchiveTaskLog(taskID, "error", msg)
			continue
		}
		if !exists {
			summary.Skipped++
			root.Stats.Skipped++
			msg := fmt.Sprintf("skip missing source: %s", oldPath)
			root.Errors = append(root.Errors, msg)
			s.appendArchiveTaskLog(taskID, "warn", msg)
			continue
		}

		finalTarget := target
		targetExists, err := pathExists(finalTarget)
		if err != nil {
			summary.Failed++
			root.Stats.Failed++
			msg := fmt.Sprintf("check target %s failed: %v", finalTarget, err)
			root.Errors = append(root.Errors, msg)
			s.appendArchiveTaskLog(taskID, "error", msg)
			continue
		}
		if targetExists && !samePath(oldPath, finalTarget) {
			if item.IsDir {
				finalTarget, err = uniqueDirPath(filepath.Dir(finalTarget), filepath.Base(finalTarget))
			} else {
				finalTarget, err = uniquePathWithSuffix(filepath.Dir(finalTarget), filepath.Base(finalTarget))
			}
			if err != nil {
				summary.Failed++
				root.Stats.Failed++
				msg := fmt.Sprintf("resolve conflict for %s failed: %v", oldPath, err)
				root.Errors = append(root.Errors, msg)
				s.appendArchiveTaskLog(taskID, "error", msg)
				continue
			}
		}

		if err := os.MkdirAll(filepath.Dir(finalTarget), 0o755); err != nil {
			summary.Failed++
			root.Stats.Failed++
			msg := fmt.Sprintf("mkdir parent for %s failed: %v", finalTarget, err)
			root.Errors = append(root.Errors, msg)
			s.appendArchiveTaskLog(taskID, "error", msg)
			continue
		}

		err = os.Rename(oldPath, finalTarget)
		if err != nil && !item.IsDir {
			err = moveFile(oldPath, finalTarget)
		}
		if err != nil {
			summary.Failed++
			root.Stats.Failed++
			msg := fmt.Sprintf("rename %s -> %s failed: %v", oldPath, finalTarget, err)
			root.Errors = append(root.Errors, msg)
			s.appendArchiveTaskLog(taskID, "error", msg)
			continue
		}

		summary.Moved++
		root.Stats.Moved++
		s.appendArchiveTaskLog(taskID, "info", "rename: %s -> %s", oldPath, finalTarget)
	}

	roots := make([]archiveWorkflowRootResult, 0, len(rootMap))
	for _, root := range rootMap {
		sort.Strings(root.Errors)
		roots = append(roots, *root)
	}
	sort.SliceStable(roots, func(i, j int) bool { return roots[i].RootPath < roots[j].RootPath })

	status := archiveTaskSucceeded
	finalErr := ""
	if summary.Failed > 0 {
		status = archiveTaskFailed
		finalErr = fmt.Sprintf("name format completed with %d failures", summary.Failed)
	}
	s.finishArchiveTask(taskID, status, summary, roots, finalErr)
}

func buildNameFormatPlanForRoot(rootPath string) ([]nameFormatPlanItem, []string, error) {
	items := make([]nameFormatPlanItem, 0, 512)
	errs := make([]string, 0, 16)

	var walkDir func(string) error
	walkDir = func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		occupied := make(map[string]struct{}, len(entries))
		for _, ent := range entries {
			occupied[ent.Name()] = struct{}{}
		}

		for _, ent := range entries {
			if ent.Type()&os.ModeSymlink != 0 {
				continue
			}
			oldName := ent.Name()
			newName := normalizeUTF8Filename(oldName, ent.IsDir())
			if newName != oldName {
				delete(occupied, oldName)
				candidate := uniqueNormalizedName(newName, occupied, ent.IsDir())
				occupied[candidate] = struct{}{}
				items = append(items, nameFormatPlanItem{
					RootPath: rootPath,
					OldAbs:   filepath.Join(dir, oldName),
					NewAbs:   filepath.Join(dir, candidate),
					OldName:  oldName,
					NewName:  candidate,
					IsDir:    ent.IsDir(),
				})
			}
		}

		for _, ent := range entries {
			if !ent.IsDir() || ent.Type()&os.ModeSymlink != 0 {
				continue
			}
			if err := walkDir(filepath.Join(dir, ent.Name())); err != nil {
				errs = append(errs, fmt.Sprintf("walk %s failed: %v", filepath.Join(dir, ent.Name()), err))
			}
		}
		return nil
	}

	err := walkDir(rootPath)
	sort.Strings(errs)
	return items, errs, err
}

func normalizeUTF8Filename(name string, isDir bool) string {
	clean := string(bytes.ToValidUTF8([]byte(name), nil))
	clean = strings.ReplaceAll(clean, "\uFFFD", "")
	clean = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\':
			return -1
		case r < 32 || r == 127:
			return -1
		case unicode.Is(unicode.Cf, r):
			return -1
		case strings.ContainsRune(`<>:"|?*`, r):
			return -1
		case unicode.IsSpace(r):
			return ' '
		default:
			return r
		}
	}, clean)
	clean = strings.Join(strings.Fields(clean), " ")
	clean = strings.TrimSpace(clean)

	if clean == "" {
		if isDir {
			return "unnamed-dir"
		}
		return "unnamed"
	}

	if isDir {
		return truncateNameRunes(clean, 160)
	}

	ext := filepath.Ext(clean)
	stem := strings.TrimSpace(strings.TrimSuffix(clean, ext))
	if stem == "" {
		stem = "unnamed"
	}
	if ext == "." {
		ext = ""
	}
	normalized := stem + ext
	return truncateFileNameWithExt(normalized, 180)
}

func truncateNameRunes(v string, maxRunes int) string {
	if maxRunes <= 0 {
		return v
	}
	r := []rune(v)
	if len(r) <= maxRunes {
		return v
	}
	return strings.TrimSpace(string(r[:maxRunes]))
}

func truncateFileNameWithExt(name string, maxRunes int) string {
	if maxRunes <= 0 {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	stemRunes := []rune(stem)
	extRunes := []rune(ext)
	if len(stemRunes)+len(extRunes) <= maxRunes {
		return strings.TrimSpace(name)
	}
	if len(extRunes) >= maxRunes {
		return string(extRunes[:maxRunes])
	}
	maxStem := maxRunes - len(extRunes)
	if maxStem < 1 {
		maxStem = 1
	}
	return strings.TrimSpace(string(stemRunes[:maxStem]) + ext)
}

func uniqueNormalizedName(base string, occupied map[string]struct{}, isDir bool) string {
	candidate := base
	if _, ok := occupied[candidate]; !ok {
		return candidate
	}
	ext := ""
	stem := base
	if !isDir {
		ext = filepath.Ext(base)
		stem = strings.TrimSuffix(base, ext)
		if stem == "" {
			stem = "unnamed"
		}
	}
	for i := 1; i <= 100000; i++ {
		next := fmt.Sprintf("%s__u8_%d%s", stem, i, ext)
		if _, ok := occupied[next]; !ok {
			return next
		}
	}
	return fmt.Sprintf("%s__u8_%d%s", stem, time.Now().UnixNano(), ext)
}

func previewDiffSample(items []nameFormatPlanItem, limit int) ([]map[string]any, bool) {
	if limit <= 0 {
		limit = 300
	}
	out := make([]map[string]any, 0, limit)
	total := len(items)
	for i := 0; i < total && i < limit; i++ {
		item := items[i]
		out = append(out, map[string]any{
			"root_path": item.RootPath,
			"old_path":  item.OldAbs,
			"new_path":  item.NewAbs,
			"old_name":  item.OldName,
			"new_name":  item.NewName,
			"is_dir":    item.IsDir,
		})
	}
	return out, total > limit
}

func (s *Server) createNameFormatPreview(mount db.Mount, items []nameFormatPlanItem, errs []string) *nameFormatPreview {
	s.nameFormatMu.Lock()
	defer s.nameFormatMu.Unlock()

	s.nameFormatSeq++
	id := fmt.Sprintf("nfp_%d_%04d", time.Now().Unix(), s.nameFormatSeq%10000)
	p := &nameFormatPreview{
		ID:        id,
		MountID:   mount.ID,
		MountName: mount.Name,
		CreatedAt: time.Now(),
		Items:     append([]nameFormatPlanItem(nil), items...),
		Errors:    append([]string(nil), errs...),
	}
	s.nameFormatPreviews[id] = p
	s.pruneNameFormatPreviewsLocked()
	return p
}

func (s *Server) popNameFormatPreview(id string) (*nameFormatPreview, bool) {
	s.nameFormatMu.Lock()
	defer s.nameFormatMu.Unlock()
	p, ok := s.nameFormatPreviews[id]
	if !ok || p == nil {
		return nil, false
	}
	delete(s.nameFormatPreviews, id)
	now := time.Now()
	if now.Sub(p.CreatedAt) > nameFormatPreviewTTL {
		return nil, false
	}
	out := &nameFormatPreview{
		ID:        p.ID,
		MountID:   p.MountID,
		MountName: p.MountName,
		CreatedAt: p.CreatedAt,
		Items:     append([]nameFormatPlanItem(nil), p.Items...),
		Errors:    append([]string(nil), p.Errors...),
	}
	return out, true
}

func (s *Server) pruneNameFormatPreviewsLocked() {
	if s.nameFormatLimit <= 0 {
		s.nameFormatLimit = 64
	}
	now := time.Now()
	type pair struct {
		id string
		ts time.Time
	}
	all := make([]pair, 0, len(s.nameFormatPreviews))
	for id, p := range s.nameFormatPreviews {
		if p == nil || now.Sub(p.CreatedAt) > nameFormatPreviewTTL {
			delete(s.nameFormatPreviews, id)
			continue
		}
		all = append(all, pair{id: id, ts: p.CreatedAt})
	}
	if len(all) <= s.nameFormatLimit {
		return
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].ts.Before(all[j].ts) })
	needDrop := len(all) - s.nameFormatLimit
	for i := 0; i < needDrop; i++ {
		delete(s.nameFormatPreviews, all[i].id)
	}
}

func pathDepth(p string) int {
	clean := filepath.Clean(p)
	if clean == string(filepath.Separator) {
		return 0
	}
	return strings.Count(clean, string(filepath.Separator))
}
