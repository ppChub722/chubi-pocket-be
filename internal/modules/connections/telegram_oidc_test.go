package connections

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

const testClientID = "9059620815"

// telegramIssuerFake signs id_tokens like oauth.telegram.org and serves its JWKS.
type telegramIssuerFake struct {
	key  *rsa.PrivateKey
	jwks *httptest.Server
	now  time.Time
}

func newTelegramIssuer(t *testing.T) *telegramIssuerFake {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &telegramIssuerFake{key: key, now: time.Unix(1793469533, 0)}
	f.jwks = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{
			{"kty": "EC", "kid": "oidc-es256-1", "crv": "P-256", "x": "AA", "y": "AA"},
			{"kty": "RSA", "kid": "oidc-1", "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())},
		}})
	}))
	t.Cleanup(f.jwks.Close)
	return f
}

func (f *telegramIssuerFake) verifier() *IDTokenVerifier {
	v := NewIDTokenVerifier(testClientID)
	v.jwksURL = f.jwks.URL
	v.now = func() time.Time { return f.now }
	return v
}

// token — a Telegram id_token for user id; edit changes the claims before signing.
func (f *telegramIssuerFake) token(t *testing.T, id string, edit func(jwt.MapClaims)) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub": "8310127286798759291", "aud": testClientID, "iss": "https://oauth.telegram.org",
		"iat": f.now.Unix(), "exp": f.now.Unix() + 30,
		"name": "naughdy chubb", "preferred_username": "chubby_naughdy33", "id": id,
		"picture": "https://t.me/i/userpic/320/x.jpg",
	}
	if edit != nil {
		edit(claims)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "oidc-1"
	s, err := tok.SignedString(f.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestIDTokenVerify(t *testing.T) {
	f := newTelegramIssuer(t)
	ctx := context.Background()

	v := f.verifier()
	who, err := v.Verify(ctx, f.token(t, "6634807219", nil))
	if err != nil {
		t.Fatalf("good token: %v", err)
	}
	if who.ID != "6634807219" || who.Name != "naughdy chubb" || who.Username != "chubby_naughdy33" {
		t.Errorf("identity = %+v", who)
	}

	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	forged := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"aud": testClientID,
		"iss": "https://oauth.telegram.org", "iat": f.now.Unix(), "exp": f.now.Unix() + 30, "id": "1"})
	forged.Header["kid"] = "oidc-1"
	forgedRaw, _ := forged.SignedString(otherKey)
	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"aud": testClientID,
		"iss": "https://oauth.telegram.org", "iat": f.now.Unix(), "exp": f.now.Unix() + 30, "id": "1"})
	hsRaw, _ := hs.SignedString([]byte("secret"))

	bad := map[string]string{
		"other app":      f.token(t, "1", func(c jwt.MapClaims) { c["aud"] = "123" }),
		"other issuer":   f.token(t, "1", func(c jwt.MapClaims) { c["iss"] = "https://evil.example" }),
		"expired":        f.token(t, "1", func(c jwt.MapClaims) { c["exp"] = f.now.Unix() - 120 }),
		"no exp":         f.token(t, "1", func(c jwt.MapClaims) { delete(c, "exp") }),
		"no id":          f.token(t, "", nil),
		"id not numeric": f.token(t, "12a", nil),
		"forged":         forgedRaw,
		"hs256":          hsRaw,
		"garbage":        "a.b.c",
	}
	for name, raw := range bad {
		if _, err := f.verifier().Verify(ctx, raw); !errors.Is(err, ErrIDTokenInvalid) {
			t.Errorf("%s: err = %v, want ErrIDTokenInvalid", name, err)
		}
	}

	again := f.token(t, "42", func(c jwt.MapClaims) { c["iat"] = f.now.Unix() - 1 })
	if _, err := v.Verify(ctx, again); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if _, err := v.Verify(ctx, again); !errors.Is(err, ErrIDTokenInvalid) {
		t.Errorf("second use: err = %v, want ErrIDTokenInvalid", err)
	}
}

// oidcEnv — the login env with OIDC on and the welcome DMs recorded.
type oidcEnv struct {
	*loginEnv
	issuer  *telegramIssuerFake
	welcome []int64
}

func newOIDCEnv(t *testing.T) *oidcEnv {
	t.Helper()
	o := &oidcEnv{loginEnv: newLoginEnv(t), issuer: newTelegramIssuer(t)}
	o.svc.WithTelegramLogin(o.issuer.verifier(), func(_ context.Context, chatID int64) {
		o.welcome = append(o.welcome, chatID)
	})
	return o
}

func (o *oidcEnv) tokenLogin(t *testing.T, raw string) (int, TokenLogin) {
	t.Helper()
	h := NewHandler(o.svc)
	r := apitest.Engine(uuid.Nil, func(g *gin.RouterGroup) { g.POST("/auth/telegram/token", h.LoginToken) })
	code, body := call(t, r, http.MethodPost, "/auth/telegram/token", map[string]string{"id_token": raw})
	apitest.Conform(t, "telegramTokenLogin", code, body)
	if code >= 300 {
		return code, TokenLogin{}
	}
	return code, decode[TokenLogin](t, body)
}

