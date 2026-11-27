package recurring

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// do — a call as is (no 0.4.2 shim).
func (f *recFix) do(method, path, op string, body any, want int, out any) []byte {
	f.t.Helper()
	if body != nil && op != "" && (method == "POST" || method == "PUT") {
		apitest.ConformRequest(f.t, op, body)
	}
	status, raw := apitest.Do(f.t, f.r, method, "/api/v1"+path, body)
	if status != want {
		f.t.Fatalf("%s %s: status %d, want %d: %s", method, path, status, want, raw)
	}
	if op != "" {
		apitest.Conform(f.t, op, status, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			f.t.Fatal(err)
		}
	}
	return raw
}

// seedCategories — the user's starter + system categories (debt rows with a wallet need them).
func (f *recFix) seedCategories() {
	f.t.Helper()
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := categories.NewService(categories.NewStore(f.pool)).SeedForUser(f.ctx, tx, f.me); err != nil {
		f.t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		f.t.Fatal(err)
	}
}

func (f *recFix) monthly(day int, start string, end map[string]any) map[string]any {
	sc := map[string]any{"freq": "monthly", "interval": 1, "month_days": []int{day}, "start_date": start}
	if end != nil {
		sc["end"] = end
	}
	return sc
}

// expenseRule — an expense rule from the wallet in the food category.
func (f *recFix) expenseRule(amount float64, schedule map[string]any) map[string]any {
	return map[string]any{"type": "expense", "category_id": f.food, "amount": amount,
		"source": map[string]any{"account_id": f.wallet}, "schedule": schedule}
}

// One recurring, several rules: each its own type, source, amount and schedule; rules are added,
// edited, paused and deleted on their own; the last one stays.
func TestRecurringRules(t *testing.T) {
	f := newRecFix(t)
	body := map[string]any{"name": "ค่าเช่า", "tracking_type": "bill", "rules": []any{
		f.expenseRule(6000, f.monthly(1, f.day(-40), nil)),
		map[string]any{"type": "income", "amount": 500, "source": map[string]any{"account_id": f.wallet},
			"schedule": map[string]any{"freq": "weekly", "interval": 1, "weekdays": []int{6}, "start_date": f.day(-10)}},
	}}
	var p ItemPage
	f.do("POST", "/recurring", "createRecurring", body, http.StatusCreated, &p)
	if len(p.Lines) != 2 || p.Lines[0].ID != p.ID || p.TrackingType != "bill" || p.Status != "active" || len(p.Touches) != 0 {
		t.Fatalf("created = %+v", p)
	}
	if p.Lines[0].Type != TypeExpense || p.Lines[0].Category == nil || p.Lines[1].Type != TypeIncome || p.Lines[1].Category != nil ||
		p.Lines[0].Seq != 1 || p.Lines[1].Seq != 2 {
		t.Errorf("rules = %+v / %+v", p.Lines[0], p.Lines[1])
	}
	second := p.Lines[1].ID

	// the list shows the recurring once, self-made; tracking_type filters
	var list SummaryList
	f.do("GET", "/recurring", "listRecurring", nil, 200, &list)
	if len(list.Data) != 1 || list.Data[0].LinesCount != 2 || list.Data[0].Amount != nil || list.Data[0].Related {
		t.Fatalf("list = %+v", list.Data)
	}
	f.do("GET", "/recurring?tracking_type=subscription", "listRecurring", nil, 200, &list)
	if len(list.Data) != 0 {
		t.Errorf("tracking filter = %+v", list.Data)
	}
	f.do("GET", "/recurring?tracking_type=nope", "listRecurring", nil, 400, nil)

	// rename: the rounds take the recurring's name; pause one rule: the recurring stays active
	f.do("PUT", "/recurring/"+p.ID.String(), "updateRecurring", map[string]any{"name": "ค่าบ้าน", "tracking_type": "bill"}, 200, &p)
	if p.Name != "ค่าบ้าน" || len(p.Lines) != 2 {
		t.Errorf("rename = %s, %d rules", p.Name, len(p.Lines))
	}
	if r, _ := getRule(f.ctx, f.pool, f.me, second, false); r == nil || r.Name != "ค่าบ้าน" {
		t.Errorf("a rule reads its recurring's name: %+v", r)
	}
	f.do("POST", "/recurring/"+p.ID.String()+"/rules/"+second.String()+"/pause", "pauseRecurringRule", nil, 200, &p)
	if p.Status != "active" || p.Lines[1].Status != "paused" {
		t.Errorf("after rule pause: recurring %s rule %s", p.Status, p.Lines[1].Status)
	}
	f.do("POST", "/recurring/"+p.ID.String()+"/rules/"+p.ID.String()+"/pause", "pauseRecurringRule", nil, 200, &p)
	if p.Status != "paused" {
		t.Errorf("both rules paused: recurring %s", p.Status)
	}
	f.do("POST", "/recurring/"+p.ID.String()+"/resume", "resumeRecurring", nil, 200, &p)
	if p.Status != "active" || p.Lines[0].Status != "active" || p.Lines[1].Status != "active" {
		t.Errorf("resume: %s / %s / %s", p.Status, p.Lines[0].Status, p.Lines[1].Status)
	}

	// add a rule (several days a month), edit it (a full replace), delete it; the last rule can't go
	sc := map[string]any{"freq": "monthly", "interval": 1, "month_days": []int{8, 16, -1}, "start_date": f.day(0)}
	f.do("POST", "/recurring/"+p.ID.String()+"/rules", "addRecurringRule", f.expenseRule(99, sc), 200, &p)
	if len(p.Lines) != 3 || len(p.Lines[2].Schedule.MonthDays) != 3 {
		t.Fatalf("rules after add = %d %+v", len(p.Lines), p.Lines[len(p.Lines)-1].Schedule)
	}
	third := p.Lines[2].ID
	edit := f.expenseRule(120, nil)
	delete(edit, "schedule")
	f.do("PUT", "/recurring/"+p.ID.String()+"/rules/"+third.String(), "updateRecurringRule", edit, 200, &p)
	if *p.Lines[2].Amount != 120 || p.Lines[2].Schedule.Freq != "monthly" {
		t.Errorf("rule edit = %+v", p.Lines[2])
	}
	bad := f.expenseRule(1, nil)
	delete(bad, "schedule")
	bad["source"] = map[string]any{"account_id": f.wallet, "paid_by": map[string]any{"person_name": "x"}}
	f.do("PUT", "/recurring/"+p.ID.String()+"/rules/"+third.String(), "updateRecurringRule", bad, 400, nil)
	f.do("DELETE", "/recurring/"+p.ID.String()+"/rules/"+third.String(), "deleteRecurringRule", nil, 200, &p)
	f.do("DELETE", "/recurring/"+p.ID.String()+"/rules/"+second.String(), "deleteRecurringRule", nil, 200, &p)
	raw := f.do("DELETE", "/recurring/"+p.ID.String()+"/rules/"+p.ID.String(), "deleteRecurringRule", nil, 409, nil)
	if !contains(raw, "RECURRING_LAST_REMINDER") {
		t.Errorf("last rule: %s", raw)
	}
	f.do("POST", "/recurring/"+p.ID.String()+"/rules/"+uuid.NewString()+"/occurrences/"+f.day(0)+"/skip", "skipRecurringOccurrence", nil, 404, nil)

	// rounds of every rule in one list
	var rounds ItemList
	f.do("GET", "/recurring/"+p.ID.String()+"/items?tab=produced", "listRecurringRuleItems", nil, 200, &rounds)
	for _, r := range rounds.Data {
		if r.RuleID != p.ID || r.Recurring == nil || r.Recurring.ID != p.ID {
			t.Errorf("round %+v not of the recurring", r)
		}
	}

	// delete the recurring: what it wrote stays
	f.do("DELETE", "/recurring/"+p.ID.String(), "deleteRecurring", nil, 200, nil)
	f.do("GET", "/recurring/"+p.ID.String(), "getRecurring", nil, 404, nil)
}

