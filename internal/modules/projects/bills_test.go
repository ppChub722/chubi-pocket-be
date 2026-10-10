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
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM personal_debts
		WHERE source_transaction_id = $1 AND project_id IS NULL`, f.bill1); n != 2 {
		t.Errorf("bill1 debts untagged = %d, want 2", n)
	}
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM personal_debts WHERE id = $1 AND settled_amount = 100 AND status = 'settled'`,
		f.debtB1); n != 1 {
		t.Errorf("settled debt changed on remove")
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
	if n := f.countRows(t, `
		SELECT COUNT(*) FROM personal_debts WHERE source_transaction_id = $1 AND project_id = $2`,
		f.bill1, second.ID); n != 2 {
		t.Errorf("debts re-tagged = %d, want 2", n)
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
