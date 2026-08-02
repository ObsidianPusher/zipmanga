package mounts

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"zipmanga/internal/db"

	"github.com/studio-b12/gowebdav"
)

const (
	webDAVRequestTimeout = 45 * time.Second
	webDAVMaxRetries     = 2
	webDAVMetaCacheTTL   = 10 * time.Minute
	webDAVLocalZipTTL    = 2 * time.Hour
	webDAVLocalZipMaxN   = 16
)

type WebDAVProvider struct {
	mount      db.Mount
	client     *gowebdav.Client
	httpClient *http.Client
	baseURL    *url.URL
	root       string

	metaMu    sync.RWMutex
	fileMetas map[string]remoteFileMeta

	localZipDir string
	localZipMu  sync.Mutex
	localZips   map[string]localZipMeta
	inflightDL  map[string]*downloadState
}

type remoteFileMeta struct {
	size           int64
	rangeSupported bool
	expiresAt      time.Time
}

type localZipMeta struct {
	path      string
	size      int64
	lastUsed  time.Time
	expiresAt time.Time
}

type downloadState struct {
	done chan struct{}
	err  error
}

func NewWebDAVProvider(m db.Mount) (*WebDAVProvider, error) {
	if strings.TrimSpace(m.WebDAVURL) == "" {
		return nil, fmt.Errorf("webdav mount requires webdav_url")
	}
	u, err := url.Parse(strings.TrimSpace(m.WebDAVURL))
	if err != nil {
		return nil, fmt.Errorf("invalid webdav url: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid webdav url: missing scheme or host")
	}

	client := gowebdav.NewClient(u.String(), m.WebDAVUser, m.WebDAVPassword)
	if err := client.Connect(); err != nil {
		return nil, fmt.Errorf("webdav connect failed: %w", err)
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
	}

	return &WebDAVProvider{
		mount:   m,
		client:  client,
		baseURL: u,
		root:    trimWebDAVPath(m.RootPath),
		httpClient: &http.Client{
			Timeout:   webDAVRequestTimeout,
			Transport: transport,
		},
		fileMetas:   make(map[string]remoteFileMeta, 512),
		localZipDir: filepath.Join(os.TempDir(), "zipmanga-webdav", fmt.Sprintf("m%d", m.ID)),
		localZips:   make(map[string]localZipMeta, 64),
		inflightDL:  make(map[string]*downloadState, 32),
	}, nil
}

func (p *WebDAVProvider) ID() int64 { return p.mount.ID }

func (p *WebDAVProvider) Name() string { return p.mount.Name }

func (p *WebDAVProvider) Type() db.MountType { return p.mount.Type }

