package scanner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"zipmanga/internal/cache"
	"zipmanga/internal/comicfmt"
	"zipmanga/internal/comicutil"
	"zipmanga/internal/db"
	"zipmanga/internal/mounts"
)

var ErrScanInProgress = errors.New("scan already running")

type Status struct {
	Running      bool
	StartedAt    time.Time
	LastAt       time.Time
	LastError    string
	CurrentMount string
	MountDone    int
	MountTotal   int
	FilesScanned int
}

type Service struct {
	store        *db.Store
	mountManager *mounts.Manager
	coverCache   *cache.CoverCache
	thumbWidth   int
	logger       *log.Logger

	running atomic.Bool

	statusMu sync.RWMutex
	status   Status
}

func NewService(store *db.Store, mountManager *mounts.Manager, coverCache *cache.CoverCache, thumbWidth int, logger *log.Logger) *Service {
	if thumbWidth <= 0 {
		thumbWidth = 360
	}
	return &Service{
		store:        store,
		mountManager: mountManager,
		coverCache:   coverCache,
		thumbWidth:   thumbWidth,
		logger:       logger,
	}
}

func (s *Service) StartAsync(timeout time.Duration) error {
	if !s.beginScan() {
		return ErrScanInProgress
	}

	go func() {
		ctx := context.Background()
		cancel := func() {}
		if timeout > 0 {
			ctx, cancel = context.WithTimeout(context.Background(), timeout)
		}
		defer cancel()

		err := s.scanAll(ctx)
		s.finishScan(err)
	}()
	return nil
}

func (s *Service) ScanAll(ctx context.Context) error {
	if !s.beginScan() {
		return ErrScanInProgress
	}
	err := s.scanAll(ctx)
	s.finishScan(err)
	return err
}

func (s *Service) ScanChanged(ctx context.Context) error {
	if !s.beginScan() {
		return ErrScanInProgress
	}
	err := s.scanChanged(ctx)
	s.finishScan(err)
	return err
}

func (s *Service) Status() Status {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	st := s.status
	st.Running = s.running.Load()
	return st
}

func (s *Service) beginScan() bool {
	if !s.running.CompareAndSwap(false, true) {
		return false
	}

	now := time.Now()
	s.statusMu.Lock()
	s.status.Running = true
	s.status.StartedAt = now
	s.status.LastError = ""
	s.status.CurrentMount = ""
	s.status.MountDone = 0
	s.status.MountTotal = 0
	s.status.FilesScanned = 0
	s.statusMu.Unlock()
	return true
}

func (s *Service) finishScan(err error) {
	now := time.Now()
	s.statusMu.Lock()
	s.status.LastAt = now
	s.status.Running = false
	s.status.CurrentMount = ""
	if err != nil {
		s.status.LastError = err.Error()
	} else {
		s.status.LastError = ""
	}
	s.statusMu.Unlock()

	s.running.Store(false)

	if err != nil {
		if s.logger != nil {
			s.logger.Printf("scan failed: %v", err)
		}
	} else {
		if s.logger != nil {
			s.logger.Printf("scan finished successfully")
		}
	}
}

func (s *Service) scanAll(ctx context.Context) error {
	mountsInDB, err := s.store.ListEnabledMounts()
	if err != nil {
		return err
	}

	s.setMountTotal(len(mountsInDB))

	errMsgs := make([]string, 0)
	for i, m := range mountsInDB {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		s.setCurrentMount(m.Name, i)
		provider, err := s.mountManager.Ensure(m)
		if err != nil {
			errMsgs = append(errMsgs, fmt.Sprintf("mount %s(%d) provider is unavailable: %v", m.Name, m.ID, err))
			s.setMountDone(i + 1)
			continue
		}

		_, err = s.ScanMount(ctx, m, provider)
		if err != nil {
			errMsgs = append(errMsgs, fmt.Sprintf("scan mount %s(%d) failed: %v", m.Name, m.ID, err))
		}
		s.setMountDone(i + 1)
	}

	if len(errMsgs) > 0 {
		return errors.New(strings.Join(errMsgs, "; "))
	}
	return nil
}

func (s *Service) scanChanged(ctx context.Context) error {
	mountsInDB, err := s.store.ListEnabledMounts()
	if err != nil {
		return err
	}

	s.setMountTotal(len(mountsInDB))

	errMsgs := make([]string, 0)
	for i, m := range mountsInDB {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		s.setCurrentMount(m.Name, i)
		provider, err := s.mountManager.Ensure(m)
		if err != nil {
			errMsgs = append(errMsgs, fmt.Sprintf("mount %s(%d) provider is unavailable: %v", m.Name, m.ID, err))
			s.setMountDone(i + 1)
			continue
		}

		files, changed, err := s.mountSnapshotChanged(ctx, m, provider)
		if err != nil {
			errMsgs = append(errMsgs, fmt.Sprintf("check mount %s(%d) changed failed: %v", m.Name, m.ID, err))
			s.setMountDone(i + 1)
			continue
		}
		if !changed {
			if s.logger != nil {
				s.logger.Printf("scan skipped mount=%s(%d) unchanged files=%d", m.Name, m.ID, len(files))
			}
			s.setMountDone(i + 1)
			continue
		}

		_, err = s.scanMountWithFiles(ctx, m, provider, files)
		if err != nil {
			errMsgs = append(errMsgs, fmt.Sprintf("scan mount %s(%d) failed: %v", m.Name, m.ID, err))
		}
		s.setMountDone(i + 1)
	}

	if len(errMsgs) > 0 {
		return errors.New(strings.Join(errMsgs, "; "))
	}
	return nil
}

