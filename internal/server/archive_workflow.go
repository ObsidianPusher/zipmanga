package server

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	"github.com/bodgit/sevenzip"
	"github.com/nwaples/rardecode"
)

const (
	archiveActionCollect     = "collect"
	archiveActionExtract     = "extract"
	archiveActionOrganize    = "organize"
	archiveActionRepack      = "repack"
	archiveActionFormatNames = "format_names"
	archiveActionWebPConvert = "webp_convert"

	minComicImageCount = 11
	archiveCopyBufSize = 1 << 20 // 1 MiB
)

type archiveWorkflowStats struct {
	Scanned   int `json:"scanned"`
	Matched   int `json:"matched"`
	Moved     int `json:"moved"`
	Extracted int `json:"extracted"`
	Kept      int `json:"kept"`
	Deleted   int `json:"deleted"`
	Zipped    int `json:"zipped"`
	Skipped   int `json:"skipped"`
	Failed    int `json:"failed"`
	Partial   int `json:"partial,omitempty"`
	Pending   int `json:"pending,omitempty"`
}

type archiveWorkflowRootResult struct {
	RootPath string               `json:"root_path"`
	Stats    archiveWorkflowStats `json:"stats"`
	Errors   []string             `json:"errors,omitempty"`
}

func (s *Server) handleAPIAdminArchiveWorkflowRun(w http.ResponseWriter, r *http.Request) {
	s.handleAPIAdminArchiveWorkflowCreateTask(w, r)
}

func runArchiveWorkflowOnRoot(action, rootPath, password string) (archiveWorkflowStats, []string, error) {
	if err := ensureDirExists(rootPath); err != nil {
		return archiveWorkflowStats{}, nil, err
	}
	switch action {
	case archiveActionCollect:
		return runArchiveCollect(rootPath)
	case archiveActionExtract:
		return runArchiveExtract(rootPath, password)
	case archiveActionOrganize:
		return runArchiveOrganize(rootPath)
	case archiveActionRepack:
		return runArchiveRepack(rootPath)
	default:
		return archiveWorkflowStats{}, nil, fmt.Errorf("unsupported action: %s", action)
	}
}

func runArchiveCollect(rootPath string) (archiveWorkflowStats, []string, error) {
	stats := archiveWorkflowStats{}
	errs := make([]string, 0)

	collectDir := filepath.Join(rootPath, "collect")
	tempDir := filepath.Join(rootPath, "temp")
	if err := os.MkdirAll(collectDir, 0o755); err != nil {
		return stats, errs, err
	}

	walkErr := filepath.WalkDir(rootPath, func(curr string, d fs.DirEntry, err error) error {
		if err != nil {
			stats.Failed++
			errs = append(errs, fmt.Sprintf("walk %s: %v", curr, err))
			return nil
		}
		if curr == rootPath {
			return nil
		}
		if d.IsDir() {
			if samePath(curr, collectDir) || samePath(curr, tempDir) {
				return filepath.SkipDir
			}
			if isSymlinkEntry(d) {
				stats.Skipped++
				return filepath.SkipDir
			}
			return nil
		}
		if isSymlinkEntry(d) {
			stats.Skipped++
			return nil
		}

		stats.Scanned++
		if !isConvertibleArchiveName(d.Name()) {
			return nil
		}
		stats.Matched++

		dst, err := uniquePathWithSuffix(collectDir, filepath.Base(curr))
		if err != nil {
			stats.Failed++
			errs = append(errs, fmt.Sprintf("prepare target for %s: %v", curr, err))
			return nil
		}
		if samePath(curr, dst) {
			stats.Skipped++
			return nil
		}
		if err := moveFile(curr, dst); err != nil {
			stats.Failed++
			errs = append(errs, fmt.Sprintf("move %s -> %s failed: %v", curr, dst, err))
			return nil
		}
		stats.Moved++
		return nil
	})
	sort.Strings(errs)
	return stats, errs, walkErr
}

