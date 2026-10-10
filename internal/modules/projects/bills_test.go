package projects

// Attaching existing transactions to an event after the fact (owner
// 2026-10-10): add / quick create from existing bills only, remove (back to
// a loose transaction), move, and deleting a board row that has personal
// copies. Same fixture + DB convention as quick_test.go.

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

// boardParentOf — the board row a personal transaction is linked to.
func (f *qcFixture) boardParentOf(t *testing.T, txID uuid.UUID) uuid.UUID {
	t.Helper()
	var pt uuid.UUID
	if err := f.pool.QueryRow(context.Background(),
		`SELECT source_project_transaction_id FROM transactions WHERE id = $1`, txID).Scan(&pt); err != nil {
		t.Fatalf("board parent of %s: %v", txID, err)
	}
	return pt
}

// insertCopy — userID's personal copy of board row ptID (what CopyToPersonal
// makes: floating, linked by source_project_transaction_id).
func (f *qcFixture) insertCopy(t *testing.T, userID, projectID, ptID uuid.UUID, amount float64) uuid.UUID {
	t.Helper()
	id := qcMustV7(t)
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO transactions (id, user_id, type, amount, date, project_id, source_project_transaction_id)
		VALUES ($1, $2, 'expense', $3, '2026-09-01', $4, $5)`,
		id, userID, amount, projectID, ptID); err != nil {
		t.Fatalf("insert copy: %v", err)
	}
	return id
}

func (f *qcFixture) assertLoose(t *testing.T, txID uuid.UUID) {
	t.Helper()
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM transactions
		WHERE id = $1 AND project_id IS NULL AND source_project_transaction_id IS NULL`, txID); n != 1 {
		t.Errorf("transaction %s should be loose", txID)
	}
}