func (s *Service) ScanMount(ctx context.Context, mount db.Mount, provider mounts.Provider) (int, error) {
	files, err := listComicFiles(ctx, provider, func(rel string, listErr error) {
		if s.logger != nil {
			s.logger.Printf("warning: skip unreadable dir mount=%s(%d) dir=%q: %v", mount.Name, mount.ID, rel, listErr)
		}
	})
	if err != nil {
		return 0, err
	}
	return s.scanMountWithFiles(ctx, mount, provider, files)
}

func (s *Service) scanMountWithFiles(ctx context.Context, mount db.Mount, provider mounts.Provider, files []mounts.FileEntry) (int, error) {
	start := time.Now()
	existing, err := s.store.ListComicsByMount(mount.ID)
	if err != nil {
		return 0, err
	}
	existingByPath := make(map[string]db.Comic, len(existing))
	for _, c := range existing {
		existingByPath[c.Path] = c
	}

	if s.logger != nil {
		s.logger.Printf("scan mount start mount=%s(%d) comic_files=%d", mount.Name, mount.ID, len(files))
	}

	seenPaths := make([]string, 0, len(files))
	fastRemoteIndex := provider.Type() == db.MountTypeWebDAV
	for _, file := range files {
		if ctx.Err() != nil {
			return len(seenPaths), ctx.Err()
		}

		coverKey := buildCoverKey(file.Path)
		title := fileNameWithoutExt(file.Path)
		pageCount := 0
		format := comicfmt.Detect(file.Path)
		fileNameKey := comicfmt.FileNameKey(file.Path)
		prev, hasPrev := existingByPath[file.Path]
		unchanged := hasPrev && prev.MtimeUnix == file.ModTime.Unix() && prev.Size == file.Size
		if unchanged {
			if prev.Title != "" {
				title = prev.Title
			}
			pageCount = prev.PageCount
		}

		coverCached := false
		if s.coverCache != nil && coverKey != "" {
			if has, err := s.coverCache.Has(coverKey); err == nil {
				coverCached = has
			}
		}
		needPageCountProbe := pageCount <= 0 && (!fastRemoteIndex || format == comicfmt.FormatPDF)
		needCoverProbe := s.coverCache != nil && !coverCached && (!fastRemoteIndex || format == comicfmt.FormatPDF)
		if needPageCountProbe || needCoverProbe {
			probe, err := comicutil.Probe(provider, file.Path, comicutil.ProbeOptions{
				NeedPageCount: needPageCountProbe,
				NeedCover:     needCoverProbe,
				ThumbWidth:    s.thumbWidth,
				Title:         title,
			})
			if err != nil {
				// 覆盖图提取失败不应拖垮页数统计，额外回退一次仅统计页数。
				if needPageCountProbe && needCoverProbe {
					countProbe, countErr := comicutil.Probe(provider, file.Path, comicutil.ProbeOptions{
						NeedPageCount: true,
						NeedCover:     false,
						ThumbWidth:    s.thumbWidth,
						Title:         title,
					})
					if countErr == nil && countProbe.PageCount > 0 {
						pageCount = countProbe.PageCount
					}
				}
				if s.logger != nil {
					s.logger.Printf("warning: probe comic %s failed: %v", file.Path, err)
				}
			} else {
				if needPageCountProbe {
					pageCount = probe.PageCount
				}
				if needCoverProbe && len(probe.CoverJPEG) > 0 {
					coverData := probe.CoverJPEG
					_, _ = s.coverCache.Ensure(coverKey, func() ([]byte, error) {
						return coverData, nil
					})
				}
			}
		}

		comic := db.Comic{
			MountID:     mount.ID,
			Path:        file.Path,
			Title:       title,
			FileNameKey: fileNameKey,
			MtimeUnix:   file.ModTime.Unix(),
			Size:        file.Size,
			PageCount:   pageCount,
			CoverKey:    coverKey,
		}
		if err := s.store.UpsertComicForMount(mount.ID, comic); err != nil {
			return len(seenPaths), err
		}
		seenPaths = append(seenPaths, comic.Path)
		s.addFilesScanned(1)
	}

	if err := s.store.DeleteMissingComicsForMount(mount.ID, seenPaths); err != nil {
		return len(seenPaths), err
	}
	if err := s.coverCache.Reap(); err != nil && s.logger != nil {
		s.logger.Printf("cover cache reap failed: %v", err)
	}

	if s.logger != nil {
		s.logger.Printf("scan completed mount=%s(%d) files=%d took=%s", mount.Name, mount.ID, len(seenPaths), time.Since(start).Truncate(time.Millisecond))
	}
	return len(seenPaths), nil
}

