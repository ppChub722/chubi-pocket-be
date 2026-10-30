package projects

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

var ErrNoShareToCopy = errors.New("you have no part in this project row to copy")

func personalCopyTx(ctx context.Context, tx pgx.Tx, userID, ptID uuid.UUID) (id uuid.UUID, amount float64, ok bool) {
	// A copy points at the bill (split_transaction_id also covers one made from a split row).
	err := tx.QueryRow(ctx, `
		SELECT id, amount FROM transactions
		WHERE user_id = $1 AND split_transaction_id = $2 AND source_project_transaction_id IS NOT NULL
		ORDER BY created_at LIMIT 1`, userID, ptID).Scan(&id, &amount)
	return id, amount, err == nil
}

func splitSharesTx(ctx context.Context, tx pgx.Tx, parentID uuid.UUID) map[uuid.UUID]float64 {
	out := map[uuid.UUID]float64{}
	rows, err := tx.Query(ctx, `
		SELECT sp.event_member_id, sp.share FROM split_transaction_people sp
		JOIN project_transactions b ON b.id = sp.split_transaction_id
		WHERE sp.split_transaction_id = $1 AND sp.share > 0 AND sp.event_member_id IS NOT NULL
		  AND sp.event_member_id IS DISTINCT FROM b.transaction_member_id`, parentID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var m uuid.UUID
		var v float64
		if rows.Scan(&m, &v) == nil {
			out[m] = v
		}
	}
	return out
}

func sumShares(m map[uuid.UUID]float64) float64 {
	var t float64
	for _, v := range m {
		t += v
	}
	return t
}

func copyAmountTx(ctx context.Context, tx pgx.Tx, pt *ProjectTransaction, memberID uuid.UUID) (float64, error) {
	var share float64
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(share), 0)::float8 FROM split_transaction_people
		WHERE split_transaction_id = $1 AND event_member_id = $2`, pt.ID, memberID).Scan(&share)
	if err != nil {
		return 0, err
	}
	if share <= 0.005 {
		return 0, ErrNoShareToCopy
	}
	return share, nil
}

func (s *Service) copyToPersonalTx(
	ctx context.Context, tx pgx.Tx, userID, memberID uuid.UUID, pt *ProjectTransaction,
) (uuid.UUID, error) {
	if id, _, ok := personalCopyTx(ctx, tx, userID, pt.ID); ok {
		return id, nil
	}
	if pt.Type == PTTypePayback {
		return uuid.Nil, ErrPaybackNotCopyable
	}
	amount, err := copyAmountTx(ctx, tx, pt, memberID)
	if err != nil {
		return uuid.Nil, err
	}
	var categoryID *uuid.UUID
	var tagIDs []uuid.UUID
	if len(pt.TagNames) > 0 {
		lowered := make([]string, len(pt.TagNames))
		for i, t := range pt.TagNames {
			lowered[i] = strings.ToLower(t)
		}
		var id uuid.UUID
		if err := tx.QueryRow(ctx, `
			SELECT id FROM categories
			WHERE user_id = $1 AND type = $2 AND NOT is_system
			  AND LOWER(name) = ANY($3::text[])
			ORDER BY array_position($3::text[], LOWER(name)),
			         (parent_id IS NOT NULL) DESC, sort_order
			LIMIT 1`, userID, pt.Type, lowered).Scan(&id); err == nil {
			categoryID = &id
		}
		if tagIDs, _, err = MatchPersonalTags(ctx, tx, userID, pt.TagNames); err != nil {
			return uuid.Nil, err
		}
	}
	created, err := s.txs.CreateInTx(ctx, tx, userID, transactions.CreateRequest{
		Type:                       transactions.TxType(pt.Type),
		Amount:                     amount,
		CategoryID:                 categoryID,
		Date:                       pt.Date,
		Description:                pt.Description,
		Note:                       pt.Note,
		TagIDs:                     tagIDs,
		SourceProjectTransactionID: &pt.ID,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("copy project row: %w", err)
	}
	d, ok := created.(*transactions.TransactionDetail)
	if !ok {
		return uuid.Nil, fmt.Errorf("copy project row: unexpected result %T", created)
	}
	return d.ID, nil
}

func (s *Service) CopyToPersonal(ctx context.Context, userID, projectID, ptID uuid.UUID) (*EventCopyResult, error) {
	member, err := s.store.MemberByUserID(ctx, projectID, userID)
	if err != nil {
		return nil, err
	}
	pt, err := s.store.GetPT(ctx, ptID)
	if err != nil {
		return nil, err
	}
	if pt.ProjectID != projectID {
		return nil, ErrPTNotFound
	}
	if pt.ParentProjectTransactionID != nil {
		return nil, ErrPTIsChild
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	id, err := s.copyToPersonalTx(ctx, tx, userID, member.ID, pt)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	p, err := s.patch(ctx, userID, projectID, &pt.ID, nil, nil)
	if err != nil {
		return nil, err
	}
	d, err := s.txs.Get(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	return &EventCopyResult{EventPatch: *p, Transaction: d}, nil
}

// (notifyRecordedTx / notifyChangedTx / suggestedUpdate, the one-way copy notices, were retired in
// 0.4.1-2 S16: a bill save fans out to every member by their switch, shared_bill.go.)
