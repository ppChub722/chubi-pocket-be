package ocr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config for the Tesseract reader.
type Config struct {
	Bin     string        // binary name or path ("tesseract")
	Lang    string        // "tha+eng"
	Timeout time.Duration // per read, waiting for a slot included
	// Slots — reads allowed at once. Each is CPU-bound on one core
	// (OMP_THREAD_LIMIT=1) and the VPS has two, shared with other apps.
	Slots int
}

// Tesseract runs the tesseract CLI: the image goes in on stdin, the text
// (txt) and word boxes (tsv) come back as files in a temp dir.
type Tesseract struct {
	cfg   Config
	slots chan struct{}
}

func NewTesseract(cfg Config) *Tesseract {
	if cfg.Bin == "" {
		cfg.Bin = "tesseract"
	}
	if cfg.Lang == "" {
		cfg.Lang = "tha+eng"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.Slots <= 0 {
		cfg.Slots = 1
	}
	return &Tesseract{cfg: cfg, slots: make(chan struct{}, cfg.Slots)}
}

// Check reports whether the binary can be found — called at startup so a
// missing install shows up in the log, not on the first slip.
func (t *Tesseract) Check() error {
	if _, err := exec.LookPath(t.cfg.Bin); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

func (t *Tesseract) Read(ctx context.Context, img []byte, opt Options) (*Result, error) {
	if err := t.Check(); err != nil {
		return nil, err
	}
	psm := opt.PSM
	if psm == 0 {
		psm = DefaultPSM
	}

	ctx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
	defer cancel()

	start := time.Now()
	p, err := prepare(img, opt.Preprocess)
	if err != nil {
		return nil, err
	}
	prepMs := time.Since(start).Milliseconds()

	select {
	case t.slots <- struct{}{}:
		defer func() { <-t.slots }()
	case <-ctx.Done():
		return nil, ErrTimeout
	}

	dir, err := os.MkdirTemp("", "ocr-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	base := filepath.Join(dir, "out")

	start = time.Now()
	cmd := exec.CommandContext(ctx, t.cfg.Bin, "stdin", base,
		"-l", t.cfg.Lang,
		"--oem", "1", // LSTM — tessdata_best has nothing else
		"--psm", strconv.Itoa(psm),
		"-c", "preserve_interword_spaces=1", // Thai has no spaces between words
		"-c", "tessedit_create_txt=1",
		"-c", "tessedit_create_tsv=1",
	)
	cmd.Stdin = bytes.NewReader(p.data)
	cmd.Env = append(os.Environ(), "OMP_THREAD_LIMIT=1")
	// Alpine's tesseract is built with OpenCL: it profiles the device and
	// saves the result in the working dir. /app isn't writable, so it would
	// re-profile on every slip — /tmp keeps it after the first.
	cmd.Dir = os.TempDir()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("tesseract: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	ocrMs := time.Since(start).Milliseconds()

	txt, err := os.ReadFile(base + ".txt")
	if err != nil {
		return nil, fmt.Errorf("tesseract txt: %w", err)
	}
	tsv, err := os.ReadFile(base + ".tsv")
	if err != nil {
		return nil, fmt.Errorf("tesseract tsv: %w", err)
	}
	lines, conf := parseTSV(string(tsv), p.scale)
	useTxtSpacing(lines, string(txt))

	return &Result{
		Text:       normalizeThai(strings.TrimSpace(string(txt))),
		Lines:      lines,
		Conf:       conf,
		Width:      p.width,
		Height:     p.height,
		Lang:       t.cfg.Lang,
		PSM:        psm,
		Preprocess: opt.Preprocess,
		Scale:      math.Round(p.scale*100) / 100,
		PrepMs:     prepMs,
		OCRMs:      ocrMs,
	}, nil
}

// parseTSV groups Tesseract's word rows (level 5) into lines. Boxes are
// divided by scale to land back on the uploaded image. Words are joined
// with a space only where there's a real gap between them — Tesseract
// splits Thai into "words" that sit right next to each other.
//
// Columns: level page_num block_num par_num line_num word_num
// left top width height conf text
func parseTSV(tsv string, scale float64) ([]Line, float64) {
	if scale <= 0 {
		scale = 1
	}
	type word struct {
		x, y, w, h int
		conf       float64
		text       string
	}
	type group struct {
		key   string
		words []word
	}
	var groups []*group
	byKey := map[string]*group{}
	var confSum float64
	var confN int

	for i, row := range strings.Split(tsv, "\n") {
		if i == 0 {
			continue // header
		}
		f := strings.Split(strings.TrimRight(row, "\r"), "\t")
		if len(f) < 12 || f[0] != "5" {
			continue
		}
		text := strings.TrimSpace(f[11])
		if text == "" {
			continue
		}
		num := func(s string) int { n, _ := strconv.Atoi(s); return n }
		conf, _ := strconv.ParseFloat(f[10], 64)
		w := word{x: num(f[6]), y: num(f[7]), w: num(f[8]), h: num(f[9]), conf: conf, text: text}
		if conf >= 0 {
			confSum += conf
			confN++
		}
		key := f[1] + "/" + f[2] + "/" + f[3] + "/" + f[4]
		g := byKey[key]
		if g == nil {
			g = &group{key: key}
			byKey[key] = g
			groups = append(groups, g)
		}
		g.words = append(g.words, w)
	}

	lines := make([]Line, 0, len(groups))
	for _, g := range groups {
		var sb strings.Builder
		x0, y0 := math.MaxInt, math.MaxInt
		x1, y1 := 0, 0
		var sum float64
		var n int
		prevRight := -1
		for _, w := range g.words {
			if prevRight >= 0 {
				// Gap wider than ~a third of the word height → a space.
				if w.x-prevRight > w.h/3 {
					sb.WriteByte(' ')
				}
			}
			sb.WriteString(w.text)
			prevRight = w.x + w.w
			x0, y0 = min(x0, w.x), min(y0, w.y)
			x1, y1 = max(x1, w.x+w.w), max(y1, w.y+w.h)
			if w.conf >= 0 {
				sum += w.conf
				n++
			}
		}
		l := Line{
			Text: normalizeThai(sb.String()),
			Box: Box{
				X: int(math.Round(float64(x0) / scale)),
				Y: int(math.Round(float64(y0) / scale)),
				W: int(math.Round(float64(x1-x0) / scale)),
				H: int(math.Round(float64(y1-y0) / scale)),
			},
		}
		if n > 0 {
			l.Conf = math.Round(sum/float64(n)*10) / 10
		}
		lines = append(lines, l)
	}

	var conf float64
	if confN > 0 {
		conf = math.Round(confSum/float64(confN)*10) / 10
	}
	return lines, conf
}

// thaiFix — Tesseract writes sara am (ำ) as nikhahit + sara aa (ํา): it
// looks the same but "จํานวน" ≠ "จำนวน" for anything matching on it.
var thaiFix = strings.NewReplacer("\u0e4d\u0e32", "\u0e33")

func normalizeThai(s string) string { return thaiFix.Replace(s) }

// useTxtSpacing swaps each line's text for the same line from the txt
// output when they match ignoring spaces. TSV words carry no spacing, so
// a line rebuilt from them gets stray spaces inside Thai words ("ต .ค .",
// "รหัสพร้อม เพย์"); the txt renderer, with preserve_interword_spaces,
// spaces them like the slip does.
func useTxtSpacing(lines []Line, txt string) {
	bySkeleton := map[string]string{}
	for _, t := range strings.Split(normalizeThai(txt), "\n") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		// Runs of spaces in txt are layout (columns), not word gaps.
		t = strings.Join(strings.Fields(t), " ")
		bySkeleton[skeleton(t)] = t
	}
	for i := range lines {
		if t, ok := bySkeleton[skeleton(lines[i].Text)]; ok {
			lines[i].Text = t
		}
	}
}

// skeleton — no spaces, sara am rejoined: the key two readings of one
// line share however OCR spaced them ("จํ า" vs "จำ").
func skeleton(s string) string { return normalizeThai(strings.Join(strings.Fields(s), "")) }
