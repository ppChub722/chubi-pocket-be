package pending

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/projects"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

const evTestDefaultDSN = "postgres://chubadmin:admin1234@localhost:5433/chubi_pocket_db?sslmode=disable"

type evFixture struct {
	pool *pgxpool.Pool
	svc  *Service
	prj  *projects.Service

	owner, other uuid.UUID
	acct         uuid.UUID
	cat          uuid.UUID
	event        uuid.UUID

	userIDs []uuid.UUID
}

func evV7(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	return id
}

func (f *evFixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func (f *evFixture) newUser(t *testing.T, name string) uuid.UUID {
	t.Helper()
	id := evV7(t)
	uniq := fmt.Sprintf("evt_%s", id)
	f.exec(t, `INSERT INTO users (id, username, email, display_name, password_hash, currency)
		VALUES ($1, $2, $3, $4, 'x', 'THB')`, id, uniq, uniq+"@test.local", name)
	f.userIDs = append(f.userIDs, id)
	return id
}

func newEvFixture(t *testing.T) *evFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = evTestDefaultDSN
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("cannot build pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("database unreachable (start docker compose db): %v", err)
	}
	t.Cleanup(pool.Close)
	f := &evFixture{pool: pool}

	catSvc := categories.NewService(categories.NewStore(pool))
	txSvc := transactions.NewService(transactions.NewStore(pool), catSvc)
	pdSvc := personal_debts.NewService(personal_debts.NewStore(pool), txSvc)
	notifSvc := notifications.NewService(notifications.NewStore(pool))
	pdSvc.WithNotifications(notifSvc)
	txSvc.WithDebtsCreator(pdSvc.CreateForTransactionTx)
	txSvc.WithSplitsEditor(pdSvc.EditForTransactionTx)
	f.prj = projects.NewService(projects.NewStore(pool))
	f.prj.WithNotificationService(notifSvc)
	f.prj.WithTransactionsService(txSvc)
	f.svc = NewService(NewStore(pool), txSvc, nil, pdSvc, f.prj)

	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM pending_transactions WHERE user_id = ANY($1)`,
			`DELETE FROM personal_debts WHERE user_id = ANY($1)`,
			`DELETE FROM transactions WHERE user_id = ANY($1)`,
			`DELETE FROM projects WHERE owner_user_id = ANY($1)`,
			`DELETE FROM notifications WHERE recipient_user_id = ANY($1) OR actor_user_id = ANY($1)`,
			`DELETE FROM account_members WHERE user_id = ANY($1)`,
			`DELETE FROM accounts WHERE user_id = ANY($1)`,
			`DELETE FROM categories WHERE user_id = ANY($1)`,
			`DELETE FROM contacts WHERE user_id = ANY($1)`,
			`DELETE FROM user_notification_settings WHERE user_id = ANY($1)`,
			`DELETE FROM users WHERE id = ANY($1)`,
		} {
			if _, err := pool.Exec(context.Background(), q, f.userIDs); err != nil {
				t.Errorf("cleanup %q: %v", q, err)
			}
		}
	})

	f.owner = f.newUser(t, "Alice")
	f.other = f.newUser(t, "Bee")
	f.acct = evV7(t)
	f.exec(t, `INSERT INTO accounts (id, user_id, name, type, balance, currency, status)
		VALUES ($1, $2, 'Alice wallet', 'bank', 1000, 'THB', 'active')`, f.acct, f.owner)
	f.exec(t, `INSERT INTO account_members (id, account_id, user_id, role, report_scope, joined_at)
		VALUES ($1, $2, $3, 'owner', 'none', NOW())`, evV7(t), f.acct, f.owner)
	f.cat = evV7(t)
	f.exec(t, `INSERT INTO categories (id, user_id, name, type, is_system)
		VALUES ($1, $2, 'Food', 'expense', FALSE)`, f.cat, f.owner)
	f.event = f.newEvent(t, f.owner, "Trip")
	return f
}

func (f *evFixture) newEvent(t *testing.T, owner uuid.UUID, name string) uuid.UUID {
	t.Helper()
	p, err := f.prj.Create(context.Background(), owner, projects.CreateProjectRequest{Name: name})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	return p.Event.ID
}

func (f *evFixture) draft(project *uuid.UUID) Draft {
	return Draft{
		Type:       ptr("expense"),
		Amount:     ptr(150.0),
		AccountID:  &f.acct,
		CategoryID: &f.cat,
		Date:       ptr("2026-10-10"),
		ProjectID:  project,
	}
}

func (f *evFixture) save(t *testing.T, d Draft) PendingTransaction {
	t.Helper()
	out, err := f.svc.Create(context.Background(), f.owner, CreateRequest{Items: []CreateItem{{Draft: d}}})
	if err != nil {
		t.Fatalf("save draft: %v", err)
	}
	return out[0]
}

func (f *evFixture) submit(t *testing.T, id uuid.UUID) *SubmitResponse {
	t.Helper()
	out, err := f.svc.Submit(context.Background(), f.owner, SubmitRequest{IDs: []uuid.UUID{id}})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	return out
}

func (f *evFixture) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", q, err)
	}
	return n
}

func (f *evFixture) balance(t *testing.T) float64 {
	t.Helper()
	var b float64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT balance FROM accounts WHERE id = $1`, f.acct).Scan(&b); err != nil {
		t.Fatalf("balance: %v", err)
	}
	return b
}

