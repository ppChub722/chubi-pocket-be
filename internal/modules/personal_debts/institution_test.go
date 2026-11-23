package personal_debts

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"
)

// 0.4.3: an institution is a debt counterparty (key i:<id>): its rows, a payment of one of its
// plan items ticking an installment, the list's sections and preview rows, its card debts.
func TestInstitutionCounterparty(t *testing.T) {
	f := newDebtHFix(t)
	inst := uuid.New()
	f.exec(`INSERT INTO institutions (id, user_id, name, kind) VALUES ($1, $2, 'ไฟแนนซ์รถ', 'finance')`, inst, f.me)
	key := "i:" + inst.String()
	page := "/api/v1/personal-debts/people/" + url.PathEscape(key)

	// the page exists before any row (an institution of mine), with nothing in it
	p := f.call("GET", page, nil, "getDebtPerson", http.StatusOK)
	if p["kind"] != "institution" || obj(p, "institution")["id"] != inst.String() || num(p, "net") != 0 {
		t.Fatalf("empty institution page = %v", p)
	}
	f.code("GET", "/api/v1/personal-debts/people/"+url.PathEscape("i:"+uuid.NewString()), nil, http.StatusNotFound, "NOT_FOUND")

	// a recurring item with a plan of this institution
	item, plan := uuid.New(), uuid.New()
	f.exec(`INSERT INTO debt_plans (id, user_id, name, direction, institution_id, principal, count, start_date, interest_method)
		VALUES ($1, $2, 'ผ่อนรถ', 'received', $3, 30000, 3, CURRENT_DATE, 'none')`, plan, f.me, inst)
	f.exec(`INSERT INTO recurrings (id, user_id, name, type, debt_direction, debt_plan_id)
		VALUES ($1, $2, 'ผ่อนรถ', 'debt', 'received', $3)`, item, f.me, plan)
	for i := 1; i <= 3; i++ {
		f.exec(`INSERT INTO debt_installments (user_id, debt_plan_id, seq, due_date, amount, principal, interest)
			VALUES ($1, $2, $3, CURRENT_DATE + ($3 - 1) * 30, 10000, 10000, 0)`, f.me, plan, i)
	}
	debt := f.call("POST", page+"/entries", map[string]any{"direction": "received", "amount": 30000, "recurring_id": item,
		"due_date": f.today()}, "createDebtEntry", http.StatusCreated)
	if obj(debt, "entry")["key"] != key || obj(debt, "entry", "recurring")["id"] != item.String() || num(debt, "person_balance", "net") != -30000 {
		t.Fatalf("institution debt = %v", debt)
	}
	// a repayment of the item ticks the oldest open installment
	rep := f.call("POST", page+"/repayments", map[string]any{"kind": "pay", "amount": 10000, "recurring_id": item},
		"createDebtRepayment", http.StatusCreated)
	var seq *int
	_ = f.pool.QueryRow(f.ctx, `SELECT seq FROM debt_installments WHERE personal_debt_id = $1`, obj(rep, "entry")["id"]).Scan(&seq)
	if seq == nil || *seq != 1 || num(rep, "person_balance", "net") != -20000 {
		t.Errorf("repayment ticked %v, net %v", seq, num(rep, "person_balance", "net"))
	}
	if r := obj(rep, "entry", "recurring"); r["installment_seq"] != 1.0 || r["installment_count"] != 3.0 {
		t.Errorf("repayment tag = %v", r)
	}
	// an item of another counterparty is refused
	f.code("POST", "/api/v1/personal-debts/people/n:mom/entries", map[string]any{"direction": "paid", "amount": 5, "recurring_id": item},
		http.StatusBadRequest, "VALIDATION_ERROR")

	// the list: sections, the preview rows
	f.call("POST", "/api/v1/personal-debts/people/n:mom/entries", map[string]any{"direction": "paid", "amount": 50}, "createDebtEntry", http.StatusCreated)
	insts := f.call("GET", "/api/v1/personal-debts/people?section=institution&sort=due", nil, "listDebtPeople", http.StatusOK)
	rows, _ := insts["data"].([]any)
	if len(rows) != 1 {
		t.Fatalf("institution section = %v", insts["data"])
	}
	row := rows[0].(map[string]any)
	prev, _ := row["entries_preview"].([]any)
	if row["kind"] != "institution" || obj(row, "person")["name"] != "ไฟแนนซ์รถ" || len(prev) != 1 ||
		obj(prev[0].(map[string]any), "recurring")["id"] != item.String() {
		t.Errorf("institution row = %v", row)
	}
	people := f.call("GET", "/api/v1/personal-debts/people?section=person", nil, "listDebtPeople", http.StatusOK)
	if rows, _ := people["data"].([]any); len(rows) != 1 || rows[0].(map[string]any)["key"] != "n:mom" {
		t.Errorf("person section = %v", people["data"])
	}
	f.code("GET", "/api/v1/personal-debts/people?section=bank", nil, http.StatusBadRequest, "VALIDATION_ERROR")

	// a card of the institution: what is owed minus the installments not billed yet
	f.exec(`UPDATE accounts SET type = 'credit_card', credit_limit = 50000, balance = -12000, institution_id = $2 WHERE id = $1`, f.wallet, inst)
	card := uuid.New()
	f.exec(`INSERT INTO debt_plans (id, user_id, name, direction, account_id, principal, count, start_date, interest_method)
		VALUES ($1, $2, 'ผ่อนทีวี', 'received', $3, 9000, 3, CURRENT_DATE, 'none')`, card, f.me, f.wallet)
	for i := 1; i <= 3; i++ {
		f.exec(`INSERT INTO debt_installments (user_id, debt_plan_id, seq, due_date, amount, principal, interest)
			VALUES ($1, $2, $3, CURRENT_DATE + ($3 - 1) * 30, 3000, 3000, 0)`, f.me, card, i)
	}
	p = f.call("GET", page, nil, "getDebtPerson", http.StatusOK)
	cds, _ := p["card_debts"].([]any)
	if len(cds) != 1 || num(cds[0].(map[string]any), "amount") != 12000-6000 || num(p, "net") != -20000 {
		t.Errorf("card debts = %v net %v", p["card_debts"], num(p, "net"))
	}
	// the row page keeps its tag
	got := f.call("GET", "/api/v1/personal-debts/"+obj(debt, "entry")["id"].(string), nil, "getDebtEntry", http.StatusOK)
	if obj(got, "entry", "recurring")["name"] != "ผ่อนรถ" {
		t.Errorf("row page recurring = %v", obj(got, "entry", "recurring"))
	}
	// deleting the item makes its rows general
	f.exec(`DELETE FROM recurrings WHERE id = $1`, item)
	got = f.call("GET", "/api/v1/personal-debts/"+obj(debt, "entry")["id"].(string), nil, "getDebtEntry", http.StatusOK)
	if obj(got, "entry")["recurring"] != nil || num(got, "person_balance", "net") != -20000 {
		t.Errorf("after the item went: %v", obj(got, "entry"))
	}
}

func (f *debtHFix) today() string {
	var d string
	_ = f.pool.QueryRow(f.ctx, `SELECT to_char(CURRENT_DATE, 'YYYY-MM-DD')`).Scan(&d)
	return d
}
