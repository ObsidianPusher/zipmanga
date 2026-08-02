package ziputil

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"zipmanga/internal/mounts"

	_ "golang.org/x/image/bmp"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

type Archive struct {
	Reader *zip.Reader
	close  func() error
}

func (a *Archive) Close() error {
	if a == nil || a.close == nil {
		return nil
	}
	return a.close()
}

func OpenArchive(provider mounts.Provider, relPath string) (*Archive, error) {
	rc, size, err := provider.OpenReaderAt(relPath)
	if err != nil {
		if errors.Is(err, mounts.ErrReaderAtUnsupported) {
			return openArchiveFromStream(provider, relPath)
		}
		return nil, err
	}
	zr, err := zip.NewReader(rc, size)
	if err != nil {
		_ = rc.Close()
		return nil, err
	}
	return &Archive{
		Reader: zr,
		close:  rc.Close,
	}, nil
}

func openArchiveFromStream(provider mounts.Provider, relPath string) (*Archive, error) {
	stream, err := provider.Open(relPath)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	tmp, err := os.CreateTemp("", "zipmanga-zip-*.cbz")
	if err != nil {
		return nil, err
	}

	if _, err := io.Copy(tmp, stream); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, err
	}

	st, err := tmp.Stat()
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, err
	}

	zr, err := zip.NewReader(tmp, st.Size())
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, err
	}

	return &Archive{
		Reader: zr,
		close: func() error {
			name := tmp.Name()
			err := tmp.Close()
			_ = os.Remove(name)
			return err
		},
	}, nil
}

// FirstImageFile 快速找到zip中的封面图片，极致优化性能
func FirstImageFile(zr *zip.Reader) (*zip.File, error) {
	var candidates []*zip.File

	// 单次遍历收集所有图片，同时检查是否有明显的封面文件
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if IsImageName(f.Name) {
			candidates = append(candidates, f)
		}
	}

	if len(candidates) == 0 {
		return nil, errors.New("zip has no image files")
	}

	// 快速找到最佳封面，避免完整排序
	bestIdx := 0
	bestScore := -1

	for i, f := range candidates {
		name := strings.ToLower(f.Name)
		score := 0

		// 封面关键词加分
		if strings.Contains(name, "cover") || strings.Contains(name, "封面") || strings.Contains(name, "folder") {
			score += 100
		}
		// 首页编号加分
		if strings.Contains(name, "001") || strings.Contains(name, "0001") || strings.Contains(name, "01") {
			score += 50
		}
		// 根目录图片优先（非子目录）
		if !strings.Contains(name, "/") && !strings.Contains(name, "\\") {
			score += 30
		}
		// 文件名字典序靠前
		score += (1000 - i)

		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}

	return candidates[bestIdx], nil
}

func ImageFiles(zr *zip.Reader) []*zip.File {
	out := make([]*zip.File, 0)
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if IsImageName(f.Name) {
			out = append(out, f)
		}
	}
	return out
}

func IsImageName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".avif":
		return true
	default:
		return false
	}
}

func BuildCoverJPEG(zr *zip.Reader, thumbWidth int) ([]byte, error) {
	if thumbWidth <= 0 {
		thumbWidth = 360
	}

	images := rankedImageFiles(zr)
	if len(images) == 0 {
		return nil, errors.New("zip has no image files")
	}

	var lastErr error
	var fallback []byte
	maxTry := len(images)
	if maxTry > 18 {
		maxTry = 18
	}
	for i := 0; i < maxTry; i++ {
		imgFile := images[i]
		rc, err := imgFile.Open()
		if err != nil {
			lastErr = err
			continue
		}

		img, _, err := image.Decode(rc)
		_ = rc.Close()

		if err != nil {
			lastErr = err
			continue
		}

		if isLikelyBlankImage(img) {
			if fallback == nil {
				resized := resizeImageOptimized(img, thumbWidth)
				var fb bytes.Buffer
				if jpeg.Encode(&fb, resized, &jpeg.Options{Quality: 72}) == nil {
					fallback = fb.Bytes()
				}
			}
			continue
		}

		resized := resizeImageOptimized(img, thumbWidth)

		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, resized, &jpeg.Options{Quality: 75}); err != nil {
			lastErr = err
			continue
		}

		return buf.Bytes(), nil
	}
	if len(fallback) > 0 {
		return fallback, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("failed to decode any image: %w", lastErr)
	}
	return nil, errors.New("failed to generate cover")
}

