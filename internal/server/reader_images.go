package server

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"zipmanga/internal/archiveutil"
	"zipmanga/internal/cache"
	"zipmanga/internal/comicfmt"
	"zipmanga/internal/db"
	"zipmanga/internal/imgenhance"
	"zipmanga/internal/mounts"
	"zipmanga/internal/pdfutil"
)

// Archive readers currently expose this precise error rather than a sentinel.
// Never classify arbitrary transport errors or a missing source file as page EOF.
func readerPageError(err error) (string, int) {
	if err != nil && err.Error() == "page index out of range" {
		return "page_out_of_range", http.StatusNotFound
	}
	return "page_unavailable", http.StatusBadGateway
}

func readerDisplayWidth(width int, quality string) int {
	limit := 2560
	if quality == "economy" {
		limit = 1280
	}
	if width <= 0 || width > limit {
		width = limit
	}
	for _, bucket := range []int{640, 960, 1280, 1600, 1920, 2560} {
		if width <= bucket {
			return bucket
		}
	}
	return limit
}

func readerPrefetchIndexes(page, count, limit int, direction string) []int {
	if limit > 3 {
		limit = 3
	}
	step := 1
	if direction == "backward" {
		step = -1
	}
	var indexes []int
	for i := 1; i <= limit; i++ {
		index := page + step*i
		if index < 0 || (count > 0 && index >= count) {
			break
		}
		indexes = append(indexes, index)
	}
	return indexes
}

func (s *Server) ensureOriginalComicPageCache(comic db.Comic, provider mounts.Provider, format comicfmt.Format, index int) (string, error) {
	if s.pageCache == nil {
		return "", errors.New("page cache is unavailable")
	}
	key := fmt.Sprintf("page:original:v1:%s:%d:%d:%d:%d", format, comic.ID, index, comic.MtimeUnix, comic.Size)
	return s.pageCache.EnsureObject(key, func() (cache.CacheObject, error) {
		if format == comicfmt.FormatPDF {
			return s.withPageEnhanceObjectSlot(func() (cache.CacheObject, error) {
				raw, _, err := pdfutil.RenderPage(provider, comic.Path, index, 3600, pdfutil.RenderFormatJPEG)
				if err == nil && len(raw) == 0 {
					err = errors.New("empty rendered page")
				}
				return cache.CacheObject{Data: raw, Ext: ".jpg"}, err
			})
		}
		name, raw, err := archiveutil.PageImage(provider, comic.Path, format, index, 64<<20)
		if err != nil {
			return cache.CacheObject{}, err
		}
		if len(raw) == 0 {
			return cache.CacheObject{}, errors.New("empty page image")
		}
		return cache.CacheObject{Data: raw, Ext: filepath.Ext(name)}, nil
	})
}

