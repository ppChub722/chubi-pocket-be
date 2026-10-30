package projects

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// My book vs the event book (owner 2026-10-21): my transaction and the event bill it is linked to
// (the bill made from it = origin, or my copy of my share = copy) never change each other by
// themselves. These are the explicit moves: my tx → the bill (apply_to_event on PUT /transactions),
// the bill → my tx (apply_to_my_tx on a bill edit, or POST /transactions/{id}/apply-event later, both
// computed live), and a transaction switching into / out of an event (link_mode on PUT
// /transactions). transactions.EventLinks is this.

var _ transactions.EventLinks = (*Service)(nil)

// billOfTx — the board bill (parent) a transaction is linked to, or nil.
func billOfTx(ctx context.Context, tx pgx.Tx, row *transactions.Transaction) (*ProjectTransaction, error) {
	if row.SourceProjectTransactionID == nil {
		return nil, nil
	}
	var billID uuid.UUID
	err := tx.QueryRow(ctx, `SELECT COALESCE(parent_project_transaction_id, id) FROM project_transactions WHERE id = $1`,
		*row.SourceProjectTransactionID).Scan(&billID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("bill of tx: %w", err)
	}
	return getPT(ctx, tx, billID, true)
}

func linkedTxOfBillTx(ctx context.Context, tx pgx.Tx, billID uuid.UUID) (*uuid.UUID, error) {
	var id *uuid.UUID
	err := tx.QueryRow(ctx, `SELECT linked_transaction_id FROM project_transactions WHERE id = $1`, billID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return id, err
}

// JoinTx — a transaction of mine switches into an event (link_mode event): it becomes the bill (I
// paid) or my copy of my share (someone else paid: my share, no wallet).
func (s *Service) JoinTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, row *transactions.Transaction,
	projectID uuid.UUID, people shared.BillPeople) error {
	if row.Type != transactions.TypeExpense && row.Type != transactions.TypeIncome {
		return ErrQuickTxNotBillable
	}
	p, me, mb, err := s.billWriterTx(ctx, tx, userID, projectID)
	if err != nil {
		return err
	}
	req := transactions.CreateRequest{
		Type: row.Type, Amount: row.Amount, Date: row.Date, Description: row.Description, Note: row.Note,
		CategoryID: row.CategoryID, AccountID: row.AccountID,
	}
	if _, _, err := s.addPeopleBillTx(ctx, tx, p, me.ID, mb, req, people, row); err != nil {
		return err
	}
	return s.dispatchAddedTx(ctx, tx, p, mb, me.DisplayName)
}

// LeaveTx — a transaction of mine leaves its event: the bill made from it goes (its splits are not
// brought back: the new link mode decides them); my copy just unlinks (the bill stays).
func (s *Service) LeaveTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) error {
	_, err := s.detachBillTx(ctx, tx, userID, txID, false)
	return err
}

// PushTx — apply_to_event: my transaction's amount / date / description / note go to its bill (an
// origin also sends its tags, as event tags by name; a copy sends its amount as my share). Other
// members hear about it as any bill edit (project_tx_changed).
func (s *Service) PushTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, row *transactions.Transaction) error {
	bill, err := billOfTx(ctx, tx, row)
	if err != nil || bill == nil {
		return err
	}
	if err := s.store.AssertWritable(ctx, bill.ProjectID, "edit_pt"); err != nil {
		return err
	}
	me, err := s.store.AssertCanWrite(ctx, bill.ProjectID, userID)
	if err != nil {
		return err
	}
	linked, err := linkedTxOfBillTx(ctx, tx, bill.ID)
	if err != nil {
		return err
	}
	req := UpdateProjectTransactionRequest{Date: &row.Date}
	req.SetText(row.Description, row.Note)
	switch {
	case linked != nil && *linked == row.ID: // origin: the bill IS this transaction
		amount := row.Amount
		req.Amount = &amount
		names, err := personalTagNamesTx(ctx, tx, userID, row.ID)
		if err != nil {
			return err
		}
		tagIDs, err := ensureEventTagsTx(ctx, tx, bill.ProjectID, userID, names)
		if err != nil {
			return err
		}
		req.TagIDs = &tagIDs
	case bill.TransactionMemberID == me.ID: // I paid a bill someone else recorded: my share = the rest
		amount := row.Amount + sumShares(splitSharesTx(ctx, tx, bill.ID))
		req.Amount = &amount
	default: // my copy: my share is my split
		shares := splitSharesTx(ctx, tx, bill.ID)
		shares[me.ID] = row.Amount
		splits := make([]ProjectSplitInput, 0, len(shares))
		for m, v := range shares {
			if v > 0.005 {
				splits = append(splits, ProjectSplitInput{MemberID: m, Amount: v})
			}
		}
		req.Splits = &splits
	}
	if _, err := s.applyBillEditTx(ctx, tx, userID, bill.ProjectID, bill, req); err != nil {
		if errors.Is(err, ErrSplitsExceedParent) {
			return transactions.ErrSplitsExceedAmount
		}
		return err
	}
	return nil
}

