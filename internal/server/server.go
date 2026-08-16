package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	neturl "net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"zipmanga/internal/archiveutil"
	"zipmanga/internal/auth"
	"zipmanga/internal/cache"
	"zipmanga/internal/comicfmt"
	"zipmanga/internal/comicutil"
	"zipmanga/internal/config"
	"zipmanga/internal/db"
	"zipmanga/internal/imgcodec"
	"zipmanga/internal/imgenhance"
	"zipmanga/internal/mounts"
	"zipmanga/internal/pdfutil"
	"zipmanga/internal/scanner"
	webassets "zipmanga/web"
)

const sessionCookieName = "zm_session"
const maxJSONBodyBytes = 2 << 20
const maxPathLen = 4096

type ctxKey int

const userCtxKey ctxKey = 1

type Server struct {
	cfg          config.Config
	store        *db.Store
	auth         *auth.Service
	mountManager *mounts.Manager
	scanner      *scanner.Service
	coverCache   *cache.CoverCache
	pageCache    *cache.PageCache
	logger       *log.Logger

	sessionTTL time.Duration
	staticFS   fs.FS
	tmplLogin  *template.Template
	tmplApp    *template.Template

	router *http.ServeMux

	runtimeMu  sync.RWMutex
	runtimeCfg config.RuntimeConfig

	coverWarmMu       sync.Mutex
	coverWarmInFlight map[string]struct{}
	coverWarmSem      chan struct{}
	pageEnhanceSem    chan struct{}
	pageCountWarmMu   sync.Mutex
	pageCountInFlight map[int64]struct{}
	pageCountWarmSem  chan struct{}
	pageLoadMu        sync.Mutex
	pageLoadQueues    map[string]*comicPageLoadQueue
	pageLoadSem       chan struct{}

	archiveTaskMu    sync.RWMutex
	archiveTasks     map[string]*archiveWorkflowTask
	archiveTaskOrder []string
	archiveTaskSeq   uint64
	archiveTaskLimit int

	webpTaskMu   sync.Mutex
	webpTaskRuns map[string]*webpTaskRun

	nameFormatMu       sync.RWMutex
	nameFormatPreviews map[string]*nameFormatPreview
	nameFormatSeq      uint64
	nameFormatLimit    int
}

type Dependencies struct {
	Config       config.Config
	Store        *db.Store
	Auth         *auth.Service
	MountManager *mounts.Manager
	Scanner      *scanner.Service
	CoverCache   *cache.CoverCache
	PageCache    *cache.PageCache
	Logger       *log.Logger
	WebDir       string
}

func New(deps Dependencies) (*Server, error) {
	sessionTTL, err := time.ParseDuration(deps.Config.Server.SessionTTL)
	if err != nil {
		return nil, err
	}
	loginTpl, appTpl, staticFS, sourceDesc, err := loadWebUI(deps.WebDir)
	if err != nil {
		return nil, err
	}
	if deps.Logger != nil {
		deps.Logger.Printf("web ui source: %s", sourceDesc)
	}

	s := &Server{
		cfg:                deps.Config,
		store:              deps.Store,
		auth:               deps.Auth,
		mountManager:       deps.MountManager,
		scanner:            deps.Scanner,
		coverCache:         deps.CoverCache,
		pageCache:          deps.PageCache,
		logger:             deps.Logger,
		sessionTTL:         sessionTTL,
		staticFS:           staticFS,
		tmplLogin:          loginTpl,
		tmplApp:            appTpl,
		router:             http.NewServeMux(),
		runtimeCfg:         normalizeRuntimeConfig(deps.Config.Runtime),
		coverWarmInFlight:  make(map[string]struct{}, 128),
		coverWarmSem:       make(chan struct{}, 2),
		pageEnhanceSem:     make(chan struct{}, 1),
		pageCountInFlight:  make(map[int64]struct{}, 256),
		pageCountWarmSem:   make(chan struct{}, 1),
		pageLoadQueues:     make(map[string]*comicPageLoadQueue, 128),
		pageLoadSem:        make(chan struct{}, 2),
		archiveTasks:       make(map[string]*archiveWorkflowTask, 64),
		archiveTaskOrder:   make([]string, 0, 64),
		archiveTaskLimit:   120,
		webpTaskRuns:       make(map[string]*webpTaskRun, 16),
		nameFormatPreviews: make(map[string]*nameFormatPreview, 32),
		nameFormatLimit:    64,
	}
	s.registerRoutes()
	s.recoverWebPTasks()
	return s, nil
}

func (s *Server) Handler() http.Handler {
	return s.withRecovery(s.withCommonGuards(s.router))
}

func (s *Server) registerRoutes() {
	if s.staticFS != nil {
		staticHandler := http.FileServer(http.FS(s.staticFS))
		s.router.Handle("GET /static/", http.StripPrefix("/static/", staticHandler))
	}

	s.router.HandleFunc("GET /login", s.handleLoginPage)
	s.router.HandleFunc("GET /", s.authRequired(s.handleAppPage))
	s.router.HandleFunc("GET /history", s.authRequired(s.handleHistoryPage))
	s.router.HandleFunc("GET /favorites", s.authRequired(s.handleFavoritesPage))
	s.router.HandleFunc("GET /reader/{id}", s.authRequired(s.handleReaderPage))
	s.router.HandleFunc("GET /settings", s.authRequired(s.handleSettingsPage))
	s.router.HandleFunc("GET /admin", s.authRequired(s.adminOnly(s.handleAdminPage)))

	s.router.HandleFunc("POST /api/login", s.handleAPILogin)
	s.router.HandleFunc("POST /api/logout", s.authRequired(s.handleAPILogout))
	s.router.HandleFunc("GET /api/me", s.authRequired(s.handleAPIMe))
	s.router.HandleFunc("GET /api/me/settings", s.authRequired(s.handleAPIGetMyReaderSettings))
	s.router.HandleFunc("PUT /api/me/settings", s.authRequired(s.handleAPIUpdateMyReaderSettings))

	s.router.HandleFunc("GET /api/mounts", s.authRequired(s.handleAPIListMounts))
	s.router.HandleFunc("POST /api/mounts", s.authRequired(s.adminOnly(s.handleAPICreateMount)))
	s.router.HandleFunc("POST /api/mounts/browse", s.authRequired(s.adminOnly(s.handleAPIBrowseMountDirs)))
	s.router.HandleFunc("POST /api/admin/mounts/{id}/browse", s.authRequired(s.adminOnly(s.handleAPIAdminBrowseStoredMountDirs)))
	s.router.HandleFunc("PUT /api/mounts/{id}", s.authRequired(s.adminOnly(s.handleAPIUpdateMount)))
	s.router.HandleFunc("DELETE /api/mounts/{id}", s.authRequired(s.adminOnly(s.handleAPIDeleteMount)))
	s.router.HandleFunc("GET /api/root-directories", s.authRequired(s.adminOnly(s.handleAPIListRootDirectories)))
	s.router.HandleFunc("POST /api/root-directories", s.authRequired(s.adminOnly(s.handleAPICreateRootDirectory)))
	s.router.HandleFunc("DELETE /api/root-directories/{id}", s.authRequired(s.adminOnly(s.handleAPIDeleteRootDirectory)))
	s.router.HandleFunc("POST /api/admin/root-directories/{id}/browse", s.authRequired(s.adminOnly(s.handleAPIAdminBrowseRootDirectoryDirs)))

	s.router.HandleFunc("POST /api/scan", s.authRequired(s.adminOnly(s.handleAPIScanAll)))
	s.router.HandleFunc("GET /api/scan/status", s.authRequired(s.adminOnly(s.handleAPIScanStatus)))

	s.router.HandleFunc("GET /api/comics", s.authRequired(s.handleAPIListComics))
	s.router.HandleFunc("GET /api/comics/folders", s.authRequired(s.handleAPIListComicFolders))
	s.router.HandleFunc("GET /api/comics/{id}", s.authRequired(s.handleAPIComicDetail))
	s.router.HandleFunc("GET /api/comics/{id}/cover", s.authRequired(s.handleAPIComicCover))
	s.router.HandleFunc("GET /api/comics/{id}/file", s.authRequired(s.handleAPIComicFile))
	s.router.HandleFunc("GET /api/comics/{id}/pages/{index}", s.authRequired(s.handleAPIComicPage))
	s.router.HandleFunc("GET /api/comics/{id}/progress", s.authRequired(s.handleAPIGetProgress))
	s.router.HandleFunc("POST /api/comics/{id}/progress", s.authRequired(s.handleAPIUpdateProgress))
	s.router.HandleFunc("POST /api/comics/{id}/favorite", s.authRequired(s.handleAPIAddFavorite))
	s.router.HandleFunc("DELETE /api/comics/{id}/favorite", s.authRequired(s.handleAPIRemoveFavorite))
	s.router.HandleFunc("GET /api/history", s.authRequired(s.handleAPIHistory))

	s.router.HandleFunc("GET /api/admin/users", s.authRequired(s.adminOnly(s.handleAPIListUsers)))
	s.router.HandleFunc("POST /api/admin/users", s.authRequired(s.adminOnly(s.handleAPICreateUser)))
	s.router.HandleFunc("PUT /api/admin/users/{id}/password", s.authRequired(s.adminOnly(s.handleAPIUpdateUserPassword)))
	s.router.HandleFunc("GET /api/admin/runtime", s.authRequired(s.adminOnly(s.handleAPIGetRuntimeSettings)))
	s.router.HandleFunc("PUT /api/admin/runtime", s.authRequired(s.adminOnly(s.handleAPIUpdateRuntimeSettings)))
	s.router.HandleFunc("GET /api/admin/webp/tasks", s.authRequired(s.adminOnly(s.handleAPIAdminListWebPTasks)))
	s.router.HandleFunc("POST /api/admin/webp/tasks", s.authRequired(s.adminOnly(s.handleAPIAdminCreateWebPTask)))
	s.router.HandleFunc("GET /api/admin/webp/tasks/{id}", s.authRequired(s.adminOnly(s.handleAPIAdminGetWebPTask)))
	s.router.HandleFunc("POST /api/admin/webp/tasks/{id}/cancel", s.authRequired(s.adminOnly(s.handleAPIAdminCancelWebPTask)))
	s.router.HandleFunc("GET /api/admin/archive-workflow/tasks", s.authRequired(s.adminOnly(s.handleAPIAdminArchiveWorkflowListTasks)))
	s.router.HandleFunc("POST /api/admin/archive-workflow/tasks", s.authRequired(s.adminOnly(s.handleAPIAdminArchiveWorkflowCreateTask)))
	s.router.HandleFunc("GET /api/admin/archive-workflow/tasks/{id}", s.authRequired(s.adminOnly(s.handleAPIAdminArchiveWorkflowGetTask)))
	s.router.HandleFunc("POST /api/admin/archive-workflow/name-format/preview", s.authRequired(s.adminOnly(s.handleAPIAdminNameFormatPreview)))
	s.router.HandleFunc("POST /api/admin/archive-workflow/name-format/apply", s.authRequired(s.adminOnly(s.handleAPIAdminNameFormatApply)))
	s.router.HandleFunc("POST /api/admin/archive-workflow/run", s.authRequired(s.adminOnly(s.handleAPIAdminArchiveWorkflowRun)))
	s.router.HandleFunc("POST /api/me/password", s.authRequired(s.handleAPIUpdateMyPassword))
}

