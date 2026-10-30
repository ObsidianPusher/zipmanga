package server

import (
	"archive/zip"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"zipmanga/internal/db"
	"zipmanga/internal/imgcodec"
	"zipmanga/internal/ziputil"

	"github.com/nwaples/rardecode"
)

var errWebPTaskCanceled = errors.New("webp task canceled")

type webpConvertArchiveJob struct {
	SourcePath string
	RelPath    string
	FinalZip   string
	WorkDir    string
}

type webpArchiveEntry struct {
	Index        int
	SourceRel    string
	TargetRel    string
	IsWebP       bool
	KeepOriginal bool
}

type webpArchiveWriteResult struct {
	Entry webpArchiveEntry
	Data  []byte
	Err   error
}

func (s *Server) executeWebPTask(task *webpTask) (webpTaskStatus, string) {
	if task == nil {
		return webpTaskFailed, "task is nil"
	}
	stateGuard := &webpTaskStateGuard{}

	sourcePath, err := canonicalizeLocalRootPath(task.SourcePath)
	if err != nil {
		return webpTaskFailed, fmt.Sprintf("invalid source path: %v", err)
	}
	outputRoot, err := canonicalizeLocalRootPath(task.OutputRootPath)
	if err != nil {
		return webpTaskFailed, fmt.Sprintf("invalid output root: %v", err)
	}
	task.SourcePath = sourcePath
	task.OutputRootPath = outputRoot
	task.OutputDirPath = filepath.Join(outputRoot, sanitizeDirectoryName(filepath.Base(sourcePath))+"-webp")

	if err := ensureDirExists(sourcePath); err != nil {
		return webpTaskFailed, fmt.Sprintf("source path is not accessible: %v", err)
	}
	if err := ensureDirExists(outputRoot); err != nil {
		return webpTaskFailed, fmt.Sprintf("output root is not accessible: %v", err)
	}
	if err := os.MkdirAll(task.OutputDirPath, 0o755); err != nil {
		return webpTaskFailed, fmt.Sprintf("create output dir failed: %v", err)
	}

	task.CurrentArchive = ""
	task.CurrentItem = sourcePath
	task.CurrentPhase = "扫描源目录"
	task.UpdatedAt = timeNow()
	if err := s.persistWebPTask(task); err != nil && s.logger != nil {
		s.logger.Printf("persist scan start for webp task %s failed: %v", task.ID, err)
	}
	jobs, stats, scanErr := scanWebPArchiveJobs(sourcePath, task.OutputDirPath, func() bool {
		return s.isWebPTaskCancelRequested(task.ID)
	}, func(current archiveWorkflowStats, currentPath string) {
		_ = stateGuard.update(func() error {
			task.Summary = current
			task.CurrentArchive = ""
			task.CurrentItem = currentPath
			task.CurrentPhase = fmt.Sprintf("扫描源目录: 已扫描 %d 个文件，命中 %d 个压缩包", current.Scanned, current.Matched)
			task.UpdatedAt = timeNow()
			return s.persistWebPTask(task)
		}, false, nil)
	})
	task.Summary = stats
	task.ProgressTotal = len(jobs)
	task.ProgressDone = 0
	task.CurrentArchive = ""
	task.CurrentItem = ""
	task.CurrentPhase = "扫描完成"
	task.UpdatedAt = timeNow()
	if err := s.persistWebPTask(task); err != nil && s.logger != nil {
		s.logger.Printf("persist scan result for webp task %s failed: %v", task.ID, err)
	}
	if scanErr != nil {
		_ = s.appendPersistedWebPTaskLog(task, "error", "scan source %q failed: %v", sourcePath, scanErr)
	}
	if len(jobs) == 0 {
		if scanErr != nil {
			return webpTaskFailed, scanErr.Error()
		}
		return webpTaskSucceeded, ""
	}
	_ = s.appendPersistedWebPTaskLog(task, "info", "扫描完成: source=%q output=%q scanned=%d matched=%d", sourcePath, task.OutputDirPath, stats.Scanned, stats.Matched)

	pendingJobs := make([]webpConvertArchiveJob, 0, len(jobs))
	doneCount := 0
	for idx, job := range jobs {
		task.CurrentArchive = job.RelPath
		task.CurrentItem = job.RelPath
		task.CurrentPhase = fmt.Sprintf("校验已有输出 %d/%d", idx+1, len(jobs))
		task.UpdatedAt = timeNow()
		if err := s.persistWebPTask(task); err != nil && s.logger != nil {
			s.logger.Printf("persist preflight state for webp task %s failed: %v", task.ID, err)
		}

		manifest, complete, manifestErr := s.matchCompletedWebPArchiveManifest(job, task.Quality, task.Method)
		if manifestErr != nil {
			_ = s.appendPersistedWebPTaskLog(task, "warn", "检查完成清单失败，准备重新处理: %s -> %s: %v", job.SourcePath, job.FinalZip, manifestErr)
		}
		if complete {
			doneCount++
			stats.Moved++
			stats.Zipped++
			stats.Skipped++
			stats.Kept += manifest.KeptEntries
			stats.Extracted += manifest.ConvertedEntries
			_ = os.RemoveAll(job.WorkDir)
			continue
		}
		pendingJobs = append(pendingJobs, job)
	}

	task.ProgressDone = doneCount
	task.Summary = stats
	task.CurrentPhase = fmt.Sprintf("等待处理 %d/%d", doneCount, len(jobs))
	task.UpdatedAt = timeNow()
	if err := s.persistWebPTask(task); err != nil && s.logger != nil {
		s.logger.Printf("persist ready state for webp task %s failed: %v", task.ID, err)
	}

	firstFailure := scanErr
	for idx, job := range pendingJobs {
		if s.isWebPTaskCancelRequested(task.ID) {
			task.CancelRequested = true
			return webpTaskCanceled, "task canceled by user"
		}
		task.CurrentArchive = job.RelPath
		task.CurrentItem = job.RelPath
		task.CurrentPhase = fmt.Sprintf("处理压缩包 %d/%d", doneCount+1, len(jobs))
		task.UpdatedAt = timeNow()
		if err := s.persistWebPTask(task); err != nil && s.logger != nil {
			s.logger.Printf("persist current archive for webp task %s failed: %v", task.ID, err)
		}
		_ = s.appendPersistedWebPTaskLog(task, "info", "开始处理: %s", job.SourcePath)

		archiveStats, convErr := s.convertArchiveJobToWebP(task, job, stateGuard)
		stats.Extracted += archiveStats.Extracted
		stats.Kept += archiveStats.Kept
		if convErr != nil {
			if errors.Is(convErr, errWebPTaskCanceled) {
				task.CancelRequested = true
				task.Summary = stats
				return webpTaskCanceled, "task canceled by user"
			}
			stats.Failed++
			if firstFailure == nil {
				firstFailure = fmt.Errorf("%s: %w", job.RelPath, convErr)
			}
			task.Error = firstFailure.Error()
			task.Summary = stats
			task.UpdatedAt = timeNow()
			_ = s.persistWebPTask(task)
			_ = s.appendPersistedWebPTaskLog(task, "error", "处理失败: %s: %v", job.SourcePath, convErr)
			continue
		}

		doneCount++
		stats.Moved++
		stats.Zipped++
		task.ProgressDone = doneCount
		task.Summary = stats
		task.CurrentPhase = fmt.Sprintf("处理压缩包 %d/%d", doneCount, len(jobs))
		task.UpdatedAt = timeNow()
		if err := s.persistWebPTask(task); err != nil && s.logger != nil {
			s.logger.Printf("persist archive done for webp task %s failed: %v", task.ID, err)
		}
		_ = s.appendPersistedWebPTaskLog(task, "info", "处理完成: %s -> %s, converted=%d kept=%d", job.SourcePath, job.FinalZip, archiveStats.Extracted, archiveStats.Kept)

		if idx == len(pendingJobs)-1 {
			task.CurrentItem = ""
		}
	}

	task.ProgressDone = doneCount
	task.Summary = stats
	if scanErr != nil || stats.Failed > 0 {
		return webpTaskFailed, fmt.Sprintf("webp task finished with %d failures; first failure: %v", stats.Failed, firstFailure)
	}
	return webpTaskSucceeded, ""
}

