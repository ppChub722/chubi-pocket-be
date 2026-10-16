package categories

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// decorateMonth fills month_spent / month_count: this month's money in the
// category and its sub-categories (my share, the same rule as reports and
// budgets; income categories sum income) and how many transactions it was.
// Month = the user's timezone (header, else stored, else Asia/Bangkok).
func (s *Service) decorateMonth(ctx context.Context, userID uuid.UUID, cats []Category, tz string) error {
	if len(cats) == 0 {
		return nil
	}
	today, err := shared.UserToday(ctx, s.store.db, userID, tz)
	if err != nil {
		return err
	}
	first, last := shared.MonthBounds(today)
	rows, err := s.store.db.Query(ctx, `
		SELECT t.category_id, COUNT(*), COALESCE(SUM(`+shared.ShareAmountExpr("t")+`), 0)::float8
		FROM transactions t
		WHERE t.user_id = $1 AND `+shared.ReportScopePredicate("t", "$1")+`
		  AND t.type IN ('income', 'expense') AND t.category_id IS NOT NULL
		  AND t.date BETWEEN $2 AND $3
		GROUP BY t.category_id`, userID, first, last)
	if err != nil {
		return fmt.Errorf("category month: %w", err)
	}
	type agg struct {
		n   int
		sum float64
	}
	own := map[uuid.UUID]agg{}
	for rows.Next() {
		var (
			id uuid.UUID
			a  agg
		)
		if err := rows.Scan(&id, &a.n, &a.sum); err != nil {
			rows.Close()
			return err
		}
		own[id] = a
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	tree, err := s.store.TreeNames(ctx, userID)
	if err != nil {
		return err
	}
	total := map[uuid.UUID]agg{}
	for id, a := range own {
		for p, hops := &id, 0; p != nil && hops < 8; hops++ {
			t := total[*p]
			t.n += a.n
			t.sum += a.sum
			total[*p] = t
			node, ok := tree[*p]
			if !ok {
				break
			}
			p = node.ParentID
		}
	}
	for i := range cats {
		a := total[cats[i].ID]
		cats[i].MonthSpent, cats[i].MonthCount = round2(a.sum), a.n
	}
	return nil
}

func round2(v float64) float64 { return float64(int64(v*100+sign(v)*0.5)) / 100 }

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}
