package apidoc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

func schemaAt(t *testing.T, doc map[string]any, path ...string) map[string]any {
	t.Helper()
	var cur any = doc
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("spec path %v: %q is not an object", path, p)
		}
		cur = m[p]
	}
	m, ok := cur.(map[string]any)
	if !ok {
		t.Fatalf("spec path %v is missing", path)
	}
	return m
}

// The OpenAPI code enums are the apierr registry, nothing more, nothing less.
func TestErrorCodeEnumsMatchRegistry(t *testing.T) {
	doc := mustDoc(t)
	for _, tc := range []struct {
		name string
		use  apierr.Use
		path []string
	}{
		{"Error.error.code", apierr.InEnvelope, []string{"components", "schemas", "Error", "properties", "error", "properties", "code"}},
		{"SubmitError.code", apierr.InPendingLastError, []string{"components", "schemas", "SubmitError", "properties", "code"}},
		{"SyncResponse.errors.*.code", apierr.InSyncDatasetError, []string{"components", "schemas", "SyncResponse", "properties", "errors", "additionalProperties", "properties", "code"}},
	} {
		s := schemaAt(t, doc, tc.path...)
		inSpec := map[string]bool{}
		raw, _ := s["enum"].([]any)
		for _, v := range raw {
			inSpec[fmt.Sprint(v)] = true
		}
		var want []string
		inReg := map[string]bool{}
		for _, c := range apierr.Codes(tc.use) {
			want = append(want, string(c))
			inReg[string(c)] = true
		}
		var missing, extra []string
		for _, c := range want {
			if !inSpec[c] {
				missing = append(missing, c)
			}
		}
		for c := range inSpec {
			if !inReg[c] {
				extra = append(extra, c)
			}
		}
		sort.Strings(extra)
		if len(missing) > 0 || len(extra) > 0 {
			t.Errorf("%s enum differs from the apierr registry: missing %v, not registered %v\nwant: enum: [%s]",
				tc.name, missing, extra, strings.Join(want, ", "))
		}
	}
}

// A real VALIDATION_ERROR (response.BindError / Validation) matches the documented FieldError.
func TestValidationDetailsMatchFieldErrorSchema(t *testing.T) {
	doc := mustDoc(t)
	fieldErr := schemaAt(t, doc, "components", "schemas", "FieldError")
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/bind", func(c *gin.Context) {
		var req struct {
			Name   string  `json:"name" binding:"required,max=3"`
			Amount float64 `json:"amount" binding:"gt=0"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			response.BindError(c, err)
		}
	})
	r.POST("/plain", func(c *gin.Context) {
		response.Validation(c, "Invalid status", apierr.Field("status", apierr.FieldInvalid))
	})
	for path, body := range map[string]string{"/bind": `{"name":"long","amount":-1}`, "/plain": `{}`} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		var env struct {
			Error struct {
				Details []any `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || len(env.Error.Details) == 0 {
			t.Fatalf("%s: %s", path, w.Body.String())
		}
		if err := check("Error", schemaAt(t, doc, "components", "schemas", "Error"), w.Body.Bytes()); err != nil {
			t.Error(err)
		}
		for _, d := range env.Error.Details {
			raw, _ := json.Marshal(d)
			if err := check("FieldError", fieldErr, raw); err != nil {
				t.Error(err)
			}
		}
	}
}
