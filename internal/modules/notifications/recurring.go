package notifications

// Recurring notices (0.3.1-10). No job runner: they are written by the due
// check that runs inside the boot reads. ONE grouped recurring_due per user
// while it is unread (its count grows), and one recurring_ending per setting
// when 2 upcoming items are left.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// RecurringEndingPayload — payload of recurring_ending.
type RecurringEndingPayload struct {
	RecurringID uuid.UUID `json:"recurring_id"`
	Name        string    `json:"name"`
	Left        int       `json:"left"`
}

// WithDueCheck runs the recurring due check before the inbox / badge reads.
func (s *Service) WithDueCheck(fn shared.DueChecker) { s.due = fn }

// RunDueCheck — the hook, when wired.
func (s *Service) RunDueCheck(ctx context.Context, userID uuid.UUID, headerTZ string) error {
	if s.due == nil {
		return nil
	}
	return s.due(ctx, userID, headerTZ)
}

// RecurringDueTx adds `added` produced items to the user's unread grouped
// recurring_due notice, or opens a new one ("มีรายการประจำรอยืนยัน n รายการ").
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

// RecurringEndingTx — a setting has 2 or fewer upcoming items left.
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