// applyBillToTxTx — the bill → my transaction, live: amount (the bill for an origin, my share for a
// copy), date, description, note; the bill's event tags that I have as personal tags are added.
// My own project_tx_changed notices about it are marked actioned.
func (s *Service) applyBillToTxTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) ([]uuid.UUID, error) {
	var row transactions.Transaction
	if err := tx.QueryRow(ctx, `SELECT user_id, account_id, source_project_transaction_id FROM transactions WHERE id = $1`,
		txID).Scan(&row.UserID, &row.AccountID, &row.SourceProjectTransactionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, transactions.ErrTxNotFound
		}
		return nil, fmt.Errorf("my tx: %w", err)
	}
	row.ID = txID
	if row.UserID != userID {
		return nil, transactions.ErrTxNotFound
	}
	bill, err := billOfTx(ctx, tx, &row)
	if err != nil {
		return nil, err
	}
	if bill == nil {
		return nil, ErrBillNotInProject
	}
	people, err := shared.LoadEventBillPeople(ctx, tx, userID, []uuid.UUID{bill.ID})
	if err != nil {
		return nil, err
	}
	b := people[bill.ID]
	linked, err := linkedTxOfBillTx(ctx, tx, bill.ID)
	if err != nil {
		return nil, err
	}
	amount := b.TargetAmount(linked != nil && *linked == txID)
	if amount <= 0.005 {
		return nil, ErrNoShareToCopy
	}
	date := b.Date
	up := transactions.UpdateRequest{Amount: &amount, Date: &date}
	up.SetText(b.Description, b.Note)
	if len(bill.TagNames) > 0 {
		matched, _, err := MatchPersonalTags(ctx, tx, userID, bill.TagNames)
		if err != nil {
			return nil, err
		}
		if len(matched) > 0 {
			current, err := tx.Query(ctx, `SELECT tt.tag_id FROM transaction_tags tt JOIN tags t ON t.id = tt.tag_id
				WHERE tt.transaction_id = $1 AND t.user_id = $2`, txID, userID)
			if err != nil {
				return nil, err
			}
			ids := map[uuid.UUID]bool{}
			for current.Next() {
				var id uuid.UUID
				if err := current.Scan(&id); err != nil {
					current.Close()
					return nil, err
				}
				ids[id] = true
			}
			current.Close()
			all := make([]uuid.UUID, 0, len(ids)+len(matched))
			for id := range ids {
				all = append(all, id)
			}
			for _, id := range matched {
				if !ids[id] {
					all = append(all, id)
				}
			}
			up.TagIDs = all
		}
	}
	if err := s.txs.UpdateInTx(ctx, tx, userID, txID, up); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE notifications SET actioned_at = NOW()
		WHERE recipient_user_id = $1 AND type = 'project_tx_changed' AND actioned_at IS NULL
		  AND payload->>'personal_transaction_id' = $2`, userID, txID.String()); err != nil {
		return nil, fmt.Errorf("action notices: %w", err)
	}
	var touched []uuid.UUID
	if row.AccountID != nil {
		touched = append(touched, *row.AccountID)
	}
	return touched, nil
}

// ApplyEventToTx — POST /transactions/{id}/apply-event: "อัปเดตตามอีเวนต์" whenever I choose. 0.4.1-2
// S7: the same as POST /event-bills/{bill}/apply for this transaction's bill (my debts of it too,
// effects); the row answers as it is now (none when my copy went: I have no part any more).
func (s *Service) ApplyEventToTx(ctx context.Context, userID, txID uuid.UUID) (*transactions.WriteResult, error) {
	if s.txs == nil {
		return nil, errors.New("transactions module not wired")
	}
	var source *uuid.UUID
	err := s.store.db.QueryRow(ctx, `SELECT source_project_transaction_id FROM transactions WHERE id = $1 AND user_id = $2`,
		txID, userID).Scan(&source)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, transactions.ErrTxNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("my tx: %w", err)
	}
	if source == nil {
		return nil, ErrBillNotInProject
	}
	res, err := s.ApplyBill(ctx, userID, *source)
	if err != nil {
		return nil, err
	}
	out := &transactions.WriteResult{Rows: []transactions.TransactionDetail{}, AccountBalances: res.Effects.AccountBalances,
		Effects: res.Effects}
	if d, err := s.txs.Get(ctx, userID, txID); err == nil {
		out.Rows = append(out.Rows, *d)
	} else if !errors.Is(err, transactions.ErrTxNotFound) {
		return nil, err
	}
	return out, nil
}

// myLinkedTxTx — my transaction linked to a bill: the one it was made from, or my copy (a copy may
// point at the bill or at one of its split rows).
func myLinkedTxTx(ctx context.Context, tx pgx.Tx, userID, billID uuid.UUID) (*uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT t.id FROM transactions t
		WHERE t.user_id = $1 AND t.split_transaction_id = $2 AND t.source_project_transaction_id IS NOT NULL
		ORDER BY t.created_at LIMIT 1`, userID, billID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("my linked tx: %w", err)
	}
	return &id, nil
}
