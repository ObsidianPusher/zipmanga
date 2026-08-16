package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"zipmanga/internal/auth"
	"zipmanga/internal/cache"
	"zipmanga/internal/config"
	"zipmanga/internal/db"
	"zipmanga/internal/mounts"
	"zipmanga/internal/scanner"
	"zipmanga/internal/server"
)

type App struct {
	cfg          config.Config
	logger       *log.Logger
	store        *db.Store
	auth         *auth.Service
	mountManager *mounts.Manager
	coverCache   *cache.CoverCache
	pageCache    *cache.PageCache
	scanner      *scanner.Service
	server       *server.Server
}

func New(configPath, webDir string, logger *log.Logger) (*App, error) {
	if logger != nil {
		logger.Printf("startup: loading config %s", configPath)
	}
	created, err := config.EnsureDefaultFile(configPath)
	if err != nil {
		return nil, err
	}
	if created && logger != nil {
		logger.Printf("config file not found, generated defaults: %s", configPath)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}

	if logger != nil {
		logger.Printf("startup: preparing storage at %s", cfg.Database.Path)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Database.Path), 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	if err := os.MkdirAll(cfg.Cache.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}

	if logger != nil {
		logger.Printf("startup: opening database")
	}
	store, err := db.Open(cfg.Database.Path, logger)
	if err != nil {
		return nil, err
	}
	if logger != nil {
		logger.Printf("startup: database opened")
	}
	if schemaVersion, schemaErr := store.SchemaVersion(); schemaErr == nil && logger != nil {
		logger.Printf("database schema version: %d", schemaVersion)
	}
	effectiveRuntime := cfg.Runtime
	if runtimeSettings, rtErr := store.GetRuntimeSettings(); rtErr == nil {
		effectiveRuntime.MemoryLimitMB = runtimeSettings.MemoryLimitMB
		effectiveRuntime.DiskCacheMB = runtimeSettings.DiskCacheMB
		effectiveRuntime.PagePrefetchPages = runtimeSettings.PagePrefetchPages
		effectiveRuntime.PageLoadParallelism = runtimeSettings.PageLoadParallelism
		effectiveRuntime.WebPQuality = runtimeSettings.WebPQuality
		effectiveRuntime.WebPMethod = runtimeSettings.WebPMethod
		effectiveRuntime.WebPConcurrency = runtimeSettings.WebPConcurrency
		effectiveRuntime.WebPOutputRoot = runtimeSettings.WebPOutputRoot
		cfg.Runtime = effectiveRuntime
		if logger != nil {
			logger.Printf("runtime settings loaded from db: memory_limit=%dMiB disk_cache=%dMiB backend_prefetch_pages=%d page_load_parallelism=%d webp_quality=%d webp_method=%d webp_concurrency=%d webp_output_root=%q",
				effectiveRuntime.MemoryLimitMB, effectiveRuntime.DiskCacheMB, effectiveRuntime.PagePrefetchPages, effectiveRuntime.PageLoadParallelism, effectiveRuntime.WebPQuality, effectiveRuntime.WebPMethod, effectiveRuntime.WebPConcurrency, effectiveRuntime.WebPOutputRoot)
		}
	} else if !errors.Is(rtErr, sql.ErrNoRows) && logger != nil {
		logger.Printf("runtime settings load warning: %v", rtErr)
	}
	applyRuntimeBudget(cfg.Runtime, logger)
	if created && logger != nil {
		logger.Printf("database initialized: %s", cfg.Database.Path)
		logger.Printf("cache directory initialized: %s", cfg.Cache.Dir)
	}

	if logger != nil {
		logger.Printf("startup: initializing auth")
	}
	ttl, err := time.ParseDuration(cfg.Server.SessionTTL)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	authSvc := auth.NewService(store, ttl)
	if err := authSvc.EnsureBootstrapAdmin(cfg.BootstrapAdmin.Username, cfg.BootstrapAdmin.Password); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("bootstrap admin: %w", err)
	}
	if logger != nil {
		logger.Printf("startup: auth ready")
	}

	if logger != nil {
		logger.Printf("startup: initializing mount manager")
	}
	mountManager := mounts.NewManager(store)

	if logger != nil {
		logger.Printf("startup: initializing cover cache")
	}
	coverCache, err := cache.NewCoverCache(store, cfg.Cache.Dir, cfg.Cache.CoverMaxEntries)
	if err != nil {
		_ = mountManager.Close()
		_ = store.Close()
		return nil, err
	}
	pageCache, err := cache.NewPageCache(store, cfg.Cache.Dir, cfg.Runtime.DiskCacheMB<<20)
	if err != nil {
		_ = mountManager.Close()
		_ = store.Close()
		return nil, err
	}

	if logger != nil {
		logger.Printf("startup: initializing scanner")
	}
	scanSvc := scanner.NewService(store, mountManager, coverCache, cfg.Cache.ThumbWidth, logger)
	if logger != nil {
		logger.Printf("startup: initializing server")
	}
	srv, err := server.New(server.Dependencies{
		Config:       cfg,
		Store:        store,
		Auth:         authSvc,
		MountManager: mountManager,
		Scanner:      scanSvc,
		CoverCache:   coverCache,
		PageCache:    pageCache,
		Logger:       logger,
		WebDir:       webDir,
	})
	if err != nil {
		_ = mountManager.Close()
		_ = store.Close()
		return nil, err
	}
	if logger != nil {
		logger.Printf("startup: initialization complete")
	}

	return &App{
		cfg:          cfg,
		logger:       logger,
		store:        store,
		auth:         authSvc,
		mountManager: mountManager,
		coverCache:   coverCache,
		pageCache:    pageCache,
		scanner:      scanSvc,
		server:       srv,
	}, nil
}

