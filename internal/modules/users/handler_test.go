package users

// Handler tests for the Me operations of the API spec v2, driven over HTTP
// with apitest and checked against openapi.yaml.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
		`INSERT INTO user_preferences (user_id, preferences) VALUES ($1, '{"timezone":"Asia/Bangkok","theme":"system","language":"th"}')`, uid); err != nil {
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
	if len(prefs) != 4 || prefs["timezone"] != "Asia/Bangkok" || prefs["theme"] != "system" ||
		prefs["language"] != "th" || prefs["fee_category_id"] != nil {
		t.Errorf("preferences = %v (all four keys must be present)", prefs)
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
		"preferences":  map[string]any{"theme": "dark", "fee_category_id": cat.String()},
		"currency":     "USD", // not part of the request: must be ignored
	}
	out := f.do(t, "PUT", "/api/v1/users/me", body, "updateMe", http.StatusOK)
	if out["display_name"] != "New name" || out["email"] != email {
		t.Errorf("updated = %v", out)
	}
	if out["currency"] != "THB" {
		t.Errorf("currency is locked, got %v", out["currency"])
	}
	prefs := out["preferences"].(map[string]any)
	if prefs["theme"] != "dark" || prefs["fee_category_id"] != cat.String() || prefs["timezone"] != "Asia/Bangkok" {
		t.Errorf("preferences = %v", prefs)
	}
	var cur string
	if err := f.pool.QueryRow(context.Background(), `SELECT currency FROM users WHERE id = $1`, f.uid).Scan(&cur); err != nil || cur != "THB" {
		t.Errorf("stored currency = %q (%v)", cur, err)
	}

	// null clears a preference.
	out = f.do(t, "PUT", "/api/v1/users/me", `{"preferences":{"fee_category_id":null}}`, "updateMe", http.StatusOK)
	if out["preferences"].(map[string]any)["fee_category_id"] != nil {
		t.Errorf("fee_category_id not cleared: %v", out["preferences"])
	}

	// Errors.
	f.do(t, "PUT", "/api/v1/users/me", map[string]any{"display_name": ""}, "updateMe", http.StatusBadRequest)
	f.do(t, "PUT", "/api/v1/users/me", map[string]any{"preferences": map[string]any{"colour": "red"}}, "updateMe", http.StatusBadRequest)
	f.do(t, "PUT", "/api/v1/users/me", map[string]any{"preferences": map[string]any{"fee_category_id": uuid.NewString()}}, "updateMe", http.StatusBadRequest)
	f.do(t, "PUT", "/api/v1/users/me", map[string]any{"preferences": map[string]any{"theme": 5}}, "updateMe", http.StatusBadRequest)
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
	// Already active: not in the spec's response list for this operation.
	f.do(t, "POST", "/api/v1/users/me/reactivate", nil, "", http.StatusBadRequest)
}

func TestListMyPacks(t *testing.T) {
	f := newFix(t)
	out := f.do(t, "GET", "/api/v1/users/me/packs", nil, "listMyPacks", http.StatusOK)
	if len(out["data"].([]any)) != 0 {
		t.Errorf("packs = %v", out)
	}
}
