package connections

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
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
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

func (f *fakeBot) HasProfilePhoto(context.Context, int64) (bool, error) { return len(f.photo) > 0, nil }

const verifier = "verifier-0123456789-abcdefghij-ABCDEFGHIJ-xyz"

func challengeOf(v string) string {
	sum := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

type loginEnv struct {
	*env
	auth *auth.Service
	r    *gin.Engine
	cfg  *config.Config
}

func newLoginEnv(t *testing.T) *loginEnv {
	t.Helper()
	e := newEnv(t)
	cfg := &config.Config{App: config.AppConfig{RegistrationOpen: true},
		JWT: config.JWTConfig{Secret: "test-secret", ExpirationTime: time.Hour}}
	a := auth.NewService(auth.NewStore(e.pool), cfg)
	e.svc.WithAuth(a)
	h := NewHandler(e.svc)
	r := apitest.Engine(uuid.Nil, func(g *gin.RouterGroup) {
		g.POST("/auth/telegram/start", h.LoginStart)
		g.POST("/auth/telegram/exchange", h.LoginExchange)
		g.POST("/auth/telegram/signup", h.LoginSignup)
	})
	return &loginEnv{env: e, auth: a, r: r, cfg: cfg}
}

// start → the code from the answer.
func (l *loginEnv) start(t *testing.T) string {
	t.Helper()
	code, body := call(t, l.r, http.MethodPost, "/auth/telegram/start", map[string]string{"code_challenge": challengeOf(verifier)})
	apitest.Conform(t, "startTelegramLogin", code, body)
	if code != 200 {
		t.Fatalf("start = %d %s", code, body)
	}
	s := decode[LoginStart](t, body)
	if s.URL != "https://t.me/chubiPocket_bot?start=login_"+s.Code || len(s.Code) != 22 {
		t.Fatalf("start = %+v", s)
	}
	if d := time.Until(s.ExpiresAt); d < 4*time.Minute || d > 5*time.Minute+time.Second {
		t.Errorf("expires in %v", d)
	}
	return s.Code
}

func (l *loginEnv) exchange(t *testing.T, code, v string) (int, []byte) {
	t.Helper()
	status, body := call(t, l.r, http.MethodPost, "/auth/telegram/exchange", map[string]string{"code": code, "code_verifier": v})
	apitest.Conform(t, "exchangeTelegramLogin", status, body)
	return status, body
}

func cleanupUser(t *testing.T, l *loginEnv, username string) {
	t.Cleanup(func() {
		_, _ = l.pool.Exec(context.Background(), `DELETE FROM users WHERE username = $1`, username)
	})
}

func TestLoginWithLinkedTelegram(t *testing.T) {
	l := newLoginEnv(t)
	ctx := context.Background()
	me := testdb.User(t, l.pool)
	s, _ := l.svc.StartLink(ctx, me, ProviderTelegram)
	if _, ok, _ := l.svc.TelegramBind(ctx, codeOf(t, s.URL), 990001, "@nong"); !ok {
		t.Fatal("bind")
	}

	code := l.start(t)
	// before the chat opens it: pending (202)
	if status, body := l.exchange(t, code, verifier); status != 202 || decode[Exchange](t, body).Status != "pending" {
		t.Errorf("pending = %d %s", status, body)
	}
	id, name, ok, err := l.svc.TelegramLoginOpen(ctx, code, 990001, "@nong", "Nong Lee")
	if err != nil || !ok || name != "Test user" {
		t.Fatalf("open = %v %q %v %v", id, name, ok, err)
	}
	// another chat can't take or confirm it
	if _, _, ok, _ := l.svc.TelegramLoginOpen(ctx, code, 990002, "", ""); ok {
		t.Errorf("a second chat took the code")
	}
	if ok, _ := l.svc.TelegramLoginConfirm(ctx, id, 990002); ok {
		t.Errorf("a second chat confirmed")
	}
	if status, _ := l.exchange(t, code, verifier); status != 202 {
		t.Errorf("opened but not confirmed = %d", status)
	}
	if ok, err := l.svc.TelegramLoginConfirm(ctx, id, 990001); !ok || err != nil {
		t.Fatalf("confirm = %v %v", ok, err)
	}
	// a wrong verifier: one 401
	if status, body := l.exchange(t, code, verifier+"x"); status != 401 || !strings.Contains(string(body), "LOGIN_CODE_INVALID") {
		t.Errorf("wrong verifier = %d %s", status, body)
	}
	status, body := l.exchange(t, code, verifier)
	ex := decode[Exchange](t, body)
	if status != 200 || ex.Status != "login" || ex.Auth == nil || ex.Auth.User.ID != me || ex.Auth.Token.AccessToken == "" {
		t.Fatalf("login = %d %s", status, body)
	}
	// single use
	if status, _ := l.exchange(t, code, verifier); status != 401 {
		t.Errorf("reused code = %d", status)
	}

	// an expired code
	code = l.start(t)
	_, _ = l.pool.Exec(ctx, `UPDATE connection_link_codes SET expires_at = NOW() - INTERVAL '1 second' WHERE code_hash = $1`, hashCode(code))
	if status, _ := l.exchange(t, code, verifier); status != 401 {
		t.Errorf("expired = %d", status)
	}
	if _, _, ok, _ := l.svc.TelegramLoginOpen(ctx, code, 990001, "", ""); ok {
		t.Errorf("an expired code opened")
	}
	// a login code can't link, a link code can't log in
	code = l.start(t)
	if _, ok, _ := l.svc.TelegramBind(ctx, code, 990003, ""); ok {
		t.Errorf("a login code linked a chat")
	}
	s, _ = l.svc.StartLink(ctx, me, ProviderTelegram)
	if _, _, ok, _ := l.svc.TelegramLoginOpen(ctx, codeOf(t, s.URL), 990001, "", ""); ok {
		t.Errorf("a link code opened a login")
	}
}

func TestSignupWithTelegram(t *testing.T) {
	l := newLoginEnv(t)
	ctx := context.Background()
	taken := testdb.User(t, l.pool)
	// a Telegram-style username (a-z 0-9 _) that is taken here
	takenName := "beam_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	if _, err := l.pool.Exec(ctx, `UPDATE users SET username = $2 WHERE id = $1`, taken, takenName); err != nil {
		t.Fatal(err)
	}

	code := l.start(t)
	id, name, ok, _ := l.svc.TelegramLoginOpen(ctx, code, 991001, "@"+strings.ToUpper(takenName), "Beam Lee")
	if !ok || name != "" {
		t.Fatalf("open = %v %q", ok, name)
	}
	l.svc.TelegramLoginConfirm(ctx, id, 991001)
	status, body := l.exchange(t, code, verifier)
	ex := decode[Exchange](t, body)
	if status != 200 || ex.Status != "signup" || ex.Signup == nil || ex.Signup.DisplayName != "Beam Lee" || !ex.Signup.HasPhoto {
		t.Fatalf("signup info = %d %s", status, body)
	}
	// the Telegram username is taken → a free variant of it
	if s := ex.Signup.UsernameSuggestion; !strings.HasPrefix(s, takenName) || s == takenName {
		t.Errorf("suggestion = %q (taken %q)", s, takenName)
	}

	signup := func(username string) (int, []byte) {
		st, b := call(t, l.r, http.MethodPost, "/auth/telegram/signup", map[string]string{"code": code, "code_verifier": verifier,
			"display_name": "Beam Lee", "username": username, "currency": "thb"})
		apitest.Conform(t, "signupWithTelegram", st, b)
		return st, b
	}
	if st, b := signup(takenName); st != 409 || !strings.Contains(string(b), "USERNAME_EXISTS") ||
		!strings.Contains(string(b), "username_suggestion") {
		t.Errorf("taken username = %d %s", st, b)
	}
	if st, _ := signup("ab"); st != 400 {
		t.Errorf("short username = %d", st)
	}
	l.cfg.App.RegistrationOpen = false
	if st, b := signup(ex.Signup.UsernameSuggestion); st != 403 || !strings.Contains(string(b), "REGISTRATION_CLOSED") {
		t.Errorf("closed = %d %s", st, b)
	}
	l.cfg.App.RegistrationOpen = true

	newName := ex.Signup.UsernameSuggestion
	cleanupUser(t, l, newName)
	st, b := signup(newName)
	if st != 201 {
		t.Fatalf("signup = %d %s", st, b)
	}
	resp := decode[auth.AuthResponse](t, b)
	if resp.User.Username != newName || resp.User.Currency != "THB" || resp.Token.AccessToken == "" {
		t.Errorf("signed up = %+v", resp.User)
	}
	// passwordless, created via telegram, linked, photo imported + picked
	var hasHash bool
	var via, source string
	_ = l.pool.QueryRow(ctx, `SELECT password_hash IS NOT NULL, created_via, avatar_settings->>'source' FROM users WHERE id = $1`,
		resp.User.ID).Scan(&hasHash, &via, &source)
	if hasHash || via != "telegram" || source != "telegram" {
		t.Errorf("account: hash %v via %q source %q", hasHash, via, source)
	}
	if resp.User.Avatar.Kind != shared.AvatarPhoto {
		t.Errorf("avatar = %+v", resp.User.Avatar)
	}
	if uid, on, linked, _ := l.svc.TelegramUser(ctx, 991001); !linked || uid != resp.User.ID || !on {
		t.Errorf("chat link = %v %v %v", uid, on, linked)
	}
	// the code is used up
	if st, _ := signup(newName + "x"); st != 401 {
		t.Errorf("reused signup code = %d", st)
	}

	// password login is refused for it (one 401), and it can't drop its only way in
	if _, err := l.auth.Login(ctx, auth.LoginRequest{Identifier: newName, Password: "anything-123"}); err == nil {
		t.Errorf("a passwordless account logged in with a password")
	}
	if _, err := l.svc.Unlink(ctx, resp.User.ID, ProviderTelegram); !errors.Is(err, shared.ErrLastLoginMethod) {
		t.Errorf("unlink of the only way in = %v", err)
	}
	if _, err := l.svc.TelegramUnlink(ctx, 991001); !errors.Is(err, shared.ErrLastLoginMethod) {
		t.Errorf("/unlink of the only way in = %v", err)
	}
	// another account can't take that chat away
	other := testdb.User(t, l.pool)
	s, _ := l.svc.StartLink(ctx, other, ProviderTelegram)
	if _, _, err := l.svc.TelegramBind(ctx, codeOf(t, s.URL), 991001, ""); !errors.Is(err, shared.ErrLastLoginMethod) {
		t.Errorf("moving the only way in = %v", err)
	}
	// re-linking its own Telegram (change account) is fine
	s, _ = l.svc.StartLink(ctx, resp.User.ID, ProviderTelegram)
	if _, ok, err := l.svc.TelegramBind(ctx, codeOf(t, s.URL), 991002, ""); !ok || err != nil {
		t.Errorf("change account = %v %v", ok, err)
	}
	// with a password set, unlink works
	if err := l.auth.ChangePassword(ctx, resp.User.ID, auth.ChangePasswordRequest{NewPassword: "new-password-1"}); err != nil {
		t.Fatalf("set password = %v", err)
	}
	if _, err := l.svc.Unlink(ctx, resp.User.ID, ProviderTelegram); err != nil {
		t.Errorf("unlink with a password = %v", err)
	}
	if _, err := l.auth.Login(ctx, auth.LoginRequest{Identifier: newName, Password: "new-password-1"}); err != nil {
		t.Errorf("password login after setting one = %v", err)
	}

	// a chat that got linked meanwhile → TELEGRAM_ALREADY_LINKED
	code = l.start(t)
	id, _, _, _ = l.svc.TelegramLoginOpen(ctx, code, 991003, "", "Someone")
	l.svc.TelegramLoginConfirm(ctx, id, 991003)
	s, _ = l.svc.StartLink(ctx, other, ProviderTelegram)
	l.svc.TelegramBind(ctx, codeOf(t, s.URL), 991003, "")
	st, b = call(t, l.r, http.MethodPost, "/auth/telegram/signup", map[string]string{"code": code, "code_verifier": verifier,
		"display_name": "X", "username": "nobody_" + uuid.NewString()[:8]})
	if st != 409 || !strings.Contains(string(b), "TELEGRAM_ALREADY_LINKED") {
		t.Errorf("already linked = %d %s", st, b)
	}
}

