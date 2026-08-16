package archiveutil

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"os"
	"sort"
	"strings"

	"zipmanga/internal/comicfmt"
	"zipmanga/internal/mounts"
	"zipmanga/internal/ziputil"

	"github.com/bodgit/sevenzip"
	"github.com/nwaples/rardecode"
	"golang.org/x/image/draw"
)

type ProbeOptions struct {
	NeedPageCount bool
	NeedCover     bool
	ThumbWidth    int
}

type ProbeResult struct {
	PageCount int
	CoverJPEG []byte
}

type PageImageData struct {
	Name string
	Data []byte
}

var errLikelyBlankCover = errors.New("image is likely blank")

func Probe(provider mounts.Provider, relPath string, format comicfmt.Format, opt ProbeOptions) (ProbeResult, error) {
	switch format {
	case comicfmt.FormatZIP:
		return probeZIP(provider, relPath, opt)
	case comicfmt.Format7Z:
		return probe7Z(provider, relPath, opt)
	case comicfmt.FormatRAR:
		return probeRAR(provider, relPath, opt)
	default:
		return ProbeResult{}, fmt.Errorf("unsupported archive format: %s", format)
	}
}

func PageImage(provider mounts.Provider, relPath string, format comicfmt.Format, index int, maxBytes int64) (string, []byte, error) {
	switch format {
	case comicfmt.FormatZIP:
		archive, err := ziputil.OpenArchive(provider, relPath)
		if err != nil {
			return "", nil, err
		}
		defer archive.Close()

		page, err := ziputil.PageFile(archive.Reader, index)
		if err != nil {
			return "", nil, err
		}
		rc, err := page.Open()
		if err != nil {
			return "", nil, err
		}
		defer rc.Close()
		buf, err := readLimited(rc, maxBytes)
		if err != nil {
			return "", nil, err
		}
		return page.Name, buf, nil
	case comicfmt.Format7Z:
		return pageImageFrom7Z(provider, relPath, index, maxBytes)
	case comicfmt.FormatRAR:
		return pageImageFromRAR(provider, relPath, index, maxBytes)
	default:
		return "", nil, fmt.Errorf("unsupported archive format: %s", format)
	}
}

func FirstImage(provider mounts.Provider, relPath string, format comicfmt.Format, maxBytes int64) (string, []byte, error) {
	return PageImage(provider, relPath, format, 0, maxBytes)
}

func PageImages(provider mounts.Provider, relPath string, format comicfmt.Format, indexes []int, maxBytes int64) (map[int]PageImageData, error) {
	sorted := normalizePageIndexes(indexes)
	if len(sorted) == 0 {
		return map[int]PageImageData{}, nil
	}
	switch format {
	case comicfmt.FormatZIP:
		return pageImagesFromZIP(provider, relPath, sorted, maxBytes)
	case comicfmt.Format7Z:
		return pageImagesFrom7Z(provider, relPath, sorted, maxBytes)
	case comicfmt.FormatRAR:
		return pageImagesFromRAR(provider, relPath, sorted, maxBytes)
	default:
		return nil, fmt.Errorf("unsupported archive format: %s", format)
	}
}

func probeZIP(provider mounts.Provider, relPath string, opt ProbeOptions) (ProbeResult, error) {
	archive, err := ziputil.OpenArchive(provider, relPath)
	if err != nil {
		return ProbeResult{}, err
	}
	defer archive.Close()

	var out ProbeResult
	if opt.NeedPageCount {
		out.PageCount = ziputil.PageCount(archive.Reader)
	}
	if opt.NeedCover {
		cover, err := ziputil.BuildCoverJPEG(archive.Reader, normalizeThumbWidth(opt.ThumbWidth))
		if err != nil {
			if opt.NeedPageCount {
				return out, nil
			}
			return ProbeResult{}, err
		}
		out.CoverJPEG = cover
	}
	return out, nil
}

func probe7Z(provider mounts.Provider, relPath string, opt ProbeOptions) (ProbeResult, error) {
	thumbWidth := normalizeThumbWidth(opt.ThumbWidth)
	var out ProbeResult
	err := with7ZReader(provider, relPath, func(r *sevenzip.ReadCloser) error {
		images := imageFilesFrom7Z(r)
		if opt.NeedPageCount {
			out.PageCount = len(images)
		}
		if opt.NeedCover {
			cover, err := buildCoverFrom7ZFiles(images, thumbWidth)
			if err != nil {
				if opt.NeedPageCount {
					return nil
				}
				return err
			}
			out.CoverJPEG = cover
		}
		return nil
	})
	if err != nil {
		return ProbeResult{}, err
	}
	return out, nil
}

