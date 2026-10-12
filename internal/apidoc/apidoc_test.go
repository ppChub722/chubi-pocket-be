package apidoc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func mustDoc(t *testing.T) map[string]any {
	t.Helper()
	doc, err := Document()
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	return doc
}

// lookup walks "#/a/b/c" in doc.
func lookup(doc map[string]any, ref string) bool {
	if !strings.HasPrefix(ref, "#/") {
		return false
	}
	var cur any = doc
	for _, part := range strings.Split(ref[2:], "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		if cur, ok = m[part]; !ok {
			return false
		}
	}
	return true
}

func walkRefs(t *testing.T, doc map[string]any, v any, where string) {
	switch x := v.(type) {
	case map[string]any:
		if ref, ok := x["$ref"].(string); ok && !lookup(doc, ref) {
			t.Errorf("%s: unresolved $ref %s", where, ref)
		}
		for k, child := range x {
			walkRefs(t, doc, child, where+"/"+k)
		}
	case []any:
		for i, child := range x {
			walkRefs(t, doc, child, where)
			_ = i
		}
	}
}

func TestSpecIsValidEnough(t *testing.T) {
	doc := mustDoc(t)
	if doc["openapi"] != "3.1.0" {
		t.Errorf("openapi = %v", doc["openapi"])
	}
	walkRefs(t, doc, doc, "#")

	pathParam := regexp.MustCompile(`\{([^}]+)\}`)
	paths := doc["paths"].(map[string]any)
	seenIDs := map[string]bool{}
	for path, item := range paths {
		ops := item.(map[string]any)
		shared := map[string]bool{}
		if params, ok := ops["parameters"].([]any); ok {
			for _, p := range params {
				if m, ok := p.(map[string]any); ok {
					if ref, ok := m["$ref"].(string); ok && strings.HasPrefix(ref, "#/components/parameters/") {
						name := strings.TrimPrefix(ref, "#/components/parameters/")
						if p, ok := doc["components"].(map[string]any)["parameters"].(map[string]any)[name].(map[string]any); ok && p["in"] == "path" {
							m = p
						}
					}
					if m["in"] == "path" {
						shared[m["name"].(string)] = true
					}
				}
			}
		}
		for method, raw := range ops {
			if !httpMethods[method] {
				continue
			}
			op := raw.(map[string]any)
			label := strings.ToUpper(method) + " " + path
			id, _ := op["operationId"].(string)
			if id == "" || seenIDs[id] {
				t.Errorf("%s: missing or duplicate operationId %q", label, id)
			}
			seenIDs[id] = true
			if s, _ := op["summary"].(string); s == "" {
				t.Errorf("%s: no summary", label)
			}
			if tags, _ := op["tags"].([]any); len(tags) == 0 {
				t.Errorf("%s: no tags", label)
			}
			// x-used-by is optional now (the hub reads the FE page-flow registry);
			// when present it must be well formed.
			used, _ := op["x-used-by"].([]any)
			for _, u := range used {
				m, ok := u.(map[string]any)
				if !ok || m["page"] == nil || m["calls"] == nil {
					t.Errorf("%s: bad x-used-by entry %v", label, u)
				}
			}
			if _, ok := op["responses"].(map[string]any); !ok {
				t.Errorf("%s: no responses", label)
			}
			for _, m := range pathParam.FindAllStringSubmatch(path, -1) {
				if !shared[m[1]] {
					t.Errorf("%s: path param {%s} not declared", label, m[1])
				}
			}
		}
	}
}

func TestServeInjectsCounters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/a", func(c *gin.Context) {})
	r.POST("/api/v1/b", func(c *gin.Context) {})
	r.GET("/health", func(c *gin.Context) {})
	r.GET("/api/v1/openapi.json", NewHandler(r).Serve)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("json: %v", err)
	}
	if got := doc["x-registered-routes"]; got != float64(3) {
		t.Errorf("x-registered-routes = %v, want 3 (/api/v1 routes only)", got)
	}
	if got, want := doc["x-spec-routes"], float64(SpecRoutes(mustDoc(t))); got != want {
		t.Errorf("x-spec-routes = %v, want %v", got, want)
	}
}

