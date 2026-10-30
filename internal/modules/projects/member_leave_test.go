package projects

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// 0.4.1-2 V15: removing a member leaves an empty spot to assign and disconnects their book; leaving
// disconnects the leaver's book only.
func TestRemoveMemberEmptySpotAndAssign(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()
	me := evClient{t, eventsEngine(f, f.owner)}
	ev := m(me.do("POST", "/projects", map[string]any{"name": "Trip"}, "createProject", http.StatusCreated), "event")
	id := ev["id"].(string)
	beeM, chaiM := uuid.New(), uuid.New()
	if _, err := f.pool.Exec(ctx, `INSERT INTO project_members (id, project_id, user_id, display_name, role, status) VALUES
		($1, $2, $3, 'Bee', 'contributor', 'active'), ($4, $2, $5, 'Chai', 'contributor', 'active')`,
		beeM, id, f.friend, chaiM, f.mate); err != nil {
		t.Fatal(err)
	}
	pts := "/projects/" + id + "/project-transactions"
	// Bee paid 300 (me 100, Chai 100); I paid 200 (Bee 50)
	b1 := m(me.do("POST", pts, map[string]any{"transaction_member_id": beeM, "type": "expense", "amount": 300, "date": "2026-10-02",
		"splits": []map[string]any{{"member_id": ev["my_member_id"], "amount": 100}, {"member_id": chaiM, "amount": 100}}},
		"createProjectTransaction", http.StatusCreated), "row")["id"].(string)
	b2 := m(me.do("POST", pts, map[string]any{"transaction_member_id": ev["my_member_id"], "type": "expense", "amount": 200, "date": "2026-10-03",
		"splits": []map[string]any{{"member_id": beeM, "amount": 50}}}, "createProjectTransaction", http.StatusCreated), "row")["id"].(string)
	// Bee's book holds a copy of b1 (a transaction of the event)
	beeTx := f.insertCopy(t, f.friend, uuid.MustParse(id), uuid.MustParse(b1), 200)

	me.code("POST", "/projects/"+id+"/members/"+beeM.String()+"/assign", map[string]any{"to": map[string]any{"me": true}},
		http.StatusConflict, "MEMBER_NOT_REMOVED")
	got := me.do("DELETE", "/projects/"+id+"/members/"+beeM.String(), nil, "removeProjectMember", http.StatusOK)
	if n(m(got, "event"), "empty_person_bill_count") != 2 {
		t.Errorf("empty person bills = %v", m(got, "event")["empty_person_bill_count"])
	}
	if c := f.countRows(t, `SELECT COUNT(*) FROM transactions WHERE id = $1 AND project_id IS NULL AND source_project_transaction_id IS NULL`, beeTx); c != 1 {
		t.Errorf("Bee's copy still connected")
	}
	if c := f.countRows(t, `SELECT COUNT(*) FROM notifications WHERE recipient_user_id = $1 AND type = 'event_member_removed'`, f.friend); c != 1 {
		t.Errorf("event_member_removed = %d", c)
	}
	me.code("POST", "/projects/"+id+"/complete", nil, http.StatusConflict, "EVENT_HAS_EMPTY_PERSON")

	// b1: Chai takes the payer spot (his own 100 share merges into the rest); b2: Bee's 50 share goes to Chai
	got = me.do("POST", "/projects/"+id+"/members/"+beeM.String()+"/assign",
		map[string]any{"to": map[string]any{"member_id": chaiM}}, "assignRemovedMember", http.StatusOK)
	if n(m(got, "event"), "empty_person_bill_count") != 0 {
		t.Errorf("after assign = %v", m(got, "event")["empty_person_bill_count"])
	}
	var payer uuid.UUID
	var children int
	_ = f.pool.QueryRow(ctx, `SELECT transaction_member_id, (SELECT COUNT(*) FROM split_transaction_people c WHERE c.split_transaction_id = p.id AND c.sort > 0 AND c.share > 0)
		FROM project_transactions p WHERE id = $1`, b1).Scan(&payer, &children)
	if payer != chaiM || children != 1 {
		t.Errorf("b1 payer %v children %d, want Chai and 1 (my share)", payer, children)
	}
	if c := f.countRows(t, `SELECT COUNT(*) FROM split_transaction_people WHERE split_transaction_id = $1
		AND event_member_id = $2 AND share = 50`, b2, chaiM); c != 1 {
		t.Errorf("b2 share not moved to Chai")
	}
	me.do("POST", "/projects/"+id+"/complete", nil, "completeProject", http.StatusOK)
}
