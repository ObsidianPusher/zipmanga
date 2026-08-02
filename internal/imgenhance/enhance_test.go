package imgenhance

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestAnalyzePageProfileDetectsBlackWhiteMangaLikePage(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			c := uint8(255)
			if x%11 == 0 || y%13 == 0 || (x > 18 && x < 46 && y > 20 && y < 24) {
				c = 18
			}
			img.SetNRGBA(x, y, color.NRGBA{R: c, G: c, B: c, A: 255})
		}
	}

	profile := analyzePageProfile(img)
	if !profile.LikelyMangaBW {
		t.Fatal("expected grayscale line-art page to be detected as LikelyMangaBW")
	}
	if !profile.NearGray {
		t.Fatal("expected grayscale line-art page to be detected as NearGray")
	}
}

func TestNormalizeModeCanonicalizesLegacyAliases(t *testing.T) {
	cases := map[string]Mode{
		"":           ModeLevel1,
		"off":        ModeOff,
		"level1":     ModeLevel1,
		"balanced":   ModeLevel1,
		"strong":     ModeLevel1,
		"level2":     ModeLevel2,
		"quality":    ModeLevel2,
		"ai-4x":      ModeLevel2,
		"unexpected": ModeLevel1,
	}
	for input, want := range cases {
		if got := normalizeMode(input); got != want {
			t.Fatalf("normalizeMode(%q)=%q want %q", input, got, want)
		}
	}
}

func TestEnhanceQualityProducesJPEGForMangaLikePage(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 96, 128))
	for y := 0; y < 128; y++ {
		for x := 0; x < 96; x++ {
			c := uint8(242)
			if x%9 == 0 || y%17 == 0 {
				c = 32
			}
			if x > 20 && x < 76 && y > 36 && y < 42 {
				c = 0
			}
			img.SetNRGBA(x, y, color.NRGBA{R: c, G: c, B: c, A: 255})
		}
	}

	var raw bytes.Buffer
	if err := jpeg.Encode(&raw, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("jpeg encode source failed: %v", err)
	}

	out, contentType, err := Enhance(raw.Bytes(), "page.jpg", Options{
		Mode:        ModeLevel2,
		TargetWidth: 1200,
		Source:      SourceImage,
	})
	if err != nil {
		t.Fatalf("Enhance failed: %v", err)
	}
	if contentType != "image/jpeg" {
		t.Fatalf("contentType=%q want image/jpeg", contentType)
	}
	if len(out) == 0 {
		t.Fatal("expected enhanced output bytes")
	}
	if _, err := jpeg.Decode(bytes.NewReader(out)); err != nil {
		t.Fatalf("enhanced output should be valid jpeg: %v", err)
	}
}

func TestEnhanceLevel1PreservesArchiveMangaSourceWhenNotDownscaling(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 96, 128))
	for y := 0; y < 128; y++ {
		for x := 0; x < 96; x++ {
			c := uint8(248)
			if (x+y)%6 == 0 {
				c = 172
			}
			if x%9 == 0 || y%11 == 0 {
				c = 26
			}
			img.SetNRGBA(x, y, color.NRGBA{R: c, G: c, B: c, A: 255})
		}
	}

	var raw bytes.Buffer
	if err := jpeg.Encode(&raw, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("jpeg encode source failed: %v", err)
	}

	out, contentType, err := Enhance(raw.Bytes(), "page.jpg", Options{
		Mode:        ModeLevel1,
		TargetWidth: 0,
		Source:      SourceImage,
	})
	if err != nil {
		t.Fatalf("Enhance failed: %v", err)
	}
	if contentType != "image/jpeg" {
		t.Fatalf("contentType=%q want image/jpeg", contentType)
	}
	if !bytes.Equal(out, raw.Bytes()) {
		t.Fatal("expected archive manga enhancement to preserve original bytes when no downscale is requested")
	}
}

