package ocr

import (
	"context"
	"errors"
)

var (
	ErrUnavailable = errors.New("ocr engine not available")
	ErrTimeout = errors.New("ocr timed out")
	ErrBadImage = errors.New("image could not be decoded")
)

var AllowedPSM = map[int]bool{3: true, 4: true, 6: true, 11: true}

const DefaultPSM = 11

type Options struct {
	PSM int // 0 → DefaultPSM
	Preprocess bool
}

type Box struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

type Line struct {
	Text string  `json:"text"`
	Conf float64 `json:"conf"` // mean word confidence, 0–100
	Box  Box     `json:"box"`
}

type Result struct {
	Text  string  `json:"text"`
	Lines []Line  `json:"lines"`
	Conf  float64 `json:"conf"` // mean word confidence, 0–100
	Width  int `json:"width"`
	Height int `json:"height"`
	Lang       string  `json:"lang"`
	PSM        int     `json:"psm"`
	Preprocess bool    `json:"preprocess"`
	Scale      float64 `json:"scale"` // upscale applied before OCR (1 = none)
	PrepMs int64 `json:"prep_ms"`
	OCRMs  int64 `json:"ocr_ms"`
}

type Reader interface {
	Read(ctx context.Context, img []byte, opt Options) (*Result, error)
}
