package projects

import (
	"context"
	"math"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
)

// Split phase A (migrations 124-125): the people rows follow the old child rows / payers / marks /
// opt-outs / paid-back ticks through the triggers, and the readers already moved to them give the
// numbers worked out by hand. One event with every kind of bill:
//
//	b1 expense 300, me paid; shares me 100, Lee 100, Bee 100; Lee marked; my copy disconnected
//	b2 expense 200, Lee paid 150 + Bee 50; shares me 100, Lee 100 (the main payer's rest)
//	b3 income   90, Bee received; shares me 30, Lee 30, Bee 30
//	b4 payback 100, Lee -> me, made as the "คืนแล้ว" of b1
//
// By hand: paid me 300 / Lee 150 / Bee 50; share me 200 / Lee 200 / Bee 100; sent Lee 100;
// received me 100; income net me +30, Lee +30, Bee -60 → net me +30, Lee +80, Bee -110.
func TestSplitPeopleFollowTheOldRows(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()
	h := NewHandler(f.svc)
	me := evClient{t, apitest.CountedEngine(f.owner, func(g *gin.RouterGroup) {
		g.POST("/projects", h.Create)
		g.GET("/projects/:id/detail", h.Detail)
	})}
	ev := m(me.do("POST", "/projects", map[string]any{"name": "Trip"}, "createProject", http.StatusCreated), "event")
	id := ev["id"].(string)
	mine := uuid.MustParse(ev["my_member_id"].(string))
	lee, bee := uuid.New(), uuid.New()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := f.pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO project_members (id, project_id, user_id, display_name, role, status) VALUES
		($1, $2, NULL, 'Lee', 'contributor', 'active'), ($3, $2, NULL, 'Bee', 'contributor', 'active')`, lee, id, bee)

	bill := func(member uuid.UUID, typ string, amount float64) uuid.UUID {
		t.Helper()
		b := uuid.New()
		exec(`INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date)
			VALUES ($1, $2, $3, $4, $5, $6, 'THB', '2026-10-30')`, b, id, member, f.owner, typ, amount)
		return b
	}
	child := func(parent, member uuid.UUID, typ string, amount float64) uuid.UUID {
		t.Helper()
		c := uuid.New()
		exec(`INSERT INTO project_transactions (id, project_id, parent_project_transaction_id, transaction_member_id,
			record_user_id, type, amount, currency, date) VALUES ($1, $2, $3, $4, $5, $6, $7, 'THB', '2026-10-30')`,
			c, id, parent, member, f.owner, typ, amount)
		return c
	}

	b1 := bill(mine, "expense", 300)
	child(b1, lee, "expense", 100)
	beeOnB1 := child(b1, bee, "expense", 100)
	exec(`UPDATE project_transactions SET marks = ARRAY[$2::uuid] WHERE id = $1`, b1, lee)
	exec(`INSERT INTO shared_bill_opt_outs (project_transaction_id, user_id) VALUES ($1, $2)`, b1, f.owner)

	b2 := bill(lee, "expense", 200)
	child(b2, mine, "expense", 100)
	exec(`INSERT INTO project_transaction_payers (project_transaction_id, member_id, amount) VALUES ($1, $2, 150), ($1, $3, 50)`, b2, lee, bee)

	b3 := bill(bee, "income", 90)
	child(b3, mine, "income", 30)
	child(b3, lee, "income", 30)

	b4 := bill(lee, "payback", 100)
	child(b4, mine, "payback", 100)
	exec(`UPDATE project_transactions SET paid_back_for_id = $2 WHERE id = $1`, b4, b1)

	// the people rows
	type person struct {
		share, paid           float64
		paidBack, marked, off bool
	}
	people := func(b uuid.UUID) map[uuid.UUID]person {
		t.Helper()
		rows, err := f.pool.Query(ctx, `SELECT event_member_id, share::float8, paid::float8, paid_back,
			marked_at IS NOT NULL, disconnected_at IS NOT NULL FROM split_transaction_people WHERE split_transaction_id = $1`, b)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[uuid.UUID]person{}
		for rows.Next() {
			var (
				mid uuid.UUID
				p   person
			)
			if err := rows.Scan(&mid, &p.share, &p.paid, &p.paidBack, &p.marked, &p.off); err != nil {
				t.Fatal(err)
			}
			out[mid] = p
		}
		return out
	}
	want := func(name string, got map[uuid.UUID]person, w map[uuid.UUID]person) {
		t.Helper()
		if len(got) != len(w) {
			t.Fatalf("%s: %d people, want %d (%+v)", name, len(got), len(w), got)
		}
		for k, v := range w {
			if got[k] != v {
				t.Fatalf("%s: member %s = %+v, want %+v", name, k, got[k], v)
			}
		}
	}
	want("b1", people(b1), map[uuid.UUID]person{
		mine: {share: 100, paid: 300, off: true},
		lee:  {share: 100, paidBack: true, marked: true},
		bee:  {share: 100},
	})
	want("b2", people(b2), map[uuid.UUID]person{
		mine: {share: 100},
		lee:  {share: 100, paid: 150},
		bee:  {paid: 50},
	})
	want("b3", people(b3), map[uuid.UUID]person{
		mine: {share: 30},
		lee:  {share: 30},
		bee:  {share: 30, paid: 90},
	})
	want("b4", people(b4), map[uuid.UUID]person{
		mine: {share: 100},
		lee:  {paid: 100},
	})

	// the event page: positions from the people rows match the hand numbers
	type pos struct{ paid, share, sent, received, net float64 }
	wantPos := map[string]pos{
		mine.String(): {paid: 300, share: 200, received: 100, net: 30},
		lee.String():  {paid: 150, share: 200, sent: 100, net: 80},
		bee.String():  {paid: 50, share: 100, net: -110},
	}
	detail := me.do("GET", "/projects/"+id+"/detail", nil, "getProjectDetail", http.StatusOK)
	members := m(detail, "summary")["members"].([]any)
	if len(members) != 3 {
		t.Fatalf("positions: %d members, want 3", len(members))
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 0.005 }
	for _, raw := range members {
		p := raw.(map[string]any)
		mid := p["member"].(map[string]any)["id"].(string)
		w, ok := wantPos[mid]
		if !ok {
			t.Fatalf("positions: unexpected member %s", mid)
		}
		got := pos{p["paid"].(float64), p["share"].(float64), p["sent"].(float64), p["received"].(float64), p["net"].(float64)}
		if !near(got.paid, w.paid) || !near(got.share, w.share) || !near(got.sent, w.sent) ||
			!near(got.received, w.received) || !near(got.net, w.net) {
			t.Fatalf("position of %s = %+v, want %+v", mid, got, w)
		}
	}

	// edits through the old tables keep the people equal: a smaller split, a removed split,
	// the main payer swapped
	exec(`UPDATE project_transactions SET amount = 50 WHERE parent_project_transaction_id = $1 AND transaction_member_id = $2`, b1, lee)
	exec(`DELETE FROM project_transactions WHERE id = $1`, beeOnB1)
	want("b1 edited", people(b1), map[uuid.UUID]person{
		mine: {share: 250, paid: 300, off: true},
		lee:  {share: 50, paidBack: true, marked: true},
	})
	exec(`DELETE FROM project_transaction_payers WHERE project_transaction_id = $1`, b2)
	exec(`UPDATE project_transactions SET transaction_member_id = $2 WHERE id = $1`, b2, bee)
	want("b2 one payer", people(b2), map[uuid.UUID]person{
		mine: {share: 100},
		bee:  {share: 100, paid: 200},
	})
	exec(`UPDATE project_transactions SET marks = '{}' WHERE id = $1`, b1)
	exec(`DELETE FROM shared_bill_opt_outs WHERE project_transaction_id = $1`, b1)
	exec(`DELETE FROM project_transactions WHERE id = $1`, b4)
	want("b1 unticked", people(b1), map[uuid.UUID]person{
		mine: {share: 250, paid: 300},
		lee:  {share: 50},
	})

	// every bill still adds up
	var bad int
	if err := f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM project_transactions b
		WHERE b.project_id = $1 AND b.parent_project_transaction_id IS NULL
		  AND ((SELECT COALESCE(SUM(share), 0) FROM split_transaction_people WHERE split_transaction_id = b.id) <> b.amount
		    OR (SELECT COALESCE(SUM(paid), 0) FROM split_transaction_people WHERE split_transaction_id = b.id) <> b.amount)`,
		id).Scan(&bad); err != nil {
		t.Fatal(err)
	}
	if bad != 0 {
		t.Fatalf("%d bill(s) whose people do not add up", bad)
	}
}