func (p *WebDAVProvider) ListDir(rel string) ([]FileEntry, error) {
	davPath := p.davPath(rel)
	var infos []os.FileInfo
	var err error
	for attempt := 0; attempt <= webDAVMaxRetries; attempt++ {
		infos, err = p.client.ReadDir(davPath)
		if err == nil {
			break
		}
		if !isTransientNetErr(err) || attempt >= webDAVMaxRetries {
			return nil, err
		}
		time.Sleep(retryBackoff(attempt))
	}

	entries := make([]FileEntry, 0, len(infos))
	baseRel := trimWebDAVPath(rel)
	for _, info := range infos {
		childRel := trimWebDAVPath(path.Join(baseRel, info.Name()))
		entries = append(entries, FileEntry{
			Path:    childRel,
			Name:    info.Name(),
			IsDir:   info.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	filtered := make([]FileEntry, 0, len(entries))
	for _, entry := range entries {
		if ShouldHideEntry(entry) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered, nil
}

func (p *WebDAVProvider) Open(rel string) (io.ReadCloser, error) {
	targetURL := p.fileURL(rel)
	resp, err := doWithRetry(p.httpClient, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, targetURL, nil)
		if err != nil {
			return nil, err
		}
		if p.mount.WebDAVUser != "" {
			req.SetBasicAuth(p.mount.WebDAVUser, p.mount.WebDAVPassword)
		}
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		_ = drainAndClose(resp)
		return nil, fmt.Errorf("open %s returned %d", targetURL, resp.StatusCode)
	}
	return resp.Body, nil
}

func (p *WebDAVProvider) OpenReaderAt(rel string) (ReadAtCloser, int64, error) {
	targetURL := p.fileURL(rel)
	if meta, ok := p.getFileMeta(targetURL); ok {
		if !meta.rangeSupported {
			return p.openReaderAtFromLocalCache(targetURL, meta.size)
		}
		r := newHTTPRangeReaderAt(p.httpClient, targetURL, p.mount.WebDAVUser, p.mount.WebDAVPassword, meta.size)
		return r, meta.size, nil
	}

	size, rangeSupported, err := probeRemoteFile(p.httpClient, targetURL, p.mount.WebDAVUser, p.mount.WebDAVPassword)
	if err != nil {
		return nil, 0, err
	}
	p.setFileMeta(targetURL, remoteFileMeta{
		size:           size,
		rangeSupported: rangeSupported,
		expiresAt:      time.Now().Add(webDAVMetaCacheTTL),
	})
	if !rangeSupported {
		return p.openReaderAtFromLocalCache(targetURL, size)
	}
	r := newHTTPRangeReaderAt(p.httpClient, targetURL, p.mount.WebDAVUser, p.mount.WebDAVPassword, size)
	return r, size, nil
}

func (p *WebDAVProvider) Close() error {
	p.localZipMu.Lock()
	for _, v := range p.localZips {
		_ = os.Remove(v.path)
	}
	p.localZips = make(map[string]localZipMeta)
	p.inflightDL = make(map[string]*downloadState)
	p.localZipMu.Unlock()
	return nil
}

func (p *WebDAVProvider) davPath(rel string) string {
	r := trimWebDAVPath(rel)
	if p.root == "" && r == "" {
		return "/"
	}
	joined := trimWebDAVPath(path.Join(p.root, r))
	return "/" + joined
}

func (p *WebDAVProvider) fileURL(rel string) string {
	u := *p.baseURL
	full := p.davPath(rel)
	if strings.HasPrefix(full, "/") {
		u.Path = path.Clean(path.Join(u.Path, full))
	} else {
		u.Path = path.Clean(path.Join(u.Path, "/"+full))
	}
	return u.String()
}

func trimWebDAVPath(v string) string {
	v = strings.TrimSpace(v)
	v = strings.ReplaceAll(v, "\\", "/")
	v = path.Clean("/" + v)
	if v == "/" {
		return ""
	}
	return strings.TrimPrefix(v, "/")
}

func probeRemoteFile(client *http.Client, targetURL, user, pass string) (size int64, rangeSupported bool, err error) {
	headResp, err := doWithRetry(client, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodHead, targetURL, nil)
		if err != nil {
			return nil, err
		}
		if user != "" {
			req.SetBasicAuth(user, pass)
		}
		return req, nil
	})
	if err != nil {
		return 0, false, err
	}
	if headResp != nil {
		if headResp.StatusCode < 400 && headResp.ContentLength > 0 {
			size = headResp.ContentLength
		}
		if headResp.StatusCode < 400 && strings.Contains(strings.ToLower(headResp.Header.Get("Accept-Ranges")), "bytes") {
			rangeSupported = true
		}
		_ = drainAndClose(headResp)
	}

	rangeResp, err := doWithRetry(client, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, targetURL, nil)
		if err != nil {
			return nil, err
		}
		if user != "" {
			req.SetBasicAuth(user, pass)
		}
		req.Header.Set("Range", "bytes=0-0")
		return req, nil
	})
	if err != nil {
		return 0, false, err
	}
	defer rangeResp.Body.Close()
	if rangeResp.StatusCode >= 400 {
		return 0, false, fmt.Errorf("range probe %s returned %d", targetURL, rangeResp.StatusCode)
	}

	switch rangeResp.StatusCode {
	case http.StatusPartialContent:
		rangeSupported = true
		if cr := rangeResp.Header.Get("Content-Range"); cr != "" {
			total, err := parseContentRangeTotal(cr)
			if err == nil && total > 0 {
				size = total
			}
		}
	case http.StatusOK:
		rangeSupported = false
	default:
		return 0, false, fmt.Errorf("range probe %s returned %d", targetURL, rangeResp.StatusCode)
	}

	if size <= 0 && rangeResp.ContentLength > 0 {
		size = rangeResp.ContentLength
	}
	if size <= 0 {
		return 0, false, errors.New("unable to determine remote file size")
	}
	return size, rangeSupported, nil
}

