// Package apidoc serves the OpenAPI document (source of truth for the API
// shapes, owner-approved 2026-10-12) at GET /api/v1/openapi.json.
//
// The yaml is embedded and converted to JSON once. At serve time two
// counters are injected so the dev hub can show coverage exactly:
//   - x-registered-routes — routes the server really registers under /api/v1
//   - x-spec-routes       — method + path pairs described in the document
package apidoc

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-yaml"
)

//go:embed openapi.yaml
var specYAML []byte

const apiPrefix = "/api/v1/"

var httpMethods = map[string]bool{"get": true, "put": true, "post": true, "delete": true, "patch": true}

// Handler serves the converted document.
type Handler struct {
	engine *gin.Engine

	once sync.Once
	body []byte
	err  error
}

// NewHandler — engine is read lazily (on the first request) so every route
// is registered by then.
func NewHandler(engine *gin.Engine) *Handler { return &Handler{engine: engine} }

// Document converts the embedded yaml into a JSON object (no counters).
func Document() (map[string]any, error) {
	raw, err := yaml.YAMLToJSON(specYAML)
	if err != nil {
		return nil, fmt.Errorf("openapi.yaml: %w", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("openapi.yaml json: %w", err)
	}
	return doc, nil
}

// SpecRoutes counts the method + path pairs in the document.
func SpecRoutes(doc map[string]any) int {
	paths, _ := doc["paths"].(map[string]any)
	n := 0
	for _, item := range paths {
		ops, _ := item.(map[string]any)
		for method := range ops {
			if httpMethods[method] {
				n++
			}
		}
	}
	return n
}

func (h *Handler) build() {
	doc, err := Document()
	if err != nil {
		h.err = err
		return
	}
	registered := 0
	for _, r := range h.engine.Routes() {
		if strings.HasPrefix(r.Path, apiPrefix) {
			registered++
		}
	}
	doc["x-registered-routes"] = registered
	doc["x-spec-routes"] = SpecRoutes(doc)
	h.body, h.err = json.Marshal(doc)
}

// Serve — GET /api/v1/openapi.json (public, read-only).
func (h *Handler) Serve(c *gin.Context) {
	h.once.Do(h.build)
	if h.err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false,
			"error": gin.H{"code": "INTERNAL_SERVER_ERROR", "message": h.err.Error()}})
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", h.body)
}
