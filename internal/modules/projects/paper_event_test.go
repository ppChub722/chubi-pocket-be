package projects

import (
	"testing"
)

// eventWithBee — A's event "Trip" with Bee (linked, joined at once) and Dee (a typed name).
func (f *fx) eventWithBee(A client) (string, map[string]string) {
	f.t.Helper()
	f.settings(f.b, map[string]any{"auto_accept_event_invites": true})
	ev := str(obj(A.do("POST", "/projects", map[string]any{"name": "Trip"}, "createProject", 201), "event"), "id")
	A.do("POST", "/projects/"+ev+"/members", map[string]any{"contact_id": f.contactB}, "addProjectMember", 201)
	A.do("POST", "/projects/"+ev+"/members", map[string]any{"display_name": "Dee", "ad_hoc": true}, "addProjectMember", 201)
	ids := map[string]string{}
	for _, m := range arr(A.do("GET", "/projects/"+ev+"/members", nil, "listProjectMembers", 200), "data") {
		ids[str(obj(m), "name")] = str(obj(m), "id")
	}
	return ev, ids
}

// Event bills: Bee's auto claim books her share with her event defaults and no debt (events settle
// as a lump); the settle page books the pair once; completing the event locks its bills.
func TestEventBillsAndLump(t *testing.T) {
	f := newFx(t)
	A, B := f.as(f.a), f.as(f.b)
	ev, mem := f.eventWithBee(A)
	if mem["Bee"] == "" || mem["Dee"] == "" {
		t.Fatalf("members: %v", mem)
	}
	f.settings(f.b, map[string]any{"auto_accept_events": true, "auto_sync_events": true})
	B.do("PUT", "/projects/"+ev+"/my-defaults", map[string]any{"category_id": f.catB, "account_id": nil},
		"setEventMyDefaults", 200)

	created := A.do("POST", "/projects/"+ev+"/bills", map[string]any{"paper": map[string]any{
		"type": "expense", "amount": 300, "date": "2026-11-01", "description": "Hotel", "split_mode": "equal",
		"people": []any{
			map[string]any{"me": true, "paid": 300},
			map[string]any{"member_id": mem["Bee"]},
			map[string]any{"member_id": mem["Dee"]},
		}}, "my_books": map[string]any{"account_id": f.walletA, "category_id": f.catA}}, "createEventBill", 201)
	bill := str(obj(created, "paper"), "id")
	if b := f.balance(f.walletA); !near(b, 700) {
		t.Errorf("A's wallet %v, want 700", b)
	}
	if f.count(`SELECT COUNT(*) FROM personal_debts WHERE user_id = ANY($1)`, []any{f.a, f.b}) != 0 {
		t.Errorf("an event bill made debts (events settle as a lump)")
	}
	bView := B.do("GET", "/projects/"+ev+"/bills/"+bill, nil, "getEventBill", 200)
	bTx := obj(bView, "my_books", "transaction")
	if bTx == nil || !near(num(bTx, "amount"), 100) || bTx["account"] != nil || str(obj(bTx, "category"), "id") != f.catB.String() {
		t.Errorf("Bee's auto claim: %v", bTx)
	}
	if obj(bTx, "event_bill") == nil {
		t.Errorf("Bee's tx does not point at the bill")
	}
	if l := arr(A.do("GET", "/projects/"+ev+"/bills", nil, "listEventBills", 200), "data"); len(l) != 1 {
		t.Errorf("board: %d bills", len(l))
	}
	A.do("GET", "/projects/"+ev+"/detail", nil, "getProjectDetail", 200)

	// Settle: Bee owes A 100, Dee owes A 100. A books the pair with Bee → one lump debt.
	settle := A.do("GET", "/projects/"+ev+"/settle", nil, "getProjectSettle", 200)
	if len(arr(settle, "pairs")) != 2 {
		t.Errorf("pairs: %v", arr(settle, "pairs"))
	}
	A.do("POST", "/projects/"+ev+"/settle/book", map[string]any{"mode": "pairs", "with": mem["Bee"]},
		"bookEventSettle", 200)
	if v := f.personNet(f.a, "Bee"); !near(v, 100) {
		t.Errorf("A's lump with Bee: %v, want 100", v)
	}
	A.fail("POST", "/projects/"+ev+"/settle/book", map[string]any{"mode": "pairs", "with": mem["Bee"]}, 422, "SETTLE_NOTHING")

	// A second bill moves the pair: the lump shows out of sync until อัปเดต.
	A.do("POST", "/projects/"+ev+"/bills", map[string]any{"paper": map[string]any{
		"type": "expense", "amount": 60, "date": "2026-11-02", "split_mode": "equal",
		"people": []any{map[string]any{"me": true, "paid": 60}, map[string]any{"member_id": mem["Bee"]}}},
		"my_books": map[string]any{"account_id": f.walletA}}, "createEventBill", 201)
	for _, l := range arr(A.do("GET", "/projects/"+ev+"/settle", nil, "getProjectSettle", 200), "pairs") {
		line := obj(l)
		if str(obj(line, "from"), "name") == "Bee" && (!near(num(line, "amount"), 130) || !near(num(line, "unbooked"), 30)) {
			t.Errorf("Bee's line after the 2nd bill: %v", line)
		}
	}

	// Complete: bills are locked.
	A.do("POST", "/projects/"+ev+"/complete", nil, "completeProject", 200)
	A.fail("PUT", "/projects/"+ev+"/bills/"+bill, map[string]any{"version": 1, "paper": map[string]any{
		"type": "expense", "amount": 10, "date": "2026-11-01", "people": []any{map[string]any{"me": true, "paid": 10, "share": 10}}}},
		409, "EVENT_COMPLETED")
}