func (s *Server) withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if s.logger != nil {
					s.logger.Printf("panic recovered path=%s method=%s panic=%v\n%s", r.URL.Path, r.Method, rec, string(debug.Stack()))
				}
				if strings.HasPrefix(r.URL.Path, "/api/") {
					writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal server error"})
					return
				}
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withCommonGuards(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r == nil || r.URL == nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if len(r.URL.Path) > maxPathLen {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, http.StatusRequestURITooLong, map[string]any{"error": "request path is too long"})
				return
			}
			http.Error(w, "request uri too long", http.StatusRequestURITooLong)
			return
		}

		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if _, _, err := s.currentUser(r); err == nil {
		http.Redirect(w, r, s.requestAppURL(r, "/"), http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.tmplLogin.Execute(w, map[string]any{
		"Title":    "ZIP Manga Login",
		"BaseHref": s.requestBaseHref(r),
	})
}

func (s *Server) handleAppPage(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromCtx(r)
	s.renderAppPage(w, map[string]any{
		"Title":         "ZIP Manga",
		"Username":      u.Username,
		"IsAdmin":       u.IsAdmin,
		"View":          "library",
		"ReaderComicID": "",
		"BaseHref":      s.requestBaseHref(r),
	})
}

func (s *Server) handleHistoryPage(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromCtx(r)
	s.renderAppPage(w, map[string]any{
		"Title":         "观看历史 - ZIP Manga",
		"Username":      u.Username,
		"IsAdmin":       u.IsAdmin,
		"View":          "history",
		"ReaderComicID": "",
		"BaseHref":      s.requestBaseHref(r),
	})
}

func (s *Server) handleFavoritesPage(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromCtx(r)
	s.renderAppPage(w, map[string]any{
		"Title":         "收藏 - ZIP Manga",
		"Username":      u.Username,
		"IsAdmin":       u.IsAdmin,
		"View":          "favorites",
		"ReaderComicID": "",
		"BaseHref":      s.requestBaseHref(r),
	})
}

func (s *Server) handleReaderPage(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromCtx(r)
	id := strings.TrimSpace(r.PathValue("id"))
	s.renderAppPage(w, map[string]any{
		"Title":         "阅读 - ZIP Manga",
		"Username":      u.Username,
		"IsAdmin":       u.IsAdmin,
		"View":          "reader",
		"ReaderComicID": id,
		"BaseHref":      s.requestBaseHref(r),
	})
}

func (s *Server) handleAdminPage(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, s.requestAppURL(r, "/settings#scan"), http.StatusFound)
}

func (s *Server) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromCtx(r)
	s.renderAppPage(w, map[string]any{
		"Title":         "设置 - ZIP Manga",
		"Username":      u.Username,
		"IsAdmin":       u.IsAdmin,
		"View":          "settings",
		"ReaderComicID": "",
		"BaseHref":      s.requestBaseHref(r),
	})
}

func (s *Server) renderAppPage(w http.ResponseWriter, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.tmplApp.Execute(w, data)
}

func (s *Server) handleAPILogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	token, u, err := s.auth.Login(req.Username, req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid credentials"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	expires := time.Now().Add(s.sessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     s.requestCookiePath(r),
		HttpOnly: true,
		Secure:   s.isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
	})

	writeJSON(w, http.StatusOK, map[string]any{"user": userDTO(u)})
}