func probeRAR(provider mounts.Provider, relPath string, opt ProbeOptions) (ProbeResult, error) {
	thumbWidth := normalizeThumbWidth(opt.ThumbWidth)
	var out ProbeResult
	readerErr := withRARReader(provider, relPath, func(rr *rardecode.Reader) error {
		if !opt.NeedPageCount && !opt.NeedCover {
			return nil
		}
		coverCandidates := 18
		coverReady := !opt.NeedCover
		var (
			decodeErr      error
			blankFallback  []byte
			hasImageRecord bool
		)
		for {
			h, err := rr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			if h == nil || h.IsDir || !ziputil.IsImageName(h.Name) {
				continue
			}
			hasImageRecord = true
			if opt.NeedPageCount {
				out.PageCount++
			}
			if coverReady {
				continue
			}
			if coverCandidates <= 0 {
				continue
			}
			coverCandidates--
			imgData, err := readLimited(rr, 64<<20)
			if err != nil {
				decodeErr = err
				continue
			}
			cover, err := buildJPEGCoverFromRaw(imgData, thumbWidth)
			if err != nil {
				if errors.Is(err, errLikelyBlankCover) && len(blankFallback) == 0 {
					fb, fbErr := buildJPEGCoverFromRawAllowBlank(imgData, thumbWidth)
					if fbErr == nil {
						blankFallback = fb
					} else {
						decodeErr = fbErr
					}
				}
				decodeErr = err
				continue
			}
			out.CoverJPEG = cover
			coverReady = true
		}
		if opt.NeedCover && len(out.CoverJPEG) == 0 && len(blankFallback) > 0 {
			out.CoverJPEG = blankFallback
		}
		if opt.NeedCover && len(out.CoverJPEG) == 0 {
			if opt.NeedPageCount && (out.PageCount > 0 || hasImageRecord) {
				return nil
			}
			if decodeErr != nil {
				return decodeErr
			}
			return errors.New("archive has no decodable image files")
		}
		return nil
	})
	if readerErr != nil {
		return ProbeResult{}, readerErr
	}
	return out, nil
}

