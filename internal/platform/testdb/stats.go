package testdb

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/database"
)

// StatsPool is Pool with the query tracer the real pool has, so a test can count a
// request's SQL with database.WithQueryStats (see apitest.DoCounted).
func StatsPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = defaultDSN
	}
	pool, err := database.New(dsn, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Skipf("database unreachable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var migrated bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables WHERE table_name = 'recurring_due_checks'
	)`).Scan(&migrated); err != nil || !migrated {
		pool.Close()
		t.Skip("recurring_due_checks missing: apply migration 000075 first")
	}
	t.Cleanup(pool.Close)
	return pool
}