func (s *Server) handleAPILogout(w http.ResponseWriter, r *http.Request) {
	ck, err := r.Cookie(sessionCookieName)
	if err == nil && ck.Value != "" {
		_ = s.auth.Logout(ck.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     s.requestCookiePath(r),
		HttpOnly: true,
		Secure:   s.isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAPIMe(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromCtx(r)
	writeJSON(w, http.StatusOK, map[string]any{"user": userDTO(u)})
}

func (s *Server) handleAPIListUsers(w http.ResponseWriter, _ *http.Request) {
	users, err := s.store.ListUsers()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	items := make([]map[string]any, 0, len(users))
	for _, u := range users {
		items = append(items, userDTO(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleAPICreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		IsAdmin  bool   `json:"is_admin"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	u, err := s.auth.CreateUser(req.Username, req.Password, req.IsAdmin)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": userDTO(u)})
}

func (s *Server) handleAPIUpdateUserPassword(w http.ResponseWriter, r *http.Request) {
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid user id"})
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	hash, err := s.auth.HashPassword(req.Password)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := s.store.UpdateUserPassword(id, hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "user not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAPIUpdateMyPassword(w http.ResponseWriter, r *http.Request) {
	u, ok := userFromCtx(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if strings.TrimSpace(req.CurrentPassword) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "current_password is required"})
		return
	}
	if strings.TrimSpace(req.NewPassword) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "new_password is required"})
		return
	}
	currentUser, err := s.store.GetUserByID(u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := s.auth.VerifyPassword(currentUser.PasswordHash, req.CurrentPassword); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "current password is incorrect"})
		return
	}
	hash, err := s.auth.HashPassword(req.NewPassword)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := s.store.UpdateUserPassword(u.ID, hash); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAPIGetMyReaderSettings(w http.ResponseWriter, r *http.Request) {
	u, ok := userFromCtx(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	settings, err := s.store.GetReaderSettings(u.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusOK, map[string]any{
				"settings":   readerSettingsDTO(defaultReaderSettings(u.ID)),
				"has_custom": false,
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":   readerSettingsDTO(normalizeReaderSettings(settings)),
		"has_custom": true,
	})
}

func (s *Server) handleAPIUpdateMyReaderSettings(w http.ResponseWriter, r *http.Request) {
	u, ok := userFromCtx(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	var req struct {
		Mode            string `json:"mode"`
		Fit             string `json:"fit"`
		EnhanceZip      string `json:"enhance_zip"`
		EnhancePDF      string `json:"enhance_pdf"`
		SpreadDirection string `json:"spread_direction"`
		MobileCompact   bool   `json:"mobile_compact"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	settings := normalizeReaderSettings(db.ReaderSettings{
		UserID:          u.ID,
		Mode:            req.Mode,
		Fit:             req.Fit,
		EnhanceZip:      req.EnhanceZip,
		EnhancePDF:      req.EnhancePDF,
		SpreadDirection: req.SpreadDirection,
		MobileCompact:   req.MobileCompact,
	})
	if err := s.store.UpsertReaderSettings(settings); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	updated, err := s.store.GetReaderSettings(u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": readerSettingsDTO(normalizeReaderSettings(updated))})
}

func (s *Server) handleAPIGetRuntimeSettings(w http.ResponseWriter, _ *http.Request) {
	rt, err := s.store.GetRuntimeSettings()
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusOK, map[string]any{
				"runtime":    runtimeSettingsDTO(runtimeSettingsFromConfig(s.currentRuntimeConfig())),
				"has_custom": false,
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"runtime":    runtimeSettingsDTO(rt),
		"has_custom": true,
	})
}

func (s *Server) handleAPIUpdateRuntimeSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MemoryLimitMB       *int64  `json:"memory_limit_mb"`
		DiskCacheMB         *int64  `json:"disk_cache_mb"`
		PagePrefetchPages   *int64  `json:"page_prefetch_pages"`
		PageLoadParallelism *int64  `json:"page_load_parallelism"`
		WebPQuality         *int64  `json:"webp_quality"`
		WebPMethod          *int64  `json:"webp_method"`
		WebPConcurrency     *int64  `json:"webp_concurrency"`
		WebPOutputRoot      *string `json:"webp_output_root"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	current := s.currentRuntimeConfig()
	next := current
	if req.MemoryLimitMB != nil {
		next.MemoryLimitMB = *req.MemoryLimitMB
	}
	if req.DiskCacheMB != nil {
		next.DiskCacheMB = *req.DiskCacheMB
	}
	if req.PagePrefetchPages != nil {
		next.PagePrefetchPages = *req.PagePrefetchPages
	}
	if req.PageLoadParallelism != nil {
		next.PageLoadParallelism = *req.PageLoadParallelism
	}
	if req.WebPQuality != nil {
		next.WebPQuality = *req.WebPQuality
	}
	if req.WebPMethod != nil {
		next.WebPMethod = *req.WebPMethod
	}
	if req.WebPConcurrency != nil {
		next.WebPConcurrency = *req.WebPConcurrency
	}
	if req.WebPOutputRoot != nil {
		next.WebPOutputRoot = strings.TrimSpace(*req.WebPOutputRoot)
	}
	next = normalizeRuntimeConfig(next)
	if err := validateRuntimeConfig(next); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	record := runtimeSettingsFromConfig(next)
	if err := s.store.UpsertRuntimeSettings(record); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.applyRuntimeConfig(next)
	persisted, err := s.store.GetRuntimeSettings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"runtime":    runtimeSettingsDTO(persisted),
		"has_custom": true,
		"applied":    true,
	})
}

func (s *Server) handleAPIListMounts(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromCtx(r)
	items, err := s.store.ListMounts()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, m := range items {
		if u.IsAdmin {
			out = append(out, mountDTO(m))
		} else {
			out = append(out, mountPublicDTO(m))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleAPICreateMount(w http.ResponseWriter, r *http.Request) {
	m, err := decodeMountRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	m, err = s.store.CreateMount(m)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := s.mountManager.Reload(); err != nil && s.logger != nil {
		s.logger.Printf("mount manager reload warning: %v", err)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"mount": mountDTO(m)})
}

func (s *Server) handleAPIBrowseMountDirs(w http.ResponseWriter, r *http.Request) {
	previewMount, browsePath, err := decodeMountBrowseRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	provider, err := mounts.NewProvider(previewMount)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	defer func() { _ = provider.Close() }()

	rel := normalizeProviderPath(browsePath)
	entries, err := provider.ListDir(rel)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	type dirItem struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	dirs := make([]dirItem, 0, len(entries))
	for _, item := range entries {
		if !item.IsDir {
			continue
		}
		if mounts.ShouldHideEntry(item) {
			continue
		}
		fullPath := "/" + strings.TrimPrefix(normalizeProviderPath(item.Path), "/")
		dirs = append(dirs, dirItem{
			Name: item.Name,
			Path: fullPath,
		})
	}
	sort.Slice(dirs, func(i, j int) bool {
		return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name)
	})
	if len(dirs) > 2000 {
		dirs = dirs[:2000]
	}

	currentPath := "/" + strings.TrimPrefix(rel, "/")
	if currentPath == "//" || currentPath == "" {
		currentPath = "/"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"current_path": currentPath,
		"items":        dirs,
	})
}

func (s *Server) handleAPIAdminBrowseStoredMountDirs(w http.ResponseWriter, r *http.Request) {
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid mount id"})
		return
	}
	mount, err := s.store.GetMount(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "mount not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if mount.Type != db.MountTypeLocal {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "only local mounts support folder browsing"})
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
		type dirItem struct {
			Name string `json:"name"`
			Path string `json:"path"`
		}
		roots := make([]dirItem, 0, len(mount.RootPaths))
		for _, root := range uniqueStrings(mount.RootPaths) {
			if strings.TrimSpace(root) == "" {
				continue
			}
			roots = append(roots, dirItem{
				Name: filepath.Base(root),
				Path: root,
			})
		}
		sort.Slice(roots, func(i, j int) bool {
			return strings.ToLower(roots[i].Path) < strings.ToLower(roots[j].Path)
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"current_path": "",
			"items":        roots,
		})
		return
	}

	fullPath, err := validateStoredMountBrowsePath(mount, currentPath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	entries, err := os.ReadDir(fullPath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	type dirItem struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	dirs := make([]dirItem, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		item := mounts.FileEntry{Name: entry.Name()}
		if mounts.ShouldHideEntry(item) {
			continue
		}
		dirs = append(dirs, dirItem{
			Name: entry.Name(),
			Path: filepath.Join(fullPath, entry.Name()),
		})
	}
	sort.Slice(dirs, func(i, j int) bool {
		return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name)
	})
	if len(dirs) > 2000 {
		dirs = dirs[:2000]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"current_path": fullPath,
		"items":        dirs,
	})
}

func (s *Server) handleAPIUpdateMount(w http.ResponseWriter, r *http.Request) {
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid mount id"})
		return
	}
	m, err := decodeMountRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	m.ID = id
	if err := s.store.UpdateMount(m); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "mount not found"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := s.mountManager.Reload(); err != nil && s.logger != nil {
		s.logger.Printf("mount manager reload warning: %v", err)
	}
	updated, _ := s.store.GetMount(id)
	writeJSON(w, http.StatusOK, map[string]any{"mount": mountDTO(updated)})
}

func (s *Server) handleAPIDeleteMount(w http.ResponseWriter, r *http.Request) {
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid mount id"})
		return
	}
	if err := s.store.DeleteMount(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := s.mountManager.Reload(); err != nil && s.logger != nil {
		s.logger.Printf("mount manager reload warning: %v", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAPIListRootDirectories(w http.ResponseWriter, _ *http.Request) {
	items, err := s.store.ListRootDirectories()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, rootDirectoryDTO(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleAPICreateRootDirectory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = strings.TrimSpace(req.Path)
	}
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "name is required"})
		return
	}
	if len(name) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "name is too long"})
		return
	}

	pathValue, err := canonicalizeLocalRootPath(req.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	item, err := s.store.CreateRootDirectory(name, pathValue)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"item": rootDirectoryDTO(item)})
}

func (s *Server) handleAPIDeleteRootDirectory(w http.ResponseWriter, r *http.Request) {
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid root directory id"})
		return
	}
	if err := s.store.DeleteRootDirectory(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "root directory not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAPIScanAll(w http.ResponseWriter, r *http.Request) {
	if err := s.scanner.StartAsync(0); err != nil {
		if errors.Is(err, scanner.ErrScanInProgress) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "scan already running"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "scan started"})
}

func (s *Server) handleAPIScanStatus(w http.ResponseWriter, _ *http.Request) {
	status := s.scanner.Status()
	writeJSON(w, http.StatusOK, map[string]any{
		"running":       status.Running,
		"started_at":    status.StartedAt,
		"last_at":       status.LastAt,
		"last_error":    status.LastError,
		"current_mount": status.CurrentMount,
		"mount_done":    status.MountDone,
		"mount_total":   status.MountTotal,
		"files_scanned": status.FilesScanned,
	})
}

func (s *Server) handleAPIListComics(w http.ResponseWriter, r *http.Request) {
	u, ok := userFromCtx(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	mountID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("mount_id")), 10, 64)
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	formatFilter := strings.TrimSpace(r.URL.Query().Get("format"))
	favoriteFilter := strings.TrimSpace(r.URL.Query().Get("favorite"))
	folderFilter := strings.TrimSpace(r.URL.Query().Get("folder"))
	sortBy := strings.TrimSpace(r.URL.Query().Get("sort"))
	sortOrder := strings.TrimSpace(r.URL.Query().Get("order"))
	page := parseBoundedInt(strings.TrimSpace(r.URL.Query().Get("page")), 1, 1, 500000)
	pageSize := parseBoundedInt(strings.TrimSpace(r.URL.Query().Get("page_size")), 0, 0, 500)
	limit := parseBoundedInt(strings.TrimSpace(r.URL.Query().Get("limit")), 100, 1, 5000)
	offset := parseBoundedInt(strings.TrimSpace(r.URL.Query().Get("offset")), 0, 0, 500000)
	if pageSize > 0 {
		limit = pageSize
		offset = (page - 1) * pageSize
	}

	total, err := s.store.CountComicsForUser(u.ID, q, mountID, statusFilter, formatFilter, favoriteFilter, folderFilter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	totalPages := 0
	if total > 0 && limit > 0 {
		totalPages = (total + limit - 1) / limit
		maxPage := totalPages
		if maxPage < 1 {
			maxPage = 1
		}
		if page > maxPage {
			page = maxPage
			offset = (page - 1) * limit
		}
	}

	items, err := s.store.ListComicsForUser(u.ID, q, mountID, statusFilter, formatFilter, favoriteFilter, folderFilter, sortBy, sortOrder, limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	resp := make([]map[string]any, 0, len(items))
	for _, c := range items {
		resp = append(resp, s.comicWithProgressDTO(r, c))
	}
	if limit > 0 {
		page = (offset / limit) + 1
	}
	if total <= 0 {
		page = 1
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": resp,
		"pagination": map[string]any{
			"page":        page,
			"page_size":   limit,
			"total":       total,
			"total_pages": totalPages,
		},
	})
}

func (s *Server) handleAPIListComicFolders(w http.ResponseWriter, r *http.Request) {
	u, ok := userFromCtx(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	mountID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("mount_id")), 10, 64)
	if mountID <= 0 {
		writeJSON(w, http.StatusOK, map[string]any{"items": []map[string]any{}})
		return
	}
	mount, err := s.store.GetMount(mountID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "mount not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	folders, err := s.store.ListComicTopFoldersForUser(u.ID, mountID, strings.TrimSpace(r.URL.Query().Get("favorite")))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	items := make([]map[string]any, 0, len(folders))
	for _, raw := range folders {
		items = append(items, map[string]any{
			"value": raw,
			"label": mountFolderLabel(mount, raw),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleAPIComicDetail(w http.ResponseWriter, r *http.Request) {
	u, ok := userFromCtx(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid comic id"})
		return
	}
	c, err := s.store.GetComic(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "comic not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	format := comicfmt.Detect(c.Path)
	// 对归档漫画，页数为 0/1 都可能来自历史探测异常，先异步回温一次。
	if c.PageCount <= 0 || (comicfmt.IsArchive(format) && c.PageCount <= 1) {
		if provider, ok := s.mountManager.Get(c.MountID); ok {
			warmComic := c
			if comicfmt.IsArchive(format) && warmComic.PageCount > 0 {
				// 允许修正历史误判的“1 页”数据。
				warmComic.PageCount = 0
			}
			s.warmPageCountAsync(warmComic, provider, format)
		}
	}

	// WebDAV 封面仅在“开始阅读”时异步缓存，列表请求不触发远程开包。
	if provider, ok := s.mountManager.Get(c.MountID); ok && provider.Type() == db.MountTypeWebDAV && comicfmt.IsArchive(format) {
		s.warmCoverForReadingAsync(c, provider)
	}
	comic := s.comicDTO(r, c)
	favorite, err := s.store.IsComicFavorite(u.ID, c.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	comic["is_favorite"] = favorite
	writeJSON(w, http.StatusOK, map[string]any{"comic": comic})
}

func (s *Server) handleAPIGetProgress(w http.ResponseWriter, r *http.Request) {
	u, ok := userFromCtx(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	comicID, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid comic id"})
		return
	}
	fileNameKey := strings.TrimSpace(r.URL.Query().Get("comic_file_name_key"))
	p, err := s.store.GetReadingProgressForComic(u.ID, comicID, fileNameKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusOK, map[string]any{"progress": nil})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"progress": progressDTO(p)})
}

func (s *Server) handleAPIUpdateProgress(w http.ResponseWriter, r *http.Request) {
	u, ok := userFromCtx(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	comicID, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid comic id"})
		return
	}
	var req struct {
		LastPage         int    `json:"last_page"`
		TotalPages       int    `json:"total_pages"`
		ComicFileNameKey string `json:"comic_file_name_key"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if req.TotalPages > 200000 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "total_pages is too large"})
		return
	}
	if req.LastPage > 200000 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "last_page is too large"})
		return
	}
	if comic, err := s.store.GetComic(comicID); err == nil && comic.PageCount > 0 {
		if req.TotalPages <= 0 || req.TotalPages > comic.PageCount {
			req.TotalPages = comic.PageCount
		}
	}
	if req.LastPage < 0 {
		req.LastPage = 0
	}
	if req.TotalPages > 0 && req.LastPage >= req.TotalPages {
		req.LastPage = req.TotalPages - 1
	}
	p, err := s.store.UpsertReadingProgress(u.ID, comicID, req.ComicFileNameKey, req.LastPage, req.TotalPages)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "comic not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"progress": progressDTO(p)})
}

func (s *Server) handleAPIAddFavorite(w http.ResponseWriter, r *http.Request) {
	s.handleAPISetFavorite(w, r, true)
}

func (s *Server) handleAPIRemoveFavorite(w http.ResponseWriter, r *http.Request) {
	s.handleAPISetFavorite(w, r, false)
}

func (s *Server) handleAPISetFavorite(w http.ResponseWriter, r *http.Request, favorite bool) {
	u, ok := userFromCtx(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	comicID, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid comic id"})
		return
	}
	fileNameKey := r.URL.Query().Get("file_name_key")
	if _, err := s.store.GetComic(comicID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "comic not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := s.store.SetComicFavorite(u.ID, comicID, fileNameKey, favorite); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"comic_id":    comicID,
		"is_favorite": favorite,
	})
}

func (s *Server) handleAPIHistory(w http.ResponseWriter, r *http.Request) {
	u, ok := userFromCtx(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	limit := parseBoundedInt(strings.TrimSpace(r.URL.Query().Get("limit")), 100, 1, 100)
	offset := parseBoundedInt(strings.TrimSpace(r.URL.Query().Get("offset")), 0, 0, 500000)
	items, err := s.store.ListReadingHistory(u.ID, limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	resp := make([]map[string]any, 0, len(items))
	for _, it := range items {
		comic := s.comicDTO(r, it.Comic)
		comic["is_favorite"] = it.IsFavorite
		resp = append(resp, map[string]any{
			"progress": progressDTO(it.Progress),
			"comic":    comic,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp})
}

func (s *Server) handleAPIComicCover(w http.ResponseWriter, r *http.Request) {
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid comic id"})
		return
	}
	comic, provider, err := s.fetchComicAndProvider(id)
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("warning: fetch comic/provider failed comic=%d: %v", id, err)
		}
		s.serveFallbackCover(w, "")
		return
	}

	if provider.Type() == db.MountTypeWebDAV {
		coverKey := s.coverCacheKey(comic)
		if comicfmt.Detect(comic.Path) == comicfmt.FormatPDF {
			coverPath, err := s.coverCache.Ensure(coverKey, func() ([]byte, error) {
				return s.buildComicCover(provider, comic)
			})
			if err != nil {
				if s.logger != nil {
					s.logger.Printf("warning: build pdf cover failed comic=%d path=%s: %v", comic.ID, comic.Path, err)
				}
				s.serveFallbackCover(w, comic.Title)
				return
			}
			w.Header().Set("Cache-Control", "private, max-age=86400")
			http.ServeFile(w, r, coverPath)
			return
		}
		coverPath, ok, err := s.coverCache.GetPathIfExists(coverKey)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if !ok {
			coverPath, err = s.coverCache.Ensure(coverKey, func() ([]byte, error) {
				return s.buildComicCover(provider, comic)
			})
			if err != nil {
				if s.logger != nil {
					s.logger.Printf("warning: build webdav archive cover failed comic=%d path=%s: %v", comic.ID, comic.Path, err)
				}
				if s.tryServeRawArchiveCover(w, r, provider, comic) {
					return
				}
				s.serveFallbackCover(w, comic.Title)
				return
			}
		}
		w.Header().Set("Cache-Control", "private, max-age=86400")
		http.ServeFile(w, r, coverPath)
		return
	}

	coverKey := s.coverCacheKey(comic)
	coverPath, err := s.coverCache.Ensure(coverKey, func() ([]byte, error) {
		return s.buildComicCover(provider, comic)
	})
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("warning: build cover failed comic=%d path=%s: %v", comic.ID, comic.Path, err)
		}
		if s.tryServeRawArchiveCover(w, r, provider, comic) {
			return
		}
		s.serveFallbackCover(w, comic.Title)
		return
	}

	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, coverPath)
}

