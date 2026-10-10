// Package testdb gives store tests the local dev Postgres
// (TEST_DATABASE_URL, default the compose db on :5433) and throwaway
// users. Tests SKIP when the database is unreachable or not migrated.
package testdb

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultDSN = "postgres://chubadmin:admin1234@localhost:5433/chubi_pocket_db?sslmode=disable"

// Pool connects and checks migration 000051 (name / description / note).
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = defaultDSN
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
	var migrated bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_name = 'transactions' AND column_name = 'description'
	)`).Scan(&migrated); err != nil || !migrated {
		pool.Close()
		t.Skip("transactions.description missing — apply migration 000051 first")
	}
	t.Cleanup(pool.Close)
	return pool
}

// User inserts a user and removes it (and its rows) when the test ends.
func User(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	uniq := "tdb_" + id.String()
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, username, email, display_name, password_hash, currency)
		VALUES ($1, $2, $3, 'Test user', 'x', 'THB')`, id, uniq, uniq+"@test.local"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM transactions WHERE user_id = $1`,
			`DELETE FROM personal_debts WHERE user_id = $1`,
			`DELETE FROM scheduled_transactions WHERE user_id = $1`,
			`DELETE FROM saving_goals WHERE user_id = $1`,
			`DELETE FROM budgets WHERE user_id = $1`,
			`DELETE FROM projects WHERE owner_user_id = $1`,
			`DELETE FROM account_members WHERE user_id = $1`,
			`DELETE FROM accounts WHERE user_id = $1`,
			`DELETE FROM notifications WHERE recipient_user_id = $1 OR actor_user_id = $1`,
			`DELETE FROM contacts WHERE user_id = $1`,
			`DELETE FROM tags WHERE user_id = $1`,
			`DELETE FROM user_notification_settings WHERE user_id = $1`,
			`DELETE FROM categories WHERE user_id = $1 AND parent_id IS NOT NULL`,
			`DELETE FROM categories WHERE user_id = $1`,
			`DELETE FROM users WHERE id = $1`,
		} {
			if _, err := pool.Exec(ctx, q, id); err != nil {
				t.Errorf("cleanup %q: %v", q, err)
			}
		}
	})
	return id
}

// Account inserts an active THB bank wallet owned by userID.
func Account(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) uuid.UUID {
	t.Helper()
	id, _ := uuid.NewV7()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO accounts (id, user_id, name, type, balance, currency, status)
		VALUES ($1, $2, 'Test wallet', 'bank', 0, 'THB', 'active')`, id, userID); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	mid, _ := uuid.NewV7()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO account_members (id, account_id, user_id, role, report_scope, joined_at)
		VALUES ($1, $2, $3, 'owner', 'all', NOW())`, mid, id, userID); err != nil {
		t.Fatalf("insert owner member: %v", err)
	}
	return id
}

// Category inserts a user expense category.
func Category(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id, _ := uuid.NewV7()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO categories (id, user_id, name, type, is_system)
		VALUES ($1, $2, $3, 'expense', FALSE)`, id, userID, name); err != nil {
		t.Fatalf("insert category: %v", err)
	}
	return id
}

// Str returns a pointer to s.
func Str(s string) *string { return &s }

// Text dereferences a nullable column for messages ("<nil>" when NULL).
func Text(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// CheckText fails when description / note differ from want ("" = NULL).
func CheckText(t *testing.T, step string, gotD, gotN *string, wantD, wantN string) {
	t.Helper()
	if Text(gotD) != orNil(wantD) || Text(gotN) != orNil(wantN) {
		t.Errorf("%s: description=%q note=%q, want %q / %q",
			step, Text(gotD), Text(gotN), orNil(wantD), orNil(wantN))
	}
}

func orNil(s string) string {
	if s == "" {
		return "<nil>"
	}
	return s
}

// Steps is the shared update script every module runs: a body without
// the keys keeps both, null clears, "" clears, a value is trimmed.
var Steps = []struct {
	Body         string
	WantD, WantN string
}{
	{`{}`, "D1", "N1"},
	{`{"description":null}`, "", "N1"},
	{`{"description":"  D2  "}`, "D2", "N1"},
	{`{"note":""}`, "D2", ""},
	{`{"note":"N2"}`, "D2", "N2"},
}

// Float returns a pointer to f.
func Float(f float64) *float64 { return &f }
