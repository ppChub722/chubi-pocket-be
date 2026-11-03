package projects

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// fx — three users (A owns, B and C are app users), each with a wallet and a category; A knows B
// and C as linked contacts and Dee as a plain name.
type fx struct {
	t     *testing.T
	pool  *pgxpool.Pool
	svc   *Service
	txs   *transactions.Service
	debts *personal_debts.Service

	a, b, c          uuid.UUID
	walletA, walletB uuid.UUID
	catA, catB       uuid.UUID
	contactB         uuid.UUID // A's contact for B
	contactC         uuid.UUID // A's contact for C
	contactA         uuid.UUID // B's contact for A
	users            []uuid.UUID
}

func newFx(t *testing.T) *fx {
	t.Helper()
	pool := testdb.Pool(t)
	f := &fx{t: t, pool: pool}
	catSvc := categories.NewService(categories.NewStore(pool))
	f.txs = transactions.NewService(transactions.NewStore(pool), catSvc)
	f.debts = personal_debts.NewService(personal_debts.NewStore(pool), f.txs)
	notifs := notifications.NewService(notifications.NewStore(pool))
	f.debts.WithNotifications(notifs)
	f.txs.WithDebtLinks(f.debts)
	f.svc = NewService(NewStore(pool))
	f.svc.WithNotificationService(notifs)
	f.svc.WithTransactionsService(f.txs)
	f.svc.WithDebts(f.debts)
	t.Cleanup(f.cleanup)

	f.a, f.b, f.c = f.user("Alice"), f.user("Bee"), f.user("Chai")
	seed, err := pool.Begin(context.Background()) // the system categories a real user has (Debt Paid / Received)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []uuid.UUID{f.a, f.b, f.c} {
		if err := catSvc.SeedForUser(context.Background(), seed, u); err != nil {
			t.Fatalf("seed categories: %v", err)
		}
	}
	if err := seed.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.walletA, f.walletB = f.wallet(f.a, 1000), f.wallet(f.b, 1000)
	f.catA, f.catB = f.category(f.a, "Food"), f.category(f.b, "Food")
	f.contactB = f.contact(f.a, "Bee", &f.b)
	f.contactC = f.contact(f.a, "Chai", &f.c)
	f.contactA = f.contact(f.b, "Alice", &f.a)
	return f
}

func (f *fx) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), q, args...); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
}

func (f *fx) id() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fx) user(name string) uuid.UUID {
	id := f.id()
	f.exec(`INSERT INTO users (id, username, email, display_name, password_hash, currency)
		VALUES ($1, $2, $3, $4, 'x', 'THB')`, id, "pp_"+id.String(), "pp_"+id.String()+"@test.local", name)
	f.users = append(f.users, id)
	return id
}

func (f *fx) wallet(owner uuid.UUID, balance float64) uuid.UUID {
	id := f.id()
	f.exec(`INSERT INTO accounts (id, user_id, name, type, balance, currency, status)
		VALUES ($1, $2, 'Wallet', 'bank', $3, 'THB', 'active')`, id, owner, balance)
	f.exec(`INSERT INTO account_members (id, account_id, user_id, role, report_scope, joined_at)
		VALUES ($1, $2, $3, 'owner', 'none', NOW())`, f.id(), id, owner)
	return id
}

func (f *fx) category(owner uuid.UUID, name string) uuid.UUID {
	id := f.id()
	f.exec(`INSERT INTO categories (id, user_id, name, type, is_system) VALUES ($1, $2, $3, 'expense', FALSE)`, id, owner, name)
	return id
}

func (f *fx) contact(owner uuid.UUID, name string, linked *uuid.UUID) uuid.UUID {
	id := f.id()
	f.exec(`INSERT INTO contacts (id, user_id, display_name, linked_user_id) VALUES ($1, $2, $3, $4)`, id, owner, name, linked)
	return id
}

// settings — one user's switches (columns of user_notification_settings).
func (f *fx) settings(user uuid.UUID, cols map[string]any) {
	f.t.Helper()
	f.exec(`INSERT INTO user_notification_settings (user_id, created_by_user_id, updated_by_user_id)
		VALUES ($1, $1, $1) ON CONFLICT (user_id) DO NOTHING`, user)
	for k, v := range cols {
		f.exec(fmt.Sprintf(`UPDATE user_notification_settings SET %s = $2 WHERE user_id = $1`, k), user, v)
	}
}

