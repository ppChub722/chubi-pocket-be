//go:build ocrsmoke

package ocr

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestSmoke runs the real tesseract over every image in $OCR_SMOKE_DIR and
// prints what it read, for each psm × preprocess. Needs tesseract, so it
// runs inside the BE image:
//
//	GOOS=linux go test -c -tags ocrsmoke -o ocr.test ./internal/modules/imports/ocr
//	docker run --rm -v "$PWD:/w" -e OCR_SMOKE_DIR=/w/slips --entrypoint /w/ocr.test chubi-be:prod -test.v
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
