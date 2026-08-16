package cache

import (
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"zipmanga/internal/db"
)

const pageCacheKeyPrefix = "page:"
const cacheTouchMinInterval = 5 * 60

type PageCache struct {
	store        *db.Store
	baseDir      string
	pageDir      string
	maxSizeBytes int64
	mu           sync.Mutex
	inflight     map[string]*pageBuildCall
	buildSem     chan struct{}
	touchCh      chan cacheTouchRequest
}

type pageBuildCall struct {
	done chan struct{}
	path string
	err  error
}

type cacheTouchRequest struct {
	key     string
	nowUnix int64
}

func NewPageCache(store *db.Store, baseDir string, maxSizeBytes int64) (*PageCache, error) {
	if maxSizeBytes <= 0 {
		maxSizeBytes = 10 << 30
	}
	pageDir := filepath.Join(baseDir, "pages")
	if err := os.MkdirAll(pageDir, 0o755); err != nil {
		return nil, fmt.Errorf("create page cache dir: %w", err)
	}
	cache := &PageCache{
		store:        store,
		baseDir:      baseDir,
		pageDir:      pageDir,
		maxSizeBytes: maxSizeBytes,
		inflight:     make(map[string]*pageBuildCall, 256),
		buildSem:     make(chan struct{}, 2),
		touchCh:      make(chan cacheTouchRequest, 1024),
	}
	go cache.runTouchWorker()
	return cache, nil
}

func (c *PageCache) SetMaxSizeBytes(maxSizeBytes int64) {
	if maxSizeBytes <= 0 {
		maxSizeBytes = 10 << 30
	}
	c.mu.Lock()
	c.maxSizeBytes = maxSizeBytes
	c.mu.Unlock()
}

func (c *PageCache) Ensure(key string, build func() ([]byte, error)) (string, error) {
	return c.EnsureObject(key, func() (CacheObject, error) {
		data, err := build()
		if err != nil {
			return CacheObject{}, err
		}
		return CacheObject{Data: data, Ext: ".jpg"}, nil
	})
}

func (c *PageCache) EnsureObject(key string, build func() (CacheObject, error)) (string, error) {
	if p, ok, err := c.GetPathIfExists(key); err != nil {
		return "", err
	} else if ok {
		return p, nil
	}

	c.mu.Lock()
	if p, ok, err := c.getPathIfExistsNoLock(key); err != nil {
		c.mu.Unlock()
		return "", err
	} else if ok {
		c.mu.Unlock()
		return p, nil
	}
	if call, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		<-call.done
		return call.path, call.err
	}
	call := &pageBuildCall{done: make(chan struct{})}
	c.inflight[key] = call
	c.mu.Unlock()

	c.buildSem <- struct{}{}
	defer func() { <-c.buildSem }()

	obj, err := build()
	if err != nil {
		c.finishBuildCall(key, call, "", err)
		return "", err
	}
	absPath, relPath, err := c.writePage(key, obj.Data, obj.Ext)
	if err != nil {
		c.finishBuildCall(key, call, "", err)
		return "", err
	}

	now := time.Now().Unix()
	if err := c.store.UpsertCacheEntry(db.CacheEntry{
		Key:        key,
		RelPath:    relPath,
		Size:       int64(len(obj.Data)),
		LastAccess: now,
		CreatedAt:  now,
	}); err != nil {
		c.finishBuildCall(key, call, "", err)
		return "", err
	}

	c.finishBuildCall(key, call, absPath, nil)
	_ = c.Reap()
	return absPath, nil
}