func (s *Server) cleanupWebPTaskWorkDirs(task *webpTask) error {
	if task == nil {
		return errors.New("task is nil")
	}
	if strings.TrimSpace(task.SourcePath) == "" || strings.TrimSpace(task.OutputDirPath) == "" {
		return nil
	}

	jobs, _, err := scanWebPArchiveJobs(task.SourcePath, task.OutputDirPath, nil, nil)
	if err != nil {
		return err
	}

	var cleanupErrs []string
	for _, job := range jobs {
		if strings.TrimSpace(job.WorkDir) == "" {
			continue
		}
		if err := os.RemoveAll(job.WorkDir); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Sprintf("%s: %v", job.WorkDir, err))
		}
	}
	if len(cleanupErrs) > 0 {
		return fmt.Errorf("cleanup webp work dirs failed: %s", strings.Join(cleanupErrs, "; "))
	}
	return nil
}

func scanWebPArchiveJobs(sourcePath, outputDir string, shouldCancel func() bool, progress func(archiveWorkflowStats, string)) ([]webpConvertArchiveJob, archiveWorkflowStats, error) {
	stats := archiveWorkflowStats{}
	jobs := make([]webpConvertArchiveJob, 0, 128)
	outputDir = filepath.Clean(outputDir)
	outputPrefix := outputDir + string(filepath.Separator)
	err := filepath.WalkDir(sourcePath, func(curr string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if shouldCancel != nil && shouldCancel() {
			return errWebPTaskCanceled
		}
		if curr == sourcePath {
			return nil
		}
		if d.IsDir() {
			if mountsShouldSkipDirName(d.Name()) {
				return filepath.SkipDir
			}
			cleanCurr := filepath.Clean(curr)
			if cleanCurr == outputDir || strings.HasPrefix(cleanCurr, outputPrefix) {
				return filepath.SkipDir
			}
			if shouldCancel != nil && shouldCancel() {
				return errWebPTaskCanceled
			}
			return nil
		}
		stats.Scanned++
		if progress != nil && (stats.Scanned == 1 || stats.Scanned%25 == 0) {
			progress(stats, curr)
		}
		if !isSupportedWebPSourceArchive(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(sourcePath, curr)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		jobs = append(jobs, webpConvertArchiveJob{
			SourcePath: curr,
			RelPath:    rel,
		})
		stats.Matched++
		if progress != nil && (stats.Matched == 1 || stats.Matched%25 == 0) {
			progress(stats, curr)
		}
		return nil
	})
	sort.Slice(jobs, func(i, j int) bool {
		left, right := strings.ToLower(jobs[i].RelPath), strings.ToLower(jobs[j].RelPath)
		if left == right {
			return jobs[i].RelPath < jobs[j].RelPath
		}
		return left < right
	})
	assignWebPArchiveOutputPaths(jobs, outputDir)
	return jobs, stats, err
}

func (s *Server) convertArchiveJobToWebP(task *webpTask, job webpConvertArchiveJob, guard *webpTaskStateGuard) (archiveWorkflowStats, error) {
	stats := archiveWorkflowStats{}
	entries, err := scanWebPArchiveEntries(job.SourcePath, func() bool {
		return s.isWebPTaskCancelRequested(task.ID)
	})
	if err != nil {
		return stats, err
	}
	if len(entries) == 0 {
		return stats, fmt.Errorf("archive has no image files")
	}

	if manifest, complete, err := s.matchCompletedWebPArchiveManifest(job, task.Quality, task.Method); err == nil && complete {
		stats.Kept += manifest.KeptEntries
		stats.Extracted += manifest.ConvertedEntries
		_ = os.RemoveAll(job.WorkDir)
		return stats, nil
	}

	if err := os.MkdirAll(filepath.Dir(job.FinalZip), 0o755); err != nil {
		return stats, err
	}

	_ = guard.update(func() error {
		task.CurrentArchive = job.RelPath
		task.CurrentItem = job.RelPath
		task.CurrentArchiveDone = 0
		task.CurrentArchiveTotal = len(entries)
		task.CurrentPhase = fmt.Sprintf("处理压缩包 %s · 图片 %d/%d", job.RelPath, 0, len(entries))
		task.UpdatedAt = timeNow()
		return s.persistWebPTask(task)
	}, true, nil)
	_ = guard.update(func() error {
		return s.appendPersistedWebPTaskLog(task, "info", "开始转换图片: %s %d/%d", job.RelPath, 0, len(entries))
	}, true, nil)

	doneImages := 0
	progressCB := func(entry webpArchiveEntry) {
		_ = guard.update(func() error {
			doneImages++
			task.CurrentArchive = job.RelPath
			task.CurrentArchiveDone = doneImages
			task.CurrentArchiveTotal = len(entries)
			task.CurrentItem = fmt.Sprintf("%s · %s", job.RelPath, entry.TargetRel)
			task.CurrentPhase = fmt.Sprintf("处理压缩包 %s · 图片 %d/%d", job.RelPath, doneImages, len(entries))
			task.UpdatedAt = timeNow()
			if err := s.persistWebPTask(task); err != nil {
				return err
			}
			if doneImages == 1 || doneImages == len(entries) || doneImages%25 == 0 {
				return s.appendPersistedWebPTaskLog(task, "info", "进度: %s %d/%d", job.RelPath, doneImages, len(entries))
			}
			return nil
		}, false, nil)
	}

	options := imgcodec.LossyWebPEncodeOptions{
		Quality: float32(task.Quality),
		Method:  int(task.Method),
	}
	if err := writeWebPArchiveOutput(job.SourcePath, job.FinalZip, entries, options, int(task.Concurrency), func() bool {
		// Yield between entries before acquiring any image resources. Keep
		// cancellation responsive and bound a busy reader's delay of this job.
		deadline := time.Now().Add(2 * time.Second)
		for s.interactiveReads.Load() > 0 && time.Now().Before(deadline) {
			if s.isWebPTaskCancelRequested(task.ID) {
				return true
			}
			time.Sleep(25 * time.Millisecond)
		}
		return s.isWebPTaskCancelRequested(task.ID)
	}, progressCB, &stats); err != nil {
		return stats, err
	}
	if s.isWebPTaskCancelRequested(task.ID) {
		return stats, errWebPTaskCanceled
	}
	sourceInfo, err := os.Stat(job.SourcePath)
	if err != nil {
		return stats, err
	}
	outputInfo, err := os.Stat(job.FinalZip)
	if err != nil {
		return stats, err
	}
	if err := s.store.UpsertWebPArchiveManifest(db.WebPArchiveManifest{
		SourcePath:       job.SourcePath,
		OutputZipPath:    job.FinalZip,
		Quality:          task.Quality,
		Method:           task.Method,
		SourceMtimeUnix:  sourceInfo.ModTime().UnixNano(),
		SourceSize:       sourceInfo.Size(),
		SourceEntryCount: len(entries),
		ConvertedEntries: stats.Extracted,
		KeptEntries:      stats.Kept,
		OutputSize:       outputInfo.Size(),
		CompletedAt:      timeNow(),
	}); err != nil && s.logger != nil {
		s.logger.Printf("persist webp manifest failed %s: %v", job.FinalZip, err)
	}
	return stats, nil
}

func (s *Server) matchCompletedWebPArchiveManifest(job webpConvertArchiveJob, quality, method int64) (db.WebPArchiveManifest, bool, error) {
	sourceInfo, err := os.Stat(job.SourcePath)
	if err != nil {
		return db.WebPArchiveManifest{}, false, err
	}
	outputInfo, err := os.Stat(job.FinalZip)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return db.WebPArchiveManifest{}, false, nil
		}
		return db.WebPArchiveManifest{}, false, err
	}
	manifest, err := s.store.GetWebPArchiveManifest(job.SourcePath, job.FinalZip, quality, method)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return db.WebPArchiveManifest{}, false, nil
		}
		return db.WebPArchiveManifest{}, false, err
	}
	if manifest.SourceMtimeUnix != sourceInfo.ModTime().UnixNano() {
		return manifest, false, nil
	}
	if manifest.SourceSize != sourceInfo.Size() || manifest.OutputSize != outputInfo.Size() {
		return manifest, false, nil
	}
	if manifest.SourceEntryCount <= 0 {
		return manifest, false, nil
	}
	return manifest, true, nil
}

