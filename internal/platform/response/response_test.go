package response

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
)

type envelope struct {
	Success bool `json:"success"`
	Error   struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Details json.RawMessage `json:"details"`
	} `json:"error"`
}

func run(t *testing.T, h gin.HandlerFunc, body string) (int, envelope, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/x", h)
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var env envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("not JSON: %s", w.Body.String())
	}
	return w.Code, env, w.Body.String()
}

func TestInternalErrorNeverSendsTheCause(t *testing.T) {
	var logged []string
	status, env, raw := run(t, func(c *gin.Context) {
		InternalError(c, "Budget operation failed", errors.New(`ERROR: relation "budgets" does not exist (SQLSTATE 42P01)`))
		logged = append(logged, c.Errors.String())
	}, "")
	if status != 500 || env.Error.Code != string(apierr.InternalServerError) || env.Error.Message != "Budget operation failed" {
		t.Fatalf("got %d %+v", status, env)
	}
	if strings.Contains(raw, "SQLSTATE") || len(env.Error.Details) != 0 {
		t.Errorf("the cause leaked: %s", raw)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "SQLSTATE") {
		t.Errorf("the cause must reach the request log (c.Errors): %v", logged)
	}
}

func TestFailReplacesLeakyMessages(t *testing.T) {
	for _, msg := range []string{
		"json: cannot unmarshal string into Go struct field Request.amount of type float64",
		"Key: 'Req.Name' Error:Field validation for 'Name' failed on the 'required' tag",
		"unexpected EOF",
		`ERROR: duplicate key value violates unique constraint "x" (SQLSTATE 23505)`,
		"invalid UUID length: 3",
		"no rows in result set",
	} {
		_, env, _ := run(t, func(c *gin.Context) { Fail(c, http.StatusConflict, apierr.TagExists, msg, nil) }, "")
		if env.Error.Message == msg || env.Error.Code != string(apierr.TagExists) {
			t.Errorf("%q was sent as is: %+v", msg, env.Error)
		}
	}
	for _, msg := range []string{"wallet is archived: restore it first", "Budget not found", "private_note: at most 500 characters"} {
		if Leaky(msg) {
			t.Errorf("%q is one of our own messages, not a leak", msg)
		}
	}
}

type bindReq struct {
	Name   string    `json:"name" binding:"required,max=5"`
	Amount float64   `json:"amount" binding:"gt=0"`
	Kind   string    `json:"kind" binding:"omitempty,oneof=a b"`
	Splits []bindRow `json:"splits" binding:"omitempty,dive"`
	Tags   []string  `json:"tags" binding:"omitempty,max=2"`
}

type bindRow struct {
	PersonName string `json:"person_name" binding:"required"`
}

func TestBindErrorListsFieldsByJSONPath(t *testing.T) {
	h := func(c *gin.Context) {
		var r bindReq
		if err := c.ShouldBindJSON(&r); err != nil {
			BindError(c, err)
		}
	}
	_, env, raw := run(t, h, `{"name":"toolong","amount":0,"kind":"z","splits":[{"person_name":""}],"tags":["a","b","c"]}`)
	if env.Error.Code != string(apierr.ValidationError) || env.Error.Message != "Invalid request body" {
		t.Fatalf("got %s", raw)
	}
	var got []apierr.FieldError
	if err := json.Unmarshal(env.Error.Details, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"name": apierr.FieldTooLong, "amount": apierr.FieldOutOfRange, "kind": apierr.FieldInvalid,
		"splits[0].person_name": apierr.FieldRequired, "tags": apierr.FieldTooMany,
	}
	if len(got) != len(want) {
		t.Fatalf("details %s", env.Error.Details)
	}
	for _, fe := range got {
		if want[fe.Field] != fe.Code || fe.Params == nil {
			t.Errorf("unexpected %+v", fe)
		}
	}
	if got[0].Params["max"] != float64(5) {
		t.Errorf("name params %v", got[0].Params)
	}
	if strings.Contains(raw, "Key: '") || strings.Contains(raw, "bindReq") {
		t.Errorf("validator text leaked: %s", raw)
	}
}

func TestValidationDetailsAlwaysAList(t *testing.T) {
	for _, h := range []gin.HandlerFunc{
		func(c *gin.Context) { Validation(c, "Invalid status") },
		func(c *gin.Context) { ValidationErr(c, errors.New("invalid request")) },
	} {
		_, env, _ := run(t, h, "")
		if string(env.Error.Details) != "[]" {
			t.Errorf("details %s, want []", env.Error.Details)
		}
	}
	sentinel := apierr.Invalid("private_note: at most 500 characters", "private_note", apierr.FieldTooLong, "max", 500)
	_, env, _ := run(t, func(c *gin.Context) { ValidationErr(c, sentinel) }, "")
	if !strings.Contains(string(env.Error.Details), `"field":"private_note"`) || env.Error.Message != sentinel.Error() {
		t.Errorf("got %+v", env.Error)
	}
}

func TestNoRouteAnswersTheEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.NoRoute(NoRoute)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
	var env envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("not JSON: %q", w.Body.String())
	}
	if env.Success || env.Error.Code != string(apierr.NotFound) {
		t.Errorf("envelope = %+v", env)
	}
}
