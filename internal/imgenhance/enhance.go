package imgenhance

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"path/filepath"
	"strings"

	"zipmanga/internal/imgcodec"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

type Mode string

const (
	ModeOff    Mode = "off"
	ModeLevel1 Mode = "level1"
	ModeLevel2 Mode = "level2"

	// Legacy aliases kept for backward compatibility with older persisted values.
	ModeBalanced = ModeLevel1
	ModeQuality  = ModeLevel2
)

type Options struct {
	Mode        Mode
	TargetWidth int
	Source      string
}

type profile struct {
	upscale      float64
	maxEdge      int
	denoiseBlend float64
	unsharpAmt   float64
	readability  float64
	binaryBoost  float64
}

const (
	SourceImage = "image"
	SourcePDF   = "pdf"
)

func normalizeMode(v string) Mode {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off":
		return ModeOff
	case "level2", "quality", "ultra", "ai-2x", "ai-4x":
		return ModeLevel2
	case "level1", "balanced", "clarity", "strong":
		return ModeLevel1
	default:
		return ModeLevel1
	}
}

func normalizeSource(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case SourcePDF:
		return SourcePDF
	default:
		return SourceImage
	}
}

func modeProfile(m Mode, source string) profile {
	isPDF := source == SourcePDF
	switch m {
	case ModeLevel2:
		p := profile{
			upscale:      1.75,
			maxEdge:      5600,
			denoiseBlend: 0.12,
			unsharpAmt:   0.48,
			readability:  0.56,
			binaryBoost:  0.78,
		}
		if isPDF {
			p.upscale = 1.0
			p.maxEdge = 7600
			p.denoiseBlend = 0.04
			p.unsharpAmt = 0.30
			p.readability = 0.66
			p.binaryBoost = 0.92
		}
		return p
	case ModeLevel1:
		p := profile{
			upscale:      1.42,
			maxEdge:      4200,
			denoiseBlend: 0.08,
			unsharpAmt:   0.34,
			readability:  0.34,
			binaryBoost:  0.48,
		}
		if isPDF {
			p.upscale = 1.0
			p.maxEdge = 6400
			p.denoiseBlend = 0.02
			p.unsharpAmt = 0.22
			p.readability = 0.42
			p.binaryBoost = 0.56
		}
		return p
	default:
		return profile{
			upscale:      1.0,
			maxEdge:      3200,
			denoiseBlend: 0,
			unsharpAmt:   0,
			readability:  0,
			binaryBoost:  0,
		}
	}
}

func Enhance(src []byte, fileName string, opt Options) ([]byte, string, error) {
	if len(src) == 0 {
		return nil, "", fmt.Errorf("empty image data")
	}
	mode := normalizeMode(string(opt.Mode))
	source := normalizeSource(opt.Source)

	if mode == ModeOff {
		return src, guessContentType(fileName), nil
	}

	config, _, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return nil, "", err
	}
	srcW := max(1, config.Width)
	srcH := max(1, config.Height)
	pf := modeProfile(mode, source)
	targetW, targetH := resolveTargetSize(srcW, srcH, opt.TargetWidth, source, pf)

	if source == SourceImage && targetW >= srcW {
		return src, guessContentType(fileName), nil
	}

	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, "", err
	}
	in := toNRGBA(img)
	work := resizeForMode(in, targetW, targetH, mode)
	if source == SourcePDF {
		work = postProcessPDF(work, mode)
		return encodePDFByMode(work, mode)
	}
	return encodeByMode(fileName, work, mode)
}

