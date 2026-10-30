package users

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/user_pack_permissions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

type ufix struct {
	pool *pgxpool.Pool
	uid  uuid.UUID
	r    *gin.Engine
}

func newFix(t *testing.T) *ufix {
	t.Helper()
	pool := testdb.Pool(t)
	uid := testdb.User(t, pool)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO user_preferences (user_id, preferences) VALUES ($1, '{"timezone":"Asia/Bangkok"}')`, uid); err != nil {
		t.Fatalf("seed preferences: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM user_preferences WHERE user_id = $1`, uid) })
	svc := NewService(NewStore(pool))
	svc.WithFeeCategoryCheck(func(ctx context.Context, userID, categoryID uuid.UUID) error {
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM categories WHERE id = $1 AND user_id = $2 AND type = 'expense'`, categoryID, userID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return errors.New("not an expense category")
		}
		return nil
	})
	h := NewHandler(svc, nil)
	packs := user_pack_permissions.NewHandler(user_pack_permissions.NewStore(pool))
	r := apitest.Engine(uid, func(g *gin.RouterGroup) {
		g.GET("/users/me", h.GetMe)
		g.PUT("/users/me", h.UpdateMe)
		g.DELETE("/avatars/:id", h.DeleteAvatar)
		g.POST("/users/me/deactivate", h.Deactivate)
		g.POST("/users/me/reactivate", h.Reactivate)
		g.GET("/users/me/packs", packs.ListMyPacks)
	})
	return &ufix{pool: pool, uid: uid, r: r}
}

func (f *ufix) do(t *testing.T, method, path string, body any, op string, want int) map[string]any {
	t.Helper()
	status, raw := apitest.Do(t, f.r, method, path, body)
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

func TestGetMe(t *testing.T) {
	f := newFix(t)
	out := f.do(t, "GET", "/api/v1/users/me", nil, "getMe", http.StatusOK)
	if out["id"] != f.uid.String() || out["currency"] != "THB" || out["status"] != "active" {
		t.Errorf("me = %v", out)
	}
	prefs := out["preferences"].(map[string]any)
	if len(prefs) != 10 || prefs["timezone"] != "Asia/Bangkok" || prefs["theme_mode"] != nil ||
		prefs["language"] != nil || prefs["fee_category_id"] != nil || prefs["money_style"] != nil ||
		prefs["money_decimals"] != nil || prefs["font_th"] != nil {
		t.Errorf("preferences = %v (all ten keys must be present)", prefs)
	}
	if pp, ok := prefs["page_prefs"].(map[string]any); !ok || len(pp) != 0 {
		t.Errorf("page_prefs = %v (always an object)", prefs["page_prefs"])
	}
	if out["auto_sync_events"] != true || out["auto_sync_debts"] != true || out["fee_category"] != nil {
		t.Errorf("settings = %v / %v / %v", out["auto_sync_events"], out["auto_sync_debts"], out["fee_category"])
	}
	if sum := out["notification_summary"].(map[string]any); sum["on"] != sum["total"] || sum["total"].(float64) < 1 {
		t.Errorf("notification_summary = %v", sum)
	}
	if out["email_verified_at"] != nil || out["icon_code"] != nil {
		t.Errorf("nullable keys = %v", out)
	}
}

func TestUpdateMe(t *testing.T) {
	f := newFix(t)
	cat := testdb.Category(t, f.pool, f.uid, "Fees")
	email := "upd_" + f.uid.String() + "@test.local"
	body := map[string]any{
		"display_name": "New name",
		"email":        email,
		"preferences": map[string]any{
			"theme_mode": "dark", "theme_id": "mint", "language": "en",
			"font_th": "IBM Plex Sans Thai", "font_en": "Inter", "fee_category_id": cat.String(),
		},
		"currency": "usd", // relabel (no conversion), upper-cased
	}
	out := f.do(t, "PUT", "/api/v1/users/me", body, "updateMe", http.StatusOK)
	if out["display_name"] != "New name" || out["email"] != email {
		t.Errorf("updated = %v", out)
	}
	if out["currency"] != "USD" {
		t.Errorf("currency relabel, got %v", out["currency"])
	}
	prefs := out["preferences"].(map[string]any)
	if prefs["theme_mode"] != "dark" || prefs["theme_id"] != "mint" || prefs["language"] != "en" ||
		prefs["font_th"] != "IBM Plex Sans Thai" || prefs["font_en"] != "Inter" ||
		prefs["fee_category_id"] != cat.String() || prefs["timezone"] != "Asia/Bangkok" {
		t.Errorf("preferences = %v", prefs)
	}
	if fc, _ := out["fee_category"].(map[string]any); fc == nil || fc["id"] != cat.String() || fc["name"] != "Fees" {
		t.Errorf("fee_category = %v", out["fee_category"])
	}
	var cur string
	if err := f.pool.QueryRow(context.Background(), `SELECT currency FROM users WHERE id = $1`, f.uid).Scan(&cur); err != nil || cur != "USD" {
		t.Errorf("stored currency = %q (%v)", cur, err)
	}

	out = f.do(t, "PUT", "/api/v1/users/me", `{"preferences":{"fee_category_id":null}}`, "updateMe", http.StatusOK)
	if out["preferences"].(map[string]any)["fee_category_id"] != nil {
		t.Errorf("fee_category_id not cleared: %v", out["preferences"])
	}

	out = f.do(t, "PUT", "/api/v1/users/me", `{"preferences":{"money_style":"parentheses_plain"}}`, "updateMe", http.StatusOK)
	if out["preferences"].(map[string]any)["money_style"] != "parentheses_plain" {
		t.Errorf("money_style = %v", out["preferences"])
	}
	f.do(t, "PUT", "/api/v1/users/me", map[string]any{"preferences": map[string]any{"money_style": "rainbow"}}, "updateMe", http.StatusBadRequest)
	out = f.do(t, "PUT", "/api/v1/users/me", `{"preferences":{"money_decimals":"never"}}`, "updateMe", http.StatusOK)
	if out["preferences"].(map[string]any)["money_decimals"] != "never" {
		t.Errorf("money_decimals = %v", out["preferences"])
	}
	f.do(t, "PUT", "/api/v1/users/me", map[string]any{"preferences": map[string]any{"money_decimals": "some"}}, "updateMe", http.StatusBadRequest)

	f.do(t, "PUT", "/api/v1/users/me", map[string]any{"display_name": ""}, "updateMe", http.StatusBadRequest)
	f.do(t, "PUT", "/api/v1/users/me", map[string]any{"preferences": map[string]any{"colour": "red"}}, "updateMe", http.StatusBadRequest)
	f.do(t, "PUT", "/api/v1/users/me", map[string]any{"preferences": map[string]any{"fee_category_id": uuid.NewString()}}, "updateMe", http.StatusBadRequest)
	f.do(t, "PUT", "/api/v1/users/me", map[string]any{"preferences": map[string]any{"theme_id": 5}}, "updateMe", http.StatusBadRequest)
	f.do(t, "PUT", "/api/v1/users/me", map[string]any{"preferences": map[string]any{"theme": "dark"}}, "updateMe", http.StatusBadRequest)
	f.do(t, "PUT", "/api/v1/users/me", map[string]any{"preferences": map[string]any{"theme_mode": "dim"}}, "updateMe", http.StatusBadRequest)
	f.do(t, "PUT", "/api/v1/users/me", map[string]any{"preferences": map[string]any{"language": "fr"}}, "updateMe", http.StatusBadRequest)
	f.do(t, "PUT", "/api/v1/users/me", map[string]any{"preferences": map[string]any{"font_en": ""}}, "updateMe", http.StatusBadRequest)
	f.do(t, "PUT", "/api/v1/users/me", map[string]any{"email": "not-an-email"}, "updateMe", http.StatusBadRequest)
}

func TestDeactivateReactivate(t *testing.T) {
	f := newFix(t)
	out := f.do(t, "POST", "/api/v1/users/me/deactivate", nil, "deactivateMe", http.StatusOK)
	if out["status"] != "deactivated" {
		t.Errorf("deactivated = %v", out["status"])
	}
	out = f.do(t, "GET", "/api/v1/users/me", nil, "getMe", http.StatusOK)
	if out["status"] != "deactivated" {
		t.Errorf("me after deactivate = %v", out["status"])
	}
	out = f.do(t, "POST", "/api/v1/users/me/reactivate", nil, "reactivateMe", http.StatusOK)
	if out["status"] != "active" {
		t.Errorf("reactivated = %v", out["status"])
	}
	f.do(t, "POST", "/api/v1/users/me/reactivate", nil, "", http.StatusBadRequest)
}

func TestListMyPacks(t *testing.T) {
	f := newFix(t)
	out := f.do(t, "GET", "/api/v1/users/me/packs", nil, "listMyPacks", http.StatusOK)
	if len(out["data"].([]any)) != 0 {
		t.Errorf("packs = %v", out)
	}
}

func TestMeSettings(t *testing.T) {
	f := newFix(t)
	acc := testdb.Account(t, f.pool, f.uid)
	out := f.do(t, "GET", "/api/v1/users/me", nil, "getMe", http.StatusOK)
	if out["default_account"] != nil || out["auto_sync_events"] != true {
		t.Errorf("defaults = %v / %v", out["default_account"], out["auto_sync_events"])
	}
	out = f.do(t, "PUT", "/api/v1/users/me", map[string]any{"default_account_id": acc, "auto_sync_events": false, "auto_sync_debts": false}, "updateMe", http.StatusOK)
	da, _ := out["default_account"].(map[string]any)
	if da == nil || da["id"] != acc.String() || out["auto_sync_events"] != false || out["auto_sync_debts"] != false {
		t.Errorf("after set = %v", out)
	}
	var ev, dbt bool
	if err := f.pool.QueryRow(context.Background(), `SELECT auto_sync_events, auto_sync_debts FROM user_notification_settings
		WHERE user_id = $1`, f.uid).Scan(&ev, &dbt); err != nil || ev || dbt {
		t.Errorf("stored switches = %v / %v (%v): one DB value each", ev, dbt, err)
	}
	out = f.do(t, "PUT", "/api/v1/users/me", map[string]any{"auto_sync_debts": true}, "updateMe", http.StatusOK)
	if out["auto_sync_events"] != false || out["auto_sync_debts"] != true {
		t.Errorf("partial switch = %v / %v", out["auto_sync_events"], out["auto_sync_debts"])
	}
	// auto-accept (owner 2026-10-28): events on, debts off by default
	if out["auto_accept_events"] != true || out["auto_accept_debts"] != false {
		t.Errorf("auto-accept defaults = %v / %v", out["auto_accept_events"], out["auto_accept_debts"])
	}
	out = f.do(t, "PUT", "/api/v1/users/me", map[string]any{"auto_accept_events": false, "auto_accept_debts": true}, "updateMe", http.StatusOK)
	if out["auto_accept_events"] != false || out["auto_accept_debts"] != true || out["auto_sync_debts"] != true {
		t.Errorf("auto-accept after set = %v / %v", out["auto_accept_events"], out["auto_accept_debts"])
	}
	other := testdb.User(t, f.pool)
	foreign := testdb.Account(t, f.pool, other)
	f.do(t, "PUT", "/api/v1/users/me", map[string]any{"default_account_id": foreign}, "", http.StatusBadRequest)
	out = f.do(t, "PUT", "/api/v1/users/me", map[string]any{"display_name": "X"}, "updateMe", http.StatusOK)
	if out["default_account"] == nil {
		t.Errorf("an absent key cleared the default wallet")
	}
	out = f.do(t, "PUT", "/api/v1/users/me", `{"default_account_id":null}`, "updateMe", http.StatusOK)
	if out["default_account"] != nil || out["auto_sync_debts"] != true {
		t.Errorf("after clear = %v / %v", out["default_account"], out["auto_sync_debts"])
	}
}

// P6: page_prefs merged per page; null resets a page / every page; unknown pages and big entries 400.
func TestPagePrefs(t *testing.T) {
	f := newFix(t)
	pages := func(out map[string]any) map[string]any {
		return out["preferences"].(map[string]any)["page_prefs"].(map[string]any)
	}
	out := f.do(t, "PUT", "/api/v1/users/me", map[string]any{"preferences": map[string]any{"page_prefs": map[string]any{
		"budgets":    map[string]any{"filter": "active", "sort": "near_limit"},
		"categories": map[string]any{"collapsed": []string{"a", "b"}, "color": nil},
	}}}, "updateMe", http.StatusOK)
	if p := pages(out); len(p) != 2 || p["budgets"].(map[string]any)["sort"] != "near_limit" {
		t.Errorf("page_prefs = %v", p)
	}
	if c := pages(out)["categories"].(map[string]any); len(c) != 2 || c["color"] != nil {
		t.Errorf("an inner null must stay: %v", c)
	}
	// one page replaced whole, one reset, the rest kept; other preferences untouched
	out = f.do(t, "PUT", "/api/v1/users/me", map[string]any{"preferences": map[string]any{
		"theme_mode": "light",
		"page_prefs": map[string]any{"budgets": map[string]any{"sort": "name"}, "categories": nil, "wallets": map[string]any{"sort": "custom"}},
	}}, "updateMe", http.StatusOK)
	p := pages(out)
	if len(p) != 2 || len(p["budgets"].(map[string]any)) != 1 || p["wallets"] == nil || p["categories"] != nil {
		t.Errorf("merged page_prefs = %v", p)
	}
	if out["preferences"].(map[string]any)["theme_mode"] != "light" {
		t.Errorf("theme_mode = %v", out["preferences"])
	}
	out = f.do(t, "PUT", "/api/v1/users/me", `{"preferences":{"page_prefs":null}}`, "updateMe", http.StatusOK)
	if p := pages(out); len(p) != 0 {
		t.Errorf("reset all = %v", p)
	}
	if out["preferences"].(map[string]any)["theme_mode"] != "light" {
		t.Errorf("reset all touched theme_mode: %v", out["preferences"])
	}
	// 0.4.2-1: the transactions list (sort + filters) and the notification inbox (filter).
	out = f.do(t, "PUT", "/api/v1/users/me", map[string]any{"preferences": map[string]any{"page_prefs": map[string]any{
		"transactions":  map[string]any{"sort": "amount_desc", "types": []string{"expense"}, "wallets": []string{uuid.NewString(), "none"}},
		"notifications": map[string]any{"filter": "unread"},
	}}}, "updateMe", http.StatusOK)
	if p := pages(out); len(p) != 2 || p["transactions"].(map[string]any)["sort"] != "amount_desc" ||
		p["notifications"].(map[string]any)["filter"] != "unread" {
		t.Errorf("transactions / notifications page_prefs = %v", p)
	}
	big := make([]string, 300)
	for i := range big {
		big[i] = uuid.NewString()
	}
	for _, bad := range []any{
		map[string]any{"home": map[string]any{"sort": "x"}},
		map[string]any{"dashboard": map[string]any{"sort": "x"}},
		map[string]any{"transactions": "date_desc"},
		map[string]any{"transactions": map[string]any{"wallets": big}},
		map[string]any{"budgets": "name"},
		map[string]any{"budgets": map[string]any{"collapsed": big}},
		"all",
	} {
		f.do(t, "PUT", "/api/v1/users/me", map[string]any{"preferences": map[string]any{"page_prefs": bad}}, "", http.StatusBadRequest)
	}
}

// 0.4.2 1b: avatar + avatar_settings on /users/me, DELETE /avatars/{source}.
func TestAvatarSettings(t *testing.T) {
	f := newFix(t)
	ctx := context.Background()
	avatar := func(out map[string]any) map[string]any { return out["avatar"].(map[string]any) }

	out := f.do(t, "GET", "/api/v1/users/me", nil, "getMe", http.StatusOK)
	if a := avatar(out); a["kind"] != "initials" || a["initials"] != "TU" || a["photo_url"] != nil {
		t.Errorf("fresh avatar = %v", a)
	}
	if s := out["avatar_settings"].(map[string]any); s["source"] != "icon" || len(s["colors"].([]any)) != 0 || s["border"] != nil {
		t.Errorf("fresh settings = %v", s)
	}
	if _, ok := out["icon_code"]; ok {
		t.Errorf("top-level icon_code is still sent")
	}
	if out["has_password"] != true {
		t.Errorf("has_password = %v, want true", out["has_password"])
	}
	if _, err := f.pool.Exec(ctx, `UPDATE users SET created_via = 'telegram', password_hash = NULL WHERE id = $1`, f.uid); err != nil {
		t.Fatal(err)
	}
	if out := f.do(t, "GET", "/api/v1/users/me", nil, "getMe", http.StatusOK); out["has_password"] != false {
		t.Errorf("has_password = %v, want false (1b-2 passwordless)", out["has_password"])
	}

	// the icon (PUT icon_code) → kind icon, colours from it
	out = f.do(t, "PUT", "/api/v1/users/me", `{"icon_code":{"icon":"face","iconColors":["#FFFFFF"],"bgColors":["#5AA9F0"],"borderColors":[]}}`,
		"updateMe", http.StatusOK)
	if a := avatar(out); a["kind"] != "icon" || a["colors"].([]any)[0] != "#5AA9F0" || a["colors"].([]any)[1] != "#FFFFFF" {
		t.Errorf("icon avatar = %v", a)
	}

	// a photo source I don't have → 409; bad values → 400
	f.do(t, "PUT", "/api/v1/users/me", `{"avatar_settings":{"source":"telegram","colors":[],"border":null}}`, "", http.StatusConflict)
	for _, bad := range []string{
		`{"avatar_settings":{"source":"myspace","colors":[],"border":null}}`,
		`{"avatar_settings":{"source":"icon","colors":["1","2","3","4","5","6","7","8","9"],"border":null}}`,
		`{"avatar_settings":{"source":"icon","colors":["123456789012345678901234567890123"],"border":null}}`,
		`{"avatar_settings":{"source":"icon","colors":[],"border":{"style":"","colors":[]}}}`,
		`{"avatar_settings":{"source":"icon","colors":[],"border":{"style":"solid","colors":[]}}}`,
		`{"avatar_settings":{"source":"icon","colors":[],"border":{"style":"none","colors":[]}}}`,
	} {
		f.do(t, "PUT", "/api/v1/users/me", bad, "", http.StatusBadRequest)
	}

	// unset colour slots ("" / null) are dropped on save, never stored or sent
	out = f.do(t, "PUT", "/api/v1/users/me",
		`{"avatar_settings":{"source":"icon","colors":["","#E8A33D",null," "],"border":{"style":"thin","colors":[null,"#F06292",""]}}}`,
		"updateMe", http.StatusOK)
	s := out["avatar_settings"].(map[string]any)
	if cs := s["colors"].([]any); len(cs) != 1 || cs[0] != "#E8A33D" {
		t.Errorf("saved colours = %v", cs)
	}
	if b := s["border"].(map[string]any); b["style"] != "thin" || len(b["colors"].([]any)) != 1 || b["colors"].([]any)[0] != "#F06292" {
		t.Errorf("saved border = %v", b)
	}
	var stored string
	_ = f.pool.QueryRow(ctx, `SELECT avatar_settings::text FROM users WHERE id = $1`, f.uid).Scan(&stored)
	if strings.Contains(stored, `""`) || strings.Contains(stored, "null,") || strings.Contains(stored, `" "`) {
		t.Errorf("stored avatar_settings keeps empty slots: %s", stored)
	}

	// a Telegram picture while linked → pickable; the avatar is the photo
	if _, err := f.pool.Exec(ctx, `INSERT INTO user_connections (user_id, provider, external_id) VALUES ($1, 'telegram', $2)`,
		f.uid, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	var photoID uuid.UUID
	if err := f.pool.QueryRow(ctx, `INSERT INTO user_avatars (user_id, source, image) VALUES ($1, 'telegram', '\xffd8'::bytea) RETURNING id`,
		f.uid).Scan(&photoID); err != nil {
		t.Fatal(err)
	}
	out = f.do(t, "PUT", "/api/v1/users/me",
		`{"avatar_settings":{"source":"telegram","colors":["#E8A33D",""],"border":{"style":"thick","colors":["#F06292",""]}}}`,
		"updateMe", http.StatusOK)
	a := avatar(out)
	if a["kind"] != "photo" || a["photo_url"] != "/api/v1/avatars/"+photoID.String()+"?v=1" || a["icon_code"] == nil {
		t.Errorf("photo avatar = %v", a)
	}
	if cs := a["colors"].([]any); len(cs) != 2 || cs[0] != "#E8A33D" || cs[1] != "#F06292" {
		t.Errorf("photo colours = %v", cs)
	}
	if b := a["border"].(map[string]any); b["style"] != "thick" || len(b["colors"].([]any)) != 1 {
		t.Errorf("border = %v", b)
	}

	// DELETE /avatars/telegram → the pick falls back to the icon; idempotent; unknown source → 400
	out = f.do(t, "DELETE", "/api/v1/avatars/telegram", nil, "deleteAvatarPhoto", http.StatusOK)
	if a := avatar(out); a["kind"] != "icon" || out["avatar_settings"].(map[string]any)["source"] != "icon" {
		t.Errorf("after delete = %v / %v", a, out["avatar_settings"])
	}
	f.do(t, "DELETE", "/api/v1/avatars/telegram", nil, "deleteAvatarPhoto", http.StatusOK)
	f.do(t, "DELETE", "/api/v1/avatars/myspace", nil, "", http.StatusBadRequest)
	var n int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM user_avatars WHERE user_id = $1`, f.uid).Scan(&n)
	if n != 0 {
		t.Errorf("pictures left = %d", n)
	}
}
