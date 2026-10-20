package recurring

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
	"github.com/ppChub722/chubi-pocket-be/internal/shared/recurrence"
)

// maxAutoDrafts caps one run per auto-save, so a long gap doesn't flood the
// pending list.
const maxAutoDrafts = 12

// produceAutoSaves makes a saving draft (🐷) for every occurrence of every
// auto-save from its start to today that has none yet, newest 12 per item.
func (s *Service) produceAutoSaves(ctx context.Context, tx pgx.Tx, userID uuid.UUID, today time.Time) error {
	rows, err := tx.Query(ctx, `SELECT a.id, a.goal_id, a.amount::float8, a.account_id, a.rule, a.start_date, a.end_rule, a.last_produced
		FROM saving_auto a
		LEFT JOIN saving_goals g ON g.id = a.goal_id
		WHERE a.user_id = $1 AND a.start_date <= $2 AND (a.last_produced IS NULL OR a.last_produced < $2)
		  AND (a.goal_id IS NULL OR g.status = 'active')
		FOR UPDATE OF a SKIP LOCKED`, userID, today)
	if err != nil {
		return fmt.Errorf("auto-saves: %w", err)
	}
	type due struct {
		id       uuid.UUID
		goal     *uuid.UUID
		amount   float64
		account  *uuid.UUID
		rule     []byte
		start    time.Time
		end      []byte
		produced *time.Time
	}
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.goal, &d.amount, &d.account, &d.rule, &d.start, &d.end, &d.produced); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range list {
		var rule recurrence.RecurrenceRule
		var end recurrence.RecurrenceEnd
		if json.Unmarshal(d.rule, &rule) != nil || json.Unmarshal(d.end, &end) != nil {
			continue
		}
		sp, err := recurrence.Build(rule, d.start.Format(dateLayout), &end, today)
		if err != nil {
			continue
		}
		var dates []time.Time
		for _, date := range sp.Within(today) {
			if d.produced == nil || date.After(*d.produced) {
				dates = append(dates, date)
			}
		}
		if len(dates) == 0 {
			continue
		}
		if len(dates) > maxAutoDrafts {
			dates = dates[len(dates)-maxAutoDrafts:]
		}
		for _, date := range dates {
			typ := typeSaving
			ds := date.Format(dateLayout)
			amount := d.amount
			draft := pending.Draft{Type: &typ, Amount: &amount, AccountID: d.account, Date: &ds, GoalID: d.goal}
			ref, _ := json.Marshal(map[string]any{"auto_save_id": d.id})
			if _, err := s.pending.Store().InsertTx(ctx, tx, userID, "auto_save", pending.KindSaving, draft, ref); err != nil {
				return fmt.Errorf("auto-save draft: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE saving_auto SET last_produced = $2 WHERE id = $1`, d.id, dates[len(dates)-1]); err != nil {
			return err
		}
	}
	return nil
}