func renderMangaByMode(src []byte, fileName string, targetWidth int, targetHeight int, img *image.NRGBA, mode Mode, preserveOriginal bool) ([]byte, string, error) {
	if img == nil {
		return src, guessContentType(fileName), nil
	}
	b := img.Bounds()
	srcW := max(1, b.Dx())
	if targetWidth <= 0 || targetWidth >= srcW {
		if preserveOriginal {
			return src, guessContentType(fileName), nil
		}
		switch mode {
		case ModeLevel2:
			return encodeLosslessMangaImage(img)
		case ModeLevel1:
			return encodeHighFidelityMangaImage(fileName, img)
		default:
			return encodeDisplayImage(fileName, img)
		}
	}

	dstW := clampInt(targetWidth, 1, srcW)
	dstH := targetHeight
	if dstH <= 0 {
		srcH := max(1, b.Dy())
		dstH = max(1, int(math.Round(float64(dstW)*float64(srcH)/float64(srcW))))
	}
	switch mode {
	case ModeLevel2:
		dst := downscaleForMangaDisplayStrong(img, dstW, dstH)
		return encodeLosslessMangaImage(dst)
	case ModeLevel1:
		dst := downscaleForMangaDisplay(img, dstW, dstH)
		return encodeHighFidelityMangaImage(fileName, dst)
	default:
		dst := downscaleForDisplay(img, dstW, dstH)
		return encodeDisplayImage(fileName, dst)
	}
}

func RenderForDisplay(src []byte, fileName string, opt Options) ([]byte, string, error) {
	if len(src) == 0 {
		return nil, "", fmt.Errorf("empty image data")
	}
	mode := normalizeMode(string(opt.Mode))
	source := normalizeSource(opt.Source)

	config, _, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return nil, "", err
	}
	srcW := max(1, config.Width)
	srcH := max(1, config.Height)
	pf := modeProfile(mode, source)
	targetW, targetH := resolveTargetSize(srcW, srcH, opt.TargetWidth, source, pf)
	if targetW >= srcW {
		return src, guessContentType(fileName), nil
	}
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, "", err
	}
	in := toNRGBA(img)
	work := resizeForMode(in, targetW, targetH, mode)
	return encodeByMode(fileName, work, mode)
}

func ResizeForDisplay(src []byte, fileName string, targetWidth int) ([]byte, string, error) {
	return RenderForDisplay(src, fileName, Options{
		Mode:        ModeLevel1,
		TargetWidth: targetWidth,
		Source:      SourceImage,
	})
}

func resizeForMode(src *image.NRGBA, dstW, dstH int, mode Mode) *image.NRGBA {
	srcB := src.Bounds()
	srcW := max(1, srcB.Dx())
	srcH := max(1, srcB.Dy())
	dstW = clampInt(dstW, 1, srcW)
	dstH = clampInt(dstH, 1, srcH)
	if dstW == srcW && dstH == srcH {
		return src
	}

	out := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
	scaler := xdraw.ApproxBiLinear
	if mode == ModeLevel2 {
		scaler = xdraw.CatmullRom
	}
	scaler.Scale(out, out.Bounds(), src, src.Bounds(), xdraw.Src, nil)
	return out
}

func encodeByMode(fileName string, img image.Image, mode Mode) ([]byte, string, error) {
	if strings.EqualFold(filepath.Ext(strings.TrimSpace(fileName)), imgcodec.LossyWebPExt) {
		data, err := imgcodec.EncodeImageLossyWebP(img)
		return data, "image/webp", err
	}
	buf := bytes.NewBuffer(make([]byte, 0, 256*1024))
	if strings.EqualFold(filepath.Ext(strings.TrimSpace(fileName)), ".png") {
		if err := png.Encode(buf, img); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), "image/png", nil
	}

	quality := 90
	switch mode {
	case ModeLevel2:
		quality = 98
	case ModeLevel1:
		quality = 94
	}
	if err := jpeg.Encode(buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "image/jpeg", nil
}

func encodePDFByMode(img image.Image, mode Mode) ([]byte, string, error) {
	switch mode {
	case ModeLevel2:
		return encodeLosslessMangaImage(img)
	case ModeLevel1:
		return encodePDFJPEGImage(img, 94)
	default:
		return encodePDFJPEGImage(img, 90)
	}
}

