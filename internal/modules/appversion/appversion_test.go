package appversion

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
)

func newRouter(info Info) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewHandler(info)
	r := gin.New()
	r.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	api := r.Group("/api/v1")
	api.Use(h.Middleware("/api/v1/app/version"))
	api.GET("/app/version", h.Version)
	api.GET("/transactions", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"reached": true}) })
	return r
}

func get(r *gin.Engine, path, build string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if build != "" {
		req.Header.Set(Header, build)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

var info = Info{
	MinBuild: 41, LatestBuild: 42,
	DownloadURL: "https://appdistribution.firebase.dev/i/test", MessageTH: "อัปเดตแอปก่อนนะ",
}

func TestVersionEndpoint(t *testing.T) {
	w := get(newRouter(info), "/api/v1/app/version", "1") // old build still gets it
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	apitest.Conform(t, "getAppVersion", w.Code, w.Body.Bytes())
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"min_build": 41.0, "latest_build": 42.0,
		"download_url": "https://appdistribution.firebase.dev/i/test", "message_th": "อัปเดตแอปก่อนนะ",
		"message_en": nil,
	}
	if len(body) != len(want) {
		t.Fatalf("body = %s", w.Body)
	}
	for k, v := range want {
		got, ok := body[k]
		if !ok || got != v {
			t.Errorf("%s = %v (present %v), want %v", k, got, ok, v)
		}
	}
}

func TestOutdatedBuildGets426(t *testing.T) {
	w := get(newRouter(info), "/api/v1/transactions", "40")
	if w.Code != http.StatusUpgradeRequired {
		t.Fatalf("status = %d, want 426", w.Code)
	}
	var body struct {
		Success bool `json:"success"`
		Error   struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Success || body.Error.Code != string(CodeOutdated) ||
		body.Error.Details["download_url"] != info.DownloadURL || body.Error.Details["min_build"] != 41.0 {
		t.Errorf("body = %s", w.Body)
	}
}

// A phone sends the split-per-ABI versionCode (ABI × 1000 + build): it counts as the build.
func TestSplitAbiBuild(t *testing.T) {
	i := info
	i.MinBuild = 170
	r := newRouter(i)
	for build, want := range map[string]int{
		"165": http.StatusUpgradeRequired, "2167": http.StatusUpgradeRequired, "1169": http.StatusUpgradeRequired,
		"170": http.StatusOK, "2170": http.StatusOK, "1170": http.StatusOK, "4171": http.StatusOK,
	} {
		if w := get(r, "/api/v1/transactions", build); w.Code != want {
			t.Errorf("build %s: status = %d, want %d", build, w.Code, want)
		}
	}
}

func TestPasses(t *testing.T) {
	cases := []struct {
		name, path, build string
		min               int
	}{
		{"no header", "/api/v1/transactions", "", 41},
		{"not a number", "/api/v1/transactions", "abc", 41},
		{"same build", "/api/v1/transactions", "41", 41},
		{"newer build", "/api/v1/transactions", " 42 ", 41},
		{"check off", "/api/v1/transactions", "1", 0},
		{"health", "/health", "1", 41},
	}
	for _, tc := range cases {
		i := info
		i.MinBuild = tc.min
		if w := get(newRouter(i), tc.path, tc.build); w.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 (%s)", tc.name, w.Code, w.Body)
		}
	}
}
