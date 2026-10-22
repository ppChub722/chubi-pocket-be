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
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

// applySplitChangeTx — "อัปเดตตาม": my mirror follows the splitter's change, and so does my
// expense of it (paid_for_transaction_id; wallet-less, so no balance moves).
func (s *Service) applySplitChangeTx(ctx context.Context, tx pgx.Tx, mirror *Entry, p notifications.SplitChangedPayload) (string, error) {
	if p.Change == notifications.SplitChangeRemoved {
		if mirror.PaidForTransactionID != nil && s.txs != nil {
			if _, err := s.txs.DeleteInTx(ctx, tx, mirror.UserID, *mirror.PaidForTransactionID); err != nil &&
				!errors.Is(err, transactions.ErrTxNotFound) {
				return "", fmt.Errorf("remove mirror expense: %w", err)
			}
		}
		if err := removeDebtTx(ctx, tx, mirror); err != nil {
			return "", fmt.Errorf("remove mirror: %w", err)
		}
		return "deleted", nil
	}
	if err := setDebtAmountTx(ctx, tx, mirror.ID, p.NewAmount); err != nil {
		return "", fmt.Errorf("update mirror: %w", err)
	}
	if mirror.PaidForTransactionID != nil && s.txs != nil {
		if err := s.txs.SetPaidForAmountTx(ctx, tx, *mirror.PaidForTransactionID, p.NewAmount); err != nil {
			return "", err
		}
	}
	return "updated", nil
}

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
	result, err := s.applySplitChangeTx(ctx, tx, mirror, p)
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

func removeDebtTx(ctx context.Context, tx pgx.Tx, d *Entry) error {
	_, err := tx.Exec(ctx, `DELETE FROM personal_debts WHERE id = $1`, d.ID)
	return err
}
