package accounts

import (
	"context"
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// TestSystemRowsHaveNoCannedText — opening-balance and adjust-balance rows
// store no English default; the app labels them by their system category
// (owner 2026-10-10). What the user types is kept.
func TestSystemRowsHaveNoCannedText(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	cats := categories.NewService(categories.NewStore(pool))
	svc := NewService(NewStore(pool), transactions.NewService(transactions.NewStore(pool), cats), cats)
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

	acc, err := svc.Create(ctx, uid, "THB", CreateRequest{Name: "Opening", Type: "bank", Balance: 500})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	read := func(step, where string, arg any) {
		t.Helper()
		var d, n *string
		if err := pool.QueryRow(ctx, `SELECT description, note FROM transactions WHERE `+where+
			` ORDER BY created_at DESC LIMIT 1`, arg).Scan(&d, &n); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		testdb.CheckText(t, step, d, n, "", "")
	}
	read("opening balance", "account_id = $1", acc.ID)

	if _, err := svc.AdjustBalance(ctx, uid, acc.ID, AdjustBalanceRequest{NewBalance: testdb.Float(450)}); err != nil {
		t.Fatalf("adjust: %v", err)
	}
	read("adjust, nothing typed", "account_id = $1", acc.ID)

	if _, err := svc.AdjustBalance(ctx, uid, acc.ID, AdjustBalanceRequest{
		NewBalance: testdb.Float(400), Description: testdb.Str(" Cash count "), Note: testdb.Str("N1"),
	}); err != nil {
		t.Fatalf("adjust typed: %v", err)
	}
	var d, n *string
	if err := pool.QueryRow(ctx, `SELECT description, note FROM transactions WHERE account_id = $1
		ORDER BY created_at DESC LIMIT 1`, acc.ID).Scan(&d, &n); err != nil {
		t.Fatalf("read typed: %v", err)
	}
	testdb.CheckText(t, "adjust, typed", d, n, "Cash count", "N1")
}
