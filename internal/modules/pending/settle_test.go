package pending

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

)

// TestSubmitSettleDebtMakesRepaymentTx — a settle_debt draft submits as a repayment to the debt's
// person: a person-book row (0.4.1-1 D3) with its transaction (a wallet was given).
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
	if row.Type != "income" || row.Amount != 60 || row.Counterparty == nil || row.Counterparty.Kind != "repayment" || row.Counterparty.Key != "n:lee" {
		t.Fatalf("settle row = %+v (counterparty %+v)", row, row.Counterparty)
	}
	if e.balance(t, e.acct) != 1060 {
		t.Errorf("wallet = %v, want 1060", e.balance(t, e.acct))
	}
	var debts int
	var paid bool
	if err := e.pool.QueryRow(context.Background(), `SELECT COUNT(*), BOOL_OR(paid) FROM personal_debts WHERE user_id = $1`, e.uid).
		Scan(&debts, &paid); err != nil || debts != 2 || paid {
		t.Errorf("personal_debts = %d rows, paid %v (%v); want the debt + the repayment row (0.4.1-1 D3), label untouched", debts, paid, err)
	}
}
