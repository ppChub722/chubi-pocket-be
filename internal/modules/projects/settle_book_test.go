package projects

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
)

// 0.4.1-2 V12 (concept example): A owes B 300, B owes C 300 → simplified A pays C 300. I am A, I
// claimed my debt to B first; after completion "ลงในสมุดฉัน" = a 300 expense from my wallet + a
// repayment to B without a transaction (B nets to 0), once.
func TestBookSimplifiedSettle(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()
	h := NewHandler(f.svc)
	r := apitest.CountedEngine(f.owner, func(g *gin.RouterGroup) {
		g.POST("/projects", h.Create)
		g.POST("/projects/:id/complete", h.Complete)
		g.GET("/projects/:id/settle", h.Settle)
		g.POST("/projects/:id/settle/book", h.BookSettle)
		g.POST("/projects/:id/claim", h.Claim)
	})
	me := evClient{t, r}
	ev := m(me.do("POST", "/projects", map[string]any{"name": "Trip"}, "createProject", http.StatusCreated), "event")
	id := ev["id"].(string)
	mine := ev["my_member_id"].(string)
	beeM, chaiM := uuid.New(), uuid.New()
	if _, err := f.pool.Exec(ctx, `INSERT INTO project_members (id, project_id, user_id, display_name, role, status) VALUES
		($1, $2, $3, 'Bee', 'contributor', 'active'), ($4, $2, $5, 'Chai', 'contributor', 'active')`,
		beeM, id, f.friend, chaiM, f.mate); err != nil {
		t.Fatal(err)
	}
	bill := func(payer, debtor any, amount float64) string {
		b := uuid.New()
		if _, err := f.pool.Exec(ctx, `INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date)
			VALUES ($1, $2, $3, $4, 'expense', $5, 'THB', '2026-10-02')`, b, id, payer, f.owner, amount); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, `INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date, parent_project_transaction_id)
			VALUES (gen_random_uuid(), $2, $3, $4, 'expense', $5, 'THB', '2026-10-02', $1)`, b, id, debtor, f.owner, amount); err != nil {
			t.Fatal(err)
		}
		f.rowsToPeople(t, nil)
		return b.String()
	}
	b1 := bill(beeM, mine, 300) // Bee paid for me
	bill(chaiM, beeM, 300)      // Chai paid for Bee
	b3 := bill(beeM, mine, 10)  // not claimed before the booking…
	bill(mine, beeM, 10)        // …and offset, so the lines stay the same
	me.do("POST", "/projects/"+id+"/claim", map[string]any{"member_id": beeM, "bill_ids": []string{b1}, "create_copies": false},
		"claimBills", http.StatusOK)

	book := "/projects/" + id + "/settle/book"
	line := map[string]any{"lines": []map[string]any{{"member_id": chaiM, "account_id": f.acctPersonal, "category_id": f.cat}}}
	me.code("POST", book, line, http.StatusConflict, "EVENT_NOT_COMPLETED")
	if st := me.do("GET", "/projects/"+id+"/settle", nil, "getProjectSettle", http.StatusOK); st["simplified_available"] != false {
		t.Errorf("simplified_available while active = %v", st["simplified_available"])
	}
	me.do("POST", "/projects/"+id+"/complete", nil, "completeProject", http.StatusOK)
	me.code("POST", book, map[string]any{"lines": []map[string]any{}}, http.StatusBadRequest, "SETTLE_LINES")

	before := f.accountBalance(t, f.acctPersonal)
	got := me.do("POST", book, line, "bookEventSettle", http.StatusOK)
	if m(got, "settle")["my_settle_booking"] == nil {
		t.Errorf("no booking in answer: %v", got["settle"])
	}
	if after := f.accountBalance(t, f.acctPersonal); after != before-300 {
		t.Errorf("wallet %v → %v, want −300", before, after)
	}
	if c := f.countRows(t, `SELECT COUNT(*) FROM personal_debts WHERE user_id = $1 AND project_id = $2 AND kind = 'repayment'
		AND direction = 'paid' AND amount = 300 AND id NOT IN (SELECT source_personal_debt_id FROM transactions WHERE source_personal_debt_id IS NOT NULL)`,
		f.owner, id); c != 1 {
		t.Errorf("clearing repayment to Bee (no tx) = %d", c)
	}
	var net float64
	_ = f.pool.QueryRow(ctx, `SELECT COALESCE(SUM(CASE WHEN direction = 'paid' THEN amount ELSE -amount END), 0)::float8
		FROM personal_debts WHERE user_id = $1 AND project_id = $2`, f.owner, id).Scan(&net)
	if net != 0 {
		t.Errorf("my event rows net %v, want 0", net)
	}
	me.code("POST", book, line, http.StatusConflict, "SETTLE_BOOKED")
	me.code("POST", "/projects/"+id+"/claim", map[string]any{"member_id": beeM, "bill_ids": []string{b3}}, http.StatusConflict, "SETTLE_BOOKED")
}