func encodePDFJPEGImage(img image.Image, quality int) ([]byte, string, error) {
	buf := bytes.NewBuffer(make([]byte, 0, 256*1024))
	if quality < 1 {
		quality = 1
	}
	if quality > 100 {
		quality = 100
	}
	if err := jpeg.Encode(buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "image/jpeg", nil
}

func postProcessPDF(src *image.NRGBA, mode Mode) *image.NRGBA {
	if mode != ModeLevel2 {
		return src
	}
	return unsharp3x3(src, 0.16, 6)
}

func downscaleForDisplay(src *image.NRGBA, dstW, dstH int) *image.NRGBA {
	srcB := src.Bounds()
	srcW := max(1, srcB.Dx())
	srcH := max(1, srcB.Dy())
	dstW = clampInt(dstW, 1, srcW)
	dstH = clampInt(dstH, 1, srcH)

	work := src
	workW := srcW
	workH := srcH
	for workW >= dstW*2 || workH >= dstH*2 {
		nextW := max(dstW, workW/2)
		nextH := max(dstH, workH/2)
		if nextW == workW && nextH == workH {
			break
		}
		next := image.NewNRGBA(image.Rect(0, 0, nextW, nextH))
		xdraw.ApproxBiLinear.Scale(next, next.Bounds(), work, work.Bounds(), xdraw.Src, nil)
		work = next
		workW = nextW
		workH = nextH
	}

	if workW == dstW && workH == dstH {
		return work
	}

	out := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
	xdraw.ApproxBiLinear.Scale(out, out.Bounds(), work, work.Bounds(), xdraw.Src, nil)
	return out
}

func downscaleForMangaDisplay(src *image.NRGBA, dstW, dstH int) *image.NRGBA {
	srcB := src.Bounds()
	srcW := max(1, srcB.Dx())
	srcH := max(1, srcB.Dy())
	dstW = clampInt(dstW, 1, srcW)
	dstH = clampInt(dstH, 1, srcH)

	work := src
	workW := srcW
	workH := srcH
	for workW >= dstW*2 || workH >= dstH*2 {
		nextW := max(dstW, (workW+1)/2)
		nextH := max(dstH, (workH+1)/2)
		if nextW == workW && nextH == workH {
			break
		}
		work = boxDownscale(work, nextW, nextH)
		workW = nextW
		workH = nextH
	}

	if workW == dstW && workH == dstH {
		return work
	}

	out := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
	xdraw.CatmullRom.Scale(out, out.Bounds(), work, work.Bounds(), xdraw.Src, nil)
	return out
}

func downscaleForMangaDisplayStrong(src *image.NRGBA, dstW, dstH int) *image.NRGBA {
	srcB := src.Bounds()
	srcW := max(1, srcB.Dx())
	srcH := max(1, srcB.Dy())
	dstW = clampInt(dstW, 1, srcW)
	dstH = clampInt(dstH, 1, srcH)

	work := src
	workW := srcW
	workH := srcH
	for workW >= dstW*2 || workH >= dstH*2 {
		nextW := max(dstW, (workW+1)/2)
		nextH := max(dstH, (workH+1)/2)
		if nextW == workW && nextH == workH {
			break
		}
		work = boxDownscale(work, nextW, nextH)
		workW = nextW
		workH = nextH
	}

	if workW == dstW && workH == dstH {
		return work
	}
	if workW >= int(math.Ceil(float64(dstW)*1.12)) || workH >= int(math.Ceil(float64(dstH)*1.12)) {
		return boxDownscale(work, dstW, dstH)
	}

	out := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
	xdraw.ApproxBiLinear.Scale(out, out.Bounds(), work, work.Bounds(), xdraw.Src, nil)
	return out
}

func boxDownscale(src *image.NRGBA, dstW, dstH int) *image.NRGBA {
	srcB := src.Bounds()
	srcW := max(1, srcB.Dx())
	srcH := max(1, srcB.Dy())
	dstW = clampInt(dstW, 1, srcW)
	dstH = clampInt(dstH, 1, srcH)
	out := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))

	for y := 0; y < dstH; y++ {
		sy0 := y * srcH / dstH
		sy1 := (y + 1) * srcH / dstH
		if sy1 <= sy0 {
			sy1 = min(srcH, sy0+1)
		}
		for x := 0; x < dstW; x++ {
			sx0 := x * srcW / dstW
			sx1 := (x + 1) * srcW / dstW
			if sx1 <= sx0 {
				sx1 = min(srcW, sx0+1)
			}

			var sumR, sumG, sumB, sumA, count int
			for sy := sy0; sy < sy1; sy++ {
				row := sy * src.Stride
				for sx := sx0; sx < sx1; sx++ {
					i := row + sx*4
					sumR += int(src.Pix[i+0])
					sumG += int(src.Pix[i+1])
					sumB += int(src.Pix[i+2])
					sumA += int(src.Pix[i+3])
					count++
				}
			}
			if count <= 0 {
				continue
			}
			di := y*out.Stride + x*4
			out.Pix[di+0] = uint8(sumR / count)
			out.Pix[di+1] = uint8(sumG / count)
			out.Pix[di+2] = uint8(sumB / count)
			out.Pix[di+3] = uint8(sumA / count)
		}
	}
	return out
}