// A person-paid rule (คนจ่ายแทน): each round = my expense without wallet money + my debt to that
// person, through one จ่ายแทน paper; the debt row belongs to the recurring.
func TestRecurringPaidByLine(t *testing.T) {
	f := newRecFix(t)
	before := f.balance()
	rule := map[string]any{"type": "expense", "category_id": f.food, "amount": 6000,
		"source": map[string]any{"paid_by": map[string]any{"person_name": "แฟน"}}, "schedule": f.monthly(f.today.Day(), f.day(0), nil)}
	var p ItemPage
	f.do("POST", "/recurring", "createRecurring", map[string]any{"name": "ค่าเช่า", "rules": []any{rule}}, http.StatusCreated, &p)
	if p.Lines[0].PaidBy == nil || p.Lines[0].PaidBy.Name != "แฟน" || p.Lines[0].Source.AccountID != nil {
		t.Fatalf("rule = %+v", p.Lines[0])
	}
	var res ItemWriteResult
	f.do("POST", "/recurring/"+p.ID.String()+"/rules/"+p.ID.String()+"/occurrences/"+f.day(0)+"/confirm", "confirmRecurringOccurrence",
		map[string]any{}, 200, &res)
	if res.Item.Status != StatusDone || res.Item.Transaction == nil {
		t.Fatalf("round = %+v", res.Item)
	}
	var (
		cat     *uuid.UUID
		account *uuid.UUID
	)
	if err := f.pool.QueryRow(f.ctx, `SELECT category_id, account_id FROM transactions WHERE id = $1`, res.Item.Transaction.ID).Scan(&cat, &account); err != nil {
		t.Fatal(err)
	}
	if cat == nil || *cat != f.food || account != nil || f.balance() != before {
		t.Errorf("my tx category %v account %v, balance %v → %v", cat, account, before, f.balance())
	}
	var (
		amount float64
		dir    string
		rec    *uuid.UUID
	)
	if err := f.pool.QueryRow(f.ctx, `SELECT amount::float8, direction, recurring_id FROM personal_debts WHERE user_id = $1 AND kind = 'debt'`,
		f.me).Scan(&amount, &dir, &rec); err != nil {
		t.Fatal(err)
	}
	if amount != 6000 || dir != "received" || rec == nil || *rec != p.ID {
		t.Errorf("debt = %v %s recurring %v", amount, dir, rec)
	}

	// a person can't pay an income for me
	bad := map[string]any{"name": "x", "rules": []any{map[string]any{"type": "income", "amount": 1,
		"source": map[string]any{"paid_by": map[string]any{"person_name": "a"}}, "schedule": f.monthly(1, f.day(0), nil)}}}
	if raw := f.do("POST", "/recurring", "createRecurring", bad, 400, nil); !contains(raw, "RECURRING_SOURCE_INVALID") {
		t.Errorf("paid_by income: %s", raw)
	}

	// deleting the recurring keeps its debt rows as general debts
	f.do("DELETE", "/recurring/"+p.ID.String(), "deleteRecurring", nil, 200, nil)
	_ = f.pool.QueryRow(f.ctx, `SELECT recurring_id FROM personal_debts WHERE user_id = $1 AND kind = 'debt'`, f.me).Scan(&rec)
	if rec != nil {
		t.Errorf("debt still tied to a deleted recurring")
	}
}