func (s *Server) tryServeRawArchiveCover(w http.ResponseWriter, _ *http.Request, provider mounts.Provider, comic db.Comic) bool {
	if provider == nil {
		return false
	}
	format := comicfmt.Detect(comic.Path)
	if !comicfmt.IsArchive(format) {
		return false
	}
	name, raw, err := archiveutil.FirstImage(provider, comic.Path, format, 64<<20)
	if err != nil {
		return false
	}
	if len(raw) == 0 {
		return false
	}

	ct := strings.ToLower(strings.TrimSpace(archiveutil.GuessImageContentType(name)))
	if ct == "" || !strings.HasPrefix(ct, "image/") {
		return false
	}
	// 部分移动端不支持 AVIF，直出会显示空白。
	if ct == "image/avif" {
		return false
	}

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=600")
	w.Header().Set("X-ZM-Cover-Raw", "1")
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	_, _ = w.Write(raw)
	return true
}

const pdfRawPageRenderWidth = 1920

func (s *Server) handleAPIComicPage(w http.ResponseWriter, r *http.Request) {
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid comic id"})
		return
	}
	pageIdx, err := parsePathInt(r, "index")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid page index"})
		return
	}

	comic, provider, err := s.fetchComicAndProvider(id)
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("warning: fetch comic/provider failed comic=%d page=%d: %v", id, pageIdx, err)
		}
		s.servePageFallbackImage(w, "")
		return
	}
	format := comicfmt.Detect(comic.Path)
	if !comicfmt.IsArchive(format) && format != comicfmt.FormatPDF {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported comic format"})
		return
	}
	if provider.Type() == db.MountTypeWebDAV {
		s.warmCoverForReadingAsync(comic, provider)
	}
	enhanceMode := normalizeEnhanceMode(r.URL.Query().Get("enhance"))
	targetWidth := parseBoundedInt(strings.TrimSpace(r.URL.Query().Get("w")), 0, 0, 7000)

	if format == comicfmt.FormatPDF {
		if comic.PageCount <= 0 {
			s.warmPageCountAsync(comic, provider, format)
		}
		if comic.PageCount > 0 && pageIdx >= comic.PageCount {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "page not found"})
			return
		}
		rawPath, rawErr := s.ensureRawComicPageCache(comic, provider, format, pageIdx)
		if rawErr != nil {
			if s.logger != nil {
				s.logger.Printf("warning: load pdf raw page failed comic=%d page=%d: %v", comic.ID, pageIdx, rawErr)
			}
			s.servePageFallbackImage(w, comic.Title)
			return
		}
		go s.prefetchComicPages(comic, provider, format, pageIdx)
		if enhanceMode == "off" {
			s.serveCachedPageFile(w, r, rawPath, "image/jpeg")
			return
		}
		raw, readErr := os.ReadFile(rawPath)
		if readErr != nil {
			if s.logger != nil {
				s.logger.Printf("warning: read pdf raw page failed comic=%d page=%d: %v", comic.ID, pageIdx, readErr)
			}
			s.servePageFallbackImage(w, comic.Title)
			return
		}
		enhanced, contentType, err := imgenhance.Enhance(raw, rawPath, imgenhance.Options{
			Mode:        imgenhance.Mode(enhanceMode),
			TargetWidth: targetWidth,
			Source:      imgenhance.SourcePDF,
		})
		if err != nil {
			if s.logger != nil {
				s.logger.Printf("warning: enhance pdf page failed comic=%d page=%d mode=%s: %v", comic.ID, pageIdx, enhanceMode, err)
			}
			s.serveCachedPageFile(w, r, rawPath, "image/jpeg")
			return
		}
		s.serveInlineImage(w, enhanced, contentType)
		return
	}

	// 归档页数历史上可能被错误写小（尤其是 0/1），不做硬拦截，直接按实际页读取。
	if comic.PageCount <= 1 {
		warmComic := comic
		if warmComic.PageCount > 0 {
			warmComic.PageCount = 0
		}
		s.warmPageCountAsync(warmComic, provider, format)
	}

	rawPath, rawErr := s.ensureRawComicPageCache(comic, provider, format, pageIdx)
	if rawErr != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "page not found"})
		return
	}
	go s.prefetchComicPages(comic, provider, format, pageIdx)
	if enhanceMode == "off" {
		s.serveCachedPageFile(w, r, rawPath, archiveutil.GuessImageContentType(rawPath))
		return
	}

	raw, readErr := os.ReadFile(rawPath)
	if readErr != nil {
		if s.logger != nil {
			s.logger.Printf("warning: read archive raw page failed comic=%d page=%d: %v", comic.ID, pageIdx, readErr)
		}
		s.serveCachedPageFile(w, r, rawPath, archiveutil.GuessImageContentType(rawPath))
		return
	}
	resized, contentType, err := imgenhance.RenderForDisplay(raw, rawPath, imgenhance.Options{
		Mode:        imgenhance.Mode(enhanceMode),
		TargetWidth: targetWidth,
		Source:      imgenhance.SourceImage,
	})
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("warning: render archive display page failed comic=%d page=%d mode=%s target_width=%d: %v", comic.ID, pageIdx, enhanceMode, targetWidth, err)
		}
		s.serveCachedPageFile(w, r, rawPath, archiveutil.GuessImageContentType(rawPath))
		return
	}
	s.serveInlineImage(w, resized, contentType)
}

func (s *Server) ensureRawComicPageCache(comic db.Comic, provider mounts.Provider, format comicfmt.Format, pageIdx int) (string, error) {
	if s.pageCache == nil {
		return "", errors.New("page cache is unavailable")
	}
	key := rawComicPageCacheKey(comic, format, pageIdx)
	if path, ok, err := s.pageCache.GetPathIfExists(key); err != nil {
		return "", err
	} else if ok {
		return path, nil
	}

	waiter := s.enqueueComicPageLoad(comic, provider, format, pageIdx, true)
	result, ok := <-waiter
	if !ok {
		return "", errors.New("page cache load task ended unexpectedly")
	}
	return result.path, result.err
}

func (s *Server) loadRawComicPageCacheDirect(comic db.Comic, provider mounts.Provider, format comicfmt.Format, pageIdx int) (string, error) {
	if s.pageCache == nil {
		return "", errors.New("page cache is unavailable")
	}

	key := rawComicPageCacheKey(comic, format, pageIdx)
	if path, ok, err := s.pageCache.GetPathIfExists(key); err != nil {
		return "", err
	} else if ok {
		return path, nil
	}

	obj, err := s.buildRawComicPageCacheObject(comic, provider, format, pageIdx)
	if err != nil {
		return "", err
	}
	return s.pageCache.StoreObject(key, obj)
}

func (s *Server) buildRawComicPageCacheObject(comic db.Comic, provider mounts.Provider, format comicfmt.Format, pageIdx int) (cache.CacheObject, error) {
	switch format {
	case comicfmt.FormatPDF:
		return s.withPageEnhanceObjectSlot(func() (cache.CacheObject, error) {
			raw, _, err := pdfutil.RenderPage(provider, comic.Path, pageIdx, pdfRawPageRenderWidth, pdfutil.RenderFormatJPEG)
			if err != nil {
				return cache.CacheObject{}, err
			}
			if len(raw) == 0 {
				return cache.CacheObject{}, errors.New("rendered page is empty")
			}
			return s.buildLossyRawPageCacheObject(comic.ID, pageIdx, "pdf", raw, ".jpg"), nil
		})
	default:
		pageName, raw, err := archiveutil.PageImage(provider, comic.Path, format, pageIdx, 64<<20)
		if err != nil {
			return cache.CacheObject{}, err
		}
		if len(raw) == 0 {
			return cache.CacheObject{}, errors.New("page image is empty")
		}
		ext := filepath.Ext(pageName)
		if ext == "" {
			ext = ".jpg"
		}
		return s.withPageEnhanceObjectSlot(func() (cache.CacheObject, error) {
			return s.buildLossyRawPageCacheObject(comic.ID, pageIdx, pageName, raw, ext), nil
		})
	}
}

func rawComicPageCacheKey(comic db.Comic, format comicfmt.Format, pageIdx int) string {
	if format == comicfmt.FormatPDF {
		return fmt.Sprintf("page:raw:v5:pdf:%d:%d:%d:%d:%d:q%d", comic.ID, pageIdx, comic.MtimeUnix, comic.Size, pdfRawPageRenderWidth, int(imgcodec.LossyWebPQuality))
	}
	return fmt.Sprintf("page:raw:v5:%s:%d:%d:%d:%d:q%d", format, comic.ID, pageIdx, comic.MtimeUnix, comic.Size, int(imgcodec.LossyWebPQuality))
}

func (s *Server) prefetchComicPages(comic db.Comic, provider mounts.Provider, format comicfmt.Format, pageIdx int) {
	if s.pageCache == nil || provider == nil {
		return
	}
	prefetch := int(s.currentRuntimeConfig().PagePrefetchPages)
	if prefetch <= 0 {
		return
	}
	start := pageIdx + 1
	end := pageIdx + prefetch
	if comic.PageCount > 0 && end >= comic.PageCount {
		end = comic.PageCount - 1
	}
	if end < start {
		return
	}
	missing := s.collectMissingRawComicPageIndexes(comic, format, start, end)
	if len(missing) == 0 {
		return
	}
	for _, idx := range missing {
		s.enqueueComicPageLoad(comic, provider, format, idx, false)
	}
}

func (s *Server) collectMissingRawComicPageIndexes(comic db.Comic, format comicfmt.Format, start, end int) []int {
	if s.pageCache == nil || end < start {
		return nil
	}
	missing := make([]int, 0, end-start+1)
	for i := start; i <= end; i++ {
		key := rawComicPageCacheKey(comic, format, i)
		if _, ok, err := s.pageCache.GetPathIfExists(key); err == nil && ok {
			continue
		}
		missing = append(missing, i)
	}
	return missing
}

func (s *Server) prefetchArchiveComicPages(comic db.Comic, provider mounts.Provider, format comicfmt.Format, missing []int) {
	if s.pageCache == nil || !comicfmt.IsArchive(format) || len(missing) == 0 {
		return
	}

	pages, err := archiveutil.PageImages(provider, comic.Path, format, missing, 64<<20)
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("warning: batch prefetch archive pages failed comic=%d start=%d end=%d: %v", comic.ID, missing[0], missing[len(missing)-1], err)
		}
		s.prefetchRawComicPagesFallback(comic, provider, format, missing)
		return
	}

	s.storePrefetchedRawComicPages(comic, format, missing, func(idx int) (cache.CacheObject, bool) {
		page, ok := pages[idx]
		if !ok || len(page.Data) == 0 {
			return cache.CacheObject{}, false
		}
		ext := filepath.Ext(page.Name)
		if ext == "" {
			ext = ".jpg"
		}
		return s.buildLossyRawPageCacheObject(comic.ID, idx, page.Name, page.Data, ext), true
	})
}

