package transactions

// Shared-wallets integration tests (spec §14). They run against the
// local dev Postgres (docker compose db, migrations 000039+ applied) and
// SKIP when it is unreachable — same DB the dev stack uses; every
// fixture row is created fresh and torn down in t.Cleanup, no existing
// data is touched.
//
// Override the DSN with TEST_DATABASE_URL when needed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
)

const testDefaultDSN = "postgres://chubadmin:admin1234@localhost:5432/chubi_pocket_db?sslmode=disable"

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = testDefaultDSN
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
	var migrated bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables WHERE table_name = 'account_members'
	)`).Scan(&migrated); err != nil || !migrated {
		pool.Close()
		t.Skip("account_members table missing — apply migration 000039 first")
	}
	t.Cleanup(pool.Close)
	return pool
}

// swFixture builds the §14 cast: an owner, an active co-member, an
// ex-member (joined + left, with one historical row), and an outsider.
type swFixture struct {
	pool *pgxpool.Pool
	svc  *Service

	owner, member, exMember, outsider uuid.UUID
	wallet                            uuid.UUID
	ownerCat, memberCat, exCat        uuid.UUID
	exRow                             uuid.UUID
	userIDs                           []uuid.UUID
}

func mustV7(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	return id
}

func (f *swFixture) newUser(t *testing.T, name string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := mustV7(t)
	uniq := fmt.Sprintf("swt_%s", id)
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO users (id, username, email, display_name, password_hash, currency)
		VALUES ($1, $2, $3, $4, 'x', 'THB')`,
		id, uniq, uniq+"@test.local", name); err != nil {
		t.Fatalf("insert user %s: %v", name, err)
	}
	f.userIDs = append(f.userIDs, id)
	return id
}

func (f *swFixture) newAccount(t *testing.T, ownerID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := mustV7(t)
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO accounts (id, user_id, name, type, balance, currency, status)
		VALUES ($1, $2, $3, 'bank', 0, 'THB', 'active')`,
		id, ownerID, name); err != nil {
		t.Fatalf("insert account %s: %v", name, err)
	}
	return id
}

func (f *swFixture) addMember(t *testing.T, accountID, userID uuid.UUID, role, scope string, joined, left bool) uuid.UUID {
	t.Helper()
	id := mustV7(t)
	var joinedAt, leftAt *time.Time
	now := time.Now()
	if joined {
		joinedAt = &now
	}
	if left {
		leftAt = &now
	}
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO account_members (id, account_id, user_id, role, report_scope, joined_at, left_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, accountID, userID, role, scope, joinedAt, leftAt); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	return id
}

func (f *swFixture) newCategory(t *testing.T, userID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := mustV7(t)
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO categories (id, user_id, name, type, is_system)
		VALUES ($1, $2, $3, 'expense', FALSE)`,
		id, userID, name); err != nil {
		t.Fatalf("insert category %s: %v", name, err)
	}
	return id
}

