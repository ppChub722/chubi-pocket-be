package accounts

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
)

// A savings wallet (accounts.auto_save): the whole balance counts as saved. The unallocated part
// is derived on read (shared.LoadAutoSaveWallets), so nothing is booked per transaction.
var (
	ErrAutoSaveNeedsTracked = apierr.Invalid("a savings wallet must track its balance", "auto_save", apierr.FieldInvalid)
	ErrAutoSaveCreditType   = apierr.Invalid("a credit_card / pay_later wallet can't be a savings wallet", "auto_save", apierr.FieldInvalid)
	ErrAutoSaveShared       = apierr.Invalid("a wallet with other members can't be a savings wallet", "auto_save", apierr.FieldInvalid)
	ErrSharedAutoSave       = apierr.Invalid("a savings wallet can't have other members", "auto_save", apierr.FieldInvalid)
)

// checkAutoSave validates the savings-wallet switch of an update: the wallet as it will be after it.
func (s *Service) checkAutoSave(ctx context.Context, userID uuid.UUID, current *Account, req UpdateRequest, effectiveType string) error {
	on := current.AutoSave
	if req.AutoSave != nil {
		on = *req.AutoSave
	}
	if !on {
		return nil
	}
	tracked := current.TrackBalance
	if req.TrackBalance != nil {
		tracked = *req.TrackBalance
	}
	if !tracked {
		return ErrAutoSaveNeedsTracked
	}
	if IsCreditType(effectiveType) {
		return ErrAutoSaveCreditType
	}
	if !current.AutoSave {
		others, err := s.store.CountOtherLiveMembers(ctx, current.ID, userID)
		if err != nil {
			return err
		}
		if others > 0 {
			return ErrAutoSaveShared
		}
	}
	return nil
}

// CountOtherLiveMembers counts the wallet's members other than userID that have joined or are invited.
func (s *Store) CountOtherLiveMembers(ctx context.Context, accountID, userID uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM account_members
		WHERE account_id = $1 AND user_id <> $2 AND left_at IS NULL`, accountID, userID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count other members: %w", err)
	}
	return n, nil
}

// SetAutoSave flips the switch. Turning it on first clears the wallet's booked unallocated saving
// (a withdrawal entry, spare → available, never a delete: a goal's balance may ride on those
// entries), because from then on the unallocated part is derived. Turning it off changes no entry.
func (s *Store) SetAutoSave(ctx context.Context, userID, id uuid.UUID, on bool) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx,
		`UPDATE accounts SET auto_save = $3, updated_by_user_id = $1 WHERE id = $2 AND user_id = $1`, userID, id, on)
	if err != nil {
		return fmt.Errorf("set auto_save: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAccountNotFound
	}
	if on {
		var spare float64
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(CASE WHEN to_bucket = 'spare' THEN amount
			                         WHEN from_bucket = 'spare' THEN -amount ELSE 0 END), 0)::float8
			  FROM saving_entries WHERE user_id = $1 AND account_id = $2`, userID, id).Scan(&spare); err != nil {
			return fmt.Errorf("read spare: %w", err)
		}
		if spare > 0.004 {
			entry, _ := uuid.NewV7()
			group, _ := uuid.NewV7()
			if _, err := tx.Exec(ctx, `
				INSERT INTO saving_entries (id, user_id, group_id, kind, account_id, from_bucket, to_bucket, amount, date, source)
				VALUES ($1, $2, $3, 'withdraw', $4, 'spare', 'available', ROUND($5::numeric, 2), $6, 'manual')`,
				entry, userID, group, id, spare, time.Now().Format("2006-01-02")); err != nil {
				return fmt.Errorf("clear spare: %w", err)
			}
		}
	}
	return tx.Commit(ctx)
}