func writeWebPArchiveOutput(srcArchive, finalZip string, entries []webpArchiveEntry, options imgcodec.LossyWebPEncodeOptions, concurrency int, shouldCancel func() bool, progressCB func(webpArchiveEntry), stats *archiveWorkflowStats) error {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(srcArchive))) {
	case ".zip", ".cbz":
		return writeZipArchiveToWebPOutput(srcArchive, finalZip, entries, options, concurrency, shouldCancel, progressCB, stats)
	case ".rar", ".cbr":
		return writeRARArchiveToWebPOutput(srcArchive, finalZip, entries, options, shouldCancel, progressCB, stats)
	default:
		return fmt.Errorf("unsupported archive format: %s", srcArchive)
	}
}

func applyArchiveWriteStats(stats *archiveWorkflowStats, entry webpArchiveEntry) {
	if stats == nil {
		return
	}
	if entry.IsWebP || entry.KeepOriginal {
		stats.Kept++
		return
	}
	stats.Extracted++
}

func writeZipArchiveToWebPOutput(srcArchive, finalZip string, entries []webpArchiveEntry, options imgcodec.LossyWebPEncodeOptions, concurrency int, shouldCancel func() bool, progressCB func(webpArchiveEntry), stats *archiveWorkflowStats) error {
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 16 {
		concurrency = 16
	}
	reader, srcFile, err := openZipReader(srcArchive)
	if err != nil {
		return err
	}
	defer func() { _ = srcFile.Close() }()

	var imageFiles []*zip.File
	for _, file := range reader.File {
		if !file.FileInfo().IsDir() && isWebPSourceImage(file.Name) {
			imageFiles = append(imageFiles, file)
		}
	}
	if len(imageFiles) != len(entries) {
		return fmt.Errorf("zip image count changed during processing")
	}

	tmpZip, out, writer, closeFn, err := openTempZipWriter(finalZip)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		_ = closeFn(success)
		if !success {
			_ = os.Remove(tmpZip)
		}
	}()

	jobs := make(chan webpArchiveEntry, concurrency)
	results := make(chan webpArchiveWriteResult, concurrency)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for entry := range jobs {
				if shouldCancel != nil && shouldCancel() {
					results <- webpArchiveWriteResult{Entry: entry, Err: errWebPTaskCanceled}
					continue
				}
				if entry.Index < 0 || entry.Index >= len(imageFiles) {
					results <- webpArchiveWriteResult{Entry: entry, Err: fmt.Errorf("zip image index out of range")}
					continue
				}
				rc, err := imageFiles[entry.Index].Open()
				if err != nil {
					results <- webpArchiveWriteResult{Entry: entry, Err: fmt.Errorf("open image %q: %w", entry.SourceRel, err)}
					continue
				}
				var data []byte
				if entry.IsWebP || entry.KeepOriginal {
					data, err = io.ReadAll(rc)
				} else {
					data, err = imgcodec.EncodeLossyWebPReaderWithOptions(rc, options)
				}
				_ = rc.Close()
				if err != nil {
					err = fmt.Errorf("convert image %q: %w", entry.SourceRel, err)
				}
				results <- webpArchiveWriteResult{Entry: entry, Data: data, Err: err}
			}
		}()
	}
	go func() {
		for _, entry := range entries {
			if shouldCancel != nil && shouldCancel() {
				break
			}
			jobs <- entry
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	pending := make(map[int]webpArchiveWriteResult, concurrency)
	next := 0
	firstErr := error(nil)
	for result := range results {
		if firstErr == nil && result.Err != nil {
			firstErr = result.Err
		}
		pending[result.Entry.Index] = result
		for firstErr == nil {
			current, ok := pending[next]
			if !ok {
				break
			}
			if err := writeZipEntry(writer, &zip.FileHeader{Name: current.Entry.TargetRel, Method: zip.Deflate}, current.Data); err != nil {
				firstErr = err
				break
			}
			applyArchiveWriteStats(stats, current.Entry)
			if progressCB != nil {
				progressCB(current.Entry)
			}
			delete(pending, next)
			next++
		}
	}
	if firstErr != nil {
		_ = writer.Close()
		_ = out.Close()
		return firstErr
	}
	if shouldCancel != nil && shouldCancel() {
		_ = writer.Close()
		_ = out.Close()
		return errWebPTaskCanceled
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpZip, finalZip); err != nil {
		return err
	}
	success = true
	return nil
}

func writeRARArchiveToWebPOutput(srcArchive, finalZip string, entries []webpArchiveEntry, options imgcodec.LossyWebPEncodeOptions, shouldCancel func() bool, progressCB func(webpArchiveEntry), stats *archiveWorkflowStats) error {
	f, err := os.Open(srcArchive)
	if err != nil {
		return err
	}
	defer f.Close()

	reader, err := rardecode.NewReader(f, "")
	if err != nil {
		return err
	}
	tmpZip, out, writer, closeFn, err := openTempZipWriter(finalZip)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		_ = closeFn(success)
		if !success {
			_ = os.Remove(tmpZip)
		}
	}()

	imageIdx := 0
	for {
		if shouldCancel != nil && shouldCancel() {
			_ = writer.Close()
			_ = out.Close()
			return errWebPTaskCanceled
		}
		h, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if h == nil || h.IsDir || !isWebPSourceImage(h.Name) {
			continue
		}
		if imageIdx < 0 || imageIdx >= len(entries) {
			return fmt.Errorf("rar image index out of range")
		}
		entry := entries[imageIdx]
		var data []byte
		if entry.IsWebP || entry.KeepOriginal {
			data, err = io.ReadAll(reader)
		} else {
			data, err = imgcodec.EncodeLossyWebPReaderWithOptions(reader, options)
		}
		if err != nil {
			return fmt.Errorf("convert image %q: %w", entry.SourceRel, err)
		}
		if err := writeZipEntry(writer, &zip.FileHeader{Name: entry.TargetRel, Method: zip.Deflate}, data); err != nil {
			return fmt.Errorf("write image %q: %w", entry.TargetRel, err)
		}
		applyArchiveWriteStats(stats, entry)
		if progressCB != nil {
			progressCB(entry)
		}
		imageIdx++
	}
	if imageIdx != len(entries) {
		return fmt.Errorf("rar image count changed during processing")
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpZip, finalZip); err != nil {
		return err
	}
	success = true
	return nil
}