func runArchiveExtract(rootPath, password string) (archiveWorkflowStats, []string, error) {
	stats := archiveWorkflowStats{}
	errs := make([]string, 0)

	collectDir := filepath.Join(rootPath, "collect")
	tempDir := filepath.Join(rootPath, "temp")
	if err := os.MkdirAll(collectDir, 0o755); err != nil {
		return stats, errs, err
	}
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return stats, errs, err
	}

	type extractJob struct {
		src string
		dst string
	}
	jobs := make([]extractJob, 0, 128)

	walkErr := filepath.WalkDir(collectDir, func(curr string, d fs.DirEntry, err error) error {
		if err != nil {
			stats.Failed++
			errs = append(errs, fmt.Sprintf("walk %s: %v", curr, err))
			return nil
		}
		if d.IsDir() {
			if isSymlinkEntry(d) {
				stats.Skipped++
				return filepath.SkipDir
			}
			return nil
		}
		if isSymlinkEntry(d) {
			stats.Skipped++
			return nil
		}

		stats.Scanned++
		if !isConvertibleArchiveName(d.Name()) {
			return nil
		}
		stats.Matched++

		baseName := sanitizeDirectoryName(archiveBaseName(d.Name()))
		if baseName == "" {
			baseName = "archive"
		}
		dstDir, err := uniqueDirPath(tempDir, baseName)
		if err != nil {
			stats.Failed++
			errs = append(errs, fmt.Sprintf("prepare extract target for %s: %v", curr, err))
			return nil
		}
		if err := os.MkdirAll(dstDir, 0o755); err != nil {
			stats.Failed++
			errs = append(errs, fmt.Sprintf("create extract target %s failed: %v", dstDir, err))
			return nil
		}

		jobs = append(jobs, extractJob{
			src: curr,
			dst: dstDir,
		})
		return nil
	})
	if walkErr != nil {
		sort.Strings(errs)
		return stats, errs, walkErr
	}

	if len(jobs) == 0 {
		sort.Strings(errs)
		return stats, errs, nil
	}

	workers := archiveWorkerCount(len(jobs))
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	jobCh := make(chan extractJob)
	workerFn := func() {
		defer wg.Done()
		for job := range jobCh {
			fileCount, err := extractArchiveFile(job.src, job.dst, password)
			if err != nil {
				_ = os.RemoveAll(job.dst)
				mu.Lock()
				stats.Failed++
				errs = append(errs, fmt.Sprintf("extract %s failed: %v", job.src, err))
				mu.Unlock()
				continue
			}
			mu.Lock()
			stats.Extracted += fileCount
			mu.Unlock()
		}
	}
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go workerFn()
	}
	for _, job := range jobs {
		jobCh <- job
	}
	close(jobCh)
	wg.Wait()

	sort.Strings(errs)
	return stats, errs, nil
}

