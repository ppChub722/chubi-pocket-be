package notifications

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type RecurringEndingPayload struct {
	RecurringID uuid.UUID    `json:"recurring_id"`
	Name        string       `json:"name"`
	Left        int          `json:"left"`
	StartDate   string       `json:"start_date"`
	End         RecurringEnd `json:"end"`
}

// recurringDueLink opens the recurring page scrolled to ต้องจัดการ.
const recurringDueLink = "/recurring?focus=due"

func (s *Service) RecurringDueTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, added int) error {
	if added <= 0 {
		return nil
	}
	d, err := s.DeliveryFor(ctx, tx, userID, nil, TypeRecurringDue)
	if err != nil || !d.Deliver {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE notifications
		SET payload = jsonb_set(payload, '{count}', to_jsonb(COALESCE((payload->>'count')::int, 0) + $2)),
		    deep_link = $3,
		    created_at = NOW()
		WHERE id = (SELECT id FROM notifications
			WHERE recipient_user_id = $1 AND type = 'recurring_due'
			  AND read_at IS NULL AND dismissed_at IS NULL AND actioned_at IS NULL
			ORDER BY created_at DESC LIMIT 1)`, userID, added, recurringDueLink)
	if err != nil {
		return fmt.Errorf("grouped recurring_due: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	payload, _ := json.Marshal(map[string]int{"count": added})
	link := recurringDueLink
	_, err = s.store.InsertTx(ctx, tx, userID, TypeRecurringDue, nil, payload, &link, false)
	return err
}

// RecurringAutoCreatedTx tells the user the auto-create recorded [count] tx
// for [date] ("YYYY-MM-DD"): ONE notice per user per date, whose count grows
// with each run (read or not, it stays one row).
func (s *Service) RecurringAutoCreatedTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, count int, date string) error {
	if count <= 0 {
		return nil
	}
	d, err := s.DeliveryFor(ctx, tx, userID, nil, TypeRecurringAutoCreated)
	if err != nil || !d.Deliver {
		return err
	}
	// Two due checks of the same user at once must not both insert the day's row.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('recurring_auto_created:' || $1::text || ':' || $2::text))`,
		userID, date); err != nil {
		return fmt.Errorf("lock recurring_auto_created: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE notifications
		SET payload = jsonb_set(payload, '{count}', to_jsonb(COALESCE((payload->>'count')::int, 0) + $3))
		WHERE recipient_user_id = $1 AND type = 'recurring_auto_created' AND payload->>'date' = $2`,
		userID, date, count)
	if err != nil {
		return fmt.Errorf("grouped recurring_auto_created: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	payload, _ := json.Marshal(RecurringAutoCreatedPayload{Count: count, Date: date})
	link := "/recurring"
	_, err = s.store.InsertTx(ctx, tx, userID, TypeRecurringAutoCreated, nil, payload, &link, false)
	return err
}

type RecurringAutoCreatedPayload struct {
	Count int    `json:"count"`
	Date  string `json:"date"`
}

func (s *Service) RecurringEndingTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, p RecurringEndingPayload) error {
	d, err := s.DeliveryFor(ctx, tx, userID, nil, TypeRecurringEnding)
	if err != nil || !d.Deliver {
		return err
	}
	payload, _ := json.Marshal(p)
	link := fmt.Sprintf("/recurring/%s", p.RecurringID)
	_, err = s.store.InsertTx(ctx, tx, userID, TypeRecurringEnding, nil, payload, &link, false)
	return err
}

type RecurringEnd struct {
	Kind  string  `json:"kind"`
	Date  *string `json:"date,omitempty"`
	Count *int    `json:"count,omitempty"`
}