func openTempZipWriter(finalZip string) (string, *os.File, *zip.Writer, func(bool) error, error) {
	if err := os.MkdirAll(filepath.Dir(finalZip), 0o755); err != nil {
		return "", nil, nil, nil, err
	}
	out, err := os.CreateTemp(filepath.Dir(finalZip), ".webp-*.tmp")
	if err != nil {
		return "", nil, nil, nil, err
	}
	tmpZip := out.Name()
	if err := out.Chmod(0o644); err != nil {
		_ = out.Close()
		_ = os.Remove(tmpZip)
		return "", nil, nil, nil, err
	}
	writer := zip.NewWriter(out)
	closeFn := func(success bool) error {
		if !success {
			_ = writer.Close()
		}
		return out.Close()
	}
	return tmpZip, out, writer, closeFn, nil
}

func isWebPArchiveOutputComplete(sourceArchive, finalZip string) (bool, error) {
	entries, err := scanWebPArchiveEntries(sourceArchive, nil)
	if err != nil {
		return false, err
	}
	return isWebPArchiveOutputCompleteWithEntries(finalZip, entries)
}

func isWebPArchiveOutputCompleteWithEntries(finalZip string, entries []webpArchiveEntry) (bool, error) {
	if len(entries) == 0 {
		return false, fmt.Errorf("archive has no image files")
	}
	exists, err := pathExists(finalZip)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	reader, srcFile, err := openZipReader(finalZip)
	if err != nil {
		return false, err
	}
	defer func() { _ = srcFile.Close() }()

	imageFiles := ziputil.ImageFiles(reader)
	if len(imageFiles) != len(entries) {
		return false, nil
	}
	for idx, entry := range entries {
		cleanName, ok := cleanArchiveEntryPath(imageFiles[idx].Name)
		if !ok {
			return false, nil
		}
		if cleanName != entry.TargetRel {
			return false, nil
		}
		if !entry.KeepOriginal && !strings.EqualFold(filepath.Ext(cleanName), ".webp") {
			return false, nil
		}
	}
	return true, nil
}

