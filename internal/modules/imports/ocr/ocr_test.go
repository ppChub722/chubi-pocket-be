package ocr

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"testing"
)

const header = "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n"

func TestParseTSVGroupsLinesAndScalesBoxes(t *testing.T) {
	tsv := header +
		"1\t1\t0\t0\t0\t0\t0\t0\t2000\t3000\t-1\t\n" +
		"4\t1\t1\t1\t1\t0\t100\t200\t600\t40\t-1\t\n" +
		// Thai "words" touching each other → no space; a real gap → space.
		"5\t1\t1\t1\t1\t1\t100\t200\t100\t40\t90\tโอน\n" +
		"5\t1\t1\t1\t1\t2\t202\t200\t100\t40\t80\tเงิน\n" +
		"5\t1\t1\t1\t1\t3\t500\t200\t200\t40\t70\t500.00\n" +
		"5\t1\t1\t1\t2\t1\t100\t300\t100\t40\t-1\t \n" + // empty word
		"5\t1\t2\t1\t1\t1\t100\t400\t300\t40\t60\tKBank\r\n"

	lines, conf := parseTSV(tsv, 2)
	if len(lines) != 2 {
		t.Fatalf("lines = %+v", lines)
	}
	if lines[0].Text != "โอนเงิน 500.00" {
		t.Errorf("line 0 text = %q", lines[0].Text)
	}
	if lines[0].Conf != 80 {
		t.Errorf("line 0 conf = %v", lines[0].Conf)
	}
	if got := lines[0].Box; got != (Box{X: 50, Y: 100, W: 300, H: 20}) {
		t.Errorf("line 0 box = %+v", got)
	}
	if lines[1].Text != "KBank" {
		t.Errorf("line 1 text = %q", lines[1].Text)
	}
	if conf != 75 {
		t.Errorf("conf = %v", conf)
	}
}

func TestParseTSVEmpty(t *testing.T) {
	lines, conf := parseTSV(header, 1)
	if len(lines) != 0 || conf != 0 {
		t.Fatalf("got %v %v", lines, conf)
	}
}

func pngOf(w, h int) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)))
	return buf.Bytes()
}

func TestPrepareUpscalesNarrowImages(t *testing.T) {
	p, err := prepare(pngOf(800, 1200), true)
	if err != nil {
		t.Fatal(err)
	}
	if p.width != 800 || p.height != 1200 || p.scale != 2 {
		t.Fatalf("got %dx%d scale %v", p.width, p.height, p.scale)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(p.data))
	if err != nil || cfg.Width != 1600 || cfg.Height != 2400 {
		t.Fatalf("out %+v %v", cfg, err)
	}
}

func TestPrepareCapsScaleAndKeepsWideImages(t *testing.T) {
	if p, _ := prepare(pngOf(100, 100), true); p.scale != maxScale {
		t.Errorf("tiny scale = %v", p.scale)
	}
	if p, _ := prepare(pngOf(2000, 100), true); p.scale != 1 {
		t.Errorf("wide scale = %v", p.scale)
	}
}

func TestPrepareOffPassesBytesThrough(t *testing.T) {
	img := pngOf(800, 1200)
	p, err := prepare(img, false)
	if err != nil || !bytes.Equal(p.data, img) || p.scale != 1 || p.width != 800 {
		t.Fatalf("got %+v %v", p, err)
	}
}

func TestPrepareRejectsNonImages(t *testing.T) {
	for _, on := range []bool{true, false} {
		if _, err := prepare([]byte("not an image"), on); !errors.Is(err, ErrBadImage) {
			t.Errorf("preprocess=%v err = %v", on, err)
		}
	}
}

func TestNormalizeThaiSaraAm(t *testing.T) {
	if got := normalizeThai("จํานวน โอนเงินสําเร็จ"); got != "จำนวน โอนเงินสำเร็จ" {
		t.Fatalf("got %q", got)
	}
}

func TestUseTxtSpacing(t *testing.T) {
	lines := []Line{{Text: "9 ต .ค . 69 13:19 น ."}, {Text: "รหัสพร้อม เพย์"}, {Text: "only in tsv"}}
	useTxtSpacing(lines, "9 ต.ค. 69 13:19 น.\n\nรหัสพร้อมเพย์\n")
	want := []string{"9 ต.ค. 69 13:19 น.", "รหัสพร้อมเพย์", "only in tsv"}
	for i, w := range want {
		if lines[i].Text != w {
			t.Errorf("line %d = %q, want %q", i, lines[i].Text, w)
		}
	}
}