func TestResizeForDisplayDownscalesPNGWithoutLosingImageValidity(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 1200, 1800))
	for y := 0; y < 1800; y++ {
		for x := 0; x < 1200; x++ {
			c := uint8(255)
			if x%13 == 0 || y%17 == 0 {
				c = 24
			}
			img.SetNRGBA(x, y, color.NRGBA{R: c, G: c, B: c, A: 255})
		}
	}

	var raw bytes.Buffer
	if err := png.Encode(&raw, img); err != nil {
		t.Fatalf("png encode source failed: %v", err)
	}

	out, contentType, err := ResizeForDisplay(raw.Bytes(), "page.png", 720)
	if err != nil {
		t.Fatalf("ResizeForDisplay failed: %v", err)
	}
	if contentType != "image/png" {
		t.Fatalf("contentType=%q want image/png", contentType)
	}

	decoded, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decoded resized image failed: %v", err)
	}
	if got := decoded.Bounds().Dx(); got != 720 {
		t.Fatalf("resized width=%d want=720", got)
	}
}

func TestRenderForDisplaySeparatesModesForMangaLikePage(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 1600, 2200))
	for y := 0; y < 2200; y++ {
		for x := 0; x < 1600; x++ {
			c := uint8(246)
			if x%17 == 0 || y%19 == 0 || (x > 420 && x < 1180 && y > 280 && y < 340) {
				c = 30
			}
			img.SetNRGBA(x, y, color.NRGBA{R: c, G: c, B: c, A: 255})
		}
	}

	var raw bytes.Buffer
	if err := jpeg.Encode(&raw, img, &jpeg.Options{Quality: 92}); err != nil {
		t.Fatalf("jpeg encode source failed: %v", err)
	}

	cases := []struct {
		name        string
		mode        Mode
		wantContent string
	}{
		{name: "off", mode: ModeOff, wantContent: "image/jpeg"},
		{name: "level1", mode: ModeLevel1, wantContent: "image/jpeg"},
		{name: "level2", mode: ModeLevel2, wantContent: "image/jpeg"},
	}

	seen := make(map[string][]byte, len(cases))
	for _, tc := range cases {
		out, contentType, err := RenderForDisplay(raw.Bytes(), "page.jpg", Options{
			Mode:        tc.mode,
			TargetWidth: 720,
			Source:      SourceImage,
		})
		if err != nil {
			t.Fatalf("%s: RenderForDisplay failed: %v", tc.name, err)
		}
		if contentType != tc.wantContent {
			t.Fatalf("%s: contentType=%q want %q", tc.name, contentType, tc.wantContent)
		}
		decoded, _, err := image.Decode(bytes.NewReader(out))
		if err != nil {
			t.Fatalf("%s: decode failed: %v", tc.name, err)
		}
		if got := decoded.Bounds().Dx(); got != 720 {
			t.Fatalf("%s: width=%d want 720", tc.name, got)
		}
		seen[tc.name] = out
	}

	if bytes.Equal(seen["off"], seen["level1"]) {
		t.Fatal("expected off and level1 outputs to differ")
	}
	if bytes.Equal(seen["level1"], seen["level2"]) {
		t.Fatal("expected level1 and level2 outputs to differ")
	}
}

func TestEnhancePDFLevel2DownscalesAndUsesPNG(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 1800, 2400))
	for y := 0; y < 2400; y++ {
		for x := 0; x < 1800; x++ {
			c := uint8(248)
			if x%23 == 0 || y%29 == 0 {
				c = 22
			}
			img.SetNRGBA(x, y, color.NRGBA{R: c, G: c, B: c, A: 255})
		}
	}

	var raw bytes.Buffer
	if err := jpeg.Encode(&raw, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("jpeg encode source failed: %v", err)
	}

	out, contentType, err := Enhance(raw.Bytes(), "page.jpg", Options{
		Mode:        ModeLevel2,
		TargetWidth: 900,
		Source:      SourcePDF,
	})
	if err != nil {
		t.Fatalf("Enhance failed: %v", err)
	}
	if contentType != "image/png" {
		t.Fatalf("contentType=%q want image/png", contentType)
	}
	decoded, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if got := decoded.Bounds().Dx(); got != 900 {
		t.Fatalf("width=%d want 900", got)
	}
}
