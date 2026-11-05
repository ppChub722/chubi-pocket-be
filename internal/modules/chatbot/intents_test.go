package chatbot

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/llm"
)

func parseKW(t *testing.T, lists Lists, kw string) Outcome {
	t.Helper()
	out, err := Parse(context.Background(), llm.NewKeywordFake(), testDay, lists, kw, nil)
	if err != nil || out.Fail != "" {
		t.Fatalf("%s: %v / %s", kw, err, out.Fail)
	}
	return out
}

func one(t *testing.T, kw string) Item {
	t.Helper()
	out := parseKW(t, testLists, kw)
	if len(out.Items) != 1 {
		t.Fatalf("%s: %d items, hint %q", kw, len(out.Items), out.Hint)
	}
	return out.Items[0]
}

func hintFor(t *testing.T, kw string) string {
	t.Helper()
	out := parseKW(t, testLists, kw)
	if len(out.Items) != 0 {
		t.Fatalf("%s: %d items, want none", kw, len(out.Items))
	}
	return out.Hint
}

func eq(a, b *uuid.UUID) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }

func TestParseSavings(t *testing.T) {
	if it := one(t, "save goal"); it.Kind != KindSaving || it.SavingAction != ActSave || !eq(it.GoalID, &goalTrip.ID) || !eq(it.AccountID, &walletA.ID) {
		t.Errorf("save goal = %+v", it)
	}
	if it := one(t, "save spare"); it.GoalID != nil || !eq(it.AccountID, &walletA.ID) {
		t.Errorf("save spare = %+v", it)
	}
	// the main wallet is for transactions: a saving without a wallet has none (two wallets); one wallet is used
	if it := one(t, "save nowallet"); it.AccountID != nil {
		t.Errorf("save nowallet took a wallet: %+v", it)
	}
	single := Lists{Wallets: []Wallet{walletB}, Goals: testLists.Goals}
	if out := parseKW(t, single, "save nowallet"); len(out.Items) != 1 || !eq(out.Items[0].AccountID, &walletB.ID) {
		t.Errorf("save nowallet with one wallet = %+v", out.Items)
	}
	if it := one(t, "save unknown goal"); it.GoalID != nil || !contains(it.Unsure, "goal") {
		t.Errorf("save unknown goal = %+v", it)
	}
	if it := one(t, "allocate"); it.SavingAction != ActAllocate || !eq(it.GoalID, &goalTrip.ID) {
		t.Errorf("allocate = %+v", it)
	}
	if h := hintFor(t, "allocate nogoal"); h != HintGoalMissing {
		t.Errorf("allocate with no goal: hint %q", h)
	}
	if it := one(t, "move goals"); !eq(it.FromGoalID, &goalTrip.ID) || !eq(it.GoalID, &goalCar.ID) || it.ToSpare {
		t.Errorf("move goals = %+v", it)
	}
	if it := one(t, "move spare"); !eq(it.FromGoalID, &goalTrip.ID) || it.GoalID != nil || !it.ToSpare {
		t.Errorf("move spare = %+v", it)
	}
	if h := hintFor(t, "move same"); h != HintGoalMissing {
		t.Errorf("move to the same goal: hint %q", h)
	}
	if it := one(t, "withdraw saving"); it.SavingAction != ActWithdraw || !eq(it.FromGoalID, &goalTrip.ID) || !eq(it.AccountID, &walletA.ID) {
		t.Errorf("withdraw saving = %+v", it)
	}
	if it := one(t, "withdraw nowallet"); it.FromGoalID != nil || it.AccountID != nil {
		t.Errorf("withdraw nowallet = %+v", it)
	}
}

func TestParseBills(t *testing.T) {
	p := one(t, "event bill").Paper
	if p == nil || !eq(p.EventID, &eventTrip.ID) || len(p.People) != 3 || p.PaidBy != nil || p.People[0].MemberID == nil {
		t.Errorf("event bill = %+v", p)
	}
	p = one(t, "event payer").Paper
	if p == nil || p.PaidBy == nil || !eq(p.PaidBy.MemberID, &memberBoy.ID) {
		t.Errorf("event payer = %+v", p)
	}
	if p = one(t, "event exclude").Paper; p == nil || len(p.People) != 2 || p.People[0].Name != "ลี" {
		t.Errorf("event exclude = %+v", p)
	}
	// a new event only when the name says it is one
	if p = one(t, "event new").Paper; p == nil || p.NewEvent != "event new" {
		t.Errorf("event new = %+v", p)
	}
	if it := one(t, "event new plain"); it.Paper != nil {
		t.Errorf("a name that is not an event made a bill: %+v", it.Paper)
	}
	if h := hintFor(t, "event done"); h != HintEventDone {
		t.Errorf("finished event: hint %q", h)
	}
	if it := one(t, "event unknown"); it.Paper != nil {
		t.Errorf("an event not on the list made a bill: %+v", it.Paper)
	}
	p = one(t, "split ลี").Paper
	if p == nil || p.EventID != nil || len(p.People) != 2 || !eq(p.People[0].ContactID, &contactBoy.ID) || p.People[1].Name != "ลี" || p.People[1].ContactID != nil {
		t.Errorf("split = %+v", p)
	}
	if p = one(t, "split nobody").Paper; p == nil || len(p.People) != 0 {
		t.Errorf("split nobody = %+v", p)
	}
	if p = one(t, "split payer").Paper; p == nil || p.PaidBy == nil || !eq(p.PaidBy.ContactID, &contactBoy.ID) {
		t.Errorf("split payer = %+v", p)
	}
}

