package saving_goals

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared/recurrence"
)

// The recurring "saving" settings (recurring_rules.type = 'saving') are a goal's plan:
// read here in SQL, since recurring imports this package.

// monthPlans — per goal with at least one active saving setting: Σ amount × its
// occurrences in the month that starts on monthStart.
func (s *Service) monthPlans(ctx context.Context, userID uuid.UUID, monthStart time.Time) (map[uuid.UUID]float64, error) {
	rows, err := s.store.db.Query(ctx, `SELECT goal_id, amount::float8, freq, interval_n, weekdays, monthly_mode,
		month_day, month_nth, month_weekday, dates, start_date, end_kind, end_date, end_count, total_count, generated_until
		FROM recurring_rules WHERE user_id = $1 AND type = 'saving' AND status = 'active' AND goal_id IS NOT NULL`, userID)
	if err != nil {
		return nil, fmt.Errorf("saving plans: %w", err)
	}
	defer rows.Close()
	monthEnd := monthStart.AddDate(0, 1, -1)
	out := map[uuid.UUID]float64{}
	for rows.Next() {
		var (
			goal                               uuid.UUID
			amount                             float64
			sp                                 recurrence.Spec
			interval                           int32
			weekdays                           []int32
			monthDay, monthNth, monthWD, count *int32
			total                              *int32
		)
		if err := rows.Scan(&goal, &amount, &sp.Freq, &interval, &weekdays, &sp.MonthlyMode, &monthDay, &monthNth, &monthWD,
			&sp.Dates, &sp.StartDate, &sp.EndKind, &sp.EndDate, &count, &total, &sp.GeneratedUntil); err != nil {
			return nil, err
		}
		sp.Interval = int(interval)
		for _, w := range weekdays {
			sp.Weekdays = append(sp.Weekdays, int(w))
		}
		sp.MonthDay, sp.MonthNth, sp.MonthWeekday, sp.EndCount, sp.TotalCount = intOf(monthDay), intOf(monthNth), intOf(monthWD), intOf(count), intOf(total)
		n := len(sp.Between(monthStart, monthEnd))
		out[goal] = round2(out[goal] + amount*float64(n))
	}
	return out, rows.Err()
}

func intOf(p *int32) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}

// pauseSavingRulesTx pauses the active saving settings of a goal that stops taking
// money (closed / archived), like a pause from /recurring; reopening never resumes them.
func pauseSavingRulesTx(ctx context.Context, tx pgx.Tx, userID, goalID uuid.UUID, today time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE recurring_rules SET status = 'paused', paused_from = $3
		WHERE user_id = $1 AND goal_id = $2 AND type = 'saving' AND status = 'active'`, userID, goalID, today)
	return err
}

// deleteSavingRulesTx deletes the saving settings of a goal (their occurrences go with them).
func deleteSavingRulesTx(ctx context.Context, tx pgx.Tx, userID, goalID uuid.UUID) error {
	_, err := tx.Exec(ctx, `DELETE FROM recurring_rules WHERE user_id = $1 AND goal_id = $2 AND type = 'saving'`, userID, goalID)
	return err
}