func runArchiveOrganize(rootPath string) (archiveWorkflowStats, []string, error) {
	stats := archiveWorkflowStats{}
	errs := make([]string, 0)

	tempDir := filepath.Join(rootPath, "temp")
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return stats, errs, err
	}

	candidates, scanned, candidateErrs, walkErr := findLeafComicDirs(tempDir, minComicImageCount)
	errs = append(errs, candidateErrs...)
	if walkErr != nil {
		return stats, errs, walkErr
	}
	stats.Scanned = scanned
	stats.Matched = len(candidates)

	kept := make(map[string]struct{}, len(candidates))
	for _, src := range candidates {
		rel, err := filepath.Rel(tempDir, src)
		if err != nil {
			stats.Failed++
			errs = append(errs, fmt.Sprintf("resolve relative path for %s failed: %v", src, err))
			continue
		}
		name := sanitizeDirectoryName(strings.ReplaceAll(filepath.ToSlash(rel), "/", "__"))
		if name == "" {
			name = sanitizeDirectoryName(filepath.Base(src))
		}
		if name == "" {
			name = "comic"
		}
		preferred := filepath.Join(tempDir, name)
		dst := preferred
		if !samePath(src, preferred) {
			dst, err = uniqueDirPath(tempDir, name)
			if err != nil {
				stats.Failed++
				errs = append(errs, fmt.Sprintf("prepare organize target for %s failed: %v", src, err))
				continue
			}
		}
		if samePath(src, dst) {
			kept[filepath.Clean(src)] = struct{}{}
			stats.Kept++
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			stats.Failed++
			errs = append(errs, fmt.Sprintf("move %s -> %s failed: %v", src, dst, err))
			continue
		}
		kept[filepath.Clean(dst)] = struct{}{}
		stats.Kept++
	}

	entries, err := os.ReadDir(tempDir)
	if err != nil {
		return stats, errs, err
	}
	for _, ent := range entries {
		target := filepath.Join(tempDir, ent.Name())
		cleanTarget := filepath.Clean(target)
		if ent.IsDir() {
			if _, ok := kept[cleanTarget]; ok {
				imgCount, countErr := countImagesDirect(cleanTarget)
				if countErr != nil {
					stats.Failed++
					errs = append(errs, fmt.Sprintf("count images in %s failed: %v", cleanTarget, countErr))
					continue
				}
				if imgCount >= minComicImageCount {
					continue
				}
			}
			if err := os.RemoveAll(cleanTarget); err != nil {
				stats.Failed++
				errs = append(errs, fmt.Sprintf("remove %s failed: %v", cleanTarget, err))
				continue
			}
			stats.Deleted++
			continue
		}
		if isArchiveLikeName(ent.Name()) {
			stats.Skipped++
			continue
		}
		if err := os.Remove(cleanTarget); err != nil && !errors.Is(err, os.ErrNotExist) {
			stats.Failed++
			errs = append(errs, fmt.Sprintf("remove file %s failed: %v", cleanTarget, err))
			continue
		}
		stats.Deleted++
	}

	sort.Strings(errs)
	return stats, errs, nil
}

func runArchiveRepack(rootPath string) (archiveWorkflowStats, []string, error) {
	stats := archiveWorkflowStats{}
	errs := make([]string, 0)

	tempDir := filepath.Join(rootPath, "temp")
	zipDir := filepath.Join(rootPath, "zip")
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return stats, errs, err
	}
	if err := os.MkdirAll(zipDir, 0o755); err != nil {
		return stats, errs, err
	}

	entries, err := os.ReadDir(tempDir)
	if err != nil {
		return stats, errs, err
	}
	type repackJob struct {
		srcDir  string
		tmpZip  string
		zipPath string
	}
	jobs := make([]repackJob, 0, len(entries))
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		if ent.Type()&os.ModeSymlink != 0 {
			stats.Skipped++
			continue
		}
		srcDir := filepath.Join(tempDir, ent.Name())
		stats.Scanned++

		zipName := sanitizeFileName(ent.Name()) + ".zip"
		zipPath, err := uniquePathWithSuffix(zipDir, zipName)
		if err != nil {
			stats.Failed++
			errs = append(errs, fmt.Sprintf("prepare zip name for %s failed: %v", srcDir, err))
			continue
		}
		tmpZip, err := uniquePathWithSuffix(tempDir, zipName+".tmp")
		if err != nil {
			stats.Failed++
			errs = append(errs, fmt.Sprintf("prepare temp zip name for %s failed: %v", srcDir, err))
			continue
		}
		jobs = append(jobs, repackJob{
			srcDir:  srcDir,
			tmpZip:  tmpZip,
			zipPath: zipPath,
		})
	}
	if len(jobs) == 0 {
		sort.Strings(errs)
		return stats, errs, nil
	}

	workers := archiveWorkerCount(len(jobs))
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	jobCh := make(chan repackJob)
	workerFn := func() {
		defer wg.Done()
		for job := range jobCh {
			if _, err := zipDirectoryStore(job.srcDir, job.tmpZip); err != nil {
				_ = os.Remove(job.tmpZip)
				mu.Lock()
				stats.Failed++
				errs = append(errs, fmt.Sprintf("zip %s failed: %v", job.srcDir, err))
				mu.Unlock()
				continue
			}
			if err := moveFile(job.tmpZip, job.zipPath); err != nil {
				_ = os.Remove(job.tmpZip)
				mu.Lock()
				stats.Failed++
				errs = append(errs, fmt.Sprintf("move zip %s -> %s failed: %v", job.tmpZip, job.zipPath, err))
				mu.Unlock()
				continue
			}
			removeErr := os.RemoveAll(job.srcDir)
			mu.Lock()
			stats.Zipped++
			stats.Moved++
			if removeErr != nil {
				stats.Failed++
				errs = append(errs, fmt.Sprintf("remove source dir %s failed: %v", job.srcDir, removeErr))
			} else {
				stats.Deleted++
			}
			mu.Unlock()
		}
	}
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go workerFn()
	}
	for _, job := range jobs {
		jobCh <- job
	}
	close(jobCh)
	wg.Wait()

	sort.Strings(errs)
	return stats, errs, nil
}

