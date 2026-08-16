package pdfutil

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"zipmanga/internal/mounts"
)

const pdfRenderTimeout = 45 * time.Second

var (
	pdfRenderSemaphore = make(chan struct{}, maxInt(1, minInt(4, maxInt(1, runtime.NumCPU()/2))))
	pdfToolOnce        sync.Once
	pdfToolPath        string
	pdfToolName        string
	pdfToolErr         error
)

type renderAttempt struct {
	width   int
	timeout time.Duration
}

type RenderFormat string

const (
	RenderFormatJPEG RenderFormat = "jpeg"
	RenderFormatPNG  RenderFormat = "png"
)

type RenderedPage struct {
	Data        []byte
	ContentType string
}

func RenderPageJPEG(provider mounts.Provider, relPath string, pageIndex int, targetWidth int) ([]byte, error) {
	data, _, err := RenderPage(provider, relPath, pageIndex, targetWidth, RenderFormatJPEG)
	return data, err
}

func RenderPage(provider mounts.Provider, relPath string, pageIndex int, targetWidth int, format RenderFormat) ([]byte, string, error) {
	if provider == nil {
		return nil, "", fmt.Errorf("provider is nil")
	}
	if pageIndex < 0 {
		return nil, "", fmt.Errorf("page index must be non-negative")
	}
	if format != RenderFormatPNG {
		format = RenderFormatJPEG
	}

	toolPath, toolName, err := lookupPDFRenderTool()
	if err != nil {
		return nil, "", err
	}

	if err := acquireRenderSlot(); err != nil {
		return nil, "", err
	}
	defer releaseRenderSlot()

	inputFile, cleanup, err := materializePDF(provider, relPath)
	if err != nil {
		return nil, "", err
	}
	defer cleanup()

	workDir, err := os.MkdirTemp("", "zipmanga-pdf-render-*")
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	outPrefix := filepath.Join(workDir, "page")
	pageNum := pageIndex + 1

	attempts := buildRenderAttempts(targetWidth)
	var firstErr error
	for _, attempt := range attempts {
		data, contentType, renderErr := renderPageWithTool(toolPath, toolName, inputFile, outPrefix, pageNum, attempt, format)
		if renderErr == nil {
			return data, contentType, nil
		}
		if firstErr == nil {
			firstErr = renderErr
		} else {
			firstErr = fmt.Errorf("%v; retry failed: %w", firstErr, renderErr)
		}
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("unknown render error")
	}
	return nil, "", firstErr
}