// A debt plan (a cash loan to a person): the opening row counts at once; a debt rule pays it (each
// round = the oldest open installment); edit / recalc / mark / pay with interest / close.
func TestDebtPlan(t *testing.T) {
	f := newRecFix(t)
	var prev InstallmentList
	f.do("POST", "/debt-plans/preview", "previewDebtPlan", map[string]any{"principal": 10000, "count": 3,
		"start_date": f.day(0), "interest_method": "none"}, 200, &prev)
	if len(prev.Data) != 3 || prev.Data[0].Principal != 3333.33 || prev.Data[2].Principal != 3333.34 {
		t.Fatalf("preview = %+v", prev.Data)
	}
	var red InstallmentList
	f.do("POST", "/debt-plans/preview", "previewDebtPlan", map[string]any{"principal": 12000, "count": 12,
		"start_date": f.day(0), "interest_method": "reducing", "interest_rate": 12}, 200, &red)
	sum := 0.0
	for _, x := range red.Data {
		sum += x.Principal
	}
	if len(red.Data) != 12 || red.Data[0].Interest != 120 || round2(sum) != 12000 || red.Data[11].Interest >= red.Data[0].Interest {
		t.Errorf("reducing = %+v (principal %v)", red.Data, sum)
	}
	if raw := f.do("POST", "/debt-plans/preview", "previewDebtPlan", map[string]any{"principal": 1, "count": 0,
		"start_date": f.day(0), "interest_method": "none"}, 400, nil); !contains(raw, "PLAN_INVALID") {
		t.Errorf("bad plan: %s", raw)
	}

	f.seedCategories()
	before := f.balance()
	var pl PlanView
	f.do("POST", "/debt-plans", "createDebtPlan", map[string]any{"name": "ให้ยืม", "direction": "paid", "origin": "cash",
		"creditor": map[string]any{"person_name": "บอย"},
		"plan":     map[string]any{"principal": 10000, "count": 5, "start_date": f.day(-40), "interest_method": "none", "opening_account_id": f.wallet}},
		http.StatusCreated, &pl)
	if pl.Count != 5 || pl.Done != 0 || len(pl.Due) != 2 || pl.LeftTotal != 10000 || pl.Creditor.Kind != "person" || pl.Origin != "cash" {
		t.Fatalf("plan = %+v", pl)
	}
	if f.balance() != before-10000 {
		t.Errorf("the opening row's wallet tx: balance %v → %v", before, f.balance())
	}
	// the rule that pays it; a round pays (here: collects) the oldest open installment
	rule := map[string]any{"type": "debt", "debt_plan_id": pl.ID, "source": map[string]any{"account_id": f.wallet},
		"schedule": f.monthly(f.today.Day(), f.day(0), map[string]any{"kind": "count", "count": 5})}
	var p ItemPage
	f.do("POST", "/recurring", "createRecurring", map[string]any{"name": "ให้ยืม", "rules": []any{rule}}, http.StatusCreated, &p)
	ln := p.Lines[0]
	if p.Plan == nil || p.Plan.ID != pl.ID || ln.Counterparty == nil || ln.Counterparty.Net != 10000 || ln.Action != ActionCollect ||
		ln.AmountMode != "schedule" || len(p.Touches) != 1 || p.Touches[0].Kind != "plan" {
		t.Fatalf("plan recurring = %+v plan %+v rule %+v", p, p.Plan, ln)
	}
	var res ItemWriteResult
	f.do("POST", "/recurring/"+p.ID.String()+"/rules/"+p.ID.String()+"/occurrences/"+f.day(0)+"/confirm", "confirmRecurringOccurrence",
		map[string]any{}, 200, &res)
	f.do("GET", "/recurring/"+p.ID.String(), "getRecurring", nil, 200, &p)
	if p.Plan.Done != 1 || p.Plan.Chain[0].Status != "paid" || p.Lines[0].Counterparty.Net != 8000 {
		t.Errorf("after a payment: plan %+v net %v", p.Plan.Chain, p.Lines[0].Counterparty.Net)
	}
	// mark / unmark by hand, edit one
	id := pl.ID.String()
	f.do("POST", "/debt-plans/"+id+"/installments/2/mark", "markDebtPlanInstallment", map[string]any{"paid": true}, 200, &pl)
	f.do("POST", "/debt-plans/"+id+"/installments/2/mark", "markDebtPlanInstallment", map[string]any{"paid": false}, 200, &pl)
	f.do("PUT", "/debt-plans/"+id+"/installments/3", "updateDebtPlanInstallment", map[string]any{"amount": 2500, "interest": 500}, 200, &pl)
	var inst InstallmentList
	f.do("GET", "/debt-plans/"+id+"/installments", "listDebtPlanInstallments", nil, 200, &inst)
	if inst.Data[1].Status == "paid" || inst.Data[2].Principal != 2000 || inst.Data[2].Interest != 500 || !inst.Data[2].Edited ||
		inst.Data[3].Principal != 2000 {
		t.Errorf("installments (no recalc unless asked) = %+v", inst.Data)
	}
	// recalc_rest: the user said yes — the rest is rebuilt from the real remaining principal
	f.do("PUT", "/debt-plans/"+id+"/installments/3", "updateDebtPlanInstallment", map[string]any{"principal": 3000, "interest": 0,
		"recalc_rest": true}, 200, &pl)
	f.do("GET", "/debt-plans/"+id+"/installments", "listDebtPlanInstallments", nil, 200, &inst)
	if inst.Data[3].Principal != 1500 || inst.Data[4].Principal != 1500 || inst.Data[3].Edited {
		t.Errorf("recalc (10000 − 2000 − 2000 − 3000 over 2) = %+v", inst.Data)
	}
	f.do("POST", "/debt-plans/"+id+"/installments/9/mark", "markDebtPlanInstallment", map[string]any{"paid": true}, 404, nil)
	// ข้าม: installment 4 moves to the end (6th), the plan still adds up
	f.do("POST", "/debt-plans/"+id+"/installments/4/skip", "skipDebtPlanInstallment", nil, 200, &pl)
	if pl.Count != 6 || pl.Chain[3].Status != "skipped" || len(pl.Installments) != 6 || pl.Installments[5].Principal != 1500 {
		t.Errorf("after skip: count %d chain %+v", pl.Count, pl.Chain)
	}
	f.do("POST", "/debt-plans/"+id+"/installments/4/skip", "skipDebtPlanInstallment", nil, 409, nil)
	// pay installment 3 from the debts page: principal 3000 back
	before = f.balance()
	f.do("POST", "/debt-plans/"+id+"/pay", "payDebtPlanInstallment", map[string]any{"seq": 3, "account_id": f.wallet}, 200, &pl)
	if pl.Done != 2 || pl.Chain[2].Status != "paid" || f.balance() != before+3000 {
		t.Errorf("after pay: done %d chain %+v balance %v → %v", pl.Done, pl.Chain, before, f.balance())
	}
	// closing the plan stops its rule
	f.do("POST", "/debt-plans/"+id+"/close", "closeDebtPlan", nil, 200, &pl)
	if !pl.Closed || pl.LeftCount != 0 || pl.Done != 2 {
		t.Errorf("closed plan = %+v", pl)
	}
	f.do("GET", "/recurring/"+p.ID.String(), "getRecurring", nil, 200, &p)
	if p.Lines[0].Status != "archived" || p.Status != "archived" {
		t.Errorf("after close: rule %s recurring %s", p.Lines[0].Status, p.Status)
	}
	f.do("POST", "/debt-plans/"+id+"/close", "closeDebtPlan", nil, 409, nil)
	var list PlanList
	f.do("GET", "/debt-plans", "listDebtPlans", nil, 200, &list)
	if len(list.Data) != 1 || list.Data[0].RecurringID == nil {
		t.Errorf("list = %+v", list.Data)
	}
	f.do("DELETE", "/debt-plans/"+id, "deleteDebtPlan", nil, http.StatusNoContent, nil)
	f.do("GET", "/recurring/"+p.ID.String(), "getRecurring", nil, 404, nil) // left with no rule, it went too
}

