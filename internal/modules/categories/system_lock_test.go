package categories

import (
	"context"
	"errors"
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// 0.4.1-1 D5 / D6: new users get the four person-book kinds; a system category keeps its name and
// include_in_report (the same values may be sent back), its icon stays editable.
func TestSystemCategoryLock(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	seed, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewStore(pool).SeedForUserTx(ctx, seed, uid); err != nil {
		t.Fatal(err)
	}
	if err := seed.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	svc := NewService(NewStore(pool))
	for _, k := range []SystemKind{SystemLoanGiven, SystemLoanTaken, SystemDebtPaid, SystemDebtReceived} {
		if c, err := svc.SystemFor(ctx, uid, k); err != nil || c.IncludeInReport {
			t.Fatalf("system %s = %+v (%v), want seeded, out of reports", k, c, err)
		}
	}
	loan, _ := svc.SystemFor(ctx, uid, SystemLoanGiven)

	rename, on := "My loans", true
	if _, err := svc.Update(ctx, uid, loan.ID, UpdateCategoryRequest{Name: &rename}); !errors.Is(err, ErrSystemImmutable) {
		t.Errorf("rename: err = %v, want immutable", err)
	}
	if _, err := svc.Update(ctx, uid, loan.ID, UpdateCategoryRequest{IncludeInReport: &on}); !errors.Is(err, ErrSystemImmutable) {
		t.Errorf("include_in_report: err = %v, want immutable", err)
	}
	same, off, icon := loan.Name, false, "savings"
	if _, err := svc.Update(ctx, uid, loan.ID, UpdateCategoryRequest{Name: &same, IncludeInReport: &off,
		IconCode: &shared.IconCode{Icon: &icon}, iconCodePresent: true}); err != nil {
		t.Errorf("same values + a new icon: %v", err)
	}
}
