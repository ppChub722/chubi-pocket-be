package me

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/budgets"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
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

func TestMyCountsRecurringFromComputedOccurrences(t *testing.T) {
	pool := testdb.Pool(t)
	uid := testdb.User(t, pool)
	loc, _ := time.LoadLocation("Asia/Bangkok")
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	occ := func(n int, amount float64, due bool) shared.ExpectedOccurrence {
		d := today.AddDate(0, 0, n)
		return shared.ExpectedOccurrence{RuleID: uuid.New(), RuleName: "Rent", Slot: d, Date: d, Type: "expense", Amount: amount, Due: due}
	}
	svc := NewService(pool).
		WithBudgets(budgets.NewService(budgets.NewStore(pool))).
		WithExpected(fakeExpected{occs: []shared.ExpectedOccurrence{
			occ(-2, 100, true), // overdue: due
			occ(0, 0, true),    // due today, no amount yet: due + needs an amount
			occ(5, 50, false),  // in the next 14 days: upcoming
			occ(40, 0, false),  // far off, no amount, not this month: counts nowhere
		}})
	h := NewHandler(svc)
	r := apitest.Engine(uid, func(g *gin.RouterGroup) { g.GET("/me/counts", h.Counts) })

	status, raw := apitest.Do(t, r, "GET", "/api/v1/me/counts", nil)
	apitest.Conform(t, "getMyCounts", status, raw)
	var got MeCounts
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	c := got.Counts.Recurring
	if c.DueCount != 2 || c.UpcomingCount != 1 || c.NeedsAmountCount != 1 {
		t.Errorf("recurring = %+v, want due 2 / upcoming 1 / needs_amount 1", c)
	}
}