// A purchase on credit from an institution, with interest: the full price is my expense today, a
// ledger-only debt row; paying an installment books the principal back + ดอกเบี้ย as my expense.
func TestDebtPlanPurchase(t *testing.T) {
	f := newRecFix(t)
	f.seedCategories()
	inst := uuid.New()
	f.exec(`INSERT INTO institutions (id, user_id, name, kind) VALUES ($1, $2, 'Toyota Leasing', 'finance')`, inst, f.me)
	before := f.balance()
	var pl PlanView
	f.do("POST", "/debt-plans", "createDebtPlan", map[string]any{"name": "ผ่อนรถ", "category_id": f.food,
		"creditor": map[string]any{"institution_id": inst},
		"plan":     map[string]any{"principal": 12000, "count": 12, "start_date": f.day(0), "interest_method": "flat", "interest_rate": 10}},
		http.StatusCreated, &pl)
	if pl.Direction != "received" || pl.Origin != "purchase" || pl.Purchase == nil || pl.Purchase.Amount != 12000 || pl.Creditor.Kind != "institution" {
		t.Fatalf("plan = %+v", pl)
	}
	if f.balance() != before {
		t.Errorf("a purchase on credit moved my wallet: %v → %v", before, f.balance())
	}
	f.do("POST", "/debt-plans/"+pl.ID.String()+"/pay", "payDebtPlanInstallment", map[string]any{"account_id": f.wallet}, 200, &pl)
	var interest float64
	_ = f.pool.QueryRow(f.ctx, `SELECT COALESCE(SUM(t.amount), 0)::float8 FROM transactions t JOIN categories c ON c.id = t.category_id
		WHERE t.user_id = $1 AND c.system_kind = 'INTEREST_PAID'`, f.me).Scan(&interest)
	if pl.Done != 1 || interest != 100 || f.balance() != before-1100 {
		t.Errorf("after pay: done %d interest %v balance %v → %v", pl.Done, interest, before, f.balance())
	}
	// recalc on flat keeps each round's interest, spreads the principal left
	f.do("PUT", "/debt-plans/"+pl.ID.String()+"/installments/2", "updateDebtPlanInstallment", map[string]any{"principal": 2000,
		"interest": 100, "recalc_rest": true}, 200, &pl)
	var inst2 InstallmentList
	f.do("GET", "/debt-plans/"+pl.ID.String()+"/installments", "listDebtPlanInstallments", nil, 200, &inst2)
	if inst2.Data[2].Principal != 900 || inst2.Data[2].Interest != 100 {
		t.Errorf("flat recalc (12000 − 1000 − 2000 over 10) = %+v", inst2.Data[2])
	}
	// a purchase is mine to owe
	f.do("POST", "/debt-plans", "createDebtPlan", map[string]any{"name": "x", "direction": "paid",
		"creditor": map[string]any{"person_name": "บอย"},
		"plan":     map[string]any{"principal": 100, "count": 1, "start_date": f.day(0), "interest_method": "none"}}, 400, nil)
}