// A split bill moved into an event finds its people among the members (the same user, the very same
// name); moved out again it is a split bill with the same people.
func TestMovePaperMatchesMembers(t *testing.T) {
	f := newFx(t)
	A := f.as(f.a)
	ev, mem := f.eventWithBee(A)
	f.settings(f.b, map[string]any{"auto_accept_debts": true})
	paper := obj(A.do("POST", "/split-bills", map[string]any{"paper": map[string]any{
		"type": "expense", "amount": 90, "date": "2026-11-01", "split_mode": "equal", "people": []any{
			map[string]any{"me": true, "paid": 90},
			map[string]any{"contact_id": f.contactB},
			map[string]any{"person_name": "Dee"},
		}}, "my_books": map[string]any{"account_id": f.walletA}}, "", 201), "paper")
	id := str(paper, "id")
	moved := obj(A.do("POST", "/split-bills/"+id+"/move-to-event", map[string]any{"project_id": ev}, "moveSplitBillToEvent", 200), "paper")
	if str(person(moved, "Bee"), "member_id") != mem["Bee"] || str(person(moved, "Dee"), "member_id") != mem["Dee"] {
		t.Errorf("people not matched to members: Bee %v Dee %v (members %v)", person(moved, "Bee"), person(moved, "Dee"), mem)
	}
	if n := f.count(`SELECT COUNT(*) FROM project_members WHERE project_id = $1`, ev); n != 3 {
		t.Errorf("members after the move: %d, want 3 (no new ones)", n)
	}
	if f.notices(f.b, "bill_moved") != 1 {
		t.Errorf("Bee got no bill_moved")
	}
	A.fail("GET", "/split-bills/"+id, nil, 404, "BILL_NOT_FOUND")
	out := obj(A.do("POST", "/projects/"+ev+"/bills/"+id+"/move", map[string]any{"project_id": nil}, "moveEventBill", 200), "paper")
	if out["project_id"] != nil || len(arr(out, "people")) != 3 {
		t.Errorf("moved out: %v", out)
	}
}

// Two members that are one person: merging adds their shares and paid on every bill.
func TestMergeMember(t *testing.T) {
	f := newFx(t)
	A := f.as(f.a)
	ev, mem := f.eventWithBee(A)
	A.do("POST", "/projects/"+ev+"/members", map[string]any{"display_name": "D", "ad_hoc": true}, "addProjectMember", 201)
	for _, m := range arr(A.do("GET", "/projects/"+ev+"/members", nil, "", 200), "data") {
		mem[str(obj(m), "name")] = str(obj(m), "id")
	}
	bill := str(obj(A.do("POST", "/projects/"+ev+"/bills", map[string]any{"paper": map[string]any{
		"type": "expense", "amount": 90, "date": "2026-11-01", "split_mode": "custom", "people": []any{
			map[string]any{"me": true, "paid": 90, "share": 30},
			map[string]any{"member_id": mem["Dee"], "share": 30},
			map[string]any{"member_id": mem["D"], "share": 30},
		}}, "my_books": map[string]any{"account_id": f.walletA}}, "", 201), "paper"), "id")
	A.fail("POST", "/projects/"+ev+"/members/"+mem["Bee"]+"/merge", map[string]any{"into": mem["Alice"]}, 409, "MERGE_TWO_USERS")
	A.do("POST", "/projects/"+ev+"/members/"+mem["D"]+"/merge", map[string]any{"into": mem["Dee"]}, "mergeEventMember", 200)
	view := A.do("GET", "/projects/"+ev+"/bills/"+bill, nil, "getEventBill", 200)
	if len(arr(view, "people")) != 2 || !near(num(person(view, "Dee"), "share"), 60) {
		t.Errorf("after merge: %v", arr(view, "people"))
	}
}

// A split-bill create with new_event makes the event and its first bill in one go: its people become
// members; on an event's own bills new_event is refused.
func TestNewEventWithBill(t *testing.T) {
	f := newFx(t)
	A := f.as(f.a)
	created := A.do("POST", "/split-bills", map[string]any{
		"new_event": map[string]any{"name": "Khao Yai"},
		"paper": map[string]any{
			"type": "expense", "amount": 300, "date": "2026-11-01", "split_mode": "equal",
			"people": []any{
				map[string]any{"me": true, "paid": 300},
				map[string]any{"person_name": "Dee"},
			}},
		"my_books": map[string]any{"account_id": f.walletA}}, "createSplitBill", 201)
	ev := str(obj(created, "paper"), "project_id")
	if ev == "" {
		t.Fatalf("no event on the bill: %v", obj(created, "paper"))
	}
	names := map[string]bool{}
	for _, m := range arr(A.do("GET", "/projects/"+ev+"/members", nil, "listProjectMembers", 200), "data") {
		names[str(obj(m), "name")] = true
	}
	if !names["Dee"] || len(names) != 2 {
		t.Errorf("members: %v", names)
	}
	if l := arr(A.do("GET", "/projects/"+ev+"/bills", nil, "listEventBills", 200), "data"); len(l) != 1 {
		t.Errorf("board: %d bills", len(l))
	}
	A.fail("POST", "/projects/"+ev+"/bills", map[string]any{
		"new_event": map[string]any{"name": "Again"},
		"paper": map[string]any{"type": "expense", "amount": 10, "date": "2026-11-01",
			"people": []any{map[string]any{"me": true, "paid": 10, "share": 10}}}}, 400, "VALIDATION_ERROR")
}