func encodeBySource(fileName string, img image.Image, mode Mode) ([]byte, string, error) {
	_ = strings.TrimSpace(fileName)
	buf := bytes.NewBuffer(make([]byte, 0, 256*1024))

	quality := 92
	if mode == ModeLevel2 {
		quality = 95
	}
	if err := jpeg.Encode(buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "image/jpeg", nil
}

func encodeDisplayImage(fileName string, img image.Image) ([]byte, string, error) {
	buf := bytes.NewBuffer(make([]byte, 0, 256*1024))
	if strings.EqualFold(filepath.Ext(strings.TrimSpace(fileName)), ".png") {
		if err := png.Encode(buf, img); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), "image/png", nil
	}
	if err := jpeg.Encode(buf, img, &jpeg.Options{Quality: 95}); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "image/jpeg", nil
}

func encodeHighFidelityMangaImage(fileName string, img image.Image) ([]byte, string, error) {
	buf := bytes.NewBuffer(make([]byte, 0, 256*1024))
	if strings.EqualFold(filepath.Ext(strings.TrimSpace(fileName)), ".png") {
		if err := png.Encode(buf, img); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), "image/png", nil
	}
	if err := jpeg.Encode(buf, img, &jpeg.Options{Quality: 98}); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "image/jpeg", nil
}

func encodeLosslessMangaImage(img image.Image) ([]byte, string, error) {
	buf := bytes.NewBuffer(make([]byte, 0, 256*1024))
	if err := png.Encode(buf, img); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "image/png", nil
}

func toNRGBA(src image.Image) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	xdraw.Draw(dst, dst.Bounds(), src, b.Min, xdraw.Src)
	return dst
}

func denoise3x3(src *image.NRGBA, blend float64) *image.NRGBA {
	if blend <= 0 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 3 || h < 3 {
		return src
	}
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	copy(out.Pix, src.Pix)
	stride := src.Stride
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			i := y*stride + x*4
			for c := 0; c < 3; c++ {
				sum := 0
				for ky := -1; ky <= 1; ky++ {
					for kx := -1; kx <= 1; kx++ {
						idx := (y+ky)*stride + (x+kx)*4 + c
						sum += int(src.Pix[idx])
					}
				}
				avg := float64(sum) / 9.0
				base := float64(src.Pix[i+c])
				v := base*(1.0-blend) + avg*blend
				out.Pix[i+c] = uint8(clampInt(int(math.Round(v)), 0, 255))
			}
		}
	}
	return out
}