// A known contact the model gave by name is that contact (found on the first real run of the eval).
func TestAContactGivenByNameIsTheContact(t *testing.T) {
	if it := one(t, "debt by name บอย"); !eq(it.Debt.ContactID, &contactBoy.ID) || it.Debt.Name != "บอย" {
		t.Errorf("debt by name = %+v", it.Debt)
	}
	p := one(t, "split by name บอย").Paper
	if p == nil || len(p.People) != 1 || !eq(p.People[0].ContactID, &contactBoy.ID) {
		t.Errorf("split by name = %+v", p)
	}
}

func TestParseRecurringPayment(t *testing.T) {
	it := one(t, "recurring")
	if it.RecurringID == nil || *it.RecurringID != rulePower.ID || it.Amount != 500 || it.Slot != "2026-10-05" ||
		!eq(it.AccountID, &walletB.ID) || !eq(it.CategoryID, &catFood.ID) || it.Description == nil || *it.Description != "ค่าไฟ" {
		t.Errorf("recurring = %+v", it)
	}
	// no amount said: the rule's
	if it := one(t, "recurring noamount"); it.Amount != 500 || it.RecurringID == nil {
		t.Errorf("recurring noamount = %+v", it)
	}
	// no such rule: a plain expense
	if it := one(t, "recurring unknown"); it.RecurringID != nil || it.Amount != 500 {
		t.Errorf("recurring unknown = %+v", it)
	}
	// a rule with nothing left to record (a finished installment): a plain expense
	done := testLists
	done.Rules = []Rule{{ID: rulePower.ID, Name: "ค่าไฟ", Type: "expense", Amount: 500}}
	if out := parseKW(t, done, "recurring"); len(out.Items) != 1 || out.Items[0].RecurringID != nil {
		t.Errorf("rule without a slot = %+v", out.Items)
	}
	if it := one(t, "recurring unsure"); !contains(it.Unsure, "recurring") {
		t.Errorf("recurring unsure = %+v", it)
	}
}

func TestParseDebts(t *testing.T) {
	it := one(t, "debt lend")
	if it.Kind != KindDebt || it.Type != "" || it.Debt == nil || it.Debt.Side != SideTheyOweMe || !it.Debt.WithMoney ||
		!eq(it.Debt.ContactID, &contactBoy.ID) || !eq(it.AccountID, &walletA.ID) {
		t.Errorf("debt lend = %+v / %+v", it, it.Debt)
	}
	if it = one(t, "debt ledger"); it.Debt.WithMoney || it.AccountID != nil {
		t.Errorf("debt ledger = %+v / %+v", it, it.Debt)
	}
	if it = one(t, "debt borrow"); it.Debt.Side != SideIOweThem || !it.Debt.WithMoney {
		t.Errorf("debt borrow = %+v", it.Debt)
	}
	if it = one(t, "debt repay"); it.Debt.Side != SideTheyRepay || it.Amount != 200 {
		t.Errorf("debt repay = %+v", it)
	}
	if it = one(t, "debt unsure"); !contains(it.Unsure, "with_money") {
		t.Errorf("debt unsure = %+v", it)
	}
	if it = one(t, "debt name ลี"); it.Debt.ContactID != nil || it.Debt.Name != "ลี" {
		t.Errorf("debt name = %+v", it.Debt)
	}
	if it = one(t, "debt nobody"); it.Debt.ContactID != nil || it.Debt.Name != "" {
		t.Errorf("debt nobody = %+v", it.Debt)
	}
	out := parseKW(t, testLists, "mixed")
	if len(out.Items) != 2 || out.Items[0].Kind != KindCreate || out.Items[1].Kind != KindDebt {
		t.Errorf("mixed = %+v", out.Items)
	}
}