func (f *fx) cleanup() {
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM personal_debts WHERE user_id = ANY($1)`,
		`DELETE FROM transactions WHERE user_id = ANY($1)`,
		`UPDATE projects SET completed_at = NULL WHERE owner_user_id = ANY($1)`, // a completed event's bills are locked
		`DELETE FROM project_transactions WHERE owner_user_id = ANY($1)`,
		`DELETE FROM projects WHERE owner_user_id = ANY($1)`,
		`DELETE FROM notifications WHERE recipient_user_id = ANY($1) OR actor_user_id = ANY($1)`,
		`DELETE FROM account_members WHERE user_id = ANY($1)`,
		`DELETE FROM accounts WHERE user_id = ANY($1)`,
		`DELETE FROM categories WHERE user_id = ANY($1)`,
		`DELETE FROM contacts WHERE user_id = ANY($1)`,
		`DELETE FROM tags WHERE user_id = ANY($1)`,
		`DELETE FROM user_notification_settings WHERE user_id = ANY($1)`,
		`DELETE FROM users WHERE id = ANY($1)`,
	} {
		if _, err := f.pool.Exec(ctx, q, f.users); err != nil {
			f.t.Errorf("cleanup %q: %v", q, err)
		}
	}
}

func (f *fx) count(q string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func (f *fx) num(q string, args ...any) float64 {
	f.t.Helper()
	var v float64
	if err := f.pool.QueryRow(context.Background(), q, args...).Scan(&v); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
	return v
}

func (f *fx) balance(wallet uuid.UUID) float64 {
	return f.num(`SELECT balance::float8 FROM accounts WHERE id = $1`, wallet)
}

// personNet — what a user's person book says about one name: + they owe me, − I owe them (paid =
// money went to them, received = money came from them; debts and repayments alike).
func (f *fx) personNet(user uuid.UUID, name string) float64 {
	return f.num(`SELECT COALESCE(SUM(CASE WHEN direction = 'paid' THEN amount ELSE -amount END), 0)::float8
		FROM personal_debts WHERE user_id = $1 AND counterparty_person_name = $2`, user, name)
}

func (f *fx) notices(user uuid.UUID, typ string) int {
	return f.count(`SELECT COUNT(*) FROM notifications WHERE recipient_user_id = $1 AND type = $2`, user, typ)
}

// --- HTTP ---

func (f *fx) engine(as uuid.UUID) http.Handler {
	h := NewHandler(f.svc)
	return apitest.CountedEngine(as, func(g *gin.RouterGroup) {
		g.POST("/projects", h.Create)
		g.GET("/projects/:id/detail", h.Detail)
		g.POST("/projects/:id/complete", h.Complete)
		g.GET("/projects/:id/members", h.ListMembers)
		g.POST("/projects/:id/members", h.AddMember)
		g.POST("/projects/:id/members/:member_id/merge", h.MergeMember)
		g.POST("/projects/:id/members/:member_id/invite", h.InviteMember)
		g.GET("/projects/:id/settle", h.Settle)
		g.POST("/projects/:id/settle/book", h.BookSettle)
		g.PUT("/projects/:id/my-defaults", h.SetMyDefaults)
		g.GET("/projects/:id/bills", h.ListEventBills)
		g.POST("/projects/:id/bills", h.CreateEventBill)
		g.GET("/projects/:id/bills/:bid", h.GetPaper(true))
		g.PUT("/projects/:id/bills/:bid", h.UpdatePaper(true))
		g.POST("/projects/:id/bills/:bid/claim", h.ClaimPaper(true))
		g.POST("/projects/:id/bills/:bid/leave", h.LeavePaper(true))
		g.POST("/projects/:id/bills/:bid/move", h.MoveEventBill)
		g.DELETE("/projects/:id/bills/:bid/people/:pid", h.RemovePaperPerson(true))
		g.PATCH("/projects/:id/bills/:bid/people/:pid", h.MarkPaperPerson(true))
		g.GET("/split-bills", h.ListSplitBills)
		g.POST("/split-bills", h.CreateSplitBill)
		g.GET("/split-bills/:id", h.GetPaper(false))
		g.PUT("/split-bills/:id", h.UpdatePaper(false))
		g.POST("/split-bills/:id/join", h.JoinSplitBill)
		g.POST("/split-bills/:id/decline", h.DeclineSplitBill)
		g.POST("/split-bills/:id/claim", h.ClaimPaper(false))
		g.POST("/split-bills/:id/leave", h.LeavePaper(false))
		g.POST("/split-bills/:id/move-to-event", h.MoveSplitBill)
		g.DELETE("/split-bills/:id/people/:pid", h.RemovePaperPerson(false))
		g.PATCH("/split-bills/:id/people/:pid", h.MarkPaperPerson(false))
		g.POST("/transactions/:id/paper-sync", h.SyncTransaction)
		g.POST("/transactions/:id/unlink-paper", h.UnlinkTransaction)
		g.POST("/personal-debts/:id/paper-sync", h.SyncDebt)
		g.POST("/personal-debts/:id/unlink-paper", h.UnlinkDebt)
	})
}

type client struct {
	t *testing.T
	r http.Handler
}

func (f *fx) as(user uuid.UUID) client { return client{f.t, f.engine(user)} }

// do — one call; op = the spec operation the answer must conform to ("" = don't check).
func (c client) do(method, path string, body any, op string, want int) map[string]any {
	c.t.Helper()
	status, raw := apitest.Do(c.t, c.r, method, "/api/v1"+path, body)
	if status != want {
		c.t.Fatalf("%s %s: status %d, want %d; %s", method, path, status, want, raw)
	}
	if op != "" {
		apitest.Conform(c.t, op, status, raw)
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(raw, &env)
	if env.Data != nil {
		return env.Data
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// fail — a call that must fail with this status and code.
func (c client) fail(method, path string, body any, want int, code string) {
	c.t.Helper()
	status, raw := apitest.Do(c.t, c.r, method, "/api/v1"+path, body)
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	if status != want || e.Error.Code != code {
		c.t.Errorf("%s %s: %d %s, want %d %s (%s)", method, path, status, e.Error.Code, want, code, raw)
	}
}

func obj(v any, keys ...string) map[string]any {
	cur, _ := v.(map[string]any)
	for _, k := range keys {
		cur, _ = cur[k].(map[string]any)
	}
	return cur
}

func arr(v map[string]any, key string) []any {
	l, _ := v[key].([]any)
	return l
}

func str(v map[string]any, key string) string {
	s, _ := v[key].(string)
	return s
}

func num(v map[string]any, key string) float64 {
	n, _ := v[key].(float64)
	return n
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.005 }

// person — the paper person whose name is name.
func person(paper map[string]any, name string) map[string]any {
	for _, p := range arr(paper, "people") {
		if str(obj(p), "name") == name {
			return obj(p)
		}
	}
	return nil
}

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
