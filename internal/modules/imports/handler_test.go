package imports

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/imports/ocr"
)

// newRouter wires the stub routes behind a fake auth step.
func newRouter(logs *bytes.Buffer) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewHandler(slog.New(slog.NewTextHandler(logs, nil)), &fakeOCR{})
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user_id", uuid.New()) })
	r.POST("/slip-imports/check", h.CheckSlips)
	r.POST("/pending-transactions/scan-slip", h.ScanSlip)
	r.POST("/pending-transactions/parse-text", h.ParseText)
	return r
}

func do(r *gin.Engine, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestCheckSlipsEchoesFiles(t *testing.T) {
	var logs bytes.Buffer
	r := newRouter(&logs)
	req := httptest.NewRequest(http.MethodPost, "/slip-imports/check",
		strings.NewReader(`{"files":["slip_1.jpg|1200|1696","slip_2.jpg|900|1697"]}`))
	req.Header.Set("Content-Type", "application/json")
	w := do(r, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"new_files":["slip_1.jpg|1200|1696","slip_2.jpg|900|1697"]`) {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	if !strings.Contains(logs.String(), "slip check OK") {
		t.Fatalf("no log: %s", logs.String())
	}
}

func TestCheckSlipsNeedsFiles(t *testing.T) {
	r := newRouter(&bytes.Buffer{})
	req := httptest.NewRequest(http.MethodPost, "/slip-imports/check", strings.NewReader(`{"files":[]}`))
	req.Header.Set("Content-Type", "application/json")
	if w := do(r, req); w.Code != http.StatusBadRequest {
		t.Fatalf("got %d", w.Code)
	}
}

func TestScanSlipLogsPayload(t *testing.T) {
	var logs bytes.Buffer
	r := newRouter(&logs)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("file_key", "slip_1.jpg|1200|1696")
	_ = mw.WriteField("trans_ref", "016282103123ABC")
	fw, _ := mw.CreateFormFile("image", "slip_1.jpg")
	_, _ = fw.Write(slipPNG())
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/pending-transactions/scan-slip", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := do(r, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"trans_ref":"016282103123ABC"`) {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	if !strings.Contains(logs.String(), "scan slip OK") || !strings.Contains(logs.String(), "slip_1.jpg") {
		t.Fatalf("log missing payload: %s", logs.String())
	}
}

func TestScanSlipNeedsImage(t *testing.T) {
	r := newRouter(&bytes.Buffer{})
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("file_key", "x")
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/pending-transactions/scan-slip", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if w := do(r, req); w.Code != http.StatusBadRequest {
		t.Fatalf("got %d", w.Code)
	}
}

func TestParseTextEchoes(t *testing.T) {
	var logs bytes.Buffer
	r := newRouter(&logs)
	req := httptest.NewRequest(http.MethodPost, "/pending-transactions/parse-text",
		strings.NewReader(`{"text":"กาแฟ 65"}`))
	req.Header.Set("Content-Type", "application/json")
	w := do(r, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "กาแฟ 65") {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	if !strings.Contains(logs.String(), "parse text OK") {
		t.Fatalf("no log: %s", logs.String())
	}
}

// check → scan one → check again: the scanned file is now "seen"; reset
// forgets it.
func TestSlipFlowRemembersScanned(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandler(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), &fakeOCR{})
	uid := uuid.New()
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user_id", uid) })
	r.POST("/check", h.CheckSlips)
	r.POST("/scan", h.ScanSlip)
	r.DELETE("/reset", h.ResetSlips)

	check := func() string {
		req := httptest.NewRequest(http.MethodPost, "/check",
			strings.NewReader(`{"files":["a","b"]}`))
		req.Header.Set("Content-Type", "application/json")
		return do(r, req).Body.String()
	}
	if got := check(); !strings.Contains(got, `"new_files":["a","b"],"seen_files":[]`) {
		t.Fatalf("first check: %s", got)
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("file_key", "a")
	fw, _ := mw.CreateFormFile("image", "a.jpg")
	_, _ = fw.Write(slipPNG())
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/scan", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if w := do(r, req); w.Code != http.StatusOK {
		t.Fatalf("scan: %d", w.Code)
	}

	if got := check(); !strings.Contains(got, `"new_files":["b"],"seen_files":["a"]`) {
		t.Fatalf("second check: %s", got)
	}
	do(r, httptest.NewRequest(http.MethodDelete, "/reset", nil))
	if got := check(); !strings.Contains(got, `"new_files":["a","b"]`) {
		t.Fatalf("after reset: %s", got)
	}
}

// fakeOCR answers res (or a one-line text), or err when set; it keeps the
// options it was called with.
type fakeOCR struct {
	err error
	res *ocr.Result
	got *ocr.Options
}

func (f *fakeOCR) Read(_ context.Context, _ []byte, opt ocr.Options) (*ocr.Result, error) {
	f.got = &opt
	if f.err != nil {
		return nil, f.err
	}
	if f.res != nil {
		return f.res, nil
	}
	return &ocr.Result{Text: "โอนเงินสำเร็จ 500.00", PSM: opt.PSM, Preprocess: opt.Preprocess}, nil
}

func slipPNG() []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewGray(image.Rect(0, 0, 4, 4)))
	return buf.Bytes()
}

