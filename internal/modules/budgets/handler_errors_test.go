package budgets

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
)

// A body that fails to decode used to send the Go decoder's text ("json: cannot unmarshal string
// into Go struct field Request.amount of type float64") as the message. Now: a safe message and
// the field in details. No DB: the bind fails before the service is called.
func TestCreateBadBodyHasNoRawDecoderText(t *testing.T) {
	h := NewHandler(nil)
	r := apitest.Engine(uuid.New(), func(g *gin.RouterGroup) { g.POST("/budgets", h.Create) })

	cases := []struct {
		body      string
		field     string
		fieldCode string
	}{
		{`{"name":"Food","amount":"lots"}`, "amount", apierr.FieldInvalidType},
		{`{"name":`, "", apierr.FieldInvalidJSON},
	}
	for _, tc := range cases {
		status, body := apitest.Do(t, r, http.MethodPost, "/api/v1/budgets", tc.body)
		if status != http.StatusBadRequest {
			t.Fatalf("%s: status %d", tc.body, status)
		}
		apitest.Conform(t, "createBudget", status, body)
		var env struct {
			Error struct {
				Code    string              `json:"code"`
				Message string              `json:"message"`
				Details []apierr.FieldError `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatal(err)
		}
		e := env.Error
		if e.Code != string(apierr.ValidationError) {
			t.Errorf("%s: code %q", tc.body, e.Code)
		}
		if strings.Contains(e.Message, "json") || strings.Contains(e.Message, "Go struct") || strings.Contains(string(body), "float64") {
			t.Errorf("%s: raw decoder text leaked: %s", tc.body, body)
		}
		if len(e.Details) != 1 || e.Details[0].Field != tc.field || e.Details[0].Code != tc.fieldCode {
			t.Errorf("%s: details %+v, want [{%q %q}]", tc.body, e.Details, tc.field, tc.fieldCode)
		}
	}
}