func pageImageFrom7Z(provider mounts.Provider, relPath string, index int, maxBytes int64) (string, []byte, error) {
	var (
		name string
		data []byte
	)
	err := with7ZReader(provider, relPath, func(r *sevenzip.ReadCloser) error {
		images := imageFilesFrom7Z(r)
		if index < 0 || index >= len(images) {
			return fmt.Errorf("page index out of range")
		}
		f := images[index]
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		buf, err := readLimited(rc, maxBytes)
		if err != nil {
			return err
		}
		name = f.Name
		data = buf
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	return name, data, nil
}

func pageImagesFromZIP(provider mounts.Provider, relPath string, indexes []int, maxBytes int64) (map[int]PageImageData, error) {
	archive, err := ziputil.OpenArchive(provider, relPath)
	if err != nil {
		return nil, err
	}
	defer archive.Close()

	images := ziputil.ImageFiles(archive.Reader)
	if len(images) == 0 {
		return nil, errors.New("zip has no image files")
	}
	if indexes[0] < 0 || indexes[0] >= len(images) {
		return nil, fmt.Errorf("page index out of range")
	}

	out := make(map[int]PageImageData, len(indexes))
	for _, idx := range indexes {
		if idx < 0 || idx >= len(images) {
			continue
		}
		page := images[idx]
		rc, err := page.Open()
		if err != nil {
			return nil, err
		}
		buf, err := readLimited(rc, maxBytes)
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		out[idx] = PageImageData{Name: page.Name, Data: buf}
	}
	return out, nil
}

func pageImagesFrom7Z(provider mounts.Provider, relPath string, indexes []int, maxBytes int64) (map[int]PageImageData, error) {
	var out map[int]PageImageData
	err := with7ZReader(provider, relPath, func(r *sevenzip.ReadCloser) error {
		images := imageFilesFrom7Z(r)
		if len(images) == 0 {
			return errors.New("archive has no image files")
		}
		if indexes[0] < 0 || indexes[0] >= len(images) {
			return fmt.Errorf("page index out of range")
		}
		out = make(map[int]PageImageData, len(indexes))
		for _, idx := range indexes {
			if idx < 0 || idx >= len(images) {
				continue
			}
			f := images[idx]
			rc, err := f.Open()
			if err != nil {
				return err
			}
			buf, err := readLimited(rc, maxBytes)
			_ = rc.Close()
			if err != nil {
				return err
			}
			out[idx] = PageImageData{Name: f.Name, Data: buf}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func pageImageFromRAR(provider mounts.Provider, relPath string, index int, maxBytes int64) (string, []byte, error) {
	if index < 0 {
		return "", nil, fmt.Errorf("page index out of range")
	}
	var (
		imgName string
		imgData []byte
		imgIdx  int
	)
	err := withRARReader(provider, relPath, func(rr *rardecode.Reader) error {
		for {
			h, err := rr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			if h == nil || h.IsDir || !ziputil.IsImageName(h.Name) {
				continue
			}
			if imgIdx != index {
				imgIdx++
				continue
			}
			buf, err := readLimited(rr, maxBytes)
			if err != nil {
				return err
			}
			imgName = h.Name
			imgData = buf
			return nil
		}
		return fmt.Errorf("page index out of range")
	})
	if err != nil {
		return "", nil, err
	}
	return imgName, imgData, nil
}

func pageImagesFromRAR(provider mounts.Provider, relPath string, indexes []int, maxBytes int64) (map[int]PageImageData, error) {
	if len(indexes) == 0 {
		return map[int]PageImageData{}, nil
	}
	if indexes[0] < 0 {
		return nil, fmt.Errorf("page index out of range")
	}

	targets := make(map[int]struct{}, len(indexes))
	maxIndex := indexes[len(indexes)-1]
	for _, idx := range indexes {
		if idx < 0 {
			return nil, fmt.Errorf("page index out of range")
		}
		targets[idx] = struct{}{}
	}

	out := make(map[int]PageImageData, len(indexes))
	imgIdx := 0
	err := withRARReader(provider, relPath, func(rr *rardecode.Reader) error {
		for {
			h, err := rr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			if h == nil || h.IsDir || !ziputil.IsImageName(h.Name) {
				continue
			}
			if imgIdx > maxIndex {
				return nil
			}
			if _, ok := targets[imgIdx]; ok {
				buf, err := readLimited(rr, maxBytes)
				if err != nil {
					return err
				}
				out[imgIdx] = PageImageData{Name: h.Name, Data: buf}
			}
			imgIdx++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("page index out of range")
	}
	return out, nil
}

func with7ZReader(provider mounts.Provider, relPath string, fn func(*sevenzip.ReadCloser) error) error {
	tmpPath, cleanup, err := copyToTempFile(provider, relPath, "zipmanga-7z-*.7z")
	if err != nil {
		return err
	}
	defer cleanup()

	r, err := sevenzip.OpenReader(tmpPath)
	if err != nil {
		return err
	}
	defer r.Close()

	return fn(r)
}

func withRARReader(provider mounts.Provider, relPath string, fn func(*rardecode.Reader) error) error {
	tmpPath, cleanup, err := copyToTempFile(provider, relPath, "zipmanga-rar-*.rar")
	if err != nil {
		return err
	}
	defer cleanup()

	f, err := os.Open(tmpPath)
	if err != nil {
		return err
	}
	defer f.Close()

	r, err := rardecode.NewReader(f, "")
	if err != nil {
		return err
	}
	return fn(r)
}

func copyToTempFile(provider mounts.Provider, relPath, pattern string) (string, func(), error) {
	rc, err := provider.Open(relPath)
	if err != nil {
		return "", nil, err
	}
	defer rc.Close()

	tmp, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", nil, err
	}
	name := tmp.Name()
	cleanup := func() {
		_ = os.Remove(name)
	}
	if _, err := io.Copy(tmp, rc); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", nil, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return name, cleanup, nil
}

func imageFilesFrom7Z(r *sevenzip.ReadCloser) []*sevenzip.File {
	if r == nil {
		return nil
	}
	files := make([]*sevenzip.File, 0, len(r.File))
	for _, f := range r.File {
		if f == nil || f.FileInfo().IsDir() || !ziputil.IsImageName(f.Name) {
			continue
		}
		files = append(files, f)
	}
	return files
}

func buildCoverFrom7ZFiles(images []*sevenzip.File, thumbWidth int) ([]byte, error) {
	if len(images) == 0 {
		return nil, errors.New("archive has no image files")
	}
	type ranked struct {
		f     *sevenzip.File
		score int
	}
	ranks := make([]ranked, 0, len(images))
	for i, f := range images {
		score := rankImageName(f.Name, i)
		ranks = append(ranks, ranked{f: f, score: score})
	}
	sort.SliceStable(ranks, func(i, j int) bool { return ranks[i].score > ranks[j].score })

	var (
		lastErr       error
		blankFallback []byte
	)
	maxTry := len(ranks)
	if maxTry > 18 {
		maxTry = 18
	}
	for i := 0; i < maxTry; i++ {
		rc, err := ranks[i].f.Open()
		if err != nil {
			lastErr = err
			continue
		}
		raw, err := readLimited(rc, 64<<20)
		_ = rc.Close()
		if err != nil {
			lastErr = err
			continue
		}
		cover, err := buildJPEGCoverFromRaw(raw, thumbWidth)
		if err != nil {
			if errors.Is(err, errLikelyBlankCover) && len(blankFallback) == 0 {
				fb, fbErr := buildJPEGCoverFromRawAllowBlank(raw, thumbWidth)
				if fbErr == nil {
					blankFallback = fb
				} else {
					lastErr = fbErr
				}
			}
			lastErr = err
			continue
		}
		return cover, nil
	}
	if len(blankFallback) > 0 {
		return blankFallback, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("failed to generate cover")
}

func rankImageName(name string, idx int) int {
	n := strings.ToLower(name)
	score := 0
	if strings.Contains(n, "cover") || strings.Contains(n, "封面") || strings.Contains(n, "folder") {
		score += 100
	}
	if strings.Contains(n, "001") || strings.Contains(n, "0001") || strings.Contains(n, "01") {
		score += 50
	}
	if !strings.Contains(n, "/") && !strings.Contains(n, "\\") {
		score += 30
	}
	score += 1000 - idx
	return score
}

func normalizePageIndexes(indexes []int) []int {
	if len(indexes) == 0 {
		return nil
	}
	sorted := append([]int(nil), indexes...)
	sort.Ints(sorted)
	out := sorted[:0]
	prev := 0
	for i, idx := range sorted {
		if i == 0 || idx != prev {
			out = append(out, idx)
			prev = idx
		}
	}
	return out
}

func buildJPEGCoverFromRaw(raw []byte, thumbWidth int) ([]byte, error) {
	return buildJPEGCoverFromRawWithPolicy(raw, thumbWidth, false)
}

func buildJPEGCoverFromRawAllowBlank(raw []byte, thumbWidth int) ([]byte, error) {
	return buildJPEGCoverFromRawWithPolicy(raw, thumbWidth, true)
}

func buildJPEGCoverFromRawWithPolicy(raw []byte, thumbWidth int, allowBlank bool) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if !allowBlank && isLikelyBlankImage(img) {
		return nil, errLikelyBlankCover
	}
	resized := resizeImage(img, thumbWidth)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, resized, &jpeg.Options{Quality: 75}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func resizeImage(img image.Image, targetWidth int) image.Image {
	if targetWidth <= 0 {
		targetWidth = 360
	}
	bounds := img.Bounds()
	srcWidth := bounds.Dx()
	srcHeight := bounds.Dy()
	if srcWidth <= targetWidth {
		return img
	}
	targetHeight := int(float64(srcHeight) * (float64(targetWidth) / float64(srcWidth)))
	if targetHeight <= 0 {
		targetHeight = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)
	return dst
}

func isLikelyBlankImage(img image.Image) bool {
	b := img.Bounds()
	w := b.Dx()
	h := b.Dy()
	if w <= 0 || h <= 0 {
		return true
	}
	stepX := w / 32
	if stepX < 1 {
		stepX = 1
	}
	stepY := h / 32
	if stepY < 1 {
		stepY = 1
	}
	var count int64
	var sumY, sumY2 float64
	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			yy := 0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)
			sumY += yy
			sumY2 += yy * yy
			count++
		}
	}
	if count <= 0 {
		return true
	}
	mean := sumY / float64(count)
	variance := (sumY2 / float64(count)) - (mean * mean)
	return variance < 18 && (mean > 242 || mean < 12)
}

func readLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = 64 << 20
	}
	reader := io.LimitReader(r, maxBytes+1)
	buf, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > maxBytes {
		return nil, fmt.Errorf("image too large")
	}
	return buf, nil
}

func normalizeThumbWidth(v int) int {
	if v <= 0 {
		return 360
	}
	return v
}

func GuessImageContentType(name string) string {
	return ziputil.GuessImageContentType(name)
}
