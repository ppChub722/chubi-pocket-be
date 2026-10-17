package users

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// ErrInvalidDefaultAccount — default_account_id must be an active wallet the
// caller is an active member of.
var ErrInvalidDefaultAccount = errors.New("default_account_id must be an active wallet you're a member of")

// defaultAccountChange — (id, true) sets, (nil, true) clears, false = absent.
func (r *UpdateProfileRequest) defaultAccountChange() (*uuid.UUID, bool, error) {
	raw := bytes.TrimSpace(r.DefaultAccountID)
	if len(raw) == 0 {
		return nil, false, nil
	}
	if string(raw) == "null" {
		return nil, true, nil
	}
	var id uuid.UUID
	if err := json.Unmarshal(raw, &id); err != nil {
		return nil, false, ErrInvalidDefaultAccount
	}
	return &id, true, nil
}

// loadSettings fills DefaultAccount and AutoCopyOwnEventBills of the profile.
func (s *Store) loadSettings(ctx context.Context, p *Profile) error {
	var (
		acc  *uuid.UUID
		auto bool
	)
	err := s.db.QueryRow(ctx, `SELECT default_account_id, auto_resolve_own_in_projects
		FROM user_notification_settings WHERE user_id = $1`, p.ID).Scan(&acc, &auto)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("settings: %w", err)
	}
	p.AutoCopyOwnEventBills = auto
	if acc != nil {
		refs, err := shared.LoadAccountRefs(ctx, s.db, []uuid.UUID{*acc})
		if err != nil {
			return err
		}
		if r, ok := refs[*acc]; ok {
			p.DefaultAccount = &r
		}
	}
	return nil
}

// saveSettingsTx upserts the two settings that live in user_notification_settings.
func saveSettingsTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, acc *uuid.UUID, accSet bool, auto *bool) error {
	if !accSet && auto == nil {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_notification_settings (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING`, userID); err != nil {
		return fmt.Errorf("settings row: %w", err)
	}
	if accSet {
		if acc != nil {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounts a WHERE a.id = $1 AND a.status = 'active' AND `+
				shared.ActiveMembershipPredicate("a.id", "$2")+`)`, *acc, userID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return ErrInvalidDefaultAccount
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE user_notification_settings SET default_account_id = $2, updated_by_user_id = $1 WHERE user_id = $1`, userID, acc); err != nil {
			return fmt.Errorf("default account: %w", err)
		}
	}
	if auto != nil {
		if _, err := tx.Exec(ctx, `UPDATE user_notification_settings SET auto_resolve_own_in_projects = $2, updated_by_user_id = $1 WHERE user_id = $1`, userID, *auto); err != nil {
			return fmt.Errorf("auto copy: %w", err)
		}
	}
	return nil
}
