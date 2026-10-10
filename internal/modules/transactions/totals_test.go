package transactions

import (
	"context"
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// TestListTotals — totals cover the whole filtered set (not just the page),
// use the list's own filters, and leave transfers out of both sides.
func TestListTotals(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	cats := categories.NewService(categories.NewStore(pool))
	svc := NewService(NewStore(pool), cats)
	seed, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := cats.SeedForUser(ctx, seed, uid); err != nil {
		t.Fatalf("seed categories: %v", err)
	}
	if err := seed.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	a := testdb.Account(t, pool, uid)
	b := testdb.Account(t, pool, uid)
	food := testdb.Category(t, pool, uid, "Totals food")

	for _, req := range []CreateRequest{
		{Type: TypeIncome, AccountID: &a, Amount: 1000, Date: "2026-10-01", Description: testdb.Str("Salary")},
		{Type: TypeExpense, AccountID: &a, CategoryID: &food, Amount: 300, Date: "2026-10-02", Description: testdb.Str("Lunch")},
		{Type: TypeExpense, AccountID: &b, Amount: 200, Date: "2026-10-03"},
		{Type: TypeTransfer, AccountID: &a, TransferToAccountID: &b, Amount: 50, Date: "2026-10-04"},
		{Type: TypeExpense, Amount: 100, Date: "2026-09-30", Description: testdb.Str("Lunch cash")},
	} {
		if _, err := svc.Create(ctx, uid, req); err != nil {
			t.Fatalf("create %+v: %v", req, err)
		}
	}

	from := "2026-10-01"
	cases := []struct {
		name      string
		f         ListFilter
		want      Totals
		wantTotal int // pagination.total — transfers' two rows included
	}{
		{"all time, page 2 of 1-per-page", ListFilter{Page: 2, PerPage: 1}, Totals{1000, 600, 400, 4}, 6},
		{"one wallet", ListFilter{AccountID: &a}, Totals{1000, 300, 700, 2}, 3},
		{"no wallet", ListFilter{NoWallet: true}, Totals{0, 100, -100, 1}, 1},
		{"search description", ListFilter{Q: "lunch"}, Totals{0, 400, -400, 2}, 2},
		{"category", ListFilter{CategoryID: &food}, Totals{0, 300, -300, 1}, 1},
		{"from date", ListFilter{From: &from}, Totals{1000, 500, 500, 3}, 5},
		{"nothing matches", ListFilter{Q: "zzz-none"}, Totals{}, 0},
	}
	for _, tc := range cases {
		got, err := svc.List(ctx, uid, tc.f)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got.Totals != tc.want || got.Pagination.Total != tc.wantTotal {
			t.Errorf("%s: totals = %+v (pagination.total %d), want %+v (%d)",
				tc.name, got.Totals, got.Pagination.Total, tc.want, tc.wantTotal)
		}
	}
}
