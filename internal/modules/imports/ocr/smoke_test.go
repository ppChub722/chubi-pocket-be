//go:build ocrsmoke

package ocr

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSmoke(t *testing.T) {
	dir := os.Getenv("OCR_SMOKE_DIR")
	if dir == "" {
		t.Skip("OCR_SMOKE_DIR not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	r := NewTesseract(Config{})
	for _, f := range files {
		img, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, psm := range []int{4, 6} {
			for _, pre := range []bool{true, false} {
				res, err := r.Read(context.Background(), img, Options{PSM: psm, Preprocess: pre})
				if err != nil {
					t.Errorf("%s psm=%d pre=%v: %v", filepath.Base(f), psm, pre, err)
					continue
				}
				t.Logf("── %s psm=%d pre=%v scale=%v conf=%v prep=%dms ocr=%dms lines=%d\n%s",
					filepath.Base(f), psm, pre, res.Scale, res.Conf, res.PrepMs, res.OCRMs,
					len(res.Lines), res.Text)
			}
		}
	}
}