func (s *Server) serveReaderPage(w http.ResponseWriter, r *http.Request) {
	s.interactiveReads.Add(1)
	defer s.interactiveReads.Add(-1)
	started := time.Now()
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": "invalid comic id"})
		return
	}
	index, err := parsePathInt(r, "index")
	if err != nil || index < 0 {
		writeJSON(w, 400, map[string]any{"error": "invalid page index"})
		return
	}
	quality := strings.TrimSpace(r.URL.Query().Get("quality"))
	if quality == "" {
		quality = "standard"
	}
	if quality != "standard" && quality != "economy" && quality != "original" {
		writeJSON(w, 400, map[string]any{"error": "invalid image quality"})
		return
	}
	comic, provider, err := s.fetchComicAndProvider(id)
	if err != nil {
		s.writeReaderPageError(w, err)
		return
	}
	format := comicfmt.Detect(comic.Path)
	if !comicfmt.IsArchive(format) {
		writeJSON(w, 400, map[string]any{"error": "unsupported comic format"})
		return
	}
	if format == comicfmt.FormatPDF && comic.PageCount > 0 && index >= comic.PageCount {
		s.writeReaderPageError(w, errors.New("page index out of range"))
		return
	}
	if comic.PageCount <= 1 {
		warm := comic
		warm.PageCount = 0
		s.warmPageCountAsync(warm, provider, format)
	}
	if provider.Type() == db.MountTypeWebDAV {
		s.warmCoverForReadingAsync(comic, provider)
	}
	enhance := normalizeEnhanceMode(r.URL.Query().Get("enhance"))
	width := readerDisplayWidth(parseBoundedInt(r.URL.Query().Get("w"), 0, 0, 7000), quality)
	key := fmt.Sprintf("page:display:v2:%s:%d:%d:%d:%d:%s:%s:%d", format, comic.ID, index, comic.MtimeUnix, comic.Size, quality, enhance, width)
	if quality != "original" && s.pageCache != nil {
		if displayPath, ok, err := s.pageCache.GetPathIfExists(key); err == nil && ok {
			if r.Context().Err() != nil {
				return
			}
			s.prefetchReaderPages(comic, provider, format, index, r.URL.Query().Get("direction"))
			w.Header().Set("Server-Timing", fmt.Sprintf("read;dur=%.2f, render;dur=0, cache;desc=\"true\"", float64(time.Since(started).Microseconds())/1000))
			s.serveCachedPageFile(w, r, displayPath, archiveutil.GuessImageContentType(displayPath))
			return
		}
	}
	var rawPath string
	if quality == "original" {
		rawPath, err = s.ensureOriginalComicPageCache(comic, provider, format, index)
	} else {
		rawPath, err = s.ensureRawComicPageCache(comic, provider, format, index)
	}
	readDuration := time.Since(started)
	if err != nil {
		s.writeReaderPageError(w, err)
		return
	}
	if r.Context().Err() != nil {
		return
	}
	direction := r.URL.Query().Get("direction")
	if quality != "original" {
		s.prefetchReaderPages(comic, provider, format, index, direction)
	}
	if quality == "original" || (quality == "standard" && strings.EqualFold(filepath.Ext(rawPath), ".webp")) {
		w.Header().Set("Server-Timing", fmt.Sprintf("read;dur=%.2f", float64(readDuration.Microseconds())/1000))
		s.serveCachedPageFile(w, r, rawPath, archiveutil.GuessImageContentType(rawPath))
		return
	}
	renderStarted := time.Now()
	cached := false
	if _, ok, _ := s.pageCache.GetPathIfExists(key); ok {
		cached = true
	}
	displayPath, err := s.pageCache.EnsureObject(key, func() (cache.CacheObject, error) {
		raw, err := os.ReadFile(rawPath)
		if err != nil {
			return cache.CacheObject{}, err
		}
		return s.withPageEnhanceObjectSlot(func() (cache.CacheObject, error) {
			source := imgenhance.SourceImage
			if format == comicfmt.FormatPDF {
				source = imgenhance.SourcePDF
			}
			opt := imgenhance.Options{Mode: imgenhance.Mode(enhance), TargetWidth: width, Source: source}
			var data []byte
			var ct string
			if enhance == "off" {
				data, ct, err = imgenhance.RenderForDisplay(raw, rawPath, opt)
			} else {
				data, ct, err = imgenhance.Enhance(raw, rawPath, opt)
			}
			return cache.CacheObject{Data: data, Ext: cacheExtForContentType(ct)}, err
		})
	})
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 502, map[string]any{"code": "page_decode_failed", "error": "cannot render page"})
		return
	}
	w.Header().Set("Server-Timing", fmt.Sprintf("read;dur=%.2f, render;dur=%.2f, cache;desc=\"%t\"", float64(readDuration.Microseconds())/1000, float64(time.Since(renderStarted).Microseconds())/1000, cached))
	s.serveCachedPageFile(w, r, displayPath, archiveutil.GuessImageContentType(displayPath))
}

func (s *Server) writeReaderPageError(w http.ResponseWriter, err error) {
	code, status := readerPageError(err)
	w.Header().Set("Cache-Control", "no-store")
	if s.logger != nil {
		s.logger.Printf("reader page: %v", err)
	}
	writeJSON(w, status, map[string]any{"code": code, "error": code})
}

func (s *Server) prefetchReaderPages(comic db.Comic, provider mounts.Provider, format comicfmt.Format, index int, direction string) {
	if s.pageCache == nil || provider == nil {
		return
	}
	for _, page := range readerPrefetchIndexes(index, comic.PageCount, int(s.currentRuntimeConfig().PagePrefetchPages), direction) {
		if _, ok, err := s.pageCache.GetPathIfExists(rawComicPageCacheKey(comic, format, page)); err == nil && !ok {
			s.enqueueComicPageLoad(comic, provider, format, page, false)
		}
	}
}