func scanWebPArchiveEntries(srcArchive string, shouldCancel func() bool) ([]webpArchiveEntry, error) {
	usedNames := make(map[string]int, 128)
	entries := make([]webpArchiveEntry, 0, 128)
	appendEntry := func(rawName string) {
		cleanName, ok := cleanArchiveEntryPath(safeWebPArchiveName(rawName))
		if !ok || !isWebPSourceImage(cleanName) {
			return
		}
		keepOriginal := strings.EqualFold(filepath.Ext(cleanName), ".avif")
		var target string
		if keepOriginal {
			target = uniqueArchiveEntryName(cleanName, usedNames)
		} else {
			target = uniqueWebPEntryName(cleanName, usedNames)
		}
		entries = append(entries, webpArchiveEntry{
			Index:        len(entries),
			SourceRel:    cleanName,
			TargetRel:    target,
			IsWebP:       strings.EqualFold(filepath.Ext(cleanName), ".webp"),
			KeepOriginal: keepOriginal,
		})
	}

	switch strings.ToLower(filepath.Ext(strings.TrimSpace(srcArchive))) {
	case ".zip", ".cbz":
		reader, srcFile, err := openZipReader(srcArchive)
		if err != nil {
			return nil, err
		}
		defer func() { _ = srcFile.Close() }()
		for _, file := range reader.File {
			if shouldCancel != nil && shouldCancel() {
				return nil, errWebPTaskCanceled
			}
			if file == nil || file.FileInfo().IsDir() {
				continue
			}
			appendEntry(file.Name)
		}
	case ".rar", ".cbr":
		f, err := os.Open(srcArchive)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		reader, err := rardecode.NewReader(f, "")
		if err != nil {
			return nil, err
		}
		for {
			if shouldCancel != nil && shouldCancel() {
				return nil, errWebPTaskCanceled
			}
			h, err := reader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, err
			}
			if h == nil || h.IsDir {
				continue
			}
			appendEntry(h.Name)
			if _, err := io.Copy(io.Discard, reader); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("unsupported archive format: %s", srcArchive)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("archive has no image files")
	}
	return entries, nil
}

