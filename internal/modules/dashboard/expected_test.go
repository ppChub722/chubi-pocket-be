package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/accounts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/budgets"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/saving_goals"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// fakeExpected serves fixed occurrences the way recurring.Service serves a
// window.
type fakeExpected struct{ occs []shared.ExpectedOccurrence }

func (f fakeExpected) ExpectedBetween(_ context.Context, _ uuid.UUID, from, to time.Time) ([]shared.ExpectedOccurrence, error) {
	var out []shared.ExpectedOccurrence
	for _, o := range f.occs {
		if !o.Date.Before(from) && !o.Date.After(to) {
			out = append(out, o)
		}
	}
	return out, nil
}

// router is a dashboard whose service reads e (nil = the stored items).
func (e *dashEnv) router(t *testing.T, ex shared.Expecter) *gin.Engine {
	t.Helper()
	catSvc := categories.NewService(categories.NewStore(e.pool))
	accSvc := accounts.NewService(accounts.NewStore(e.pool), e.txs, catSvc)
	pdSvc := personal_debts.NewService(personal_debts.NewStore(e.pool), e.txs)
	svc := NewService(NewStore(e.pool), e.txs, accSvc, budgets.NewService(budgets.NewStore(e.pool)),
		saving_goals.NewService(saving_goals.NewStore(e.pool)), pdSvc)
	if ex != nil {
		svc.WithExpected(ex)
	}
	h := NewHandler(svc)
	return apitest.Engine(e.uid, func(g *gin.RouterGroup) { g.GET("/dashboard", h.Get) })
}

type upcomingJSON struct {
	Upcoming struct {
		Items []struct {
			Kind    string     `json:"kind"`
			ID      uuid.UUID  `json:"id"`
			Name    string     `json:"name"`
			Type    string     `json:"type"`
			Amount  float64    `json:"amount"`
			DueDate string     `json:"due_date"`
			RuleID  *uuid.UUID `json:"rule_id"`
			Slot    *string    `json:"slot"`
		} `json:"items"`
		TotalExpense float64 `json:"total_expense"`
	} `json:"upcoming"`
}

func TestDashboardUpcomingReadsComputedOccurrences(t *testing.T) {
	e := newDashEnv(t)
	loc, _ := time.LoadLocation("Asia/Bangkok")
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return today.AddDate(0, 0, n) }
	fmtDay := func(n int) string { return day(n).Format("2006-01-02") }

	rule, other, stored := uuid.New(), uuid.New(), uuid.New()
	occ := func(r uuid.UUID, name string, n int, amount float64, due bool) shared.ExpectedOccurrence {
		return shared.ExpectedOccurrence{
			RuleID: r, RuleName: name, Name: name + " ประจำเดือน", Slot: day(n), Date: day(n),
			Type: "expense", Amount: amount, Due: due,
		}
	}
	editedRow := occ(other, "Gym", 5, 300, false)
	editedRow.ItemID = &stored
	f := fakeExpected{occs: []shared.ExpectedOccurrence{
		occ(rule, "Netflix", 3, 100, false), // computed, not due: listed
		editedRow,                           // a stored (edited) row: listed with its own id
		occ(rule, "Netflix", 0, 100, true),  // due: it is in ต้องจัดการ, not upcoming
		occ(rule, "Netflix", 20, 100, false), // past the 14 days: not asked for
	}}

	r := e.router(t, f)
	status, body := apitest.Do(t, r, "GET", "/api/v1/dashboard", nil)
	apitest.Conform(t, "getDashboard", status, body)
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	var d upcomingJSON
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatal(err)
	}
	items := d.Upcoming.Items
	if len(items) != 2 {
		t.Fatalf("upcoming items = %+v, want the computed Netflix and the stored Gym", items)
	}
	// Ordered by date: Netflix (+3) first, then Gym (+5).
	n, g := items[0], items[1]
	if n.Kind != "recurring" || n.Name != "Netflix ประจำเดือน" || n.Amount != 100 || n.DueDate != fmtDay(3) {
		t.Errorf("computed row = %+v", n)
	}
	if n.ID != rule || n.RuleID == nil || *n.RuleID != rule || n.Slot == nil || *n.Slot != fmtDay(3) {
		t.Errorf("computed row id / rule_id / slot = %s / %v / %v, want the rule id, the rule id, %s", n.ID, n.RuleID, n.Slot, fmtDay(3))
	}
	if g.ID != stored || g.RuleID == nil || *g.RuleID != other || g.Slot == nil || *g.Slot != fmtDay(5) || g.Amount != 300 {
		t.Errorf("stored row = %+v, want id %s, rule_id %s, slot %s", g, stored, other, fmtDay(5))
	}
	if d.Upcoming.TotalExpense != 400 {
		t.Errorf("total_expense = %v, want 400", d.Upcoming.TotalExpense)
	}
}

// With no Expecter the stored recurring items are read as before, and a row
// has no rule_id / slot.
func TestDashboardUpcomingWithoutExpecterKeepsStoredItems(t *testing.T) {
	e := newDashEnv(t)
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM recurring_items WHERE user_id = $1`, e.uid)
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM recurring_rules WHERE user_id = $1`, e.uid)
	})
	e.exec(t, `WITH r AS (INSERT INTO recurring_rules (id, user_id, name, type, amount, account_id, freq, start_date, status)
			VALUES (gen_random_uuid(), $1, 'Stored', 'expense', 10, $2, 'monthly', CURRENT_DATE + 3, 'active') RETURNING id)
		INSERT INTO recurring_items (id, user_id, rule_id, seq, slot, date, draft)
		SELECT gen_random_uuid(), $1, id, 1, CURRENT_DATE + 3, CURRENT_DATE + 3, '{"type":"expense","amount":10}' FROM r`, e.uid, e.acct)

	r := e.router(t, nil)
	status, body := apitest.Do(t, r, "GET", "/api/v1/dashboard", nil)
	apitest.Conform(t, "getDashboard", status, body)
	var d upcomingJSON
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Upcoming.Items) != 1 || d.Upcoming.Items[0].Name != "Stored" || d.Upcoming.Items[0].RuleID != nil || d.Upcoming.Items[0].Slot != nil {
		t.Errorf("upcoming = %+v, want the one stored item with no rule_id / slot", d.Upcoming.Items)
	}
}