func (s *Service) mountSnapshotChanged(ctx context.Context, mount db.Mount, provider mounts.Provider) ([]mounts.FileEntry, bool, error) {
	files, err := listComicFiles(ctx, provider, nil)
	if err != nil {
		return nil, false, err
	}
	existing, err := s.store.ListComicsByMount(mount.ID)
	if err != nil {
		return nil, false, err
	}
	if len(files) != len(existing) {
		return files, true, nil
	}

	existingByPath := make(map[string]db.Comic, len(existing))
	for _, c := range existing {
		existingByPath[c.Path] = c
	}
	for _, file := range files {
		prev, ok := existingByPath[file.Path]
		if !ok || prev.MtimeUnix != file.ModTime.Unix() || prev.Size != file.Size {
			return files, true, nil
		}
	}
	return files, false, nil
}

func listComicFiles(ctx context.Context, provider mounts.Provider, onSkipDirError func(rel string, err error)) ([]mounts.FileEntry, error) {
	dirs := make([]string, 0, 256)
	dirs = append(dirs, "")

	visited := make(map[string]struct{}, 512)
	visited[""] = struct{}{}

	files := make([]mounts.FileEntry, 0, 1024)

	for len(dirs) > 0 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		d := dirs[len(dirs)-1]
		dirs = dirs[:len(dirs)-1]

		entries, err := provider.ListDir(d)
		if err != nil {
			if shouldSkipDirListError(d, err) {
				if onSkipDirError != nil {
					onSkipDirError(d, err)
				}
				continue
			}
			return nil, fmt.Errorf("list dir %q: %w", d, err)
		}
		for _, e := range entries {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}

			clean := normalizedEntryPath(d, e)
			if mounts.ShouldHideEntry(e) {
				continue
			}

			if e.IsDir {
				if clean == "" {
					continue
				}
				if _, ok := visited[clean]; ok {
					continue
				}
				visited[clean] = struct{}{}
				dirs = append(dirs, clean)
				continue
			}

			if clean != "" && isComicName(clean) {
				e.Path = clean
				files = append(files, e)
			}
		}
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func shouldSkipDirListError(rel string, err error) bool {
	// 根目录无法读取时应直接失败，避免掩盖挂载不可用。
	if rel == "" {
		return false
	}
	// 子目录在扫描过程中可能被删除/改名，权限也可能临时变化；此类错误跳过并继续扫描。
	return errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) || errors.Is(err, fs.ErrPermission) || os.IsPermission(err)
}

func normalizedEntryPath(parent string, e mounts.FileEntry) string {
	pathByProvider := normalizeRelPath(e.Path)
	pathByName := normalizeRelPath(path.Join(parent, e.Name))

	if pathByProvider == "" {
		return pathByName
	}
	if pathByName == "" {
		return pathByProvider
	}

	// 部分存储后端会返回不完整/不稳定的 Path，这里优先使用父目录+Name 拼装的稳定结果。
	if !strings.EqualFold(path.Base(pathByProvider), path.Base(pathByName)) {
		return pathByName
	}
	return pathByProvider
}

func normalizeRelPath(p string) string {
	// 路径首尾空格在 POSIX 文件系统中是合法字符，不能裁剪，否则会把真实目录名改坏。
	p = strings.ReplaceAll(p, "\\", "/")
	if p == "" || p == "." {
		return ""
	}
	p = path.Clean("/" + p)
	if p == "/" || p == "/." {
		return ""
	}
	return strings.TrimPrefix(p, "/")
}

func isComicName(name string) bool {
	return comicfmt.IsSupportedPath(name)
}

func fileNameWithoutExt(p string) string {
	base := filepath.Base(p)
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext)
}

func buildCoverKey(path string) string {
	key := comicfmt.FileNameKey(path)
	if key == "" {
		return ""
	}
	return "cv6:cover:file:" + key
}

func (s *Service) setMountTotal(n int) {
	s.statusMu.Lock()
	s.status.MountTotal = n
	s.statusMu.Unlock()
}

func (s *Service) setCurrentMount(name string, idx int) {
	s.statusMu.Lock()
	s.status.CurrentMount = name
	s.status.MountDone = idx
	s.statusMu.Unlock()
}

func (s *Service) setMountDone(done int) {
	s.statusMu.Lock()
	s.status.MountDone = done
	s.statusMu.Unlock()
}

func (s *Service) addFilesScanned(n int) {
	if n <= 0 {
		return
	}
	s.statusMu.Lock()
	s.status.FilesScanned += n
	s.statusMu.Unlock()
}