// scanReq builds a scan-slip upload; extra holds the other form fields.
func scanReq(img []byte, extra map[string]string) *http.Request {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range extra {
		_ = mw.WriteField(k, v)
	}
	fw, _ := mw.CreateFormFile("image", "slip.png")
	_, _ = fw.Write(img)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/scan", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func scanRouter(reader ocr.Reader) (*gin.Engine, *Handler) {
	gin.SetMode(gin.TestMode)
	h := NewHandler(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), reader)
	uid := uuid.New()
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user_id", uid) })
	r.POST("/scan", h.ScanSlip)
	return r, h
}

func TestScanSlipReturnsOCR(t *testing.T) {
	fake := &fakeOCR{}
	r, _ := scanRouter(fake)
	w := do(r, scanReq(slipPNG(), map[string]string{"psm": "4", "preprocess": "1", "debug": "1"}))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "โอนเงินสำเร็จ 500.00") {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	if fake.got == nil || fake.got.PSM != 4 || !fake.got.Preprocess {
		t.Fatalf("options = %+v", fake.got)
	}
}

func TestScanSlipDefaults(t *testing.T) {
	fake := &fakeOCR{}
	r, _ := scanRouter(fake)
	if w := do(r, scanReq(slipPNG(), nil)); w.Code != http.StatusOK {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	if fake.got.PSM != ocr.DefaultPSM || fake.got.Preprocess {
		t.Fatalf("options = %+v", fake.got)
	}
}

func TestScanSlipRejectsBadInput(t *testing.T) {
	r, _ := scanRouter(&fakeOCR{})
	w := do(r, scanReq([]byte("not an image"), nil))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "UNSUPPORTED_IMAGE") {
		t.Fatalf("non-image: %d %s", w.Code, w.Body)
	}
	if w := do(r, scanReq(slipPNG(), map[string]string{"psm": "7"})); w.Code != http.StatusBadRequest {
		t.Fatalf("psm 7: %d", w.Code)
	}
}

// A failed read maps to its own status and doesn't count the file as seen.
func TestScanSlipOCRErrors(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{ocr.ErrUnavailable, http.StatusServiceUnavailable, "OCR_UNAVAILABLE"},
		{ocr.ErrTimeout, http.StatusGatewayTimeout, "OCR_TIMEOUT"},
		{ocr.ErrBadImage, http.StatusBadRequest, "UNSUPPORTED_IMAGE"},
	}
	for _, tc := range cases {
		r, h := scanRouter(&fakeOCR{err: tc.err})
		w := do(r, scanReq(slipPNG(), map[string]string{"file_key": "a"}))
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) {
			t.Errorf("%v: got %d %s", tc.err, w.Code, w.Body)
		}
		for _, keys := range h.seen {
			if keys["a"] {
				t.Errorf("%v: file marked seen", tc.err)
			}
		}
	}
}

