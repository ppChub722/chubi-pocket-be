package personal_debts

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
)

// ErrDebtNotLinked — the entry has no copy in another user's book.
var ErrDebtNotLinked = errors.New("this debt has no copy in another user's book")

// Disconnect — POST /personal-debts/{id}/disconnect (0.4.1-1 D9): either side breaks the link
// between the two books. Both entries stay (same amount) as plain debts with that person: the
// counterpart link goes on both sides, a partner mirror becomes my own paid-by debt on my expense of
// it (or a manual debt without one), and the other user is told. My own transaction ⇄ entry link
// stays (a split stays a split of my bill).
func (s *Service) Disconnect(ctx context.Context, userID, id uuid.UUID) (*WriteResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	e, err := getEntry(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	if e.CounterpartDebtID == nil {
		return nil, ErrDebtNotLinked
	}
	other, err := getAnyEntryTx(ctx, tx, *e.CounterpartDebtID)
	if err != nil && !errors.Is(err, ErrEntryNotFound) {
		return nil, err
	}
	ids := []uuid.UUID{e.ID}
	if other != nil {
		ids = append(ids, other.ID)
	}
	if _, err := tx.Exec(ctx, `UPDATE personal_debts
		SET counterpart_debt_id = NULL, disconnected_at = NOW(), updated_by_user_id = user_id,
		    source_kind = CASE WHEN source_kind = 'partner'
		                       THEN CASE WHEN paid_for_transaction_id IS NOT NULL THEN 'paid_by' END
		                       ELSE source_kind END
		WHERE id = ANY($1)`, ids); err != nil {
		return nil, fmt.Errorf("disconnect: %w", err)
	}
	if other != nil && s.notifs != nil {
		// The split notices carry the splitter's entry id (the side that isn't the mirror).
		splitID := e.ID
		if e.SourceKind != nil && *e.SourceKind == SourcePartner {
			splitID = other.ID
		}
		if err := s.notifs.DispatchDebtDisconnected(ctx, tx, other.UserID, userID, splitID, notifications.DebtDisconnectedPayload{
			DebtID: other.ID, Key: other.Key(), PersonName: other.PersonName, Amount: other.Amount,
			Direction: other.Direction, Description: other.Description,
		}); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.writeResult(ctx, userID, e.ID, touch{}, nil)
}
