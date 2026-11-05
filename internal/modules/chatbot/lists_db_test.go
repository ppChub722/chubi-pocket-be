package chatbot

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/projects"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/recurring"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/saving_goals"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/tags"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/llm"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

type dbWorld struct {
	t      *testing.T
	pool   *pgxpool.Pool
	user   uuid.UUID
	wallet uuid.UUID
	svc    *pending.Service
}

func (w *dbWorld) exec(q string, args ...any) {
	w.t.Helper()
	if _, err := w.pool.Exec(context.Background(), q, args...); err != nil {
		w.t.Fatalf("%s: %v", q, err)
	}
}

func (w *dbWorld) count(q string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// newDBWorld is a user with a main wallet, a category, a goal, a contact and two events (one finished), and the
// pending service wired like the app's.
func newDBWorld(t *testing.T) *dbWorld {
	t.Helper()
	pool := testdb.Pool(t)
	w := &dbWorld{t: t, pool: pool, user: testdb.User(t, pool)}
	w.wallet = testdb.Account(t, pool, w.user)
	w.exec(`UPDATE accounts SET balance = 1000 WHERE id = $1`, w.wallet)
	testdb.Category(t, pool, w.user, "Food")
	w.exec(`INSERT INTO user_notification_settings (user_id, default_account_id) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET default_account_id = $2`, w.user, w.wallet)
	for _, c := range [][2]string{{"LOAN_GIVEN", "expense"}, {"LOAN_TAKEN", "income"}, {"DEBT_PAID", "expense"}, {"DEBT_RECEIVED", "income"}} {
		w.exec(`INSERT INTO categories (id, user_id, name, type, is_system, system_kind) VALUES ($1, $2, $3, $4, TRUE, $3)`, uuid.New(), w.user, c[0], c[1])
	}
	w.exec(`INSERT INTO saving_goals (id, user_id, name, status) VALUES ($1, $2, 'trip', 'active')`, uuid.New(), w.user)
	w.exec(`INSERT INTO contacts (id, user_id, display_name, status) VALUES ($1, $2, 'Boy', 'active')`, uuid.New(), w.user)
	for _, e := range [][2]string{{"Phuket trip", "active"}, {"New year trip", "completed"}} {
		id := uuid.New()
		w.exec(`INSERT INTO projects (id, owner_user_id, name, status) VALUES ($1, $2, $3, $4)`, id, w.user, e[0], e[1])
		w.exec(`INSERT INTO project_members (id, project_id, user_id, display_name, role, status) VALUES ($1, $2, $3, 'Me', 'owner', 'active')`, uuid.New(), id, w.user)
		if e[1] == "active" {
			w.exec(`INSERT INTO project_members (id, project_id, display_name, role, status) VALUES ($1, $2, 'Lee', 'contributor', 'active')`, uuid.New(), id)
		}
	}
	catSvc := categories.NewService(categories.NewStore(pool))
	txSvc := transactions.NewService(transactions.NewStore(pool), catSvc)
	pdSvc := personal_debts.NewService(personal_debts.NewStore(pool), txSvc)
	txSvc.WithDebtLinks(pdSvc)
	prj := projects.NewService(projects.NewStore(pool))
	prj.WithTransactionsService(txSvc)
	prj.WithDebts(pdSvc)
	w.svc = pending.NewService(pending.NewStore(pool), txSvc, tags.NewService(tags.NewStore(pool)), pdSvc, prj)
	w.svc.WithSavings(saving_goals.NewService(saving_goals.NewStore(pool)))
	return w
}

type stubRules struct{ id uuid.UUID }

func (s stubRules) ChatRules(context.Context, uuid.UUID) ([]recurring.ChatRule, error) {
	return []recurring.ChatRule{{ID: s.id, Name: "Power", Type: "expense", Amount: 500}}, nil
}

func (s stubRules) PickSlot(context.Context, uuid.UUID, uuid.UUID, time.Time) (time.Time, bool, error) {
	return time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), true, nil
}

// The lists the model points at, read from the real tables.
func TestDBListsReadsEverythingTheBotPointsAt(t *testing.T) {
	w := newDBWorld(t)
	rule := uuid.New()
	l, err := NewDBLists(w.pool).WithRules(stubRules{id: rule}).Lists(context.Background(), w.user)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Wallets) != 1 || !l.Wallets[0].Main || len(l.Goals) != 1 || l.Goals[0].Name != "trip" ||
		len(l.Contacts) != 1 || l.Contacts[0].Name != "Boy" || l.Contacts[0].Linked {
		t.Fatalf("lists = %+v", l)
	}
	if len(l.Events) != 2 || l.Events[0].Name != "Phuket trip" || l.Events[0].Completed || !l.Events[1].Completed {
		t.Fatalf("events = %+v", l.Events)
	}
	members := l.Events[0].Members
	if len(members) != 2 || !members[0].Me || members[1].Name != "Lee" || members[1].Me {
		t.Fatalf("members = %+v", members)
	}
	if len(l.Rules) != 1 || l.Rules[0].ID != rule || l.Rules[0].Slot != "2026-10-05" || l.Rules[0].Amount != 500 {
		t.Fatalf("rules = %+v", l.Rules)
	}
}

