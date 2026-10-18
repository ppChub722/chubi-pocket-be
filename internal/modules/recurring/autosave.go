package recurring

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
)

func (s *Service) produceAutoSaves(ctx context.Context, tx pgx.Tx, userID uuid.UUID, today time.Time) error {
	first := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	rows, err := tx.Query(ctx, `SELECT a.id, a.goal_id, a.amount::float8, a.day, a.account_id
		FROM saving_auto a
		LEFT JOIN saving_goals g ON g.id = a.goal_id
		WHERE a.user_id = $1 AND (a.last_month IS NULL OR a.last_month < $2)
		  AND (a.goal_id IS NULL OR g.status = 'active')
		FOR UPDATE OF a SKIP LOCKED`, userID, first)
	if err != nil {
		return fmt.Errorf("auto-saves: %w", err)
	}
	type due struct {
		id      uuid.UUID
		goal    *uuid.UUID
		amount  float64
		day     int
		account *uuid.UUID
	}
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.goal, &d.amount, &d.day, &d.account); err != nil {
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
		last := first.AddDate(0, 1, -1).Day()
		day := d.day
		if day > last {
			day = last
		}
		date := time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, time.UTC)
		if date.After(today) {
			continue
		}
		typ := typeSaving
		ds := date.Format(dateLayout)
		draft := pending.Draft{Type: &typ, Amount: &d.amount, AccountID: d.account, Date: &ds, GoalID: d.goal}
		ref, _ := json.Marshal(map[string]any{"auto_save_id": d.id})
		if _, err := s.pending.Store().InsertTx(ctx, tx, userID, "auto_save", pending.KindSaving, draft, ref); err != nil {
			return fmt.Errorf("auto-save draft: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE saving_auto SET last_month = $2 WHERE id = $1`, d.id, first); err != nil {
			return err
		}
	}
	return nil
}
