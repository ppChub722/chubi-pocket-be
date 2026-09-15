package projects

// Quick-create-from-bills integration tests (spec §10/4.24). They run
// against the local dev Postgres (docker compose db, migrations 000041+
// applied) and SKIP when it is unreachable — same convention as
// transactions/sharedwallets_test.go: every fixture row is created fresh
// and torn down in t.Cleanup, no existing data is touched.
//
// Override the DSN with TEST_DATABASE_URL when needed.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

const qcTestDefaultDSN = "postgres://chubadmin:admin1234@localhost:5432/chubi_pocket_db?sslmode=disable"

func qcTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = qcTestDefaultDSN
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
	var checkDef string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conname = 'notifications_type_check'`).Scan(&checkDef); err != nil ||
		!strings.Contains(checkDef, "project_added") {
		pool.Close()
		t.Skip("notifications type CHECK missing 'project_added' — apply migration 000041 first")
	}
	t.Cleanup(pool.Close)
	return pool
}

// qcFixture builds the §4.24 cast: owner A, linked friend B (A's contact
// with linked_user_id), shared-wallet mate C, an ad-hoc "Grandma", one
// personal account and one shared wallet, plus two loose bills with debts.
type qcFixture struct {
	pool *pgxpool.Pool
	svc  *Service

	owner, friend, mate uuid.UUID
	acctPersonal        uuid.UUID // owner-only wallet, balance 1000
	acctShared          uuid.UUID // owner + mate active, balance 1000
	cat                 uuid.UUID // owner's expense category
	contactB            uuid.UUID // owner's contact linked to friend

	bill1, bill2            uuid.UUID // loose bills: 300 on personal, 200 on shared
	debtB1, debtGr1, debtB2 uuid.UUID

	userIDs []uuid.UUID
}

func qcMustV7(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	return id
}

func (f *qcFixture) newUser(t *testing.T, name string) uuid.UUID {
	t.Helper()
	id := qcMustV7(t)
	uniq := fmt.Sprintf("qct_%s", id)
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO users (id, username, email, display_name, password_hash, currency)
		VALUES ($1, $2, $3, $4, 'x', 'THB')`,
		id, uniq, uniq+"@test.local", name); err != nil {
		t.Fatalf("insert user %s: %v", name, err)
	}
	f.userIDs = append(f.userIDs, id)
	return id
}

func (f *qcFixture) newAccount(t *testing.T, ownerID uuid.UUID, name string, balance float64) uuid.UUID {
	t.Helper()
	id := qcMustV7(t)
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO accounts (id, user_id, name, type, balance, currency, status)
		VALUES ($1, $2, $3, 'bank', $4, 'THB', 'active')`,
		id, ownerID, name, balance); err != nil {
		t.Fatalf("insert account %s: %v", name, err)
	}
	return id
}

func (f *qcFixture) addAccountMember(t *testing.T, accountID, userID uuid.UUID, role string) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO account_members (id, account_id, user_id, role, report_scope, joined_at)
		VALUES ($1, $2, $3, $4, 'none', NOW())`,
		qcMustV7(t), accountID, userID, role); err != nil {
		t.Fatalf("insert account member: %v", err)
	}
}

func (f *qcFixture) newCategory(t *testing.T, userID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := qcMustV7(t)
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO categories (id, user_id, name, type, is_system)
		VALUES ($1, $2, $3, 'expense', FALSE)`,
		id, userID, name); err != nil {
		t.Fatalf("insert category %s: %v", name, err)
	}
	return id
}

func (f *qcFixture) newContact(t *testing.T, userID uuid.UUID, name string, linkedUserID *uuid.UUID) uuid.UUID {
	t.Helper()
	id := qcMustV7(t)
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO contacts (id, user_id, display_name, linked_user_id)
		VALUES ($1, $2, $3, $4)`,
		id, userID, name, linkedUserID); err != nil {
		t.Fatalf("insert contact %s: %v", name, err)
	}
	return id
}