// From a message to the ledger on the real database: the fake model's answer, the lists, the checks and the
// booking, for the kinds that touch the saving, debt and bill services.
func TestChatToLedgerOnARealDatabase(t *testing.T) {
	w := newDBWorld(t)
	ctx := context.Background()
	lists, err := NewDBLists(w.pool).Lists(ctx, w.user)
	if err != nil {
		t.Fatal(err)
	}
	saver := NewPendingSaver(w.svc)
	book := func(kw string) {
		t.Helper()
		out, err := Parse(ctx, llm.NewKeywordFake(), testDay, lists, kw, nil)
		if err != nil || len(out.Items) != 1 {
			t.Fatalf("%s: %d items, hint %q, %v", kw, len(out.Items), out.Hint, err)
		}
		if miss := saver.Check(out.Items); len(miss[0]) != 0 {
			t.Fatalf("%s: not ready: %v", kw, miss[0])
		}
		res, err := saver.Book(ctx, w.user, SaveRef{ChatID: 1, CardID: "c", MessageID: 1}, out.Items)
		if err != nil || res.Booked != 1 {
			t.Fatalf("%s: Book = %+v, %v", kw, res, err)
		}
	}
	entries := func() int { return w.count(`SELECT COUNT(*) FROM saving_entries WHERE user_id = $1`, w.user) }
	debts := func() int { return w.count(`SELECT COUNT(*) FROM personal_debts WHERE user_id = $1`, w.user) }
	txs := func() int { return w.count(`SELECT COUNT(*) FROM transactions WHERE user_id = $1`, w.user) }

	book("save goal") // wallet -> the goal
	book("save spare")
	book("allocate") // spare -> the goal
	book("move spare")
	book("withdraw saving")
	if n := entries(); n != 5 {
		t.Errorf("saving entries = %d, want 5", n)
	}
	book("debt lend") // money out + the debt
	book("debt ledger")
	book("debt repay")
	if debts() != 3 || txs() != 2 {
		t.Errorf("debts %d txs %d, want 3 / 2", debts(), txs())
	}
	book("split ลี") // a split bill with a contact and a typed name
	if n := w.count(`SELECT COUNT(*) FROM transactions WHERE user_id = $1 AND split_transaction_id IS NOT NULL`, w.user); n != 1 {
		t.Errorf("split bill rows = %d, want 1", n)
	}
	// the chat leaves nothing pending, and keeps no text
	if n := w.count(`SELECT COUNT(*) FROM pending_transactions WHERE user_id = $1`, w.user); n != 0 {
		t.Errorf("pending rows = %d, want 0", n)
	}

	// an event bill by the list's event: every member is on it
	out, err := Parse(ctx, llm.NewKeywordFake(), testDay, lists, "event bill", nil)
	if err != nil || len(out.Items) != 1 || out.Items[0].Paper == nil || len(out.Items[0].Paper.People) != 1 {
		t.Fatalf("event bill = %+v, %v", out.Items, err)
	}
	if miss := saver.Check(out.Items); len(miss[0]) != 0 {
		t.Fatalf("event bill not ready: %v", miss[0])
	}
	if res, err := saver.Book(ctx, w.user, SaveRef{}, out.Items); err != nil || res.Booked != 1 {
		t.Fatalf("event bill Book = %+v, %v", res, err)
	}
	// ✓ without auto queues rows for the pending page, with a source and no text in the ref
	out, _ = Parse(ctx, llm.NewKeywordFake(), testDay, lists, "debt ledger", nil)
	if err := saver.Queue(ctx, w.user, SaveRef{ChatID: 9, CardID: "x", MessageID: 2}, out.Items); err != nil {
		t.Fatal(err)
	}
	var source string
	var ref []byte
	if err := w.pool.QueryRow(ctx, `SELECT source, source_ref FROM pending_transactions WHERE user_id = $1`, w.user).Scan(&source, &ref); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(ref, &m)
	if source != "telegram" || m["text"] != nil || m["card_id"] != "x" {
		t.Errorf("queued row: source %q ref %v", source, m)
	}
}
