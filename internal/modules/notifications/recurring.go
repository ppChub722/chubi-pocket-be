package notifications

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type RecurringEndingPayload struct {
	RecurringID uuid.UUID    `json:"recurring_id"`
	Name        string       `json:"name"`
	Left        int          `json:"left"`
	StartDate   string       `json:"start_date"`
	End         RecurringEnd `json:"end"`
}

func (s *Service) WithDueCheck(fn shared.DueChecker) { s.due = fn }

func (s *Service) RunDueCheck(ctx context.Context, userID uuid.UUID, headerTZ string) error {
	if s.due == nil {
		return nil
	}
	return s.due(ctx, userID, headerTZ)
}

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
		    created_at = NOW()
		WHERE id = (SELECT id FROM notifications
			WHERE recipient_user_id = $1 AND type = 'recurring_due'
			  AND read_at IS NULL AND dismissed_at IS NULL AND actioned_at IS NULL
			ORDER BY created_at DESC LIMIT 1)`, userID, added)
	if err != nil {
		return fmt.Errorf("grouped recurring_due: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	payload, _ := json.Marshal(map[string]int{"count": added})
	link := "/pending"
	_, err = s.store.InsertTx(ctx, tx, userID, TypeRecurringDue, nil, payload, &link, false)
	return err
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
