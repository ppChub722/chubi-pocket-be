package transactions_test

import (
	"context"
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// Reports and the list totals count my share (a claimed paper I paid: amount = the bill, my_share =
// my part); the wallet moves the whole amount.
func TestShareBasisReports(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	cats := categories.NewService(categories.NewStore(pool))
	txs := transactions.NewService(transactions.NewStore(pool), cats)
	wallet := testdb.Account(t, pool, uid)

	created, err := txs.Create(ctx, uid, transactions.CreateRequest{
		Type: transactions.TypeExpense, AccountID: &wallet, Amount: 300, Date: "2026-10-10",
	})
	if err != nil {
		t.Fatalf("create bill: %v", err)
	}
	bill := created.(*transactions.TransactionDetail)
	if _, err := pool.Exec(ctx, `UPDATE transactions SET my_share = 100 WHERE id = $1`, bill.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := txs.Create(ctx, uid, transactions.CreateRequest{
		Type: transactions.TypeExpense, AccountID: &wallet, Amount: 40, Date: "2026-10-11",
	}); err != nil {
		t.Fatalf("create plain: %v", err)
	}

	sum, err := txs.Summary(ctx, uid, transactions.SummaryRequest{From: "2026-10-01", To: "2026-10-31"})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.TotalExpense != 140 {
		t.Errorf("summary expense = %v, want 140 (100 share + 40)", sum.TotalExpense)
	}
	list, err := txs.List(ctx, uid, transactions.ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list.Totals.Expense != 140 {
		t.Errorf("list totals expense = %v, want 140", list.Totals.Expense)
	}
	for _, r := range list.Data {
		if r.ID == bill.ID && (r.MyShare == nil || *r.MyShare != 100) {
			t.Errorf("bill my_share = %v, want 100", r.MyShare)
		}
	}
	var balance float64
	if err := pool.QueryRow(ctx, `SELECT balance FROM accounts WHERE id = $1`, wallet).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != -340 {
		t.Errorf("wallet balance = %v, want -340 (cash moves in full)", balance)
	}
}
