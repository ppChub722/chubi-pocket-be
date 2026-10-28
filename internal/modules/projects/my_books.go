package projects

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Book kinds — the parts of my books a row can be in.
const (
	BookTx    = "tx"    // my transaction for the bill (the whole bill, or my share)
	BookDebt  = "debt"  // a debt made from it
	BookRepay = "repay" // a repayment pointing at it (a payback's money)
)

// MyBooks (0.4.0-3) — which parts of my books hold this row.
type MyBooks struct {
	Tx    bool `json:"tx"`
	Debt  bool `json:"debt"`
	Repay bool `json:"repay"`
}

// MyBooksRequest — the event sheet's "ลงบัญชีของฉัน" on an existing row. Unlink keeps the rows in
// my books and remembers the row; link takes the remembered rows back.
type MyBooksRequest struct {
	Link   []string `json:"link" binding:"omitempty,dive,oneof=tx debt repay"`
	Unlink []string `json:"unlink" binding:"omitempty,dive,oneof=tx debt repay"`
}

// MyBooksResult — Relinked: the kinds of Link that found rows to take back (the rest are for the
// app to create).
type MyBooksResult struct {
	EventPatch
	Relinked []string `json:"relinked"`
}

func (s *Service) SetMyBooks(
	ctx context.Context, callerUserID, projectID, ptID uuid.UUID, req MyBooksRequest,
) (*MyBooksResult, error) {
	if _, err := s.store.MemberByUserID(ctx, projectID, callerUserID); err != nil {
		return nil, err
	}
	current, err := s.store.GetPT(ctx, ptID)
	if err != nil {
		return nil, err
	}
	if current.ProjectID != projectID || current.ParentProjectTransactionID != nil {
		return nil, ErrPTNotFound
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, kind := range req.Unlink {
		if slices.Contains(req.Link, kind) {
			continue
		}
		if err := unlinkMineTx(ctx, tx, callerUserID, ptID, kind); err != nil {
			return nil, err
		}
	}
	relinked := []string{}
	for _, kind := range req.Link {
		if slices.Contains(req.Unlink, kind) || slices.Contains(relinked, kind) {
			continue
		}
		found, err := relinkMineTx(ctx, tx, callerUserID, projectID, ptID, kind)
		if err != nil {
			return nil, err
		}
		if found {
			relinked = append(relinked, kind)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	patch, err := s.patch(ctx, callerUserID, projectID, &ptID, nil, nil)
	if err != nil {
		return nil, err
	}
	return &MyBooksResult{EventPatch: *patch, Relinked: relinked}, nil
}

// myTxOfRow — my transactions of a kind linked to the row: tx = the row's linked tx or one pointing
// at it / its splits; repay = a repayment pointing at it.
const myTxOfRow = `t.user_id = $2 AND (
		t.id = (SELECT linked_transaction_id FROM project_transactions WHERE id = $1)
		OR t.source_project_transaction_id = $1
		OR t.source_project_transaction_id IN (
			SELECT c.id FROM project_transactions c WHERE c.parent_project_transaction_id = $1))`

func unlinkMineTx(ctx context.Context, tx pgx.Tx, userID, ptID uuid.UUID, kind string) error {
	switch kind {
	case BookDebt, BookRepay:
		// 0.4.1-1 D3: debts and repayments are person-book rows; the bill link sits on the row.
		// V5: the debts' parts of this bill go too (the debt keeps its amount, unlinked).
		if kind == BookDebt {
			if _, err := tx.Exec(ctx, `DELETE FROM personal_debt_bills WHERE user_id = $1 AND pt_id = $2`, userID, ptID); err != nil {
				return fmt.Errorf("unlink bill parts: %w", err)
			}
		}
		_, err := tx.Exec(ctx, `UPDATE personal_debts
			SET unlinked_project_transaction_id = $1, source_kind = NULL, source_project_transaction_id = NULL,
			    project_id = NULL, updated_by_user_id = $2
			WHERE user_id = $2 AND source_project_transaction_id = $1 AND kind = $3`, ptID, userID, bookRowKind(kind))
		if err != nil {
			return fmt.Errorf("unlink debts: %w", err)
		}
		return nil
	case BookTx:
		ids, err := collectIDs(ctx, tx, `SELECT t.id FROM transactions t WHERE `+myTxOfRow, ptID, userID)
		if err != nil {
			return fmt.Errorf("my %s of row: %w", kind, err)
		}
		if len(ids) == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE project_transactions
			SET unlinked_transaction_id = linked_transaction_id, linked_transaction_id = NULL
			WHERE id = $1 AND linked_transaction_id = ANY($2)`, ptID, ids); err != nil {
			return fmt.Errorf("unlink bill origin: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE transactions
			SET unlinked_project_transaction_id = $1, source_project_transaction_id = NULL, project_id = NULL,
			    updated_by_user_id = $2
			WHERE id = ANY($3)`, ptID, userID, ids); err != nil {
			return fmt.Errorf("unlink %s: %w", kind, err)
		}
		return nil
	}
	return nil
}

func relinkMineTx(ctx context.Context, tx pgx.Tx, userID, projectID, ptID uuid.UUID, kind string) (bool, error) {
	switch kind {
	case BookDebt, BookRepay:
		tag, err := tx.Exec(ctx, `UPDATE personal_debts
			SET source_kind = 'event', source_project_transaction_id = $1, project_id = $3,
			    unlinked_project_transaction_id = NULL, updated_by_user_id = $2
			WHERE user_id = $2 AND unlinked_project_transaction_id = $1
			  AND source_kind IS NULL AND source_project_transaction_id IS NULL AND kind = $4`,
			ptID, userID, projectID, bookRowKind(kind))
		if err != nil {
			return false, fmt.Errorf("relink debts: %w", err)
		}
		return tag.RowsAffected() > 0, nil
	case BookTx:
		ids, err := collectIDs(ctx, tx, `SELECT id FROM transactions
			WHERE user_id = $2 AND unlinked_project_transaction_id = $1
			  AND source_project_transaction_id IS NULL AND source_personal_debt_id IS NULL AND project_id IS NULL`,
			ptID, userID)
		if err != nil {
			return false, fmt.Errorf("unlinked %s of row: %w", kind, err)
		}
		if len(ids) == 0 {
			return false, nil
		}
		// The bill's own tx goes back to the bill; a copy of my share to my split.
		var origin *uuid.UUID
		if err := tx.QueryRow(ctx, `UPDATE project_transactions
			SET linked_transaction_id = unlinked_transaction_id, unlinked_transaction_id = NULL
			WHERE id = $1 AND unlinked_transaction_id = ANY($2) AND linked_transaction_id IS NULL
			RETURNING linked_transaction_id`, ptID, ids).Scan(&origin); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, fmt.Errorf("relink bill origin: %w", err)
		}
		var mySplit *uuid.UUID
		err = tx.QueryRow(ctx, `SELECT c.id FROM project_transactions c
			JOIN project_members pm ON pm.id = c.transaction_member_id AND pm.user_id = $2
			WHERE c.parent_project_transaction_id = $1 LIMIT 1`, ptID, userID).Scan(&mySplit)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, fmt.Errorf("my split: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE transactions
			SET source_project_transaction_id = CASE
			        WHEN $4::uuid IS NOT NULL AND id IS DISTINCT FROM $5::uuid THEN $4::uuid ELSE $1 END,
			    project_id = $3, unlinked_project_transaction_id = NULL, updated_by_user_id = $2
			WHERE id = ANY($6)`, ptID, userID, projectID, mySplit, origin, ids); err != nil {
			return false, fmt.Errorf("relink %s: %w", kind, err)
		}
		return true, nil
	}
	return false, nil
}

// bookRowKind — the person-book row kind of a "ลงบัญชีของฉัน" kind (debt / repay).
func bookRowKind(kind string) string {
	if kind == BookRepay {
		return "repayment"
	}
	return "debt"
}

func collectIDs(ctx context.Context, tx pgx.Tx, q string, args ...any) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}
