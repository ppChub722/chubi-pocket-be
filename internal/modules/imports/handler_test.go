package imports

import (
	"bytes"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// newRouter wires the stub routes behind a fake auth step.
func newRouter(logs *bytes.Buffer) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewHandler(slog.New(slog.NewTextHandler(logs, nil)))
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
	_, _ = fw.Write([]byte("fake image bytes"))
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
	h := NewHandler(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
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
	_, _ = fw.Write([]byte("x"))
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
