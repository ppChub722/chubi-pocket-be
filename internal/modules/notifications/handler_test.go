package notifications

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

type nfix struct {
	pool *pgxpool.Pool
	uid  uuid.UUID
	r    *gin.Engine
}

func newFix(t *testing.T) *nfix {
	t.Helper()
	pool := testdb.Pool(t)
	uid := testdb.User(t, pool)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO user_notification_settings (user_id) VALUES ($1)`, uid); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	h := NewHandler(NewService(NewStore(pool)))
	r := apitest.Engine(uid, func(g *gin.RouterGroup) {
		g.GET("/notifications", h.List)
		g.GET("/notifications/unread-count", h.UnreadCount)
		g.POST("/notifications/read-all", h.ReadAll)
		g.GET("/notifications/settings", h.GetSettings)
		g.PUT("/notifications/settings", h.UpdateSettings)
		g.POST("/notifications/:id/read", h.MarkRead)
		g.POST("/notifications/:id/actioned", h.MarkActioned)
		g.POST("/notifications/:id/dismiss", h.MarkDismissed)
		g.POST("/notifications/:id/restore", h.Restore)
		g.DELETE("/notifications/:id", h.Delete)
	})
	return &nfix{pool: pool, uid: uid, r: r}
}

func (f *nfix) add(t *testing.T, typ string, actor *uuid.UUID, payload string, age time.Duration) uuid.UUID {
	t.Helper()
	id, _ := uuid.NewV7()
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO notifications (id, recipient_user_id, type, actor_user_id, payload, deep_link, created_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, '/x', NOW() - $6::interval)`,
		id, f.uid, typ, actor, payload, age.String()); err != nil {
		t.Fatalf("insert notification: %v", err)
	}
	return id
}

func (f *nfix) do(t *testing.T, method, path string, body any, op string, want int) map[string]any {
	t.Helper()
	status, raw := apitest.Do(t, f.r, method, path, body)
	if status != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, status, want, raw)
	}
	apitest.Conform(t, op, status, raw)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func num(m map[string]any, path ...string) float64 {
	var cur any = m
	for _, p := range path {
		cur = cur.(map[string]any)[p]
	}
	return cur.(float64)
}

func TestListNotifications(t *testing.T) {
	f := newFix(t)
	actor := testdb.User(t, f.pool)
	oldest := f.add(t, TypeProjectAdded, nil, `{"project_name":"Trip"}`, 3*time.Hour)
	mid := f.add(t, TypeContactLinkRequest, &actor, `{"contact_id":"`+uuid.NewString()+`"}`, 2*time.Hour)
	newest := f.add(t, TypeSplitCreated, &actor, `{"amount":10,"superseded":true}`, time.Hour)

	out := f.do(t, "GET", "/api/v1/notifications", nil, "listNotifications", http.StatusOK)
	data := out["data"].([]any)
	if len(data) != 3 || num(out, "unread_count") != 3 || num(out, "pagination", "total") != 3 {
		t.Fatalf("list = %v", out)
	}
	gotIDs := []string{}
	for _, d := range data {
		gotIDs = append(gotIDs, d.(map[string]any)["id"].(string))
	}
	if gotIDs[0] != newest.String() || gotIDs[1] != mid.String() || gotIDs[2] != oldest.String() {
		t.Errorf("order = %v", gotIDs)
	}
	first := data[0].(map[string]any)
	if first["awaits_answer"] != false { // superseded split_created
		t.Errorf("superseded split_created awaits_answer = %v", first["awaits_answer"])
	}
	if first["state"] != "unread" || first["actor"].(map[string]any)["user_id"] != actor.String() {
		t.Errorf("first = %v", first)
	}
	second := data[1].(map[string]any)
	if second["awaits_answer"] != true {
		t.Errorf("link request awaits_answer = %v", second["awaits_answer"])
	}
	third := data[2].(map[string]any)
	if third["actor"] != nil || third["awaits_answer"] != false {
		t.Errorf("project_added = %v", third)
	}

	if _, err := f.pool.Exec(context.Background(),
		`UPDATE notifications SET read_at = NOW() WHERE id = $1`, oldest); err != nil {
		t.Fatal(err)
	}
	out = f.do(t, "GET", "/api/v1/notifications?read=false", nil, "listNotifications", http.StatusOK)
	if len(out["data"].([]any)) != 2 || num(out, "unread_count") != 2 {
		t.Errorf("read=false = %v", out)
	}
	out = f.do(t, "GET", "/api/v1/notifications?type=project_added&read=all", nil, "listNotifications", http.StatusOK)
	if len(out["data"].([]any)) != 1 {
		t.Errorf("type filter = %v", out)
	}
	today := time.Now().UTC().Format("2006-01-02")
	out = f.do(t, "GET", "/api/v1/notifications?to="+today+"&from="+today, nil, "listNotifications", http.StatusOK)
	if num(out, "pagination", "total") < 0 {
		t.Errorf("date filter = %v", out)
	}
	f.do(t, "GET", "/api/v1/notifications?read=maybe", nil, "listNotifications", http.StatusBadRequest)
}

