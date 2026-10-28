package projects

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
)

// 0.4.1-2 V13 (X6): deleting an event bill follows each member's auto_sync_events.
func TestDeleteEventBillBySwitch(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()
	h := NewHandler(f.svc)
	engine := func(as uuid.UUID) evClient {
		return evClient{t, apitest.CountedEngine(as, func(g *gin.RouterGroup) {
			g.POST("/projects", h.Create)
			g.POST("/projects/:id/claim", h.Claim)
			g.DELETE("/event-bills/:pt_id", h.DeleteBill)
		})}
	}
	me, bee, chai := engine(f.owner), engine(f.friend), engine(f.mate)
	ev := m(me.do("POST", "/projects", map[string]any{"name": "Trip"}, "createProject", http.StatusCreated), "event")
	id := ev["id"].(string)
	mine := ev["my_member_id"].(string)
	beeM, chaiM := uuid.New(), uuid.New()
	if _, err := f.pool.Exec(ctx, `INSERT INTO project_members (id, project_id, user_id, display_name, role, status) VALUES
		($1, $2, $3, 'Bee', 'contributor', 'active'), ($4, $2, $5, 'Chai', 'contributor', 'active')`,
		beeM, id, f.friend, chaiM, f.mate); err != nil {
		t.Fatal(err)
	}
	bill := func(debtor uuid.UUID, amount, share float64) string {
		b := uuid.New()
		if _, err := f.pool.Exec(ctx, `INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date)
			VALUES ($1, $2, $3, $4, 'expense', $5, 'THB', '2026-10-02')`, b, id, mine, f.owner, amount); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, `INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date, parent_project_transaction_id)
			VALUES (gen_random_uuid(), $2, $3, $4, 'expense', $5, 'THB', '2026-10-02', $1)`, b, id, debtor, f.owner, share); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	bBee, bChai := bill(beeM, 300, 100), bill(chaiM, 200, 50)
	// a "คืนแล้ว" payback made for bBee
	if _, err := f.pool.Exec(ctx, `INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date, paid_back_for_id)
		VALUES (gen_random_uuid(), $1, $2, $3, 'payback', 10, 'THB', '2026-10-02', $4)`, id, beeM, f.owner, bBee); err != nil {
		t.Fatal(err)
	}
	ownerID := uuid.MustParse(mine)
	bee.do("POST", "/projects/"+id+"/claim", map[string]any{"member_id": ownerID, "bill_ids": []string{bBee}}, "claimBills", http.StatusOK)
	chai.do("POST", "/projects/"+id+"/claim", map[string]any{"member_id": ownerID, "bill_ids": []string{bChai}}, "claimBills", http.StatusOK)
	if _, err := f.pool.Exec(ctx, `INSERT INTO user_notification_settings (user_id, auto_sync_events) VALUES ($1, FALSE)
		ON CONFLICT (user_id) DO UPDATE SET auto_sync_events = FALSE`, f.mate); err != nil {
		t.Fatal(err)
	}
	beeTx := f.countRows(t, `SELECT COUNT(*) FROM transactions WHERE user_id = $1 AND source_project_transaction_id = $2`, f.friend, bBee)
	if beeTx != 1 {
		t.Fatalf("Bee's copy before = %d", beeTx)
	}

	me.do("DELETE", "/event-bills/"+bBee, nil, "deleteEventBill", http.StatusOK)
	if c := f.countRows(t, `SELECT COUNT(*) FROM personal_debts WHERE user_id = $1 AND project_id = $2`, f.friend, id); c != 0 {
		t.Errorf("Bee (switch on) still has %d debts of the event", c)
	}
	if c := f.countRows(t, `SELECT COUNT(*) FROM project_transactions WHERE paid_back_for_id = $1 OR id = $1`, bBee); c != 0 {
		t.Errorf("bill / its paybacks left: %d", c)
	}
	if c := f.countRows(t, `SELECT COUNT(*) FROM notifications WHERE recipient_user_id = $1 AND type = 'shared_bill_deleted'
		AND (payload->>'applied')::boolean`, f.friend); c != 1 {
		t.Errorf("Bee's info notice = %d", c)
	}

	me.do("DELETE", "/event-bills/"+bChai, nil, "deleteEventBill", http.StatusOK)
	if c := f.countRows(t, `SELECT COUNT(*) FROM personal_debts WHERE user_id = $1 AND kind = 'debt' AND amount = 50
		AND source_kind IS NULL AND project_id IS NULL`, f.mate); c != 1 {
		t.Errorf("Chai (switch off): plain debt kept = %d", c)
	}
	if c := f.countRows(t, `SELECT COUNT(*) FROM transactions WHERE user_id = $1 AND amount = 50 AND project_id IS NULL
		AND source_project_transaction_id IS NULL`, f.mate); c != 1 {
		t.Errorf("Chai's copy kept plain = %d", c)
	}
	if c := f.countRows(t, `SELECT COUNT(*) FROM notifications WHERE recipient_user_id = $1 AND type = 'shared_bill_deleted'
		AND NOT (payload->>'applied')::boolean AND jsonb_array_length(payload->'my_debt_ids') = 1`, f.mate); c != 1 {
		t.Errorf("Chai's asking notice = %d", c)
	}
}