type imageRank struct {
	f     *zip.File
	score int
	idx   int
}

func rankedImageFiles(zr *zip.Reader) []*zip.File {
	ranks := make([]imageRank, 0, len(zr.File))
	for i, f := range zr.File {
		if f.FileInfo().IsDir() || !IsImageName(f.Name) {
			continue
		}
		name := strings.ToLower(f.Name)
		score := 0
		if strings.Contains(name, "cover") || strings.Contains(name, "封面") || strings.Contains(name, "folder") {
			score += 100
		}
		if strings.Contains(name, "001") || strings.Contains(name, "0001") || strings.Contains(name, "01") {
			score += 50
		}
		if !strings.Contains(name, "/") && !strings.Contains(name, "\\") {
			score += 30
		}
		score += 1000 - i
		ranks = append(ranks, imageRank{f: f, score: score, idx: i})
	}
	sort.SliceStable(ranks, func(i, j int) bool {
		return ranks[i].score > ranks[j].score
	})
	out := make([]*zip.File, 0, len(ranks))
	for _, r := range ranks {
		out = append(out, r.f)
	}
	return out
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
	if variance < 18 && (mean > 242 || mean < 12) {
		return true
	}
	return false
}

// tryBuildCoverFromFile 尝试从单个文件构建封面，快速失败
func tryBuildCoverFromFile(imgFile *zip.File, thumbWidth int) ([]byte, bool) {
	rc, err := imgFile.Open()
	if err != nil {
		return nil, false
	}
	defer rc.Close()

	img, _, err := image.Decode(rc)
	if err != nil {
		return nil, false
	}

	resized := resizeImageOptimized(img, thumbWidth)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, resized, &jpeg.Options{Quality: 75}); err != nil {
		return nil, false
	}

	return buf.Bytes(), true
}

// resizeImageOptimized 优化的图片缩放函数
func resizeImageOptimized(img image.Image, targetWidth int) image.Image {
	bounds := img.Bounds()
	srcWidth := bounds.Dx()
	srcHeight := bounds.Dy()

	// 如果已经很小了，直接返回
	if srcWidth <= targetWidth {
		return img
	}

	// 计算目标高度，保持宽高比
	targetHeight := int(float64(srcHeight) * (float64(targetWidth) / float64(srcWidth)))
	if targetHeight <= 0 {
		targetHeight = 1
	}

	// 选择合适的缩放算法
	// 对于大幅缩小，使用更快的算法
	var scaler draw.Scaler
	scaleRatio := float64(targetWidth) / float64(srcWidth)

	if scaleRatio < 0.3 {
		// 大幅缩小，使用BiLinear，速度更快
		scaler = draw.BiLinear
	} else {
		// 小幅缩小，使用CatmullRom保持质量
		scaler = draw.CatmullRom
	}

	dst := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	scaler.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)
	return dst
}

func PageCount(zr *zip.Reader) int {
	return len(ImageFiles(zr))
}

func PageFile(zr *zip.Reader, index int) (*zip.File, error) {
	images := ImageFiles(zr)
	if index < 0 || index >= len(images) {
		return nil, fmt.Errorf("page index out of range")
	}
	return images[index], nil
}

func GuessImageContentType(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	ct := mime.TypeByExtension(ext)
	if ct == "" {
		return "application/octet-stream"
	}
	return ct
}

// 保留原有的resizeToWidth函数，用于向后兼容
func resizeToWidth(img image.Image, width int) image.Image {
	return resizeImageOptimized(img, width)
}
