package recurring

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// do — a 0.4.3 call as is (no 0.4.2 shim).
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
	sc := map[string]any{"freq": "monthly", "interval": 1, "monthly": map[string]any{"mode": "day", "day": day}, "start_date": start}
	if end != nil {
		sc["end"] = end
	}
	return sc
}

// One item, several lines: each line its own source / amount / schedule; the item's fields reach
// every line; lines are added, edited, paused and deleted on their own; the last one stays.
func TestRecurringItemLines(t *testing.T) {
	f := newRecFix(t)
	other := f.wallet
	body := map[string]any{"name": "ค่าเช่า", "type": "expense", "category_id": f.food, "reminders": []any{
		map[string]any{"amount": 6000, "source": map[string]any{"account_id": f.wallet}, "schedule": f.monthly(1, f.day(-40), nil)},
		map[string]any{"amount": 500, "source": map[string]any{"account_id": other}, "schedule": map[string]any{"freq": "weekly", "interval": 1,
			"weekdays": []int{6}, "start_date": f.day(-10)}},
	}}
	var p ItemPage
	f.do("POST", "/recurring", "createRecurring", body, http.StatusCreated, &p)
	if len(p.Lines) != 2 || p.Lines[0].ID != p.ID || p.Type != TypeExpense || p.Category == nil || p.Status != "active" {
		t.Fatalf("created = %+v", p)
	}
	if p.MonthTotal < 6000+4*500 || p.Next == nil {
		t.Errorf("month total %v next %+v", p.MonthTotal, p.Next)
	}
	second := p.Lines[1].ID
	var lineCat *uuid.UUID
	_ = f.pool.QueryRow(f.ctx, `SELECT category_id FROM recurring_reminders WHERE id = $1`, second).Scan(&lineCat)
	if lineCat == nil || *lineCat != f.food {
		t.Errorf("the item's category did not reach its second line")
	}

	// the list shows the item once
	var list SummaryList
	f.do("GET", "/recurring", "listRecurring", nil, 200, &list)
	if len(list.Data) != 1 || list.Data[0].LinesCount != 2 || list.Data[0].Amount != nil {
		t.Fatalf("list = %+v", list.Data)
	}

	// the type is fixed: another type is refused
	f.do("PUT", "/recurring/"+p.ID.String(), "updateRecurring", map[string]any{"name": "ค่าบ้าน", "type": "income", "category_id": f.food}, http.StatusBadRequest, nil)

	// rename: every reminder follows; pause one reminder: the item stays active
	f.do("PUT", "/recurring/"+p.ID.String(), "updateRecurring", map[string]any{"name": "ค่าบ้าน", "type": "expense", "category_id": f.food}, 200, &p)
	var n int
	_ = f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM recurring_reminders WHERE recurring_id = $1 AND name = 'ค่าบ้าน'`, p.ID).Scan(&n)
	if n != 2 || p.Type != TypeExpense {
		t.Errorf("rename reached %d reminders, type %s", n, p.Type)
	}
	f.do("POST", "/recurring/"+p.ID.String()+"/reminders/"+second.String()+"/pause", "pauseRecurringReminder", nil, 200, &p)
	if p.Status != "active" || p.Lines[1].Status != "paused" {
		t.Errorf("after line pause: item %s line %s", p.Status, p.Lines[1].Status)
	}
	f.do("POST", "/recurring/"+p.ID.String()+"/reminders/"+p.ID.String()+"/pause", "pauseRecurringReminder", nil, 200, &p)
	if p.Status != "paused" {
		t.Errorf("both lines paused: item %s", p.Status)
	}
	f.do("POST", "/recurring/"+p.ID.String()+"/resume", "resumeRecurring", nil, 200, &p)
	if p.Status != "active" || p.Lines[0].Status != "active" || p.Lines[1].Status != "active" {
		t.Errorf("resume item: %s / %s / %s", p.Status, p.Lines[0].Status, p.Lines[1].Status)
	}

	// add a line, edit it, delete it; the last line can't go
	f.do("POST", "/recurring/"+p.ID.String()+"/reminders", "addRecurringReminder",
		map[string]any{"amount": 99, "source": map[string]any{"account_id": f.wallet}, "schedule": f.monthly(15, f.day(0), nil)}, 200, &p)
	if len(p.Lines) != 3 {
		t.Fatalf("lines after add = %d", len(p.Lines))
	}
	third := p.Lines[2].ID
	f.do("PUT", "/recurring/"+p.ID.String()+"/reminders/"+third.String(), "updateRecurringReminder",
		map[string]any{"amount": 120, "source": map[string]any{"account_id": f.wallet}}, 200, &p)
	if *p.Lines[2].Amount != 120 || p.Lines[2].Schedule.Freq != "monthly" {
		t.Errorf("line edit = %+v", p.Lines[2])
	}
	f.do("PUT", "/recurring/"+p.ID.String()+"/reminders/"+third.String(), "updateRecurringReminder",
		map[string]any{"amount": 1, "source": map[string]any{"account_id": f.wallet, "paid_by": map[string]any{"person_name": "x"}}}, 400, nil)
	f.do("DELETE", "/recurring/"+p.ID.String()+"/reminders/"+third.String(), "deleteRecurringReminder", nil, 200, &p)
	f.do("DELETE", "/recurring/"+p.ID.String()+"/reminders/"+second.String(), "deleteRecurringReminder", nil, 200, &p)
	raw := f.do("DELETE", "/recurring/"+p.ID.String()+"/reminders/"+p.ID.String(), "deleteRecurringReminder", nil, 409, nil)
	if !contains(raw, "RECURRING_LAST_REMINDER") {
		t.Errorf("last line: %s", raw)
	}
	f.do("POST", "/recurring/"+p.ID.String()+"/reminders/"+uuid.NewString()+"/occurrences/"+f.day(0)+"/skip", "skipRecurringOccurrence", nil, 404, nil)

	// rounds of every line in one list
	var rounds ItemList
	f.do("GET", "/recurring/"+p.ID.String()+"/items?tab=produced", "listRecurringRuleItems", nil, 200, &rounds)
	for _, r := range rounds.Data {
		if r.RuleID != p.ID || r.Recurring == nil || r.Recurring.ID != p.ID {
			t.Errorf("round %+v not of the item", r)
		}
	}

	// delete the item: what it wrote stays
	f.do("DELETE", "/recurring/"+p.ID.String(), "deleteRecurring", nil, 200, nil)
	f.do("GET", "/recurring/"+p.ID.String(), "getRecurring", nil, 404, nil)
}

// A person-paid line (คนจ่ายแทน): each round = my expense without wallet money + my debt to that
// person, through one จ่ายแทน paper; the debt row belongs to the item.
func TestRecurringPaidByLine(t *testing.T) {
	f := newRecFix(t)
	before := f.balance()
	body := map[string]any{"name": "ค่าเช่า", "type": "expense", "category_id": f.food,
		"reminders": []any{map[string]any{"amount": 6000, "source": map[string]any{"paid_by": map[string]any{"person_name": "แฟน"}},
			"schedule": f.monthly(f.today.Day(), f.day(0), nil)}}}
	var p ItemPage
	f.do("POST", "/recurring", "createRecurring", body, http.StatusCreated, &p)
	if p.Lines[0].PaidBy == nil || p.Lines[0].PaidBy.Name != "แฟน" || p.Lines[0].Source.AccountID != nil {
		t.Fatalf("line = %+v", p.Lines[0])
	}
	var res ItemWriteResult
	f.do("POST", "/recurring/"+p.ID.String()+"/reminders/"+p.ID.String()+"/occurrences/"+f.day(0)+"/confirm", "confirmRecurringOccurrence",
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
		t.Errorf("debt = %v %s item %v", amount, dir, rec)
	}

	// a person can't pay an income for me
	bad := map[string]any{"name": "x", "type": "income", "reminders": []any{map[string]any{"amount": 1,
		"source": map[string]any{"paid_by": map[string]any{"person_name": "a"}}, "schedule": f.monthly(1, f.day(0), nil)}}}
	if raw := f.do("POST", "/recurring", "createRecurring", bad, 400, nil); !contains(raw, "RECURRING_SOURCE_INVALID") {
		t.Errorf("paid_by income: %s", raw)
	}

	// deleting the item keeps its debt rows as general debts
	f.do("DELETE", "/recurring/"+p.ID.String(), "deleteRecurring", nil, 200, nil)
	_ = f.pool.QueryRow(f.ctx, `SELECT recurring_id FROM personal_debts WHERE user_id = $1 AND kind = 'debt'`, f.me).Scan(&rec)
	if rec != nil {
		t.Errorf("debt still tied to a deleted item")
	}
}

// A debt plan (a cash loan to a person): the opening row counts at once; a debt item pays it by
// reminder (each round = the oldest open installment); edit / mark / pay with interest / close.
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
	// the item that pays it: a reminder; a round pays (here: collects) the oldest open installment
	body := map[string]any{"name": "ให้ยืม", "type": "debt", "debt_plan_id": pl.ID,
		"reminders": []any{map[string]any{"source": map[string]any{"account_id": f.wallet},
			"schedule": f.monthly(f.today.Day(), f.day(0), map[string]any{"kind": "count", "count": 5})}}}
	var p ItemPage
	f.do("POST", "/recurring", "createRecurring", body, http.StatusCreated, &p)
	if p.Plan == nil || p.Plan.ID != pl.ID || p.Counterparty == nil || p.Counterparty.Net != 10000 || p.Lines[0].Action != ActionCollect {
		t.Fatalf("plan item = %+v plan %+v cp %+v", p, p.Plan, p.Counterparty)
	}
	var res ItemWriteResult
	f.do("POST", "/recurring/"+p.ID.String()+"/reminders/"+p.ID.String()+"/occurrences/"+f.day(0)+"/confirm", "confirmRecurringOccurrence",
		map[string]any{}, 200, &res)
	f.do("GET", "/recurring/"+p.ID.String(), "getRecurring", nil, 200, &p)
	if p.Plan.Done != 1 || p.Plan.Chain[0].Status != "paid" || p.Counterparty.Net != 8000 {
		t.Errorf("after a payment: plan %+v net %v", p.Plan.Chain, p.Counterparty.Net)
	}
	// mark / unmark by hand, edit one
	id := pl.ID.String()
	f.do("POST", "/debt-plans/"+id+"/installments/2/mark", "markDebtPlanInstallment", map[string]any{"paid": true}, 200, &pl)
	f.do("POST", "/debt-plans/"+id+"/installments/2/mark", "markDebtPlanInstallment", map[string]any{"paid": false}, 200, &pl)
	f.do("PUT", "/debt-plans/"+id+"/installments/3", "updateDebtPlanInstallment", map[string]any{"amount": 2500, "interest": 500}, 200, &pl)
	var inst InstallmentList
	f.do("GET", "/debt-plans/"+id+"/installments", "listDebtPlanInstallments", nil, 200, &inst)
	if inst.Data[1].Status == "paid" || inst.Data[2].Principal != 2000 || inst.Data[2].Interest != 500 || !inst.Data[2].Edited {
		t.Errorf("installments = %+v", inst.Data)
	}
	f.do("POST", "/debt-plans/"+id+"/installments/9/mark", "markDebtPlanInstallment", map[string]any{"paid": true}, 404, nil)
	// ข้าม: installment 4 moves to the end (6th), the plan still adds up
	f.do("POST", "/debt-plans/"+id+"/installments/4/skip", "skipDebtPlanInstallment", nil, 200, &pl)
	if pl.Count != 6 || pl.Chain[3].Status != "skipped" || len(pl.Installments) != 6 || pl.Installments[5].Principal != 2000 {
		t.Errorf("after skip: count %d chain %+v", pl.Count, pl.Chain)
	}
	f.do("POST", "/debt-plans/"+id+"/installments/4/skip", "skipDebtPlanInstallment", nil, 409, nil)
	// pay installment 3 from the debts page: principal 2000 back + interest 500 as ดอกเบี้ยรับ
	before = f.balance()
	f.do("POST", "/debt-plans/"+id+"/pay", "payDebtPlanInstallment", map[string]any{"seq": 3, "account_id": f.wallet}, 200, &pl)
	var interest float64
	_ = f.pool.QueryRow(f.ctx, `SELECT COALESCE(SUM(t.amount), 0)::float8 FROM transactions t JOIN categories c ON c.id = t.category_id
		WHERE t.user_id = $1 AND c.system_kind = 'INTEREST_RECEIVED'`, f.me).Scan(&interest)
	if pl.Done != 2 || pl.Chain[2].Status != "paid" || f.balance() != before+2500 || interest != 500 {
		t.Errorf("after pay: done %d chain %+v balance %v → %v interest %v", pl.Done, pl.Chain, before, f.balance(), interest)
	}
	f.do("POST", "/debt-plans/"+id+"/close", "closeDebtPlan", nil, 200, &pl)
	if !pl.Closed || pl.LeftCount != 0 || pl.Done != 2 {
		t.Errorf("closed plan = %+v", pl)
	}
	f.do("POST", "/debt-plans/"+id+"/close", "closeDebtPlan", nil, 409, nil)
	var list PlanList
	f.do("GET", "/debt-plans", "listDebtPlans", nil, 200, &list)
	if len(list.Data) != 1 || list.Data[0].RecurringID == nil {
		t.Errorf("list = %+v", list.Data)
	}
	f.do("DELETE", "/debt-plans/"+id, "deleteDebtPlan", nil, http.StatusNoContent, nil)
	f.do("GET", "/recurring/"+p.ID.String(), "getRecurring", nil, 404, nil) // its item went with it
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
	// a purchase is mine to owe; a card plan takes no reminder
	f.do("POST", "/debt-plans", "createDebtPlan", map[string]any{"name": "x", "direction": "paid",
		"creditor": map[string]any{"person_name": "บอย"},
		"plan":     map[string]any{"principal": 100, "count": 1, "start_date": f.day(0), "interest_method": "none"}}, 400, nil)
}

// A card plan: the card is the creditor, no debt row, no reminder (its bill pays it); a card bill
// item: a transfer into the card each round.
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
	line := map[string]any{"source": map[string]any{"account_id": f.wallet}, "schedule": f.monthly(f.today.Day(), f.day(0), nil)}
	f.do("POST", "/recurring", "createRecurring", map[string]any{"name": "x", "type": "debt", "debt_plan_id": pl.ID,
		"reminders": []any{line}}, 400, nil)
	// the card's bill: asks its amount, a transfer into the card
	var p ItemPage
	f.do("POST", "/recurring", "createRecurring", map[string]any{"name": "บิล KTC", "type": "debt",
		"counterparty": map[string]any{"account_id": card}, "reminders": []any{line}}, http.StatusCreated, &p)
	if p.Counterparty == nil || p.Counterparty.Kind != "card" || p.Lines[0].Action != ActionTransfer || p.Lines[0].AmountMode != AmountAsk ||
		p.Lines[0].ToAccountID == nil || *p.Lines[0].ToAccountID != card {
		t.Fatalf("card bill = %+v cp %+v lines %+v", p, p.Counterparty, p.Lines)
	}
	before := f.balance()
	var res ItemWriteResult
	f.do("POST", "/recurring/"+p.ID.String()+"/reminders/"+p.Lines[0].ID.String()+"/occurrences/"+f.day(0)+"/confirm", "confirmRecurringOccurrence",
		map[string]any{"amount": 5595}, 200, &res)
	var cardBal float64
	_ = f.pool.QueryRow(f.ctx, `SELECT balance::float8 FROM accounts WHERE id = $1`, card).Scan(&cardBal)
	if f.balance() != before-5595 || cardBal != 5595 {
		t.Errorf("bill paid: wallet %v → %v, card %v", before, f.balance(), cardBal)
	}
}

// The card summary: last cycle's spending (a plan purchase left out) + the plan installments due in
// it; a bill reminder round starts with what is left.
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
	f.do("POST", "/recurring", "createRecurring", map[string]any{"name": "บิล KTC", "type": "debt", "counterparty": map[string]any{"account_id": card},
		"reminders": []any{map[string]any{"source": map[string]any{"account_id": f.wallet}, "schedule": f.monthly(f.today.Day(), f.day(0), nil)}}},
		http.StatusCreated, &p)
	var res ItemWriteResult
	f.do("POST", "/recurring/"+p.ID.String()+"/reminders/"+p.Lines[0].ID.String()+"/occurrences/"+f.day(0)+"/confirm", "confirmRecurringOccurrence",
		map[string]any{}, 200, &res)
	f.do("GET", "/card-summaries/"+card.String(), "getCardSummary", nil, 200, &sm)
	if sm.Bill.Paid != 5595 || sm.Bill.Left != 0 || sm.RecurringID == nil {
		t.Errorf("after paying the bill: %+v", sm.Bill)
	}
}

// แค่แจ้งเตือน: a notice reminder books nothing; its due round closes by itself; confirm is refused.
func TestNotifyOnlyReminder(t *testing.T) {
	f := newRecFix(t)
	f.seedCategories()
	before := f.balance()
	var p ItemPage
	f.do("POST", "/recurring", "createRecurring", map[string]any{"name": "ต่อภาษีรถ", "type": "expense",
		"reminders": []any{map[string]any{"notify_only": true, "source": map[string]any{"account_id": nil},
			"schedule": f.monthly(f.today.Day(), f.day(-31), nil)}}}, http.StatusCreated, &p)
	if p.Lines[0].Action != ActionNotify || p.DueCount != 0 {
		t.Fatalf("notice = %+v due %d", p.Lines[0], p.DueCount)
	}
	var n int
	_ = f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM recurring_items WHERE recurring_reminder_id = $1 AND cleared_at IS NOT NULL`, p.Lines[0].ID).Scan(&n)
	if n < 1 || f.balance() != before {
		t.Errorf("closed rounds %d, balance %v → %v", n, before, f.balance())
	}
	f.do("POST", "/recurring/"+p.ID.String()+"/reminders/"+p.Lines[0].ID.String()+"/occurrences/"+f.today.AddDate(0, 1, 0).Format(dateLayout)+"/confirm",
		"confirmRecurringOccurrence", map[string]any{}, 409, nil)
}