func extractArchiveFile(srcArchive, dstDir, password string) (int, error) {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(srcArchive)))
	switch ext {
	case ".7z", ".cb7":
		return extract7ZArchive(srcArchive, dstDir, password)
	case ".rar", ".cbr":
		return extractRARArchive(srcArchive, dstDir, password)
	default:
		return 0, fmt.Errorf("unsupported archive: %s", srcArchive)
	}
}

func extract7ZArchive(srcArchive, dstDir, password string) (int, error) {
	var (
		reader *sevenzip.ReadCloser
		err    error
	)
	if strings.TrimSpace(password) == "" {
		reader, err = sevenzip.OpenReader(srcArchive)
	} else {
		reader, err = sevenzip.OpenReaderWithPassword(srcArchive, password)
	}
	if err != nil {
		return 0, err
	}
	defer reader.Close()

	copyBuf := make([]byte, archiveCopyBufSize)
	createdDirs := map[string]struct{}{filepath.Clean(dstDir): {}}
	extracted := 0
	for _, file := range reader.File {
		if file == nil {
			continue
		}
		entryPath, ok := cleanArchiveEntryPath(file.Name)
		if !ok {
			continue
		}
		target, err := secureJoin(dstDir, entryPath)
		if err != nil {
			return extracted, err
		}
		info := file.FileInfo()
		if info.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return extracted, err
			}
			createdDirs[filepath.Clean(target)] = struct{}{}
			continue
		}
		if err := ensureDirCached(filepath.Dir(target), createdDirs); err != nil {
			return extracted, err
		}

		rc, err := file.Open()
		if err != nil {
			return extracted, err
		}
		mode := info.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		if err := writeFileFromReader(target, rc, mode, copyBuf); err != nil {
			_ = rc.Close()
			return extracted, err
		}
		_ = rc.Close()
		extracted++
	}
	return extracted, nil
}

func extractRARArchive(srcArchive, dstDir, password string) (int, error) {
	f, err := os.Open(srcArchive)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	reader, err := rardecode.NewReader(f, password)
	if err != nil {
		return 0, err
	}

	copyBuf := make([]byte, archiveCopyBufSize)
	createdDirs := map[string]struct{}{filepath.Clean(dstDir): {}}
	extracted := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return extracted, err
		}
		if header == nil {
			continue
		}
		entryPath, ok := cleanArchiveEntryPath(header.Name)
		if !ok {
			continue
		}
		target, err := secureJoin(dstDir, entryPath)
		if err != nil {
			return extracted, err
		}
		if header.IsDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return extracted, err
			}
			createdDirs[filepath.Clean(target)] = struct{}{}
			continue
		}
		if err := ensureDirCached(filepath.Dir(target), createdDirs); err != nil {
			return extracted, err
		}

		mode := header.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		if err := writeFileFromReader(target, reader, mode, copyBuf); err != nil {
			return extracted, err
		}
		extracted++
	}
	return extracted, nil
}

func zipDirectoryStore(srcDir, dstZip string) (int, error) {
	out, err := os.Create(dstZip)
	if err != nil {
		return 0, err
	}

	zw := zip.NewWriter(out)
	fileCount := 0
	copyBuf := make([]byte, archiveCopyBufSize)
	walkErr := filepath.WalkDir(srcDir, func(curr string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}

		rel, err := filepath.Rel(srcDir, curr)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." || rel == "" {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = rel
		header.Method = zip.Store
		writer, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		f, err := os.Open(curr)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyBuffer(writer, f, copyBuf)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		fileCount++
		return nil
	})
	closeZipErr := zw.Close()
	closeFileErr := out.Close()

	if walkErr != nil {
		return fileCount, walkErr
	}
	if closeZipErr != nil {
		return fileCount, closeZipErr
	}
	if closeFileErr != nil {
		return fileCount, closeFileErr
	}
	return fileCount, nil
}

