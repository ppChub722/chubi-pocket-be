package auth_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
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
	var style string
	if err := testdb.Pool(t).QueryRow(context.Background(),
		`SELECT preferences->>'money_style' FROM user_preferences WHERE user_id = $1`, user["id"]).Scan(&style); err != nil || style != "color_only" {
		t.Errorf("new user's money_style = %q (%v)", style, err)
	}
	tok := out["token"].(map[string]any)
	if tok["access_token"] == "" || tok["expires_in"].(float64) != 3600 {
		t.Errorf("token = %v", tok)
	}

	out = call(t, r, "POST", "/api/v1/auth/login", map[string]any{"identifier": name, "password": "Password123!"}, "login", http.StatusOK)
	if out["user"].(map[string]any)["id"] != user["id"] {
		t.Errorf("login user = %v", out["user"])
	}
	call(t, r, "POST", "/api/v1/auth/login", map[string]any{"identifier": email, "password": "Password123!"}, "login", http.StatusOK)

	call(t, r, "POST", "/api/v1/auth/login", map[string]any{"identifier": name, "password": "wrong-password"}, "login", http.StatusUnauthorized)
	// 0.4.2 1b-2: every login failure is the same 401 INVALID_CREDENTIALS, a bad / empty body included
	for _, bad := range []any{
		map[string]any{"identifier": name},
		map[string]any{"identifier": "nobody_" + name, "password": "Password123!"},
		map[string]any{},
		"not json",
		nil,
	} {
		out := call(t, r, "POST", "/api/v1/auth/login", bad, "login", http.StatusUnauthorized)
		if e, _ := out["error"].(map[string]any); e["code"] != "INVALID_CREDENTIALS" {
			t.Errorf("login %v = %v", bad, out)
		}
	}
	call(t, r, "POST", "/api/v1/auth/register", map[string]any{"username": uniq(), "password": "short", "display_name": "X"}, "register", http.StatusBadRequest)
	call(t, r, "POST", "/api/v1/auth/register", map[string]any{"username": "ab", "password": "Password123!", "display_name": "X"}, "register", http.StatusBadRequest)
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

// 0.4.2 1b-2: an account made through Telegram has no password: password login is refused (the
// same 401), and it sets its first password without a current one.
func TestPasswordlessAccount(t *testing.T) {
	pub, h := newPublic(t)
	pool := testdb.Pool(t)
	ctx := context.Background()
	cfg := &config.Config{}
	cfg.App.RegistrationOpen = true
	cfg.JWT.Secret, cfg.JWT.ExpirationTime = "test-secret", time.Hour
	svc := auth.NewService(auth.NewStore(pool), cfg)

	name := uniq()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := svc.CreateExternalUserTx(ctx, tx, auth.ExternalSignup{Username: name, DisplayName: "Tg", CreatedVia: auth.CreatedViaTelegram})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// a password account can't be created without one (DB check)
	tx, _ = pool.Begin(ctx)
	if _, err := svc.CreateExternalUserTx(ctx, tx, auth.ExternalSignup{Username: uniq(), DisplayName: "X", CreatedVia: auth.CreatedViaPassword}); err == nil {
		t.Errorf("a passwordless password account was made")
	}
	tx.Rollback(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, username, display_name, password_hash, currency) VALUES ($1, $2, 'X', NULL, 'THB')`,
		uuid.New(), uniq()); err == nil {
		t.Errorf("the DB took a password account with no password")
	}

	for _, pw := range []string{"", "anything-123"} {
		out := call(t, pub, "POST", "/api/v1/auth/login", map[string]any{"identifier": name, "password": pw}, "login", http.StatusUnauthorized)
		if e, _ := out["error"].(map[string]any); e["code"] != "INVALID_CREDENTIALS" {
			t.Errorf("passwordless login = %v", out)
		}
	}
	if resp, err := svc.IssueFor(ctx, uid); err != nil || resp.User.Username != name || resp.Token.AccessToken == "" {
		t.Errorf("issue for = %+v %v", resp, err)
	}
	if taken, err := svc.UsernameTaken(ctx, strings.ToUpper(name)); err != nil || !taken {
		t.Errorf("username taken = %v %v", taken, err)
	}

	r := apitest.Engine(uid, func(g *gin.RouterGroup) { g.PUT("/auth/password", h.ChangePassword) })
	call(t, r, "PUT", "/api/v1/auth/password", map[string]any{"new_password": "First123!"}, "changePassword", http.StatusOK)
	call(t, pub, "POST", "/api/v1/auth/login", map[string]any{"identifier": name, "password": "First123!"}, "login", http.StatusOK)
	// now it has one: the current password is required again
	call(t, r, "PUT", "/api/v1/auth/password", map[string]any{"new_password": "Second123!"}, "changePassword", http.StatusUnauthorized)
	call(t, r, "PUT", "/api/v1/auth/password", map[string]any{"current_password": "First123!", "new_password": "Second123!"}, "changePassword", http.StatusOK)
}
