package projects

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
)

// PUT …/my-books: unlink keeps my rows in my books and remembers the board row, link takes them back.
// Each member decides for their own books only — a viewer too ("my books, my choice").
func TestSetMyBooks(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()

	resp, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{Name: "Trip", TransactionIDs: []uuid.UUID{f.bill1}})
	if err != nil {
		t.Fatalf("quick create: %v", err)
	}
	proj := resp.Detail.Event.ID
	pt := f.boardParentOf(t, f.bill1)
	books := func(userID uuid.UUID, req MyBooksRequest) *MyBooksResult {
		t.Helper()
		out, err := f.svc.SetMyBooks(ctx, userID, proj, pt, req)
		if err != nil {
			t.Fatalf("my-books %+v: %v", req, err)
		}
		return out
	}
	origin := func() (linked, unlinked *uuid.UUID) {
		t.Helper()
		if err := f.pool.QueryRow(ctx, `SELECT linked_transaction_id, unlinked_transaction_id FROM project_transactions WHERE id = $1`,
			pt).Scan(&linked, &unlinked); err != nil {
			t.Fatal(err)
		}
		return
	}
	if l, _ := origin(); l == nil || *l != f.bill1 {
		t.Fatalf("bill origin = %v, want bill1", l)
	}

	// tx: unlink → my bill is loose, the row remembers it; link → back on the row, the origin too
	out := books(f.owner, MyBooksRequest{Unlink: []string{BookTx}})
	f.assertLoose(t, f.bill1)
	if l, u := origin(); l != nil || u == nil || *u != f.bill1 {
		t.Errorf("after unlink: linked %v unlinked %v", l, u)
	}
	if out.Row == nil || out.Row.MyBooks.Tx {
		t.Errorf("after unlink: row my_books = %+v", out.Row)
	}
	out = books(f.owner, MyBooksRequest{Link: []string{BookTx}})
	if !slices.Equal(out.Relinked, []string{BookTx}) || f.boardParentOf(t, f.bill1) != pt || out.Row == nil || !out.Row.MyBooks.Tx {
		t.Errorf("relink tx: relinked %v, row %+v", out.Relinked, out.Row)
	}
	if l, u := origin(); l == nil || *l != f.bill1 || u != nil {
		t.Errorf("after relink: linked %v unlinked %v", l, u)
	}

	// nothing remembered → nothing relinked (the app creates it); the same kind in link + unlink is a no-op
	if out := books(f.owner, MyBooksRequest{Link: []string{BookDebt, BookRepay}}); len(out.Relinked) != 0 {
		t.Errorf("relink with nothing remembered = %v, want []", out.Relinked)
	}
	if out := books(f.owner, MyBooksRequest{Link: []string{BookTx}, Unlink: []string{BookTx}}); len(out.Relinked) != 0 || f.boardParentOf(t, f.bill1) != pt {
		t.Errorf("tx in link + unlink changed something: %v", out.Relinked)
	}

	// debt: a debt made from the row goes out of the event and back
	debt := qcMustV7(t)
	if _, err := f.pool.Exec(ctx, `INSERT INTO personal_debts
		(id, user_id, direction, counterparty_person_name, amount, currency, date, source_kind, source_project_transaction_id, project_id)
		VALUES ($1, $2, 'paid', 'Grandma', 30, 'THB', CURRENT_DATE, 'event', $3, $4)`, debt, f.owner, pt, proj); err != nil {
		t.Fatal(err)
	}
	books(f.owner, MyBooksRequest{Unlink: []string{BookDebt}})
	if n := f.countRows(t, `SELECT COUNT(*) FROM personal_debts WHERE id = $1 AND source_kind IS NULL
		AND source_project_transaction_id IS NULL AND project_id IS NULL AND unlinked_project_transaction_id = $2`, debt, pt); n != 1 {
		t.Errorf("debt not unlinked")
	}
	if out := books(f.owner, MyBooksRequest{Link: []string{BookDebt}}); !slices.Equal(out.Relinked, []string{BookDebt}) {
		t.Errorf("relink debt = %v", out.Relinked)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM personal_debts WHERE id = $1 AND source_kind = 'event'
		AND source_project_transaction_id = $2 AND project_id = $3 AND unlinked_project_transaction_id IS NULL`, debt, pt, proj); n != 1 {
		t.Errorf("debt not relinked")
	}

	// repay: a repayment row pointing at the row (0.4.1-1 D3: the link sits on the person-book row);
	// unlinking it leaves my bill and my debts alone
	repay := qcMustV7(t)
	if _, err := f.pool.Exec(ctx, `INSERT INTO personal_debts
		(id, user_id, kind, direction, counterparty_person_name, amount, currency, date, source_kind, source_project_transaction_id, project_id)
		VALUES ($1, $2, 'repayment', 'received', 'Grandma', 30, 'THB', '2026-09-02', 'event', $3, $4)`, repay, f.owner, pt, proj); err != nil {
		t.Fatal(err)
	}
	books(f.owner, MyBooksRequest{Unlink: []string{BookRepay}})
	if n := f.countRows(t, `SELECT COUNT(*) FROM personal_debts WHERE id = $1 AND source_project_transaction_id IS NULL
		AND unlinked_project_transaction_id = $2`, repay, pt); n != 1 {
		t.Errorf("repayment not unlinked")
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM personal_debts WHERE id = $1 AND source_project_transaction_id = $2`, debt, pt); n != 1 {
		t.Errorf("unlinking the repayment unlinked the debt")
	}
	if f.boardParentOf(t, f.bill1) != pt {
		t.Errorf("unlinking the repayment moved my bill")
	}
	if out := books(f.owner, MyBooksRequest{Link: []string{BookRepay}}); !slices.Equal(out.Relinked, []string{BookRepay}) {
		t.Errorf("relink repay = %v", out.Relinked)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM personal_debts WHERE id = $1 AND source_project_transaction_id = $2
		AND kind = 'repayment'`, repay, pt); n != 1 {
		t.Errorf("repayment not relinked")
	}

	// a viewer books / unbooks their OWN copy; my bill stays where it is. Bee has a split on the row
	// (bill1's split moved onto the board): her copy goes back to her split.
	if _, err := f.pool.Exec(ctx, `UPDATE project_members SET role = $3 WHERE project_id = $1 AND user_id = $2`,
		proj, f.friend, RoleViewer); err != nil {
		t.Fatal(err)
	}
	var split uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT c.id FROM project_transactions c
		JOIN project_members pm ON pm.id = c.transaction_member_id AND pm.user_id = $2
		WHERE c.parent_project_transaction_id = $1`, pt, f.friend).Scan(&split); err != nil {
		t.Fatalf("Bee's split on the row: %v", err)
	}
	beeCopy := f.insertCopy(t, f.friend, proj, pt, 100)
	books(f.friend, MyBooksRequest{Unlink: []string{BookTx}})
	f.assertLoose(t, beeCopy)
	if f.boardParentOf(t, f.bill1) != pt {
		t.Errorf("a member's unlink touched my bill")
	}
	if out := books(f.friend, MyBooksRequest{Link: []string{BookTx}}); !slices.Equal(out.Relinked, []string{BookTx}) {
		t.Errorf("viewer relink = %v", out.Relinked)
	}
	if got := f.boardParentOf(t, beeCopy); got != split {
		t.Errorf("copy relinked to %s, want Bee's split %s", got, split)
	}
	if l, _ := origin(); l == nil || *l != f.bill1 {
		t.Errorf("a member's relink took the bill origin: %v", l)
	}

	// wrong project / a split row → not found
	if _, err := f.svc.SetMyBooks(ctx, f.owner, proj, split, MyBooksRequest{Link: []string{BookTx}}); !errors.Is(err, ErrPTNotFound) {
		t.Errorf("my-books on a split row: err = %v", err)
	}
	other, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{Name: "Other", TransactionIDs: []uuid.UUID{f.bill2}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetMyBooks(ctx, f.owner, other.Detail.Event.ID, pt, MyBooksRequest{Link: []string{BookTx}}); !errors.Is(err, ErrPTNotFound) {
		t.Errorf("my-books through another event: err = %v", err)
	}
}
