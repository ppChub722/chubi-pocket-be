package budgets

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// fakeExpected serves fixed occurrences the way recurring.Service serves a
// window, so the reader can be tested without the recurring module.
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

// withExpected swaps the fixture's router for one whose service reads e (the
// budgets are made first, through the router newBudFix built).
func (f *budFix) withExpected(e shared.Expecter) {
	h := NewHandler(NewService(NewStore(f.pool)).WithExpected(e))
	f.r = apitest.Engine(f.me, func(g *gin.RouterGroup) {
		g.GET("/budgets/:id/items", h.Items)
		g.GET("/budgets/:id", h.Get)
	})
}

// occ is a computed occurrence of rule on day n from today.
func (f *budFix) occ(rule uuid.UUID, n int, amount float64, cat, acct *uuid.UUID, tags ...uuid.UUID) shared.ExpectedOccurrence {
	d := f.today.AddDate(0, 0, n)
	return shared.ExpectedOccurrence{
		RuleID: rule, RuleName: "Rent", Name: "Rent ประจำเดือน", Slot: d, Date: d, Type: "expense",
		Amount: amount, AccountID: acct, CategoryID: cat, TagIDs: tags,
	}
}

func (f *budFix) budget(body map[string]any) string {
	f.t.Helper()
	var d Detail
	f.call("POST", "/budgets", "createBudget", body, http.StatusCreated, &d)
	return d.Budget.ID.String()
}

func (f *budFix) upcomingItems(id string) []Item {
	f.t.Helper()
	var il ItemList
	f.call("GET", "/budgets/"+id+"/items?source=upcoming", "listBudgetItems", nil, http.StatusOK, &il)
	return il.Data
}

func (f *budFix) expectedOf(id string) float64 {
	f.t.Helper()
	var d Detail
	f.call("GET", "/budgets/"+id, "getBudget", nil, http.StatusOK, &d)
	return d.Budget.Period.Expected
}

func TestBudgetReadsComputedOccurrences(t *testing.T) {
	f := newBudFix(t)
	id := f.budget(map[string]any{
		"name": "Food", "amount": 1000, "start_date": f.day(-4), "end": map[string]any{"kind": "date", "date": f.day(5)},
		"scope": map[string]any{"categories": []map[string]any{{"id": f.food}}},
	})
	rule := uuid.New()
	o := f.occ(rule, 2, 70, &f.food, &f.wallet)
	two, total := 3, 6
	o.Seq, o.Total = &two, &total
	f.withExpected(fakeExpected{occs: []shared.ExpectedOccurrence{o}})

	var d Detail
	f.call("GET", "/budgets/"+id, "getBudget", nil, http.StatusOK, &d)
	if p := d.Budget.Period; p.Used != 0 || p.Expected != 70 {
		t.Fatalf("used %v / expected %v, want 0 / 70", p.Used, p.Expected)
	}
	if s := src(d, SourceUpcoming); s == nil || s.Count != 1 || s.Amount != 70 {
		t.Errorf("upcoming source = %+v", s)
	}

	items := f.upcomingItems(id)
	if len(items) != 1 {
		t.Fatalf("upcoming items = %+v, want the one computed occurrence", items)
	}
	it := items[0]
	if it.Kind != "recurring_item" || it.Source != SourceUpcoming || it.CountsAs != "expected" || it.Amount != 70 {
		t.Errorf("item = %+v", it)
	}
	if it.RuleID == nil || *it.RuleID != rule || it.Slot == nil || *it.Slot != f.day(2) || it.Date == nil || *it.Date != f.day(2) {
		t.Errorf("rule / slot / date = %v / %v / %v, want %s / %s", it.RuleID, it.Slot, it.Date, rule, f.day(2))
	}
	if it.ID != uuid.NewSHA1(rule, []byte(f.day(2))) {
		t.Errorf("id = %s, want the id derived from the rule and the slot", it.ID)
	}
	l := it.Recurring
	if l == nil || l.ItemID != nil || l.Recurring == nil || l.Recurring.ID != rule || l.Recurring.Name != "Rent" ||
		l.Seq == nil || *l.Seq != 3 || l.Total == nil || *l.Total != 6 {
		t.Errorf("recurring link = %+v, want item_id null, the rule, 3 / 6", l)
	}
	if it.Description == nil || *it.Description != "Rent ประจำเดือน" {
		t.Errorf("description = %v, want the occurrence name", it.Description)
	}
}