func TestAddExistingBillsOnly(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()

	// New event from an existing bill — no new_transaction.
	resp, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name: "From bill1", TransactionIDs: []uuid.UUID{f.bill1},
	})
	if err != nil {
		t.Fatalf("quick create from existing: %v", err)
	}
	if resp.TransactionID != nil || resp.LinkedCount != 1 {
		t.Errorf("transaction_id=%v linked=%d, want nil and 1", resp.TransactionID, resp.LinkedCount)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM transactions WHERE id = $1 AND project_id = $2`,
		f.bill1, resp.ID); n != 1 {
		t.Errorf("bill1 not linked to the new event")
	}
	if b := f.accountBalance(t, f.acctPersonal); b != 1000 {
		t.Errorf("balance moved: %.2f", b)
	}

	// Add an existing bill to that event.
	added, err := f.svc.AddBills(ctx, f.owner, resp.ID, AddBillsRequest{TransactionIDs: []uuid.UUID{f.bill2}})
	if err != nil {
		t.Fatalf("add existing: %v", err)
	}
	if added.TransactionID != nil || added.LinkedCount != 1 {
		t.Errorf("add: transaction_id=%v linked=%d", added.TransactionID, added.LinkedCount)
	}

	// Nothing to add.
	if _, err := f.svc.AddBills(ctx, f.owner, resp.ID, AddBillsRequest{}); !errors.Is(err, ErrQuickNothingToAdd) {
		t.Errorf("empty add: want ErrQuickNothingToAdd, got %v", err)
	}
	if _, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{Name: "Empty"}); !errors.Is(err, ErrQuickNothingToAdd) {
		t.Errorf("empty quick create: want ErrQuickNothingToAdd, got %v", err)
	}
}

func TestPullRejectsRepayment(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()

	repayment := qcMustV7(t)
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO transactions (id, user_id, account_id, type, amount, date, source_personal_debt_id)
		VALUES ($1, $2, $3, 'income', 50, '2026-09-02', $4)`,
		repayment, f.owner, f.acctPersonal, f.debtGr1); err != nil {
		t.Fatalf("insert repayment: %v", err)
	}
	if _, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name: "Repay", TransactionIDs: []uuid.UUID{repayment},
	}); !errors.Is(err, ErrQuickTxIsRepayment) {
		t.Errorf("repayment: want ErrQuickTxIsRepayment, got %v", err)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM projects WHERE owner_user_id = $1`, f.owner); n != 0 {
		t.Errorf("failed call left %d projects", n)
	}
}

// A wallet exists only on my side; an event has none (owner 2026-10-10):
// wallet-less bills join, the board row takes the author's currency.
func TestPullWalletlessBill(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()

	floating := qcMustV7(t)
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO transactions (id, user_id, type, amount, date)
		VALUES ($1, $2, 'expense', 70, '2026-09-03')`, floating, f.owner); err != nil {
		t.Fatalf("insert wallet-less: %v", err)
	}
	resp, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name: "Floating", TransactionIDs: []uuid.UUID{floating},
	})
	if err != nil {
		t.Fatalf("wallet-less pull: %v", err)
	}
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM project_transactions pt
		JOIN transactions t ON t.source_project_transaction_id = pt.id
		WHERE t.id = $1 AND t.project_id = $2 AND pt.currency = 'THB' AND pt.amount = 70`,
		floating, resp.ID); n != 1 {
		t.Errorf("wallet-less bill not on the board with the user's currency")
	}

	// A wallet-less new bill too.
	added, err := f.svc.AddBills(ctx, f.owner, resp.ID, AddBillsRequest{
		NewTransaction: &transactions.CreateRequest{
			Type: transactions.TypeExpense, Amount: 30, Date: "2026-09-04",
		},
	})
	if err != nil {
		t.Fatalf("wallet-less new bill: %v", err)
	}
	if added.TransactionID == nil || added.LinkedCount != 1 {
		t.Errorf("new wallet-less bill: transaction_id=%v linked=%d", added.TransactionID, added.LinkedCount)
	}
}

func TestRemoveBill(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()

	resp, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name: "Trip", TransactionIDs: []uuid.UUID{f.bill1},
	})
	if err != nil {
		t.Fatalf("quick create: %v", err)
	}
	parent := f.boardParentOf(t, f.bill1)
	beeCopy := f.insertCopy(t, f.friend, resp.ID, parent, 100)

	// Only the author: the owner can't remove Bee's copy, Bee can't remove bill1.
	if err := f.svc.RemoveBill(ctx, f.owner, resp.ID, beeCopy); !errors.Is(err, ErrQuickTxNotFound) {
		t.Errorf("non-author: want ErrQuickTxNotFound, got %v", err)
	}
	if err := f.svc.RemoveBill(ctx, f.friend, resp.ID, f.bill1); !errors.Is(err, ErrQuickTxNotFound) {
		t.Errorf("non-author bill: want ErrQuickTxNotFound, got %v", err)
	}

	// Bee removes their copy: only the copy is unlinked; the board row stays.
	if err := f.svc.RemoveBill(ctx, f.friend, resp.ID, beeCopy); err != nil {
		t.Fatalf("remove copy: %v", err)
	}
	f.assertLoose(t, beeCopy)
	if n := f.countRows(t, `SELECT COUNT(*) FROM project_transactions WHERE id = $1`, parent); n != 1 {
		t.Errorf("board row must stay when a copy leaves")
	}

	// The origin leaves: board row + children go, every copy is unlinked,
	// the bill's debts lose their project tag but stay as they were.
	beeCopy2 := f.insertCopy(t, f.friend, resp.ID, parent, 100)
	if err := f.svc.RemoveBill(ctx, f.owner, resp.ID, f.bill1); err != nil {
		t.Fatalf("remove origin: %v", err)
	}
	f.assertLoose(t, f.bill1)
	f.assertLoose(t, beeCopy2)
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM project_transactions WHERE id = $1 OR parent_project_transaction_id = $1`,
		parent); n != 0 {
		t.Errorf("board rows left: %d", n)
	}
	// The board's member splits came back as bill1's personal splits: Bee
	// through my contact linked to her, Grandma by name.
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM personal_debts
		WHERE source_transaction_id = $1 AND project_id IS NULL`, f.bill1); n != 2 {
		t.Errorf("bill1 splits back = %d, want 2", n)
	}
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM personal_debts
		WHERE source_transaction_id = $1 AND counterparty_contact_id = $2 AND amount = 100`,
		f.bill1, f.contactB); n != 1 {
		t.Errorf("Bee's split should come back through my contact for her")
	}
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM personal_debts
		WHERE source_transaction_id = $1 AND counterparty_contact_id IS NULL
		  AND counterparty_person_name = 'Grandma'`, f.bill1); n != 1 {
		t.Errorf("Grandma's split should come back by name")
	}
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM notifications WHERE type = 'split_created' AND recipient_user_id = $1`,
		f.friend); n != 1 {
		t.Errorf("split_created to Bee = %d, want 1 (her split is back in my book)", n)
	}
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM notifications
		WHERE type = 'project_tx_changed' AND recipient_user_id = $1 AND payload->>'change_kind' = 'deleted'`,
		f.friend); n != 1 {
		t.Errorf("project_tx_changed(deleted) to Bee = %d, want 1", n)
	}

	// Already out.
	if err := f.svc.RemoveBill(ctx, f.owner, resp.ID, f.bill1); !errors.Is(err, ErrBillNotInProject) {
		t.Errorf("second remove: want ErrBillNotInProject, got %v", err)
	}
}

func TestMoveBill(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()

	first, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name: "First", TransactionIDs: []uuid.UUID{f.bill1},
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	oldParent := f.boardParentOf(t, f.bill1)

	// Without move → 409; same event with move → still 409.
	if _, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name: "No move", TransactionIDs: []uuid.UUID{f.bill1},
	}); !errors.Is(err, ErrQuickTxAlreadyInProject) {
		t.Errorf("no move: want ErrQuickTxAlreadyInProject, got %v", err)
	}
	if _, err := f.svc.AddBills(ctx, f.owner, first.ID, AddBillsRequest{
		TransactionIDs: []uuid.UUID{f.bill1}, Move: true,
	}); !errors.Is(err, ErrQuickTxAlreadyInProject) {
		t.Errorf("same event: want ErrQuickTxAlreadyInProject, got %v", err)
	}

	second, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name: "Second", TransactionIDs: []uuid.UUID{f.bill1}, Move: true,
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM transactions WHERE id = $1 AND project_id = $2`,
		f.bill1, second.ID); n != 1 {
		t.Errorf("bill1 not in the second event")
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM project_transactions WHERE id = $1`, oldParent); n != 0 {
		t.Errorf("old board row should be gone")
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM project_transactions WHERE project_id = $1`, first.ID); n != 0 {
		t.Errorf("first event still has %d board rows", n)
	}
	// The board splits went straight to the new board row — never back
	// into personal debts in between.
	if n := f.countRows(t, `SELECT COUNT(*) FROM personal_debts WHERE source_transaction_id = $1`,
		f.bill1); n != 0 {
		t.Errorf("personal splits on the moved bill = %d, want 0", n)
	}
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM project_transactions
		WHERE parent_project_transaction_id = $1`, f.boardParentOf(t, f.bill1)); n != 2 {
		t.Errorf("new board row splits = %d, want 2", n)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM notifications WHERE type = 'split_created' AND recipient_user_id = $1`,
		f.friend); n != 0 {
		t.Errorf("a move must not bounce splits through personal debts (split_created = %d)", n)
	}
}