func parseContentRangeTotal(v string) (int64, error) {
	v = strings.TrimSpace(v)
	parts := strings.Split(v, "/")
	if len(parts) != 2 {
		return 0, errors.New("invalid content-range")
	}
	totalRaw := strings.TrimSpace(parts[1])
	if totalRaw == "*" {
		return 0, errors.New("unknown total size")
	}
	total, err := strconv.ParseInt(totalRaw, 10, 64)
	if err != nil {
		return 0, err
	}
	return total, nil
}

type httpRangeReaderAt struct {
	client *http.Client
	url    string
	user   string
	pass   string
	size   int64

	blockSize int64
	maxBlocks int

	mu     sync.Mutex
	blocks map[int64][]byte
	order  []int64
	closed bool
}

func newHTTPRangeReaderAt(client *http.Client, targetURL, user, pass string, size int64) *httpRangeReaderAt {
	return &httpRangeReaderAt{
		client:    client,
		url:       targetURL,
		user:      user,
		pass:      pass,
		size:      size,
		blockSize: 256 * 1024,
		maxBlocks: 64,
		blocks:    make(map[int64][]byte),
		order:     make([]int64, 0, 64),
	}
}

func (r *httpRangeReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("negative offset")
	}
	if len(p) == 0 {
		return 0, nil
	}

	r.mu.Lock()
	closed := r.closed
	size := r.size
	r.mu.Unlock()
	if closed {
		return 0, io.ErrClosedPipe
	}
	if off >= size {
		return 0, io.EOF
	}

	end := off + int64(len(p))
	if end > size {
		end = size
	}

	written := 0
	cur := off
	for cur < end {
		idx := cur / r.blockSize
		block, err := r.getBlock(idx)
		if err != nil {
			return written, err
		}
		blockStart := idx * r.blockSize
		inner := cur - blockStart
		if inner >= int64(len(block)) {
			return written, io.EOF
		}
		remainingInBlock := int64(len(block)) - inner
		remainingWant := end - cur
		cp := remainingInBlock
		if cp > remainingWant {
			cp = remainingWant
		}
		copy(p[written:written+int(cp)], block[inner:inner+cp])
		written += int(cp)
		cur += cp
	}

	if written < len(p) {
		return written, io.EOF
	}
	return written, nil
}

func (r *httpRangeReaderAt) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.blocks = nil
	r.order = nil
	return nil
}

func (r *httpRangeReaderAt) getBlock(idx int64) ([]byte, error) {
	r.mu.Lock()
	if b, ok := r.blocks[idx]; ok {
		r.moveToEndLocked(idx)
		r.mu.Unlock()
		return b, nil
	}
	if r.closed {
		r.mu.Unlock()
		return nil, io.ErrClosedPipe
	}
	start := idx * r.blockSize
	end := start + r.blockSize - 1
	if end >= r.size {
		end = r.size - 1
	}
	r.mu.Unlock()

	rangeHeader := fmt.Sprintf("bytes=%d-%d", start, end)
	resp, err := doWithRetry(r.client, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, r.url, nil)
		if err != nil {
			return nil, err
		}
		if r.user != "" {
			req.SetBasicAuth(r.user, r.pass)
		}
		req.Header.Set("Range", rangeHeader)
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("range request failed with status %d", resp.StatusCode)
	}
	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(buf) == 0 {
		return nil, io.EOF
	}

	if resp.StatusCode == http.StatusOK && start > 0 {
		return nil, fmt.Errorf("server does not support ranged reads")
	}
	if resp.StatusCode == http.StatusOK && start == 0 {
		maxLen := end - start + 1
		if int64(len(buf)) > maxLen {
			buf = buf[:maxLen]
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, io.ErrClosedPipe
	}
	r.blocks[idx] = buf
	r.order = append(r.order, idx)
	r.evictLocked()
	return r.blocks[idx], nil
}

