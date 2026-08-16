package imgcodec

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestEncodeLossyWebPFromPNG(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 12, 12))
	for y := 0; y < 12; y++ {
		for x := 0; x < 12; x++ {
			src.Set(x, y, color.NRGBA{
				R: uint8(x * 11),
				G: uint8(y * 9),
				B: 200,
				A: 255,
			})
		}
	}

	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, src); err != nil {
		t.Fatalf("png.Encode failed: %v", err)
	}

	got, err := EncodeLossyWebP(pngBuf.Bytes())
	if err != nil {
		t.Fatalf("EncodeLossyWebP failed: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected encoded webp bytes")
	}
	if !bytes.HasPrefix(got, []byte("RIFF")) {
		t.Fatalf("encoded webp missing RIFF header: %q", got[:minInt(len(got), 4)])
	}

	decoded, _, err := image.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("image.Decode(webp) failed: %v", err)
	}
	if decoded.Bounds().Dx() != 12 || decoded.Bounds().Dy() != 12 {
		t.Fatalf("decoded bounds=%v want 12x12", decoded.Bounds())
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
