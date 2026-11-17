package recurring

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
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
	body := map[string]any{"name": "ค่าเช่า", "type": "expense", "category_id": f.food, "lines": []any{
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
	_ = f.pool.QueryRow(f.ctx, `SELECT category_id FROM recurring_rules WHERE id = $1`, second).Scan(&lineCat)
	if lineCat == nil || *lineCat != f.food {
		t.Errorf("the item's category did not reach its second line")
	}

	// the list shows the item once
	var list SummaryList
	f.do("GET", "/recurring", "listRecurring", nil, 200, &list)
	if len(list.Data) != 1 || list.Data[0].LinesCount != 2 || list.Data[0].Amount != nil {
		t.Fatalf("list = %+v", list.Data)
	}

	// rename: every line follows; pause one line: the item stays active
	f.do("PUT", "/recurring/"+p.ID.String(), "updateRecurring", map[string]any{"name": "ค่าบ้าน", "type": "bill", "category_id": f.food}, 200, &p)
	var n int
	_ = f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM recurring_rules WHERE recurring_id = $1 AND name = 'ค่าบ้าน' AND kind = 'reminder'`, p.ID).Scan(&n)
	if n != 2 || p.Type != TypeBill {
		t.Errorf("rename reached %d lines, type %s", n, p.Type)
	}
	f.do("POST", "/recurring/"+p.ID.String()+"/lines/"+second.String()+"/pause", "pauseRecurringLine", nil, 200, &p)
	if p.Status != "active" || p.Lines[1].Status != "paused" {
		t.Errorf("after line pause: item %s line %s", p.Status, p.Lines[1].Status)
	}
	f.do("POST", "/recurring/"+p.ID.String()+"/lines/"+p.ID.String()+"/pause", "pauseRecurringLine", nil, 200, &p)
	if p.Status != "paused" {
		t.Errorf("both lines paused: item %s", p.Status)
	}
	f.do("POST", "/recurring/"+p.ID.String()+"/resume", "resumeRecurring", nil, 200, &p)
	if p.Status != "active" || p.Lines[0].Status != "active" || p.Lines[1].Status != "active" {
		t.Errorf("resume item: %s / %s / %s", p.Status, p.Lines[0].Status, p.Lines[1].Status)
	}

	// add a line, edit it, delete it; the last line can't go
	f.do("POST", "/recurring/"+p.ID.String()+"/lines", "addRecurringLine",
		map[string]any{"amount": 99, "source": map[string]any{"account_id": f.wallet}, "schedule": f.monthly(15, f.day(0), nil)}, 200, &p)
	if len(p.Lines) != 3 {
		t.Fatalf("lines after add = %d", len(p.Lines))
	}
	third := p.Lines[2].ID
	f.do("PUT", "/recurring/"+p.ID.String()+"/lines/"+third.String(), "updateRecurringLine",
		map[string]any{"amount": 120, "source": map[string]any{"account_id": f.wallet}}, 200, &p)
	if *p.Lines[2].Amount != 120 || p.Lines[2].Schedule.Freq != "monthly" {
		t.Errorf("line edit = %+v", p.Lines[2])
	}
	f.do("PUT", "/recurring/"+p.ID.String()+"/lines/"+third.String(), "updateRecurringLine",
		map[string]any{"amount": 1, "source": map[string]any{"account_id": f.wallet, "paid_by": map[string]any{"person_name": "x"}}}, 400, nil)
	f.do("DELETE", "/recurring/"+p.ID.String()+"/lines/"+third.String(), "deleteRecurringLine", nil, 200, &p)
	f.do("DELETE", "/recurring/"+p.ID.String()+"/lines/"+second.String(), "deleteRecurringLine", nil, 200, &p)
	raw := f.do("DELETE", "/recurring/"+p.ID.String()+"/lines/"+p.ID.String(), "deleteRecurringLine", nil, 409, nil)
	if !contains(raw, "RECURRING_LAST_LINE") {
		t.Errorf("last line: %s", raw)
	}
	f.do("POST", "/recurring/"+p.ID.String()+"/lines/"+uuid.NewString()+"/occurrences/"+f.day(0)+"/skip", "skipRecurringOccurrence", nil, 404, nil)

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
	body := map[string]any{"name": "ค่าเช่า", "type": "debt", "category_id": f.food, "debt_direction": "received",
		"counterparty": map[string]any{"person_name": "แฟน"},
		"lines": []any{map[string]any{"amount": 6000, "source": map[string]any{"paid_by": map[string]any{"person_name": "แฟน"}},
			"schedule": f.monthly(f.today.Day(), f.day(0), nil)}}}
	var p ItemPage
	f.do("POST", "/recurring", "createRecurring", body, http.StatusCreated, &p)
	if p.Lines[0].PaidBy == nil || p.Lines[0].PaidBy.Name != "แฟน" || p.Lines[0].Source.AccountID != nil {
		t.Fatalf("line = %+v", p.Lines[0])
	}
	var res ItemWriteResult
	f.do("POST", "/recurring/"+p.ID.String()+"/lines/"+p.ID.String()+"/occurrences/"+f.day(0)+"/confirm", "confirmRecurringOccurrence",
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
	f.do("GET", "/recurring/"+p.ID.String(), "getRecurring", nil, 200, &p)
	if p.Counterparty == nil || p.Counterparty.Net != -6000 || p.Counterparty.Key != "n:แฟน" {
		t.Errorf("counterparty = %+v", p.Counterparty)
	}

	// a person can't pay an income for me
	bad := map[string]any{"name": "x", "type": "income", "lines": []any{map[string]any{"amount": 1,
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

// A debt item with a plan: the opening row counts at once; installments are ticks; a payment round
// ticks the oldest open one; edit / mark / close / drop the plan.
func TestRecurringDebtPlan(t *testing.T) {
	f := newRecFix(t)
	var prev InstallmentList
	f.do("POST", "/recurring/plan-preview", "previewRecurringPlan", map[string]any{"principal": 10000, "count": 3,
		"start_date": f.day(0), "interest_method": "none"}, 200, &prev)
	if len(prev.Data) != 3 || prev.Data[0].Principal != 3333.33 || prev.Data[2].Principal != 3333.34 {
		t.Fatalf("preview = %+v", prev.Data)
	}
	var red InstallmentList
	f.do("POST", "/recurring/plan-preview", "previewRecurringPlan", map[string]any{"principal": 12000, "count": 12,
		"start_date": f.day(0), "interest_method": "reducing", "interest_rate": 12}, 200, &red)
	sum := 0.0
	for _, x := range red.Data {
		sum += x.Principal
	}
	if len(red.Data) != 12 || red.Data[0].Interest != 120 || round2(sum) != 12000 || red.Data[11].Interest >= red.Data[0].Interest {
		t.Errorf("reducing = %+v (principal %v)", red.Data, sum)
	}
	if raw := f.do("POST", "/recurring/plan-preview", "previewRecurringPlan", map[string]any{"principal": 1, "count": 0,
		"start_date": f.day(0), "interest_method": "none"}, 400, nil); !contains(raw, "PLAN_INVALID") {
		t.Errorf("bad plan: %s", raw)
	}

	f.seedCategories()
	before := f.balance()
	body := map[string]any{"name": "ให้ยืม", "type": "debt", "debt_direction": "paid", "counterparty": map[string]any{"person_name": "บอย"},
		"plan": map[string]any{"principal": 10000, "count": 5, "start_date": f.day(-40), "interest_method": "none", "opening_account_id": f.wallet},
		"lines": []any{map[string]any{"amount": 2000, "debt_kind": "repayment", "source": map[string]any{"account_id": f.wallet},
			"schedule": f.monthly(f.today.Day(), f.day(0), map[string]any{"kind": "count", "count": 5})}}}
	var p ItemPage
	f.do("POST", "/recurring", "createRecurring", body, http.StatusCreated, &p)
	if p.Plan == nil || p.Plan.Count != 5 || p.Plan.Done != 0 || len(p.Plan.Due) != 2 || p.Plan.LeftTotal != 10000 ||
		p.Counterparty == nil || p.Counterparty.Net != 10000 {
		t.Fatalf("plan item = %+v plan %+v cp %+v", p, p.Plan, p.Counterparty)
	}
	if f.balance() != before-10000 || len(p.AccountBalances) != 1 {
		t.Errorf("the opening row's wallet tx: balance %v → %v, %d balances", before, f.balance(), len(p.AccountBalances))
	}
	// a payment round: a repayment row of the item, the oldest open installment ticked
	var res ItemWriteResult
	f.do("POST", "/recurring/"+p.ID.String()+"/lines/"+p.ID.String()+"/occurrences/"+f.day(0)+"/confirm", "confirmRecurringOccurrence",
		map[string]any{}, 200, &res)
	f.do("GET", "/recurring/"+p.ID.String(), "getRecurring", nil, 200, &p)
	if p.Plan.Done != 1 || p.Plan.Chain[0].Status != "paid" || p.Counterparty.Net != 8000 {
		t.Errorf("after a payment: plan %+v net %v", p.Plan.Chain, p.Counterparty.Net)
	}
	// mark / unmark by hand, edit one, close early
	f.do("POST", "/recurring/"+p.ID.String()+"/installments/2/mark", "markRecurringInstallment", map[string]any{"paid": true}, 200, &p)
	f.do("POST", "/recurring/"+p.ID.String()+"/installments/2/mark", "markRecurringInstallment", map[string]any{"paid": false}, 200, &p)
	f.do("PUT", "/recurring/"+p.ID.String()+"/installments/3", "updateRecurringInstallment", map[string]any{"amount": 2500, "interest": 500}, 200, &p)
	var inst InstallmentList
	f.do("GET", "/recurring/"+p.ID.String()+"/installments", "listRecurringInstallments", nil, 200, &inst)
	if inst.Data[1].Status == "paid" || inst.Data[2].Principal != 2000 || inst.Data[2].Interest != 500 || !inst.Data[2].Edited {
		t.Errorf("installments = %+v", inst.Data)
	}
	f.do("POST", "/recurring/"+p.ID.String()+"/installments/9/mark", "markRecurringInstallment", map[string]any{"paid": true}, 404, nil)
	f.do("POST", "/recurring/"+p.ID.String()+"/plan/close", "closeRecurringPlan", nil, 200, &p)
	if !p.Plan.Closed || p.Plan.LeftCount != 0 || p.Plan.Done != 1 {
		t.Errorf("closed plan = %+v", p.Plan)
	}
	f.do("POST", "/recurring/"+p.ID.String()+"/plan/close", "closeRecurringPlan", nil, 409, nil)
	f.do("DELETE", "/recurring/"+p.ID.String()+"/plan", "deleteRecurringPlan", nil, 200, &p)
	if p.Plan != nil || p.Counterparty.Net != 8000 {
		t.Errorf("after dropping the plan: %+v net %v", p.Plan, p.Counterparty.Net)
	}
}

// A card installment: no line, no debt row (the card holds it); an institution counterparty.
func TestRecurringCardPlan(t *testing.T) {
	f := newRecFix(t)
	card := f.wallet
	f.exec(`UPDATE accounts SET type = 'credit_card', credit_limit = 50000 WHERE id = $1`, card)
	inst := uuid.New()
	f.exec(`INSERT INTO institutions (id, user_id, name, kind) VALUES ($1, $2, 'KTC', 'card')`, inst, f.me)
	body := map[string]any{"name": "ผ่อนทีวี", "type": "debt", "debt_direction": "received",
		"counterparty": map[string]any{"institution_id": inst},
		"plan":         map[string]any{"principal": 9000, "count": 3, "start_date": f.day(10), "interest_method": "none", "card_account_id": card},
		"lines":        []any{}}
	var p ItemPage
	f.do("POST", "/recurring", "createRecurring", body, http.StatusCreated, &p)
	if len(p.Lines) != 0 || p.Plan == nil || p.Plan.CardAccount == nil || p.Counterparty == nil || p.Counterparty.Kind != "institution" ||
		p.Counterparty.Key != "i:"+inst.String() || p.Counterparty.Net != 0 {
		t.Fatalf("card plan = %+v plan %+v cp %+v", p, p.Plan, p.Counterparty)
	}
	var n int
	_ = f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM personal_debts WHERE user_id = $1`, f.me).Scan(&n)
	if n != 0 {
		t.Errorf("a card plan wrote %d debt rows", n)
	}
	// without a plan an item needs a line
	delete(body, "plan")
	f.do("POST", "/recurring", "createRecurring", body, 400, nil)
}