func findLeafComicDirs(tempDir string, minImages int) ([]string, int, []string, error) {
	candidates := make([]string, 0, 256)
	errs := make([]string, 0)
	scanned := 0
	walkErr := filepath.WalkDir(tempDir, func(curr string, d fs.DirEntry, err error) error {
		if err != nil {
			errs = append(errs, fmt.Sprintf("walk %s: %v", curr, err))
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if curr == tempDir {
			return nil
		}
		if isSymlinkEntry(d) {
			return filepath.SkipDir
		}

		entries, err := os.ReadDir(curr)
		if err != nil {
			errs = append(errs, fmt.Sprintf("read dir %s failed: %v", curr, err))
			return nil
		}
		hasSubDir := false
		imageCount := 0
		for _, item := range entries {
			if item.IsDir() {
				if item.Type()&os.ModeSymlink != 0 {
					continue
				}
				hasSubDir = true
				continue
			}
			if item.Type()&os.ModeSymlink != 0 {
				continue
			}
			scanned++
			if isImageName(item.Name()) {
				imageCount++
			}
		}
		if !hasSubDir && imageCount >= minImages {
			candidates = append(candidates, curr)
		}
		return nil
	})

	sort.SliceStable(candidates, func(i, j int) bool {
		a := filepath.Clean(candidates[i])
		b := filepath.Clean(candidates[j])
		return len(a) < len(b)
	})
	sort.Strings(errs)
	return candidates, scanned, errs, walkErr
}

func countImagesDirect(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ent := range entries {
		if ent.IsDir() || ent.Type()&os.ModeSymlink != 0 {
			continue
		}
		if isImageName(ent.Name()) {
			n++
		}
	}
	return n, nil
}

func addArchiveStats(dst *archiveWorkflowStats, src archiveWorkflowStats) {
	dst.Scanned += src.Scanned
	dst.Matched += src.Matched
	dst.Moved += src.Moved
	dst.Extracted += src.Extracted
	dst.Kept += src.Kept
	dst.Deleted += src.Deleted
	dst.Zipped += src.Zipped
	dst.Skipped += src.Skipped
	dst.Failed += src.Failed
}

func isConvertibleArchiveName(name string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".rar", ".cbr", ".7z", ".cb7":
		return true
	default:
		return false
	}
}

func isImageName(name string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".avif", ".tif", ".tiff":
		return true
	default:
		return false
	}
}

func isArchiveLikeName(name string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".zip", ".cbz", ".rar", ".cbr", ".7z", ".cb7":
		return true
	default:
		return false
	}
}

func archiveBaseName(name string) string {
	base := strings.TrimSpace(filepath.Base(name))
	ext := filepath.Ext(base)
	if ext == "" {
		return base
	}
	return strings.TrimSuffix(base, ext)
}

func sanitizeDirectoryName(name string) string {
	v := sanitizeFileName(name)
	v = strings.ReplaceAll(v, ".", "_")
	v = strings.Trim(v, "_ ")
	if len(v) > 120 {
		v = strings.TrimSpace(truncateUTF8(v, 120))
	}
	if v == "" {
		return "item"
	}
	return v
}

func sanitizeFileName(name string) string {
	v := strings.TrimSpace(name)
	if v == "" {
		return "item"
	}
	v = strings.ReplaceAll(v, "\\", "_")
	v = strings.ReplaceAll(v, "/", "_")
	v = strings.ReplaceAll(v, ":", "_")
	v = strings.ReplaceAll(v, "\n", "_")
	v = strings.ReplaceAll(v, "\r", "_")
	v = strings.ReplaceAll(v, "\t", "_")
	if len(v) > 180 {
		v = strings.TrimSpace(truncateUTF8(v, 180))
	}
	if v == "" {
		return "item"
	}
	return v
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && !utf8.RuneStart(value[maxBytes]) {
		maxBytes--
	}
	return value[:maxBytes]
}

