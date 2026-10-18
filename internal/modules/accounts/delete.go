package accounts

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

type DeletePreview struct {
	Transactions int `json:"transactions"`
	Transfers int `json:"transfers"`
	SystemRowsDeleted int `json:"system_rows_deleted"`
	Recurring int `json:"recurring"`
	SavingEntries SavingEntriesPreview `json:"saving_entries"`
}

type SavingEntriesPreview struct {
	Count  int     `json:"count"`
	Amount float64 `json:"amount"`
}

type DeleteResult struct {
	ID      uuid.UUID     `json:"id"`
	Deleted DeletePreview `json:"deleted"`
}

const systemRowsSQL = `SELECT t.id FROM transactions t JOIN categories c ON c.id = t.category_id
	WHERE t.account_id = $1 AND c.is_system AND c.system_kind IN ('OPENING_IN', 'OPENING_OUT', 'ADJUST_IN', 'ADJUST_OUT')`

func (s *Service) ownedForDelete(ctx context.Context, userID, id uuid.UUID) error {
	a, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return err
	}
	if !a.CanEdit {
		return ErrNotOwner
	}
	n, err := s.store.CountActiveMembers(ctx, id)
	if err != nil {
		return err
	}
	if n > 1 {
		return ErrAccountHasMembers
	}
	return nil
}

func (s *Service) previewOn(ctx context.Context, id uuid.UUID) (*DeletePreview, error) {
	p := &DeletePreview{}
	db := s.store.Pool()
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE t.type <> 'transfer' AND NOT COALESCE(sys.is, FALSE)),
			COUNT(*) FILTER (WHERE t.type = 'transfer'),
			COUNT(*) FILTER (WHERE COALESCE(sys.is, FALSE))
		FROM transactions t
		LEFT JOIN LATERAL (SELECT COALESCE(c.is_system AND c.system_kind IN ('OPENING_IN', 'OPENING_OUT', 'ADJUST_IN', 'ADJUST_OUT'), FALSE) AS is
			FROM categories c WHERE c.id = t.category_id) sys ON TRUE
		WHERE t.account_id = $1`, id).Scan(&p.Transactions, &p.Transfers, &p.SystemRowsDeleted); err != nil {
		return nil, fmt.Errorf("delete preview: %w", err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM recurring_rules WHERE account_id = $1 OR to_account_id = $1`, id).Scan(&p.Recurring); err != nil {
		return nil, fmt.Errorf("delete preview: %w", err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*), COALESCE(SUM(amount), 0)::float8 FROM saving_entries WHERE account_id = $1`, id).
		Scan(&p.SavingEntries.Count, &p.SavingEntries.Amount); err != nil {
		return nil, fmt.Errorf("delete preview: %w", err)
	}
	p.SavingEntries.Amount = round2(p.SavingEntries.Amount)
	return p, nil
}

func (s *Service) DeletePreview(ctx context.Context, userID, id uuid.UUID) (*DeletePreview, error) {
	if err := s.ownedForDelete(ctx, userID, id); err != nil {
		return nil, err
	}
	return s.previewOn(ctx, id)
}

func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) (*DeleteResult, error) {
	if err := s.ownedForDelete(ctx, userID, id); err != nil {
		return nil, err
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT 1 FROM accounts WHERE id = $1 FOR UPDATE`, id); err != nil {
		return nil, fmt.Errorf("lock wallet: %w", err)
	}
	preview, err := s.previewOn(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM transactions WHERE id IN (`+systemRowsSQL+`)`, id); err != nil {
		return nil, fmt.Errorf("delete system rows: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE transactions SET account_id = NULL WHERE account_id = $1`, id); err != nil {
		return nil, fmt.Errorf("unlink rows: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, id); err != nil {
		return nil, fmt.Errorf("delete wallet: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return &DeleteResult{ID: id, Deleted: *preview}, nil
}