func (f *evFixture) assertFailed(t *testing.T, id uuid.UUID, res *SubmitResponse, code string) {
	t.Helper()
	if len(res.Submitted) != 0 || len(res.Failed) != 1 || res.Failed[0].Error.Code != code {
		t.Fatalf("want one failure %q, got %+v", code, res)
	}
	got, err := f.svc.store.Get(context.Background(), f.owner, id)
	if err != nil {
		t.Fatalf("draft should stay pending: %v", err)
	}
	if got.LastError == nil || got.LastError.Code != code {
		t.Errorf("last_error = %+v, want code %q", got.LastError, code)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM transactions WHERE user_id = $1`, f.owner); n != 0 {
		t.Errorf("%d transactions leaked", n)
	}
	if b := f.balance(t); b != 1000 {
		t.Errorf("balance moved to %.2f", b)
	}
}

func TestDraftKeepsProjectID(t *testing.T) {
	f := newEvFixture(t)
	ctx := context.Background()

	saved := f.save(t, f.draft(&f.event))
	if saved.Draft.ProjectID == nil || *saved.Draft.ProjectID != f.event {
		t.Fatalf("create: project_id = %v, want %s", saved.Draft.ProjectID, f.event)
	}

	list, err := f.svc.List(ctx, f.owner)
	if err != nil || list.Count != 1 || list.Data[0].Draft.ProjectID == nil || *list.Data[0].Draft.ProjectID != f.event {
		t.Fatalf("list: %+v, %v", list, err)
	}

	e2 := f.newEvent(t, f.owner, "Dinner")
	up, err := f.svc.Update(ctx, f.owner, saved.ID, UpdateRequest{Draft: f.draft(&e2)})
	if err != nil || up.Draft.ProjectID == nil || *up.Draft.ProjectID != e2 {
		t.Fatalf("update: %+v, %v", up, err)
	}
	up, err = f.svc.Update(ctx, f.owner, saved.ID, UpdateRequest{Draft: f.draft(nil)})
	if err != nil || up.Draft.ProjectID != nil {
		t.Fatalf("update without event: %+v, %v", up, err)
	}

	ghost := evV7(t)
	if _, err := f.svc.Update(ctx, f.owner, saved.ID, UpdateRequest{Draft: f.draft(&ghost)}); err != nil {
		t.Errorf("unknown event at save should be accepted: %v", err)
	}
}

func TestSubmitDraftIntoEvent(t *testing.T) {
	f := newEvFixture(t)

	d := f.draft(&f.event)
	d.Splits = []transactions.SplitInput{{PersonName: "Grandma", OwedAmount: 50}}
	saved := f.save(t, d)
	res := f.submit(t, saved.ID)
	if len(res.Failed) != 0 || len(res.Submitted) != 1 || res.Submitted[0].TransactionID == nil {
		t.Fatalf("submit: %+v", res)
	}
	txID := *res.Submitted[0].TransactionID

	if b := f.balance(t); b != 850 {
		t.Errorf("balance = %.2f, want 850", b)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM transactions
		WHERE id = $1 AND project_id = $2 AND source_project_transaction_id IS NOT NULL`, txID, f.event); n != 1 {
		t.Errorf("transaction not linked to the event")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM project_transactions WHERE project_id = $1`, f.event); n != 2 {
		t.Errorf("board rows = %d, want parent + 1 split child", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM personal_debts WHERE user_id = $1`, f.owner); n != 0 {
		t.Errorf("%d personal debts, want none (splits moved to the board)", n)
	}
	if _, err := f.svc.store.Get(context.Background(), f.owner, saved.ID); err == nil {
		t.Errorf("confirmed draft should be deleted")
	}
}

