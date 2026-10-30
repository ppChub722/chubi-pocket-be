package projects

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
)

// 0.4.1-2 multi-payer (owner example): a 123 bill, payers ปอนด์ (me) 110 + ลี 13, my share 123 → I owe
// ลี 13 (a partial "จ่ายแทน"). A second bill with two payers and two shares checks the proportion.
func TestMultiPayerPairs(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()
	h := NewHandler(f.svc)
	me := evClient{t, apitest.CountedEngine(f.owner, func(g *gin.RouterGroup) {
		g.POST("/projects", h.Create)
		g.GET("/projects/:id", h.Get)
		g.GET("/projects/:id/settle", h.Settle)
		g.GET("/projects/:id/claimable", h.ListClaimable)
		g.GET("/projects/:id/transactions", h.ListPT)
	})}
	ev := m(me.do("POST", "/projects", map[string]any{"name": "Lunch"}, "createProject", http.StatusCreated), "event")
	id := ev["id"].(string)
	mine := uuid.MustParse(ev["my_member_id"].(string))
	lee, bee := uuid.New(), uuid.New()
	if _, err := f.pool.Exec(ctx, `INSERT INTO project_members (id, project_id, user_id, display_name, role, status) VALUES
		($1, $2, NULL, 'Lee', 'contributor', 'active'), ($3, $2, NULL, 'Bee', 'contributor', 'active')`, lee, id, bee); err != nil {
		t.Fatal(err)
	}
	b1 := uuid.New()
	if _, err := f.pool.Exec(ctx, `INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date)
		VALUES ($1, $2, $3, $4, 'expense', 123, 'THB', '2026-10-28')`, b1, id, mine, f.owner); err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetBillPayersTx(ctx, tx, b1, map[uuid.UUID]float64{mine: 110, lee: 13}); err != nil {
		t.Fatal(err)
	}
	pairs, err := BillPairsTx(ctx, tx, b1)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 || pairs[0].Debtor != mine || pairs[0].Creditor != lee || pairs[0].Amount != 13 {
		t.Fatalf("123 bill pairs = %+v, want me owes Lee 13", pairs)
	}

	// 200 bill: Lee paid 150, Bee 50; shares me 100, Lee 100 (Lee's rest)
	b2 := uuid.New()
	if _, err := f.pool.Exec(ctx, `INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date)
		VALUES ($1, $2, $3, $4, 'expense', 200, 'THB', '2026-10-28')`, b2, id, lee, f.owner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date, parent_project_transaction_id)
		VALUES (gen_random_uuid(), $2, $3, $4, 'expense', 100, 'THB', '2026-10-28', $1)`, b2, id, mine, f.owner); err != nil {
		t.Fatal(err)
	}
	f.rowsToPeople(t, nil)
	tx, _ = f.pool.Begin(ctx)
	if err := SetBillPayersTx(ctx, tx, b2, map[uuid.UUID]float64{lee: 150, bee: 50}); err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit(ctx)
	// b2: I owe Lee 75 + Bee 25; Lee owes Bee 25 (Lee's share 100 × 50/200)
	// total: me → Lee 13 + 75 = 88, me → Bee 25, Lee → Bee 25
	st := me.do("GET", "/projects/"+id+"/settle", nil, "getProjectSettle", http.StatusOK)
	want := map[[2]string]float64{{"me", "Lee"}: 88, {"me", "Bee"}: 25, {"Lee", "Bee"}: 25}
	got := map[[2]string]float64{}
	name := func(r map[string]any) string {
		if r["is_me"] == true {
			return "me"
		}
		return r["name"].(string)
	}
	for _, p := range st["pairs"].([]any) {
		l := p.(map[string]any)
		got[[2]string{name(m(l, "from")), name(m(l, "to"))}] = n(l, "amount")
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("pair %v = %v, want %v (all %v)", k, got[k], v, got)
		}
	}
	if e := me.do("GET", "/projects/"+id, nil, "getProject", http.StatusOK); n(e, "my_net") != -113 || n(e, "my_paid") != 110 {
		t.Errorf("my_net %v / my_paid %v, want −113 / 110", e["my_net"], e["my_paid"])
	}
	cl := me.do("GET", "/projects/"+id+"/claimable?member_id="+lee.String(), nil, "listClaimableBills", http.StatusOK)
	if tot := cl["totals"].([]any); len(tot) != 1 || n(tot[0].(map[string]any), "unclaimed_net") != -88 {
		t.Errorf("claimable with Lee = %v", tot)
	}
	board := me.do("GET", "/projects/"+id+"/transactions", nil, "listBoardRows", http.StatusOK)
	for _, r := range board["data"].([]any) {
		row := r.(map[string]any)
		if row["id"] == b2.String() {
			ps := row["payers"].([]any)
			if len(ps) != 2 || m(ps[0].(map[string]any), "member")["name"] != "Lee" || n(ps[0].(map[string]any), "amount") != 150 {
				t.Errorf("b2 payers = %v", ps)
			}
		}
	}
}
