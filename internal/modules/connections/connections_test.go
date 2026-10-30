package connections

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// fakeBot stands in for the Telegram client.
type fakeBot struct {
	photo    []byte
	photoErr error
	asked    []int64
}

func (f *fakeBot) Username(context.Context) (string, error) { return "chubiPocket_bot", nil }

func (f *fakeBot) ProfilePhoto(_ context.Context, userID int64) ([]byte, error) {
	f.asked = append(f.asked, userID)
	return f.photo, f.photoErr
}

type env struct {
	pool *pgxpool.Pool
	svc  *Service
	bot  *fakeBot
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testdb.Pool(t)
	bot := &fakeBot{photo: []byte{0xFF, 0xD8, 0xFF, 0xE0, 1}}
	return &env{pool: pool, svc: NewService(NewStore(pool), bot), bot: bot}
}

func (e *env) router(uid uuid.UUID) *gin.Engine {
	h := NewHandler(e.svc)
	return apitest.Engine(uid, func(g *gin.RouterGroup) {
		g.GET("/connections", h.List)
		g.GET("/connections/:provider", h.Get)
		g.DELETE("/connections/:provider", h.Unlink)
		g.POST("/connections/:provider/link", h.StartLink)
		g.PATCH("/connections/:provider/permissions", h.UpdatePermissions)
		g.POST("/connections/:provider/photo", h.ImportPhoto)
		g.GET("/avatars/:id", h.Image)
		g.GET("/brand-logos/:file", BrandLogo)
	})
}

// call — apitest.Do on a path under /api/v1 (apitest.Engine mounts the routes there).
func call(t *testing.T, r http.Handler, method, path string, body any) (int, []byte) {
	t.Helper()
	return apitest.Do(t, r, method, "/api/v1"+path, body)
}

// codeOf takes the code out of a link URL.
func codeOf(t *testing.T, link string) string {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil || u.Host != "t.me" || u.Path != "/chubiPocket_bot" {
		t.Fatalf("link url = %q", link)
	}
	return u.Query().Get("start")
}

func decode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode: %v: %s", err, raw)
	}
	return v
}