func (s *Server) prefetchPDFComicPages(comic db.Comic, provider mounts.Provider, missing []int) {
	if s.pageCache == nil || len(missing) == 0 {
		return
	}

	pages, err := pdfutil.RenderPages(provider, comic.Path, missing[0], missing[len(missing)-1], pdfRawPageRenderWidth, pdfutil.RenderFormatJPEG)
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("warning: batch prefetch pdf pages failed comic=%d start=%d end=%d: %v", comic.ID, missing[0], missing[len(missing)-1], err)
		}
		s.prefetchRawComicPagesFallback(comic, provider, comicfmt.FormatPDF, missing)
		return
	}

	s.storePrefetchedRawComicPages(comic, comicfmt.FormatPDF, missing, func(idx int) (cache.CacheObject, bool) {
		page, ok := pages[idx]
		if !ok || len(page.Data) == 0 {
			return cache.CacheObject{}, false
		}
		return s.buildLossyRawPageCacheObject(comic.ID, idx, "pdf", page.Data, cacheExtForContentType(page.ContentType)), true
	})
}

func (s *Server) buildLossyRawPageCacheObject(comicID int64, pageIdx int, pageName string, raw []byte, fallbackExt string) cache.CacheObject {
	if strings.EqualFold(strings.TrimSpace(fallbackExt), imgcodec.LossyWebPExt) {
		return cache.CacheObject{Data: raw, Ext: imgcodec.LossyWebPExt}
	}
	webp, err := imgcodec.EncodeLossyWebP(raw)
	if err == nil {
		return cache.CacheObject{Data: webp, Ext: imgcodec.LossyWebPExt}
	}
	if s.logger != nil {
		s.logger.Printf("warning: encode raw page to lossy webp failed comic=%d page=%d name=%s: %v", comicID, pageIdx, pageName, err)
	}
	if fallbackExt == "" {
		fallbackExt = ".jpg"
	}
	return cache.CacheObject{Data: raw, Ext: fallbackExt}
}

func (s *Server) storePrefetchedRawComicPages(comic db.Comic, format comicfmt.Format, indexes []int, build func(idx int) (cache.CacheObject, bool)) {
	if s.pageCache == nil || len(indexes) == 0 || build == nil {
		return
	}
	for _, idx := range indexes {
		obj, ok := build(idx)
		if !ok || len(obj.Data) == 0 {
			if s.logger != nil {
				s.logger.Printf("warning: prefetched page payload missing comic=%d page=%d", comic.ID, idx)
			}
			continue
		}
		key := rawComicPageCacheKey(comic, format, idx)
		objCopy := obj
		if _, ensureErr := s.pageCache.EnsureObject(key, func() (cache.CacheObject, error) {
			return objCopy, nil
		}); ensureErr != nil && s.logger != nil {
			s.logger.Printf("warning: store prefetched page failed comic=%d page=%d: %v", comic.ID, idx, ensureErr)
		}
	}
}

func (s *Server) prefetchRawComicPagesFallback(comic db.Comic, provider mounts.Provider, format comicfmt.Format, indexes []int) {
	for _, idx := range indexes {
		if _, err := s.ensureRawComicPageCache(comic, provider, format, idx); err != nil && s.logger != nil {
			s.logger.Printf("warning: prefetch page failed comic=%d page=%d: %v", comic.ID, idx, err)
		}
	}
}

func (s *Server) serveCachedPageFile(w http.ResponseWriter, r *http.Request, path string, fallbackContentType string) {
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if contentType == "" {
		contentType = fallbackContentType
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=43200")
	http.ServeFile(w, r, path)
}

func (s *Server) serveInlineImage(w http.ResponseWriter, data []byte, contentType string) {
	if len(data) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if contentType == "" {
		contentType = "image/jpeg"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=43200")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func (s *Server) servePageFallbackImage(w http.ResponseWriter, title string) {
	data, err := pdfutil.BuildPlaceholderCoverJPEG(title, 1280)
	if err != nil || len(data) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("X-ZM-Page-Fallback", "1")
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func (s *Server) handleAPIComicFile(w http.ResponseWriter, r *http.Request) {
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid comic id"})
		return
	}
	comic, provider, err := s.fetchComicAndProvider(id)
	if err != nil {
		writeJSON(w, statusFromErr(err), map[string]any{"error": err.Error()})
		return
	}
	if comicfmt.Detect(comic.Path) == comicfmt.FormatUnknown {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported comic format"})
		return
	}
	if err := s.serveComicFile(w, r, comic, provider); err != nil {
		writeJSON(w, statusFromErr(err), map[string]any{"error": err.Error()})
	}
}

func (s *Server) fetchComicAndProvider(id int64) (db.Comic, mounts.Provider, error) {
	comic, err := s.store.GetComic(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return db.Comic{}, nil, fmt.Errorf("comic not found")
		}
		return db.Comic{}, nil, err
	}
	provider, ok := s.mountManager.Get(comic.MountID)
	if ok {
		return comic, provider, nil
	}

	mt, err := s.store.GetMount(comic.MountID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return db.Comic{}, nil, fmt.Errorf("mount not found")
		}
		return db.Comic{}, nil, err
	}
	if !mt.Enabled {
		return db.Comic{}, nil, fmt.Errorf("mount disabled")
	}
	provider, err = s.mountManager.Ensure(mt)
	if err != nil {
		return db.Comic{}, nil, fmt.Errorf("mount provider unavailable: %w", err)
	}
	return comic, provider, nil
}

func (s *Server) readComicPageCount(provider mounts.Provider, comic db.Comic) (int, error) {
	probe, err := comicutil.Probe(provider, comic.Path, comicutil.ProbeOptions{
		NeedPageCount: true,
		NeedCover:     false,
		ThumbWidth:    s.cfg.Cache.ThumbWidth,
		Title:         comic.Title,
	})
	if err != nil {
		return 0, err
	}
	return probe.PageCount, nil
}

func (s *Server) buildComicCover(provider mounts.Provider, comic db.Comic) ([]byte, error) {
	probe, err := comicutil.Probe(provider, comic.Path, comicutil.ProbeOptions{
		NeedPageCount: false,
		NeedCover:     true,
		ThumbWidth:    s.cfg.Cache.ThumbWidth,
		Title:         comic.Title,
	})
	if err != nil {
		// 历史脏数据可能没有后缀导致格式识别失败，兜底返回可显示封面，避免前端裂图。
		if comicfmt.Detect(comic.Path) == comicfmt.FormatUnknown {
			return pdfutil.BuildPlaceholderCoverJPEG(comic.Title, s.cfg.Cache.ThumbWidth)
		}
		return nil, err
	}
	if len(probe.CoverJPEG) == 0 {
		return nil, fmt.Errorf("cover is empty")
	}
	return probe.CoverJPEG, nil
}

func (s *Server) serveFallbackCover(w http.ResponseWriter, title string) {
	data, err := pdfutil.BuildPlaceholderCoverJPEG(title, s.cfg.Cache.ThumbWidth)
	if err != nil {
		s.servePlaceholderCover(w, nil)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func (s *Server) serveComicFile(w http.ResponseWriter, r *http.Request, comic db.Comic, provider mounts.Provider) error {
	ext := strings.ToLower(filepath.Ext(comic.Path))
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		if comicfmt.Detect(comic.Path) == comicfmt.FormatPDF {
			contentType = "application/pdf"
		} else {
			contentType = "application/octet-stream"
		}
	}
	fileName := filepath.Base(comic.Path)
	if fileName == "" || fileName == "." || fileName == "/" {
		fileName = "comic" + ext
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", fileName))

	ra, size, err := provider.OpenReaderAt(comic.Path)
	if err == nil && ra != nil && size >= 0 {
		defer ra.Close()
		if size >= 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		}
		http.ServeContent(w, r, fileName, time.Unix(comic.MtimeUnix, 0), io.NewSectionReader(ra, 0, size))
		return nil
	}
	if err == nil && ra != nil {
		_ = ra.Close()
	}
	if err != nil && !errors.Is(err, mounts.ErrReaderAtUnsupported) {
		return err
	}

	rc, err := provider.Open(comic.Path)
	if err != nil {
		return err
	}
	defer rc.Close()
	if comic.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(comic.Size, 10))
	}
	_, err = io.Copy(w, rc)
	return err
}

func legacyCoverKey(comic db.Comic) string {
	return fmt.Sprintf("cover:m%d:%s:%d:%d", comic.MountID, comic.Path, comic.MtimeUnix, comic.Size)
}

func (s *Server) coverCacheKey(comic db.Comic) string {
	key := s.ensureComicCoverKey(comic)
	if strings.HasPrefix(key, "cv6:") {
		return key
	}
	return "cv6:" + key
}

func (s *Server) ensureComicCoverKey(comic db.Comic) string {
	key := strings.TrimSpace(comic.CoverKey)
	if strings.HasPrefix(key, "cv6:") {
		return key
	}
	key = strings.TrimSpace(comic.FileNameKey)
	if key == "" {
		key = comicfmt.FileNameKey(comic.Path)
	}
	if key != "" {
		key = "cv6:cover:file:" + key
	}
	if comic.ID > 0 {
		_ = s.store.UpdateComicCoverKey(comic.ID, key)
	}
	return key
}

func (s *Server) warmCoverForReadingAsync(comic db.Comic, provider mounts.Provider) {
	if provider == nil {
		return
	}
	coverKey := s.coverCacheKey(comic)
	if coverKey == "" {
		return
	}

	s.coverWarmMu.Lock()
	if _, exists := s.coverWarmInFlight[coverKey]; exists {
		s.coverWarmMu.Unlock()
		return
	}
	s.coverWarmInFlight[coverKey] = struct{}{}
	s.coverWarmMu.Unlock()

	go func() {
		defer func() {
			s.coverWarmMu.Lock()
			delete(s.coverWarmInFlight, coverKey)
			s.coverWarmMu.Unlock()
		}()

		select {
		case s.coverWarmSem <- struct{}{}:
		default:
			// Too many warmups in progress; skip and let subsequent reading requests retry.
			return
		}
		defer func() { <-s.coverWarmSem }()

		if _, ok, err := s.coverCache.GetPathIfExists(coverKey); err == nil && ok {
			return
		}
		_, err := s.coverCache.Ensure(coverKey, func() ([]byte, error) {
			return s.buildComicCover(provider, comic)
		})
		if err != nil && s.logger != nil {
			s.logger.Printf("cover warmup failed comic=%d path=%s: %v", comic.ID, comic.Path, err)
		}
	}()
}