func TestSubmitDraftWithoutEventUnchanged(t *testing.T) {
	f := newEvFixture(t)
	d := f.draft(nil)
	d.Splits = []transactions.SplitInput{{PersonName: "Grandma", OwedAmount: 50}}
	res := f.submit(t, f.save(t, d).ID)
	if len(res.Submitted) != 1 {
		t.Fatalf("submit: %+v", res)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM transactions WHERE user_id = $1 AND project_id IS NULL`, f.owner); n != 1 {
		t.Errorf("want one loose transaction")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM personal_debts WHERE user_id = $1`, f.owner); n != 1 {
		t.Errorf("want the personal split debt as before")
	}
}

func TestSubmitEventNotAllowed(t *testing.T) {
	f := newEvFixture(t)

	ghost := evV7(t)
	d := f.save(t, f.draft(&ghost))
	f.assertFailed(t, d.ID, f.submit(t, d.ID), "PROJECT_NOT_FOUND")

	for status, code := range map[string]string{
		projects.StatusCompleted: "PROJECT_NOT_ACTIVE",
		projects.StatusCancelled: "PROJECT_LOCKED",
		projects.StatusArchived:  "PROJECT_LOCKED",
	} {
		e := f.newEvent(t, f.owner, "Closed "+status)
		f.exec(t, `UPDATE projects SET status = $2 WHERE id = $1`, e, status)
		d := f.save(t, f.draft(&e))
		f.assertFailed(t, d.ID, f.submit(t, d.ID), code)
	}

	theirs := f.newEvent(t, f.other, "Theirs")
	d = f.save(t, f.draft(&theirs))
	f.assertFailed(t, d.ID, f.submit(t, d.ID), "PROJECT_NOT_MEMBER")

	// A viewer can't add bills.
	viewerEvent := f.newEvent(t, f.other, "Viewing")
	f.exec(t, `INSERT INTO project_members (id, project_id, user_id, display_name, role, status)
		VALUES ($1, $2, $3, 'Alice', 'viewer', 'active')`, evV7(t), viewerEvent, f.owner)
	d = f.save(t, f.draft(&viewerEvent))
	f.assertFailed(t, d.ID, f.submit(t, d.ID), "PROJECT_READ_ONLY")

	// Transfers can't be event bills.
	d2 := f.draft(&f.event)
	d2.Type = ptr("transfer")
	d2.CategoryID = nil
	to := evV7(t)
	f.exec(t, `INSERT INTO accounts (id, user_id, name, type, balance, currency, status)
		VALUES ($1, $2, 'Second', 'bank', 0, 'THB', 'active')`, to, f.owner)
	d2.TransferToAccountID = &to
	d = f.save(t, d2)
	f.assertFailed(t, d.ID, f.submit(t, d.ID), "TX_NOT_BILLABLE")
}

