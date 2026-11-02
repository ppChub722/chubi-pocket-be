package projects

import (
	"context"
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
)

// Bee pays back her debt of a split bill and ticks "notify": A (auto accept off) gets debt_repaid
// that still asks; after a disconnect the pair is gone and nothing crosses any more.
func TestRepaymentNotifiesPartner(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	f.settings(f.b, map[string]any{"auto_accept_debts": true})
	f.settings(f.a, map[string]any{"auto_accept_repayments": false})
	A := f.as(f.a)
	A.do("POST", "/split-bills", map[string]any{"paper": map[string]any{
		"type": "expense", "amount": 200, "date": "2026-11-01", "split_mode": "equal", "people": []any{
			map[string]any{"me": true, "paid": 200},
			map[string]any{"contact_id": f.contactB},
		}}, "my_books": map[string]any{"account_id": f.walletA}}, "", 201)
	if v := f.personNet(f.b, "Alice"); !near(v, -100) {
		t.Fatalf("Bee's auto claim: owes %v, want 100", -v)
	}
	no := false
	key := "c:" + f.contactA.String()
	if _, err := f.debts.CreateRepayment(ctx, f.b, key, personal_debts.RepaymentRequest{
		Kind: personal_debts.RepayPay, Amount: 100, CreateTransaction: &no, Notify: true}); err != nil {
		t.Fatalf("repayment: %v", err)
	}
	if v := f.personNet(f.b, "Alice"); !near(v, 0) {
		t.Errorf("Bee's book after paying back: %v", v)
	}
	if n := f.count(`SELECT COUNT(*) FROM notifications WHERE recipient_user_id = $1 AND type = 'debt_repaid'
		AND payload->>'repayment_id' IS NULL AND actioned_at IS NULL`, f.a); n != 1 {
		t.Errorf("A's asking debt_repaid notices: %d, want 1", n)
	}
	if v := f.personNet(f.a, "Bee"); !near(v, 100) {
		t.Errorf("A's book moved without accepting: Bee owes %v", v)
	}

	var beeDebt string
	if err := f.pool.QueryRow(ctx, `SELECT id::text FROM personal_debts WHERE user_id = $1 AND kind = 'debt'`, f.b).Scan(&beeDebt); err != nil {
		t.Fatal(err)
	}
	if _, err := f.debts.Disconnect(ctx, f.b, mustUUID(t, beeDebt)); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if n := f.count(`SELECT COUNT(*) FROM personal_debts WHERE user_id = ANY($1) AND counterpart_debt_id IS NOT NULL`,
		[]any{f.a, f.b}); n != 0 {
		t.Errorf("partners left after disconnect: %d", n)
	}
	if _, err := f.debts.CreateRepayment(ctx, f.b, key, personal_debts.RepaymentRequest{
		Kind: personal_debts.RepayPay, Amount: 10, CreateTransaction: &no, Notify: true}); err != nil {
		t.Fatalf("repayment: %v", err)
	}
	if f.notices(f.a, "debt_repaid") != 1 {
		t.Errorf("a notice crossed after the disconnect")
	}
}

// After complete: Bee can still claim (a one-time copy), the settle books the fewest transfers once.
func TestCompletedEventClaimAndSimplified(t *testing.T) {
	f := newFx(t)
	A, B := f.as(f.a), f.as(f.b)
	ev, mem := f.eventWithBee(A)
	f.settings(f.b, map[string]any{"auto_accept_events": false})
	bill := str(obj(A.do("POST", "/projects/"+ev+"/bills", map[string]any{"paper": map[string]any{
		"type": "expense", "amount": 300, "date": "2026-11-01", "split_mode": "equal", "people": []any{
			map[string]any{"me": true, "paid": 300},
			map[string]any{"member_id": mem["Bee"]},
			map[string]any{"member_id": mem["Dee"]},
		}}, "my_books": map[string]any{"account_id": f.walletA}}, "", 201), "paper"), "id")
	if f.count(`SELECT COUNT(*) FROM transactions WHERE user_id = $1`, f.b) != 0 {
		t.Fatalf("Bee claimed with auto accept off")
	}
	if f.notices(f.b, "bill_new") != 1 {
		t.Errorf("Bee got no bill_new")
	}
	A.do("POST", "/projects/"+ev+"/complete", nil, "completeProject", 200)

	got := B.do("POST", "/projects/"+ev+"/bills/"+bill+"/claim", map[string]any{"category_id": f.catB}, "claimEventBill", 200)
	if tx := obj(got, "paper", "my_books", "transaction"); tx == nil || !near(num(tx, "amount"), 100) {
		t.Errorf("Bee's claim after complete: %v", tx)
	}

	A.do("POST", "/projects/"+ev+"/settle/book", map[string]any{"mode": "simplified"}, "bookEventSettle", 200)
	if v := f.personNet(f.a, "Bee"); !near(v, 100) {
		t.Errorf("A's simplified line with Bee: %v, want 100", v)
	}
	if v := f.personNet(f.a, "Dee"); !near(v, 100) {
		t.Errorf("A's simplified line with Dee: %v, want 100", v)
	}
	A.fail("POST", "/projects/"+ev+"/settle/book", map[string]any{"mode": "simplified"}, 409, "SETTLE_BOOKED")
}