// AppleDouble resource forks use image extensions but contain no image data.
// Apply the same filter when scanning and reading to keep page indexes aligned.
func isWebPSourceImage(name string) bool {
	cleanName, ok := cleanArchiveEntryPath(name)
	if !ok || !ziputil.IsImageName(cleanName) {
		return false
	}
	for _, part := range strings.Split(cleanName, "/") {
		if part == "__MACOSX" || strings.HasPrefix(part, "._") {
			return false
		}
	}
	return true
}

func countWebPEntries(entries []webpArchiveEntry) int {
	count := 0
	for _, entry := range entries {
		if entry.IsWebP {
			count++
		}
	}
	return count
}

func openZipReader(path string) (*zip.Reader, *os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	reader, err := zip.NewReader(f, info.Size())
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	for _, file := range reader.File {
		file.Name = webPZIPEntryName(file)
	}
	return reader, f, nil
}

func uniqueWebPEntryName(name string, used map[string]int) string {
	dir := pathDir(name)
	base := strings.TrimSuffix(pathBase(name), filepath.Ext(name))
	if strings.TrimSpace(base) == "" {
		base = "image"
	}
	return uniqueArchiveEntryName(pathJoin(dir, base+".webp"), used)
}

func uniqueArchiveEntryName(name string, used map[string]int) string {
	current := strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if current == "" {
		current = "item"
	}
	key := webPNameKey(current)
	if used[key] == 0 {
		used[key] = 1
		return current
	}
	ext := filepath.Ext(current)
	base := strings.TrimSuffix(current, ext)
	for count := used[key]; ; count++ {
		candidate := fmt.Sprintf("%s__%d%s", base, count, ext)
		candidateKey := webPNameKey(candidate)
		if used[candidateKey] != 0 {
			continue
		}
		used[key] = count + 1
		used[candidateKey] = 1
		return candidate
	}
}

func writeZipEntry(writer *zip.Writer, header *zip.FileHeader, data []byte) error {
	entryWriter, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = entryWriter.Write(data)
	return err
}

func isSupportedWebPSourceArchive(name string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".zip", ".cbz", ".rar", ".cbr":
		return true
	default:
		return false
	}
}

func mountsShouldSkipDirName(name string) bool {
	return strings.HasPrefix(strings.TrimSpace(name), ".") || strings.EqualFold(strings.TrimSpace(name), "@eadir")
}

func pathDir(name string) string {
	dir := path.Clean(path.Dir(strings.ReplaceAll(name, "\\", "/")))
	if dir == "." || dir == "/" {
		return ""
	}
	return dir
}

func pathBase(name string) string {
	return path.Base(strings.ReplaceAll(name, "\\", "/"))
}

func pathJoin(dir, name string) string {
	if strings.TrimSpace(dir) == "" {
		return name
	}
	return strings.TrimPrefix(path.Clean("/"+dir+"/"+name), "/")
}

func timeNow() time.Time {
	return time.Now()
}