// A Reply edit hands the card back to the model in its own shape: every kind must survive the round trip.
func TestEditKeepsEveryKind(t *testing.T) {
	for _, kw := range []string{"normal tx", "transfer", "save goal", "allocate", "move spare", "withdraw saving",
		"event bill", "event payer", "event new", "split ลี", "recurring", "debt lend", "debt ledger", "debt unsure", "debt name ลี"} {
		first := parseKW(t, testLists, kw).Items
		edited, err := Parse(context.Background(), llm.NewKeywordFake(), testDay, testLists, "edit", first)
		if err != nil || len(edited.Items) != len(first) {
			t.Fatalf("%s: edit gave %d items (%v)", kw, len(edited.Items), err)
		}
		want := first[0]
		want.Amount = 50
		if !reflect.DeepEqual(edited.Items[0], want) {
			t.Errorf("%s: after an edit\n got %+v\nwant %+v", kw, edited.Items[0], want)
		}
	}
}

// The provider refuses an enum next to a type list ("Enum value 'equal' does not match declared type ['string', 'null']",
// found on the first real call): a nullable enum must be an anyOf.
func TestSchemaHasNoEnumNextToATypeList(t *testing.T) {
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			if _, hasEnum := x["enum"]; hasEnum {
				if _, isList := x["type"].([]string); isList {
					t.Errorf("%s: enum next to a type list", path)
				}
			}
			for k, c := range x {
				walk(path+"."+k, c)
			}
		case []any:
			for i, c := range x {
				walk(fmt.Sprintf("%s[%d]", path, i), c)
			}
		}
	}
	walk("schema", map[string]any(answerSchema))
}

func TestSchemaCoversTheDraft(t *testing.T) {
	// the schema asks for every key the answer reads, and no others
	item := answerSchema["properties"].(map[string]any)["items"].(map[string]any)["items"].(map[string]any)
	draft := item["properties"].(map[string]any)["draft"].(map[string]any)
	want := []string{"type", "amount", "description", "note", "date", "category_id", "account_id", "transfer_to_account_id",
		"goal_id", "from_goal_id", "saving_action", "recurring_id", "paper", "debt"}
	got := draft["required"].([]string)
	if !reflect.DeepEqual(got, want) || len(draft["properties"].(map[string]any)) != len(want) {
		t.Errorf("draft keys = %v", got)
	}
}

// The pending drafts a card turns into, and what a chat row needs on top of the pending page's own check.
func TestDraftsAndCheck(t *testing.T) {
	saver := NewPendingSaver(&pending.Service{})

	d := drafts([]Item{one(t, "save goal")})[0]
	if d.Type == nil || *d.Type != "saving" || d.SavingAction != ActSave || !eq(d.GoalID, &goalTrip.ID) || !eq(d.AccountID, &walletA.ID) {
		t.Errorf("saving draft = %+v", d)
	}
	mv := drafts([]Item{one(t, "move goals")})[0]
	if mv.SavingAction != ActMove || !eq(mv.FromGoalID, &goalTrip.ID) || !eq(mv.GoalID, &goalCar.ID) {
		t.Errorf("move draft = %+v", mv)
	}
	debt := drafts([]Item{one(t, "debt lend")})[0]
	if debt.Debt == nil || debt.Type != nil || debt.Debt.Side != SideTheyOweMe || !debt.Debt.WithMoney || !eq(debt.Debt.ContactID, &contactBoy.ID) {
		t.Errorf("debt draft = %+v / %+v", debt, debt.Debt)
	}
	named := drafts([]Item{one(t, "debt name ลี")})[0]
	if named.Debt.PersonName == nil || *named.Debt.PersonName != "ลี" || named.Debt.ContactID != nil {
		t.Errorf("named debt draft = %+v", named.Debt)
	}
	rec := drafts([]Item{one(t, "recurring")})[0]
	if rec.RecurringID == nil || *rec.RecurringID != rulePower.ID {
		t.Errorf("recurring draft = %+v", rec)
	}

	// an event bill: me first (I paid it all), then the three members; someone else paid: they paid it all
	bill := drafts([]Item{one(t, "event bill")})[0]
	if bill.Paper == nil || !eq(bill.Paper.ProjectID, &eventTrip.ID) || bill.Paper.Pay != "wallet" || len(bill.Paper.People) != 4 ||
		!bill.Paper.People[0].Me || bill.Paper.People[0].Paid != 3000 || bill.Paper.People[1].MemberID == nil {
		t.Errorf("event bill draft = %+v", bill.Paper)
	}
	other := drafts([]Item{one(t, "event payer")})[0].Paper
	paid := 0.0
	for _, p := range other.People {
		paid += p.Paid
	}
	if other.Pay != "debt" || other.People[0].Paid != 0 || paid != 900 {
		t.Errorf("event payer draft = %+v", other)
	}
	if n := drafts([]Item{one(t, "event new")})[0].Paper; n.NewEvent == nil || n.NewEvent.Name != "event new" {
		t.Errorf("new event draft = %+v", n)
	}

	// Check: a saving that touches a wallet names it; a bill has someone to share with; a debt names a person and a wallet
	got := saver.Check([]Item{one(t, "save nowallet"), one(t, "withdraw nowallet"), one(t, "save goal"), one(t, "split nobody"),
		one(t, "split ลี"), one(t, "debt nobody"), one(t, "debt lend")})
	// a new event may start with only me on it: nothing is missing
	if miss := saver.Check([]Item{one(t, "event new")}); len(miss[0]) != 0 {
		t.Errorf("a new event alone is not ready: %v", miss[0])
	}
	want := []string{"wallet", "wallet", "", "people", "", "person", ""}
	for i, w := range want {
		if (w == "" && len(got[i]) != 0) || (w != "" && (len(got[i]) != 1 || got[i][0] != w)) {
			t.Errorf("Check[%d] = %v, want %q", i, got[i], w)
		}
	}
}