// v0.3.3 plan step 1: deleting a board row someone copied used to 500 on
// CHECK transactions_project_id_implies_source.
func TestDeleteBoardRowWithCopies(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()

	resp, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name: "Trip", TransactionIDs: []uuid.UUID{f.bill1},
	})
	if err != nil {
		t.Fatalf("quick create: %v", err)
	}
	parent := f.boardParentOf(t, f.bill1)
	beeCopy := f.insertCopy(t, f.friend, resp.ID, parent, 100)

	if err := f.svc.DeleteProjectTransaction(ctx, f.owner, resp.ID, parent); err != nil {
		t.Fatalf("delete board row with copies: %v", err)
	}
	f.assertLoose(t, f.bill1)
	f.assertLoose(t, beeCopy)
	if n := f.countRows(t, `SELECT COUNT(*) FROM transactions WHERE id = ANY($1)`,
		[]uuid.UUID{f.bill1, beeCopy}); n != 2 {
		t.Errorf("personal rows must survive a board-row delete")
	}
}

// Opening balance / balance adjustment rows (system categories) aren't
// spending — they can't join an event.
func TestPullRejectsSystemRow(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()

	sysCat := qcMustV7(t)
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO categories (id, user_id, name, type, is_system, system_kind)
		VALUES ($1, $2, 'Balance adjustment', 'expense', TRUE, 'ADJUST_OUT')`,
		sysCat, f.owner); err != nil {
		t.Fatalf("insert system category: %v", err)
	}
	adj := f.insertBill(t, f.owner, f.acctPersonal, sysCat, 40, "2026-09-04")
	if _, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name: "Adj", TransactionIDs: []uuid.UUID{adj},
	}); !errors.Is(err, ErrQuickTxSystemRow) {
		t.Errorf("system row: want ErrQuickTxSystemRow, got %v", err)
	}
}

// Splits move onto the board, so money that already moved in the personal
// book (a repayment) or a forgiven split blocks the pull.
func TestPullBlocksRepaidOrForgivenSplits(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()

	if _, err := f.pool.Exec(ctx, `UPDATE personal_debts SET settled_amount = 50 WHERE id = $1`, f.debtGr1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name: "Repaid", TransactionIDs: []uuid.UUID{f.bill1},
	}); !errors.Is(err, ErrQuickTxSplitRepaid) {
		t.Errorf("repaid split: want ErrQuickTxSplitRepaid, got %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE personal_debts SET status = 'cancelled' WHERE id = $1`, f.debtB2); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name: "Forgiven", TransactionIDs: []uuid.UUID{f.bill2},
	}); !errors.Is(err, ErrQuickTxSplitCancelled) {
		t.Errorf("forgiven split: want ErrQuickTxSplitCancelled, got %v", err)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM projects WHERE owner_user_id = $1`, f.owner); n != 0 {
		t.Errorf("blocked pulls left %d projects", n)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM personal_debts WHERE source_transaction_id = ANY($1)`,
		[]uuid.UUID{f.bill1, f.bill2}); n != 3 {
		t.Errorf("blocked pulls touched the splits: %d left, want 3", n)
	}
}