func TestTelegramTokenSignupThenLogin(t *testing.T) {
	o := newOIDCEnv(t)
	ctx := context.Background()
	tgID := strconv.FormatInt(time.Now().UnixNano()%1e10, 10)

	code, out := o.tokenLogin(t, o.issuer.token(t, tgID, func(c jwt.MapClaims) {
		c["preferred_username"] = "Nong_" + tgID
	}))
	if code != http.StatusCreated || out.Status != "signup" || out.Auth == nil {
		t.Fatalf("signup = %d %+v", code, out)
	}
	userID, _, linked, err := o.svc.store.byExternal(ctx, ProviderTelegram, tgID)
	if err != nil || !linked || userID != out.Auth.User.ID {
		t.Fatalf("link = %v %v %v", userID, linked, err)
	}
	var username, display, currency, via string
	var noPassword bool
	if err := o.pool.QueryRow(ctx, `SELECT username, display_name, currency, created_via, password_hash IS NULL
		FROM users WHERE id = $1`, userID).Scan(&username, &display, &currency, &via, &noPassword); err != nil {
		t.Fatal(err)
	}
	if username != "nong_"+tgID || display != "naughdy chubb" || currency != "THB" || via != "telegram" || !noPassword {
		t.Errorf("user = %s %s %s %s nopw=%v", username, display, currency, via, noPassword)
	}
	links, _ := o.svc.store.links(ctx, userID)
	if l := links[ProviderTelegram]; l.Account == nil || *l.Account != "@Nong_"+tgID || !l.Permissions[PermChat] {
		t.Errorf("link row = %+v", l)
	}
	if len(o.welcome) != 1 || strconv.FormatInt(o.welcome[0], 10) != tgID {
		t.Errorf("welcome = %v, want [%s]", o.welcome, tgID)
	}

	code, again := o.tokenLogin(t, o.issuer.token(t, tgID, func(c jwt.MapClaims) { c["iat"] = o.issuer.now.Unix() - 1 }))
	if code != http.StatusOK || again.Status != "login" || again.Auth.User.ID != out.Auth.User.ID {
		t.Errorf("login = %d %+v", code, again)
	}
	if len(o.welcome) != 1 {
		t.Errorf("a plain login sent a welcome: %v", o.welcome)
	}

	if code, _ := o.tokenLogin(t, "a.b.c"); code != http.StatusUnauthorized {
		t.Errorf("bad token = %d, want 401", code)
	}
}

func TestTelegramTokenSignupUsernames(t *testing.T) {
	o := newOIDCEnv(t)
	ctx := context.Background()
	base := time.Now().UnixNano() % 1e9
	taken := testdb.User(t, o.pool)
	var takenName string
	if err := o.pool.QueryRow(ctx, `SELECT username FROM users WHERE id = $1`, taken).Scan(&takenName); err != nil {
		t.Fatal(err)
	}

	usernameOf := func(id string, edit func(jwt.MapClaims)) string {
		t.Helper()
		code, out := o.tokenLogin(t, o.issuer.token(t, id, edit))
		if code != http.StatusCreated {
			t.Fatalf("signup %s = %d", id, code)
		}
		return out.Auth.User.Username
	}
	if got := usernameOf(strconv.FormatInt(base+1, 10), func(c jwt.MapClaims) { c["preferred_username"] = takenName }); len(got) <= len(takenName) || got[:len(takenName)] != takenName {
		t.Errorf("taken username → %q, want %s + digits", got, takenName)
	}
	if got := usernameOf(strconv.FormatInt(base+2, 10), func(c jwt.MapClaims) {
		delete(c, "preferred_username")
		c["name"] = "Pond Poompich"
	}); got[:len("pond_poompich")] != "pond_poompich" {
		t.Errorf("no username → %q, want pond_poompich…", got)
	}
}

func TestTelegramTokenRegistrationClosed(t *testing.T) {
	o := newOIDCEnv(t)
	o.cfg.App.RegistrationOpen = false
	if code, _ := o.tokenLogin(t, o.issuer.token(t, strconv.FormatInt(time.Now().UnixNano()%1e10, 10), nil)); code != http.StatusForbidden {
		t.Errorf("closed = %d, want 403", code)
	}
}

func TestLinkTelegramWithToken(t *testing.T) {
	o := newOIDCEnv(t)
	ctx := context.Background()
	me := testdb.User(t, o.pool)
	h := NewHandler(o.svc)
	r := apitest.Engine(me, func(g *gin.RouterGroup) { g.POST("/connections/:provider", h.Link) })
	tgID := strconv.FormatInt(time.Now().UnixNano()%1e10, 10)

	code, body := call(t, r, http.MethodPost, "/connections/telegram", map[string]string{"id_token": o.issuer.token(t, tgID, nil)})
	apitest.Conform(t, "linkConnection", code, body)
	c := decode[Connection](t, body)
	if code != http.StatusOK || !c.Linked || c.Account == nil || *c.Account != "@chubby_naughdy33" ||
		c.BotURL == nil || *c.BotURL != "https://t.me/chubiPocket_bot" {
		t.Fatalf("link = %d %+v", code, c)
	}
	if owner, _, linked, _ := o.svc.store.byExternal(ctx, ProviderTelegram, tgID); !linked || owner != me {
		t.Errorf("owner = %v linked %v", owner, linked)
	}
	if len(o.welcome) != 1 {
		t.Errorf("welcome = %v, want one", o.welcome)
	}

	again := o.issuer.token(t, tgID, func(c jwt.MapClaims) { c["iat"] = o.issuer.now.Unix() - 1 })
	if code, _ := call(t, r, http.MethodPost, "/connections/telegram", map[string]string{"id_token": again}); code != http.StatusOK {
		t.Errorf("relink = %d", code)
	}
	if len(o.welcome) != 1 {
		t.Errorf("relinking the same Telegram sent another welcome: %v", o.welcome)
	}

	if code, _ := call(t, r, http.MethodPost, "/connections/line", map[string]string{"id_token": "x"}); code != http.StatusConflict {
		t.Errorf("line = %d, want 409", code)
	}
}
