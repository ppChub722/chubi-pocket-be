package projects

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
)

// mpEvent — an event with me (A) and three ad-hoc members, and a way to add multi-payer bills.
type mpEvent struct {
	t               *testing.T
	f               *qcFixture
	me              evClient
	id              string
	a, b, lee, pond uuid.UUID
}

func newMPEvent(t *testing.T) *mpEvent {
	f := newQCFixture(t)
	h := NewHandler(f.svc)
	me := evClient{t, apitest.CountedEngine(f.owner, func(g *gin.RouterGroup) {
		g.POST("/projects", h.Create)
		g.GET("/projects/:id", h.Get)
		g.DELETE("/projects/:id", h.Delete)
		g.POST("/projects/:id/complete", h.Complete)
		g.GET("/projects/:id/settle", h.Settle)
		g.GET("/projects/:id/claimable", h.ListClaimable)
		g.POST("/projects/:id/claim", h.Claim)
	})}
	ev := m(me.do("POST", "/projects", map[string]any{"name": "Krabi"}, "createProject", http.StatusCreated), "event")
	e := &mpEvent{t: t, f: f, me: me, id: ev["id"].(string), a: uuid.MustParse(ev["my_member_id"].(string)),
		b: uuid.New(), lee: uuid.New(), pond: uuid.New()}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO project_members (id, project_id, user_id, display_name, role, status) VALUES
		($1, $4, NULL, 'B', 'contributor', 'active'), ($2, $4, NULL, 'Lee', 'contributor', 'active'),
		($3, $4, NULL, 'Pond', 'contributor', 'active')`, e.b, e.lee, e.pond, e.id); err != nil {
		t.Fatal(err)
	}
	return e
}

// bill writes a bill of total: payers (member → paid), shares of everyone but the main payer (whose
// share is the rest).
func (e *mpEvent) bill(total float64, payers, shares map[uuid.UUID]float64) string {
	e.t.Helper()
	ctx := context.Background()
	var main uuid.UUID
	best := -1.0
	for m, a := range payers {
		if a > best {
			main, best = m, a
		}
	}
	id := uuid.New()
	if _, err := e.f.pool.Exec(ctx, `INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date)
		VALUES ($1, $2, $3, $4, 'expense', $5, 'THB', '2026-10-28')`, id, e.id, main, e.f.owner, total); err != nil {
		e.t.Fatal(err)
	}
	for m, a := range shares {
		if m == main {
			continue
		}
		if _, err := e.f.pool.Exec(ctx, `INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date, parent_project_transaction_id)
			VALUES (gen_random_uuid(), $2, $3, $4, 'expense', $5, 'THB', '2026-10-28', $1)`, id, e.id, m, e.f.owner, a); err != nil {
			e.t.Fatal(err)
		}
	}
	e.f.rowsToPeople(e.t, nil)
	tx, err := e.f.pool.Begin(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := SetBillPayersTx(ctx, tx, id, payers); err != nil {
		e.t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		e.t.Fatal(err)
	}
	return id.String()
}

func (e *mpEvent) name(r map[string]any) string {
	if r["is_me"] == true {
		return "A"
	}
	return r["name"].(string)
}

// Lead's example: 1,200 paid by ลี 700 + ปอนด์ 500, shared by 4 × 300 → A owes ลี 175 and ปอนด์ 125;
// ปอนด์ and ลี net to ปอนด์ owes ลี 50 (175 − 125).
func TestMultiPayerSettleProportional(t *testing.T) {
	e := newMPEvent(t)
	e.bill(1200, map[uuid.UUID]float64{e.lee: 700, e.pond: 500},
		map[uuid.UUID]float64{e.a: 300, e.b: 300, e.pond: 300})
	st := e.me.do("GET", "/projects/"+e.id+"/settle", nil, "getProjectSettle", http.StatusOK)
	got := map[[2]string]float64{}
	for _, p := range st["pairs"].([]any) {
		l := p.(map[string]any)
		got[[2]string{e.name(m(l, "from")), e.name(m(l, "to"))}] = n(l, "amount")
	}
	want := map[[2]string]float64{{"A", "Lee"}: 175, {"A", "Pond"}: 125, {"B", "Lee"}: 175, {"B", "Pond"}: 125, {"Pond", "Lee"}: 50}
	if len(got) != len(want) {
		t.Errorf("pairs = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("pair %v = %v, want %v", k, got[k], v)
		}
	}
	if ev := e.me.do("GET", "/projects/"+e.id, nil, "getProject", http.StatusOK); n(ev, "my_net") != -300 || n(ev, "my_paid") != 0 {
		t.Errorf("A: my_net %v / my_paid %v, want −300 / 0", ev["my_net"], ev["my_paid"])
	}
}

// A lump across two multi-payer bills, then complete → delete: the bills become split bills and my
// lump, its parts and my copies stay connected to them.
func TestMultiPayerLumpThenDeleteEvent(t *testing.T) {
	e := newMPEvent(t)
	ctx := context.Background()
	b1 := e.bill(1200, map[uuid.UUID]float64{e.lee: 700, e.pond: 500},
		map[uuid.UUID]float64{e.a: 300, e.b: 300, e.pond: 300}) // A owes Lee 175
	b2 := e.bill(600, map[uuid.UUID]float64{e.lee: 400, e.b: 200},
		map[uuid.UUID]float64{e.a: 300}) // shares A 300, Lee 300 → A owes Lee 200, B 100

	cl := e.me.do("GET", "/projects/"+e.id+"/claimable?member_id="+e.lee.String(), nil, "listClaimableBills", http.StatusOK)
	if tot := cl["totals"].([]any); len(tot) != 1 || n(tot[0].(map[string]any), "unclaimed_net") != -375 {
		t.Fatalf("with Lee = %v, want −375", tot)
	}
	got := e.me.do("POST", "/projects/"+e.id+"/claim", map[string]any{"member_id": e.lee, "bill_ids": []string{b1, b2}},
		"claimBills", http.StatusOK)
	d := got["debts"].([]any)[0].(map[string]any)
	if n(d, "amount") != 375 || d["direction"] != "received" || len(list(d, "bills")) != 2 {
		t.Fatalf("lump = %v", d)
	}
	debtID := d["id"].(string)
	// my copies = my share (300 each), "จ่ายโดย" (no wallet)
	if c := e.f.countRows(t, `SELECT COUNT(*) FROM transactions WHERE user_id = $1 AND account_id IS NULL AND amount = 300
		AND source_project_transaction_id IN ($2::uuid, $3::uuid)`, e.f.owner, b1, b2); c != 2 {
		t.Errorf("my copies = %d, want 2 × 300", c)
	}
	// B pays the rest of the pair with Lee on another lump? A's pair with B on b2 stays open
	e.me.code("POST", "/projects/"+e.id+"/claim", map[string]any{"member_id": e.lee, "bill_ids": []string{b2}},
		http.StatusConflict, "BILL_ALREADY_BOOKED")

	e.me.do("POST", "/projects/"+e.id+"/complete", nil, "completeProject", http.StatusOK)
	out := e.me.do("DELETE", "/projects/"+e.id, nil, "deleteProject", http.StatusOK)
	if n(out, "split_bill_count") != 2 {
		t.Errorf("split_bill_count = %v", out["split_bill_count"])
	}
	if c := e.f.countRows(t, `SELECT COUNT(*) FROM projects WHERE id = $1`, e.id); c != 0 {
		t.Errorf("the event is still there")
	}
	// each bill is now a split bill (no event, its own members), payers kept
	if c := e.f.countRows(t, `SELECT COUNT(*) FROM project_transactions x WHERE x.id IN ($1::uuid, $2::uuid)
		AND x.project_id IS NULL AND x.scope_id = x.id
		AND NOT EXISTS (SELECT 1 FROM split_transaction_people sp JOIN project_members pm ON pm.id = sp.event_member_id
		                 WHERE sp.split_transaction_id = x.id AND pm.split_transaction_id IS DISTINCT FROM x.id)`, b1, b2); c != 2 {
		t.Errorf("split bills = %d, want 2", c)
	}
	if c := e.f.countRows(t, `SELECT COUNT(*) FROM split_transaction_people WHERE split_transaction_id IN ($1::uuid, $2::uuid) AND paid > 0`, b1, b2); c != 4 {
		t.Errorf("payers rows kept = %d, want 4", c)
	}
	// my lump still covers both bills, my copies still point at them
	if c := e.f.countRows(t, `SELECT COUNT(*) FROM personal_debt_bills WHERE debt_id = $1`, debtID); c != 2 {
		t.Errorf("lump parts = %d, want 2", c)
	}
	if c := e.f.countRows(t, `SELECT COUNT(*) FROM transactions t
		WHERE t.user_id = $1 AND t.source_project_transaction_id IN ($2::uuid, $3::uuid) AND t.project_id IS NULL
		  AND t.split_transaction_id = t.source_project_transaction_id`, e.f.owner, b1, b2); c != 2 {
		t.Errorf("copies connected to the split bills = %d, want 2", c)
	}
	var amount float64
	_ = e.f.pool.QueryRow(ctx, `SELECT amount::float8 FROM personal_debts WHERE id = $1`, debtID).Scan(&amount)
	if amount != 375 {
		t.Errorf("lump amount after delete = %v", amount)
	}
}
