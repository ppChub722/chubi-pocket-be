package projects

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
)

// 0.4.1-2 V6 / V9 / V11 / X7: claim bills with one person (a lump), once.
func TestClaimBills(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()
	h := NewHandler(f.svc)
	r := apitest.CountedEngine(f.owner, func(g *gin.RouterGroup) {
		g.POST("/projects", h.Create)
		g.POST("/projects/:id/project-transactions", h.CreatePT)
		g.GET("/projects/:id/claimable", h.ListClaimable)
		g.POST("/projects/:id/claim", h.Claim)
	})
	me := evClient{t, r}
	ev := m(me.do("POST", "/projects", map[string]any{"name": "Trip"}, "createProject", http.StatusCreated), "event")
	id := ev["id"].(string)
	mine := ev["my_member_id"]
	beeM, chaiM := uuid.New(), uuid.New()
	if _, err := f.pool.Exec(ctx, `INSERT INTO project_members (id, project_id, user_id, display_name, role, status) VALUES
		($1, $2, $3, 'Bee', 'contributor', 'active'), ($4, $2, $5, 'Chai', 'contributor', 'active')`,
		beeM, id, f.friend, chaiM, f.mate); err != nil {
		t.Fatal(err)
	}
	pts := "/projects/" + id + "/project-transactions"
	bill := func(payer any, typ string, amount float64, date string, splits ...map[string]any) string {
		return m(me.do("POST", pts, map[string]any{"transaction_member_id": payer, "type": typ, "amount": amount, "date": date,
			"splits": splits}, "createProjectTransaction", http.StatusCreated), "row")["id"].(string)
	}
	b1 := bill(beeM, "expense", 300, "2026-10-02", map[string]any{"member_id": mine, "amount": 100}, map[string]any{"member_id": chaiM, "amount": 100})
	b2 := bill(beeM, "expense", 90, "2026-10-03", map[string]any{"member_id": mine, "amount": 30})
	b3 := bill(mine, "expense", 200, "2026-10-04", map[string]any{"member_id": beeM, "amount": 50})
	p1 := uuid.New().String() // Bee paid me back 20 (a payback row; inserted directly)
	if _, err := f.pool.Exec(ctx, `INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date) VALUES
		($1, $2, $3, $4, 'payback', 20, 'THB', '2026-10-05')`, p1, id, beeM, f.owner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date, parent_project_transaction_id) VALUES
		(gen_random_uuid(), $2, $3, $4, 'payback', 20, 'THB', '2026-10-05', $1)`, p1, id, mine, f.owner); err != nil {
		t.Fatal(err)
	}
	f.rowsToPeople(t, nil)

	list := me.do("GET", "/projects/"+id+"/claimable?member_id="+beeM.String(), nil, "listClaimableBills", http.StatusOK)
	if d := list["data"].([]any); len(d) != 4 {
		t.Fatalf("claimable with Bee = %d rows: %v", len(d), d)
	}
	if tot := list["totals"].([]any); len(tot) != 1 || n(tot[0].(map[string]any), "unclaimed_net") != -100 {
		t.Errorf("totals = %v (−100 −30 +50 −20)", tot)
	}
	if d := me.do("GET", "/projects/"+id+"/claimable?contact_id="+f.contactB.String(), nil, "listClaimableBills", http.StatusOK)["data"].([]any); len(d) != 4 {
		t.Errorf("by my contact for Bee = %d rows", len(d))
	}
	if d := me.do("GET", "/projects/"+id+"/claimable?person_name=Nobody", nil, "listClaimableBills", http.StatusOK)["data"].([]any); len(d) != 0 {
		t.Errorf("unknown name = %d rows", len(d))
	}
	claim := "/projects/" + id + "/claim"
	me.code("POST", claim, map[string]any{"member_id": beeM, "bill_ids": []string{b1, b3}}, http.StatusBadRequest, "CLAIM_MIXED_DIRECTION")
	me.code("POST", claim, map[string]any{"member_id": chaiM, "bill_ids": []string{b2}}, http.StatusBadRequest, "NOT_IN_BILL")

	// preview: nothing written
	me.do("POST", claim, map[string]any{"member_id": beeM, "bill_ids": []string{b1, b2}, "preview": true}, "claimBills", http.StatusOK)
	if c := f.countRows(t, `SELECT COUNT(*) FROM personal_debt_bills WHERE user_id = $1`, f.owner); c != 0 {
		t.Fatalf("preview wrote %d parts", c)
	}
	got := me.do("POST", claim, map[string]any{"member_id": beeM, "bill_ids": []string{b1, b2}}, "claimBills", http.StatusOK)
	debts := got["debts"].([]any)
	if len(debts) != 1 || n(debts[0].(map[string]any), "amount") != 130 || debts[0].(map[string]any)["direction"] != "received" {
		t.Fatalf("lump = %v", debts)
	}
	if c := f.countRows(t, `SELECT COUNT(*) FROM personal_debt_bills WHERE user_id = $1 AND transaction_id IS NOT NULL`, f.owner); c != 2 {
		t.Errorf("parts with my copy = %d, want 2", c)
	}
	if c := f.countRows(t, `SELECT COUNT(*) FROM transactions WHERE user_id = $1 AND account_id IS NULL
		AND source_project_transaction_id IN ($2::uuid, $3::uuid)`, f.owner, b1, b2); c != 2 {
		t.Errorf("จ่ายโดย copies = %d, want 2", c)
	}
	me.code("POST", claim, map[string]any{"member_id": beeM, "bill_ids": []string{b1}}, http.StatusConflict, "BILL_ALREADY_BOOKED")

	// a payback = a repayment row
	got = me.do("POST", claim, map[string]any{"member_id": beeM, "bill_ids": []string{p1}}, "claimBills", http.StatusOK)
	if d := got["debts"].([]any); len(d) != 1 || d[0].(map[string]any)["kind"] != "repayment" || n(d[0].(map[string]any), "amount") != 20 {
		t.Errorf("payback claim = %v", d)
	}
	// b3 (I paid; my origin was made with it): Bee owes me 50
	got = me.do("POST", claim, map[string]any{"member_id": beeM, "bill_ids": []string{b3}}, "claimBills", http.StatusOK)
	if d := got["debts"].([]any); len(d) != 1 || d[0].(map[string]any)["direction"] != "paid" || n(d[0].(map[string]any), "amount") != 50 {
		t.Errorf("b3 claim = %v", d)
	}
	// b4 (I paid 80, Bee 40; no transaction of mine): connect my existing 80 transaction as its origin
	b4 := uuid.New().String()
	if _, err := f.pool.Exec(ctx, `INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date) VALUES
		($1, $2, $3, $4, 'expense', 80, 'THB', '2026-10-06')`, b4, id, mine, f.owner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date, parent_project_transaction_id) VALUES
		(gen_random_uuid(), $2, $3, $4, 'expense', 40, 'THB', '2026-10-06', $1)`, b4, id, beeM, f.owner); err != nil {
		t.Fatal(err)
	}
	f.rowsToPeople(t, nil)
	existing := f.insertBill(t, f.owner, f.acctPersonal, f.cat, 80, "2026-10-06")
	me.do("POST", claim, map[string]any{"member_id": beeM, "bill_ids": []string{b4}, "link_transaction_id": existing}, "claimBills", http.StatusOK)
	if c := f.countRows(t, `SELECT COUNT(*) FROM project_transactions WHERE id = $1 AND linked_transaction_id = $2`, b4, existing); c != 1 {
		t.Errorf("b4 origin not linked")
	}
	list = me.do("GET", "/projects/"+id+"/claimable?member_id="+beeM.String(), nil, "listClaimableBills", http.StatusOK)
	if tot := list["totals"].([]any); len(tot) != 0 {
		t.Errorf("after claims totals = %v", tot)
	}
}
