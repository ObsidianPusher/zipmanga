package cache

import (
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"zipmanga/internal/db"
)

type CoverCache struct {
	store      *db.Store
	baseDir    string
	coverDir   string
	maxEntries int64

	mu       sync.Mutex
	inflight map[string]*coverBuildCall
	buildSem chan struct{}
	touchCh  chan cacheTouchRequest
}

type coverBuildCall struct {
	done chan struct{}
	path string
	err  error
}

type CacheObject struct {
	Data []byte
	Ext  string
}

func NewCoverCache(store *db.Store, baseDir string, maxEntries int64) (*CoverCache, error) {
	if maxEntries <= 0 {
		maxEntries = 5000
	}
	coverDir := filepath.Join(baseDir, "covers")
	if err := os.MkdirAll(coverDir, 0o755); err != nil {
		return nil, fmt.Errorf("create cover cache dir: %w", err)
	}
	cache := &CoverCache{
		store:      store,
		baseDir:    baseDir,
		coverDir:   coverDir,
		maxEntries: maxEntries,
		inflight:   make(map[string]*coverBuildCall, 128),
		buildSem:   make(chan struct{}, 2),
		touchCh:    make(chan cacheTouchRequest, 512),
	}
	go cache.runTouchWorker()
	return cache, nil
}

func (c *CoverCache) Ensure(key string, build func() ([]byte, error)) (string, error) {
	return c.EnsureObject(key, func() (CacheObject, error) {
		data, err := build()
		if err != nil {
			return CacheObject{}, err
		}
		return CacheObject{Data: data, Ext: ".jpg"}, nil
	})
}

func (c *CoverCache) EnsureTyped(key, ext string, build func() ([]byte, error)) (string, error) {
	return c.EnsureObject(key, func() (CacheObject, error) {
		data, err := build()
		if err != nil {
			return CacheObject{}, err
		}
		return CacheObject{Data: data, Ext: ext}, nil
	})
}

func (c *CoverCache) EnsureObject(key string, build func() (CacheObject, error)) (string, error) {
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
	call := &coverBuildCall{done: make(chan struct{})}
	c.inflight[key] = call
	c.mu.Unlock()

	c.buildSem <- struct{}{}
	defer func() { <-c.buildSem }()

	obj, err := build()
	if err != nil {
		c.finishBuildCall(key, call, "", err)
		return "", err
	}
	absPath, relPath, err := c.writeCover(key, obj.Data, obj.Ext)
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

func (c *CoverCache) Has(key string) (bool, error) {
	_, ok, err := c.GetPathIfExists(key)
	return ok, err
}

func (c *CoverCache) finishBuildCall(key string, call *coverBuildCall, path string, err error) {
	c.mu.Lock()
	call.path = path
	call.err = err
	delete(c.inflight, key)
	close(call.done)
	c.mu.Unlock()
}

func (c *CoverCache) GetPathIfExists(key string) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.getPathIfExistsNoLock(key)
}

func (c *CoverCache) getPathIfExistsNoLock(key string) (string, bool, error) {
	e, err := c.store.GetCacheEntry(key)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
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

func (c *CoverCache) Reap() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reapNoLock()
}

func (c *CoverCache) reapNoLock() error {
	total, err := c.store.TotalCacheEntriesByRelPathPrefix("covers/")
	if err != nil {
		return err
	}
	if total <= c.maxEntries {
		return nil
	}

	for total > c.maxEntries {
		batch := int(c.maxEntries / 10)
		if batch < 32 {
			batch = 32
		}
		oldest, err := c.store.ListOldestCacheEntriesByRelPathPrefix("covers/", batch)
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
			total--
			if total <= c.maxEntries {
				break
			}
		}
	}
	return nil
}

func (c *CoverCache) writeCover(key string, data []byte, ext string) (absPath string, relPath string, err error) {
	h := sha1.Sum([]byte(key))
	hexKey := hex.EncodeToString(h[:])
	dirPart := hexKey[:2]
	relPath = filepath.Join("covers", dirPart, hexKey+normalizeCacheExt(ext))
	absPath = filepath.Join(c.baseDir, relPath)

	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(absPath, data, 0o644); err != nil {
		return "", "", err
	}
	return absPath, filepath.ToSlash(relPath), nil
}

func (c *CoverCache) enqueueTouch(key string, nowUnix int64) {
	if key == "" || nowUnix <= 0 || c.touchCh == nil {
		return
	}
	select {
	case c.touchCh <- cacheTouchRequest{key: key, nowUnix: nowUnix}:
	default:
	}
}

func (c *CoverCache) runTouchWorker() {
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

func normalizeCacheExt(ext string) string {
	switch filepath.Ext(ext) {
	case ".png":
		return ".png"
	case ".webp":
		return ".webp"
	case ".gif":
		return ".gif"
	case ".jpeg":
		return ".jpeg"
	case ".jpg":
		return ".jpg"
	default:
		return ".jpg"
	}
}
