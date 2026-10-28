package projects

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// 0.4.1-2 V14 (Q-E3, default 2): on an income bill the member who received owes each split.
func TestIncomeBillInSettle(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()
	me := evClient{t, eventsEngine(f, f.owner)}
	ev := m(me.do("POST", "/projects", map[string]any{"name": "Market"}, "createProject", http.StatusCreated), "event")
	id := ev["id"].(string)
	beeM := uuid.New()
	if _, err := f.pool.Exec(ctx, `INSERT INTO project_members (id, project_id, user_id, display_name, role, status) VALUES
		($1, $2, NULL, 'Bee', 'contributor', 'active')`, beeM, id); err != nil {
		t.Fatal(err)
	}
	// Bee received 300 for the stall; my part is 100
	b, c := uuid.New(), uuid.New()
	if _, err := f.pool.Exec(ctx, `INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date)
		VALUES ($1, $2, $3, $4, 'income', 300, 'THB', '2026-10-02')`, b, id, beeM, f.owner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date, parent_project_transaction_id)
		VALUES ($1, $2, $3, $4, 'income', 100, 'THB', '2026-10-02', $5)`, c, id, ev["my_member_id"], f.owner, b); err != nil {
		t.Fatal(err)
	}
	st := me.do("GET", "/projects/"+id+"/settle", nil, "getProjectSettle", http.StatusOK)
	pairs := st["pairs"].([]any)
	if len(pairs) != 1 {
		t.Fatalf("pairs = %v", pairs)
	}
	p := pairs[0].(map[string]any)
	if m(p, "from")["name"] != "Bee" || m(p, "to")["is_me"] != true || n(p, "amount") != 100 {
		t.Errorf("pair = %v, want Bee owes me 100", p)
	}
	got := me.do("GET", "/projects/"+id, nil, "getProject", http.StatusOK)
	if n(got, "my_net") != 100 || n(got, "my_receivable") != 100 {
		t.Errorf("my_net = %v / receivable %v, want 100", got["my_net"], got["my_receivable"])
	}
	sum := me.do("GET", "/projects/"+id+"/summary", nil, "getProjectSummary", http.StatusOK)
	if pos := m(sum, "my_position"); n(pos, "net") != 100 {
		t.Errorf("my position = %v", pos)
	}
}
