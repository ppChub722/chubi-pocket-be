package transactions_test

import (
	"context"
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// TestDetailSplitsAndProject — split_count on list rows + detail, the
// debts themselves on detail only, and the embedded project ref.
func TestDetailSplitsAndProject(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	cats := categories.NewService(categories.NewStore(pool))
	txs := transactions.NewService(transactions.NewStore(pool), cats)
	debts := personal_debts.NewService(personal_debts.NewStore(pool), txs)
	txs.WithDebtsCreator(debts.CreateForTransactionTx)
	wallet := testdb.Account(t, pool, uid)

	created, err := txs.Create(ctx, uid, transactions.CreateRequest{
		Type: transactions.TypeExpense, AccountID: &wallet, Amount: 300, Date: "2026-10-10",
		Description: testdb.Str("Dinner"),
		Splits: []transactions.SplitInput{
			{PersonName: "Aom", OwedAmount: 100},
			{PersonName: "Beam", OwedAmount: 100},
		},
	})
	if err != nil {
		t.Fatalf("create split bill: %v", err)
	}
	split := created.(*transactions.TransactionDetail)
	if split.SplitCount != 2 {
		t.Errorf("create response split_count = %d, want 2", split.SplitCount)
	}
	// The create response is the full detail, splits included.
	if split.Splits == nil || len(*split.Splits) != 2 {
		t.Fatalf("create response splits = %v, want 2", split.Splits)
	}
	if s := (*split.Splits)[1]; s.PersonName != "Beam" || s.Amount != 100 || s.Direction != "owed_to_me" {
		t.Errorf("create response second split = %+v", s)
	}
	if split.AccountBalanceAfter == nil {
		t.Errorf("create response lost account_balance_after")
	}
	if split.Account == nil {
		t.Errorf("create response lost the account ref")
	}
	plain, err := txs.Create(ctx, uid, transactions.CreateRequest{
		Type: transactions.TypeExpense, AccountID: &wallet, Amount: 40, Date: "2026-10-10",
	})
	if err != nil {
		t.Fatalf("create plain: %v", err)
	}
	plainID := plain.(*transactions.TransactionDetail).ID

	// The plain row becomes a project row's personal copy (what a claim does).
	if _, err := pool.Exec(ctx, `
		WITH p AS (INSERT INTO projects (id, owner_user_id, name, status)
		           VALUES (gen_random_uuid(), $1, 'Japan Trip', 'active') RETURNING id),
		     m AS (INSERT INTO project_members (id, project_id, user_id, display_name, role, status)
		           SELECT gen_random_uuid(), p.id, $1, 'Me', 'owner', 'active' FROM p RETURNING id, project_id),
		     pt AS (INSERT INTO project_transactions (id, project_id, transaction_member_id, record_user_id, type, amount, currency, date)
		           SELECT gen_random_uuid(), m.project_id, m.id, $1, 'expense', 40, 'THB', '2026-10-10' FROM m RETURNING id, project_id)
		UPDATE transactions SET project_id = pt.project_id, source_project_transaction_id = pt.id
		FROM pt WHERE transactions.id = $2`, uid, plainID); err != nil {
		t.Fatalf("link project: %v", err)
	}

	d, err := txs.Get(ctx, uid, split.ID)
	if err != nil {
		t.Fatalf("get split bill: %v", err)
	}
	if d.SplitCount != 2 || !d.HasSplits || d.Splits == nil || len(*d.Splits) != 2 {
		t.Fatalf("detail: split_count=%d has_splits=%v splits=%v", d.SplitCount, d.HasSplits, d.Splits)
	}
	if s := (*d.Splits)[0]; s.PersonName != "Aom" || s.Amount != 100 || s.Status != "open" || s.Direction != "owed_to_me" {
		t.Errorf("first split = %+v", s)
	}
	if d.Project != nil {
		t.Errorf("split bill has no project, got %+v", d.Project)
	}

	p, err := txs.Get(ctx, uid, plainID)
	if err != nil {
		t.Fatalf("get plain: %v", err)
	}
	if p.SplitCount != 0 || p.Splits == nil || len(*p.Splits) != 0 {
		t.Errorf("plain detail: split_count=%d splits=%v, want 0 and []", p.SplitCount, p.Splits)
	}
	if p.Project == nil || p.Project.Name != "Japan Trip" {
		t.Errorf("plain detail project = %+v, want Japan Trip", p.Project)
	}

	list, err := txs.List(ctx, uid, transactions.ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, r := range list.Data {
		if r.Splits != nil {
			t.Errorf("list row %s carries splits", r.ID)
		}
		switch r.ID {
		case split.ID:
			if r.SplitCount != 2 || !r.HasSplits {
				t.Errorf("list split bill: split_count=%d has_splits=%v", r.SplitCount, r.HasSplits)
			}
		case plainID:
			if r.Project == nil || r.Project.Name != "Japan Trip" {
				t.Errorf("list plain project = %+v", r.Project)
			}
		}
	}
}

// TestWalletlessSplitCurrency — a wallet-less bill's split debts take the
// user's currency (they used to be stored with currency '').
func TestWalletlessSplitCurrency(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	if _, err := pool.Exec(ctx, `UPDATE users SET currency = 'USD' WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	cats := categories.NewService(categories.NewStore(pool))
	txs := transactions.NewService(transactions.NewStore(pool), cats)
	debts := personal_debts.NewService(personal_debts.NewStore(pool), txs)
	txs.WithDebtsCreator(debts.CreateForTransactionTx)

	created, err := txs.Create(ctx, uid, transactions.CreateRequest{
		Type: transactions.TypeExpense, Amount: 90, Date: "2026-10-10",
		Splits: []transactions.SplitInput{{PersonName: "Aom", OwedAmount: 30}},
	})
	if err != nil {
		t.Fatalf("create wallet-less split bill: %v", err)
	}
	var currency string
	if err := pool.QueryRow(ctx, `SELECT currency FROM personal_debts WHERE source_transaction_id = $1`,
		created.(*transactions.TransactionDetail).ID).Scan(&currency); err != nil {
		t.Fatal(err)
	}
	if currency != "USD" {
		t.Errorf("split debt currency = %q, want USD (the user's)", currency)
	}
}

// TestShareBasisReports — spec 12 §4.5: the wallet moves the cash amount,
// reports count my share (amount − what others owe on it).
func TestShareBasisReports(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	cats := categories.NewService(categories.NewStore(pool))
	txs := transactions.NewService(transactions.NewStore(pool), cats)
	debts := personal_debts.NewService(personal_debts.NewStore(pool), txs)
	txs.WithDebtsCreator(debts.CreateForTransactionTx)
	wallet := testdb.Account(t, pool, uid)

	created, err := txs.Create(ctx, uid, transactions.CreateRequest{
		Type: transactions.TypeExpense, AccountID: &wallet, Amount: 300, Date: "2026-10-10",
		Splits: []transactions.SplitInput{
			{PersonName: "Aom", OwedAmount: 100},
			{PersonName: "Beam", OwedAmount: 100},
		},
	})
	if err != nil {
		t.Fatalf("create split bill: %v", err)
	}
	if d := created.(*transactions.TransactionDetail); d.MyShare == nil || *d.MyShare != 100 {
		t.Errorf("create response my_share = %v, want 100", d.MyShare)
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
		if r.MyShare == nil {
			t.Errorf("list row %s has no my_share", r.ID)
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
