package saving_goals

import (
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
)

// The goals pages read the saving balances once (not once per question) and the same
// number of queries however many goals there are (the budget excludes the auth
// middleware's one user lookup).
func TestSavingGoalsQueryBudget(t *testing.T) {
	f := newGoalFix(t)
	var first = f.goal("Trip", 30000, nil)
	f.goal("Car", 90000, nil)
	f.goal("Laptop", 45000, nil)
	f.save(f.kbank, &first, 5000)
	f.save(f.scb, nil, 1200)

	for _, c := range []struct {
		name, path string
		max        int64
	}{
		{"list", "/saving-goals?status=active&sort=created", 8},
		{"goal", "/saving-goals/" + first.String(), 9},
		{"spare", "/saving-goals/spare", 7},
	} {
		status, raw, n := apitest.DoCounted(t, f.r, "GET", "/api/v1"+c.path, nil)
		if status != 200 {
			t.Fatalf("%s: %d %s", c.name, status, raw)
		}
		t.Logf("%s: %d queries", c.name, n)
		if n > c.max {
			t.Errorf("%s ran %d queries, budget %d", c.name, n, c.max)
		}
	}
}