func (s *Server) warmPageCountAsync(comic db.Comic, provider mounts.Provider, format comicfmt.Format) {
	if comic.ID <= 0 || provider == nil || format == comicfmt.FormatUnknown {
		return
	}
	if comic.PageCount > 0 {
		return
	}

	s.pageCountWarmMu.Lock()
	if _, exists := s.pageCountInFlight[comic.ID]; exists {
		s.pageCountWarmMu.Unlock()
		return
	}
	s.pageCountInFlight[comic.ID] = struct{}{}
	s.pageCountWarmMu.Unlock()

	go func() {
		defer func() {
			s.pageCountWarmMu.Lock()
			delete(s.pageCountInFlight, comic.ID)
			s.pageCountWarmMu.Unlock()
		}()

		select {
		case s.pageCountWarmSem <- struct{}{}:
		default:
			return
		}
		defer func() { <-s.pageCountWarmSem }()

		var count int
		var err error
		switch format {
		case comicfmt.FormatPDF:
			count, err = pdfutil.PageCount(provider, comic.Path)
		case comicfmt.FormatZIP, comicfmt.FormatRAR, comicfmt.Format7Z:
			count, err = s.readComicPageCount(provider, comic)
		default:
			return
		}
		if err != nil || count <= 0 {
			if err != nil && s.logger != nil {
				s.logger.Printf("warning: warm page count failed comic=%d format=%s: %v", comic.ID, format, err)
			}
			return
		}
		_ = s.store.UpdateComicPageCount(comic.ID, count)
	}()
}

func (s *Server) servePlaceholderCover(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg" width="360" height="520" viewBox="0 0 360 520"><rect width="360" height="520" fill="#e8edf4"/><text x="180" y="248" text-anchor="middle" fill="#6b7a90" font-size="28" font-family="Arial,sans-serif">WebDAV</text><text x="180" y="288" text-anchor="middle" fill="#8a97ab" font-size="18" font-family="Arial,sans-serif">开始阅读后生成封面</text></svg>`)
}

func (s *Server) authRequired(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, renewedExpiresAt, err := s.currentUser(r)
		if err != nil {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			} else {
				http.Redirect(w, r, s.requestAppURL(r, "/login"), http.StatusFound)
			}
			return
		}
		if !renewedExpiresAt.IsZero() {
			http.SetCookie(w, &http.Cookie{
				Name:     sessionCookieName,
				Value:    mustCookieValue(r, sessionCookieName),
				Path:     s.requestCookiePath(r),
				HttpOnly: true,
				Secure:   s.isSecureRequest(r),
				SameSite: http.SameSiteLaxMode,
				Expires:  renewedExpiresAt,
			})
		}
		next(w, withUserCtx(r, u))
	}
}

func (s *Server) adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := userFromCtx(r)
		if !ok || !u.IsAdmin {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "admin required"})
			return
		}
		next(w, r)
	}
}

func (s *Server) currentUser(r *http.Request) (db.User, time.Time, error) {
	ck, err := r.Cookie(sessionCookieName)
	if err != nil {
		return db.User{}, time.Time{}, err
	}
	if strings.TrimSpace(ck.Value) == "" {
		return db.User{}, time.Time{}, sql.ErrNoRows
	}
	user, expiresAt, renewed, err := s.auth.AuthByTokenWithRefresh(ck.Value)
	if err != nil {
		return db.User{}, time.Time{}, err
	}
	if renewed {
		return user, expiresAt, nil
	}
	return user, time.Time{}, nil
}

func decodeMountRequest(r *http.Request) (db.Mount, error) {
	var req struct {
		Name           string   `json:"name"`
		Type           string   `json:"type"`
		RootPath       string   `json:"root_path"`
		RootPaths      []string `json:"root_paths"`
		SMBHost        string   `json:"smb_host"`
		SMBPort        int      `json:"smb_port"`
		SMBShare       string   `json:"smb_share"`
		SMBUser        string   `json:"smb_user"`
		SMBPassword    string   `json:"smb_password"`
		SMBDomain      string   `json:"smb_domain"`
		WebDAVURL      string   `json:"webdav_url"`
		WebDAVUser     string   `json:"webdav_user"`
		WebDAVPassword string   `json:"webdav_password"`
		Enabled        bool     `json:"enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return db.Mount{}, err
	}

	mt := db.MountType(strings.ToLower(strings.TrimSpace(req.Type)))
	rootPaths, err := normalizeRootPathsInput(mt, req.RootPath, req.RootPaths)
	if err != nil {
		return db.Mount{}, err
	}
	m := db.Mount{
		Name:           strings.TrimSpace(req.Name),
		Type:           mt,
		RootPath:       rootPaths[0],
		RootPaths:      rootPaths,
		SMBHost:        strings.TrimSpace(req.SMBHost),
		SMBPort:        req.SMBPort,
		SMBShare:       strings.TrimSpace(req.SMBShare),
		SMBUser:        strings.TrimSpace(req.SMBUser),
		SMBPassword:    req.SMBPassword,
		SMBDomain:      strings.TrimSpace(req.SMBDomain),
		WebDAVURL:      strings.TrimSpace(req.WebDAVURL),
		WebDAVUser:     strings.TrimSpace(req.WebDAVUser),
		WebDAVPassword: req.WebDAVPassword,
		Enabled:        req.Enabled,
	}

	if m.Name == "" {
		return db.Mount{}, fmt.Errorf("name is required")
	}
	if len(m.Name) > 128 {
		return db.Mount{}, fmt.Errorf("name is too long")
	}

	switch m.Type {
	case db.MountTypeLocal:
		if m.RootPath == "" {
			return db.Mount{}, fmt.Errorf("root_path is required for local mount")
		}
	case db.MountTypeSMB:
		if m.SMBHost == "" || m.SMBShare == "" {
			return db.Mount{}, fmt.Errorf("smb_host and smb_share are required for smb mount")
		}
		if m.SMBPort == 0 {
			m.SMBPort = 445
		} else if m.SMBPort < 1 || m.SMBPort > 65535 {
			return db.Mount{}, fmt.Errorf("smb_port is out of range")
		}
	case db.MountTypeWebDAV:
		if m.WebDAVURL == "" {
			return db.Mount{}, fmt.Errorf("webdav_url is required for webdav mount")
		}
		u, err := neturl.Parse(m.WebDAVURL)
		if err != nil || u.Host == "" {
			return db.Mount{}, fmt.Errorf("webdav_url is invalid")
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return db.Mount{}, fmt.Errorf("webdav_url scheme must be http or https")
		}
	default:
		return db.Mount{}, fmt.Errorf("unsupported mount type")
	}

	return m, nil
}

func mustCookieValue(r *http.Request, name string) string {
	if r == nil {
		return ""
	}
	ck, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return ck.Value
}

func cacheExtForContentType(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/jpeg":
		return ".jpg"
	default:
		return ".jpg"
	}
}

func decodeMountBrowseRequest(r *http.Request) (db.Mount, string, error) {
	var req struct {
		Type           string   `json:"type"`
		Path           string   `json:"path"`
		SMBHost        string   `json:"smb_host"`
		SMBPort        int      `json:"smb_port"`
		SMBShare       string   `json:"smb_share"`
		SMBUser        string   `json:"smb_user"`
		SMBPassword    string   `json:"smb_password"`
		SMBDomain      string   `json:"smb_domain"`
		WebDAVURL      string   `json:"webdav_url"`
		WebDAVUser     string   `json:"webdav_user"`
		WebDAVPassword string   `json:"webdav_password"`
		RootPath       string   `json:"root_path"`
		RootPaths      []string `json:"root_paths"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return db.Mount{}, "", err
	}

	mt := db.MountType(strings.ToLower(strings.TrimSpace(req.Type)))
	m := db.Mount{
		Name:           "browse-preview",
		Type:           mt,
		RootPath:       "/",
		RootPaths:      []string{"/"},
		SMBHost:        strings.TrimSpace(req.SMBHost),
		SMBPort:        req.SMBPort,
		SMBShare:       strings.TrimSpace(req.SMBShare),
		SMBUser:        strings.TrimSpace(req.SMBUser),
		SMBPassword:    req.SMBPassword,
		SMBDomain:      strings.TrimSpace(req.SMBDomain),
		WebDAVURL:      strings.TrimSpace(req.WebDAVURL),
		WebDAVUser:     strings.TrimSpace(req.WebDAVUser),
		WebDAVPassword: req.WebDAVPassword,
		Enabled:        true,
	}

	browseRootPath := "/"
	if strings.TrimSpace(req.RootPath) != "" {
		switch mt {
		case db.MountTypeLocal:
			normalized, err := canonicalizeLocalRootPath(req.RootPath)
			if err != nil {
				return db.Mount{}, "", err
			}
			browseRootPath = normalized
		default:
			normalized, err := normalizeRootPathForStorage(req.RootPath)
			if err != nil {
				return db.Mount{}, "", err
			}
			browseRootPath = normalized
		}
	}
	m.RootPath = browseRootPath
	m.RootPaths = []string{browseRootPath}

	switch m.Type {
	case db.MountTypeLocal:
		// local 浏览使用请求中的 root_path 作为浏览根。
	case db.MountTypeSMB:
		if m.SMBHost == "" || m.SMBShare == "" {
			return db.Mount{}, "", fmt.Errorf("smb_host and smb_share are required for smb mount")
		}
		if m.SMBPort == 0 {
			m.SMBPort = 445
		} else if m.SMBPort < 1 || m.SMBPort > 65535 {
			return db.Mount{}, "", fmt.Errorf("smb_port is out of range")
		}
	case db.MountTypeWebDAV:
		if m.WebDAVURL == "" {
			return db.Mount{}, "", fmt.Errorf("webdav_url is required for webdav mount")
		}
		u, err := neturl.Parse(m.WebDAVURL)
		if err != nil || u.Host == "" {
			return db.Mount{}, "", fmt.Errorf("webdav_url is invalid")
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return db.Mount{}, "", fmt.Errorf("webdav_url scheme must be http or https")
		}
	default:
		return db.Mount{}, "", fmt.Errorf("unsupported mount type")
	}

	browsePath := strings.TrimSpace(req.Path)
	if browsePath == "" {
		browsePath = "/"
	}
	if len(browsePath) > 2048 {
		return db.Mount{}, "", fmt.Errorf("path is too long")
	}
	if strings.ContainsRune(browsePath, '\x00') {
		return db.Mount{}, "", fmt.Errorf("path contains invalid characters")
	}
	return m, browsePath, nil
}