// insertRawTx inserts a historical transactions row directly (used for
// the ex-member's pre-leave row — the API would rightly refuse it now).
func (f *swFixture) insertRawTx(t *testing.T, userID, accountID, categoryID uuid.UUID, amount float64) uuid.UUID {
	t.Helper()
	id := mustV7(t)
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO transactions (id, user_id, account_id, type, amount, category_id, date)
		VALUES ($1, $2, $3, 'expense', $4, $5, '2026-01-15'::date)`,
		id, userID, accountID, amount, categoryID); err != nil {
		t.Fatalf("insert raw tx: %v", err)
	}
	return id
}

func (f *swFixture) cleanup(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	// FK-safe order; account_members.account_id cascades with accounts but
	// user FKs don't, so clear memberships before users.
	for _, q := range []string{
		`DELETE FROM transactions WHERE user_id = ANY($1)`,
		`DELETE FROM notifications WHERE recipient_user_id = ANY($1) OR actor_user_id = ANY($1)`,
		`DELETE FROM account_members WHERE user_id = ANY($1)`,
		`DELETE FROM accounts WHERE user_id = ANY($1)`,
		`DELETE FROM categories WHERE user_id = ANY($1)`,
		`DELETE FROM users WHERE id = ANY($1)`,
	} {
		if _, err := f.pool.Exec(ctx, q, f.userIDs); err != nil {
			t.Errorf("cleanup %q: %v", q, err)
		}
	}
}

func newSWFixture(t *testing.T) *swFixture {
	t.Helper()
	pool := testPool(t)
	f := &swFixture{pool: pool}
	f.svc = NewService(NewStore(pool), categories.NewService(categories.NewStore(pool)))
	t.Cleanup(func() { f.cleanup(t) })

	f.owner = f.newUser(t, "Owner")
	f.member = f.newUser(t, "Member")
	f.exMember = f.newUser(t, "ExMember")
	f.outsider = f.newUser(t, "Outsider")

	f.wallet = f.newAccount(t, f.owner, "House wallet")
	f.addMember(t, f.wallet, f.owner, "owner", "all", true, false)
	f.addMember(t, f.wallet, f.member, "member", "none", true, false)
	f.addMember(t, f.wallet, f.exMember, "member", "own", true, true) // joined + left

	f.ownerCat = f.newCategory(t, f.owner, "Owner Food")
	f.memberCat = f.newCategory(t, f.member, "Member Food")
	f.exCat = f.newCategory(t, f.exMember, "Ex Food")

	f.exRow = f.insertRawTx(t, f.exMember, f.wallet, f.exCat, 25)
	return f
}

func createExpense(t *testing.T, f *swFixture, userID, categoryID uuid.UUID, amount float64) (*TransactionDetail, error) {
	t.Helper()
	res, err := f.svc.Create(context.Background(), userID, CreateRequest{
		Type:       TypeExpense,
		AccountID:  f.wallet,
		Amount:     amount,
		CategoryID: &categoryID,
		Date:       "2026-01-15",
	})
	if err != nil {
		return nil, err
	}
	return res.(*TransactionDetail), nil
}

func updateReq(t *testing.T, body string) UpdateRequest {
	t.Helper()
	var req UpdateRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal update request: %v", err)
	}
	return req
}

// TestSharedWalletAuthzMatrix covers the §14/2 co-ownership matrix:
// owner / member / ex-member / non-member × create / read / edit-own /
// edit-others / edit-others-category / delete.
func TestSharedWalletAuthzMatrix(t *testing.T) {
	f := newSWFixture(t)
	ctx := context.Background()

	// --- create ---
	rowO, err := createExpense(t, f, f.owner, f.ownerCat, 100)
	if err != nil {
		t.Fatalf("owner create: %v", err)
	}
	rowM, err := createExpense(t, f, f.member, f.memberCat, 40)
	if err != nil {
		t.Fatalf("member create: %v", err)
	}
	if _, err := createExpense(t, f, f.outsider, f.ownerCat, 10); !errors.Is(err, ErrAccountForbidden) {
		t.Errorf("outsider create: want ErrAccountForbidden, got %v", err)
	}
	if _, err := createExpense(t, f, f.exMember, f.exCat, 10); !errors.Is(err, ErrAccountForbidden) {
		t.Errorf("ex-member create: want ErrAccountForbidden, got %v", err)
	}

	// Shared-wallet decorations on the create response.
	if rowM.CreatedBy == nil || rowM.CreatedBy.UserID != f.member {
		t.Errorf("create response: created_by = %+v, want member author", rowM.CreatedBy)
	}
	if rowM.CanEditCategory == nil || !*rowM.CanEditCategory {
		t.Errorf("create response: can_edit_category should be true for the author")
	}

	// --- read ---
	if _, err := f.svc.Get(ctx, f.member, rowO.ID); err != nil {
		t.Errorf("member reads owner's row: %v", err)
	}
	if _, err := f.svc.Get(ctx, f.outsider, rowO.ID); !errors.Is(err, ErrTxNotFound) {
		t.Errorf("outsider reads owner's row: want ErrTxNotFound, got %v", err)
	}
	// Ex-member still sees their own history (read-only for them).
	got, err := f.svc.Get(ctx, f.exMember, f.exRow)
	if err != nil {
		t.Fatalf("ex-member reads own old row: %v", err)
	}
	if got.IsLocked == nil || !*got.IsLocked {
		t.Errorf("ex-member's own old row should be is_locked=true, got %+v", got.IsLocked)
	}

	// Ledger view: ?account_id=wallet shows ALL authors to a member.
	list, err := f.svc.List(ctx, f.member, ListFilter{AccountID: &f.wallet, Page: 1, PerPage: 50})
	if err != nil {
		t.Fatalf("member list: %v", err)
	}
	if list.Pagination.Total != 3 {
		t.Errorf("member sees %d rows on the wallet, want 3 (owner + member + ex-member)", list.Pagination.Total)
	}
	outList, err := f.svc.List(ctx, f.outsider, ListFilter{AccountID: &f.wallet, Page: 1, PerPage: 50})
	if err != nil {
		t.Fatalf("outsider list: %v", err)
	}
	if outList.Pagination.Total != 0 {
		t.Errorf("outsider sees %d wallet rows, want 0", outList.Pagination.Total)
	}

	// --- edit own ---
	if _, err := f.svc.Update(ctx, f.member, rowM.ID, updateReq(t, `{"amount": 45}`)); err != nil {
		t.Errorf("member edits own row: %v", err)
	}

	// --- edit others (allowed except category) ---
	if _, err := f.svc.Update(ctx, f.member, rowO.ID, updateReq(t, `{"note": "edited by member"}`)); err != nil {
		t.Errorf("member edits owner's note: %v", err)
	}
	catBody := fmt.Sprintf(`{"category_id": %q}`, f.memberCat)
	if _, err := f.svc.Update(ctx, f.member, rowO.ID, updateReq(t, catBody)); !errors.Is(err, ErrCategoryAuthorOnly) {
		t.Errorf("member edits owner's category: want ErrCategoryAuthorOnly, got %v", err)
	}

	// --- ex-member's own old rows are locked ---
	if _, err := f.svc.Update(ctx, f.exMember, f.exRow, updateReq(t, `{"amount": 30}`)); !errors.Is(err, ErrRowLocked) {
		t.Errorf("ex-member edits own old row: want ErrRowLocked, got %v", err)
	}
	if err := f.svc.Delete(ctx, f.exMember, f.exRow); !errors.Is(err, ErrRowLocked) {
		t.Errorf("ex-member deletes own old row: want ErrRowLocked, got %v", err)
	}

	// --- outsider mutations 404 ---
	if _, err := f.svc.Update(ctx, f.outsider, rowM.ID, updateReq(t, `{"amount": 1}`)); !errors.Is(err, ErrTxNotFound) {
		t.Errorf("outsider edit: want ErrTxNotFound, got %v", err)
	}
	if err := f.svc.Delete(ctx, f.outsider, rowM.ID); !errors.Is(err, ErrTxNotFound) {
		t.Errorf("outsider delete: want ErrTxNotFound, got %v", err)
	}

	// --- delete others (allowed for active members) ---
	if err := f.svc.Delete(ctx, f.member, rowO.ID); err != nil {
		t.Errorf("member deletes owner's row: %v", err)
	}
	// Remaining members may still edit the ex-member's locked row.
	if _, err := f.svc.Update(ctx, f.owner, f.exRow, updateReq(t, `{"note": "kept by owner"}`)); err != nil {
		t.Errorf("owner edits ex-member's row: %v", err)
	}
}

// TestReportScopePredicateSummary pins the §14/5 predicate through the
// transactions summary endpoint: 1 shared wallet, 2 users, rows by both
// + a personal-account row, verified under none / own / all.
func TestReportScopePredicateSummary(t *testing.T) {
	f := newSWFixture(t)
	ctx := context.Background()

	// Personal account for the owner (own book, scope 'all' like the
	// migration backfill).
	personal := f.newAccount(t, f.owner, "Owner personal")
	f.addMember(t, personal, f.owner, "owner", "all", true, false)

	if _, err := createExpense(t, f, f.owner, f.ownerCat, 100); err != nil {
		t.Fatalf("owner wallet row: %v", err)
	}
	if _, err := createExpense(t, f, f.member, f.memberCat, 40); err != nil {
		t.Fatalf("member wallet row: %v", err)
	}
	if _, err := f.svc.Create(ctx, f.owner, CreateRequest{
		Type: TypeExpense, AccountID: personal, Amount: 10,
		CategoryID: &f.ownerCat, Date: "2026-01-15",
	}); err != nil {
		t.Fatalf("owner personal row: %v", err)
	}

	setScope := func(userID uuid.UUID, scope string) {
		t.Helper()
		if _, err := f.pool.Exec(ctx, `
			UPDATE account_members SET report_scope = $1
			WHERE account_id = $2 AND user_id = $3 AND left_at IS NULL`,
			scope, f.wallet, userID); err != nil {
			t.Fatalf("set scope: %v", err)
		}
	}
	summarize := func(userID uuid.UUID) float64 {
		t.Helper()
		resp, err := f.svc.Summary(ctx, userID, SummaryRequest{From: "2026-01-01", To: "2026-01-31"})
		if err != nil {
			t.Fatalf("summary: %v", err)
		}
		return resp.TotalExpense
	}

	// Wallet rows: owner 100 + member 40 + ex-member 25 (fixture).
	cases := []struct {
		scope string
		want  float64
	}{
		{"none", 10}, // wallet hidden — personal account only
		{"own", 110}, // own wallet rows (100) + personal (10)
		{"all", 175}, // every wallet row (165) + personal (10)
	}
	for _, c := range cases {
		setScope(f.owner, c.scope)
		if got := summarize(f.owner); got != c.want {
			t.Errorf("owner scope=%s: total_expense = %.2f, want %.2f", c.scope, got, c.want)
		}
	}

	// Member has no personal account: own = their 40; none = 0.
	setScope(f.member, "none")
	if got := summarize(f.member); got != 0 {
		t.Errorf("member scope=none: total_expense = %.2f, want 0", got)
	}
	setScope(f.member, "own")
	if got := summarize(f.member); got != 40 {
		t.Errorf("member scope=own: total_expense = %.2f, want 40", got)
	}
	setScope(f.member, "all")
	if got := summarize(f.member); got != 165 { // 100 + 40 + ex-member's 25
		t.Errorf("member scope=all: total_expense = %.2f, want 165", got)
	}

	// Ex-member: capped ledger — their left row is scope 'own', so their
	// old wallet row still shows in their personal report.
	if got := summarize(f.exMember); got != 25 {
		t.Errorf("ex-member scope=own: total_expense = %.2f, want 25", got)
	}
	// Flip the ex-member's left row to 'none' → hidden.
	if _, err := f.pool.Exec(ctx, `
		UPDATE account_members SET report_scope = 'none'
		WHERE account_id = $1 AND user_id = $2`, f.wallet, f.exMember); err != nil {
		t.Fatalf("set ex-member scope: %v", err)
	}
	if got := summarize(f.exMember); got != 0 {
		t.Errorf("ex-member scope=none: total_expense = %.2f, want 0", got)
	}
}