// insertBill inserts a historical loose bill directly (raw SQL — the API
// path is exercised by the quick create's own new_transaction).
func (f *qcFixture) insertBill(t *testing.T, userID, accountID, categoryID uuid.UUID, amount float64, date string) uuid.UUID {
	t.Helper()
	id := qcMustV7(t)
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO transactions (id, user_id, account_id, type, amount, category_id, date)
		VALUES ($1, $2, $3, 'expense', $4, $5, $6::date)`,
		id, userID, accountID, amount, categoryID, date); err != nil {
		t.Fatalf("insert bill: %v", err)
	}
	return id
}

func (f *qcFixture) insertDebt(
	t *testing.T, userID uuid.UUID, contactID *uuid.UUID, personName string,
	sourceTxID uuid.UUID, amount, settled float64, status string,
) uuid.UUID {
	t.Helper()
	id := qcMustV7(t)
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO personal_debts
			(id, user_id, direction, counterparty_contact_id, counterparty_person_name,
			 source_transaction_id, amount, settled_amount, currency, status)
		VALUES ($1, $2, 'owed_to_me', $3, $4, $5, $6, $7, 'THB', $8)`,
		id, userID, contactID, personName, sourceTxID, amount, settled, status); err != nil {
		t.Fatalf("insert debt: %v", err)
	}
	return id
}

func (f *qcFixture) cleanup(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	// FK-safe order: transactions reference projects/project_transactions
	// (SET NULL) but users FKs don't cascade, so clear rows before users.
	// Deleting projects cascades project_members + project_transactions.
	for _, q := range []string{
		`DELETE FROM personal_debts WHERE user_id = ANY($1)`,
		`DELETE FROM transactions WHERE user_id = ANY($1)`,
		`DELETE FROM projects WHERE owner_user_id = ANY($1)`,
		`DELETE FROM notifications WHERE recipient_user_id = ANY($1) OR actor_user_id = ANY($1)`,
		`DELETE FROM account_members WHERE user_id = ANY($1)`,
		`DELETE FROM accounts WHERE user_id = ANY($1)`,
		`DELETE FROM categories WHERE user_id = ANY($1)`,
		`DELETE FROM contacts WHERE user_id = ANY($1)`,
		`DELETE FROM users WHERE id = ANY($1)`,
	} {
		if _, err := f.pool.Exec(ctx, q, f.userIDs); err != nil {
			t.Errorf("cleanup %q: %v", q, err)
		}
	}
}

func newQCFixture(t *testing.T) *qcFixture {
	t.Helper()
	pool := qcTestPool(t)
	f := &qcFixture{pool: pool}

	// Same wiring as main.go: the transactions create path used inside
	// quick create must produce personal_debts from splits.
	catSvc := categories.NewService(categories.NewStore(pool))
	txSvc := transactions.NewService(transactions.NewStore(pool), catSvc)
	pdSvc := personal_debts.NewService(personal_debts.NewStore(pool), txSvc)
	txSvc.WithDebtsCreator(pdSvc.CreateForTransactionTx)
	notifSvc := notifications.NewService(notifications.NewStore(pool))
	f.svc = NewService(NewStore(pool))
	f.svc.WithNotificationService(notifSvc)
	f.svc.WithTransactionsService(txSvc)

	t.Cleanup(func() { f.cleanup(t) })

	f.owner = f.newUser(t, "Alice")
	f.friend = f.newUser(t, "Bee")
	f.mate = f.newUser(t, "Chai")

	f.acctPersonal = f.newAccount(t, f.owner, "Alice personal", 1000)
	f.addAccountMember(t, f.acctPersonal, f.owner, "owner")
	f.acctShared = f.newAccount(t, f.owner, "House wallet", 1000)
	f.addAccountMember(t, f.acctShared, f.owner, "owner")
	f.addAccountMember(t, f.acctShared, f.mate, "member")

	f.cat = f.newCategory(t, f.owner, "Food")
	f.contactB = f.newContact(t, f.owner, "Bee", &f.friend)

	// Bill 1 (personal wallet, ฿300): split with linked Bee (settled) and
	// ad-hoc Grandma (open).
	f.bill1 = f.insertBill(t, f.owner, f.acctPersonal, f.cat, 300, "2026-09-01")
	f.debtB1 = f.insertDebt(t, f.owner, &f.contactB, "Bee", f.bill1, 100, 100, "settled")
	f.debtGr1 = f.insertDebt(t, f.owner, nil, "Grandma", f.bill1, 100, 0, "open")

	// Bill 2 (shared wallet, ฿200): split with Bee again (dedupe check).
	f.bill2 = f.insertBill(t, f.owner, f.acctShared, f.cat, 200, "2026-09-05")
	f.debtB2 = f.insertDebt(t, f.owner, &f.contactB, "Bee", f.bill2, 50, 0, "open")

	return f
}

