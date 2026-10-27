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

// planRule is one active saving setting into a goal.
type planRule struct {
	goal   uuid.UUID
	amount float64
	spec   recurrence.Spec
}

// plansBetween — per goal with at least one active saving setting: Σ amount × its
// occurrences from from to to (both days included).
func plansBetween(rules []planRule, from, to time.Time) map[uuid.UUID]float64 {
	out := map[uuid.UUID]float64{}
	for _, r := range rules {
		n := len(r.spec.Between(from, to))
		out[r.goal] = round2(out[r.goal] + r.amount*float64(n))
	}
	return out
}

// planRules reads the user's active saving settings into a goal, once per page.
func (s *Service) planRules(ctx context.Context, userID uuid.UUID) ([]planRule, error) {
	rows, err := s.store.db.Query(ctx, `SELECT goal_id, amount::float8, freq, interval_n, weekdays, monthly_mode,
		month_day, month_nth, month_weekday, dates, start_date, end_kind, end_date, end_count, total_count, generated_until, gaps
		FROM recurring_rules WHERE user_id = $1 AND type = 'saving' AND status = 'active' AND goal_id IS NOT NULL`, userID)
	if err != nil {
		return nil, fmt.Errorf("saving plans: %w", err)
	}
	defer rows.Close()
	var out []planRule
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
			&sp.Dates, &sp.StartDate, &sp.EndKind, &sp.EndDate, &count, &total, &sp.GeneratedUntil, &sp.Gaps); err != nil {
			return nil, err
		}
		sp.Interval = int(interval)
		for _, w := range weekdays {
			sp.Weekdays = append(sp.Weekdays, int(w))
		}
		sp.MonthDay, sp.MonthNth, sp.MonthWeekday, sp.EndCount, sp.TotalCount = intOf(monthDay), intOf(monthNth), intOf(monthWD), intOf(count), intOf(total)
		out = append(out, planRule{goal: goal, amount: amount, spec: sp})
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
	_, err := tx.Exec(ctx, `UPDATE recurring_rules SET status = 'paused', paused_from = $3, pause_reason = 'goal_inactive'
		WHERE user_id = $1 AND goal_id = $2 AND type = 'saving' AND status = 'active'`, userID, goalID, today)
	return err
}

// deleteSavingRulesTx deletes the saving settings of a goal (their occurrences go with them).
func deleteSavingRulesTx(ctx context.Context, tx pgx.Tx, userID, goalID uuid.UUID) error {
	_, err := tx.Exec(ctx, `DELETE FROM recurring_rules WHERE user_id = $1 AND goal_id = $2 AND type = 'saving'`, userID, goalID)
	return err
}