func TestListNotificationsEmpty(t *testing.T) {
	f := newFix(t)
	out := f.do(t, "GET", "/api/v1/notifications", nil, "listNotifications", http.StatusOK)
	if len(out["data"].([]any)) != 0 || num(out, "unread_count") != 0 {
		t.Errorf("empty = %v", out)
	}
}

func TestUnreadCount(t *testing.T) {
	f := newFix(t)
	f.add(t, TypeProjectAdded, nil, `{}`, time.Hour)
	f.add(t, TypeProjectAdded, nil, `{}`, time.Minute)
	out := f.do(t, "GET", "/api/v1/notifications/unread-count", nil, "getUnreadCount", http.StatusOK)
	if num(out, "unread_count") != 2 {
		t.Errorf("unread = %v", out)
	}
}

func TestMarkReadActionedDismiss(t *testing.T) {
	f := newFix(t)
	a := f.add(t, TypeProjectTxRecordedForYou, nil, `{}`, time.Hour)
	b := f.add(t, TypeProjectTxRecordedForYou, nil, `{}`, 2*time.Hour)
	c := f.add(t, TypeAccountInvite, nil, `{}`, 3*time.Hour)

	out := f.do(t, "POST", "/api/v1/notifications/"+a.String()+"/read", nil, "markNotificationRead", http.StatusOK)
	n := out["notification"].(map[string]any)
	if n["state"] != "read" || n["read_at"] == nil || n["awaits_answer"] != true {
		t.Errorf("read = %v", n)
	}
	if num(out, "badges", "unread_notifications") != 2 {
		t.Errorf("badges after read = %v", out["badges"])
	}
	f.do(t, "POST", "/api/v1/notifications/"+a.String()+"/read", nil, "markNotificationRead", http.StatusOK)

	out = f.do(t, "POST", "/api/v1/notifications/"+b.String()+"/actioned", nil, "markNotificationActioned", http.StatusOK)
	n = out["notification"].(map[string]any)
	if n["state"] != "actioned" || n["awaits_answer"] != false || n["read_at"] == nil || n["actioned_at"] == nil {
		t.Errorf("actioned = %v", n)
	}
	if num(out, "badges", "unread_notifications") != 1 {
		t.Errorf("badges after actioned = %v", out["badges"])
	}
	f.status(t, "POST", "/api/v1/notifications/"+b.String()+"/dismiss", nil, http.StatusConflict, "NOTIFICATION_STATE_CONFLICT")

	out = f.do(t, "POST", "/api/v1/notifications/"+c.String()+"/dismiss", nil, "dismissNotification", http.StatusOK)
	n = out["notification"].(map[string]any)
	if n["state"] != "dismissed" || n["awaits_answer"] != false || n["dismissed_at"] == nil {
		t.Errorf("dismissed = %v", n)
	}
	if num(out, "badges", "unread_notifications") != 0 {
		t.Errorf("badges after dismiss = %v", out["badges"])
	}

	out = f.do(t, "GET", "/api/v1/notifications?read=all", nil, "listNotifications", http.StatusOK)
	if len(out["data"].([]any)) != 2 {
		t.Errorf("default list after dismiss = %v", out["data"])
	}
	out = f.do(t, "GET", "/api/v1/notifications?dismissed=true&read=all", nil, "listNotifications", http.StatusOK)
	if d := out["data"].([]any); len(d) != 1 || d[0].(map[string]any)["id"] != c.String() {
		t.Errorf("dismissed list = %v", out["data"])
	}
	out = f.do(t, "GET", "/api/v1/notifications?dismissed=all&read=all", nil, "listNotifications", http.StatusOK)
	if len(out["data"].([]any)) != 3 {
		t.Errorf("dismissed=all = %v", out["data"])
	}
	out = f.do(t, "POST", "/api/v1/notifications/"+c.String()+"/restore", nil, "restoreNotification", http.StatusOK)
	if n = out["notification"].(map[string]any); n["dismissed_at"] != nil || n["state"] != "read" {
		t.Errorf("restored = %v", n)
	}
	f.do(t, "POST", "/api/v1/notifications/"+c.String()+"/restore", nil, "restoreNotification", http.StatusOK)
	f.do(t, "POST", "/api/v1/notifications/"+uuid.NewString()+"/restore", nil, "restoreNotification", http.StatusNotFound)
	out = f.do(t, "GET", "/api/v1/notifications?read=all", nil, "listNotifications", http.StatusOK)
	if len(out["data"].([]any)) != 3 {
		t.Errorf("list after restore = %v", out["data"])
	}

	missing := uuid.NewString()
	f.do(t, "POST", "/api/v1/notifications/"+missing+"/read", nil, "markNotificationRead", http.StatusNotFound)
	f.do(t, "POST", "/api/v1/notifications/"+missing+"/actioned", nil, "markNotificationActioned", http.StatusNotFound)
	f.do(t, "POST", "/api/v1/notifications/"+missing+"/dismiss", nil, "dismissNotification", http.StatusNotFound)
	other := testdb.User(t, f.pool)
	oid, _ := uuid.NewV7()
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO notifications (id, recipient_user_id, type, payload) VALUES ($1, $2, 'project_added', '{}')`, oid, other); err != nil {
		t.Fatal(err)
	}
	f.do(t, "POST", "/api/v1/notifications/"+oid.String()+"/read", nil, "markNotificationRead", http.StatusNotFound)
}

func TestReadAllAndDelete(t *testing.T) {
	f := newFix(t)
	a := f.add(t, TypeProjectAdded, nil, `{}`, time.Hour)
	f.add(t, TypeProjectAdded, nil, `{}`, 2*time.Hour)
	f.add(t, TypeProjectAdded, nil, `{}`, 3*time.Hour)
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO pending_transactions (id, user_id, source)
		VALUES (gen_random_uuid(), $1, 'text')`, f.uid); err != nil {
		t.Fatalf("seed draft: %v", err)
	}

	out := f.do(t, "DELETE", "/api/v1/notifications/"+a.String(), nil, "deleteNotification", http.StatusOK)
	if out["id"] != a.String() || num(out, "badges", "unread_notifications") != 2 || num(out, "badges", "pending_drafts") != 1 {
		t.Errorf("delete = %v", out)
	}
	f.do(t, "DELETE", "/api/v1/notifications/"+a.String(), nil, "deleteNotification", http.StatusNotFound)

	out = f.do(t, "POST", "/api/v1/notifications/read-all", nil, "readAllNotifications", http.StatusOK)
	if num(out, "marked_read") != 2 || num(out, "badges", "unread_notifications") != 0 {
		t.Errorf("read-all = %v", out)
	}
	out = f.do(t, "POST", "/api/v1/notifications/read-all", nil, "readAllNotifications", http.StatusOK)
	if num(out, "marked_read") != 0 {
		t.Errorf("read-all again = %v", out)
	}
}

