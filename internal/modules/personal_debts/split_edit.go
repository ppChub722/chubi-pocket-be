package personal_debts

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
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
		switch {
		case !ok:
			if err := removeDebtTx(ctx, tx, d); err != nil {
				return fmt.Errorf("remove split: %w", err)
			}
		case math.Abs(newAmount-d.Amount) > amountEps:
			if err := setDebtAmountTx(ctx, tx, id, newAmount); err != nil {
				return fmt.Errorf("re-amount split: %w", err)
			}
		}
		if ident, ok := idents[id]; ok {
			if err := applyIdentityEditTx(ctx, tx, d.ID, ident); err != nil {
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

// applyIdentityEditTx — the split's person changes in place (0.4.1-2 S16: no mirror to swap).
func applyIdentityEditTx(ctx context.Context, tx pgx.Tx, debtID uuid.UUID, e identityEdit) error {
	if _, err := tx.Exec(ctx, `UPDATE personal_debts
		SET counterparty_person_name = $2, counterparty_contact_id = $3,
		    updated_by_user_id = user_id
		WHERE id = $1`, debtID, e.name, e.contactID); err != nil {
		return fmt.Errorf("split person: %w", err)
	}
	return nil
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
