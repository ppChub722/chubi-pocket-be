package imports

// Spec conformance (API spec v2) of the import operations: every body is
// checked against openapi.yaml with apitest.Conform. Plain gin engines (the
// scan is multipart, outside apitest.Do).

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/accounts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/imports/ocr"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/providers"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// kbankSlip — a K PLUS transfer: sender xxxxx1234x, receiver xxxxx5678x, fee 5.
func kbankSlip() *ocr.Result {
	return &ocr.Result{Width: 1328, Lines: []ocr.Line{
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
}

func apiRouter(uid uuid.UUID, h *Handler) *gin.Engine {
	return apitest.Engine(uid, func(g *gin.RouterGroup) {
		g.POST("/pending-transactions/scan-slip", h.ScanSlip)
		g.POST("/pending-transactions/parse-text", h.ParseText)
		g.POST("/slip-imports/check", h.CheckSlips)
		g.DELETE("/slip-imports", h.ResetSlips)
	})
}

func scanAt(r *gin.Engine, extra map[string]string) (int, []byte) {
	req := scanReq(slipPNG(), extra)
	req.URL.Path = "/api/v1/pending-transactions/scan-slip"
	req.RequestURI = req.URL.Path
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

func TestScanSlipConformsAndResolves(t *testing.T) {
	pool := testdb.Pool(t)
	uid := testdb.User(t, pool)
	wallet := testdb.Account(t, pool, uid)

	h := NewHandler(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), &fakeOCR{res: kbankSlip()})
	h.WithResolver(pending.NewResolver(pool).Resolve)
	h.WithDraftContext(func(_ context.Context, _ uuid.UUID) (DraftContext, error) {
		return DraftContext{Wallets: []Wallet{{ID: wallet, Identifiers: []accounts.Identifier{
			{Kind: accounts.IdentifierBankAccount, Value: "1234", BankCode: "004"},
		}}}}, nil
	})
	r := apiRouter(uid, h)

	status, body := scanAt(r, map[string]string{"file_key": "a|1|2", "trans_ref": "REF1", "bank_code": "004"})
	apitest.Conform(t, "scanSlip", status, body)
	var out struct {
		Status  string `json:"status"`
		FileKey string `json:"file_key"`
		Slip    any    `json:"slip"`
		Pending []struct {
			Resolved pending.DraftResolved `json:"resolved"`
			Draft    pending.Draft         `json:"draft"`
		} `json:"pending"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK || out.Status != "ok" || out.FileKey != "a|1|2" || out.Slip == nil || len(out.Pending) != 2 {
		t.Fatalf("scan: %d %s", status, body)
	}
	if strings.Contains(string(body), `"success"`) || strings.Contains(string(body), `"ocr":{`) {
		t.Fatalf("envelope / debug leaked: %s", body)
	}
	first := out.Pending[0]
	if first.Resolved.Account == nil || first.Resolved.Account.ID != wallet {
		t.Fatalf("resolved account: %+v", first.Resolved)
	}
	if len(first.Resolved.Missing) != 0 || first.Resolved.Tags == nil || first.Resolved.Splits == nil {
		t.Fatalf("resolved: %+v", first.Resolved)
	}

	// Unsupported bank: no drafts, strings still present.
	status, body = scanAt(r, map[string]string{"bank_code": "014"})
	apitest.Conform(t, "scanSlip", status, body)
	if !strings.Contains(string(body), `"file_key":""`) || !strings.Contains(string(body), `"slip":null`) {
		t.Fatalf("unknown values: %s", body)
	}

	// No image: 400.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pending-transactions/scan-slip", strings.NewReader(""))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	apitest.Conform(t, "scanSlip", w.Code, w.Body.Bytes())
	if w.Code != http.StatusBadRequest {
		t.Fatalf("no image: %d", w.Code)
	}
}

func TestParseTextConforms(t *testing.T) {
	var logs bytes.Buffer
	h := NewHandler(slog.New(slog.NewTextHandler(&logs, nil)), &fakeOCR{})
	r := apiRouter(uuid.New(), h)
	status, body := apitest.Do(t, r, "POST", "/api/v1/pending-transactions/parse-text", map[string]any{"text": "coffee 65"})
	apitest.Conform(t, "parseText", status, body)
	if string(body) != `{"pending":[]}` || !strings.Contains(logs.String(), "parse text OK") {
		t.Fatalf("parse-text: %s / log %s", body, logs.String())
	}
	status, body = apitest.Do(t, r, "POST", "/api/v1/pending-transactions/parse-text", map[string]any{"text": ""})
	apitest.Conform(t, "parseText", status, body)
	if status != http.StatusBadRequest {
		t.Fatalf("empty text: %d", status)
	}
}

func TestSlipHistoryConforms(t *testing.T) {
	h := NewHandler(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), &fakeOCR{res: kbankSlip()})
	r := apiRouter(uuid.New(), h)

	status, body := apitest.Do(t, r, "POST", "/api/v1/slip-imports/check", map[string]any{"files": []string{"a", "b"}})
	apitest.Conform(t, "checkSlips", status, body)
	if string(body) != `{"new_files":["a","b"],"seen_files":[]}` {
		t.Fatalf("check: %s", body)
	}
	if status, _ = scanAt(r, map[string]string{"file_key": "a", "bank_code": "004"}); status != http.StatusOK {
		t.Fatalf("scan: %d", status)
	}
	status, body = apitest.Do(t, r, "POST", "/api/v1/slip-imports/check", map[string]any{"files": []string{"a", "b"}})
	apitest.Conform(t, "checkSlips", status, body)
	if string(body) != `{"new_files":["b"],"seen_files":["a"]}` {
		t.Fatalf("second check: %s", body)
	}
	status, body = apitest.Do(t, r, "POST", "/api/v1/slip-imports/check", map[string]any{"files": []string{}})
	apitest.Conform(t, "checkSlips", status, body)
	if status != http.StatusBadRequest {
		t.Fatalf("empty files: %d", status)
	}

	status, body = apitest.Do(t, r, "DELETE", "/api/v1/slip-imports", nil)
	apitest.Conform(t, "resetSlipHistory", status, body)
	if string(body) != `{"forgot":1}` {
		t.Fatalf("reset: %s", body)
	}
}

// The providers handler is not in this package; this only checks its answer
// against the spec.
func TestListPaymentProvidersConforms(t *testing.T) {
	pool := testdb.Pool(t)
	h := providers.NewHandler(providers.NewStore(pool))
	r := apitest.Engine(uuid.New(), func(g *gin.RouterGroup) { g.GET("/payment-providers", h.List) })
	status, body := apitest.Do(t, r, "GET", "/api/v1/payment-providers", nil)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	apitest.Conform(t, "listPaymentProviders", status, body)
}