func TestNotificationSettings(t *testing.T) {
	f := newFix(t)
	acc := testdb.Account(t, f.pool, f.uid)

	out := f.do(t, "GET", "/api/v1/notifications/settings", nil, "getNotificationSettings", http.StatusOK)
	if out["default_account"] != nil || len(out["muted_types"].([]any)) != 0 {
		t.Errorf("default settings = %v", out)
	}
	if _, ok := out["user_id"]; ok {
		t.Error("user_id must not be sent")
	}

	body := map[string]any{
		"muted_types":        []string{TypeProjectAdded},
		"auto_types":         []string{TypeProjectTxRecordedForYou},
		"default_account_id": acc.String(),
	}
	apitest.ConformRequest(t, "updateNotificationSettings", body)
	out = f.do(t, "PUT", "/api/v1/notifications/settings", body, "updateNotificationSettings", http.StatusOK)
	ref := out["default_account"].(map[string]any)
	if ref["id"] != acc.String() || ref["name"] != "Test wallet" {
		t.Errorf("updated = %v", out)
	}
	if got := out["muted_types"].([]any); len(got) != 1 || got[0] != TypeProjectAdded {
		t.Errorf("muted = %v", got)
	}

	out = f.do(t, "PUT", "/api/v1/notifications/settings", map[string]any{"muted_types": []string{}}, "updateNotificationSettings", http.StatusOK)
	if out["default_account"] == nil {
		t.Error("partial update must keep default_account")
	}
	out = f.do(t, "PUT", "/api/v1/notifications/settings", `{"default_account_id": null}`, "updateNotificationSettings", http.StatusOK)
	if out["default_account"] != nil {
		t.Errorf("null must clear default_account: %v", out)
	}

	f.status(t, "PUT", "/api/v1/notifications/settings", map[string]any{"muted_types": []string{TypeAccountInvite}}, http.StatusBadRequest, "")
	f.status(t, "PUT", "/api/v1/notifications/settings", map[string]any{"muted_types": []string{"nope"}}, http.StatusBadRequest, "")
	f.status(t, "PUT", "/api/v1/notifications/settings", map[string]any{"default_account_id": uuid.NewString()}, http.StatusBadRequest, "")
	f.status(t, "PUT", "/api/v1/notifications/settings", `{"default_account_id": "x"}`, http.StatusBadRequest, "")
}