func TestSubmitRetryAfterFixingEvent(t *testing.T) {
	f := newEvFixture(t)
	ghost := evV7(t)
	d := f.save(t, f.draft(&ghost))
	f.assertFailed(t, d.ID, f.submit(t, d.ID), "PROJECT_NOT_FOUND")

	up, err := f.svc.Update(context.Background(), f.owner, d.ID, UpdateRequest{Draft: f.draft(&f.event)})
	if err != nil || up.LastError != nil {
		t.Fatalf("update should clear last_error: %+v, %v", up, err)
	}
	if res := f.submit(t, d.ID); len(res.Submitted) != 1 {
		t.Fatalf("retry: %+v", res)
	}
}

func TestSubmitBadSplitsOnEventDraft(t *testing.T) {
	f := newEvFixture(t)

	cases := []struct {
		name   string
		splits []transactions.SplitInput
		code   string
	}{
		{"over the amount", []transactions.SplitInput{{PersonName: "A", OwedAmount: 100}, {PersonName: "B", OwedAmount: 100}}, "SPLITS_EXCEED_AMOUNT"},
		{"no name", []transactions.SplitInput{{PersonName: "  ", OwedAmount: 10}}, "INVALID_SPLIT"},
		{"zero amount", []transactions.SplitInput{{PersonName: "A", OwedAmount: 0}}, "INVALID_SPLIT"},
	}
	for _, c := range cases {
		d := f.draft(&f.event)
		d.Splits = c.splits
		saved := f.save(t, d) // saving never rejects splits
		res := f.submit(t, saved.ID)
		if len(res.Submitted) != 0 || len(res.Failed) != 1 {
			t.Errorf("%s: want a clean failure, got %+v", c.name, res)
			continue
		}
		if res.Failed[0].Error.Code != c.code {
			t.Errorf("%s: code %q, want %q", c.name, res.Failed[0].Error.Code, c.code)
		}
		if n := f.count(t, `SELECT COUNT(*) FROM transactions WHERE user_id = $1`, f.owner); n != 0 {
			t.Errorf("%s: %d transactions leaked", c.name, n)
		}
		if b := f.balance(t); b != 1000 {
			t.Errorf("%s: balance moved to %.2f", c.name, b)
		}
	}
}

// TestEventDraftWithPayer — an event draft carries payer / members (0.4.0-3): resolved names, and
// at submit someone else paid → my transaction is my share with no wallet.
func TestEventDraftWithPayer(t *testing.T) {
	f := newEvFixture(t)
	d := f.draft(&f.event)
	d.Payer = &projects.BillPerson{PersonName: "Dan"}
	d.Members = []projects.BillMember{
		{BillPerson: projects.BillPerson{Me: true}, Share: 60},
		{BillPerson: projects.BillPerson{PersonName: "Dan"}, Share: 90},
	}
	p := f.save(t, d)
	bp := p.Resolved.BillPeople
	if p.Resolved.LinkMode != "event" || bp == nil || bp.Payer.Name != "Dan" || len(bp.Members) != 2 ||
		!bp.Members[0].Me || bp.Members[0].Share == nil || *bp.Members[0].Share != 60 {
		t.Fatalf("resolved = %+v / bill_people %+v", p.Resolved, bp)
	}
	res := f.submit(t, p.ID)
	if len(res.Submitted) != 1 || len(res.Submitted[0].Rows) != 1 {
		t.Fatalf("submit: %+v", res)
	}
	row := res.Submitted[0].Rows[0]
	if row.Amount != 60 || row.Account != nil || row.LinkMode != "event" || f.balance(t) != 1000 {
		t.Errorf("my share row = %+v (wallet %v)", row.Transaction, f.balance(t))
	}

	// Payer / members without an event → refused at submit.
	bad := f.draft(nil)
	bad.Payer = &projects.BillPerson{Me: true}
	b := f.save(t, bad)
	if r := f.submit(t, b.ID); len(r.Failed) != 1 || r.Failed[0].Error.Code != "LINK_MODE_CONFLICT" {
		t.Errorf("payer without an event: %+v", r)
	}
}