// A linked partner's split leaves my book the usual way when the bill
// joins an event: they get split_changed (removed) for their copy.
func TestPullMovesLinkedSplitOffPartnersBook(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()

	created, err := f.txs.Create(ctx, f.owner, transactions.CreateRequest{
		Type: transactions.TypeExpense, AccountID: &f.acctPersonal, Amount: 90, CategoryID: &f.cat,
		Date:   "2026-09-06",
		Splits: []transactions.SplitInput{{PersonName: "Bee", ContactID: &f.contactB, OwedAmount: 30}},
	})
	if err != nil {
		t.Fatalf("create split bill: %v", err)
	}
	billID := created.(*transactions.TransactionDetail).ID
	if n := f.countRows(t, `SELECT COUNT(*) FROM personal_debts WHERE user_id = $1 AND direction = 'i_owe'`,
		f.friend); n != 1 {
		t.Fatalf("Bee's copy of the split = %d, want 1 (split_created auto)", n)
	}

	if _, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name: "Dinner", TransactionIDs: []uuid.UUID{billID},
	}); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM personal_debts WHERE source_transaction_id = $1`, billID); n != 0 {
		t.Errorf("my split still on the bill: %d", n)
	}
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM notifications
		WHERE type = 'split_changed' AND recipient_user_id = $1 AND payload->>'change' = 'removed'`,
		f.friend); n != 1 {
		t.Errorf("split_changed(removed) to Bee = %d, want 1", n)
	}
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM project_transactions WHERE parent_project_transaction_id = $1 AND amount = 30`,
		f.boardParentOf(t, billID)); n != 1 {
		t.Errorf("Bee's split should be on the board")
	}
}

// A new bill's splits go straight onto the board — never a personal debt.
func TestNewBillSplitsGoStraightToBoard(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()

	resp, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name: "Trip", TransactionIDs: []uuid.UUID{f.bill2},
	})
	if err != nil {
		t.Fatalf("quick create: %v", err)
	}
	added, err := f.svc.AddBills(ctx, f.owner, resp.ID, AddBillsRequest{
		NewTransaction: &transactions.CreateRequest{
			Type: transactions.TypeExpense, AccountID: &f.acctPersonal, Amount: 120, CategoryID: &f.cat,
			Date: "2026-09-07",
			Splits: []transactions.SplitInput{
				{PersonName: "Bee", ContactID: &f.contactB, OwedAmount: 40},
				{PersonName: "Grandma", OwedAmount: 40},
			},
		},
	})
	if err != nil {
		t.Fatalf("add new bill: %v", err)
	}
	newID := *added.TransactionID
	if n := f.countRows(t, `SELECT COUNT(*) FROM personal_debts WHERE source_transaction_id = $1`, newID); n != 0 {
		t.Errorf("new bill made %d personal debts, want 0", n)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM notifications WHERE type = 'split_created' AND recipient_user_id = $1`,
		f.friend); n != 0 {
		t.Errorf("split_created to Bee = %d, want 0", n)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM project_transactions WHERE parent_project_transaction_id = $1`,
		f.boardParentOf(t, newID)); n != 2 {
		t.Errorf("board splits = %d, want 2", n)
	}
	if b := f.accountBalance(t, f.acctPersonal); b != 880 {
		t.Errorf("balance = %.2f, want 880 (cash moves in full)", b)
	}
}

// Deleting a board row: my own bill's row gives its splits back to my bill;
// anyone else deleting it only unlinks (never writes another user's book).
func TestDeleteBoardRowRestoresOnlyForAuthor(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()

	resp, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name: "Trip", TransactionIDs: []uuid.UUID{f.bill1, f.bill2},
	})
	if err != nil {
		t.Fatalf("quick create: %v", err)
	}
	p1, p2 := f.boardParentOf(t, f.bill1), f.boardParentOf(t, f.bill2)

	if err := f.svc.DeleteProjectTransaction(ctx, f.owner, resp.ID, p1); err != nil {
		t.Fatalf("author deletes: %v", err)
	}
	f.assertLoose(t, f.bill1)
	if n := f.countRows(t, `SELECT COUNT(*) FROM personal_debts WHERE source_transaction_id = $1`, f.bill1); n != 2 {
		t.Errorf("author delete: splits back = %d, want 2", n)
	}

	if err := f.svc.DeleteProjectTransaction(ctx, f.friend, resp.ID, p2); err != nil {
		t.Fatalf("member deletes: %v", err)
	}
	f.assertLoose(t, f.bill2)
	if n := f.countRows(t, `SELECT COUNT(*) FROM personal_debts WHERE source_transaction_id = $1`, f.bill2); n != 0 {
		t.Errorf("another member's delete wrote %d splits into my book", n)
	}
}

// A copy of an event row is the member's share (project-as-separate-book
// §2 "I spent"): the actor's is the amount minus the others' splits.
func TestCopyIsMyShare(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()

	resp, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name: "Trip", TransactionIDs: []uuid.UUID{f.bill1},
	})
	if err != nil {
		t.Fatalf("quick create: %v", err)
	}
	memberOf := func(where string, arg any) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := f.pool.QueryRow(ctx, `SELECT id FROM project_members WHERE project_id = $1 AND `+where,
			resp.ID, arg).Scan(&id); err != nil {
			t.Fatalf("member: %v", err)
		}
		return id
	}
	me, bee := memberOf("user_id = $2", f.owner), memberOf("user_id = $2", f.friend)
	gran := memberOf("display_name = $2", "Grandma")
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO user_notification_settings (user_id, auto_resolve_own_in_projects) VALUES ($1, TRUE)
		ON CONFLICT (user_id) DO UPDATE SET auto_resolve_own_in_projects = TRUE`, f.owner); err != nil {
		t.Fatal(err)
	}

	pt, err := f.svc.CreateProjectTransaction(ctx, f.owner, resp.ID, CreateProjectTransactionRequest{
		TransactionMemberID: me, Type: "expense", Amount: 600, Currency: "THB", Date: "2026-09-10",
		Splits: []ProjectSplitInput{{MemberID: bee, Amount: 200}, {MemberID: gran, Amount: 200}},
	})
	if err != nil {
		t.Fatalf("record 600: %v", err)
	}
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM transactions WHERE user_id = $1 AND source_project_transaction_id = $2 AND amount = 200`,
		f.owner, pt.ID); n != 1 {
		t.Errorf("my auto copy should be my share (200)")
	}
	beeCopy, err := f.svc.CopyToPersonal(ctx, f.friend, resp.ID, pt.ID)
	if err != nil {
		t.Fatalf("Bee copies: %v", err)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM transactions WHERE id = $1 AND amount = 200`, beeCopy); n != 1 {
		t.Errorf("Bee's copy should be her split (200)")
	}

	// Fully split to others: no share, no auto copy, no error.
	full, err := f.svc.CreateProjectTransaction(ctx, f.owner, resp.ID, CreateProjectTransactionRequest{
		TransactionMemberID: me, Type: "expense", Amount: 600, Currency: "THB", Date: "2026-09-11",
		Splits: []ProjectSplitInput{{MemberID: bee, Amount: 300}, {MemberID: gran, Amount: 300}},
	})
	if err != nil {
		t.Fatalf("record fully split: %v", err)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM transactions WHERE source_project_transaction_id = $1`, full.ID); n != 0 {
		t.Errorf("fully split row got %d copies, want 0", n)
	}
}

// can_split / can_edit_splits / can_join_event follow the BE's own rules.
func TestSplitFlags(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()
	flags := func(viewer, id uuid.UUID) [3]bool {
		t.Helper()
		d, err := f.txs.Get(ctx, viewer, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		return [3]bool{d.CanSplit, d.CanEditSplits, d.CanJoinEvent}
	}

	// My split bill on the shared wallet: everything; the wallet mate sees
	// it but may do nothing with it.
	if got := flags(f.owner, f.bill2); got != [3]bool{true, true, true} {
		t.Errorf("own split bill = %v, want all true", got)
	}
	if got := flags(f.mate, f.bill2); got != [3]bool{true, false, false} {
		t.Errorf("mate's view = %v, want can_split only", got)
	}

	// A repaid split blocks joining an event, not editing.
	if _, err := f.pool.Exec(ctx, `UPDATE personal_debts SET settled_amount = 50 WHERE id = $1`, f.debtGr1); err != nil {
		t.Fatal(err)
	}
	if got := flags(f.owner, f.bill1); got != [3]bool{true, true, false} {
		t.Errorf("repaid split bill = %v, want join false", got)
	}

	// An event bill can still carry splits of its own (a layer on my share).
	if _, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name: "Trip", TransactionIDs: []uuid.UUID{f.bill2},
	}); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if got := flags(f.owner, f.bill2); got != [3]bool{true, true, true} {
		t.Errorf("event bill = %v, want all true", got)
	}

	// A repayment and an adjustment row: nothing.
	repay := qcMustV7(t)
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO transactions (id, user_id, account_id, type, amount, date, source_personal_debt_id)
		VALUES ($1, $2, $3, 'income', 50, '2026-09-02', $4)`,
		repay, f.owner, f.acctPersonal, f.debtGr1); err != nil {
		t.Fatal(err)
	}
	if got := flags(f.owner, repay); got != [3]bool{} {
		t.Errorf("repayment = %v, want none", got)
	}
	sysCat := qcMustV7(t)
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO categories (id, user_id, name, type, is_system, system_kind)
		VALUES ($1, $2, 'Balance adjustment', 'expense', TRUE, 'ADJUST_OUT')`, sysCat, f.owner); err != nil {
		t.Fatal(err)
	}
	adj := f.insertBill(t, f.owner, f.acctPersonal, sysCat, 40, "2026-09-04")
	if got := flags(f.owner, adj); got != [3]bool{} {
		t.Errorf("adjustment = %v, want none", got)
	}
}

// An event bill's own splits are a layer on my share of it (owner
// 2026-10-10): capped at amount − the board's splits, both come off
// my_share, and removing the bill from the event merges both layers back.
func TestEventBillPersonalSplits(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()

	// bill2 ฿200, Bee ฿50 → on the board; my share 150.
	resp, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name: "Trip", TransactionIDs: []uuid.UUID{f.bill2},
	})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if _, err := f.txs.EditSplits(ctx, f.owner, f.bill2, []transactions.SplitEdit{
		{PersonName: "Outsider", OwedAmount: 160},
	}); !errors.Is(err, transactions.ErrSplitsExceedShare) {
		t.Errorf("past my share: want ErrSplitsExceedShare, got %v", err)
	}
	d, err := f.txs.EditSplits(ctx, f.owner, f.bill2, []transactions.SplitEdit{
		{PersonName: "Outsider", OwedAmount: 100},
	})
	if err != nil {
		t.Fatalf("split my share: %v", err)
	}
	if d.MyShare == nil || *d.MyShare != 50 {
		t.Errorf("my_share = %v, want 50 (200 − 50 board − 100 mine)", d.MyShare)
	}

	// Leaving the event: Bee's board split comes back next to Outsider.
	if err := f.svc.RemoveBill(ctx, f.owner, resp.ID, f.bill2); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM personal_debts WHERE source_transaction_id = $1`, f.bill2); n != 2 {
		t.Errorf("splits after remove = %d, want 2 (Outsider + Bee back)", n)
	}
	got, err := f.txs.Get(ctx, f.owner, f.bill2)
	if err != nil {
		t.Fatal(err)
	}
	if got.MyShare == nil || *got.MyShare != 50 {
		t.Errorf("my_share after remove = %v, want 50 (unchanged)", got.MyShare)
	}
}

// my_share: a pulled bill counts amount − the board's member splits; a
// personal split bill amount − its splits (a forgiven one doesn't come off).
func TestMyShare(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()

	if _, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name: "Trip", TransactionIDs: []uuid.UUID{f.bill1},
	}); err != nil {
		t.Fatalf("quick create: %v", err)
	}
	share := func(id uuid.UUID) float64 {
		t.Helper()
		d, err := f.txs.Get(ctx, f.owner, id)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if d.MyShare == nil {
			t.Fatalf("my_share missing on %s", id)
		}
		return *d.MyShare
	}
	if v := share(f.bill1); v != 100 {
		t.Errorf("event bill my_share = %v, want 100 (300 − 200 on the board)", v)
	}
	if v := share(f.bill2); v != 150 {
		t.Errorf("split bill my_share = %v, want 150 (200 − 50)", v)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE personal_debts SET status = 'cancelled' WHERE id = $1`, f.debtB2); err != nil {
		t.Fatal(err)
	}
	if v := share(f.bill2); v != 200 {
		t.Errorf("forgiven split: my_share = %v, want 200", v)
	}
}