func unsharp3x3(src *image.NRGBA, amount float64, threshold uint8) *image.NRGBA {
	if amount <= 0 {
		return src
	}
	blur := boxBlur3x3(src)
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	copy(out.Pix, src.Pix)

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*src.Stride + x*4
			for c := 0; c < 3; c++ {
				base := float64(src.Pix[i+c])
				bl := float64(blur.Pix[i+c])
				diff := base - bl
				if math.Abs(diff) < float64(threshold) {
					continue
				}
				v := base + diff*amount
				out.Pix[i+c] = uint8(clampInt(int(math.Round(v)), 0, 255))
			}
			out.Pix[i+3] = src.Pix[i+3]
		}
	}
	return out
}

func boxBlur3x3(src *image.NRGBA) *image.NRGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	if w < 3 || h < 3 {
		copy(out.Pix, src.Pix)
		return out
	}
	copy(out.Pix, src.Pix)
	stride := src.Stride
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			i := y*stride + x*4
			for c := 0; c < 3; c++ {
				sum := 0
				for ky := -1; ky <= 1; ky++ {
					for kx := -1; kx <= 1; kx++ {
						idx := (y+ky)*stride + (x+kx)*4 + c
						sum += int(src.Pix[idx])
					}
				}
				out.Pix[i+c] = uint8(sum / 9)
			}
			a := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA).A
			out.Pix[i+3] = a
		}
	}
	return out
}

type pageProfile struct {
	LikelyMangaBW bool
	NearGray      bool
	Low           int
	High          int
}

func analyzePageProfile(src *image.NRGBA) pageProfile {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 1 || h <= 1 {
		return pageProfile{}
	}
	hist := [256]int{}
	total := 0
	grayScore := 0.0
	hiContrast := 0
	step := 1
	if w*h > 5_000_000 {
		step = 2
	}
	for y := 0; y < h; y += step {
		row := y * src.Stride
		for x := 0; x < w; x += step {
			i := row + x*4
			r := int(src.Pix[i+0])
			g := int(src.Pix[i+1])
			bb := int(src.Pix[i+2])
			l := luma8(r, g, bb)
			hist[l]++
			total++
			grayScore += float64(absInt(r-g)+absInt(g-bb)+absInt(bb-r)) / 3.0
			if l < 42 || l > 228 {
				hiContrast++
			}
		}
	}
	if total <= 0 {
		return pageProfile{}
	}
	nearGray := (grayScore / float64(total)) < 8.5
	low := percentileFromHist(hist[:], total, 0.012)
	high := percentileFromHist(hist[:], total, 0.992)
	contrastSpread := high - low
	contrastRatio := float64(hiContrast) / float64(total)
	likelyBW := nearGray && (contrastSpread >= 48 || contrastRatio >= 0.22)
	return pageProfile{
		LikelyMangaBW: likelyBW,
		NearGray:      nearGray,
		Low:           low,
		High:          high,
	}
}

func enhanceMonoReadability(src *image.NRGBA, strength float64, binaryBoost float64, analysis pageProfile) *image.NRGBA {
	if strength <= 0 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 1 || h <= 1 {
		return src
	}
	low := analysis.Low
	high := analysis.High
	if high-low < 20 {
		low = max(0, low-10)
		high = min(255, high+18)
	}
	if high <= low {
		return src
	}

	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	copy(out.Pix, src.Pix)
	gamma := 1.0 - strength*0.12
	if gamma < 0.82 {
		gamma = 0.82
	}
	if !analysis.NearGray {
		gamma = 1.0 - strength*0.05
	}

	den := float64(high - low)
	for y := 0; y < h; y++ {
		row := y * src.Stride
		for x := 0; x < w; x++ {
			i := row + x*4
			r := int(src.Pix[i+0])
			g := int(src.Pix[i+1])
			bb := int(src.Pix[i+2])
			l := luma8(r, g, bb)
			norm := float64(l-low) / den
			if norm < 0 {
				norm = 0
			}
			if norm > 1 {
				norm = 1
			}
			mapped := math.Pow(norm, gamma)
			if analysis.NearGray {
				mapped = clampFloat((mapped-0.5)*(1.0+strength*0.8)+0.5, 0, 1)
			}
			if analysis.LikelyMangaBW {
				mapped = applyMangaInkCurve(mapped, binaryBoost)
			}
			targetL := mapped * 255.0
			scale := targetL / math.Max(1.0, float64(l))
			if !analysis.NearGray {
				scale = 1.0 + (scale-1.0)*0.45
			}
			out.Pix[i+0] = uint8(clampInt(int(math.Round(float64(r)*scale)), 0, 255))
			out.Pix[i+1] = uint8(clampInt(int(math.Round(float64(g)*scale)), 0, 255))
			out.Pix[i+2] = uint8(clampInt(int(math.Round(float64(bb)*scale)), 0, 255))
		}
	}
	if analysis.LikelyMangaBW {
		out = strengthenInkEdges(out, 0.28+binaryBoost*0.16, 10)
	}
	return out
}