// ค่าไฟ: the partner pays the bill (paid_by: my expense + my debt) and I pay them back on the 1st
// (repay_paid_by: a debt repayment of that round's amount, not an expense).
func TestRepayPaidBy(t *testing.T) {
	f := newRecFix(t)
	f.seedCategories()
	var p ItemPage
	f.do("POST", "/recurring", "createRecurring", map[string]any{"name": "ค่าไฟ", "type": "expense", "category_id": f.food,
		"reminders": []any{map[string]any{"amount_mode": "ask", "source": map[string]any{"paid_by": map[string]any{"person_name": "แฟน"}},
			"schedule": f.monthly(f.today.Day(), f.day(0), nil)}}}, http.StatusCreated, &p)
	partner := p.Lines[0].ID
	if p.Lines[0].Action != ActionPaidBy {
		t.Fatalf("paid_by line = %+v", p.Lines[0])
	}
	f.do("POST", "/recurring/"+p.ID.String()+"/reminders", "addRecurringReminder", map[string]any{"repays_reminder_id": partner,
		"source": map[string]any{"account_id": f.wallet}, "schedule": f.monthly(f.today.Day(), f.day(0), nil)}, 200, &p)
	if len(p.Lines) != 2 || p.Lines[1].Action != ActionRepayPaidBy || p.Lines[1].RepaysReminderID == nil {
		t.Fatalf("payback line = %+v", p.Lines)
	}
	payback := p.Lines[1].ID
	var res ItemWriteResult
	f.do("POST", "/recurring/"+p.ID.String()+"/reminders/"+partner.String()+"/occurrences/"+f.day(0)+"/confirm", "confirmRecurringOccurrence",
		map[string]any{"amount": 1150}, 200, &res)
	before := f.balance()
	f.do("POST", "/recurring/"+p.ID.String()+"/reminders/"+payback.String()+"/occurrences/"+f.day(0)+"/confirm", "confirmRecurringOccurrence",
		map[string]any{}, 200, &res)
	var expenses float64
	_ = f.pool.QueryRow(f.ctx, `SELECT COALESCE(SUM(t.amount), 0)::float8 FROM transactions t JOIN categories c ON c.id = t.category_id
		WHERE t.user_id = $1 AND t.type = 'expense' AND NOT c.is_system`, f.me).Scan(&expenses)
	if f.balance() != before-1150 || expenses != 1150 {
		t.Errorf("payback: wallet %v → %v, expenses %v (want 1150 once)", before, f.balance(), expenses)
	}
	// a payback needs a person-paid reminder of the same item
	f.do("POST", "/recurring/"+p.ID.String()+"/reminders", "addRecurringReminder", map[string]any{"repays_reminder_id": payback,
		"source": map[string]any{"account_id": f.wallet}, "schedule": f.monthly(f.today.Day(), f.day(0), nil)}, 400, nil)
}

