package projects

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

// autoResolveOwnTx — auto_resolve_own_in_projects (contract §5, owner
// decision 2026-10-07): when the caller records a project row where they
// are the actor, their personal-book mirror is created in the same DB
// transaction, exactly like the manual "resolve" sheet's default: full
// amount, same type/date, linked through source_project_transaction_id.
//
// It is always a floating (no-wallet) row — the user moves it into a
// wallet later. The category is matched by name against the caller's own
// active, non-system categories of the same type; no match = none.
func (s *Service) autoResolveOwnTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, pt *ProjectTransaction) error {
	var categoryID *uuid.UUID
	if pt.CategoryName != nil && *pt.CategoryName != "" {
		var id uuid.UUID
		err := tx.QueryRow(ctx, `
			SELECT id FROM categories
			WHERE user_id = $1 AND type = $2 AND status = 'active' AND NOT is_system
			  AND LOWER(name) = LOWER($3)
			ORDER BY (parent_id IS NOT NULL) DESC, sort_order
			LIMIT 1`, userID, pt.Type, *pt.CategoryName).Scan(&id)
		if err == nil {
			categoryID = &id
		}
	}
	note := pt.Note
	if note == nil {
		note = pt.Description
	}
	_, err := s.txs.CreateInTx(ctx, tx, userID, transactions.CreateRequest{
		Type:                       transactions.TxType(pt.Type),
		Amount:                     pt.Amount,
		CategoryID:                 categoryID,
		Date:                       pt.Date,
		Note:                       note,
		SourceProjectTransactionID: &pt.ID,
	})
	if err != nil {
		return fmt.Errorf("auto-resolve own project row: %w", err)
	}
	return nil
}