// Reminders: a due-anchored reminder sends one recurring_due_soon notice per round, once.
func TestRecurringReminders(t *testing.T) {
	f := newRecFix(t)
	f.exec(`INSERT INTO user_preferences (user_id, preferences) VALUES ($1, '{"timezone":"Asia/Bangkok"}')
		ON CONFLICT (user_id) DO UPDATE SET preferences = EXCLUDED.preferences`, f.me)
	body := map[string]any{"name": "ค่าไฟ", "type": "bill", "category_id": f.food,
		"reminders": []any{map[string]any{"anchor": "due", "days_before": 2, "time": "00:00"}},
		"lines": []any{map[string]any{"amount": 800, "source": map[string]any{"account_id": f.wallet},
			"schedule": map[string]any{"freq": "daily", "interval": 1, "start_date": f.day(0)}}}}
	var p ItemPage
	f.do("POST", "/recurring", "createRecurring", body, http.StatusCreated, &p)
	if len(p.Reminders) != 1 || p.Reminders[0].DaysBefore != 2 {
		t.Fatalf("reminders = %+v", p.Reminders)
	}
	now := time.Now()
	n1, err := f.svc.sendUserReminders(f.ctx, f.me, now)
	if err != nil {
		t.Fatal(err)
	}
	n2, _ := f.svc.sendUserReminders(f.ctx, f.me, now)
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
	// same reminders again (the lines kept): nothing re-sent; removed: gone
	delete(body, "lines")
	body["reminders"] = []any{map[string]any{"anchor": "due", "days_before": 2, "time": "00:00"}}
	f.do("PUT", "/recurring/"+p.ID.String(), "updateRecurring", body, 200, &p)
	if n, _ := f.svc.sendUserReminders(f.ctx, f.me, now); n != 0 {
		t.Errorf("an unchanged reminder re-sent")
	}
	body["reminders"] = []any{}
	f.do("PUT", "/recurring/"+p.ID.String(), "updateRecurring", body, 200, &p)
	if len(p.Reminders) != 0 {
		t.Errorf("reminders after clearing = %+v", p.Reminders)
	}
}

