package personal_debts

import (
	"net/http"
	"testing"
	"time"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

// TestDebtPagesAPI — 0.4.0-3 debt pages: the people row's last activity / side / first date and the
// hero counts, the person page's running balance + day groups + both share texts, the debt
// detail's origin, the quick-create preview and an overpaid settle that flips the side.
func TestDebtPagesAPI(t *testing.T) {
	f := newDebtHFix(t)
	day := func(n int) string { return time.Now().AddDate(0, 0, n).Format("2006-01-02") }

	// Lee: −60 (ledger), −500 borrowed into the wallet (cash), then I pay back 400 → −160.
	f.call("POST", entriesPath("n:Lee"), map[string]any{"direction": "received", "amount": 60, "date": day(-40),
		"description": "ค่าอาหาร"}, "createDebtEntry", http.StatusCreated)
	cash := f.call("POST", entriesPath("n:Lee"), map[string]any{"direction": "received", "amount": 500, "date": day(-9),
		"account_id": f.wallet, "description": "ยืมเงินลี"}, "createDebtEntry", http.StatusCreated)
	if bc := obj(cash, "balance_change"); num(bc, "before") != -60 || num(bc, "after") != -560 || bc["side_after"] != "payable" || bc["flipped"] != false {
		t.Errorf("record balance_change = %v", bc)
	}
	if o := obj(cash, "entry", "origin"); o["kind"] != "cash" || o["transaction_id"] == nil || num(o, "amount") != 500 ||
		obj(o, "account")["id"] != f.wallet.String() || o["title"] != "ยืมเงินลี" {
		t.Errorf("cash origin = %v", o)
	}
	f.call("POST", repayPath("n:lee"), map[string]any{"kind": "pay", "amount": 400, "date": day(-2), "account_id": f.wallet},
		"createDebtRepayment", http.StatusCreated)
	// Bee: a split on my bill (they owe me 100).
	if _, err := f.txs.CreateWrite(f.ctx, f.me, transactions.CreateRequest{
		Type: transactions.TypeExpense, AccountID: &f.wallet, Amount: 300, Date: day(-1), Description: strp("Dinner"),
		Splits: []transactions.SplitInput{{PersonName: "Bee", OwedAmount: 100}},
	}); err != nil {
		t.Fatal(err)
	}

	// 1. People list.
	people := f.call("GET", "/api/v1/personal-debts/people", nil, "listDebtPeople", http.StatusOK)
	rows := map[string]map[string]any{}
	for _, r := range people["data"].([]any) {
		m := r.(map[string]any)
		rows[m["key"].(string)] = m
	}
	lee := rows["n:lee"]
	if la := obj(lee, "last_activity"); la["kind"] != "repayment" || num(la, "amount") != 400 || la["direction"] != "paid" ||
		la["date"] != day(-2) || lee["side"] != "payable" || lee["first_activity_at"] != day(-40) {
		t.Errorf("Lee row = %v", lee)
	}
	if bee := rows["n:bee"]; bee["side"] != "receivable" || obj(bee, "last_activity")["title"] != "Dinner" {
		t.Errorf("Bee row = %v", bee)
	}
	tot := obj(people, "totals")
	if num(tot, "receivable_total") != 100 || num(tot, "payable_total") != 160 || num(tot, "net") != -60 {
		t.Errorf("totals = %v", tot)
	}
	if c := obj(tot, "counts"); num(c, "all") != 2 || num(c, "receivable") != 1 || num(c, "payable") != 1 || num(c, "overdue_30") != 1 {
		t.Errorf("counts = %v", c)
	}
	overdue := f.call("GET", "/api/v1/personal-debts/people?side=overdue_30", nil, "listDebtPeople", http.StatusOK)
	if d := overdue["data"].([]any); len(d) != 1 || d[0].(map[string]any)["key"] != "n:lee" {
		t.Errorf("overdue_30 = %v", d)
	}

	// 2. Person page.
	page := f.call("GET", personPath("n:lee", ""), nil, "getDebtPerson", http.StatusOK)
	items := page["items"].([]any)
	wantRun := []float64{-160, -560, -60} // newest first
	for i, it := range items {
		if num(it.(map[string]any), "running_balance") != wantRun[i] {
			t.Errorf("item %d running_balance = %v, want %v", i, it.(map[string]any)["running_balance"], wantRun[i])
		}
	}
	days := page["days"].([]any)
	if len(days) != 3 || num(days[0].(map[string]any), "change") != 400 || num(days[0].(map[string]any), "balance_after") != -160 ||
		num(days[1].(map[string]any), "change") != -500 {
		t.Errorf("days = %v", days)
	}
	msg := obj(page, "summary_message")
	if msg["text_balance_only"] != "ยอดกับ Lee ตอนนี้: Lee เป็นเจ้าหนี้ 160 บาท" || msg["since"] != day(-40) {
		t.Errorf("share texts = %v", msg)
	}

	// 3. A split debt's origin (the มาจาก card).
	bee := f.call("GET", personPath("n:bee", ""), nil, "getDebtPerson", http.StatusOK)
	o := obj(bee["items"].([]any)[0].(map[string]any), "origin")
	if o["kind"] != "split" || num(o, "amount") != 300 || o["title"] != "Dinner" || o["date"] != day(-1) || o["transaction_id"] == nil {
		t.Errorf("split origin = %v", o)
	}

	// 4. Quick-create preview: record with a transaction, then an overpaid settle.
	before := f.balance()
	pv := f.call("POST", entriesPath("n:lee")+"?preview=true", map[string]any{"direction": "paid", "amount": 50,
		"create_transaction": true, "account_id": f.wallet}, "createDebtEntry", http.StatusOK)
	p0 := pv["people"].([]any)[0].(map[string]any)
	w0 := pv["wallets"].([]any)[0].(map[string]any)
	if pv["preview"] != true || num(p0, "before") != -160 || num(p0, "after") != -110 || num(w0, "delta") != -50 || f.balance() != before {
		t.Errorf("record preview = %v (wallet now %v)", pv, f.balance())
	}
	sv := f.call("POST", repayPath("n:lee")+"?preview=true", map[string]any{"kind": "pay", "amount": 200, "account_id": f.wallet},
		"createDebtRepayment", http.StatusOK)
	p1 := sv["people"].([]any)[0].(map[string]any)
	if num(p1, "after") != 40 || p1["flipped"] != true || p1["side_after"] != "receivable" {
		t.Errorf("settle preview = %v", sv)
	}
	if again := f.call("GET", personPath("n:lee", ""), nil, "getDebtPerson", http.StatusOK); num(again, "net") != -160 {
		t.Errorf("a preview saved something: net %v", again["net"])
	}

	// 5. The real overpaid settle: allowed, flipped.
	paid := f.call("POST", repayPath("n:lee"), map[string]any{"kind": "pay", "amount": 200, "account_id": f.wallet},
		"createDebtRepayment", http.StatusCreated)
	if bc := obj(paid, "balance_change"); num(bc, "before") != -160 || num(bc, "after") != 40 || bc["flipped"] != true ||
		bc["side_before"] != "payable" || bc["side_after"] != "receivable" {
		t.Errorf("overpay balance_change = %v", bc)
	}
}

func strp(s string) *string { return &s }