func TestListAndLinkFlow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	me := testdb.User(t, e.pool)
	r := e.router(me)

	code, body := call(t, r, http.MethodGet, "/connections", nil)
	apitest.Conform(t, "listConnections", code, body)
	list := decode[ConnectionList](t, body)
	if list.Count != 3 || list.Data[0].Provider != "telegram" || list.Data[1].Provider != "line" || list.Data[2].Provider != "google" {
		t.Fatalf("list = %+v", list)
	}
	tg, line := list.Data[0], list.Data[1]
	if !tg.Available || tg.Linked || tg.Account != nil || tg.BrandColor != "#26A5E4" ||
		tg.LogoURL != "/api/v1/brand-logos/telegram.png?v=1" {
		t.Errorf("telegram row = %+v", tg)
	}
	if line.Available || line.Permissions[0].Available {
		t.Errorf("line must be เร็วๆ นี้: %+v", line)
	}
	if p := tg.Permissions; len(p) != 4 || p[0].Key != "chat" || !p[0].Available || p[0].On || p[1].Available {
		t.Errorf("telegram permissions = %+v", p)
	}

	// start linking
	code, body = call(t, r, http.MethodPost, "/connections/telegram/link", nil)
	apitest.Conform(t, "startConnectionLink", code, body)
	start := decode[LinkStart](t, body)
	c := codeOf(t, start.URL)
	if len(c) != 22 || !codeFormat.MatchString(c) {
		t.Errorf("code %q: want 22 URL-safe chars (128 bits)", c)
	}
	if d := time.Until(start.ExpiresAt); d < 9*time.Minute || d > 10*time.Minute+time.Second {
		t.Errorf("expires in %v", d)
	}
	var stored int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM connection_link_codes WHERE user_id = $1 AND code_hash = $2`,
		me, hashCode(c)).Scan(&stored)
	if stored != 1 {
		t.Errorf("code hash rows = %d", stored)
	}

	// the bot binds it
	name, ok, err := e.svc.TelegramBind(ctx, c, 555001, "@nong")
	if err != nil || !ok || name != "Test user" {
		t.Fatalf("bind = %q %v %v", name, ok, err)
	}
	if _, ok, _ := e.svc.TelegramBind(ctx, c, 555002, "@x"); ok {
		t.Errorf("a used code bound again")
	}
	uid, chatOn, linked, err := e.svc.TelegramUser(ctx, 555001)
	if err != nil || !linked || uid != me || !chatOn {
		t.Errorf("chat user = %v %v %v %v", uid, chatOn, linked, err)
	}

	code, body = call(t, r, http.MethodGet, "/connections/telegram", nil)
	apitest.Conform(t, "getConnection", code, body)
	got := decode[Connection](t, body)
	if !got.Linked || got.Account == nil || *got.Account != "@nong" || got.LinkedAt == nil || !got.Permissions[0].On {
		t.Errorf("linked row = %+v", got)
	}

	// permissions: chat off, then the bad cases
	code, body = call(t, r, http.MethodPatch, "/connections/telegram/permissions", map[string]bool{"chat": false})
	apitest.Conform(t, "updateConnectionPermissions", code, body)
	if got := decode[Connection](t, body); got.Permissions[0].On {
		t.Errorf("chat still on")
	}
	if _, on, _, _ := e.svc.TelegramUser(ctx, 555001); on {
		t.Errorf("bot still sees chat on")
	}
	for _, tc := range []struct {
		body any
		code int
		err  string
	}{
		{map[string]bool{"nope": true}, 400, "VALIDATION_ERROR"},
		{map[string]bool{"ask": true}, 409, "PERMISSION_UNAVAILABLE"},
		{map[string]any{"chat": "yes"}, 400, "VALIDATION_ERROR"},
		{map[string]bool{}, 400, "VALIDATION_ERROR"},
	} {
		code, body := call(t, r, http.MethodPatch, "/connections/telegram/permissions", tc.body)
		if code != tc.code || !strings.Contains(string(body), tc.err) {
			t.Errorf("permissions %v → %d %s", tc.body, code, body)
		}
	}

	// unknown provider / unavailable provider
	if code, _ := call(t, r, http.MethodGet, "/connections/myspace", nil); code != 404 {
		t.Errorf("unknown provider = %d", code)
	}
	if code, body := call(t, r, http.MethodPost, "/connections/line/link", nil); code != 409 ||
		!strings.Contains(string(body), "CONNECTION_UNAVAILABLE") {
		t.Errorf("line link = %d %s", code, body)
	}

	// unlink (also from the app)
	code, body = call(t, r, http.MethodDelete, "/connections/telegram", nil)
	apitest.Conform(t, "unlinkConnection", code, body)
	if got := decode[Connection](t, body); got.Linked || got.Account != nil {
		t.Errorf("after unlink = %+v", got)
	}
	if code, body := call(t, r, http.MethodPatch, "/connections/telegram/permissions", map[string]bool{"chat": true}); code != 409 ||
		!strings.Contains(string(body), "NOT_LINKED") {
		t.Errorf("permissions while unlinked = %d %s", code, body)
	}
	if _, _, linked, _ := e.svc.TelegramUser(ctx, 555001); linked {
		t.Errorf("chat still linked")
	}
}

func TestBindMovesAndReplaces(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := testdb.User(t, e.pool), testdb.User(t, e.pool)
	link := func(user uuid.UUID, chat int64) {
		t.Helper()
		start, err := e.svc.StartLink(ctx, user, ProviderTelegram)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok, err := e.svc.TelegramBind(ctx, codeOf(t, start.URL), chat, "@x"); err != nil || !ok {
			t.Fatalf("bind: %v %v", ok, err)
		}
	}
	link(a, 777001)
	link(b, 777001) // the chat moves from a to b
	if uid, _, ok, _ := e.svc.TelegramUser(ctx, 777001); !ok || uid != b {
		t.Errorf("chat owner = %v, want b", uid)
	}
	if c, _ := e.svc.Get(ctx, a, ProviderTelegram); c.Linked {
		t.Errorf("a still linked")
	}
	link(b, 777002) // b's Telegram is replaced
	if _, _, ok, _ := e.svc.TelegramUser(ctx, 777001); ok {
		t.Errorf("old chat still linked")
	}
	if uid, _, ok, _ := e.svc.TelegramUser(ctx, 777002); !ok || uid != b {
		t.Errorf("new chat owner = %v", uid)
	}

	// a newer link call cancels the older unused code; an expired code fails
	s1, _ := e.svc.StartLink(ctx, a, ProviderTelegram)
	s2, _ := e.svc.StartLink(ctx, a, ProviderTelegram)
	if _, ok, _ := e.svc.TelegramBind(ctx, codeOf(t, s1.URL), 777003, ""); ok {
		t.Errorf("an older code still works")
	}
	if _, err := e.pool.Exec(ctx, `UPDATE connection_link_codes SET expires_at = NOW() - INTERVAL '1 second'
		WHERE code_hash = $1`, hashCode(codeOf(t, s2.URL))); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := e.svc.TelegramBind(ctx, codeOf(t, s2.URL), 777003, ""); ok {
		t.Errorf("an expired code works")
	}
	if _, ok, err := e.svc.TelegramBind(ctx, "not a code!", 777003, ""); ok || err != nil {
		t.Errorf("a malformed code = %v %v", ok, err)
	}

	// /unlink from the chat
	if ok, err := e.svc.TelegramUnlink(ctx, 777002); !ok || err != nil {
		t.Errorf("unlink = %v %v", ok, err)
	}
	if ok, _ := e.svc.TelegramUnlink(ctx, 777002); ok {
		t.Errorf("unlink twice = true")
	}
}

func TestPhotoImportAndAvatar(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	me := testdb.User(t, e.pool)
	r := e.router(me)

	if code, body := call(t, r, http.MethodPost, "/connections/telegram/photo", nil); code != 409 ||
		!strings.Contains(string(body), "NOT_LINKED") {
		t.Errorf("import while unlinked = %d %s", code, body)
	}
	start, _ := e.svc.StartLink(ctx, me, ProviderTelegram)
	if _, ok, _ := e.svc.TelegramBind(ctx, codeOf(t, start.URL), 888001, "@nong"); !ok {
		t.Fatal("bind")
	}

	var before time.Time
	_ = e.pool.QueryRow(ctx, `SELECT updated_at FROM users WHERE id = $1`, me).Scan(&before)
	code, body := call(t, r, http.MethodPost, "/connections/telegram/photo", nil)
	apitest.Conform(t, "importConnectionPhoto", code, body)
	var after time.Time
	_ = e.pool.QueryRow(ctx, `SELECT updated_at FROM users WHERE id = $1`, me).Scan(&after)
	if !after.After(before) {
		t.Errorf("users.updated_at not bumped (linked contacts' sync would miss the new picture)")
	}
	got := decode[Connection](t, body)
	if got.Photo == nil || got.Photo.Source != "telegram" || !strings.HasSuffix(got.Photo.URL, "?v=1") {
		t.Fatalf("photo = %+v", got.Photo)
	}
	if len(e.bot.asked) != 1 || e.bot.asked[0] != 888001 {
		t.Errorf("asked Telegram for %v", e.bot.asked)
	}
	_, body = call(t, r, http.MethodPost, "/connections/telegram/photo", nil)
	if got := decode[Connection](t, body); !strings.HasSuffix(got.Photo.URL, "?v=2") {
		t.Errorf("re-import url = %s, want v=2", got.Photo.URL)
	}

	// the picture behind the url
	path := strings.TrimPrefix(strings.Split(got.Photo.URL, "?")[0], "/api/v1")
	code, img := call(t, r, http.MethodGet, path, nil)
	if code != 200 || len(img) != 5 || img[0] != 0xFF {
		t.Errorf("image = %d %v", code, img)
	}
	if code, _ := call(t, r, http.MethodGet, "/avatars/"+uuid.NewString(), nil); code != 404 {
		t.Errorf("missing image = %d", code)
	}

	// avatar_src: picked + linked → photo; unlinked → icon / initials (picture kept)
	avatar := func() shared.Avatar {
		var raw []byte
		if err := e.pool.QueryRow(ctx, `SELECT avatar_src($1, NULL)`, me).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return shared.AvatarFromSrc("Test user", raw)
	}
	if a := avatar(); a.Kind != shared.AvatarInitials {
		t.Errorf("before picking = %+v", a)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE users SET avatar_settings = '{"source":"telegram","colors":["#E8A33D"],"border":null}'
		WHERE id = $1`, me); err != nil {
		t.Fatal(err)
	}
	if a := avatar(); a.Kind != shared.AvatarPhoto || a.PhotoURL == nil || !strings.HasSuffix(*a.PhotoURL, "?v=2") ||
		a.Colors[0] != "#E8A33D" {
		t.Errorf("picked = %+v", a)
	}
	call(t, r, http.MethodDelete, "/connections/telegram", nil)
	if a := avatar(); a.Kind != shared.AvatarInitials {
		t.Errorf("after unlink = %+v", a)
	}
	_, body = call(t, r, http.MethodGet, "/connections/telegram", nil)
	if got := decode[Connection](t, body); got.Photo == nil {
		t.Errorf("the picture went with the unlink")
	}

	// no photo / Telegram error
	start, _ = e.svc.StartLink(ctx, me, ProviderTelegram)
	e.svc.TelegramBind(ctx, codeOf(t, start.URL), 888001, "@nong")
	e.bot.photo = nil
	if code, body := call(t, r, http.MethodPost, "/connections/telegram/photo", nil); code != 404 ||
		!strings.Contains(string(body), "NO_PHOTO") {
		t.Errorf("no photo = %d %s", code, body)
	}
	e.bot.photoErr = errors.New("telegram getFile: HTTP 500 https://api.telegram.org/botSECRET/x")
	code, body = call(t, r, http.MethodPost, "/connections/telegram/photo", nil)
	if code != 502 || !strings.Contains(string(body), "PROVIDER_ERROR") || strings.Contains(string(body), "SECRET") {
		t.Errorf("telegram error = %d %s", code, body)
	}
}

