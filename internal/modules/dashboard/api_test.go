package dashboard

// GET /dashboard (spec v2): conformance with openapi.yaml plus the rules of
// the new blocks (top_categories refs, pending total vs items, attention).

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
	"github.com/ppChub722/chubi-pocket-be/internal/modules/accounts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/budgets"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/saving_goals"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

type dashEnv struct {
	pool *pgxpool.Pool
	uid  uuid.UUID
	acct uuid.UUID
	txs  *transactions.Service
	r    *gin.Engine
}

func newDashEnv(t *testing.T) *dashEnv {
	t.Helper()
	pool := testdb.Pool(t)
	uid := testdb.User(t, pool)
	catSvc := categories.NewService(categories.NewStore(pool))
	txSvc := transactions.NewService(transactions.NewStore(pool), catSvc)
	accSvc := accounts.NewService(accounts.NewStore(pool), txSvc, catSvc)
	pdSvc := personal_debts.NewService(personal_debts.NewStore(pool), txSvc)
	svc := NewService(NewStore(pool), txSvc, accSvc, budgets.NewService(budgets.NewStore(pool)),
		saving_goals.NewService(saving_goals.NewStore(pool)), pdSvc)
	h := NewHandler(svc)
	e := &dashEnv{pool: pool, uid: uid, acct: testdb.Account(t, pool, uid), txs: txSvc}
	e.r = apitest.Engine(uid, func(g *gin.RouterGroup) { g.GET("/dashboard", h.Get) })
	return e
}

func (e *dashEnv) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func (e *dashEnv) get(t *testing.T, query string) (int, map[string]json.RawMessage) {
	t.Helper()
	status, body := apitest.Do(t, e.r, "GET", "/api/v1/dashboard"+query, nil)
	apitest.Conform(t, "getDashboard", status, body)
	var out map[string]json.RawMessage
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, body)
	}
	return status, out
}

func (e *dashEnv) notif(t *testing.T, typ string, readAt, actionedAt, dismissedAt *time.Time, created time.Time, payload string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	e.exec(t, `INSERT INTO notifications (id, recipient_user_id, type, payload, read_at, actioned_at, dismissed_at, created_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8)`, id, e.uid, typ, payload, readAt, actionedAt, dismissedAt, created)
	return id
}

func TestDashboardConformsAndTopCategories(t *testing.T) {
	e := newDashEnv(t)
	food := testdb.Category(t, e.pool, e.uid, "Food")
	ctx := context.Background()
	for _, req := range []transactions.CreateRequest{
		{Type: transactions.TypeExpense, AccountID: &e.acct, Amount: 80, CategoryID: &food, Date: "2026-10-05"},
		{Type: transactions.TypeExpense, AccountID: &e.acct, Amount: 20, Date: "2026-10-06"}, // uncategorized
	} {
		if _, err := e.txs.Create(ctx, e.uid, req); err != nil {
			t.Fatalf("create tx: %v", err)
		}
	}
	status, d := e.get(t, "?month=2026-10")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	var top []struct {
		Category *struct {
			ID   uuid.UUID `json:"id"`
			Name string    `json:"name"`
		} `json:"category"`
		Expense float64 `json:"expense"`
		Count   int     `json:"count"`
	}
	if err := json.Unmarshal(d["top_categories"], &top); err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 || top[0].Category == nil || top[0].Category.Name != "Food" || top[0].Expense != 80 {
		t.Fatalf("top_categories: %s", d["top_categories"])
	}
	if top[1].Category != nil || top[1].Expense != 20 {
		t.Fatalf("uncategorized must be category null: %s", d["top_categories"])
	}
	var recent []map[string]any
	if err := json.Unmarshal(d["recent"], &recent); err != nil || len(recent) != 2 {
		t.Fatalf("recent: %s", d["recent"])
	}
	if _, ok := recent[0]["account"]; !ok {
		t.Fatalf("recent rows must be the full Transaction shape: %v", recent[0])
	}

	if status, _ := apitest.Do(t, e.r, "GET", "/api/v1/dashboard?month=nope", nil); status != http.StatusBadRequest {
		t.Fatalf("bad month: %d", status)
	}
}

