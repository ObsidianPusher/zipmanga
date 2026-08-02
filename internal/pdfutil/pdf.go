package pdfutil

import (
	"bytes"
	"errors"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	imagedraw "image/draw"
	"image/jpeg"
	"io"
	"os"
	"strings"

	"zipmanga/internal/mounts"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
	"rsc.io/pdf"
)

func PageCount(provider mounts.Provider, relPath string) (int, error) {
	if provider == nil {
		return 0, errors.New("provider is nil")
	}
	ra, size, err := openReaderAt(provider, relPath)
	if err != nil {
		return 0, err
	}
	defer ra.Close()
	if size <= 0 {
		return 0, errors.New("pdf file is empty")
	}

	r, err := pdf.NewReader(ra, size)
	if err != nil {
		return 0, err
	}
	n := r.NumPage()
	if n < 0 {
		return 0, fmt.Errorf("invalid pdf page count: %d", n)
	}
	return n, nil
}

func BuildPlaceholderCoverJPEG(title string, thumbWidth int) ([]byte, error) {
	w := clamp(thumbWidth, 180, 1024)
	h := int(float64(w) * 1.42)
	if h < 240 {
		h = 240
	}
	cleanTitle := strings.TrimSpace(title)
	if cleanTitle == "" {
		cleanTitle = "PDF Document"
	}
	theme := themeByTitle(cleanTitle)

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	imagedraw.Draw(img, img.Bounds(), image.NewUniform(theme.bg), image.Point{}, imagedraw.Src)

	pad := w / 10
	if pad < 18 {
		pad = 18
	}
	paperRect := image.Rect(pad, pad, w-pad, h-pad)
	imagedraw.Draw(img, paperRect, image.NewUniform(theme.paper), image.Point{}, imagedraw.Src)

	border := theme.border
	drawRectBorder(img, paperRect, border)

	fold := w / 7
	if fold < 22 {
		fold = 22
	}
	foldRect := image.Rect(paperRect.Max.X-fold, paperRect.Min.Y, paperRect.Max.X, paperRect.Min.Y+fold)
	imagedraw.Draw(img, foldRect, image.NewUniform(theme.fold), image.Point{}, imagedraw.Src)
	drawRectBorder(img, foldRect, theme.border)

	bannerH := h / 4
	if bannerH < 64 {
		bannerH = 64
	}
	bannerRect := image.Rect(paperRect.Min.X+pad/2, paperRect.Min.Y+h/6, paperRect.Max.X-pad/2, paperRect.Min.Y+h/6+bannerH)
	imagedraw.Draw(img, bannerRect, image.NewUniform(theme.banner), image.Point{}, imagedraw.Src)

	drawText(img, bannerRect.Min.X+16, bannerRect.Min.Y+bannerH/2+6, "PDF", color.White)
	cleanTitle = trimRunes(cleanTitle, 28)
	drawText(img, paperRect.Min.X+16, bannerRect.Max.Y+32, cleanTitle, theme.text)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func openReaderAt(provider mounts.Provider, relPath string) (mounts.ReadAtCloser, int64, error) {
	ra, size, err := provider.OpenReaderAt(relPath)
	if err == nil && ra != nil && size > 0 {
		return ra, size, nil
	}
	if err == nil && ra != nil {
		_ = ra.Close()
	}
	if err != nil && !errors.Is(err, mounts.ErrReaderAtUnsupported) {
		return nil, 0, err
	}

	stream, err := provider.Open(relPath)
	if err != nil {
		return nil, 0, err
	}
	defer stream.Close()

	tmp, err := os.CreateTemp("", "zipmanga-pdf-*.pdf")
	if err != nil {
		return nil, 0, err
	}
	if _, err := io.Copy(tmp, stream); err != nil {
		name := tmp.Name()
		_ = tmp.Close()
		_ = os.Remove(name)
		return nil, 0, err
	}
	st, err := tmp.Stat()
	if err != nil {
		name := tmp.Name()
		_ = tmp.Close()
		_ = os.Remove(name)
		return nil, 0, err
	}
	return &tempFileReadAtCloser{File: tmp, path: tmp.Name()}, st.Size(), nil
}

type tempFileReadAtCloser struct {
	*os.File
	path string
}

func (t *tempFileReadAtCloser) Close() error {
	if t == nil || t.File == nil {
		return nil
	}
	err := t.File.Close()
	_ = os.Remove(t.path)
	return err
}

func drawRectBorder(img *image.RGBA, rect image.Rectangle, c color.Color) {
	if rect.Empty() {
		return
	}
	for x := rect.Min.X; x < rect.Max.X; x++ {
		img.Set(x, rect.Min.Y, c)
		img.Set(x, rect.Max.Y-1, c)
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		img.Set(rect.Min.X, y, c)
		img.Set(rect.Max.X-1, y, c)
	}
}

func drawText(img *image.RGBA, x, y int, text string, c color.Color) {
	d := font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(c),
		Face: basicfont.Face7x13,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(text)
}

func clamp(v, minV, maxV int) int {
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
}

func trimRunes(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	if max <= 1 {
		return string(rs[:1])
	}
	return string(rs[:max-1]) + "..."
}

type coverTheme struct {
	bg     color.RGBA
	paper  color.RGBA
	fold   color.RGBA
	border color.RGBA
	banner color.RGBA
	text   color.RGBA
}

func themeByTitle(title string) coverTheme {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(strings.TrimSpace(title))))
	n := h.Sum32()

	baseR := uint8(90 + (n % 120))
	baseG := uint8(70 + ((n >> 8) % 110))
	baseB := uint8(70 + ((n >> 16) % 110))

	return coverTheme{
		bg:     color.RGBA{R: 232, G: 237, B: 246, A: 255},
		paper:  color.RGBA{R: 249, G: 251, B: 255, A: 255},
		fold:   color.RGBA{R: 226, G: 231, B: 241, A: 255},
		border: color.RGBA{R: 183, G: 194, B: 211, A: 255},
		banner: color.RGBA{R: baseR, G: baseG, B: baseB, A: 255},
		text:   color.RGBA{R: 61, G: 72, B: 91, A: 255},
	}
}
