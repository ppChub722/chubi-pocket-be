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

	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

//go:embed openapi.yaml
var specYAML []byte

const apiPrefix = "/api/v1/"

var httpMethods = map[string]bool{"get": true, "put": true, "post": true, "delete": true, "patch": true}

type Handler struct {
	engine *gin.Engine

	once sync.Once
	body []byte
	err  error
}

func NewHandler(engine *gin.Engine) *Handler { return &Handler{engine: engine} }

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

func (h *Handler) Serve(c *gin.Context) {
	h.once.Do(h.build)
	if h.err != nil {
		response.InternalError(c, "The API document could not be built", h.err)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", h.body)
}