func (r *httpRangeReaderAt) moveToEndLocked(idx int64) {
	for i, v := range r.order {
		if v == idx {
			r.order = append(r.order[:i], r.order[i+1:]...)
			r.order = append(r.order, idx)
			return
		}
	}
}

func (r *httpRangeReaderAt) evictLocked() {
	for len(r.order) > r.maxBlocks {
		victim := r.order[0]
		r.order = r.order[1:]
		delete(r.blocks, victim)
	}
}

func doWithRetry(client *http.Client, newReq func() (*http.Request, error)) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= webDAVMaxRetries; attempt++ {
		req, err := newReq()
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err == nil {
			if isTransientHTTPStatus(resp.StatusCode) && attempt < webDAVMaxRetries {
				_ = drainAndClose(resp)
				time.Sleep(retryBackoff(attempt))
				continue
			}
			return resp, nil
		}

		lastErr = err
		if !isTransientNetErr(err) || attempt >= webDAVMaxRetries {
			break
		}
		time.Sleep(retryBackoff(attempt))
	}
	if lastErr == nil {
		lastErr = errors.New("request failed")
	}
	return nil, lastErr
}

func retryBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	base := 200 * time.Millisecond
	d := base * time.Duration(1<<attempt)
	if d > 2*time.Second {
		return 2 * time.Second
	}
	return d
}

func isTransientHTTPStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusRequestTimeout,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func isTransientNetErr(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout() || netErr.Temporary()
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "unexpected eof") ||
		strings.Contains(msg, "connection refused")
}

func drainAndClose(resp *http.Response) error {
	if resp == nil || resp.Body == nil {
		return nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32*1024))
	return resp.Body.Close()
}

func (p *WebDAVProvider) getFileMeta(targetURL string) (remoteFileMeta, bool) {
	now := time.Now()
	p.metaMu.RLock()
	meta, ok := p.fileMetas[targetURL]
	p.metaMu.RUnlock()
	if !ok {
		return remoteFileMeta{}, false
	}
	if now.Before(meta.expiresAt) && meta.size > 0 {
		return meta, true
	}

	p.metaMu.Lock()
	delete(p.fileMetas, targetURL)
	p.metaMu.Unlock()
	return remoteFileMeta{}, false
}

func (p *WebDAVProvider) setFileMeta(targetURL string, meta remoteFileMeta) {
	if meta.size <= 0 {
		return
	}
	p.metaMu.Lock()
	p.fileMetas[targetURL] = meta
	p.metaMu.Unlock()
}

func (p *WebDAVProvider) openReaderAtFromLocalCache(targetURL string, size int64) (ReadAtCloser, int64, error) {
	if size <= 0 {
		return nil, 0, ErrReaderAtUnsupported
	}
	for {
		p.localZipMu.Lock()
		_ = os.MkdirAll(p.localZipDir, 0o755)
		p.reapLocalZipLocked()
		if f, ok, err := p.openCachedZipLocked(targetURL, size); ok || err != nil {
			p.localZipMu.Unlock()
			if err != nil {
				return nil, 0, err
			}
			return f, size, nil
		}

		if in, ok := p.inflightDL[targetURL]; ok {
			done := in.done
			p.localZipMu.Unlock()
			<-done
			if in.err != nil {
				return nil, 0, in.err
			}
			continue
		}

		in := &downloadState{done: make(chan struct{})}
		p.inflightDL[targetURL] = in
		p.localZipMu.Unlock()

		downloadErr := p.downloadZipToLocal(targetURL, size)

		p.localZipMu.Lock()
		in.err = downloadErr
		delete(p.inflightDL, targetURL)
		close(in.done)
		p.localZipMu.Unlock()
		if downloadErr != nil {
			return nil, 0, downloadErr
		}
	}
}

