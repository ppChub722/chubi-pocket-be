package pending

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

// TestSubmitSettleDebtMakesRepaymentTx — a settle_debt draft submits as a repayment TRANSACTION to
// the debt's person (0.4.0-3), not as a personal_debts row.
func TestSubmitSettleDebtMakesRepaymentTx(t *testing.T) {
	e := newAPIEnv(t)
	for _, c := range [][2]string{{"DEBT_PAID", "expense"}, {"DEBT_RECEIVED", "income"}} {
		e.exec(t, `INSERT INTO categories (id, user_id, name, type, is_system, system_kind, include_in_report)
			VALUES ($1, $2, $3, $4, TRUE, $3, FALSE)`, uuid.New(), e.uid, c[0], c[1])
	}
	debtID := uuid.New()
	e.exec(t, `INSERT INTO personal_debts (id, user_id, direction, counterparty_person_name, amount, currency, date,
		created_by_user_id, updated_by_user_id) VALUES ($1, $2, 'paid', 'Lee', 100, 'THB', '2026-10-01', $2, $2)`, debtID, e.uid)

	amount, date := 60.0, "2026-10-12"
	p, err := NewStore(e.pool).Insert(context.Background(), e.uid, SourceManual, KindSettleDebt,
		Draft{Amount: &amount, AccountID: &e.acct, Date: &date}, &debtID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var sub SubmitResponse
	e.call(t, "submitPending", "POST", "/pending-transactions/submit", map[string]any{"ids": []uuid.UUID{p.ID}}, http.StatusOK, &sub)
	if len(sub.Submitted) != 1 || len(sub.Submitted[0].Rows) != 1 {
		t.Fatalf("submit: %+v", sub)
	}
	row := sub.Submitted[0].Rows[0]
	if row.Type != "income" || row.Amount != 60 || row.RepaymentPerson == nil || row.RepaymentPerson.Key != "n:lee" {
		t.Fatalf("settle row = %+v (repayment_person %+v)", row, row.RepaymentPerson)
	}
	if e.balance(t, e.acct) != 1060 {
		t.Errorf("wallet = %v, want 1060", e.balance(t, e.acct))
	}
	// A paid_by draft (0.4.0-3) submits as one expense + the debt "I owe them".
	bill, lee := 123.0, 13.0
	expense := "expense"
	pb, err := NewStore(e.pool).Insert(context.Background(), e.uid, SourceManual, KindCreate,
		Draft{Type: &expense, Amount: &bill, AccountID: &e.acct, Date: &date,
			PaidBy: &transactions.PaidByInput{PersonName: "Lee", Amount: lee}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var list ListResponse
	e.call(t, "listPending", "GET", "/pending-transactions", nil, http.StatusOK, &list)
	for _, d := range list.Data {
		if d.ID == pb.ID && (d.Resolved.LinkMode != transactions.LinkPaid || d.Resolved.PaidBy == nil ||
			d.Resolved.PaidBy.Person.Name != "Lee" || d.Resolved.PaidBy.MyPaid == nil || *d.Resolved.PaidBy.MyPaid != 110) {
			t.Errorf("resolved paid_by draft = %+v / %+v", d.Resolved, d.Resolved.PaidBy)
		}
	}
	var sub2 SubmitResponse
	e.call(t, "submitPending", "POST", "/pending-transactions/submit", map[string]any{"ids": []uuid.UUID{pb.ID}}, http.StatusOK, &sub2)
	if len(sub2.Submitted) != 1 || sub2.Submitted[0].Rows[0].PaidBy == nil || sub2.Submitted[0].Rows[0].WalletAmount != 110 {
		t.Fatalf("submit paid_by draft: %+v", sub2)
	}
	if e.balance(t, e.acct) != 950 {
		t.Errorf("wallet = %v, want 950 (1060 − 110)", e.balance(t, e.acct))
	}
	if _, err := e.pool.Exec(context.Background(), `DELETE FROM personal_debts WHERE source_kind = 'paid_by' AND user_id = $1`, e.uid); err != nil {
		t.Fatal(err)
	}

	var debts int
	var paid bool
	if err := e.pool.QueryRow(context.Background(), `SELECT COUNT(*), BOOL_OR(paid) FROM personal_debts WHERE user_id = $1`, e.uid).
		Scan(&debts, &paid); err != nil || debts != 1 || paid {
		t.Errorf("personal_debts = %d rows, paid %v (%v); want the one debt, label untouched", debts, paid, err)
	}
}
