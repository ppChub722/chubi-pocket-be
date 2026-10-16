package transactions

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// DailyPoint — one day of the list's daily series (spec TransactionDailyPoint).
type DailyPoint struct {
	Date    string  `json:"date"`
	Income  float64 `json:"income"`
	Expense float64 `json:"expense"`
}

const maxDailyDays = 366

// listRange — the days the summary numbers and the daily series cover: the
// list's from / to, else the current month (user timezone).
func (s *Service) listRange(ctx context.Context, userID uuid.UUID, f ListFilter) (from, to, today time.Time, err error) {
	if today, err = shared.UserToday(ctx, s.store.db, userID, f.TZ); err != nil {
		return
	}
	first, last := shared.MonthBounds(today)
	from, to = first, last
	if f.From != nil {
		if t, e := time.Parse("2006-01-02", *f.From); e == nil {
			from = t
		}
	}
	if f.To != nil {
		if t, e := time.Parse("2006-01-02", *f.To); e == nil {
			to = t
		}
	} else if f.From != nil {
		_, to = shared.MonthBounds(from)
	}
	if to.Before(from) {
		to = from
	}
	return
}

// dailySeries — income / expense (my share) per day over [from, to] for the
// rows `where` selects, zero-filled; empty when the range is over a year.
func (s *Store) dailySeries(ctx context.Context, where string, args []any, from, to time.Time) ([]DailyPoint, error) {
	out := []DailyPoint{}
	if int(to.Sub(from).Hours()/24)+1 > maxDailyDays {
		return out, nil
	}
	share := shared.ShareAmountExpr("t")
	a := append(append([]any{}, args...), from, to)
	rows, err := s.db.Query(ctx, `SELECT t.date,
			COALESCE(SUM(`+share+`) FILTER (WHERE t.type = 'income'), 0)::float8,
			COALESCE(SUM(`+share+`) FILTER (WHERE t.type = 'expense'), 0)::float8
		FROM transactions t
		WHERE `+where+fmt.Sprintf(` AND t.date BETWEEN $%d AND $%d`, len(a)-1, len(a))+`
		GROUP BY t.date`, a...)
	if err != nil {
		return nil, fmt.Errorf("daily series: %w", err)
	}
	defer rows.Close()
	by := map[string]DailyPoint{}
	for rows.Next() {
		var (
			d    time.Time
			p    DailyPoint
		)
		if err := rows.Scan(&d, &p.Income, &p.Expense); err != nil {
			return nil, err
		}
		p.Date = d.Format("2006-01-02")
		p.Income, p.Expense = round2(p.Income), round2(p.Expense)
		by[p.Date] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		k := d.Format("2006-01-02")
		p, ok := by[k]
		if !ok {
			p = DailyPoint{Date: k}
		}
		out = append(out, p)
	}
	return out, nil
}

// avgDailySpend — expense over the days of the range that have happened
// (up to today).
func avgDailySpend(series []DailyPoint, from, to, today time.Time) float64 {
	end := to
	if today.Before(end) {
		end = today
	}
	days := int(end.Sub(from).Hours()/24) + 1
	if days < 1 {
		days = 1
	}
	var sum float64
	for _, p := range series {
		sum += p.Expense
	}
	return math.Round(sum/float64(days)*100) / 100
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
