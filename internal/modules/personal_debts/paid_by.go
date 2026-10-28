package personal_debts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Paid by someone else (0.4.0-3). The debt "I owe Lee 13" of an expense Lee paid part of is a
// received entry (source paid_by) linked to the expense by paid_for_transaction_id. It is one record
// with the expense: the transaction writes it (SetPaidByTx) and deleting the transaction deletes it.
// (The one-way partner mirror of 0.4.0-3 was retired in 0.4.1-2 S16; a linked payer is a shared bill.)

func paidForEntryTx(ctx context.Context, tx pgx.Tx, txID uuid.UUID) (*Entry, error) {
	e, err := scanEntry(tx.QueryRow(ctx,
		`SELECT `+entryColumns+` FROM personal_debts WHERE paid_for_transaction_id = $1 FOR UPDATE`, txID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("paid-by debt: %w", err)
	}
	return e, nil
}

// paidByPerson — the person a paid_by names: a contact of mine, or a typed name.
func paidByPerson(ctx context.Context, tx pgx.Tx, userID uuid.UUID, in *transactions.PaidByInput) (*uuid.UUID, string, error) {
	typed := strings.TrimSpace(in.PersonName)
	if in.ContactID == nil {
		return nil, typed, nil
	}
	contactID, name, err := resolvePerson(ctx, tx, userID, "c:"+in.ContactID.String(), nil)
	if errors.Is(err, ErrContactNotFound) {
		return nil, "", transactions.ErrSplitContactNotFound
	}
	return contactID, name, err
}

func (s *Service) SetPaidByTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, row *transactions.Transaction, in *transactions.PaidByInput) (string, error) {
	cur, err := paidForEntryTx(ctx, tx, row.ID)
	if err != nil {
		return "", err
	}
	if cur != nil && cur.UserID != userID {
		return "", ErrEntryNotFound
	}
	if in == nil {
		if cur == nil {
			return "", nil
		}
		return cur.Key(), s.removePaidByTx(ctx, tx, cur)
	}
	contactID, name, err := paidByPerson(ctx, tx, userID, in)
	if err != nil {
		return "", err
	}
	samePerson := cur != nil && sameUUID(cur.ContactID, contactID) &&
		(contactID != nil || strings.EqualFold(strings.TrimSpace(cur.PersonName), name))
	if cur != nil && !samePerson {
		// Another person paid: the old debt goes (its partner is told), a new one comes.
		if err := s.removePaidByTx(ctx, tx, cur); err != nil {
			return "", err
		}
		cur = nil
	}
	if cur == nil {
		kind := SourcePaidBy
		// they paid my expense: I owe them; they received my income ("รับแทน"): they owe me
		dir := DirectionReceived
		if row.Type == transactions.TypeIncome {
			dir = DirectionPaid
		}
		e, err := insertEntryTx(ctx, tx, &Entry{
			UserID: userID, Direction: dir, ContactID: contactID, PersonName: name,
			Amount: in.Amount, Date: row.Date, SourceKind: &kind, Description: row.Description,
			PaidForTransactionID: &row.ID,
		})
		if err != nil {
			return "", err
		}
		return e.Key(), nil
	}
	if contactID == nil {
		name = strings.TrimSpace(name) // a respelling of the same typed name
	} else {
		name = cur.PersonName
	}
	if _, err := tx.Exec(ctx, `UPDATE personal_debts
		SET amount = $2, date = $3::date, description = $4, counterparty_person_name = $5, updated_by_user_id = $6
		WHERE id = $1`, cur.ID, in.Amount, row.Date, shared.CleanText(row.Description), name, userID); err != nil {
		return "", fmt.Errorf("update paid-by debt: %w", err)
	}
	return cur.Key(), nil
}

func (s *Service) removePaidByTx(ctx context.Context, tx pgx.Tx, e *Entry) error {
	if _, err := tx.Exec(ctx, `DELETE FROM personal_debts WHERE id = $1`, e.ID); err != nil {
		return fmt.Errorf("delete paid-by debt: %w", err)
	}
	return nil
}

func (s *Service) PaidByDeletingTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) (string, *uuid.UUID, bool, error) {
	e, err := paidForEntryTx(ctx, tx, txID)
	if err != nil || e == nil || e.UserID != userID {
		return "", nil, false, err
	}
	return e.Key(), &e.ID, true, s.removePaidByTx(ctx, tx, e)
}

// PersonNetTx — a person's balance as q sees it (a write's preview reads it inside and outside its
// savepoint); 0 and the bare key's name when they have no lines.
func (s *Service) PersonNetTx(ctx context.Context, q shared.Querier, userID uuid.UUID, key string) (shared.PersonRef, float64, error) {
	k, err := shared.ParseDebtKey(key)
	if err != nil {
		return shared.PersonRef{}, 0, err
	}
	p, found, err := s.store.personBalance(ctx, q, userID, k)
	if err != nil {
		return shared.PersonRef{}, 0, err
	}
	if !found {
		return shared.PersonRef{Name: k.Name, ContactID: k.ContactID}, 0, nil
	}
	return p.Person, p.Net, nil
}