func applyRuntimeBudget(rt config.RuntimeConfig, logger *log.Logger) {
	if rt.MemoryLimitMB > 0 {
		limitBytes := rt.MemoryLimitMB << 20
		debug.SetMemoryLimit(limitBytes)
		if logger != nil {
			logger.Printf("runtime memory limit set to %d MiB", rt.MemoryLimitMB)
		}
	}
}

func (a *App) Run(ctx context.Context) error {
	scanInterval, err := time.ParseDuration(a.cfg.Scan.Interval)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var bgWG sync.WaitGroup
	bgWG.Add(3)
	go func() {
		defer bgWG.Done()
		a.periodicSessionCleanup(ctx)
	}()
	go func() {
		defer bgWG.Done()
		a.periodicCacheReap(ctx)
	}()
	go func() {
		defer bgWG.Done()
		a.periodicScan(ctx, scanInterval)
	}()

	type namedServer struct {
		name string
		srv  *http.Server
		run  func() error
	}
	servers := make([]namedServer, 0, 2)

	handler := a.server.Handler()
	if a.cfg.Server.EnableHTTP {
		httpSrv := &http.Server{
			Addr:         a.cfg.Server.HTTPAddr,
			Handler:      handler,
			ReadTimeout:  0,
			WriteTimeout: 0,
			IdleTimeout:  0,
		}
		servers = append(servers, namedServer{
			name: "http",
			srv:  httpSrv,
			run:  httpSrv.ListenAndServe,
		})
	}
	if a.cfg.Server.EnableHTTPS {
		httpsSrv := &http.Server{
			Addr:         a.cfg.Server.HTTPSAddr,
			Handler:      handler,
			ReadTimeout:  0,
			WriteTimeout: 0,
			IdleTimeout:  0,
		}
		servers = append(servers, namedServer{
			name: "https",
			srv:  httpsSrv,
			run: func() error {
				return httpsSrv.ListenAndServeTLS(a.cfg.Server.TLSCert, a.cfg.Server.TLSKey)
			},
		})
	}
	if len(servers) == 0 {
		return errors.New("no enabled server listeners")
	}

	errCh := make(chan error, len(servers))
	for _, item := range servers {
		it := item
		go func() {
			if a.logger != nil {
				a.logger.Printf("%s listening on %s", it.name, it.srv.Addr)
			}
			if err := it.run(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("%s server: %w", it.name, err)
			}
		}()
	}

	var runErr error
	select {
	case <-ctx.Done():
		runErr = nil
	case err := <-errCh:
		runErr = err
		cancel()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	for _, it := range servers {
		_ = it.srv.Shutdown(shutdownCtx)
	}

	bgWG.Wait()
	_ = a.mountManager.Close()
	_ = a.store.Close()
	return runErr
}

func (a *App) periodicSessionCleanup(ctx context.Context) {
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := a.auth.CleanupExpiredSessions(); err != nil && a.logger != nil {
				a.logger.Printf("session cleanup failed: %v", err)
			}
		}
	}
}

func (a *App) periodicCacheReap(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := a.coverCache.Reap(); err != nil && a.logger != nil {
				a.logger.Printf("cache reap failed: %v", err)
			}
			if a.pageCache != nil {
				if err := a.pageCache.Reap(); err != nil && a.logger != nil {
					a.logger.Printf("page cache reap failed: %v", err)
				}
			}
		}
	}
}

func (a *App) periodicScan(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := a.scanner.ScanChanged(ctx); err != nil && !errors.Is(err, scanner.ErrScanInProgress) && a.logger != nil {
				a.logger.Printf("periodic scan start failed: %v", err)
			}
		}
	}
}
