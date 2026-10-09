package imports

// OCR dump (0.3.1 tuning, owner 2026-10-09) — a switch for comparing OCR
// modes on real slips. Kept (not deleted) while other banks' slips are
// still to come; off unless OCR_DUMP=1, and never in production (main.go).
//
// Every slip that scan-slip reads OK is read again in the background in
// every mode (psm × preprocess) and dumped to <dir>/<time>-<file>/:
// the original image + result.json with each mode's text, lines, conf and
// timings — so the modes can be compared side by side.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/imports/ocr"
)

type ocrDumper struct {
	dir    string
	reader ocr.Reader
}

// EnableOCRDump turns the dump on.
func (h *Handler) EnableOCRDump(dir string, reader ocr.Reader) {
	h.dump = &ocrDumper{dir: dir, reader: reader}
	h.log.Warn("imports: OCR dump ON — every scanned slip is re-read in all modes", "dir", dir)
}

type dumpVariant struct {
	PSM        int         `json:"psm"`
	Preprocess bool        `json:"preprocess"`
	Error      string      `json:"error,omitempty"`
	Result     *ocr.Result `json:"result,omitempty"`
}

type dumpFile struct {
	Image       string `json:"image"` // file next to this json
	Filename    string `json:"filename"`
	FileKey     string `json:"file_key"`
	TransRef    string `json:"trans_ref"` // from the phone's QR read
	Size        int    `json:"size"`
	ContentType string `json:"content_type"`
	// The mode the app asked for — what the lab showed.
	Served   dumpVariant   `json:"served"`
	Variants []dumpVariant `json:"variants"`
	DumpedAt time.Time     `json:"dumped_at"`
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// queueOCRDump re-reads img in every mode in the background and writes
// the dump. No-op when the dump is off.
func (h *Handler) queueOCRDump(img []byte, filename, key, transRef, contentType string, served ocr.Options, servedRes *ocr.Result) {
	d := h.dump
	if d == nil {
		return
	}
	go func() {
		ext := ".png"
		if contentType == "image/jpeg" {
			ext = ".jpg"
		}
		folder := filepath.Join(d.dir,
			time.Now().Format("20060102-150405")+"-"+unsafeName.ReplaceAllString(filename, "_"))
		if err := os.MkdirAll(folder, 0o755); err != nil {
			h.log.Error("imports: OCR dump mkdir", "err", err)
			return
		}
		_ = os.WriteFile(filepath.Join(folder, "image"+ext), img, 0o644)

		var modes []ocr.Options
		for _, psm := range []int{3, 4, 6, 11} {
			for _, pre := range []bool{false, true} {
				modes = append(modes, ocr.Options{PSM: psm, Preprocess: pre})
			}
		}
		variants := make([]dumpVariant, len(modes))
		var wg sync.WaitGroup
		for i, m := range modes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				defer cancel()
				v := dumpVariant{PSM: m.PSM, Preprocess: m.Preprocess}
				res, err := d.reader.Read(ctx, img, m)
				if err != nil {
					v.Error = err.Error()
				} else {
					v.Result = res
				}
				variants[i] = v
			}()
		}
		wg.Wait()

		out := dumpFile{
			Image:       "image" + ext,
			Filename:    filename,
			FileKey:     key,
			TransRef:    transRef,
			Size:        len(img),
			ContentType: contentType,
			Served:      dumpVariant{PSM: served.PSM, Preprocess: served.Preprocess, Result: servedRes},
			Variants:    variants,
			DumpedAt:    time.Now(),
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		if err := os.WriteFile(filepath.Join(folder, "result.json"), b, 0o644); err != nil {
			h.log.Error("imports: OCR dump write", "err", err)
			return
		}
		h.log.Info(fmt.Sprintf("imports: OCR dump written (%d modes)", len(variants)), "folder", folder)
	}()
}