// A card plan: the card is the creditor, no debt row, no rule (its bill pays it); the card's bill: a
// transfer rule into the card each round, asking its amount.
func TestCardPlanAndBill(t *testing.T) {
	f := newRecFix(t)
	f.seedCategories()
	card := testdb.Account(t, f.pool, f.me)
	f.exec(`UPDATE accounts SET type = 'credit_card', credit_limit = 50000, balance = 0 WHERE id = $1`, card)
	var pl PlanView
	f.do("POST", "/debt-plans", "createDebtPlan", map[string]any{"name": "ผ่อนคอม", "creditor": map[string]any{"account_id": card},
		"plan": map[string]any{"principal": 42000, "count": 12, "start_date": f.day(10), "interest_method": "none"}},
		http.StatusCreated, &pl)
	if pl.Creditor.Kind != "card" || pl.Creditor.Account == nil || pl.Purchase != nil {
		t.Fatalf("card plan = %+v", pl)
	}
	var n int
	_ = f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM personal_debts WHERE user_id = $1`, f.me).Scan(&n)
	if n != 0 {
		t.Errorf("a card plan wrote %d debt rows", n)
	}
	f.do("POST", "/debt-plans/"+pl.ID.String()+"/pay", "payDebtPlanInstallment", map[string]any{"account_id": f.wallet}, 409, nil)
	sched := f.monthly(f.today.Day(), f.day(0), nil)
	f.do("POST", "/recurring", "createRecurring", map[string]any{"name": "x", "rules": []any{map[string]any{"type": "debt",
		"debt_plan_id": pl.ID, "source": map[string]any{"account_id": f.wallet}, "schedule": sched}}}, 400, nil)
	// the card's bill
	bill := map[string]any{"type": "transfer", "to_account_id": card, "amount_mode": "ask", "source": map[string]any{"account_id": f.wallet},
		"schedule": sched}
	var p ItemPage
	f.do("POST", "/recurring", "createRecurring", map[string]any{"name": "บิล KTC", "tracking_type": "credit_card", "rules": []any{bill}},
		http.StatusCreated, &p)
	if p.Lines[0].Action != ActionTransfer || p.Lines[0].AmountMode != AmountAsk || len(p.Touches) != 1 || p.Touches[0].Kind != "card" ||
		p.Lines[0].AutoAcceptOn {
		t.Fatalf("card bill = %+v lines %+v", p, p.Lines)
	}
	before := f.balance()
	var res ItemWriteResult
	f.do("POST", "/recurring/"+p.ID.String()+"/rules/"+p.Lines[0].ID.String()+"/occurrences/"+f.day(0)+"/confirm", "confirmRecurringOccurrence",
		map[string]any{"amount": 5595}, 200, &res)
	var cardBal float64
	_ = f.pool.QueryRow(f.ctx, `SELECT balance::float8 FROM accounts WHERE id = $1`, card).Scan(&cardBal)
	if f.balance() != before-5595 || cardBal != 5595 {
		t.Errorf("bill paid: wallet %v → %v, card %v", before, f.balance(), cardBal)
	}
}

// The card summary: last cycle's spending (a plan purchase left out) + the plan installments due in
// it; a bill rule round starts with what is left.
func TestCardSummaryAndPrefill(t *testing.T) {
	f := newRecFix(t)
	f.seedCategories()
	card := testdb.Account(t, f.pool, f.me)
	day := f.today.AddDate(0, 0, -1).Day() // the statement closed yesterday
	if day > 28 {
		t.Skip("month-end statement days clamp: covered by cardCycle")
	}
	f.exec(`UPDATE accounts SET type = 'credit_card', credit_limit = 50000, statement_date = $2 WHERE id = $1`, card, day)
	spend := map[string]any{"type": "expense", "account_id": card, "amount": 2095, "category_id": f.food, "date": f.day(-3)}
	f.do("POST", "/transactions", "createTransaction", spend, http.StatusCreated, nil)
	var pl PlanView
	f.do("POST", "/debt-plans", "createDebtPlan", map[string]any{"name": "ผ่อนคอม", "creditor": map[string]any{"account_id": card},
		"plan": map[string]any{"principal": 42000, "count": 12, "start_date": f.day(-2), "interest_method": "none"}}, http.StatusCreated, &pl)
	var sm CardSummary
	f.do("GET", "/card-summaries/"+card.String(), "getCardSummary", nil, 200, &sm)
	if sm.Bill.Spending != 2095 || sm.Bill.Installments != 3500 || sm.Bill.Left != 5595 || len(sm.Plans) != 1 || sm.Unbilled != 38500 {
		t.Fatalf("summary = %+v bill %+v", sm, sm.Bill)
	}
	var list CardSummaryList
	f.do("GET", "/card-summaries", "listCardSummaries", nil, 200, &list)
	if len(list.Data) != 1 {
		t.Errorf("cards = %+v", list.Data)
	}
	var p ItemPage
	f.do("POST", "/recurring", "createRecurring", map[string]any{"name": "บิล KTC", "rules": []any{map[string]any{"type": "transfer",
		"to_account_id": card, "amount_mode": "ask", "source": map[string]any{"account_id": f.wallet},
		"schedule": f.monthly(f.today.Day(), f.day(0), nil)}}}, http.StatusCreated, &p)
	var res ItemWriteResult
	f.do("POST", "/recurring/"+p.ID.String()+"/rules/"+p.Lines[0].ID.String()+"/occurrences/"+f.day(0)+"/confirm", "confirmRecurringOccurrence",
		map[string]any{"bill_diff": map[string]any{"amount": 0, "category": "other"}}, 400, nil)
	// the actual bill was ฿200 more: booked on the card (asked, never automatic), the transfer as typed
	f.do("POST", "/recurring/"+p.ID.String()+"/rules/"+p.Lines[0].ID.String()+"/occurrences/"+f.day(0)+"/confirm", "confirmRecurringOccurrence",
		map[string]any{"amount": 5795, "bill_diff": map[string]any{"amount": 200, "category": "other"}}, 200, &res)
	var diffs int
	_ = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM transactions WHERE account_id = $1 AND type = 'expense' AND amount = 200 AND date = $2`,
		card, f.day(0)).Scan(&diffs)
	if diffs != 1 {
		t.Errorf("bill difference expenses = %d, want 1", diffs)
	}
	f.do("GET", "/card-summaries/"+card.String(), "getCardSummary", nil, 200, &sm)
	if sm.Bill.Paid != 5795 || sm.Bill.Left != 0 || sm.RecurringID == nil || *sm.RecurringID != p.ID {
		t.Errorf("after paying the bill: %+v", sm.Bill)
	}
}