func applyMangaInkCurve(v float64, strength float64) float64 {
	if strength <= 0 {
		return v
	}
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	paperLift := 0.10 * strength
	inkDrop := 0.14 * strength
	switch {
	case v <= 0.18:
		return clampFloat(v*(1.0-inkDrop), 0, 1)
	case v >= 0.82:
		return clampFloat(v+(1.0-v)*paperLift, 0, 1)
	default:
		mid := (v - 0.18) / 0.64
		s := mid * mid * (3 - 2*mid)
		contrast := 1.0 + 0.18*strength
		mapped := ((s - 0.5) * contrast) + 0.5
		return clampFloat(0.18+mapped*0.64, 0, 1)
	}
}

func strengthenInkEdges(src *image.NRGBA, amount float64, threshold uint8) *image.NRGBA {
	if amount <= 0 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 3 || h < 3 {
		return src
	}
	blur := boxBlur3x3(src)
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	copy(out.Pix, src.Pix)
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			i := y*src.Stride + x*4
			base := luma8(int(src.Pix[i+0]), int(src.Pix[i+1]), int(src.Pix[i+2]))
			bl := luma8(int(blur.Pix[i+0]), int(blur.Pix[i+1]), int(blur.Pix[i+2]))
			diff := float64(base - bl)
			if math.Abs(diff) < float64(threshold) {
				continue
			}
			if diff >= 0 {
				continue
			}
			delta := diff * amount
			for c := 0; c < 3; c++ {
				v := float64(src.Pix[i+c]) + delta
				out.Pix[i+c] = uint8(clampInt(int(math.Round(v)), 0, 255))
			}
			out.Pix[i+3] = src.Pix[i+3]
		}
	}
	return out
}

func luma8(r, g, b int) int {
	return (77*r + 150*g + 29*b) >> 8
}

func percentileFromHist(hist []int, total int, p float64) int {
	if total <= 0 {
		return 0
	}
	target := int(math.Round(float64(total) * p))
	if target < 0 {
		target = 0
	}
	sum := 0
	for i, v := range hist {
		sum += v
		if sum >= target {
			return i
		}
	}
	return len(hist) - 1
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func clampFloat(v, minV, maxV float64) float64 {
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
}

func guessContentType(name string) string {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
	switch ext {
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}

func resolveTargetSize(srcW, srcH, requestedWidth int, source string, pf profile) (int, int) {
	targetW := srcW
	switch source {
	case SourcePDF:
		if requestedWidth > 0 {
			targetW = clampInt(requestedWidth, 1, min(srcW, pf.maxEdge))
		}
	default:
		if requestedWidth > 0 && requestedWidth < srcW {
			targetW = clampInt(requestedWidth, 1, srcW)
		}
	}
	targetH := max(1, int(math.Round(float64(targetW)*float64(srcH)/float64(srcW))))
	if pf.maxEdge > 0 && targetH > pf.maxEdge {
		targetH = pf.maxEdge
		targetW = max(1, int(math.Round(float64(targetH)*float64(srcW)/float64(srcH))))
	}
	return targetW, targetH
}

func clampInt(v, minV, maxV int) int {
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
