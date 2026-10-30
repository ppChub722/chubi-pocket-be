package users

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// ErrInvalidDefaultAccount — default_account_id must be an active wallet the caller is an active
// member of.
var ErrInvalidDefaultAccount = apierr.Invalid("default_account_id must be an active wallet you're a member of", "default_account_id", apierr.FieldInvalid)

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

func (s *Store) loadSettings(ctx context.Context, p *Profile) error {
	var (
		acc   *uuid.UUID
		muted []string
	)
	p.AutoSyncEvents, p.AutoSyncDebts, p.AutoAcceptEvents = true, true, true // no row yet = the defaults (110 / 113)
	err := s.db.QueryRow(ctx, `SELECT default_account_id, auto_sync_events, auto_sync_debts, auto_accept_events,
		auto_accept_debts, muted_types FROM user_notification_settings WHERE user_id = $1`, p.ID).Scan(&acc,
		&p.AutoSyncEvents, &p.AutoSyncDebts, &p.AutoAcceptEvents, &p.AutoAcceptDebts, &muted)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("settings: %w", err)
	}
	p.NotificationSummary.On, p.NotificationSummary.Total = notifications.SettingsCounts(muted)
	if err := s.loadFeeCategory(ctx, p); err != nil {
		return err
	}
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

// loadFeeCategory — preferences.fee_category_id as a CategoryRef while it is still one of my
// expense categories (same rule as the form's fee_category_id); else null.
func (s *Store) loadFeeCategory(ctx context.Context, p *Profile) error {
	if p.Preferences.FeeCategoryID == nil {
		return nil
	}
	id, err := uuid.Parse(*p.Preferences.FeeCategoryID)
	if err != nil {
		return nil
	}
	var ok bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM categories
		WHERE id = $1 AND user_id = $2 AND NOT is_system AND type = 'expense')`, id, p.ID).Scan(&ok); err != nil {
		return fmt.Errorf("fee category: %w", err)
	}
	if !ok {
		return nil
	}
	refs, err := shared.LoadCategoryRefs(ctx, s.db, []uuid.UUID{id})
	if err != nil {
		return err
	}
	if r, ok := refs[id]; ok {
		p.FeeCategory = &r
	}
	return nil
}

// settingSwitches — the switch columns of user_notification_settings a PUT /users/me may set.
func (r *UpdateProfileRequest) settingSwitches() map[string]*bool {
	out := map[string]*bool{}
	for col, v := range map[string]*bool{
		"auto_sync_events": r.AutoSyncEvents, "auto_sync_debts": r.AutoSyncDebts,
		"auto_accept_events": r.AutoAcceptEvents, "auto_accept_debts": r.AutoAcceptDebts,
	} {
		if v != nil {
			out[col] = v
		}
	}
	return out
}

func saveSettingsTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, acc *uuid.UUID, accSet bool, switches map[string]*bool) error {
	if !accSet && len(switches) == 0 {
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
	for col, v := range switches { // col comes from settingSwitches' fixed list, never from input
		if _, err := tx.Exec(ctx, `UPDATE user_notification_settings SET `+col+` = $2, updated_by_user_id = $1 WHERE user_id = $1`,
			userID, *v); err != nil {
			return fmt.Errorf("%s: %w", col, err)
		}
	}
	return nil
}