// ค่าไฟ: the partner pays the bill (paid_by: my expense + my debt) and I pay them back on the 1st
// (repay_paid_by: a debt repayment of that round's amount, not an expense).
func TestRepayPaidBy(t *testing.T) {
	f := newRecFix(t)
	f.seedCategories()
	var p ItemPage
	f.do("POST", "/recurring", "createRecurring", map[string]any{"name": "ค่าไฟ", "rules": []any{map[string]any{"type": "expense",
		"category_id": f.food, "amount_mode": "ask", "source": map[string]any{"paid_by": map[string]any{"person_name": "แฟน"}},
		"schedule": f.monthly(f.today.Day(), f.day(0), nil)}}}, http.StatusCreated, &p)
	partner := p.Lines[0].ID
	if p.Lines[0].Action != ActionPaidBy {
		t.Fatalf("paid_by rule = %+v", p.Lines[0])
	}
	f.do("POST", "/recurring/"+p.ID.String()+"/rules", "addRecurringRule", map[string]any{"type": "expense", "repays_rule_id": partner,
		"source": map[string]any{"account_id": f.wallet}, "schedule": f.monthly(f.today.Day(), f.day(0), nil)}, 200, &p)
	if len(p.Lines) != 2 || p.Lines[1].Action != ActionRepayPaidBy || p.Lines[1].RepaysRuleID == nil || p.Lines[1].AmountMode != "follow" {
		t.Fatalf("payback rule = %+v", p.Lines)
	}
	payback := p.Lines[1].ID
	var res ItemWriteResult
	f.do("POST", "/recurring/"+p.ID.String()+"/rules/"+partner.String()+"/occurrences/"+f.day(0)+"/confirm", "confirmRecurringOccurrence",
		map[string]any{"amount": 1150}, 200, &res)
	before := f.balance()
	f.do("POST", "/recurring/"+p.ID.String()+"/rules/"+payback.String()+"/occurrences/"+f.day(0)+"/confirm", "confirmRecurringOccurrence",
		map[string]any{}, 200, &res)
	var expenses float64
	_ = f.pool.QueryRow(f.ctx, `SELECT COALESCE(SUM(t.amount), 0)::float8 FROM transactions t JOIN categories c ON c.id = t.category_id
		WHERE t.user_id = $1 AND t.type = 'expense' AND NOT c.is_system`, f.me).Scan(&expenses)
	if f.balance() != before-1150 || expenses != 1150 {
		t.Errorf("payback: wallet %v → %v, expenses %v (want 1150 once)", before, f.balance(), expenses)
	}
	// a payback needs a person-paid rule of the same recurring
	f.do("POST", "/recurring/"+p.ID.String()+"/rules", "addRecurringRule", map[string]any{"type": "expense", "repays_rule_id": payback,
		"source": map[string]any{"account_id": f.wallet}, "schedule": f.monthly(f.today.Day(), f.day(0), nil)}, 400, nil)
}