func RenderPages(provider mounts.Provider, relPath string, startPageIndex, endPageIndex int, targetWidth int, format RenderFormat) (map[int]RenderedPage, error) {
	if provider == nil {
		return nil, fmt.Errorf("provider is nil")
	}
	if startPageIndex < 0 || endPageIndex < startPageIndex {
		return nil, fmt.Errorf("invalid page range")
	}
	if format != RenderFormatPNG {
		format = RenderFormatJPEG
	}

	toolPath, toolName, err := lookupPDFRenderTool()
	if err != nil {
		return nil, err
	}

	if err := acquireRenderSlot(); err != nil {
		return nil, err
	}
	defer releaseRenderSlot()

	inputFile, cleanup, err := materializePDF(provider, relPath)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	workDir, err := os.MkdirTemp("", "zipmanga-pdf-render-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	outPrefix := filepath.Join(workDir, "page")
	startPageNum := startPageIndex + 1
	endPageNum := endPageIndex + 1

	attempts := buildRenderAttempts(targetWidth)
	var firstErr error
	for _, attempt := range attempts {
		pages, renderErr := renderPagesWithTool(toolPath, toolName, inputFile, outPrefix, startPageNum, endPageNum, attempt, format)
		if renderErr == nil {
			out := make(map[int]RenderedPage, len(pages))
			for pageNum, page := range pages {
				out[pageNum-1] = page
			}
			return out, nil
		}
		if firstErr == nil {
			firstErr = renderErr
		} else {
			firstErr = fmt.Errorf("%v; retry failed: %w", firstErr, renderErr)
		}
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("unknown render error")
	}
	return nil, firstErr
}

func renderPageWithTool(toolPath, toolName, inputFile, outPrefix string, pageNum int, attempt renderAttempt, format RenderFormat) ([]byte, string, error) {
	args := []string{
		"-f", strconv.Itoa(pageNum),
		"-l", strconv.Itoa(pageNum),
		"-singlefile",
	}
	outExt := ".jpg"
	contentType := "image/jpeg"
	switch format {
	case RenderFormatPNG:
		args = append(args, "-png")
		outExt = ".png"
		contentType = "image/png"
	default:
		args = append(args, "-jpeg")
		if toolName == "pdftoppm" {
			args = append(args, "-jpegopt", "quality=93,optimize=y")
		}
	}
	if attempt.width > 0 {
		args = append(args, "-scale-to-x", strconv.Itoa(attempt.width), "-scale-to-y", "-1")
	}
	args = append(args, inputFile, outPrefix)

	ctx, cancel := context.WithTimeout(context.Background(), attempt.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, toolPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := stderr.String()
		if msg == "" {
			msg = err.Error()
		}
		return nil, "", fmt.Errorf("%s failed (w=%d timeout=%s): %s", toolName, attempt.width, attempt.timeout, strings.TrimSpace(msg))
	}

	outPath := outPrefix + outExt
	data, err := os.ReadFile(outPath)
	if err != nil {
		return nil, "", err
	}
	if len(data) == 0 {
		return nil, "", fmt.Errorf("rendered page is empty")
	}
	return data, contentType, nil
}

func renderPagesWithTool(toolPath, toolName, inputFile, outPrefix string, startPageNum, endPageNum int, attempt renderAttempt, format RenderFormat) (map[int]RenderedPage, error) {
	args := []string{
		"-f", strconv.Itoa(startPageNum),
		"-l", strconv.Itoa(endPageNum),
	}
	outExt := ".jpg"
	contentType := "image/jpeg"
	switch format {
	case RenderFormatPNG:
		args = append(args, "-png")
		outExt = ".png"
		contentType = "image/png"
	default:
		args = append(args, "-jpeg")
		if toolName == "pdftoppm" {
			args = append(args, "-jpegopt", "quality=93,optimize=y")
		}
	}
	if attempt.width > 0 {
		args = append(args, "-scale-to-x", strconv.Itoa(attempt.width), "-scale-to-y", "-1")
	}
	args = append(args, inputFile, outPrefix)

	pageCount := endPageNum - startPageNum + 1
	timeout := attempt.timeout
	if pageCount > 1 {
		timeout += time.Duration(pageCount-1) * 12 * time.Second
		if timeout > 4*time.Minute {
			timeout = 4 * time.Minute
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, toolPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := stderr.String()
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s batch render failed (%d-%d w=%d timeout=%s): %s", toolName, startPageNum, endPageNum, attempt.width, timeout, strings.TrimSpace(msg))
	}

	out := make(map[int]RenderedPage, pageCount)
	for pageNum := startPageNum; pageNum <= endPageNum; pageNum++ {
		outPath := fmt.Sprintf("%s-%d%s", outPrefix, pageNum, outExt)
		data, err := os.ReadFile(outPath)
		if err != nil {
			return nil, err
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("rendered page is empty")
		}
		out[pageNum] = RenderedPage{Data: data, ContentType: contentType}
	}
	return out, nil
}

func lookupPDFRenderTool() (string, string, error) {
	pdfToolOnce.Do(func() {
		if p, err := exec.LookPath("pdftoppm"); err == nil {
			pdfToolPath = p
			pdfToolName = "pdftoppm"
			return
		}
		if p, err := exec.LookPath("pdftocairo"); err == nil {
			pdfToolPath = p
			pdfToolName = "pdftocairo"
			return
		}
		pdfToolErr = fmt.Errorf("pdf renderer not found: install poppler (pdftoppm or pdftocairo)")
	})
	if pdfToolErr != nil {
		return "", "", pdfToolErr
	}
	return pdfToolPath, pdfToolName, nil
}

func buildRenderAttempts(targetWidth int) []renderAttempt {
	widths := []int{
		targetWidth,
		scaleWidth(targetWidth, 88),
		scaleWidth(targetWidth, 72),
		scaleWidth(targetWidth, 58),
		2200,
		1600,
		0,
	}
	seen := make(map[int]struct{}, len(widths))
	out := make([]renderAttempt, 0, len(widths))
	for _, w := range widths {
		if w < 0 {
			w = 0
		}
		if _, ok := seen[w]; ok {
			continue
		}
		seen[w] = struct{}{}

		timeout := pdfRenderTimeout
		if w >= 3600 {
			timeout = 80 * time.Second
		} else if w >= 2500 {
			timeout = 65 * time.Second
		} else if w > 0 && w <= 1400 {
			timeout = 35 * time.Second
		}
		out = append(out, renderAttempt{width: w, timeout: timeout})
	}
	return out
}

func scaleWidth(v int, pct int) int {
	if v <= 0 {
		return 0
	}
	if pct <= 0 {
		return 0
	}
	n := (v * pct) / 100
	if n < 800 {
		return 800
	}
	return n
}

func acquireRenderSlot() error {
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	select {
	case pdfRenderSemaphore <- struct{}{}:
		return nil
	case <-timer.C:
		return fmt.Errorf("pdf render queue timeout")
	}
}

func releaseRenderSlot() {
	select {
	case <-pdfRenderSemaphore:
	default:
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func materializePDF(provider mounts.Provider, relPath string) (string, func(), error) {
	rc, err := provider.Open(relPath)
	if err != nil {
		return "", nil, err
	}

	tmp, err := os.CreateTemp("", "zipmanga-page-*.pdf")
	if err != nil {
		_ = rc.Close()
		return "", nil, err
	}

	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}

	if _, err := io.Copy(tmp, rc); err != nil {
		_ = rc.Close()
		cleanup()
		return "", nil, err
	}
	_ = rc.Close()
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return tmp.Name(), cleanup, nil
}
