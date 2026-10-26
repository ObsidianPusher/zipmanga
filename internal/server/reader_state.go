package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"zipmanga/internal/db"
)

func (s *Server) readerStateComic(w http.ResponseWriter, r *http.Request) (db.User, db.Comic, bool) {
	u, ok := userFromCtx(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return u, db.Comic{}, false
	}
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid comic id"})
		return u, db.Comic{}, false
	}
	c, err := s.store.GetComic(id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]any{"error": "comic not found"})
		return u, c, false
	}
	return u, c, true
}

func (s *Server) handleAPIGetReaderState(w http.ResponseWriter, r *http.Request) {
	u, c, ok := s.readerStateComic(w, r)
	if !ok {
		return
	}
	state, err := s.store.GetComicReaderState(u.ID, c.ID)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"state": nil})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": state})
}

func (s *Server) handleAPIUpdateReaderState(w http.ResponseWriter, r *http.Request) {
	u, c, ok := s.readerStateComic(w, r)
	if !ok {
		return
	}
	var state db.ComicReaderState
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid reader state"})
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid reader state"})
		return
	}
	if !validComicReaderState(state) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid reader state values"})
		return
	}
	if state.Settings.Fit == "height" {
		state.Settings.Fit = "contain"
	}
	saved, err := s.store.UpsertComicReaderState(u.ID, c.ID, state)
	if errors.Is(err, db.ErrReaderStateConflict) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "state": saved})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": saved})
}

func validComicReaderState(state db.ComicReaderState) bool {
	if state.LastPage < 0 || state.LastPage > 200000 || state.TotalPages < 0 || state.TotalPages > 200000 || state.TotalPages > 0 && state.LastPage >= state.TotalPages {
		return false
	}
	if math.IsNaN(state.PageOffset) || math.IsInf(state.PageOffset, 0) || state.PageOffset < 0 || state.PageOffset > 1 {
		return false
	}
	if state.UpdatedAt.IsZero() || state.UpdatedAt.Before(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)) || state.UpdatedAt.After(time.Now().Add(5*time.Minute)) {
		return false
	}
	p := state.Settings
	allowed := func(v string, choices ...string) bool {
		for _, c := range choices {
			if v == c {
				return true
			}
		}
		return false
	}
	return allowed(p.Mode, "paged", "scroll") && allowed(p.Fit, "width", "height", "contain", "original") && allowed(p.Quality, "economy", "standard", "original") && allowed(p.Enhance, "off", "level1", "level2") && allowed(p.SpreadDirection, "ltr", "rtl") && allowed(p.Spread, "auto", "single", "double")
}

// Handles the explicit pagination contract while preserving legacy limit/offset responses.
func (s *Server) handleAPIHistoryPaged(w http.ResponseWriter, r *http.Request) bool {
	q := r.URL.Query()
	if !q.Has("page") && !q.Has("page_size") {
		return false
	}
	u, ok := userFromCtx(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return true
	}
	page := parseBoundedInt(q.Get("page"), 1, 1, 500001)
	size := parseBoundedInt(q.Get("page_size"), 24, 1, 100)
	total, err := s.store.CountReadingHistory(u.ID)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return true
	}
	pages := (total + size - 1) / size
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	items, err := s.store.ListReadingHistory(u.ID, size, (page-1)*size)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return true
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		c := s.comicDTO(r, it.Comic)
		c["is_favorite"] = it.IsFavorite
		out = append(out, map[string]any{"progress": progressDTO(it.Progress), "comic": c})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "page": page, "page_size": size, "total": total, "total_pages": pages})
	return true
}

func (s *Server) handleAPIComicNeighbors(w http.ResponseWriter, r *http.Request) {
	_, c, ok := s.readerStateComic(w, r)
	if !ok {
		return
	}
	books, err := s.store.ListComicsByMount(c.MountID)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	siblings := make([]db.Comic, 0)
	for _, book := range books {
		if path.Dir(book.Path) == path.Dir(c.Path) {
			siblings = append(siblings, book)
		}
	}
	sort.Slice(siblings, func(i, j int) bool { return comicNaturalLess(path.Base(siblings[i].Path), path.Base(siblings[j].Path)) })
	var previous, next any
	for i, book := range siblings {
		if book.ID == c.ID {
			if i > 0 {
				previous = s.comicDTO(r, siblings[i-1])
			}
			if i+1 < len(siblings) {
				next = s.comicDTO(r, siblings[i+1])
			}
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"previous": previous, "next": next})
}

// Compare numeric runs without integer conversion, so arbitrarily long volume numbers sort safely.
func comicNaturalLess(a, b string) bool {
	originalA, originalB := a, b
	a = strings.ToLower(a)
	b = strings.ToLower(b)
	for len(a) > 0 && len(b) > 0 {
		if a[0] >= '0' && a[0] <= '9' && b[0] >= '0' && b[0] <= '9' {
			i, j := 0, 0
			for i < len(a) && a[i] >= '0' && a[i] <= '9' {
				i++
			}
			for j < len(b) && b[j] >= '0' && b[j] <= '9' {
				j++
			}
			x, y := strings.TrimLeft(a[:i], "0"), strings.TrimLeft(b[:j], "0")
			if len(x) != len(y) {
				return len(x) < len(y)
			}
			if x != y {
				return x < y
			}
			a = a[i:]
			b = b[j:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a = a[1:]
		b = b[1:]
	}
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return originalA < originalB
}