// A K PLUS slip read by OCR → ScanResult with a status and its drafts.
func TestScanSlipBuildsDrafts(t *testing.T) {
	res := &ocr.Result{Width: 1328, Lines: []ocr.Line{
		{Text: "โอนเงินสำเร็จ", Box: ocr.Box{X: 224, Y: 66}},
		{Text: "9 ต.ค. 69 13:19 น.", Box: ocr.Box{X: 226, Y: 176, H: 40}},
		{Text: "นาย สมชาย ใ", Box: ocr.Box{X: 225, Y: 349}},
		{Text: "ธ.กสิกรไทย", Box: ocr.Box{X: 227, Y: 443}},
		{Text: "XXX-X-X1234-x", Box: ocr.Box{X: 226, Y: 539}},
		{Text: "ร้านกาแฟ", Box: ocr.Box{X: 225, Y: 661}},
		{Text: "ธ.กสิกรไทย", Box: ocr.Box{X: 226, Y: 757}},
		{Text: "XXX-X-X5678-x", Box: ocr.Box{X: 226, Y: 852}},
		{Text: "จำนวน:", Box: ocr.Box{X: 72, Y: 1017}},
		{Text: "97.00 บาท", Box: ocr.Box{X: 653, Y: 1118}},
		{Text: "ค่าธรรมเนียม:", Box: ocr.Box{X: 74, Y: 1223}},
		{Text: "5.00 บาท", Box: ocr.Box{X: 751, Y: 1311}},
	}}
	r, _ := scanRouter(&fakeOCR{res: res})
	w := do(r, scanReq(slipPNG(), map[string]string{
		"file_key": "a", "trans_ref": "REF1", "bank_code": "004",
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, want := range []string{
		`"v":1`, `"status":"ok"`, `"bank_code":"004"`, `"amount":97`,
		`"date":"2026-10-09"`, `"description":"ร้านกาแฟ"`, `"description":"ค่าธรรมเนียม · ร้านกาแฟ"`, `"part":"fee"`, `"amount":5`,
		`"sender_masked":"xxxxx1234x"`, `"occurred_at":"2026-10-09T13:19:00+07:00"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	if strings.Contains(body, `"ocr":{`) {
		t.Errorf("ocr sent without debug=1")
	}
}

func TestScanSlipUnsupportedBank(t *testing.T) {
	r, _ := scanRouter(&fakeOCR{})
	w := do(r, scanReq(slipPNG(), map[string]string{"bank_code": "014"}))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"unsupported_bank"`) || !strings.Contains(w.Body.String(), `"pending":[]`) {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
}

// Import logs: an unknown bank code, a bank without rules, an incomplete
// read — each written once; a clean slip writes nothing.
func TestScanSlipWritesImportLogs(t *testing.T) {
	okSlip := &ocr.Result{Width: 1000, Lines: []ocr.Line{
		{Text: "โอนเงินสำเร็จ", Box: ocr.Box{Y: 10}},
		{Text: "9 ต.ค. 69 13:19 น.", Box: ocr.Box{Y: 20, H: 5}},
		{Text: "จำนวน:", Box: ocr.Box{Y: 40}},
		{Text: "97.00 บาท", Box: ocr.Box{Y: 50}},
	}}
	noAmount := &ocr.Result{Width: 1000, Lines: []ocr.Line{
		{Text: "9 ต.ค. 69 13:19 น.", Box: ocr.Box{Y: 20, H: 5}},
	}}
	cases := []struct {
		name, bank string
		res        *ocr.Result
		want       string // "" = nothing written
	}{
		{"unknown code", "999", okSlip, LogUnknownProvider},
		{"known bank, no rules", "014", okSlip, LogUnsupportedBank},
		{"incomplete", "004", noAmount, LogIncomplete},
		{"clean", "004", okSlip, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, h := scanRouter(&fakeOCR{res: tc.res})
			var got []ImportLog
			h.WithImportLogs(
				func(_ context.Context, l ImportLog) error { got = append(got, l); return nil },
				func(_ context.Context, _, code string) (bool, error) { return code != "999", nil },
			)
			w := do(r, scanReq(slipPNG(), map[string]string{"bank_code": tc.bank, "trans_ref": "R1"}))
			if w.Code != http.StatusOK {
				t.Fatalf("got %d %s", w.Code, w.Body)
			}
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("wrote %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Kind != tc.want || got[0].Code != tc.bank ||
				got[0].Scheme != "bot" || got[0].TransRef != "R1" {
				t.Fatalf("wrote %+v", got)
			}
		})
	}
}