func TestBudgetComputedOccurrencesFollowTheScope(t *testing.T) {
	f := newBudFix(t)
	trip := uuid.New()
	f.exec(`INSERT INTO tags (id, user_id, name) VALUES ($1, $2, 'Trip')`, trip, f.me)
	other := testdb.Account(t, f.pool, f.me)

	both := f.budget(map[string]any{
		"name": "Trip food", "amount": 1000, "start_date": f.day(-4), "end": map[string]any{"kind": "date", "date": f.day(5)},
		"scope": map[string]any{"categories": []map[string]any{{"id": f.food}}, "tags": []string{"Trip"}},
	})
	wallet := f.budget(map[string]any{
		"name": "Wallet", "amount": 1000, "start_date": f.day(-4), "end": map[string]any{"kind": "date", "date": f.day(5)},
		"scope": map[string]any{"wallet_ids": []uuid.UUID{f.wallet}},
	})

	rule := uuid.New()
	f.withExpected(fakeExpected{occs: []shared.ExpectedOccurrence{
		f.occ(rule, 1, 10, &f.food, nil, trip),   // food AND trip: counts in "Trip food"
		f.occ(rule, 2, 20, &f.food, nil),         // food only: out
		f.occ(rule, 3, 30, &f.travel, nil, trip), // trip only: out
		f.occ(rule, 4, 40, nil, &f.wallet),       // on the scope wallet: counts in "Wallet"
		f.occ(rule, 0, 50, nil, &other),          // on another wallet: out
	}})

	if got := f.expectedOf(both); got != 10 {
		t.Errorf("category AND tag budget expects %v, want 10 (only the row matching both)", got)
	}
	if items := f.upcomingItems(both); len(items) != 1 || items[0].Amount != 10 {
		t.Errorf("category AND tag items = %+v", items)
	}
	if got := f.expectedOf(wallet); got != 40 {
		t.Errorf("wallet budget expects %v, want 40 (only the scope wallet)", got)
	}
}

func TestBudgetStoredRowReplacesTheComputedOne(t *testing.T) {
	f := newBudFix(t)
	id := f.budget(map[string]any{
		"name": "Food", "amount": 1000, "start_date": f.day(-4), "end": map[string]any{"kind": "date", "date": f.day(5)},
		"scope": map[string]any{"categories": []map[string]any{{"id": f.food}}},
	})
	rule := uuid.New()
	// The Expecter returns the edited stored row INSTEAD of the computed one of
	// its slot: the budget counts it once, at its own amount and date.
	stored := uuid.New()
	edited := f.occ(rule, 2, 90, &f.food, nil)
	edited.ItemID = &stored
	edited.Date = f.today.AddDate(0, 0, 3) // moved a day later; the slot stays
	f.withExpected(fakeExpected{occs: []shared.ExpectedOccurrence{edited, f.occ(rule, 4, 70, &f.food, nil)}})

	if got := f.expectedOf(id); got != 160 {
		t.Fatalf("expected %v, want 160 (90 edited + 70 computed, the slot counted once)", got)
	}
	items := f.upcomingItems(id)
	if len(items) != 2 {
		t.Fatalf("items = %+v, want 2", items)
	}
	var row *Item
	for i := range items {
		if items[i].ID == stored {
			row = &items[i]
		}
	}
	if row == nil || row.Amount != 90 || row.Date == nil || *row.Date != f.day(3) || row.Slot == nil || *row.Slot != f.day(2) {
		t.Fatalf("stored row = %+v, want amount 90, date %s, slot %s", row, f.day(3), f.day(2))
	}
	if row.Recurring == nil || row.Recurring.ItemID == nil || *row.Recurring.ItemID != stored {
		t.Errorf("stored row link = %+v, want its item id", row.Recurring)
	}
}

func TestBudgetComputedOccurrencesNeedAKnownExpense(t *testing.T) {
	f := newBudFix(t)
	id := f.budget(map[string]any{
		"name": "All", "amount": 1000, "start_date": f.day(-4), "end": map[string]any{"kind": "date", "date": f.day(5)},
		"scope": map[string]any{"all": true},
	})
	rule := uuid.New()
	income := f.occ(rule, 1, 500, nil, nil)
	income.Type = "income"
	f.withExpected(fakeExpected{occs: []shared.ExpectedOccurrence{
		income,                   // not an expense
		f.occ(rule, 2, 0, nil, nil), // amount not known yet
		f.occ(rule, 3, 25, nil, nil),
	}})
	if got := f.expectedOf(id); got != 25 {
		t.Errorf("expected %v, want 25 (an income and an amount-less occurrence do not count)", got)
	}
}