func cleanArchiveEntryPath(name string) (string, bool) {
	v := strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if v == "" {
		return "", false
	}
	clean := path.Clean("/" + v)
	if clean == "/" {
		return "", false
	}
	return strings.TrimPrefix(clean, "/"), true
}

func secureJoin(baseDir, entryPath string) (string, error) {
	base := filepath.Clean(baseDir)
	rel := filepath.Clean(filepath.FromSlash(entryPath))
	if rel == "." || rel == "" {
		return "", fmt.Errorf("invalid archive entry path")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("archive entry path is absolute: %s", entryPath)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry escapes root: %s", entryPath)
	}
	target := filepath.Join(base, rel)
	checkRel, err := filepath.Rel(base, target)
	if err != nil {
		return "", err
	}
	if checkRel == ".." || strings.HasPrefix(checkRel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry escapes root: %s", entryPath)
	}
	return target, nil
}

func uniquePathWithSuffix(dir, name string) (string, error) {
	base := sanitizeFileName(name)
	ext := filepath.Ext(base)
	prefix := strings.TrimSuffix(base, ext)
	if prefix == "" {
		prefix = "item"
	}
	for i := 0; i < 10000; i++ {
		filename := prefix + ext
		if i > 0 {
			filename = fmt.Sprintf("%s__%d%s", prefix, i, ext)
		}
		target := filepath.Join(dir, filename)
		exists, err := pathExists(target)
		if err != nil {
			return "", err
		}
		if !exists {
			return target, nil
		}
	}
	return "", errors.New("too many duplicate names")
}

func uniqueDirPath(dir, name string) (string, error) {
	base := sanitizeDirectoryName(name)
	for i := 0; i < 10000; i++ {
		dirName := base
		if i > 0 {
			dirName = fmt.Sprintf("%s__%d", base, i)
		}
		target := filepath.Join(dir, dirName)
		exists, err := pathExists(target)
		if err != nil {
			return "", err
		}
		if !exists {
			return target, nil
		}
	}
	return "", errors.New("too many duplicate directory names")
}

func pathExists(p string) (bool, error) {
	_, err := os.Stat(p)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func ensureDirExists(p string) error {
	info, err := os.Stat(p)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("path is not directory: %s", p)
	}
	return nil
}

func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !isCrossDeviceErr(err) {
		return err
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	if err := os.Remove(src); err != nil {
		return err
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o644
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return closeErr
	}
	_ = os.Chtimes(dst, info.ModTime(), info.ModTime())
	return nil
}

func isCrossDeviceErr(err error) bool {
	if errors.Is(err, syscall.EXDEV) {
		return true
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return errors.Is(linkErr.Err, syscall.EXDEV)
	}
	return false
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func isSymlinkEntry(d fs.DirEntry) bool {
	if d == nil {
		return false
	}
	return d.Type()&os.ModeSymlink != 0
}

func uniqueStrings(items []string) []string {
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		v := strings.TrimSpace(item)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func archiveWorkerCount(total int) int {
	if total <= 1 {
		return 1
	}
	if raw := strings.TrimSpace(os.Getenv("ZIPMANGA_ARCHIVE_WORKERS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			if n < 1 {
				n = 1
			}
			if n > 16 {
				n = 16
			}
			if n > total {
				n = total
			}
			return n
		}
	}

	n := runtime.GOMAXPROCS(0)
	if n < 2 {
		return 1
	}
	if n > 6 {
		n = 6
	}
	if n > total {
		n = total
	}
	if n < 1 {
		n = 1
	}
	return n
}

func ensureDirCached(dir string, cache map[string]struct{}) error {
	clean := filepath.Clean(dir)
	if _, ok := cache[clean]; ok {
		return nil
	}
	if err := os.MkdirAll(clean, 0o755); err != nil {
		return err
	}
	cache[clean] = struct{}{}
	return nil
}

func writeFileFromReader(target string, src io.Reader, mode fs.FileMode, copyBuf []byte) error {
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.CopyBuffer(out, src, copyBuf)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}