func normalizeRootPathsInput(mt db.MountType, rootPath string, rootPaths []string) ([]string, error) {
	merged := make([]string, 0, len(rootPaths)+1)
	merged = append(merged, rootPaths...)
	if strings.TrimSpace(rootPath) != "" {
		merged = append(merged, rootPath)
	}
	if len(merged) == 0 && mt != db.MountTypeLocal {
		merged = append(merged, "/")
	}
	if len(merged) == 0 && mt == db.MountTypeLocal {
		return nil, fmt.Errorf("root_path is required for local mount")
	}

	out := make([]string, 0, len(merged))
	seen := make(map[string]struct{}, len(merged))
	for _, raw := range merged {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		var (
			v   string
			err error
		)
		if mt == db.MountTypeLocal {
			v, err = canonicalizeLocalRootPath(raw)
		} else {
			v, err = normalizeRootPathForStorage(raw)
		}
		if err != nil {
			return nil, err
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 && mt == db.MountTypeLocal {
		return nil, fmt.Errorf("root_path is required for local mount")
	}
	if len(out) == 0 {
		out = append(out, "/")
	}
	return out, nil
}

func validateStoredMountBrowsePath(mount db.Mount, rawPath string) (string, error) {
	target, err := canonicalizeLocalRootPath(rawPath)
	if err != nil {
		return "", err
	}
	roots := mount.RootPaths
	if len(roots) == 0 && strings.TrimSpace(mount.RootPath) != "" {
		roots = []string{mount.RootPath}
	}
	for _, root := range uniqueStrings(roots) {
		canonicalRoot, rootErr := canonicalizeLocalRootPath(root)
		if rootErr != nil {
			continue
		}
		rootPrefix := canonicalRoot + string(filepath.Separator)
		if target == canonicalRoot || strings.HasPrefix(target, rootPrefix) {
			return target, nil
		}
	}
	return "", fmt.Errorf("path is outside mount roots")
}

func normalizeRootPathForStorage(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", fmt.Errorf("path is required")
	}
	if len(v) > 2048 {
		return "", fmt.Errorf("path is too long")
	}
	if strings.ContainsRune(v, '\x00') {
		return "", fmt.Errorf("path contains invalid characters")
	}
	v = strings.ReplaceAll(v, "\\", "/")
	if !strings.HasPrefix(v, "/") {
		v = "/" + v
	}
	clean := path.Clean(v)
	if clean == "" {
		clean = "/"
	}
	return clean, nil
}

func canonicalizeLocalRootPath(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", fmt.Errorf("path is required")
	}
	if len(v) > 2048 {
		return "", fmt.Errorf("path is too long")
	}
	if strings.ContainsRune(v, '\x00') {
		return "", fmt.Errorf("path contains invalid characters")
	}

	p := filepath.Clean(filepath.FromSlash(v))
	if !filepath.IsAbs(p) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", fmt.Errorf("resolve absolute path failed: %w", err)
		}
		p = abs
	}

	resolved := p
	if eval, err := filepath.EvalSymlinks(p); err == nil && strings.TrimSpace(eval) != "" {
		resolved = filepath.Clean(eval)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("local root not accessible: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("local root is not directory: %s", resolved)
	}
	return resolved, nil
}

func normalizeProviderPath(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" || v == "/" {
		return ""
	}
	v = strings.ReplaceAll(v, "\\", "/")
	clean := path.Clean("/" + v)
	return strings.TrimPrefix(clean, "/")
}

func parsePathInt64(r *http.Request, key string) (int64, error) {
	v, err := strconv.ParseInt(strings.TrimSpace(r.PathValue(key)), 10, 64)
	if err != nil {
		return 0, err
	}
	if v <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}
	return v, nil
}

func parsePathInt(r *http.Request, key string) (int, error) {
	v, err := strconv.Atoi(strings.TrimSpace(r.PathValue(key)))
	if err != nil {
		return 0, err
	}
	if v < 0 {
		return 0, fmt.Errorf("%s must be non-negative", key)
	}
	return v, nil
}

func decodeJSON(r *http.Request, out any) error {
	if r == nil || out == nil {
		return errors.New("invalid request")
	}
	defer r.Body.Close()
	if ct := strings.TrimSpace(r.Header.Get("Content-Type")); ct != "" {
		mt, _, err := mime.ParseMediaType(ct)
		if err == nil && mt != "application/json" {
			return errors.New("content-type must be application/json")
		}
	}

	dec := json.NewDecoder(io.LimitReader(r.Body, maxJSONBodyBytes+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

func parseBoundedInt(raw string, def, minV, maxV int) int {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		v = def
	}
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func userDTO(u db.User) map[string]any {
	return map[string]any{
		"id":         u.ID,
		"username":   u.Username,
		"is_admin":   u.IsAdmin,
		"created_at": u.CreatedAt,
	}
}

func readerSettingsDTO(v db.ReaderSettings) map[string]any {
	out := map[string]any{
		"user_id":          v.UserID,
		"mode":             v.Mode,
		"fit":              v.Fit,
		"enhance_zip":      v.EnhanceZip,
		"enhance_pdf":      v.EnhancePDF,
		"spread_direction": v.SpreadDirection,
		"mobile_compact":   v.MobileCompact,
	}
	if !v.CreatedAt.IsZero() {
		out["created_at"] = v.CreatedAt
	} else {
		out["created_at"] = nil
	}
	if !v.UpdatedAt.IsZero() {
		out["updated_at"] = v.UpdatedAt
	} else {
		out["updated_at"] = nil
	}
	return out
}

func runtimeSettingsDTO(v db.RuntimeSettings) map[string]any {
	out := map[string]any{
		"memory_limit_mb":       v.MemoryLimitMB,
		"disk_cache_mb":         v.DiskCacheMB,
		"page_prefetch_pages":   v.PagePrefetchPages,
		"page_load_parallelism": v.PageLoadParallelism,
		"webp_quality":          v.WebPQuality,
		"webp_method":           v.WebPMethod,
		"webp_concurrency":      v.WebPConcurrency,
		"webp_output_root":      v.WebPOutputRoot,
	}
	if !v.CreatedAt.IsZero() {
		out["created_at"] = v.CreatedAt
	} else {
		out["created_at"] = nil
	}
	if !v.UpdatedAt.IsZero() {
		out["updated_at"] = v.UpdatedAt
	} else {
		out["updated_at"] = nil
	}
	return out
}

func runtimeSettingsFromConfig(rt config.RuntimeConfig) db.RuntimeSettings {
	normalized := normalizeRuntimeConfig(rt)
	return db.RuntimeSettings{
		ID:                  1,
		MemoryLimitMB:       normalized.MemoryLimitMB,
		DiskCacheMB:         normalized.DiskCacheMB,
		PagePrefetchPages:   normalized.PagePrefetchPages,
		PageLoadParallelism: normalized.PageLoadParallelism,
		WebPQuality:         normalized.WebPQuality,
		WebPMethod:          normalized.WebPMethod,
		WebPConcurrency:     normalized.WebPConcurrency,
		WebPOutputRoot:      strings.TrimSpace(normalized.WebPOutputRoot),
	}
}

func normalizeRuntimeConfig(rt config.RuntimeConfig) config.RuntimeConfig {
	def := config.Default().Runtime
	if rt.MemoryLimitMB <= 0 {
		rt.MemoryLimitMB = def.MemoryLimitMB
	}
	if rt.DiskCacheMB <= 0 {
		rt.DiskCacheMB = def.DiskCacheMB
	}
	if rt.PagePrefetchPages < 0 {
		rt.PagePrefetchPages = def.PagePrefetchPages
	}
	if rt.PageLoadParallelism <= 0 {
		rt.PageLoadParallelism = def.PageLoadParallelism
	}
	if rt.WebPQuality <= 0 {
		rt.WebPQuality = def.WebPQuality
	}
	if rt.WebPMethod < 0 {
		rt.WebPMethod = def.WebPMethod
	}
	if rt.WebPConcurrency <= 0 {
		rt.WebPConcurrency = def.WebPConcurrency
	}
	rt.WebPOutputRoot = strings.TrimSpace(rt.WebPOutputRoot)
	return rt
}

func validateRuntimeConfig(rt config.RuntimeConfig) error {
	if rt.MemoryLimitMB < 256 {
		return errors.New("memory_limit_mb must be >= 256")
	}
	if rt.MemoryLimitMB > 32768 {
		return errors.New("memory_limit_mb must be <= 32768")
	}
	if rt.DiskCacheMB < 256 {
		return errors.New("disk_cache_mb must be >= 256")
	}
	if rt.DiskCacheMB > 1048576 {
		return errors.New("disk_cache_mb must be <= 1048576")
	}
	if rt.PagePrefetchPages < 0 {
		return errors.New("page_prefetch_pages must be >= 0")
	}
	if rt.PagePrefetchPages > 100 {
		return errors.New("page_prefetch_pages must be <= 100")
	}
	if rt.PageLoadParallelism < 1 {
		return errors.New("page_load_parallelism must be >= 1")
	}
	if rt.PageLoadParallelism > 16 {
		return errors.New("page_load_parallelism must be <= 16")
	}
	if rt.WebPQuality < 1 {
		return errors.New("webp_quality must be >= 1")
	}
	if rt.WebPQuality > 100 {
		return errors.New("webp_quality must be <= 100")
	}
	if rt.WebPMethod < 0 {
		return errors.New("webp_method must be >= 0")
	}
	if rt.WebPMethod > 6 {
		return errors.New("webp_method must be <= 6")
	}
	if rt.WebPConcurrency < 1 {
		return errors.New("webp_concurrency must be >= 1")
	}
	if rt.WebPConcurrency > 16 {
		return errors.New("webp_concurrency must be <= 16")
	}
	return nil
}

func (s *Server) currentRuntimeConfig() config.RuntimeConfig {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return s.runtimeCfg
}

func (s *Server) applyRuntimeConfig(rt config.RuntimeConfig) {
	normalized := normalizeRuntimeConfig(rt)
	debug.SetMemoryLimit(normalized.MemoryLimitMB << 20)

	s.runtimeMu.Lock()
	s.runtimeCfg = normalized
	s.runtimeMu.Unlock()
	if s.pageCache != nil {
		s.pageCache.SetMaxSizeBytes(normalized.DiskCacheMB << 20)
		go func() {
			_ = s.pageCache.Reap()
		}()
	}
	if s.logger != nil {
		s.logger.Printf("runtime settings updated: memory_limit=%dMiB disk_cache=%dMiB backend_prefetch_pages=%d page_load_parallelism=%d webp_quality=%d webp_method=%d webp_concurrency=%d webp_output_root=%q",
			normalized.MemoryLimitMB, normalized.DiskCacheMB, normalized.PagePrefetchPages, normalized.PageLoadParallelism, normalized.WebPQuality, normalized.WebPMethod, normalized.WebPConcurrency, normalized.WebPOutputRoot)
	}
}

func (s *Server) currentPageLoadParallelism() int {
	value := int(s.currentRuntimeConfig().PageLoadParallelism)
	if value < 1 {
		return int(config.Default().Runtime.PageLoadParallelism)
	}
	if value > 16 {
		return 16
	}
	return value
}

func (s *Server) withPageEnhanceSlot(fn func() ([]byte, error)) ([]byte, error) {
	if fn == nil {
		return nil, errors.New("enhance callback is nil")
	}
	if s.pageEnhanceSem == nil {
		return fn()
	}
	s.pageEnhanceSem <- struct{}{}
	defer func() {
		<-s.pageEnhanceSem
		s.maybeReleaseMemory("page-enhance")
	}()
	return fn()
}

func (s *Server) withPageEnhanceObjectSlot(fn func() (cache.CacheObject, error)) (cache.CacheObject, error) {
	if fn == nil {
		return cache.CacheObject{}, errors.New("enhance callback is nil")
	}
	if s.pageEnhanceSem == nil {
		return fn()
	}
	s.pageEnhanceSem <- struct{}{}
	defer func() {
		<-s.pageEnhanceSem
		s.maybeReleaseMemory("page-enhance-object")
	}()
	return fn()
}

type memorySnapshot struct {
	heapAlloc    uint64
	heapRetained uint64
}

func captureMemorySnapshot() memorySnapshot {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	retained := stats.HeapInuse
	if stats.HeapIdle > stats.HeapReleased {
		retained += stats.HeapIdle - stats.HeapReleased
	}
	return memorySnapshot{
		heapAlloc:    stats.HeapAlloc,
		heapRetained: retained,
	}
}

func bytesToMiB(v uint64) uint64 {
	return v >> 20
}

func (s *Server) maybeReleaseMemory(reason string) {
	limitMB := s.currentRuntimeConfig().MemoryLimitMB
	if limitMB <= 0 {
		return
	}
	limitBytes := uint64(limitMB) << 20
	before := captureMemorySnapshot()
	if before.heapAlloc < (limitBytes*3)/5 && before.heapRetained < (limitBytes*3)/4 {
		return
	}

	runtime.GC()
	afterGC := captureMemorySnapshot()
	if afterGC.heapAlloc < limitBytes/2 && afterGC.heapRetained < (limitBytes*7)/10 {
		return
	}

	debug.FreeOSMemory()
	if s.logger != nil {
		afterFree := captureMemorySnapshot()
		s.logger.Printf(
			"memory reclaim after %s: heap_alloc=%d->%d->%d MiB retained=%d->%d->%d MiB limit=%d MiB",
			reason,
			bytesToMiB(before.heapAlloc),
			bytesToMiB(afterGC.heapAlloc),
			bytesToMiB(afterFree.heapAlloc),
			bytesToMiB(before.heapRetained),
			bytesToMiB(afterGC.heapRetained),
			bytesToMiB(afterFree.heapRetained),
			limitMB,
		)
	}
}

func defaultReaderSettings(userID int64) db.ReaderSettings {
	return db.ReaderSettings{
		UserID:          userID,
		Mode:            "paged",
		Fit:             "width",
		EnhanceZip:      "level1",
		EnhancePDF:      "level1",
		SpreadDirection: "rtl",
		MobileCompact:   true,
	}
}

func normalizeReaderSettings(v db.ReaderSettings) db.ReaderSettings {
	base := defaultReaderSettings(v.UserID)
	base.CreatedAt = v.CreatedAt
	base.UpdatedAt = v.UpdatedAt
	mode := strings.ToLower(strings.TrimSpace(v.Mode))
	switch mode {
	case "paged", "scroll":
		base.Mode = mode
	}
	fit := strings.ToLower(strings.TrimSpace(v.Fit))
	switch fit {
	case "width", "height":
		base.Fit = fit
	}
	base.EnhanceZip = normalizeEnhanceMode(v.EnhanceZip)
	base.EnhancePDF = normalizeEnhanceMode(v.EnhancePDF)
	dir := strings.ToLower(strings.TrimSpace(v.SpreadDirection))
	if dir == "ltr" || dir == "rtl" {
		base.SpreadDirection = dir
	}
	base.MobileCompact = v.MobileCompact
	return base
}

func normalizeEnhanceMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off":
		return "off"
	case "level2", "quality", "ultra", "ai-2x", "ai-4x":
		return "level2"
	case "level1", "balanced", "clarity", "strong":
		return "level1"
	default:
		return "level1"
	}
}

func mountDTO(m db.Mount) map[string]any {
	rootPaths := m.RootPaths
	if len(rootPaths) == 0 {
		rootPaths = []string{m.RootPath}
	}
	return map[string]any{
		"id":              m.ID,
		"name":            m.Name,
		"type":            m.Type,
		"root_path":       m.RootPath,
		"root_paths":      rootPaths,
		"smb_host":        m.SMBHost,
		"smb_port":        m.SMBPort,
		"smb_share":       m.SMBShare,
		"smb_user":        m.SMBUser,
		"smb_domain":      m.SMBDomain,
		"webdav_url":      m.WebDAVURL,
		"webdav_user":     m.WebDAVUser,
		"enabled":         m.Enabled,
		"created_at":      m.CreatedAt,
		"has_smb_pass":    m.SMBPassword != "",
		"has_webdav_pass": m.WebDAVPassword != "",
	}
}

func mountPublicDTO(m db.Mount) map[string]any {
	return map[string]any{
		"id":         m.ID,
		"name":       m.Name,
		"type":       m.Type,
		"enabled":    m.Enabled,
		"created_at": m.CreatedAt,
	}
}

func mountRootDisplayLabels(m db.Mount) []string {
	roots := append([]string(nil), m.RootPaths...)
	if len(roots) == 0 && strings.TrimSpace(m.RootPath) != "" {
		roots = []string{m.RootPath}
	}
	labels := make([]string, 0, len(roots))
	used := make(map[string]int, len(roots))
	for idx, root := range roots {
		trimmed := strings.TrimSpace(root)
		label := path.Base(strings.ReplaceAll(trimmed, "\\", "/"))
		if label == "." || label == "/" || label == "" {
			label = trimmed
		}
		if label == "" {
			label = fmt.Sprintf("root-%d", idx+1)
		}
		used[label]++
		if used[label] > 1 {
			label = fmt.Sprintf("%s (%d)", label, used[label])
		}
		labels = append(labels, label)
	}
	return labels
}

func mountFolderLabel(m db.Mount, raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "__root") && strings.HasSuffix(value, "__") {
		numRaw := strings.TrimSuffix(strings.TrimPrefix(value, "__root"), "__")
		if idx, err := strconv.Atoi(numRaw); err == nil {
			labels := mountRootDisplayLabels(m)
			if idx >= 0 && idx < len(labels) && strings.TrimSpace(labels[idx]) != "" {
				return labels[idx]
			}
		}
	}
	return value
}