func (f *qcFixture) countRows(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", q, err)
	}
	return n
}

func (f *qcFixture) accountBalance(t *testing.T, accountID uuid.UUID) float64 {
	t.Helper()
	var b float64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT balance FROM accounts WHERE id = $1`, accountID).Scan(&b); err != nil {
		t.Fatalf("balance: %v", err)
	}
	return b
}

func TestQuickCreateHappyPath(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()

	resp, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name: "แฟน, บี · 15 ก.ย. 2026",
		NewTransaction: transactions.CreateRequest{
			Type:       transactions.TypeExpense,
			AccountID:  f.acctPersonal,
			Amount:     150,
			CategoryID: &f.cat,
			Date:       "2026-09-15",
			Splits: []transactions.SplitInput{
				{PersonName: "Grandma", OwedAmount: 50},
			},
		},
		// bill1 listed twice on purpose — duplicates must collapse.
		TransactionIDs: []uuid.UUID{f.bill1, f.bill2, f.bill1},
	})
	if err != nil {
		t.Fatalf("quick create: %v", err)
	}

	// --- Project shell ---
	if resp.LinkedCount != 3 {
		t.Errorf("linked_count = %d, want 3", resp.LinkedCount)
	}
	if resp.Name != "แฟน, บี · 15 ก.ย. 2026" || resp.Status != StatusActive {
		t.Errorf("project shape: name=%q status=%q", resp.Name, resp.Status)
	}
	if resp.Type == nil || *resp.Type != "other" {
		t.Errorf("project type = %v, want 'other'", resp.Type)
	}
	if resp.OwnerUserID != f.owner {
		t.Errorf("owner_user_id = %s, want caller", resp.OwnerUserID)
	}

	// --- Members: owner + Bee(linked) + Grandma(ad-hoc) + Chai(wallet) ---
	if resp.MembersCount != 4 {
		t.Errorf("members_count = %d, want 4 (deduped)", resp.MembersCount)
	}
	type memberRow struct {
		id     uuid.UUID
		userID *uuid.UUID
		name   string
		role   string
		status string
	}
	memberRows, err := f.pool.Query(ctx, `
		SELECT id, user_id, display_name, role, status
		FROM project_members WHERE project_id = $1`, resp.ID)
	if err != nil {
		t.Fatalf("members query: %v", err)
	}
	members := map[string]memberRow{} // key: user id or lower(name)
	for memberRows.Next() {
		var m memberRow
		if err := memberRows.Scan(&m.id, &m.userID, &m.name, &m.role, &m.status); err != nil {
			t.Fatalf("scan member: %v", err)
		}
		key := strings.ToLower(m.name)
		if m.userID != nil {
			key = m.userID.String()
		}
		if _, dup := members[key]; dup {
			t.Errorf("duplicate member for %s", key)
		}
		members[key] = m
	}
	memberRows.Close()
	if len(members) != 4 {
		t.Fatalf("member rows = %d, want 4", len(members))
	}
	if m := members[f.owner.String()]; m.role != RoleOwner || m.status != MemberStatusActive {
		t.Errorf("owner member: role=%s status=%s", m.role, m.status)
	}
	beeMember, ok := members[f.friend.String()]
	if !ok || beeMember.status != MemberStatusActive || beeMember.role != RoleContributor {
		t.Errorf("Bee should be an active linked contributor, got %+v", beeMember)
	}
	if m, ok := members[f.mate.String()]; !ok || m.status != MemberStatusActive {
		t.Errorf("shared-wallet mate should be an active linked member, got %+v", m)
	}
	grMember, ok := members["grandma"]
	if !ok || grMember.userID != nil {
		t.Errorf("Grandma should be a single ad-hoc member, got %+v", grMember)
	}

	// --- Board: 3 parents, 4 children, personal rows linked back ---
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM project_transactions
		WHERE project_id = $1 AND parent_project_transaction_id IS NULL`, resp.ID); n != 3 {
		t.Errorf("board parents = %d, want 3", n)
	}
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM project_transactions
		WHERE project_id = $1 AND parent_project_transaction_id IS NOT NULL`, resp.ID); n != 4 {
		t.Errorf("board children = %d, want 4", n)
	}

	var newTxID uuid.UUID
	if err := f.pool.QueryRow(ctx, `
		SELECT id FROM transactions
		WHERE user_id = $1 AND amount = 150 AND project_id = $2`,
		f.owner, resp.ID).Scan(&newTxID); err != nil {
		t.Fatalf("new bill should be linked to the project: %v", err)
	}

	checkLinked := func(txID uuid.UUID, wantAmount float64, wantChildren int) {
		t.Helper()
		var parentID uuid.UUID
		var projectID uuid.UUID
		if err := f.pool.QueryRow(ctx, `
			SELECT source_project_transaction_id, project_id FROM transactions
			WHERE id = $1 AND source_project_transaction_id IS NOT NULL`,
			txID).Scan(&parentID, &projectID); err != nil {
			t.Errorf("bill %s not auto-claimed: %v", txID, err)
			return
		}
		if projectID != resp.ID {
			t.Errorf("bill %s project_id = %s, want %s", txID, projectID, resp.ID)
		}
		var amount float64
		var isChild *uuid.UUID
		if err := f.pool.QueryRow(ctx, `
			SELECT amount, parent_project_transaction_id FROM project_transactions
			WHERE id = $1 AND project_id = $2`, parentID, resp.ID).Scan(&amount, &isChild); err != nil {
			t.Errorf("board parent for bill %s: %v", txID, err)
			return
		}
		if isChild != nil || amount != wantAmount {
			t.Errorf("board parent of %s: amount=%.2f (want %.2f), child=%v", txID, amount, wantAmount, isChild)
		}
		if n := f.countRows(t, `
			SELECT COUNT(*) FROM project_transactions
			WHERE parent_project_transaction_id = $1`, parentID); n != wantChildren {
			t.Errorf("bill %s children = %d, want %d", txID, n, wantChildren)
		}
	}
	checkLinked(f.bill1, 300, 2)
	checkLinked(f.bill2, 200, 1)
	checkLinked(newTxID, 150, 1)

	// Bill1's Bee child must be attributed to Bee's member row with the
	// debt's amount.
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM project_transactions pt
		JOIN transactions tr ON tr.source_project_transaction_id = pt.parent_project_transaction_id
		WHERE tr.id = $1 AND pt.transaction_member_id = $2 AND pt.amount = 100`,
		f.bill1, beeMember.id); n != 1 {
		t.Errorf("bill1 Bee split child rows = %d, want 1", n)
	}

	// --- Settled means settled: debts gain project_id ONLY ---
	type debtState struct {
		projectID *uuid.UUID
		settled   float64
		status    string
	}
	debtOf := func(id uuid.UUID) debtState {
		t.Helper()
		var d debtState
		if err := f.pool.QueryRow(ctx, `
			SELECT project_id, settled_amount, status FROM personal_debts WHERE id = $1`,
			id).Scan(&d.projectID, &d.settled, &d.status); err != nil {
			t.Fatalf("debt %s: %v", id, err)
		}
		return d
	}
	if d := debtOf(f.debtB1); d.projectID == nil || *d.projectID != resp.ID ||
		d.settled != 100 || d.status != "settled" {
		t.Errorf("settled debt changed: %+v", d)
	}
	if d := debtOf(f.debtGr1); d.projectID == nil || d.settled != 0 || d.status != "open" {
		t.Errorf("open debt: %+v", d)
	}
	if d := debtOf(f.debtB2); d.projectID == nil || d.status != "open" {
		t.Errorf("bill2 debt: %+v", d)
	}
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM personal_debts
		WHERE source_transaction_id = $1 AND project_id = $2`, newTxID, resp.ID); n != 1 {
		t.Errorf("new bill debt rows tagged = %d, want 1", n)
	}

	// --- Balance moved only for the new bill ---
	if b := f.accountBalance(t, f.acctPersonal); b != 850 {
		t.Errorf("personal balance = %.2f, want 850 (only the new ฿150 bill moves money)", b)
	}
	if b := f.accountBalance(t, f.acctShared); b != 1000 {
		t.Errorf("shared balance = %.2f, want 1000 (untouched)", b)
	}

	// --- project_added notifications for Bee + Chai, none for the owner ---
	for _, uid := range []uuid.UUID{f.friend, f.mate} {
		if n := f.countRows(t, `
			SELECT COUNT(*) FROM notifications
			WHERE type = 'project_added' AND recipient_user_id = $1 AND actor_user_id = $2`,
			uid, f.owner); n != 1 {
			t.Errorf("project_added for %s = %d, want 1", uid, n)
		}
	}
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM notifications
		WHERE type = 'project_added' AND recipient_user_id = $1`, f.owner); n != 0 {
		t.Errorf("owner should not receive project_added, got %d", n)
	}
}

