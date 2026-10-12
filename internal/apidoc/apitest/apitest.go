// Package apitest drives a module's real handlers over HTTP (httptest) as a
// given user and checks the bodies against the OpenAPI spec, so a handler
// test fails when the code and the spec disagree.
//
//	r := apitest.Engine(userID, func(g *gin.RouterGroup) {
//	    g.GET("/tags", h.List)
//	})
//	status, body := apitest.Do(t, r, "GET", "/api/v1/tags", nil)
//	apitest.Conform(t, "listTags", status, body)
package apitest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc"
)

// userKey is the context key auth.Middleware sets (auth.UserIDFromContext).
const userKey = "user_id"

// Engine returns a gin engine whose /api/v1 group runs register with every
// request authenticated as userID (no token, no DB lookup).
func Engine(userID uuid.UUID, register func(g *gin.RouterGroup)) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set(userKey, userID)
		c.Next()
	})
	register(g)
	return r
}

// Do sends a request (body = nil, raw []byte / string, or anything JSON
// encodable) and returns the status and the response body.
func Do(t *testing.T, r http.Handler, method, path string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case string:
		rd = bytes.NewReader([]byte(b))
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("encode body: %v", err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

// Conform fails the test when the response doesn't match the spec for
// operationId (an undocumented key, a missing required key, a wrong type…).
func Conform(t *testing.T, operationID string, status int, body []byte) {
	t.Helper()
	if err := apidoc.Response(operationID, status, body); err != nil {
		t.Errorf("%v\nbody: %s", err, truncate(body))
	}
}

// ConformRequest checks a request body the test sends against the spec.
func ConformRequest(t *testing.T, operationID string, body any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode body: %v", err)
	}
	if err := apidoc.Request(operationID, raw); err != nil {
		t.Errorf("%v", err)
	}
}

func truncate(b []byte) string {
	if len(b) > 1500 {
		return string(b[:1500]) + "…"
	}
	return string(b)
}