func rootDirectoryDTO(item db.RootDirectory) map[string]any {
	return map[string]any{
		"id":         item.ID,
		"name":       item.Name,
		"path":       item.Path,
		"created_at": item.CreatedAt,
	}
}

func (s *Server) comicDTO(r *http.Request, c db.Comic) map[string]any {
	format := string(comicfmt.Detect(c.Path))
	return map[string]any{
		"id":            c.ID,
		"mount_id":      c.MountID,
		"path":          c.Path,
		"format":        format,
		"title":         c.Title,
		"file_name_key": c.FileNameKey,
		"size":          c.Size,
		"page_count":    c.PageCount,
		"mtime_unix":    c.MtimeUnix,
		"updated_at":    c.UpdatedAt,
		"cover_url":     s.requestAppURL(r, fmt.Sprintf("/api/comics/%d/cover", c.ID)),
	}
}

func (s *Server) comicWithProgressDTO(r *http.Request, c db.ComicWithProgress) map[string]any {
	out := s.comicDTO(r, c.Comic)
	status := "unread"
	if c.HasProgress {
		status = "reading"
		if c.Finished {
			status = "finished"
		}
	}
	progressPercent := 0
	if c.ProgressTotalPages > 0 {
		progressPercent = (c.LastPage + 1) * 100 / c.ProgressTotalPages
		if progressPercent < 0 {
			progressPercent = 0
		}
		if progressPercent > 100 {
			progressPercent = 100
		}
	}
	out["has_progress"] = c.HasProgress
	out["last_page"] = c.LastPage
	out["progress_total_pages"] = c.ProgressTotalPages
	out["is_finished"] = c.Finished
	out["reading_status"] = status
	out["progress_percent"] = progressPercent
	out["is_favorite"] = c.IsFavorite
	out["comic_file_name_key"] = c.FileNameKey
	if c.LastReadAt.IsZero() {
		out["last_read_at"] = nil
	} else {
		out["last_read_at"] = c.LastReadAt
	}
	return out
}

func progressDTO(p db.ReadingProgress) map[string]any {
	out := map[string]any{
		"id":                  p.ID,
		"user_id":             p.UserID,
		"comic_file_name_key": p.ComicFileNameKey,
		"last_page":           p.LastPage,
		"total_pages":         p.TotalPages,
		"last_read_at":        p.LastReadAt,
		"updated_at":          p.UpdatedAt,
	}
	if p.ComicID != nil {
		out["comic_id"] = *p.ComicID
	}
	return out
}

func statusFromErr(err error) int {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "not found") {
		return http.StatusNotFound
	}
	if strings.Contains(msg, "unavailable") {
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

func withUserCtx(r *http.Request, u db.User) *http.Request {
	ctx := r.Context()
	ctx = contextWithUser(ctx, u)
	return r.WithContext(ctx)
}

func userFromCtx(r *http.Request) (db.User, bool) {
	return contextUser(r.Context())
}

func loadWebUI(preferredDir string) (loginTpl *template.Template, appTpl *template.Template, staticFS fs.FS, source string, err error) {
	webDir := strings.TrimSpace(preferredDir)
	if webDir == "" {
		webDir = "./web"
	}

	if looksLikeExternalWebDir(webDir) {
		loginTpl, err = template.ParseFiles(filepath.Join(webDir, "templates", "login.html"))
		if err == nil {
			appTpl, err = template.ParseFiles(filepath.Join(webDir, "templates", "app.html"))
			if err == nil {
				return loginTpl, appTpl, os.DirFS(filepath.Join(webDir, "static")), fmt.Sprintf("filesystem (%s)", webDir), nil
			}
		}
	}

	loginTpl, err = template.ParseFS(webassets.Assets, "templates/login.html")
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("parse embedded login template: %w", err)
	}
	appTpl, err = template.ParseFS(webassets.Assets, "templates/app.html")
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("parse embedded app template: %w", err)
	}
	staticFS, err = fs.Sub(webassets.Assets, "static")
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("load embedded static files: %w", err)
	}
	return loginTpl, appTpl, staticFS, "embedded", nil
}

func looksLikeExternalWebDir(webDir string) bool {
	required := []string{
		filepath.Join(webDir, "templates", "login.html"),
		filepath.Join(webDir, "templates", "app.html"),
		filepath.Join(webDir, "static"),
	}
	for _, p := range required {
		if _, err := os.Stat(p); err != nil {
			return false
		}
	}
	return true
}

func (s *Server) requestBasePath(r *http.Request) string {
	if r == nil {
		return ""
	}
	candidates := []string{
		r.Header.Get("X-Forwarded-Prefix"),
		r.Header.Get("X-Script-Name"),
	}
	for _, raw := range candidates {
		if prefix := sanitizeBasePath(raw); prefix != "" {
			return prefix
		}
	}
	return ""
}

func (s *Server) requestBaseHref(r *http.Request) string {
	basePath := s.requestBasePath(r)
	if basePath == "" {
		return "/"
	}
	return basePath + "/"
}

func (s *Server) requestCookiePath(r *http.Request) string {
	basePath := s.requestBasePath(r)
	if basePath == "" {
		return "/"
	}
	return basePath + "/"
}

func (s *Server) requestAppURL(r *http.Request, target string) string {
	basePath := s.requestBasePath(r)
	return joinAppPath(basePath, target)
}

func (s *Server) isSecureRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	proto := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]))
	return proto == "https"
}

func sanitizeBasePath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := neturl.Parse(raw); err == nil {
		if u.Path != "" {
			raw = u.Path
		}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "/" {
		return ""
	}
	if idx := strings.IndexAny(raw, "?#"); idx >= 0 {
		raw = raw[:idx]
	}
	if !strings.HasPrefix(raw, "/") {
		raw = "/" + raw
	}
	raw = path.Clean(raw)
	if raw == "." || raw == "/" {
		return ""
	}
	return strings.TrimRight(raw, "/")
}

func joinAppPath(basePath, target string) string {
	target = strings.TrimSpace(target)
	if target == "" || target == "/" {
		if basePath == "" {
			return "/"
		}
		return basePath + "/"
	}
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return target
	}
	fragment := ""
	query := ""
	if idx := strings.IndexByte(target, '#'); idx >= 0 {
		fragment = target[idx:]
		target = target[:idx]
	}
	if idx := strings.IndexByte(target, '?'); idx >= 0 {
		query = target[idx:]
		target = target[:idx]
	}
	if !strings.HasPrefix(target, "/") {
		target = "/" + target
	}
	if basePath == "" {
		return target + query + fragment
	}
	return strings.TrimRight(basePath, "/") + target + query + fragment
}
