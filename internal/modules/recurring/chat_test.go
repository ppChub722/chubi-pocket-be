package recurring

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

// The chatbot's "จ่ายค่าไฟ 500": the slot is chosen in code (the latest past slot that is still unpaid, else the
// next one) and the transaction is tied to it.
func TestChatPickSlotAndLinkNext(t *testing.T) {
	f := newRecFix(t)
	d := f.createWeekly(5) // slots at -14, -7, 0, +7, +14 days
	id := d.Recurring.ID
	f.track(id)

	rules, err := f.svc.ChatRules(f.ctx, f.me)
	if err != nil || len(rules) != 1 || rules[0].ID != id || rules[0].Name != "Netflix" || rules[0].Amount != 100 {
		t.Fatalf("ChatRules = %+v, %v", rules, err)
	}

	pick := func() string {
		t.Helper()
		slot, ok, err := f.svc.PickSlot(f.ctx, f.me, id, f.today)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return ""
		}
		return slot.Format("2006-01-02")
	}
	// three slots have passed (today's counts): the latest is picked
	if got := pick(); got != f.day(0) {
		t.Fatalf("first pick = %q, want today's slot %q", got, f.day(0))
	}
	// a payment is tied to it; the next pick is the latest one that is still unpaid
	if err := f.svc.LinkNext(f.ctx, f.me, id, f.newTx(120, 0)); err != nil {
		t.Fatalf("LinkNext: %v", err)
	}
	if got := pick(); got != f.day(-7) {
		t.Fatalf("second pick = %q, want %q", got, f.day(-7))
	}
	// a slot that was skipped is not "unpaid"
	f.call("POST", f.occ(id, f.day(-7))+"/skip", "", nil, 200, nil)
	if got := pick(); got != f.day(-14) {
		t.Fatalf("pick after a skip = %q, want %q", got, f.day(-14))
	}
	// every past slot settled: the next upcoming one
	if err := f.svc.LinkNext(f.ctx, f.me, id, f.newTx(100, -14)); err != nil {
		t.Fatalf("LinkNext: %v", err)
	}
	if got := pick(); got != f.day(7) {
		t.Fatalf("pick with nothing past = %q, want the next slot %q", got, f.day(7))
	}
	// the transaction keeps its own date and amount; one slot got it
	if n := f.storedRows(id); n != 3 {
		t.Errorf("stored rows = %d, want 3 (two linked, one skipped)", n)
	}
	// an unknown rule has nothing to link
	if err := f.svc.LinkNext(f.ctx, f.me, uuid.New(), f.newTx(10, 0)); !errors.Is(err, ErrNoSlot) {
		t.Errorf("LinkNext on an unknown rule = %v, want ErrNoSlot", err)
	}
}
