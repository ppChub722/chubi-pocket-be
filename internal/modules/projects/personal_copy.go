package projects

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

// Personal copies of project rows (contract §5). A project row lives in the
// project's own book; a member's "personal copy" is an ordinary transaction
// in their book linked by source_project_transaction_id. Copies are always
// floating (no wallet) — the user moves them into a wallet later — and the
// category is matched by name against the user's own categories.
//
// Amount basis: the actor (whose money moved) copies the full amount; a
// split member copies their own share (their split row).

var ErrNoShareToCopy = errors.New("you have no part in this project row to copy")

// personalCopyTx — the user's existing copy of a project row, if any.
func personalCopyTx(ctx context.Context, tx pgx.Tx, userID, ptID uuid.UUID) (id uuid.UUID, amount float64, ok bool) {
	err := tx.QueryRow(ctx, `
		SELECT id, amount FROM transactions
		WHERE user_id = $1 AND source_project_transaction_id = $2
		ORDER BY created_at LIMIT 1`, userID, ptID).Scan(&id, &amount)
	return id, amount, err == nil
}

// splitSharesTx — Σ split rows per member under a parent. Taken before and
// after an edit (children are replaced wholesale on update).
func splitSharesTx(ctx context.Context, tx pgx.Tx, parentID uuid.UUID) map[uuid.UUID]float64 {
	out := map[uuid.UUID]float64{}
	rows, err := tx.Query(ctx, `
		SELECT transaction_member_id, SUM(amount) FROM project_transactions
		WHERE parent_project_transaction_id = $1 GROUP BY transaction_member_id`, parentID)
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

// copyAmountTx — what memberID's copy of parent pt should be.
func copyAmountTx(ctx context.Context, tx pgx.Tx, pt *ProjectTransaction, memberID uuid.UUID) (float64, error) {
	if pt.TransactionMemberID == memberID {
		return pt.Amount, nil
	}
	var share float64
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount), 0) FROM project_transactions
		WHERE parent_project_transaction_id = $1 AND transaction_member_id = $2`,
		pt.ID, memberID).Scan(&share)
	if err != nil {
		return 0, err
	}
	if share <= 0 {
		return 0, ErrNoShareToCopy
	}
	return share, nil
}

// copyToPersonalTx creates userID's floating copy of parent pt (idempotent:
// an existing copy is returned as is).
func (s *Service) copyToPersonalTx(
	ctx context.Context, tx pgx.Tx, userID, memberID uuid.UUID, pt *ProjectTransaction,
) (uuid.UUID, error) {
	if id, _, ok := personalCopyTx(ctx, tx, userID, pt.ID); ok {
		return id, nil
	}
	amount, err := copyAmountTx(ctx, tx, pt, memberID)
	if err != nil {
		return uuid.Nil, err
	}
	var categoryID *uuid.UUID
	if pt.CategoryName != nil && *pt.CategoryName != "" {
		var id uuid.UUID
		if err := tx.QueryRow(ctx, `
			SELECT id FROM categories
			WHERE user_id = $1 AND type = $2 AND status = 'active' AND NOT is_system
			  AND LOWER(name) = LOWER($3)
			ORDER BY (parent_id IS NOT NULL) DESC, sort_order
			LIMIT 1`, userID, pt.Type, *pt.CategoryName).Scan(&id); err == nil {
			categoryID = &id
		}
	}
	note := pt.Note
	if note == nil {
		note = pt.Description
	}
	created, err := s.txs.CreateInTx(ctx, tx, userID, transactions.CreateRequest{
		Type:                       transactions.TxType(pt.Type),
		Amount:                     amount,
		CategoryID:                 categoryID,
		Date:                       pt.Date,
		Note:                       note,
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

// CopyToPersonal — POST /projects/:id/project-transactions/:pt_id/copy:
// the "add to my book" button on project_tx_recorded_for_you.
func (s *Service) CopyToPersonal(ctx context.Context, userID, projectID, ptID uuid.UUID) (uuid.UUID, error) {
	member, err := s.store.MemberByUserID(ctx, projectID, userID)
	if err != nil {
		return uuid.Nil, err
	}
	pt, err := s.store.GetPT(ctx, ptID)
	if err != nil {
		return uuid.Nil, err
	}
	if pt.ProjectID != projectID {
		return uuid.Nil, ErrPTNotFound
	}
	if pt.ParentProjectTransactionID != nil {
		return uuid.Nil, ErrPTIsChild
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	id, err := s.copyToPersonalTx(ctx, tx, userID, member.ID, pt)
	if err != nil {
		return uuid.Nil, err
	}
	return id, tx.Commit(ctx)
}

// notifyRecordedTx — project_tx_recorded_for_you to the row's actor when
// someone else recorded it. Auto = copy it into their book now.
func (s *Service) notifyRecordedTx(
	ctx context.Context, tx pgx.Tx, actorUserID, actorMemberID, recorderUserID uuid.UUID, pt *ProjectTransaction,
) error {
	d, err := s.notifs.DeliveryFor(ctx, tx, actorUserID, &recorderUserID, notifications.TypeProjectTxRecordedForYou)
	if err != nil || !d.Deliver {
		return err
	}
	var copyID *uuid.UUID
	if d.Auto && s.txs != nil {
		id, err := s.copyToPersonalTx(ctx, tx, actorUserID, actorMemberID, pt)
		if err != nil {
			return err
		}
		copyID = &id
	}
	return s.notifs.DispatchProjectTxRecorded(ctx, tx, actorUserID, recorderUserID,
		notifications.ProjectTxRecordedPayload{
			ProjectTransactionID:  pt.ID,
			ProjectID:             pt.ProjectID,
			RecorderUserID:        recorderUserID,
			RecorderDisplayName:   userDisplayOrEmpty(ctx, tx, recorderUserID),
			Amount:                pt.Amount,
			Currency:              pt.Currency,
			Type:                  pt.Type,
			Note:                  pt.Note,
			PersonalTransactionID: copyID,
		}, copyID != nil)
}

// suggestedUpdate — new values for recipient's copy after `before` became
// `after`. The amount keeps the copy's basis (full → new full, share →
// new share); a copy the user edited to something else keeps its amount.
func suggestedUpdate(
	copyAmount, beforeFull, beforeShare, afterFull, afterShare float64, after *ProjectTransaction,
) *notifications.PersonalUpdate {
	amount := copyAmount
	switch copyAmount {
	case beforeFull:
		amount = afterFull
	case beforeShare:
		amount = afterShare
	}
	note := after.Note
	if note == nil {
		note = after.Description
	}
	return &notifications.PersonalUpdate{Amount: amount, Date: after.Date, Note: note}
}

// notifyChangedTx — project_tx_changed ("edited") to one recipient. When
// they have a personal copy the payload carries the suggested update (the
// "update to match" button); auto applies it now. No copy → informational.
//
// Basis per recipient: the actor's copy is "full" (or the actor's own
// share = full − others' splits); a split member's copy is their split.
func (s *Service) notifyChangedTx(
	ctx context.Context, tx pgx.Tx, recipientUserID, editorUserID uuid.UUID,
	before, after *ProjectTransaction, beforeShares, afterShares map[uuid.UUID]float64,
	diff map[string][2]interface{},
) error {
	d, err := s.notifs.DeliveryFor(ctx, tx, recipientUserID, &editorUserID, notifications.TypeProjectTxChanged)
	if err != nil || !d.Deliver {
		return err
	}
	p := notifications.ProjectTxChangedPayload{
		ProjectTransactionID: after.ID,
		ProjectID:            after.ProjectID,
		EditorUserID:         editorUserID,
		EditorDisplayName:    userDisplayOrEmpty(ctx, tx, editorUserID),
		ChangeKind:           "edited",
		Diff:                 diff,
	}
	applied := false
	if copyID, copyAmount, ok := personalCopyTx(ctx, tx, recipientUserID, after.ID); ok {
		var beforeShare, afterShare float64
		if m, err := s.store.MemberByUserID(ctx, after.ProjectID, recipientUserID); err == nil && m.ID != after.TransactionMemberID {
			beforeShare, afterShare = beforeShares[m.ID], afterShares[m.ID]
		} else {
			beforeShare = before.Amount - sumShares(beforeShares)
			afterShare = after.Amount - sumShares(afterShares)
		}
		sug := suggestedUpdate(copyAmount, before.Amount, beforeShare, after.Amount, afterShare, after)
		p.PersonalTransactionID = &copyID
		p.Suggested = sug
		if d.Auto && s.txs != nil {
			amount, date := sug.Amount, sug.Date
			if err := s.txs.UpdateInTx(ctx, tx, recipientUserID, copyID, transactions.UpdateRequest{
				Amount: &amount, Date: &date, Note: sug.Note,
			}); err != nil {
				return fmt.Errorf("update personal copy: %w", err)
			}
			applied = true
		}
	}
	return s.notifs.DispatchProjectTxChanged(ctx, tx, recipientUserID, editorUserID, p, applied)
}
