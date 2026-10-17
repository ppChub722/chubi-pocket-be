package auth_test

// Handler tests for the Auth operations of the API spec v2. register / login
// are public, so they run on a plain gin engine (no user middleware).

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/config"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

func newPublic(t *testing.T) (*gin.Engine, *auth.Handler) {
	t.Helper()
	pool := testdb.Pool(t)
	cfg := &config.Config{}
	cfg.App.RegistrationOpen = true
	cfg.JWT.Secret = "test-secret"
	cfg.JWT.ExpirationTime = time.Hour
	h := auth.NewHandler(auth.NewService(auth.NewStore(pool), cfg), slog.Default())

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/auth/register", h.Register)
	r.POST("/api/v1/auth/login", h.Login)
	t.Cleanup(func() {
		ctx := context.Background()
		// Users created through register in this test (username prefix).
		_, _ = pool.Exec(ctx, `DELETE FROM user_preferences WHERE user_id IN (SELECT id FROM users WHERE username LIKE 'authtest_%')`)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE username LIKE 'authtest_%'`)
	})
	return r, h
}

func call(t *testing.T, r http.Handler, method, path string, body any, op string, want int) map[string]any {
	t.Helper()
	status, raw := apitest.Do(t, r, method, path, body)
	if status != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, status, want, raw)
	}
	if op != "" {
		apitest.Conform(t, op, status, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func uniq() string { return "authtest_" + uuid.NewString()[:8] }

func TestRegisterLogin(t *testing.T) {
	r, _ := newPublic(t)
	name := uniq()
	email := name + "@test.local"
	body := map[string]any{"username": name, "password": "Password123!", "display_name": "Auth Tester", "email": email}
	apitest.ConformRequest(t, "register", body)

	out := call(t, r, "POST", "/api/v1/auth/register", body, "register", http.StatusCreated)
	user := out["user"].(map[string]any)
	if user["currency"] != "THB" || user["status"] != "active" || user["username"] != name {
		t.Errorf("registered user = %v", user)
	}
	if _, ok := user["password_hash"]; ok {
		t.Error("password hash leaked")
	}
	// A new user sees amounts in colour only (owner 2026-10-17).
	var style string
	if err := testdb.Pool(t).QueryRow(context.Background(),
		`SELECT preferences->>'money_style' FROM user_preferences WHERE user_id = $1`, user["id"]).Scan(&style); err != nil || style != "color_only" {
		t.Errorf("new user's money_style = %q (%v)", style, err)
	}
	tok := out["token"].(map[string]any)
	if tok["access_token"] == "" || tok["expires_in"].(float64) != 3600 {
		t.Errorf("token = %v", tok)
	}

	// Login by username and by email.
	out = call(t, r, "POST", "/api/v1/auth/login", map[string]any{"identifier": name, "password": "Password123!"}, "login", http.StatusOK)
	if out["user"].(map[string]any)["id"] != user["id"] {
		t.Errorf("login user = %v", out["user"])
	}
	call(t, r, "POST", "/api/v1/auth/login", map[string]any{"identifier": email, "password": "Password123!"}, "login", http.StatusOK)

	// Errors.
	call(t, r, "POST", "/api/v1/auth/login", map[string]any{"identifier": name, "password": "wrong-password"}, "login", http.StatusUnauthorized)
	call(t, r, "POST", "/api/v1/auth/login", map[string]any{"identifier": name}, "login", http.StatusBadRequest)
	call(t, r, "POST", "/api/v1/auth/register", map[string]any{"username": uniq(), "password": "short", "display_name": "X"}, "register", http.StatusBadRequest)
	call(t, r, "POST", "/api/v1/auth/register", map[string]any{"username": "ab", "password": "Password123!", "display_name": "X"}, "register", http.StatusBadRequest)
	// Duplicate username: 409 is not documented in the spec for register.
	call(t, r, "POST", "/api/v1/auth/register", body, "", http.StatusConflict)
}

func TestRegisterCurrency(t *testing.T) {
	r, _ := newPublic(t)
	out := call(t, r, "POST", "/api/v1/auth/register",
		map[string]any{"username": uniq(), "password": "Password123!", "display_name": "Usd", "currency": "usd"}, "register", http.StatusCreated)
	user := out["user"].(map[string]any)
	if user["currency"] != "USD" {
		t.Errorf("currency = %v", user["currency"])
	}
	call(t, r, "POST", "/api/v1/auth/register",
		map[string]any{"username": uniq(), "password": "Password123!", "display_name": "Bad", "currency": "US"}, "register", http.StatusBadRequest)
}

func TestLogoutAndChangePassword(t *testing.T) {
	pub, h := newPublic(t)
	name := uniq()
	out := call(t, pub, "POST", "/api/v1/auth/register",
		map[string]any{"username": name, "password": "Password123!", "display_name": "Pw"}, "register", http.StatusCreated)
	uid := uuid.MustParse(out["user"].(map[string]any)["id"].(string))

	r := apitest.Engine(uid, func(g *gin.RouterGroup) {
		g.POST("/auth/logout", h.Logout)
		g.PUT("/auth/password", h.ChangePassword)
	})
	ack := call(t, r, "POST", "/api/v1/auth/logout", nil, "logout", http.StatusOK)
	if ack["success"] != true {
		t.Errorf("logout = %v", ack)
	}

	call(t, r, "PUT", "/api/v1/auth/password", map[string]any{"current_password": "nope-nope", "new_password": "Another123!"}, "changePassword", http.StatusUnauthorized)
	call(t, r, "PUT", "/api/v1/auth/password", map[string]any{"current_password": "Password123!", "new_password": "Password123!"}, "changePassword", http.StatusBadRequest)
	call(t, r, "PUT", "/api/v1/auth/password", map[string]any{"current_password": "Password123!", "new_password": "short"}, "changePassword", http.StatusBadRequest)

	ack = call(t, r, "PUT", "/api/v1/auth/password", map[string]any{"current_password": "Password123!", "new_password": "Another123!"}, "changePassword", http.StatusOK)
	if ack["success"] != true {
		t.Errorf("change password = %v", ack)
	}
	call(t, pub, "POST", "/api/v1/auth/login", map[string]any{"identifier": name, "password": "Another123!"}, "login", http.StatusOK)
	call(t, pub, "POST", "/api/v1/auth/login", map[string]any{"identifier": name, "password": "Password123!"}, "login", http.StatusUnauthorized)
}
