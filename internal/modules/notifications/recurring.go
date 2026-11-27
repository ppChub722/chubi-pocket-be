package notifications

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// recurringDueLink opens the recurring page scrolled to ต้องจัดการ.
const recurringDueLink = "/recurring?focus=due"

// RecurringDueTx — rounds came due on rules whose notify switch is on (the rule decides, so the
// type's mute is not read): one grouped notice whose count grows until it is read.
func (s *Service) RecurringDueTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, added int) error {
	if added <= 0 {
		return nil
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
