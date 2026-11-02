package connections

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/config"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type loginEnv struct {
	*env
	auth *auth.Service
	cfg  *config.Config
}

func newLoginEnv(t *testing.T) *loginEnv {
	t.Helper()
	e := newEnv(t)
	cfg := &config.Config{App: config.AppConfig{RegistrationOpen: true},
		JWT: config.JWTConfig{Secret: "test-secret", ExpirationTime: time.Hour}}
	a := auth.NewService(auth.NewStore(e.pool), cfg)
	e.svc.WithAuth(a)
	return &loginEnv{env: e, auth: a, cfg: cfg}
}

// A Telegram-made account has no password: its Telegram is its only way in until it sets one.
func TestTelegramOnlyWayIn(t *testing.T) {
	o := newOIDCEnv(t)
	ctx := context.Background()
	tgID := time.Now().UnixNano() % 1e10
	ext := strconv.FormatInt(tgID, 10)
	token := func(iatBack int64) string {
		return o.issuer.token(t, ext, func(c jwt.MapClaims) { c["iat"] = o.issuer.now.Unix() - iatBack })
	}

	code, out := o.tokenLogin(t, token(0))
	if code != http.StatusCreated {
		t.Fatalf("signup = %d", code)
	}
	me := out.Auth.User
	t.Cleanup(func() { _, _ = o.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, me.ID) })

	if _, err := o.auth.Login(ctx, auth.LoginRequest{Identifier: me.Username, Password: "anything-123"}); err == nil {
		t.Errorf("a passwordless account logged in with a password")
	}
	if _, err := o.svc.Unlink(ctx, me.ID, ProviderTelegram); !errors.Is(err, shared.ErrLastLoginMethod) {
		t.Errorf("unlink of the only way in = %v", err)
	}
	if _, err := o.svc.TelegramUnlink(ctx, tgID); !errors.Is(err, shared.ErrLastLoginMethod) {
		t.Errorf("/unlink of the only way in = %v", err)
	}
	other := testdb.User(t, o.pool)
	if _, err := o.svc.LinkTelegram(ctx, other, ProviderTelegram, token(1)); !errors.Is(err, shared.ErrLastLoginMethod) {
		t.Errorf("moving the only way in = %v", err)
	}

	if err := o.auth.ChangePassword(ctx, me.ID, auth.ChangePasswordRequest{NewPassword: "new-password-1"}); err != nil {
		t.Fatalf("set password = %v", err)
	}
	if _, err := o.svc.Unlink(ctx, me.ID, ProviderTelegram); err != nil {
		t.Errorf("unlink with a password = %v", err)
	}
	if _, err := o.auth.Login(ctx, auth.LoginRequest{Identifier: me.Username, Password: "new-password-1"}); err != nil {
		t.Errorf("password login after setting one = %v", err)
	}
}
