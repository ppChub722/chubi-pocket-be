package personal_debts

// Editing a bill's splits after save (owner 2026-10-10) and telling the
// linked partners. Ledger rule: the partner's book is never changed for
// them — they get split_changed with a one-shot "อัปเดตตาม" (or their own
// auto switch runs it on arrival).
//
//   - added person    → the usual split_created (as at create)
//   - re-amounted / removed, partner has the mirror → split_changed
//   - partner hasn't added it yet → their pending split_created is refreshed
//     (amount) or superseded (removed); nothing new is sent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

var (
	ErrNotSplitChange       = errors.New("notification is not a split change")
	ErrSplitChangeStale     = errors.New("this change no longer applies — the split was changed again or removed")
	ErrNotificationActioned = errors.New("this notification's action was already used")
)

// SplitChangeResult — what "อัปเดตตาม" did to the caller's debt.
type SplitChangeResult struct {
	Result string        `json:"result"` // updated | deleted | zeroed
	Debt   *PersonalDebt `json:"debt"`
}

const amountEps = 0.005

// EditForTransactionTx is the transactions.SplitsEditor hook: make
// parentTxID's split debts match `edits` (the whole new list).
func (s *Service) EditForTransactionTx(
	ctx context.Context, tx pgx.Tx,
	parentTxID, userID uuid.UUID,
	parentType, parentCurrency string, parentDescription *string,
	edits []transactions.SplitEdit,
) error {
	rows, err := tx.Query(ctx, `SELECT `+debtColumns+` FROM personal_debts
		WHERE source_transaction_id = $1 AND user_id = $2
		ORDER BY created_at, id FOR UPDATE`, parentTxID, userID)
	if err != nil {
		return fmt.Errorf("load splits: %w", err)
	}
	existing := map[uuid.UUID]*PersonalDebt{}
	var order []uuid.UUID
	for rows.Next() {
		d, err := scanDebt(rows)
		if err != nil {
			rows.Close()
			return fmt.Errorf("scan split: %w", err)
		}
		existing[d.ID] = d
		order = append(order, d.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Validate everything before touching a row.
	kept := map[uuid.UUID]float64{}
	idents := map[uuid.UUID]identityEdit{}
	var added []transactions.SplitInput
	for _, e := range edits {
		if e.DebtID == nil {
			if e.PersonName == "" {
				return transactions.ErrSplitPersonRequired
			}
			if e.ContactID != nil {
				if err := assertSplitContactTx(ctx, tx, userID, *e.ContactID); err != nil {
					return err
				}
			}
			added = append(added, transactions.SplitInput{
				PersonName: e.PersonName, ContactID: e.ContactID, OwedAmount: e.OwedAmount,
			})
			continue
		}
		d, ok := existing[*e.DebtID]
		if _, dup := kept[*e.DebtID]; !ok || dup {
			return transactions.ErrSplitUnknownDebt
		}
		kept[*e.DebtID] = e.OwedAmount
		ident, changed, err := checkIdentityEditTx(ctx, tx, userID, d, e)
		if err != nil {
			return err
		}
		if changed {
			idents[d.ID] = ident
		}
	}
	// Apply: removals, re-amounts, additions.
	for _, id := range order {
		d := existing[id]
		newAmount, ok := kept[id]
		switch {
		case !ok:
			if err := s.notifySplitChangeTx(ctx, tx, d, 0, notifications.SplitChangeRemoved); err != nil {
				return err
			}
			if err := removeDebtTx(ctx, tx, d); err != nil {
				return fmt.Errorf("remove split: %w", err)
			}
		case math.Abs(newAmount-d.Amount) > amountEps:
			if err := setDebtAmountTx(ctx, tx, id, newAmount); err != nil {
				return fmt.Errorf("re-amount split: %w", err)
			}
			if err := s.notifySplitChangeTx(ctx, tx, d, newAmount, notifications.SplitChangeAmount); err != nil {
				return err
			}
		}
		// After the re-amount, so a newly linked partner hears the current amount.
		if ident, ok := idents[id]; ok {
			if err := s.applyIdentityEditTx(ctx, tx, d.ID, parentTxID, ident); err != nil {
				return err
			}
		}
	}
	if len(added) > 0 {
		return s.CreateForTransactionTx(ctx, tx, parentTxID, userID, parentType, parentCurrency, parentDescription, added)
	}
	return nil
}

// identityEdit — the new person of a kept split (owner 2026-10-10).
type identityEdit struct {
	name      string
	contactID *uuid.UUID // nil: keep the row unlinked (a rename)
}

// checkIdentityEditTx — does kept split d change its person? person_name /
// contact_id left out (or equal) = unchanged. Only an unlinked row
// (contact_id NULL) may change: rename in place, or link to one of my
// contacts (name defaults to the contact's). A row with a contact is fixed
// → ErrSplitIdentityLocked (remove + add instead).
func checkIdentityEditTx(
	ctx context.Context, tx pgx.Tx, userID uuid.UUID, d *PersonalDebt, e transactions.SplitEdit,
) (identityEdit, bool, error) {
	name := strings.TrimSpace(e.PersonName)
	nameChanged := name != "" && name != d.CounterpartyPersonName
	contactChanged := e.ContactID != nil &&
		(d.CounterpartyContactID == nil || *d.CounterpartyContactID != *e.ContactID)
	if !nameChanged && !contactChanged {
		return identityEdit{}, false, nil
	}
	if d.CounterpartyContactID != nil {
		return identityEdit{}, false, transactions.ErrSplitIdentityLocked
	}
	out := identityEdit{name: name}
	if contactChanged {
		if err := assertSplitContactTx(ctx, tx, userID, *e.ContactID); err != nil {
			return identityEdit{}, false, err
		}
		out.contactID = e.ContactID
		if name == "" {
			if err := tx.QueryRow(ctx, `SELECT display_name FROM contacts WHERE id = $1`,
				*e.ContactID).Scan(&out.name); err != nil {
				return identityEdit{}, false, fmt.Errorf("contact name: %w", err)
			}
		}
	}
	return out, true, nil
}

// applyIdentityEditTx renames / links splitter debt debtID in place — same
// row, so repayments stay. A rename tells no one; a link to a contact with an
// app account tells them like a new split (split_created per their flags).
func (s *Service) applyIdentityEditTx(
	ctx context.Context, tx pgx.Tx, debtID, parentTxID uuid.UUID, e identityEdit,
) error {
	if _, err := tx.Exec(ctx, `UPDATE personal_debts
		SET counterparty_person_name = $2,
		    counterparty_contact_id = COALESCE($3, counterparty_contact_id),
		    updated_by_user_id = user_id
		WHERE id = $1`, debtID, e.name, e.contactID); err != nil {
		return fmt.Errorf("split person: %w", err)
	}
	if e.contactID == nil {
		return nil
	}
	d, err := getAnyDebtTx(ctx, tx, debtID)
	if err != nil {
		return err
	}
	partner := linkedPartnerTx(ctx, tx, d)
	if partner == nil {
		return nil
	}
	return s.splitToPartnerTx(ctx, tx, d, *partner, parentTxID)
}

// assertSplitContactTx — a new split's contact must be the caller's own and
// not archived.
func assertSplitContactTx(ctx context.Context, tx pgx.Tx, userID, contactID uuid.UUID) error {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM contacts WHERE id = $1 AND user_id = $2`,
		contactID, userID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return transactions.ErrSplitContactNotFound
	}
	if err != nil {
		return err
	}
	if status == "archived" {
		return transactions.ErrSplitContactArchived
	}
	return nil
}

// linkedPartnerTx — the app user behind a splitter debt's contact (nil when
// the contact isn't linked, or is the splitter).
func linkedPartnerTx(ctx context.Context, tx pgx.Tx, d *PersonalDebt) *uuid.UUID {
	if d.CounterpartyContactID == nil {
		return nil
	}
	var linked *uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT linked_user_id FROM contacts WHERE id = $1 AND user_id = $2`,
		*d.CounterpartyContactID, d.UserID).Scan(&linked)
	if linked == nil || *linked == d.UserID {
		return nil
	}
	return linked
}

// notifySplitChangeTx tells the linked partner that splitter debt `d` (as it
// was before) is now `newAmount` (change=amount) or gone (change=removed).
func (s *Service) notifySplitChangeTx(
	ctx context.Context, tx pgx.Tx, d *PersonalDebt, newAmount float64, change string,
) error {
	if s.notifs == nil {
		return nil
	}
	partner := linkedPartnerTx(ctx, tx, d)
	if partner == nil {
		return nil
	}
	var mirror *PersonalDebt
	if d.CounterpartDebtID != nil {
		m, err := getAnyDebtTx(ctx, tx, *d.CounterpartDebtID)
		if err != nil && !errors.Is(err, ErrDebtNotFound) {
			return err
		}
		if m != nil && m.UserID == *partner {
			mirror = m
		}
	}
	if mirror == nil {
		// Nothing in their book yet: keep a pending "add to my debts" in step.
		if change == notifications.SplitChangeRemoved {
			return s.notifs.SupersedeSplitNoticesTx(ctx, tx, *partner, notifications.TypeSplitCreated, d.ID)
		}
		_, err := s.notifs.RefreshSplitCreatedTx(ctx, tx, *partner, d.ID, newAmount)
		return err
	}

	dl, err := s.notifs.DeliveryFor(ctx, tx, *partner, &d.UserID, notifications.TypeSplitChanged)
	if err != nil || !dl.Deliver {
		return err
	}
	p := notifications.SplitChangedPayload{
		SplitID:             d.ID,
		ParentKind:          "transaction",
		Change:              change,
		SplitterDisplayName: displayNameTx(ctx, tx, d.UserID),
		OldAmount:           d.Amount,
		NewAmount:           newAmount,
		Currency:            d.Currency,
		Description:         d.Description,
		RecipientDebtID:     mirror.ID,
	}
	if d.SourceTransactionID != nil {
		p.ParentID = *d.SourceTransactionID
	}
	actioned := false
	if dl.Auto {
		// Their own switch: apply now.
		if _, err := applySplitChangeTx(ctx, tx, mirror, p); err != nil {
			return err
		}
		actioned = true
	}
	return s.notifs.DispatchSplitChanged(ctx, tx, *partner, d.UserID, p, actioned)
}

// applySplitChangeTx makes the recipient's mirror match the change.
func applySplitChangeTx(ctx context.Context, tx pgx.Tx, mirror *PersonalDebt, p notifications.SplitChangedPayload) (*SplitChangeResult, error) {
	if p.Change == notifications.SplitChangeRemoved {
		if err := removeDebtTx(ctx, tx, mirror); err != nil {
			return nil, fmt.Errorf("remove mirror: %w", err)
		}
		if mirror.SettledAmount <= amountEps {
			return &SplitChangeResult{Result: "deleted"}, nil
		}
		out, err := getAnyDebtTx(ctx, tx, mirror.ID)
		if err != nil {
			return nil, err
		}
		return &SplitChangeResult{Result: "zeroed", Debt: out}, nil
	}
	if err := setDebtAmountTx(ctx, tx, mirror.ID, p.NewAmount); err != nil {
		return nil, fmt.Errorf("update mirror: %w", err)
	}
	out, err := getAnyDebtTx(ctx, tx, mirror.ID)
	if err != nil {
		return nil, err
	}
	return &SplitChangeResult{Result: "updated", Debt: out}, nil
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
		// Gone, or kept at 0 because it had repayments.
		if err == nil && splitterAmount > amountEps {
			return nil, ErrSplitChangeStale
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrSplitChangeStale
	case err != nil:
		return nil, err
	case math.Abs(splitterAmount-p.NewAmount) > amountEps:
		return nil, ErrSplitChangeStale
	}

	mirror, err := getAnyDebtTx(ctx, tx, p.RecipientDebtID)
	if err != nil {
		return nil, err
	}
	if mirror.UserID != userID {
		return nil, ErrNotSplitChange
	}
	out, err := applySplitChangeTx(ctx, tx, mirror, p)
	if err != nil {
		return nil, err
	}
	if _, err := s.notifs.MarkActionedTx(ctx, tx, userID, notificationID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return out, nil
}

// Repayments don't constrain edits (owner 2026-10-10): the amount just
// changes; outstanding = amount − settled may go negative (overpaid, shown
// as owed the other way). Settled only when repaid exactly matches.
const debtStatusAfterAmount = `CASE WHEN status = 'cancelled' THEN status
	WHEN ABS(settled_amount - amount) < 0.005 THEN 'settled' ELSE 'open' END`

func setDebtAmountTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, amount float64) error {
	if _, err := tx.Exec(ctx, `UPDATE personal_debts SET amount = $2 WHERE id = $1`, id, amount); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE personal_debts SET status = `+debtStatusAfterAmount+`,
		updated_by_user_id = user_id WHERE id = $1`, id)
	return err
}

// removeDebtTx — a person taken off a split. Unpaid → the row is deleted.
// Already repaid → the row stays at amount 0, so what they paid shows as
// overpaid (owner 2026-10-10); the repayment transaction is untouched.
func removeDebtTx(ctx context.Context, tx pgx.Tx, d *PersonalDebt) error {
	if d.SettledAmount > amountEps {
		return setDebtAmountTx(ctx, tx, d.ID, 0)
	}
	_, err := tx.Exec(ctx, `DELETE FROM personal_debts WHERE id = $1`, d.ID)
	return err
}
