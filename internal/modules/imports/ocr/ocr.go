// Package ocr — image → text (0.3.1). A bank slip goes in, its text comes
// out: the whole text plus each line with its box and confidence, so the
// app can draw what was read over the slip.
//
// The only engine is Tesseract (Apache 2.0), run through its CLI. The
// Docker image ships it with `tha` + `eng` from tessdata_best. Nothing
// here turns text into a draft — that is the Parser in 0.3.2.
package ocr

import (
	"context"
	"errors"
)

var (
	// ErrUnavailable — the tesseract binary isn't installed (e.g. a local
	// `go run` on a machine without it).
	ErrUnavailable = errors.New("ocr engine not available")
	// ErrTimeout — reading took longer than the configured limit.
	ErrTimeout = errors.New("ocr timed out")
	// ErrBadImage — the bytes couldn't be decoded as PNG / JPEG.
	ErrBadImage = errors.New("image could not be decoded")
)

// Page segmentation modes worth trying on slips (`--psm`):
// 3 auto · 4 one column of mixed sizes · 6 one uniform block · 11 sparse.
var AllowedPSM = map[int]bool{3: true, 4: true, 6: true, 11: true}

// DefaultPSM — 11 (sparse text). Picked on 12 real K PLUS slips (owner
// 2026-10-09): it read the counterparty's name right 11/12, where 3/4
// mangled names next to the PromptPay logo (7/12) and 6 pulled the
// background art into the lines. Its extra junk lines are harmless — the
// text goes to an LLM next. Re-check when other banks' slips come in.
const DefaultPSM = 11

// Options for one read.
type Options struct {
	PSM int // 0 → DefaultPSM
	// Preprocess — grayscale + upscale small images before OCR. Off by
	// default: on the K PLUS slips it made dates worse (1/12 vs 8/12).
	Preprocess bool
}

// Box in pixels of the uploaded image (not the upscaled one).
type Box struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Line — one line of text as Tesseract segmented it.
type Line struct {
	Text string  `json:"text"`
	Conf float64 `json:"conf"` // mean word confidence, 0–100
	Box  Box     `json:"box"`
}

// Result of one read.
type Result struct {
	Text  string  `json:"text"`
	Lines []Line  `json:"lines"`
	Conf  float64 `json:"conf"` // mean word confidence, 0–100
	// Size of the uploaded image — line boxes are in these pixels.
	Width  int `json:"width"`
	Height int `json:"height"`
	// Settings the read ran with.
	Lang       string  `json:"lang"`
	PSM        int     `json:"psm"`
	Preprocess bool    `json:"preprocess"`
	Scale      float64 `json:"scale"` // upscale applied before OCR (1 = none)
	// Time spent: preparing the image, and Tesseract itself.
	PrepMs int64 `json:"prep_ms"`
	OCRMs  int64 `json:"ocr_ms"`
}

// Reader reads the text off an image.
type Reader interface {
	Read(ctx context.Context, img []byte, opt Options) (*Result, error)
}