// Rules live everywhere: a wallet / goal / person page pulls the rules whose columns touch it,
// grouped by recurring; a rule made from such a page goes into a new recurring or an existing one.
func TestRulesEverywhere(t *testing.T) {
	f := newRecFix(t)
	f.seedCategories()
	other := testdb.Account(t, f.pool, f.me)
	goal := uuid.New()
	f.exec(`INSERT INTO saving_goals (id, user_id, name, target_amount, status) VALUES ($1, $2, 'เที่ยวญี่ปุ่น', 60000, 'active')`, goal, f.me)
	// Netflix: rule 1 from my wallet, rule 2 from the other
	var nf ItemPage
	f.do("POST", "/recurring", "createRecurring", map[string]any{"name": "Netflix", "tracking_type": "subscription", "rules": []any{
		f.expenseRule(300, f.monthly(4, f.day(0), nil)),
		map[string]any{"type": "expense", "category_id": f.food, "amount": 200, "source": map[string]any{"account_id": other},
			"schedule": f.monthly(5, f.day(0), nil)},
	}}, http.StatusCreated, &nf)
	// from the goal page: a new recurring named for it, then a second wallet into the same one
	save := func(from uuid.UUID, amount float64) map[string]any {
		return map[string]any{"type": "saving", "goal_id": goal, "amount": amount, "source": map[string]any{"account_id": from},
			"schedule": f.monthly(26, f.day(0), nil)}
	}
	var g ItemPage
	f.do("POST", "/recurring-rules", "createRecurringRule", map[string]any{"new_recurring": map[string]any{"name": "ออมเป้าเที่ยว",
		"tracking_type": "saving"}, "rule": save(f.wallet, 2000)}, http.StatusCreated, &g)
	f.do("POST", "/recurring-rules", "createRecurringRule", map[string]any{"recurring_id": g.ID, "rule": save(other, 1000)},
		http.StatusCreated, &g)
	if len(g.Lines) != 2 || len(g.Touches) != 1 || g.Touches[0].Kind != "goal" || g.Lines[0].EndText == nil {
		t.Fatalf("goal recurring = %+v", g)
	}
	f.do("POST", "/recurring-rules", "createRecurringRule", map[string]any{"rule": save(f.wallet, 1)}, 400, nil)

	// the wallet page: Netflix rule 1 + the goal saving from my wallet, each under its recurring
	var wl RuleGroupList
	f.do("GET", "/recurring-rules?account_id="+f.wallet.String()+"&to_account_id="+f.wallet.String(), "listRecurringRules", nil, 200, &wl)
	if len(wl.Data) != 2 || wl.Data[0].Recurring.ID != nf.ID || wl.Data[0].Recurring.RulesCount != 2 || len(wl.Data[0].Rules) != 1 ||
		*wl.Data[0].Rules[0].Amount != 300 || wl.Data[1].Recurring.ID != g.ID || len(wl.Data[1].Rules) != 1 {
		t.Fatalf("wallet page = %+v", wl.Data)
	}
	// the goal page: one group, both rules
	var gl RuleGroupList
	f.do("GET", "/recurring-rules?goal_id="+goal.String(), "listRecurringRules", nil, 200, &gl)
	if len(gl.Data) != 1 || len(gl.Data[0].Rules) != 2 || gl.Data[0].MonthTotal != 3000 {
		t.Fatalf("goal page = %+v", gl.Data)
	}
	f.do("GET", "/recurring-rules", "listRecurringRules", nil, 400, nil)

	// a person page by its key; the list's sections
	var pp ItemPage
	f.do("POST", "/recurring", "createRecurring", map[string]any{"name": "จ่ายแฟน", "rules": []any{map[string]any{"type": "debt",
		"counterparty": map[string]any{"person_name": "แฟน"}, "debt_direction": "received", "amount": 4000,
		"source": map[string]any{"account_id": f.wallet}, "schedule": f.monthly(-1, f.day(0), nil)}}}, http.StatusCreated, &pp)
	var dl RuleGroupList
	f.do("GET", "/recurring-rules?debt_key=n:แฟน", "listRecurringRules", nil, 200, &dl)
	if len(dl.Data) != 1 || dl.Data[0].Recurring.ID != pp.ID || dl.Data[0].Rules[0].EndText == nil {
		t.Fatalf("person page = %+v", dl.Data)
	}
	var list SummaryList
	f.do("GET", "/recurring", "listRecurring", nil, 200, &list)
	related := map[uuid.UUID]bool{}
	for _, r := range list.Data {
		related[r.ID] = r.Related
	}
	if related[nf.ID] || !related[g.ID] || !related[pp.ID] {
		t.Errorf("sections = %v", related)
	}
}