func (p *WebDAVProvider) openCachedZipLocked(targetURL string, size int64) (*os.File, bool, error) {
	cached, ok := p.localZips[targetURL]
	if !ok {
		return nil, false, nil
	}
	if st, err := os.Stat(cached.path); err == nil && !st.IsDir() && st.Size() == size {
		cached.lastUsed = time.Now()
		cached.expiresAt = time.Now().Add(webDAVLocalZipTTL)
		p.localZips[targetURL] = cached
		f, err := os.Open(cached.path)
		if err != nil {
			return nil, false, nil
		}
		return f, true, nil
	}
	_ = os.Remove(cached.path)
	delete(p.localZips, targetURL)
	return nil, false, nil
}

func (p *WebDAVProvider) downloadZipToLocal(targetURL string, expectedSize int64) error {
	fileName := fmt.Sprintf("%x.cbz", sha1.Sum([]byte(targetURL)))
	finalPath := filepath.Join(p.localZipDir, fileName)
	tmp, err := os.CreateTemp(p.localZipDir, "dl-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	resp, err := doWithRetry(p.httpClient, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, targetURL, nil)
		if err != nil {
			return nil, err
		}
		if p.mount.WebDAVUser != "" {
			req.SetBasicAuth(p.mount.WebDAVUser, p.mount.WebDAVPassword)
		}
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("download %s returned %d", targetURL, resp.StatusCode)
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	st, err := tmp.Stat()
	if err != nil {
		return err
	}
	if st.Size() <= 0 {
		return errors.New("downloaded empty archive")
	}
	if expectedSize > 0 && st.Size() != expectedSize {
		return fmt.Errorf("downloaded archive size mismatch: expected=%d actual=%d", expectedSize, st.Size())
	}

	_ = os.Remove(finalPath)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return err
	}
	p.localZipMu.Lock()
	p.localZips[targetURL] = localZipMeta{
		path:      finalPath,
		size:      st.Size(),
		lastUsed:  time.Now(),
		expiresAt: time.Now().Add(webDAVLocalZipTTL),
	}
	p.localZipMu.Unlock()
	return nil
}

func (p *WebDAVProvider) reapLocalZipLocked() {
	now := time.Now()
	for k, v := range p.localZips {
		if now.After(v.expiresAt) {
			_ = os.Remove(v.path)
			delete(p.localZips, k)
		}
	}
	if len(p.localZips) <= webDAVLocalZipMaxN {
		return
	}

	type kv struct {
		key string
		v   localZipMeta
	}
	items := make([]kv, 0, len(p.localZips))
	for k, v := range p.localZips {
		items = append(items, kv{key: k, v: v})
	}
	for len(items) > webDAVLocalZipMaxN {
		oldest := 0
		for i := 1; i < len(items); i++ {
			if items[i].v.lastUsed.Before(items[oldest].v.lastUsed) {
				oldest = i
			}
		}
		_ = os.Remove(items[oldest].v.path)
		delete(p.localZips, items[oldest].key)
		items = append(items[:oldest], items[oldest+1:]...)
	}

	// Best-effort clean stale files on disk that are not in metadata map.
	entries, err := os.ReadDir(p.localZipDir)
	if err != nil {
		return
	}
	known := make(map[string]struct{}, len(p.localZips))
	for _, v := range p.localZips {
		known[filepath.Clean(v.path)] = struct{}{}
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		fp := filepath.Clean(filepath.Join(p.localZipDir, ent.Name()))
		if _, ok := known[fp]; ok {
			continue
		}
		info, err := ent.Info()
		if err != nil {
			continue
		}
		if isOldFile(info, now) {
			_ = os.Remove(fp)
		}
	}
}

func isOldFile(info fs.FileInfo, now time.Time) bool {
	if info == nil {
		return false
	}
	return now.Sub(info.ModTime()) > webDAVLocalZipTTL
}
