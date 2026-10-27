package personal_debts

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

var (
	ErrNotSplitChange       = errors.New("notification is not a split change")
	ErrSplitChangeStale     = errors.New("this change no longer applies — the split was changed again or removed")
	ErrNotificationActioned = errors.New("this notification's action was already used")
)

const amountEps = 0.005

func (s *Service) EditForTransactionTx(
	ctx context.Context, tx pgx.Tx,
	parentTxID, userID uuid.UUID,
	parentType, parentCurrency string, parentDescription *string,
	edits []transactions.SplitEdit,
) error {
	rows, err := tx.Query(ctx, `SELECT `+entryColumns+` FROM personal_debts
		WHERE source_transaction_id = $1 AND user_id = $2
		ORDER BY created_at, id FOR UPDATE`, parentTxID, userID)
	if err != nil {
		return fmt.Errorf("load splits: %w", err)
	}
	existing := map[uuid.UUID]*Entry{}
	var order []uuid.UUID
	for rows.Next() {
		d, err := scanEntry(rows)
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
	for _, id := range order {
		d := existing[id]
		newAmount, ok := kept[id]
		if ident, swap := idents[id]; ok && swap && ident.replace {
			if err := s.notifySplitChangeTx(ctx, tx, d, 0, notifications.SplitChangeRemoved); err != nil {
				return err
			}
			if err := removeDebtTx(ctx, tx, d); err != nil {
				return fmt.Errorf("remove split: %w", err)
			}
			added = append(added, transactions.SplitInput{
				PersonName: ident.name, ContactID: ident.contactID, OwedAmount: newAmount,
			})
			continue
		}
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

type identityEdit struct {
	name      string
	contactID *uuid.UUID // the split's contact after the edit; nil = a free name
	newLink   bool       // contactID is a different contact than before
	replace   bool
}

func checkIdentityEditTx(
	ctx context.Context, tx pgx.Tx, userID uuid.UUID, d *Entry, e transactions.SplitEdit,
) (identityEdit, bool, error) {
	name := strings.TrimSpace(e.PersonName)
	out := identityEdit{name: d.PersonName, contactID: d.ContactID}
	if e.ContactIDSet || e.ContactID != nil {
		out.contactID = e.ContactID
	}
	contactChanged := !sameUUID(out.contactID, d.ContactID)
	if !contactChanged && out.contactID != nil {
		return identityEdit{}, false, nil // same contact: the name is not editable
	}
	nameChanged := name != "" && name != d.PersonName
	if !nameChanged && !contactChanged {
		return identityEdit{}, false, nil
	}
	out.replace = contactChanged && linkedPartnerTx(ctx, tx, d) != nil
	if out.contactID == nil {
		if name != "" {
			out.name = name
		}
		return out, true, nil
	}
	if err := assertSplitContactTx(ctx, tx, userID, *out.contactID); err != nil {
		return identityEdit{}, false, err
	}
	out.newLink = true
	if err := tx.QueryRow(ctx, `SELECT `+shared.PersonNameSQL("c", "lu")+`
		FROM contacts c LEFT JOIN users lu ON lu.id = c.linked_user_id
		WHERE c.id = $1`, *out.contactID).Scan(&out.name); err != nil {
		return identityEdit{}, false, fmt.Errorf("contact name: %w", err)
	}
	return out, true, nil
}

func sameUUID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (s *Service) applyIdentityEditTx(
	ctx context.Context, tx pgx.Tx, debtID, parentTxID uuid.UUID, e identityEdit,
) error {
	if _, err := tx.Exec(ctx, `UPDATE personal_debts
		SET counterparty_person_name = $2, counterparty_contact_id = $3,
		    updated_by_user_id = user_id
		WHERE id = $1`, debtID, e.name, e.contactID); err != nil {
		return fmt.Errorf("split person: %w", err)
	}
	if !e.newLink {
		return nil
	}
	d, err := getAnyEntryTx(ctx, tx, debtID)
	if err != nil {
		return err
	}
	partner := linkedPartnerTx(ctx, tx, d)
	if partner == nil {
		return nil
	}
	return s.splitToPartnerTx(ctx, tx, d, *partner, parentTxID)
}

// assertSplitContactTx — a new split's contact must be the caller's own (an archived contact is
// fine: archiving only hides it from lists and pickers).
func assertSplitContactTx(ctx context.Context, tx pgx.Tx, userID, contactID uuid.UUID) error {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM contacts WHERE id = $1 AND user_id = $2`,
		contactID, userID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return transactions.ErrSplitContactNotFound
	}
	return err
}

func linkedPartnerTx(ctx context.Context, tx pgx.Tx, d *Entry) *uuid.UUID {
	if d.ContactID == nil {
		return nil
	}
	var linked *uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT linked_user_id FROM contacts WHERE id = $1 AND user_id = $2`,
		*d.ContactID, d.UserID).Scan(&linked)
	if linked == nil || *linked == d.UserID {
		return nil
	}
	return linked
}

func (s *Service) notifySplitChangeTx(
	ctx context.Context, tx pgx.Tx, d *Entry, newAmount float64, change string,
) error {
	if s.notifs == nil || d.DisconnectedAt != nil {
		return nil // a disconnected pair (D9): the other book no longer hears about it
	}
	partner := linkedPartnerTx(ctx, tx, d)
	if partner == nil {
		return nil
	}
	var mirror *Entry
	if d.CounterpartDebtID != nil {
		m, err := getAnyEntryTx(ctx, tx, *d.CounterpartDebtID)
		if err != nil && !errors.Is(err, ErrEntryNotFound) {
			return err
		}
		if m != nil && m.UserID == *partner {
			mirror = m
		}
	}
	if mirror == nil {
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
	} else if d.PaidForTransactionID != nil {
		p.ParentID = *d.PaidForTransactionID
	}
	actioned := false
	if dl.Auto {
		if _, err := s.applySplitChangeTx(ctx, tx, mirror, p); err != nil {
			return err
		}
		actioned = true
	}
	return s.notifs.DispatchSplitChanged(ctx, tx, *partner, d.UserID, p, actioned)
}