func TestLoginStartChecksAndLimits(t *testing.T) {
	l := newLoginEnv(t)
	for _, body := range []any{map[string]string{"code_challenge": "short"}, map[string]string{}, "nope"} {
		if st, _ := call(t, l.r, http.MethodPost, "/auth/telegram/start", body); st != 400 {
			t.Errorf("start %v = %d, want 400", body, st)
		}
	}
	// bot off → 409
	off := NewService(NewStore(l.pool), nil)
	off.WithAuth(l.auth)
	if _, err := off.StartLogin(context.Background(), challengeOf(verifier)); !errors.Is(err, ErrUnavailable) {
		t.Errorf("bot off = %v", err)
	}
	// 10 starts per IP per 10 minutes (3 used above by bad bodies)
	ok := 3
	for i := 0; i < 10; i++ {
		st, _ := call(t, l.r, http.MethodPost, "/auth/telegram/start", map[string]string{"code_challenge": challengeOf(verifier)})
		if st != 429 {
			ok++
		} else if st == 429 {
			apitest.Conform(t, "startTelegramLogin", st, []byte(`{"success":false,"error":{"code":"RATE_LIMITED","message":"x","details":null}}`))
		}
	}
	if ok != 10 {
		t.Errorf("allowed %d starts, want 10", ok)
	}
	// junk exchange
	if st, _ := l.exchange(t, "!!", verifier); st != 401 {
		t.Errorf("junk code = %d", st)
	}
}
