package idempotency

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

type rig struct {
	r     *gin.Engine
	pool  *pgxpool.Pool
	user  uuid.UUID
	runs  int
	fail  bool
	other uuid.UUID
}

func newRig(t *testing.T) *rig {
	t.Helper()
	pool := testdb.Pool(t)
	var has bool
	if err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'idempotency_keys')`).Scan(&has); err != nil || !has {
		t.Skip("idempotency_keys missing: apply migration 000124")
	}
	g := &rig{pool: pool, user: testdb.User(t, pool), other: testdb.User(t, pool)}
	gin.SetMode(gin.TestMode)
	g.r = gin.New()
	asUser := g.user
	g.r.Use(func(c *gin.Context) {
		u := asUser
		if c.GetHeader("X-As") == "other" {
			u = g.other
		}
		c.Set("user_id", u)
	})
	g.r.Use(Middleware(pool, slog.New(slog.NewTextHandler(io.Discard, nil))))
	g.r.POST("/things", func(c *gin.Context) {
		g.runs++
		if g.fail {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false})
			return
		}
		body, _ := io.ReadAll(c.Request.Body)
		c.JSON(http.StatusCreated, gin.H{"n": g.runs, "echo": string(body)})
	})
	g.r.GET("/things", func(c *gin.Context) {
		g.runs++
		c.JSON(http.StatusOK, gin.H{"n": g.runs})
	})
	g.r.POST("/upload", func(c *gin.Context) {
		g.runs++
		c.JSON(http.StatusOK, gin.H{"n": g.runs})
	})
	return g
}

func (g *rig) do(method, path, key, body string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set(Header, key)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	g.r.ServeHTTP(w, req)
	return w
}

func code(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return env.Error.Code
}

func TestSameKeyReplaysTheFirstAnswer(t *testing.T) {
	g := newRig(t)
	first := g.do("POST", "/things", "k1", `{"a":1}`)
	if first.Code != http.StatusCreated || first.Header().Get(ReplayHeader) != "" {
		t.Fatalf("first: %d %v", first.Code, first.Header())
	}
	second := g.do("POST", "/things", "k1", `{"a":1}`)
	if second.Code != http.StatusCreated || second.Header().Get(ReplayHeader) != "true" {
		t.Fatalf("second: %d %v", second.Code, second.Header())
	}
	if second.Body.String() != first.Body.String() {
		t.Errorf("replay body %q != first %q", second.Body.String(), first.Body.String())
	}
	if g.runs != 1 {
		t.Errorf("handler ran %d times, want 1", g.runs)
	}
}

func TestNoKeyRunsEveryTime(t *testing.T) {
	g := newRig(t)
	g.do("POST", "/things", "", `{}`)
	g.do("POST", "/things", "", `{}`)
	if g.runs != 2 {
		t.Errorf("runs = %d, want 2", g.runs)
	}
}

func TestSameKeyForADifferentRequestIsRefused(t *testing.T) {
	g := newRig(t)
	g.do("POST", "/things", "k2", `{"a":1}`)
	w := g.do("POST", "/things", "k2", `{"a":2}`)
	if w.Code != http.StatusUnprocessableEntity || code(t, w) != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	if g.runs != 1 {
		t.Errorf("runs = %d", g.runs)
	}
}

func TestAFailedFirstRequestCanBeRetriedForReal(t *testing.T) {
	g := newRig(t)
	g.fail = true
	if w := g.do("POST", "/things", "k3", `{}`); w.Code != http.StatusInternalServerError {
		t.Fatalf("first: %d", w.Code)
	}
	g.fail = false
	w := g.do("POST", "/things", "k3", `{}`)
	if w.Code != http.StatusCreated || w.Header().Get(ReplayHeader) != "" {
		t.Fatalf("retry: %d %v", w.Code, w.Header())
	}
	if g.runs != 2 {
		t.Errorf("runs = %d, want 2", g.runs)
	}
}

func TestKeysAreOnePerUser(t *testing.T) {
	g := newRig(t)
	g.do("POST", "/things", "shared", `{}`)
	w := g.do("POST", "/things", "shared", `{}`, "X-As", "other")
	if w.Header().Get(ReplayHeader) != "" || g.runs != 2 {
		t.Errorf("another user's key replayed: runs %d, %v", g.runs, w.Header())
	}
}

func TestAnUnfinishedFirstRequestIsInProgress(t *testing.T) {
	g := newRig(t)
	// a claim with no answer yet (the first request is still running)
	body := `{"a":1}`
	g.do("POST", "/things", "k4", body)
	if _, err := g.pool.Exec(context.Background(),
		`UPDATE idempotency_keys SET status_code = NULL, response_body = NULL WHERE user_id = $1 AND key = 'k4'`, g.user); err != nil {
		t.Fatal(err)
	}
	w := g.do("POST", "/things", "k4", body)
	if w.Code != http.StatusConflict || code(t, w) != "IDEMPOTENCY_IN_PROGRESS" {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	// abandoned (older than a minute): taken over and run
	if _, err := g.pool.Exec(context.Background(),
		`UPDATE idempotency_keys SET created_at = now() - interval '5 minutes' WHERE user_id = $1 AND key = 'k4'`, g.user); err != nil {
		t.Fatal(err)
	}
	w = g.do("POST", "/things", "k4", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("take over: %d %s", w.Code, w.Body.String())
	}
}

func TestReadsAndUploadsAreUntouched(t *testing.T) {
	g := newRig(t)
	g.do("GET", "/things", "k5", "")
	g.do("GET", "/things", "k5", "")
	if g.runs != 2 {
		t.Errorf("GET runs = %d", g.runs)
	}
	g.runs = 0
	for i := 0; i < 2; i++ {
		g.do("POST", "/upload", "k6", "x", "Content-Type", "multipart/form-data; boundary=b")
	}
	if g.runs != 2 {
		t.Errorf("upload runs = %d", g.runs)
	}
}

func TestATooLongKeyIsRefused(t *testing.T) {
	g := newRig(t)
	w := g.do("POST", "/things", strings.Repeat("k", maxKeyLen+1), `{}`)
	if w.Code != http.StatusBadRequest || code(t, w) != "VALIDATION_ERROR" {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}
