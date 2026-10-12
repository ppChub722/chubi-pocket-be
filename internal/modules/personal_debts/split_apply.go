package personal_debts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
)

// applySplitChangeTx makes the recipient's mirror match the change: a
// re-amount sets it, a removal deletes it. Returns updated | deleted.
func applySplitChangeTx(ctx context.Context, tx pgx.Tx, mirror *Entry, p notifications.SplitChangedPayload) (string, error) {
	if p.Change == notifications.SplitChangeRemoved {
		if err := removeDebtTx(ctx, tx, mirror); err != nil {
			return "", fmt.Errorf("remove mirror: %w", err)
		}
		return "deleted", nil
	}
	if err := setDebtAmountTx(ctx, tx, mirror.ID, p.NewAmount); err != nil {
		return "", fmt.Errorf("update mirror: %w", err)
	}
	return "updated", nil
}

// ApplySplitChange — "อัปเดตตาม" on a split_changed notification
// (POST /v1/personal-debts/split-changes/:notification_id/apply). One shot:
// a used, superseded or out-of-date change is refused.
func (s *Service) ApplySplitChange(ctx context.Context, userID, notificationID uuid.UUID) (*SplitChangeResult, error) {
	if s.notifs == nil {
		return nil, errors.New("notifications module not wired")
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	n, err := s.notifs.GetByIDForCallerTx(ctx, tx, userID, notificationID)
	if err != nil {
		return nil, err
	}
	if n.Type != notifications.TypeSplitChanged {
		return nil, ErrNotSplitChange
	}
	if n.ActionedAt != nil {
		return nil, ErrNotificationActioned
	}
	var p notifications.SplitChangedPayload
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	if p.Superseded {
		return nil, ErrSplitChangeStale
	}
	// The change must still be the splitter's current state.
	var splitterAmount float64
	err = tx.QueryRow(ctx, `SELECT amount FROM personal_debts WHERE id = $1`, p.SplitID).Scan(&splitterAmount)
	switch {
	case p.Change == notifications.SplitChangeRemoved:
		if err == nil {
			return nil, ErrSplitChangeStale // the split is back / still there
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrSplitChangeStale
	case err != nil:
		return nil, err
	case math.Abs(splitterAmount-p.NewAmount) > amountEps:
		return nil, ErrSplitChangeStale
	}

	mirror, err := getAnyEntryTx(ctx, tx, p.RecipientDebtID)
	if errors.Is(err, ErrEntryNotFound) {
		return nil, ErrSplitChangeStale // they deleted their side
	}
	if err != nil {
		return nil, err
	}
	if mirror.UserID != userID {
		return nil, ErrNotSplitChange
	}
	result, err := applySplitChangeTx(ctx, tx, mirror, p)
	if err != nil {
		return nil, err
	}
	if _, err := s.notifs.MarkActionedTx(ctx, tx, userID, notificationID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	out := &SplitChangeResult{Result: result}
	if result == "updated" {
		if out.Entry, err = entryView(ctx, s.store.db, userID, mirror.ID); err != nil {
			return nil, err
		}
	}
	if out.PersonBalance, err = s.balanceOfKey(ctx, s.store.db, userID, mirror.Key()); err != nil {
		return nil, err
	}
	return out, nil
}

func setDebtAmountTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, amount float64) error {
	_, err := tx.Exec(ctx, `UPDATE personal_debts SET amount = $2, updated_by_user_id = user_id WHERE id = $1`, id, amount)
	return err
}

// removeDebtTx — a person taken off a split: the entry goes.
func removeDebtTx(ctx context.Context, tx pgx.Tx, d *Entry) error {
	_, err := tx.Exec(ctx, `DELETE FROM personal_debts WHERE id = $1`, d.ID)
	return err
}