func (c *PageCache) StoreObject(key string, obj CacheObject) (string, error) {
	if len(obj.Data) == 0 {
		return "", errors.New("cache object data is empty")
	}

	if p, ok, err := c.GetPathIfExists(key); err != nil {
		return "", err
	} else if ok {
		return p, nil
	}

	c.mu.Lock()
	if p, ok, err := c.getPathIfExistsNoLock(key); err != nil {
		c.mu.Unlock()
		return "", err
	} else if ok {
		c.mu.Unlock()
		return p, nil
	}
	if call, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		<-call.done
		return call.path, call.err
	}
	call := &pageBuildCall{done: make(chan struct{})}
	c.inflight[key] = call
	c.mu.Unlock()

	absPath, relPath, err := c.writePage(key, obj.Data, obj.Ext)
	if err != nil {
		c.finishBuildCall(key, call, "", err)
		return "", err
	}

	now := time.Now().Unix()
	if err := c.store.UpsertCacheEntry(db.CacheEntry{
		Key:        key,
		RelPath:    relPath,
		Size:       int64(len(obj.Data)),
		LastAccess: now,
		CreatedAt:  now,
	}); err != nil {
		c.finishBuildCall(key, call, "", err)
		return "", err
	}

	c.finishBuildCall(key, call, absPath, nil)
	_ = c.Reap()
	return absPath, nil
}

func (c *PageCache) Has(key string) (bool, error) {
	_, ok, err := c.GetPathIfExists(key)
	return ok, err
}

func (c *PageCache) GetPathIfExists(key string) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.getPathIfExistsNoLock(key)
}

func (c *PageCache) getPathIfExistsNoLock(key string) (string, bool, error) {
	e, err := c.store.GetCacheEntry(key)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	if !stringsHasPrefix(e.Key, pageCacheKeyPrefix) {
		return "", false, nil
	}
	absPath := filepath.Join(c.baseDir, e.RelPath)
	st, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			_ = c.store.DeleteCacheEntryByID(e.ID)
			return "", false, nil
		}
		return "", false, err
	}
	if st.IsDir() {
		_ = c.store.DeleteCacheEntryByID(e.ID)
		return "", false, nil
	}

	now := time.Now().Unix()
	if now-e.LastAccess >= cacheTouchMinInterval {
		c.enqueueTouch(key, now)
	}
	return absPath, true, nil
}

func (c *PageCache) Reap() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reapNoLock()
}

func (c *PageCache) reapNoLock() error {
	maxSize := c.maxSizeBytes
	if maxSize <= 0 {
		maxSize = 10 << 30
	}

	total, err := c.store.TotalCacheSizeWithPrefix(pageCacheKeyPrefix)
	if err != nil {
		return err
	}
	if total <= maxSize {
		return nil
	}

	for total > maxSize {
		batch := 64
		if total > maxSize*2 {
			batch = 128
		}
		oldest, err := c.store.ListOldestCacheEntriesWithPrefix(pageCacheKeyPrefix, batch)
		if err != nil {
			return err
		}
		if len(oldest) == 0 {
			return nil
		}
		for _, e := range oldest {
			abs := filepath.Join(c.baseDir, e.RelPath)
			_ = os.Remove(abs)
			_ = c.store.DeleteCacheEntryByID(e.ID)
			if total <= e.Size {
				total = 0
			} else {
				total -= e.Size
			}
			if total <= maxSize {
				break
			}
		}
	}
	return nil
}

func (c *PageCache) finishBuildCall(key string, call *pageBuildCall, path string, err error) {
	c.mu.Lock()
	call.path = path
	call.err = err
	delete(c.inflight, key)
	close(call.done)
	c.mu.Unlock()
}

func (c *PageCache) writePage(key string, data []byte, ext string) (absPath string, relPath string, err error) {
	h := sha1.Sum([]byte(key))
	hexKey := hex.EncodeToString(h[:])
	dirPart := hexKey[:2]
	relPath = filepath.Join("pages", dirPart, hexKey+normalizeCacheExt(ext))
	absPath = filepath.Join(c.baseDir, relPath)

	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(absPath, data, 0o644); err != nil {
		return "", "", err
	}
	return absPath, filepath.ToSlash(relPath), nil
}

func (c *PageCache) enqueueTouch(key string, nowUnix int64) {
	if key == "" || nowUnix <= 0 || c.touchCh == nil {
		return
	}
	select {
	case c.touchCh <- cacheTouchRequest{key: key, nowUnix: nowUnix}:
	default:
	}
}

func (c *PageCache) runTouchWorker() {
	if c.touchCh == nil {
		return
	}
	for req := range c.touchCh {
		if req.key == "" || req.nowUnix <= 0 {
			continue
		}
		_ = c.store.TouchCacheEntry(req.key, req.nowUnix)
	}
}

func stringsHasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
