package saving_goals

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

func (f *goalFix) wallet(id uuid.UUID) shared.WalletSavings {
	f.t.Helper()
	m, err := shared.LoadWalletSavings(f.ctx, f.pool, f.me, []uuid.UUID{id})
	if err != nil {
		f.t.Fatal(err)
	}
	return m[id]
}

func (f *goalFix) bookedSpare(wallet uuid.UUID) float64 {
	f.t.Helper()
	var n float64
	if err := f.pool.QueryRow(f.ctx, `SELECT COALESCE(SUM(CASE WHEN to_bucket = 'spare' THEN amount
		WHEN from_bucket = 'spare' THEN -amount ELSE 0 END), 0)::float8 FROM saving_entries WHERE user_id = $1 AND account_id = $2`,
		f.me, wallet).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// A savings wallet counts its whole balance as saved: what the goals don't hold is the
// unallocated saving, derived from the balance and never booked.
func TestSavingsWalletDerivesUnallocated(t *testing.T) {
	f := newGoalFix(t)
	car := f.goal("Car", 300000, nil)
	f.save(f.kbank, &car, 3000)
	f.exec(`UPDATE accounts SET auto_save = TRUE WHERE id = $1`, f.kbank)

	w := f.wallet(f.kbank)
	if w.Saved != 190000 || w.Spare != 187000 || w.Available == nil || *w.Available != 0 || w.SavedExceedsBalance {
		t.Fatalf("savings wallet = %+v", w)
	}
	if len(w.Buckets) != 2 {
		t.Errorf("buckets = %+v, want the goal and the derived spare", w.Buckets)
	}

	var l GoalList
	f.call("GET", "/saving-goals", "listSavingGoals", nil, http.StatusOK, &l)
	if l.Summary.Saved != 190000 || l.Summary.InGoals != 3000 || l.Summary.Spare != 187000 || l.Spare.Amount != 187000 {
		t.Fatalf("totals = %+v / spare %+v", l.Summary, l.Spare)
	}

	// Money out: the unallocated part falls first, the goal is untouched.
	f.exec(`UPDATE accounts SET balance = 50000 WHERE id = $1`, f.kbank)
	if w = f.wallet(f.kbank); w.Saved != 50000 || w.Spare != 47000 || *w.Available != 0 {
		t.Fatalf("after money out = %+v", w)
	}
	// Out past the unallocated part: the goal is not cut, the wallet shows the red over-saved state.
	f.exec(`UPDATE accounts SET balance = 2000 WHERE id = $1`, f.kbank)
	w = f.wallet(f.kbank)
	if w.Saved != 3000 || w.Spare != 0 || !w.SavedExceedsBalance || w.OverBy != 1000 || *w.Available != -1000 {
		t.Fatalf("over-saved = %+v", w)
	}
	f.exec(`UPDATE accounts SET balance = 190000 WHERE id = $1`, f.kbank)

	// Nothing unallocated can be booked on it.
	raw := f.call("POST", "/saving-entries", "", map[string]any{"kind": "save", "amount": 10, "wallet_id": f.kbank}, http.StatusConflict, nil)
	if errCode(raw) != "INVALID_TRANSITION" {
		t.Errorf("spare save on a savings wallet = %s", raw)
	}

	// Allocate to a goal: a save from the wallet. Take back: a withdrawal. No spare entry is ever booked.
	var res WriteResult
	f.call("POST", "/saving-entries", "createSavingEntry", map[string]any{
		"kind": "move", "amount": 500, "goal_id": nil, "to_goal_id": car,
	}, http.StatusCreated, &res)
	if len(res.Entries) != 1 || res.Entries[0].Kind != "save" {
		t.Fatalf("allocate = %+v", res.Entries)
	}
	f.call("POST", "/saving-entries", "createSavingEntry", map[string]any{
		"kind": "move", "amount": 200, "goal_id": car, "to_goal_id": nil,
	}, http.StatusCreated, &res)
	if len(res.Entries) != 1 || res.Entries[0].Kind != "withdraw" {
		t.Fatalf("take back = %+v", res.Entries)
	}
	if w = f.wallet(f.kbank); w.Saved != 190000 || w.Spare != 186700 || w.Buckets[0].Amount != 3300 {
		t.Fatalf("after allocate + take back = %+v", w)
	}
	if n := f.bookedSpare(f.kbank); n != 0 {
		t.Errorf("booked spare = %v, want 0", n)
	}

	// Closing a goal to the spare pool is a withdrawal there too.
	f.call("POST", "/saving-goals/"+car.String()+"/close", "closeSavingGoal", map[string]any{"to": "spare"}, http.StatusOK, nil)
	if n := f.bookedSpare(f.kbank); n != 0 {
		t.Errorf("booked spare after close = %v, want 0", n)
	}
	if w = f.wallet(f.kbank); w.Saved != 190000 || w.Spare != 190000 {
		t.Fatalf("after close = %+v", w)
	}
}

// A card or pay later owes: its negative balance is not "saved more than the wallet holds".
func TestCreditWalletHasNoSavingsAlert(t *testing.T) {
	f := newGoalFix(t)
	f.exec(`UPDATE accounts SET type = 'credit_card', credit_limit = 80000, balance = -23248.14 WHERE id = $1`, f.scb)
	w := f.wallet(f.scb)
	if w.SavedExceedsBalance || w.OverBy != 0 || w.Available != nil {
		t.Fatalf("credit wallet = %+v", w)
	}
}