func (f *nfix) status(t *testing.T, method, path string, body any, want int, code string) {
	t.Helper()
	status, raw := apitest.Do(t, f.r, method, path, body)
	if status != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, status, want, raw)
	}
	var out struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	if code != "" && out.Error.Code != code {
		t.Errorf("code = %q, want %q", out.Error.Code, code)
	}
}

func TestInboxSummaryAndAwaitsAnswer(t *testing.T) {
	f := newFix(t)
	f.add(t, TypeProjectInvite, nil, `{}`, time.Hour)
	f.add(t, TypeRecurringEnding, nil, `{"left":2}`, 2*time.Hour)
	info := f.add(t, TypeProjectAdded, nil, `{}`, 3*time.Hour)
	hidden := f.add(t, TypeProjectAdded, nil, `{}`, 4*time.Hour)
	f.do(t, "POST", "/api/v1/notifications/"+hidden.String()+"/dismiss", nil, "dismissNotification", http.StatusOK)
	f.do(t, "POST", "/api/v1/notifications/"+info.String()+"/read", nil, "markNotificationRead", http.StatusOK)

	out := f.do(t, "GET", "/api/v1/notifications?read=all", nil, "listNotifications", http.StatusOK)
	if num(out, "summary", "needs_answer") != 2 || num(out, "summary", "unread") != 2 || num(out, "summary", "dismissed") != 1 {
		t.Errorf("summary = %v", out["summary"])
	}
	out = f.do(t, "GET", "/api/v1/notifications?type=project_added&read=all", nil, "listNotifications", http.StatusOK)
	if num(out, "summary", "needs_answer") != 2 {
		t.Errorf("filtered summary = %v", out["summary"])
	}
	for _, d := range f.do(t, "GET", "/api/v1/notifications?read=all", nil, "listNotifications", http.StatusOK)["data"].([]any) {
		n := d.(map[string]any)
		want := n["type"] == TypeProjectInvite || n["type"] == TypeRecurringEnding
		if n["awaits_answer"] != want {
			t.Errorf("%v awaits_answer = %v, want %v", n["type"], n["awaits_answer"], want)
		}
	}
}