// Quick create "ผ่อน": an expense on a card + its installment item (the card's institution, a plan
// on the card, no line, no debt row) in one write.
func TestQuickCreateInstallment(t *testing.T) {
	f := newRecFix(t)
	card := f.wallet
	f.exec(`UPDATE accounts SET type = 'credit_card', credit_limit = 50000, statement_date = 25 WHERE id = $1`, card)
	body := map[string]any{"type": "expense", "account_id": card, "amount": 9000, "category_id": f.food, "date": f.day(0),
		"description": "ทีวี", "installment": map[string]any{"count": 3}}
	if raw := f.do("POST", "/transactions", "createTransaction", body, 400, nil); !contains(raw, "INSTALLMENT_NEEDS_INSTITUTION") {
		t.Errorf("no institution: %s", raw)
	}
	inst := uuid.New()
	f.exec(`INSERT INTO institutions (id, user_id, name, kind) VALUES ($1, $2, 'KTC', 'card')`, inst, f.me)
	f.exec(`UPDATE accounts SET institution_id = $2 WHERE id = $1`, card, inst)
	var res struct {
		RecurringID *uuid.UUID `json:"recurring_id"`
		Rows        []struct {
			Amount float64 `json:"amount"`
		} `json:"rows"`
	}
	f.do("POST", "/transactions", "createTransaction", body, http.StatusCreated, &res)
	if res.RecurringID == nil || len(res.Rows) != 1 || res.Rows[0].Amount != 9000 {
		t.Fatalf("create = %+v", res)
	}
	var p ItemPage
	f.do("GET", "/recurring/"+res.RecurringID.String(), "getRecurring", nil, 200, &p)
	if p.Name != "ทีวี" || p.Type != TypeDebt || len(p.Lines) != 0 || p.Plan == nil || p.Plan.Count != 3 || p.Plan.CardAccountID == nil ||
		p.Counterparty == nil || p.Counterparty.Key != "i:"+inst.String() || p.Plan.StartDate[8:] != "25" {
		t.Fatalf("item = %+v plan %+v", p, p.Plan)
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
	body["installment"], body["account_id"] = map[string]any{"count": 3}, nil
	f.do("POST", "/transactions", "createTransaction", body, 400, nil)
}
