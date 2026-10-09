package ocr

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg" // decoders for image.Decode
	"image/png"

	xdraw "golang.org/x/image/draw"
)

// minWidth — narrower images get upscaled to about this width. Tesseract
// reads best when letters are ~30 px tall; Thai vowel and tone marks
// above / below the line suffer first when they are smaller.
const minWidth = 1600

// maxScale caps the upscale (a tiny thumbnail won't get readable anyway).
const maxScale = 3.0

// prepared — what goes into Tesseract, plus how to map its boxes back.
type prepared struct {
	data          []byte
	width, height int     // of the original image
	scale         float64 // OCR pixels per original pixel
}

// prepare decodes img and, when preprocess is on, turns it grayscale and
// upscales it if it's narrow. Off → the original bytes go in untouched.
// No binarising here: Tesseract's own Otsu pass handles clean screenshots.
func prepare(img []byte, preprocess bool) (*prepared, error) {
	if !preprocess {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(img))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadImage, err)
		}
		return &prepared{data: img, width: cfg.Width, height: cfg.Height, scale: 1}, nil
	}

	src, _, err := image.Decode(bytes.NewReader(img))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadImage, err)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()

	gray := image.NewGray(image.Rect(0, 0, w, h))
	draw.Draw(gray, gray.Bounds(), src, b.Min, draw.Src)

	scale := 1.0
	var out image.Image = gray
	if w > 0 && w < minWidth {
		scale = min(float64(minWidth)/float64(w), maxScale)
		dst := image.NewGray(image.Rect(0, 0, int(float64(w)*scale), int(float64(h)*scale)))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), gray, gray.Bounds(), draw.Src, nil)
		out = dst
	}

	var buf bytes.Buffer
	// Fast compression — the PNG only lives for the pipe to tesseract.
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, out); err != nil {
		return nil, err
	}
	return &prepared{data: buf.Bytes(), width: w, height: h, scale: scale}, nil
}