func TestDashboardPendingBlock(t *testing.T) {
	e := newDashEnv(t)
	ps := pending.NewStore(e.pool)
	var newest uuid.UUID
	for i := 0; i < 7; i++ {
		amount := float64(i + 1)
		p, err := ps.Insert(context.Background(), e.uid, pending.SourceManual, pending.KindCreate,
			pending.Draft{Amount: &amount, AccountID: &e.acct}, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		newest = p.ID
	}
	_, d := e.get(t, "")
	var blk struct {
		Total int `json:"total"`
		Items []struct {
			ID       uuid.UUID             `json:"id"`
			Resolved pending.DraftResolved `json:"resolved"`
		} `json:"items"`
	}
	if err := json.Unmarshal(d["pending"], &blk); err != nil {
		t.Fatal(err)
	}
	if blk.Total != 7 || len(blk.Items) != 5 || blk.Items[0].ID != newest {
		t.Fatalf("pending block: total %d items %d: %s", blk.Total, len(blk.Items), d["pending"])
	}
	if blk.Items[0].Resolved.Account == nil || blk.Items[0].Resolved.Account.ID != e.acct {
		t.Fatalf("items must be resolved: %+v", blk.Items[0].Resolved)
	}
	var badges struct {
		Pending int `json:"pending_drafts"`
	}
	_ = json.Unmarshal(d["badges"], &badges)
	if badges.Pending != 7 {
		t.Fatalf("badges: %s", d["badges"])
	}
}

type attentionBlock struct {
	Total int `json:"total"`
	Items []struct {
		ID           uuid.UUID `json:"id"`
		AwaitsAnswer bool      `json:"awaits_answer"`
	} `json:"items"`
}

func (e *dashEnv) attention(t *testing.T) attentionBlock {
	t.Helper()
	_, d := e.get(t, "")
	var blk attentionBlock
	if err := json.Unmarshal(d["attention"], &blk); err != nil {
		t.Fatal(err)
	}
	return blk
}

func TestDashboardAttentionRule(t *testing.T) {
	e := newDashEnv(t)
	now := time.Now()
	ago := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }
	read, actioned, dismissed := ago(1), ago(1), ago(1)
	empty := `{}`

	unreadInfo := e.notif(t, "project_invite", nil, nil, nil, ago(5), empty)                    // IN: unread
	readInfo := e.notif(t, "project_invite", &read, nil, nil, ago(6), empty)                    // OUT: read, plain info
	readAwaiting := e.notif(t, "split_created", &read, nil, nil, ago(7), empty)                 // IN: read but awaits answer
	actionedRead := e.notif(t, "split_created", &read, &actioned, nil, ago(8), empty)           // OUT: actioned + read
	dismissedUnread := e.notif(t, "project_invite", nil, nil, &dismissed, ago(9), empty)        // OUT: dismissed
	dismissedAwaiting := e.notif(t, "split_created", &read, nil, &dismissed, ago(10), empty)    // OUT: dismissed
	supersededRead := e.notif(t, "split_created", &read, nil, nil, ago(11), `{"superseded":1}`) // OUT: no longer awaits

	blk := e.attention(t)
	var got []uuid.UUID
	for _, it := range blk.Items {
		got = append(got, it.ID)
	}
	want := []uuid.UUID{unreadInfo, readAwaiting}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("attention items (newest first) = %v, want %v (out: %v)", got, want,
			[]uuid.UUID{readInfo, actionedRead, dismissedUnread, dismissedAwaiting, supersededRead})
	}
	if !blk.Items[1].AwaitsAnswer || blk.Items[0].AwaitsAnswer {
		t.Fatalf("awaits_answer flags: %+v", blk.Items)
	}
	// Badge = 1 unread (the dismissed unread one still counts as unread in
	// the badge predicate → max(badge, rows)).
	if blk.Total < len(blk.Items) {
		t.Fatalf("total %d < items %d", blk.Total, len(blk.Items))
	}
}

// total = max(unread badge, matching rows) and items cap at 10.
func TestDashboardAttentionTotalAndCap(t *testing.T) {
	e := newDashEnv(t)
	now := time.Now()
	for i := 0; i < 12; i++ {
		e.notif(t, "project_invite", nil, nil, nil, now.Add(-time.Duration(i)*time.Minute), `{}`)
	}
	blk := e.attention(t)
	if blk.Total != 12 || len(blk.Items) != 10 {
		t.Fatalf("total %d items %d, want 12 / 10", blk.Total, len(blk.Items))
	}

	// Unread but dismissed rows count in the badge (read_at IS NULL) yet
	// are not rows of the block: total follows the larger number.
	dismissed := now
	for i := 0; i < 3; i++ {
		e.notif(t, "project_invite", nil, nil, &dismissed, now.Add(-time.Hour), `{}`)
	}
	blk = e.attention(t)
	if blk.Total != 15 || len(blk.Items) != 10 {
		t.Fatalf("total %d items %d, want 15 / 10", blk.Total, len(blk.Items))
	}
}