func TestCardTextOfEveryKind(t *testing.T) {
	card := func(kw string) string {
		return cardText(Card{Items: parseKW(t, testLists, kw).Items, Lang: "th"}, testLists, testDay)
	}
	cases := []struct{ kw, want string }{
		{"save goal", "ออมเงิน ฿2,000\n🐷 เป้า: เที่ยวญี่ปุ่น\nกระเป๋า: 👛 กสิกร"},
		{"save nowallet", "ออมเงิน ฿2,000\n🐷 เป้า: เที่ยวญี่ปุ่น\nกระเป๋า: 👛 ไม่ระบุ"},
		{"allocate", "จัดสรร ฿2,000\n🐷 ยังไม่จัดสรร → เที่ยวญี่ปุ่น"},
		{"move spare", "ย้ายเป้า ฿500\n🐷 เที่ยวญี่ปุ่น → ยังไม่จัดสรร"},
		{"withdraw saving", "ถอนเงินออม ฿500\n🐷 เป้า: เที่ยวญี่ปุ่น\n👛 กลับเข้า: กสิกร"},
		{"event bill", "รายจ่าย -฿3,000\n🏝️ อีเวนต์: ทริปภูเก็ต\n💳 ฉันจ่ายเอง · หารเท่ากัน 4 คน\n👥 ฉัน · บอย · ลี · นุ่น\n🤝 รายการหนี้: ทุกคนติดเราคนละ ฿750\n💸 รายจ่าย: ออกจาก กสิกร ฿3,000\n🧮 ส่วนของฉัน ฿750\nหมวด: 🏷️ อาหาร"},
		{"event payer", "💳 บอยจ่าย · หารเท่ากัน 4 คน\n👥 ฉัน · บอย · ลี · นุ่น\n🤝 รายการหนี้: เราติดบอย ฿225\n💸 รายจ่าย: ไม่ออกจากกระเป๋า"},
		{"split ลี", "👥 ฉัน · 📇 บอย · ลี\n🤝 รายการหนี้: ทุกคนติดเราคนละ ฿200\n💸 รายจ่าย: ออกจาก กสิกร ฿600\n🧮 ส่วนของฉัน ฿200\n📨 แจ้ง: บอย"},
		{"split nobody", "👥 [ใคร ?]"},
		{"event new", "🆕 อีเวนต์ใหม่: event new"},
		{"recurring", "ค่าไฟ -฿500\n🔄 รายการประจำ: ค่าไฟ · งวด 5 ต.ค."},
		{"debt lend", "ให้บอยยืม -฿500\n🤝 รายการหนี้: บอยติดเรา ฿500\n💸 รายจ่าย: ออกจาก กสิกร ฿500"},
		{"debt ledger", "บอยค้างฉัน ฿500\n🤝 รายการหนี้: บอยติดเรา ฿500\n➖ ไม่มีรายจ่าย"},
		{"debt borrow", "ยืมบอย +฿500\n🤝 รายการหนี้: เราติดบอย ฿500\n💸 รายรับ: เข้า กสิกร ฿500"},
		{"debt unsure", "[💸 รายจ่าย: ออกจาก กสิกร ฿500 ?]"},
	}
	for _, c := range cases {
		if got := card(c.kw); !strings.Contains(got, c.want) {
			t.Errorf("%s: card\n%s\nlacks\n%s", c.kw, got, c.want)
		}
	}
	// English words for the same rows
	en := cardText(Card{Items: parseKW(t, testLists, "debt ledger").Items, Lang: "en"}, testLists, testDay)
	if !strings.Contains(en, "🤝 Debt: บอย owes me ฿500") || !strings.Contains(en, "➖ No expense") {
		t.Errorf("english debt card = %s", en)
	}
}