// Alerts: a due-anchored alert on a reminder sends one recurring_due_soon notice per round, once.
func TestRecurringAlerts(t *testing.T) {
	f := newRecFix(t)
	f.exec(`INSERT INTO user_preferences (user_id, preferences) VALUES ($1, '{"timezone":"Asia/Bangkok"}')
		ON CONFLICT (user_id) DO UPDATE SET preferences = EXCLUDED.preferences`, f.me)
	alerts := []any{map[string]any{"anchor": "due", "days_before": 2, "time": "00:00"}}
	body := map[string]any{"name": "ค่าไฟ", "type": "expense", "category_id": f.food,
		"reminders": []any{map[string]any{"amount": 800, "source": map[string]any{"account_id": f.wallet}, "alerts": alerts,
			"schedule": map[string]any{"freq": "daily", "interval": 1, "start_date": f.day(0)}}}}
	var p ItemPage
	f.do("POST", "/recurring", "createRecurring", body, http.StatusCreated, &p)
	if len(p.Lines) != 1 || len(p.Lines[0].Alerts) != 1 || p.Lines[0].Alerts[0].DaysBefore != 2 {
		t.Fatalf("alerts = %+v", p.Lines)
	}
	line := p.Lines[0].ID.String()
	now := time.Now()
	n1, err := f.svc.sendUserAlerts(f.ctx, f.me, now)
	if err != nil {
		t.Fatal(err)
	}
	n2, _ := f.svc.sendUserAlerts(f.ctx, f.me, now)
	rows, _ := f.notices("recurring_due_soon")
	if n1 != 1 || n2 != 0 || rows != 1 {
		t.Errorf("sent %d then %d, %d notices", n1, n2, rows)
	}
	var payload map[string]any
	var raw []byte
	_ = f.pool.QueryRow(f.ctx, `SELECT payload FROM notifications WHERE recipient_user_id = $1 AND type = 'recurring_due_soon'`, f.me).Scan(&raw)
	_ = json.Unmarshal(raw, &payload)
	if payload["date"] != f.day(2) || payload["amount"] != 800.0 || payload["recurring_id"] != p.ID.String() {
		t.Errorf("payload = %v", payload)
	}
	// same alerts again: nothing re-sent; a reminder update without alerts keeps them; [] clears
	upd := map[string]any{"amount": 800, "source": map[string]any{"account_id": f.wallet}, "alerts": alerts}
	f.do("PUT", "/recurring/"+p.ID.String()+"/reminders/"+line, "updateRecurringReminder", upd, 200, &p)
	if n, _ := f.svc.sendUserAlerts(f.ctx, f.me, now); n != 0 {
		t.Errorf("an unchanged alert re-sent")
	}
	delete(upd, "alerts")
	f.do("PUT", "/recurring/"+p.ID.String()+"/reminders/"+line, "updateRecurringReminder", upd, 200, &p)
	if len(p.Lines[0].Alerts) != 1 {
		t.Errorf("alerts dropped by an update without them: %+v", p.Lines[0].Alerts)
	}
	upd["alerts"] = []any{}
	f.do("PUT", "/recurring/"+p.ID.String()+"/reminders/"+line, "updateRecurringReminder", upd, 200, &p)
	if len(p.Lines[0].Alerts) != 0 {
		t.Errorf("alerts after clearing = %+v", p.Lines[0].Alerts)
	}
}

// Quick create "ผ่อน": an expense on a card + its card plan (the card is the creditor, no reminder,
// no debt row) in one write.
func TestQuickCreateInstallment(t *testing.T) {
	f := newRecFix(t)
	card := f.wallet
	f.exec(`UPDATE accounts SET type = 'credit_card', credit_limit = 50000, statement_date = 25 WHERE id = $1`, card)
	body := map[string]any{"type": "expense", "account_id": card, "amount": 9000, "category_id": f.food, "date": f.day(0),
		"description": "ทีวี", "installment": map[string]any{"count": 3}}
	var res struct {
		DebtPlanID *uuid.UUID `json:"debt_plan_id"`
		Rows        []struct {
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