// The switches: a rule without its own takes the user's defaults (the settings page's รายการประจำ
// row); an asked amount never auto-accepts.
func TestRuleSwitchDefaults(t *testing.T) {
	f := newRecFix(t)
	f.seedCategories()
	f.exec(`UPDATE users SET recurring_auto_accept_default = TRUE, recurring_notify_default = FALSE WHERE id = $1`, f.me)
	before := f.balance()
	var p ItemPage
	f.do("POST", "/recurring", "createRecurring", map[string]any{"name": "ค่าน้ำ", "rules": []any{
		f.expenseRule(150, f.monthly(f.today.Day(), f.day(0), nil)),
		map[string]any{"type": "expense", "category_id": f.food, "amount_mode": "ask", "auto_accept": true, "notify": true,
			"source": map[string]any{"account_id": f.wallet}, "schedule": f.monthly(f.today.Day(), f.day(0), nil)},
	}}, http.StatusCreated, &p)
	if !p.Lines[0].AutoAcceptOn || p.Lines[0].NotifyOn || p.Lines[1].AutoAcceptOn || p.Lines[1].AutoAccept != nil || !p.Lines[1].NotifyOn {
		t.Fatalf("switches = %+v / %+v", p.Lines[0], p.Lines[1])
	}
	if f.balance() != before-150 || p.DueCount != 1 {
		t.Errorf("auto-accepted by default: balance %v → %v, due %d (the asked one waits)", before, f.balance(), p.DueCount)
	}
	// a round coming due later: one notice, from the asked rule only (the other's notify is off)
	f.exec(`UPDATE recurring_rules SET due_notified_until = start_date - 1, auto_accepted_until = start_date WHERE recurring_id = $1`, p.ID)
	if err := f.svc.DueCheck(f.ctx, f.me, ""); err != nil {
		t.Fatal(err)
	}
	if _, count := f.notices("recurring_due"); count != 1 {
		t.Errorf("notice count = %d, want 1 (the asked rule's round)", count)
	}
}

// An asked person repayment fills the net only when it is the person's only rule; next to a fixed
// rule it shows the net as a hint (spec §2.1).
func TestPersonAskPrefill(t *testing.T) {
	f := newRecFix(t)
	f.seedCategories()
	day := f.day(-5)
	if _, err := f.debts.CreateEntry(f.ctx, f.me, "n:แฟน", personal_debts.CreateEntryRequest{Direction: "received", Amount: 3100,
		Date: &day}); err != nil {
		t.Fatal(err)
	}
	ask := map[string]any{"type": "debt", "counterparty": map[string]any{"person_name": "แฟน"}, "debt_direction": "received",
		"amount_mode": "ask", "source": map[string]any{"account_id": f.wallet}, "schedule": f.monthly(f.today.Day(), f.day(0), nil)}
	var p ItemPage
	f.do("POST", "/recurring", "createRecurring", map[string]any{"name": "ค่าหาร", "rules": []any{ask}}, http.StatusCreated, &p)
	due := func() *float64 {
		var occ OccurrencesResult
		f.do("GET", "/recurring/"+p.ID.String()+"/occurrences", "listRecurringOccurrences", nil, 200, &occ)
		for _, o := range occ.Data {
			if o.Status == StatusDue {
				return o.Amount
			}
		}
		t.Fatalf("no due round: %+v", occ.Data)
		return nil
	}
	if a := due(); a == nil || *a != 3100 || p.Lines[0].PrefillHint != nil {
		t.Errorf("only rule: round %v hint %v", a, p.Lines[0].PrefillHint)
	}
	fixed := map[string]any{"type": "debt", "counterparty": map[string]any{"person_name": "แฟน"}, "debt_direction": "received",
		"amount": 1000, "source": map[string]any{"account_id": f.wallet}, "schedule": f.monthly(1, f.day(1), nil)}
	f.do("POST", "/recurring", "createRecurring", map[string]any{"name": "ผ่อนเงินยืม", "rules": []any{fixed}}, http.StatusCreated, nil)
	f.do("GET", "/recurring/"+p.ID.String(), "getRecurring", nil, 200, &p)
	if a := due(); a != nil || p.Lines[0].PrefillHint == nil || *p.Lines[0].PrefillHint != 3100 {
		t.Errorf("next to a fixed rule: round %v hint %v", a, p.Lines[0].PrefillHint)
	}
}

// Quick create "ผ่อน": an expense on a card + its card plan (the card is the creditor, no rule, no
// debt row) in one write.
func TestQuickCreateInstallment(t *testing.T) {
	f := newRecFix(t)
	card := f.wallet
	f.exec(`UPDATE accounts SET type = 'credit_card', credit_limit = 50000, statement_date = 25 WHERE id = $1`, card)
	body := map[string]any{"type": "expense", "account_id": card, "amount": 9000, "category_id": f.food, "date": f.day(0),
		"description": "ทีวี", "installment": map[string]any{"count": 3}}
	var res struct {
		DebtPlanID *uuid.UUID `json:"debt_plan_id"`
		Rows       []struct {
			Amount float64 `json:"amount"`
		} `json:"rows"`
	}
	f.do("POST", "/transactions", "createTransaction", body, http.StatusCreated, &res)
	if res.DebtPlanID == nil || len(res.Rows) != 1 || res.Rows[0].Amount != 9000 {
		t.Fatalf("create = %+v", res)
	}
	var pl PlanView
	f.do("GET", "/debt-plans/"+res.DebtPlanID.String(), "getDebtPlan", nil, 200, &pl)
	if pl.Name != "ทีวี" || pl.Count != 3 || pl.Creditor.Kind != "card" || pl.Purchase == nil || pl.StartDate[8:] != "25" {
		t.Fatalf("plan = %+v", pl)
	}
	var n int
	_ = f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM personal_debts WHERE user_id = $1`, f.me).Scan(&n)
	if n != 0 {
		t.Errorf("%d debt rows: the card holds the debt", n)
	}
	body["installment"] = map[string]any{"count": 0}
	if raw := f.do("POST", "/transactions", "createTransaction", body, 400, nil); !contains(raw, "PLAN_INVALID") {
		t.Errorf("bad count: %s", raw)
	}
}
