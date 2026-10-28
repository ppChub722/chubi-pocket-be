package personal_debts

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

const sdTestDSN = "postgres://chubadmin:admin1234@localhost:5432/chubi_pocket_db?sslmode=disable"

func sdPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = sdTestDSN
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("cannot build pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("database unreachable (start docker compose db): %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func sdV7(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	return id
}

type sdFixture struct {
	pool              *pgxpool.Pool
	svc               *Service
	splitter, partner uuid.UUID
	splitterContact   uuid.UUID // splitter's contact for partner (linked)
	partnerContact    uuid.UUID // partner's contact back to splitter
	account           uuid.UUID
}

func newSDFixture(t *testing.T) *sdFixture {
	t.Helper()
	pool := sdPool(t)
	ctx := context.Background()
	f := &sdFixture{pool: pool, svc: NewService(NewStore(pool), nil)}

	newUser := func(name string) uuid.UUID {
		id := sdV7(t)
		uniq := fmt.Sprintf("sdt_%s", id)
		if _, err := pool.Exec(ctx, `
			INSERT INTO users (id, username, email, display_name, password_hash, currency)
			VALUES ($1, $2, $3, $4, 'x', 'THB')`,
			id, uniq, uniq+"@test.local", name); err != nil {
			t.Fatalf("insert user: %v", err)
		}
		return id
	}
	f.splitter = newUser("SD Splitter")
	f.partner = newUser("SD Partner")

	newContact := func(owner, linked uuid.UUID, name string) uuid.UUID {
		id := sdV7(t)
		if _, err := pool.Exec(ctx, `
			INSERT INTO contacts (id, user_id, display_name, linked_user_id, status)
			VALUES ($1, $2, $3, $4, 'active')`,
			id, owner, name, linked); err != nil {
			t.Fatalf("insert contact: %v", err)
		}
		return id
	}
	f.splitterContact = newContact(f.splitter, f.partner, "SD Partner")
	f.partnerContact = newContact(f.partner, f.splitter, "SD Splitter")

	f.account = sdV7(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO accounts (id, user_id, name, type, balance, currency, status)
		VALUES ($1, $2, 'sd test', 'bank', 0, 'THB', 'active')`,
		f.account, f.splitter); err != nil {
		t.Fatalf("insert account: %v", err)
	}

	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = pool.Exec(cctx, `DELETE FROM personal_debts WHERE user_id = ANY($1)`,
			[]uuid.UUID{f.splitter, f.partner})
		_, _ = pool.Exec(cctx, `DELETE FROM transactions WHERE user_id = $1`, f.splitter)
		_, _ = pool.Exec(cctx, `DELETE FROM account_members WHERE account_id = $1`, f.account)
		_, _ = pool.Exec(cctx, `DELETE FROM accounts WHERE id = $1`, f.account)
		_, _ = pool.Exec(cctx, `DELETE FROM contacts WHERE id = ANY($1)`,
			[]uuid.UUID{f.splitterContact, f.partnerContact})
		_, _ = pool.Exec(cctx, `DELETE FROM users WHERE id = ANY($1)`,
			[]uuid.UUID{f.splitter, f.partner})
	})
	return f
}

func (f *sdFixture) newBill(t *testing.T, txType string, amount float64) uuid.UUID {
	t.Helper()
	id := sdV7(t)
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO transactions (id, user_id, account_id, type, amount, date)
		VALUES ($1, $2, $3, $4, $5, CURRENT_DATE)`,
		id, f.splitter, f.account, txType, amount); err != nil {
		t.Fatalf("insert %s bill: %v", txType, err)
	}
	return id
}

func (f *sdFixture) runCreator(t *testing.T, billID uuid.UUID, parentType string) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	err = f.svc.CreateForTransactionTx(ctx, tx, billID, f.splitter, parentType, "THB", nil,
		[]transactions.SplitInput{
			{PersonName: "SD Partner", ContactID: &f.splitterContact, OwedAmount: 400},
			{PersonName: "SD Freetext", OwedAmount: 100},
		})
	if err != nil {
		t.Fatalf("CreateForTransactionTx(%s): %v", parentType, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func (f *sdFixture) directions(t *testing.T, userID uuid.UUID, source *uuid.UUID) []string {
	t.Helper()
	// $2::uuid IS NULL matches every row of the user; otherwise filter by the source bill.
	rows, err := f.pool.Query(context.Background(), `
		SELECT direction FROM personal_debts
		WHERE user_id = $1
		  AND ($2::uuid IS NULL OR source_transaction_id = $2::uuid)
		ORDER BY created_at`, userID, source)
	if err != nil {
		t.Fatalf("query directions: %v", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func TestSplitDirectionsByParentType(t *testing.T) {
	f := newSDFixture(t)

	expense := f.newBill(t, "expense", 900)
	f.runCreator(t, expense, "expense")
	if ds := f.directions(t, f.splitter, &expense); len(ds) != 2 || ds[0] != DirectionPaid || ds[1] != DirectionPaid {
		t.Fatalf("expense splitter debt directions = %v, want [paid paid]", ds)
	}

	income := f.newBill(t, "income", 1000)
	f.runCreator(t, income, "income")
	if ds := f.directions(t, f.splitter, &income); len(ds) != 2 || ds[0] != DirectionReceived || ds[1] != DirectionReceived {
		t.Fatalf("income splitter debt directions = %v, want [received received]", ds)
	}
	// 0.4.1-2 S16: a split with a linked contact makes no copy in the partner's book.
	if ds := f.directions(t, f.partner, nil); len(ds) != 0 {
		t.Fatalf("partner's book = %v, want no rows (no mirror)", ds)
	}
}
