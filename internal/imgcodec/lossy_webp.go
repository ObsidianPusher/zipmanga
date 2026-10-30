package imgcodec

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"sync"

	gowebp "github.com/linzeyan/webp-go"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

const LossyWebPExt = ".webp"
const LossyWebPQuality = float32(75)
const LossyWebPMethod = 0

type LossyWebPEncodeOptions struct {
	Quality float32
	Method  int
}

var lossyWebPBufferPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

func EncodeLossyWebP(src []byte) ([]byte, error) {
	return EncodeLossyWebPWithOptions(src, LossyWebPEncodeOptions{
		Quality: LossyWebPQuality,
		Method:  LossyWebPMethod,
	})
}

func EncodeLossyWebPWithOptions(src []byte, options LossyWebPEncodeOptions) ([]byte, error) {
	if len(src) == 0 {
		return nil, fmt.Errorf("image data is empty")
	}

	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}

	return EncodeImageLossyWebPWithOptions(img, options)
}

func EncodeLossyWebPReaderWithOptions(src io.Reader, options LossyWebPEncodeOptions) ([]byte, error) {
	if src == nil {
		return nil, fmt.Errorf("image reader is nil")
	}
	img, _, err := image.Decode(src)
	if err != nil {
		return nil, err
	}
	return EncodeImageLossyWebPWithOptions(img, options)
}

func EncodeImageLossyWebP(img image.Image) ([]byte, error) {
	return EncodeImageLossyWebPWithOptions(img, LossyWebPEncodeOptions{
		Quality: LossyWebPQuality,
		Method:  LossyWebPMethod,
	})
}

func EncodeImageLossyWebPWithOptions(img image.Image, options LossyWebPEncodeOptions) ([]byte, error) {
	if img == nil {
		return nil, fmt.Errorf("image is nil")
	}
	normalized, err := normalizeLossyWebPEncodeOptions(options)
	if err != nil {
		return nil, err
	}

	buf := lossyWebPBufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer lossyWebPBufferPool.Put(buf)

	encodeOptions := &gowebp.Options{
		Lossy:   true,
		Quality: normalized.Quality,
		Method:  normalized.Method,
	}
	if err := gowebp.Encode(buf, img, encodeOptions); err != nil {
		// VP8 has stricter dimension and partition limits than VP8L. Retry
		// losslessly without resizing the page or retaining partial VP8 output.
		buf.Reset()
		if fallbackErr := gowebp.Encode(buf, img, nil); fallbackErr != nil {
			return nil, fmt.Errorf("lossy webp: %v; lossless fallback: %w", err, fallbackErr)
		}
	}
	if buf.Len() == 0 {
		return nil, fmt.Errorf("encoded webp is empty")
	}

	out := make([]byte, buf.Len())
	copy(out, buf.Bytes())
	return out, nil
}

func normalizeLossyWebPEncodeOptions(options LossyWebPEncodeOptions) (LossyWebPEncodeOptions, error) {
	if options.Quality <= 0 {
		options.Quality = LossyWebPQuality
	}
	if options.Quality < 1 || options.Quality > 100 {
		return LossyWebPEncodeOptions{}, fmt.Errorf("webp quality must be between 1 and 100")
	}
	if options.Method < 0 {
		options.Method = LossyWebPMethod
	}
	if options.Method < 0 || options.Method > 6 {
		return LossyWebPEncodeOptions{}, fmt.Errorf("webp method must be between 0 and 6")
	}
	return options, nil
}