func TestQuickCreateAtomicRollback(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()

	newTx := func(amount float64) transactions.CreateRequest {
		return transactions.CreateRequest{
			Type:       transactions.TypeExpense,
			AccountID:  f.acctPersonal,
			Amount:     amount,
			CategoryID: &f.cat,
			Date:       "2026-09-15",
		}
	}

	// First call succeeds and claims bill1.
	first, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name:           "First",
		NewTransaction: newTx(10),
		TransactionIDs: []uuid.UUID{f.bill1},
	})
	if err != nil {
		t.Fatalf("first quick create: %v", err)
	}
	if first.LinkedCount != 2 {
		t.Errorf("first linked_count = %d, want 2", first.LinkedCount)
	}

	snapshot := func() (projects, txs, notifs int, balance float64) {
		projects = f.countRows(t, `SELECT COUNT(*) FROM projects WHERE owner_user_id = $1`, f.owner)
		txs = f.countRows(t, `SELECT COUNT(*) FROM transactions WHERE user_id = $1`, f.owner)
		notifs = f.countRows(t, `SELECT COUNT(*) FROM notifications WHERE type = 'project_added' AND actor_user_id = $1`, f.owner)
		balance = f.accountBalance(t, f.acctPersonal)
		return
	}
	projBefore, txBefore, notifBefore, balBefore := snapshot()

	// Second call lists a valid loose bill (bill2) AND the already-claimed
	// bill1 → 409, and NOTHING must persist (bill2 processed first).
	_, err = f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name:           "Second",
		NewTransaction: newTx(20),
		TransactionIDs: []uuid.UUID{f.bill2, f.bill1},
	})
	if !errors.Is(err, ErrQuickTxAlreadyInProject) {
		t.Fatalf("want ErrQuickTxAlreadyInProject, got %v", err)
	}

	projAfter, txAfter, notifAfter, balAfter := snapshot()
	if projAfter != projBefore {
		t.Errorf("projects leaked: %d -> %d", projBefore, projAfter)
	}
	if txAfter != txBefore {
		t.Errorf("transactions leaked: %d -> %d", txBefore, txAfter)
	}
	if notifAfter != notifBefore {
		t.Errorf("notifications leaked: %d -> %d", notifBefore, notifAfter)
	}
	if balAfter != balBefore {
		t.Errorf("balance moved on rollback: %.2f -> %.2f", balBefore, balAfter)
	}
	// bill2 must remain a loose bill.
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM transactions WHERE id = $1 AND project_id IS NULL`, f.bill2); n != 1 {
		t.Errorf("bill2 should still be loose after rollback")
	}
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM project_members pm JOIN projects p ON p.id = pm.project_id
		WHERE p.owner_user_id = $1`, f.owner); n != 3 {
		// First project: owner + Bee + Grandma. Nothing from the rollback.
		t.Errorf("member rows = %d, want 3 (first project only)", n)
	}

	// Unknown id → 404 TX_NOT_FOUND, still nothing persisted.
	_, err = f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name:           "Ghost",
		NewTransaction: newTx(30),
		TransactionIDs: []uuid.UUID{qcMustV7(t)},
	})
	if !errors.Is(err, ErrQuickTxNotFound) {
		t.Fatalf("want ErrQuickTxNotFound, got %v", err)
	}

	// Another user's bill → also TX_NOT_FOUND (no info leak).
	mateBill := f.insertBill(t, f.mate, f.acctShared, f.newCategory(t, f.mate, "Mate cat"), 60, "2026-09-10")
	_, err = f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name:           "Not mine",
		NewTransaction: newTx(40),
		TransactionIDs: []uuid.UUID{mateBill},
	})
	if !errors.Is(err, ErrQuickTxNotFound) {
		t.Fatalf("want ErrQuickTxNotFound for another user's bill, got %v", err)
	}

	projFinal, txFinal, _, balFinal := snapshot()
	if projFinal != projBefore || txFinal != txBefore || balFinal != balBefore {
		t.Errorf("state leaked after failed calls: projects %d/%d txs %d/%d balance %.2f/%.2f",
			projBefore, projFinal, txBefore, txFinal, balBefore, balFinal)
	}
}