// A schema's `required` keys must exist in its `properties` (catches typos).
func TestRequiredKeysAreDeclared(t *testing.T) {
	doc := mustDoc(t)
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	for name, raw := range schemas {
		s := raw.(map[string]any)
		props, _ := s["properties"].(map[string]any)
		req, _ := s["required"].([]any)
		for _, r := range req {
			if _, ok := props[r.(string)]; !ok {
				t.Errorf("schema %s: required key %q is not in properties", name, r)
			}
		}
	}
}

var routeLine = regexp.MustCompile(`(?:api|protected)\.(GET|POST|PUT|PATCH|DELETE)\("([^"]+)"`)

// TestCoverage logs the registered routes that have no entry in the spec yet
// (informational — the coverage note in ai/inbox/be.md is built from it).
func TestCoverage(t *testing.T) {
	src, err := os.ReadFile("../../cmd/api/main.go")
	if err != nil {
		t.Skipf("main.go not readable: %v", err)
	}
	doc := mustDoc(t)
	have := map[string]bool{}
	for path, item := range doc["paths"].(map[string]any) {
		for method := range item.(map[string]any) {
			if httpMethods[method] {
				have[strings.ToUpper(method)+" "+path] = true
			}
		}
	}
	param := regexp.MustCompile(`:([a-z_]+)`)
	total, covered := 0, 0
	var missing []string
	for _, m := range routeLine.FindAllStringSubmatch(string(src), -1) {
		path := "/" + strings.TrimPrefix(m[2], "/")
		path = param.ReplaceAllString(path, "{$1}")
		key := m[1] + " " + path
		total++
		if have[key] || (strings.Contains(key, "{") && have[normID(key)]) {
			covered++
		} else {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	t.Logf("routes in main.go: %d, in the spec: %d, not yet in the spec: %d", total, covered, len(missing))
	for _, k := range missing {
		t.Logf("  MISSING %s", k)
	}
}

// normID — spec paths use {id} for every single-id path.
func normID(key string) string {
	return regexp.MustCompile(`\{[a-z_]+\}`).ReplaceAllStringFunc(key, func(s string) string { return "{id}" })
}

// TestListOperations logs every operation (method path operationId [tag]) —
// the list FE-A's page-flow registry is checked against.
func TestListOperations(t *testing.T) {
	doc := mustDoc(t)
	var lines []string
	for path, item := range doc["paths"].(map[string]any) {
		for method, raw := range item.(map[string]any) {
			if !httpMethods[method] {
				continue
			}
			op := raw.(map[string]any)
			tag := ""
			if tags, ok := op["tags"].([]any); ok && len(tags) > 0 {
				tag, _ = tags[0].(string)
			}
			lines = append(lines, tag+"|"+strings.ToUpper(method)+" "+path+"|"+op["operationId"].(string))
		}
	}
	sort.Strings(lines)
	t.Logf("%d operations", len(lines))
	for _, l := range lines {
		t.Logf("OP %s", l)
	}
}

// TestSpecRoutesAreRegistered — every operation in the spec is a route main.go
// registers (the spec is the source of truth: a documented endpoint that does
// not exist is a bug in the code).
func TestSpecRoutesAreRegistered(t *testing.T) {
	src, err := os.ReadFile("../../cmd/api/main.go")
	if err != nil {
		t.Skipf("main.go not readable: %v", err)
	}
	param := regexp.MustCompile(`:([a-z_]+)`)
	registered := map[string]bool{}
	for _, m := range routeLine.FindAllStringSubmatch(string(src), -1) {
		path := param.ReplaceAllString("/"+strings.TrimPrefix(m[2], "/"), "{$1}")
		registered[normID(m[1]+" "+path)] = true
	}
	doc := mustDoc(t)
	for path, item := range doc["paths"].(map[string]any) {
		for method := range item.(map[string]any) {
			if !httpMethods[method] {
				continue
			}
			key := strings.ToUpper(method) + " " + path
			if !registered[normID(key)] {
				t.Errorf("the spec documents %s but main.go does not register it", key)
			}
		}
	}
}
