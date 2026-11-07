package imgenhance

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	"zipmanga/internal/imgcodec"
)

func webpPageFixture(t testing.TB) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 1024, 1536))
	for y := 0; y < 1536; y++ {
		for x := 0; x < 1024; x++ {
			v := uint8(245)
			if x%17 == 0 || y%19 == 0 {
				v = 30
			}
			img.SetNRGBA(x, y, color.NRGBA{R: v, G: v, B: v, A: 255})
		}
	}
	raw, err := imgcodec.EncodeImageLossyWebP(img)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Returning an unchanged WebP must not allocate a full RGBA pixel buffer.
// The budget is tied to image size, not timing or decoder implementation details.
func TestUnchangedWebPAvoidsPixelBufferAllocation(t *testing.T) {
	raw := webpPageFixture(t)
	for _, tc := range []struct {
		name   string
		render func([]byte, string, Options) ([]byte, string, error)
		mode   Mode
	}{
		{"off", RenderForDisplay, ModeOff},
		{"level1", Enhance, ModeLevel1},
		{"level2", Enhance, ModeLevel2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opt := Options{Mode: tc.mode, TargetWidth: 1280, Source: SourceImage}
			result := testing.Benchmark(func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					out, ct, err := tc.render(raw, "page.webp", opt)
					if err != nil {
						b.Fatal(err)
					}
					if ct != "image/webp" || !bytes.Equal(out, raw) {
						b.Fatal("unchanged WebP must preserve its bytes and content type")
					}
				}
			})
			t.Logf("%s; %s", result.String(), result.MemString())
			if result.AllocedBytesPerOp() >= 1024*1536*4 {
				t.Fatalf("unchanged WebP allocated %d bytes/op: a full pixel buffer is unnecessary", result.AllocedBytesPerOp())
			}
		})
	}
}
