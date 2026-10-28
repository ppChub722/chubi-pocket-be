package projects

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// 0.4.1-2 V1 / V2: completing an event locks it for good.
func TestEventComplete(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()
	me := evClient{t, eventsEngine(f, f.owner)}
	bee := evClient{t, eventsEngine(f, f.friend)}
	trip := f.personalTag(t, f.owner, "Trip")

	other := m(me.do("POST", "/projects", map[string]any{"name": "Other"}, "createProject", http.StatusCreated), "event")
	me.code("PUT", "/projects/"+other["id"].(string), map[string]any{"status": "completed"}, http.StatusBadRequest, "USE_COMPLETE_ENDPOINT")

	ev := m(me.do("POST", "/projects", map[string]any{"name": "Japan"}, "createProject", http.StatusCreated), "event")
	id := ev["id"].(string)
	if ev["can_complete"] != true || ev["completed_at"] != nil {
		t.Fatalf("new event = %v", ev)
	}
	beeM, goneM := uuid.New(), uuid.New()
	if _, err := f.pool.Exec(ctx, `INSERT INTO project_members (id, project_id, user_id, display_name, role, status) VALUES
		($1, $2, $3, 'Bee', 'contributor', 'active'), ($4, $2, NULL, 'Gone', 'contributor', 'active')`,
		beeM, id, f.friend, goneM); err != nil {
		t.Fatal(err)
	}
	pts := "/projects/" + id + "/project-transactions"
	bill := m(me.do("POST", pts, map[string]any{
		"transaction_member_id": ev["my_member_id"], "type": "expense", "amount": 300, "date": "2026-10-02",
		"splits": []map[string]any{{"member_id": beeM, "amount": 100}, {"member_id": goneM, "amount": 50}},
	}, "createProjectTransaction", http.StatusCreated), "row")
	ptID := bill["id"].(string)

	bee.code("POST", "/projects/"+id+"/complete", nil, http.StatusForbidden, "FORBIDDEN_ROLE")
	// default 3: a removed member's spot must be assigned first
	if _, err := f.pool.Exec(ctx, `UPDATE project_members SET status = 'left', left_at = NOW(), removed_at = NOW() WHERE id = $1`, goneM); err != nil {
		t.Fatal(err)
	}
	me.code("POST", "/projects/"+id+"/complete", nil, http.StatusConflict, "EVENT_HAS_EMPTY_PERSON")
	if _, err := f.pool.Exec(ctx, `DELETE FROM project_transactions WHERE transaction_member_id = $1`, goneM); err != nil {
		t.Fatal(err)
	}

	got := me.do("POST", "/projects/"+id+"/complete", nil, "completeProject", http.StatusOK)
	e := m(got, "event")
	if e["status"] != "completed" || e["completed_at"] == nil || e["can_complete"] != false || e["is_locked"] != true ||
		e["can_edit"] != false || e["can_add_bill"] != false || e["can_manage_members"] != false {
		t.Fatalf("completed event = %v", e)
	}
	if m(got, "effects") == nil {
		t.Errorf("no effects: %v", got)
	}
	if c := f.countRows(t, `SELECT COUNT(*) FROM notifications WHERE recipient_user_id = $1 AND type = 'event_completed'
		AND payload->>'project_id' = $2`, f.friend, id); c != 1 {
		t.Errorf("event_completed to Bee = %d", c)
	}

	me.code("POST", "/projects/"+id+"/complete", nil, http.StatusConflict, "PROJECT_NOT_ACTIVE")
	me.code("POST", pts, map[string]any{"transaction_member_id": ev["my_member_id"], "type": "expense", "amount": 10, "date": "2026-10-03"},
		http.StatusConflict, "EVENT_COMPLETED")
	me.code("PUT", pts+"/"+ptID, map[string]any{"amount": 400}, http.StatusConflict, "EVENT_COMPLETED")
	me.code("DELETE", pts+"/"+ptID, nil, http.StatusConflict, "EVENT_COMPLETED")
	me.code("PUT", pts+"/"+ptID+"/mark", map[string]any{"marked": true}, http.StatusConflict, "EVENT_COMPLETED")
	me.code("POST", "/projects/"+id+"/members", map[string]any{"ad_hoc": true, "display_name": "New"}, http.StatusConflict, "EVENT_COMPLETED")
	me.code("PUT", "/projects/"+id, map[string]any{"name": "Renamed"}, http.StatusConflict, "EVENT_COMPLETED")
	me.code("PUT", "/projects/"+id, map[string]any{"status": "active"}, http.StatusConflict, "EVENT_COMPLETED")
	me.code("PUT", "/projects/"+id, map[string]any{"status": "cancelled"}, http.StatusConflict, "EVENT_COMPLETED")
	if tags := list(m(me.do("PUT", "/projects/"+id, map[string]any{"tag_ids": []uuid.UUID{trip}}, "updateProject", http.StatusOK), "event"), "tags"); len(tags) != 1 {
		t.Errorf("my tags on a completed event = %v", tags)
	}

	// archive keeps it locked; restore returns it to completed
	if s := m(me.do("POST", "/projects/"+id+"/archive", nil, "archiveProject", http.StatusOK), "event")["status"]; s != "archived" {
		t.Errorf("archive = %v", s)
	}
	if s := m(me.do("POST", "/projects/"+id+"/restore", nil, "restoreProject", http.StatusOK), "event")["status"]; s != "completed" {
		t.Errorf("restore of a completed event = %v, want completed", s)
	}

	// the DB backstop (a write racing the complete call): bill values refused, link columns still change
	if _, err := f.pool.Exec(ctx, `UPDATE project_transactions SET amount = 999 WHERE id = $1`, ptID); err == nil ||
		!strings.Contains(err.Error(), "EVENT_COMPLETED") {
		t.Errorf("trigger on amount: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE project_transactions SET linked_transaction_id = NULL WHERE id = $1`, ptID); err != nil {
		t.Errorf("link column on a completed bill: %v", err)
	}
	// owner 2026-10-28: a completed event can be deleted
	me.do("DELETE", "/projects/"+id, nil, "deleteProject", http.StatusOK)
}