func TestBotOffAndLogos(t *testing.T) {
	pool := testdb.Pool(t)
	svc := NewService(NewStore(pool), nil)
	me := testdb.User(t, pool)
	list, err := svc.List(context.Background(), me)
	if err != nil || list.Data[0].Available || list.Data[0].Permissions[0].Available {
		t.Errorf("bot off: telegram row = %+v, %v", list.Data[0], err)
	}
	if _, err := svc.StartLink(context.Background(), me, ProviderTelegram); !errors.Is(err, ErrUnavailable) {
		t.Errorf("link with the bot off = %v", err)
	}

	r := apitest.Engine(me, func(g *gin.RouterGroup) { g.GET("/brand-logos/:file", BrandLogo) })
	for _, p := range []string{"telegram", "line", "google"} {
		code, b := call(t, r, http.MethodGet, "/brand-logos/"+p+".png?v=1", nil)
		if code != 200 || len(b) < 100 || string(b[1:4]) != "PNG" {
			t.Errorf("%s logo = %d (%d bytes)", p, code, len(b))
		}
	}
	for _, f := range []string{"telegram", "myspace.png", "telegram.svg"} {
		if code, _ := call(t, r, http.MethodGet, "/brand-logos/"+f, nil); code != 404 {
			t.Errorf("%s = %d, want 404", f, code)
		}
	}
}
