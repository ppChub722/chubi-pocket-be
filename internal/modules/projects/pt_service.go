package projects

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

func (s *Service) CreateProjectTransaction(
	ctx context.Context, callerUserID, projectID uuid.UUID, req CreateProjectTransactionRequest,
) (*EventPatch, error) {
	if err := s.store.AssertWritable(ctx, projectID, "create_pt"); err != nil {
		return nil, err
	}
	if _, err := s.store.AssertCanWrite(ctx, projectID, callerUserID); err != nil {
		return nil, err
	}
	member, err := s.store.GetMember(ctx, req.TransactionMemberID)
	if err != nil {
		return nil, err
	}
	if member.ProjectID != projectID {
		return nil, ErrMemberNotInProject
	}
	req.TagNames = nil // names are a Go-side input only
	payback := req.Type == PTTypePayback
	if payback {
		if err := validatePayback(req.TransactionMemberID, req.Amount, req.Splits); err != nil {
			return nil, err
		}
		req.TagIDs = nil
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	pt, err := s.store.InsertPTTx(ctx, tx, projectID, callerUserID, req)
	if err != nil {
		return nil, err
	}
	if pt, err = s.store.GetPTTx(ctx, tx, pt.ID); err != nil {
		return nil, err
	}
	if s.notifs != nil && member.UserID != nil && *member.UserID == callerUserID && s.txs != nil && !payback {
		st, err := s.notifs.SettingsTx(ctx, tx, callerUserID)
		if err == nil && st.AutoSyncEvents {
			if _, err := s.copyToPersonalTx(ctx, tx, callerUserID, member.ID, pt); err != nil &&
				!errors.Is(err, ErrNoShareToCopy) {
				return nil, err
			}
		}
	}
	// 0.4.1-2 S6: every other member with a part gets the bill by their switch (a payback = their
	// repayment row; never copied as an expense).
	if err := s.fanOutTx(ctx, tx, callerUserID, pt.ID, true, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.patch(ctx, callerUserID, projectID, &pt.ID, nil, nil)
}

func validatePayback(from uuid.UUID, amount float64, splits []ProjectSplitInput) error {
	if len(splits) != 1 || splits[0].MemberID == from || math.Abs(splits[0].Amount-amount) > 0.005 {
		return ErrBadPayback
	}
	return nil
}

// paybackEdit fills in what a payback edit leaves out (the receiver, the payer, the amount), so the
// one child always carries the whole amount.
func paybackEdit(ctx context.Context, tx pgx.Tx, current *ProjectTransaction, req *UpdateProjectTransactionRequest) error {
	from := current.TransactionMemberID
	if req.TransactionMemberID != nil {
		from = *req.TransactionMemberID
	}
	amount := current.Amount
	if req.Amount != nil {
		amount = *req.Amount
	}
	var to uuid.UUID
	if req.Splits != nil && len(*req.Splits) > 0 {
		to = (*req.Splits)[0].MemberID
		if len(*req.Splits) != 1 {
			return ErrBadPayback
		}
	} else if err := tx.QueryRow(ctx, `SELECT transaction_member_id FROM project_transactions
		WHERE parent_project_transaction_id = $1 LIMIT 1`, current.ID).Scan(&to); err != nil {
		return fmt.Errorf("payback receiver: %w", err)
	}
	splits := []ProjectSplitInput{{MemberID: to, Amount: amount}}
	req.Splits, req.TagIDs = &splits, nil
	return validatePayback(from, amount, splits)
}

type originTx struct {
	id        uuid.UUID
	userID    uuid.UUID
	accountID *uuid.UUID
}

func linkedOriginTx(ctx context.Context, tx pgx.Tx, ptID uuid.UUID) (*originTx, error) {
	var o originTx
	err := tx.QueryRow(ctx, `SELECT t.id, t.user_id, t.account_id FROM project_transactions pt
		JOIN transactions t ON t.id = pt.linked_transaction_id
		WHERE pt.id = $1 FOR UPDATE OF t`, ptID).Scan(&o.id, &o.userID, &o.accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("origin tx: %w", err)
	}
	return &o, nil
}

func (s *Service) UpdateProjectTransaction(
	ctx context.Context, callerUserID, projectID, ptID uuid.UUID, req UpdateProjectTransactionRequest,
) (*EventPatch, error) {
	if err := s.store.AssertWritable(ctx, projectID, "edit_pt"); err != nil {
		return nil, err
	}
	ptID = s.store.splitRowOf(ctx, ptID) // a board split id is a person; the row it stands for
	current, err := s.store.GetPT(ctx, ptID)
	if err != nil {
		return nil, err
	}
	if current.ProjectID != projectID {
		return nil, ErrPTNotFound
	}
	if current.ParentProjectTransactionID != nil {
		return nil, ErrPTIsChild
	}
	if _, err := s.store.AssertCanWrite(ctx, projectID, callerUserID); err != nil {
		return nil, err
	}
	if req.TransactionMemberID != nil && current.Type != PTTypePayback {
		return nil, ErrPayerFixed
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	var mine *uuid.UUID
	if current.Type == PTTypePayback {
		// A repayment that points here is not a copy of it: the payback never rewrites it.
		if err := paybackEdit(ctx, tx, current, &req); err != nil {
			return nil, err
		}
	} else if mine, err = myLinkedTxTx(ctx, tx, callerUserID, ptID); err != nil {
		return nil, err
	}

	updated, err := s.applyBillEditTx(ctx, tx, callerUserID, projectID, current, req)
	if err != nil {
		return nil, err
	}
	// "แก้ในสมุดของฉันด้วย": my linked transaction takes the bill as it is now (apply_to_my_tx; the
	// old also_update_link does the same).
	var touched []uuid.UUID
	if mine != nil && (req.ApplyToMyTx || req.AlsoUpdateLink) {
		if s.txs == nil {
			return nil, errors.New("transactions module not wired")
		}
		if touched, err = s.applyBillToTxTx(ctx, tx, callerUserID, *mine); err != nil {
			return nil, fmt.Errorf("linked transaction: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	p, err := s.patch(ctx, callerUserID, projectID, &updated.ID, nil, touched)
	if err != nil {
		return nil, err
	}
	if mine != nil && s.txs != nil {
		if p.MyTransaction, err = s.txs.Get(ctx, callerUserID, *mine); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// applyBillEditTx edits a bill and sends it to every other member (0.4.1-2 S5: by their switch).
func (s *Service) applyBillEditTx(
	ctx context.Context, tx pgx.Tx, editorUserID, projectID uuid.UUID, current *ProjectTransaction,
	req UpdateProjectTransactionRequest,
) (*ProjectTransaction, error) {
	updated, err := s.store.UpdatePTTx(ctx, tx, projectID, current.ID, editorUserID, req)
	if err != nil {
		return nil, err
	}
	if err := s.fanOutTx(ctx, tx, editorUserID, current.ID, false, buildDiff(current, updated)); err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *Service) SyncBillFromTxTx(ctx context.Context, tx pgx.Tx, userID, ptID uuid.UUID, amount float64, date string) error {
	current, err := s.store.GetPTTx(ctx, tx, ptID)
	if err != nil {
		return err
	}
	if current.ParentProjectTransactionID != nil {
		return nil
	}
	if math.Abs(current.Amount-amount) <= 0.005 && current.Date == date {
		return nil
	}
	if _, err := s.applyBillEditTx(ctx, tx, userID, current.ProjectID, current,
		UpdateProjectTransactionRequest{Amount: &amount, Date: &date}); err != nil {
		if errors.Is(err, ErrSplitsExceedParent) {
			return transactions.ErrSplitsExceedAmount
		}
		return err
	}
	return nil
}

func (s *Service) DeleteBillOfTxTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) (*uuid.UUID, error) {
	var ptID, projectID uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id, project_id FROM project_transactions
		WHERE linked_transaction_id = $1 AND parent_project_transaction_id IS NULL`, txID).Scan(&ptID, &projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("linked bill: %w", err)
	}
	if err := s.store.AssertWritable(ctx, projectID, "edit_pt"); err != nil {
		return nil, nil
	}
	if _, err := s.store.AssertCanWrite(ctx, projectID, userID); err != nil {
		return nil, nil
	}
	if err := s.deleteBoardRowTx(ctx, tx, userID, projectID, ptID); err != nil {
		return nil, err
	}
	return &ptID, nil
}

func (s *Service) DeleteProjectTransaction(
	ctx context.Context, callerUserID, projectID, ptID uuid.UUID,
) (*EventPatch, error) {
	if err := s.store.AssertWritable(ctx, projectID, "edit_pt"); err != nil {
		return nil, err
	}
	ptID = s.store.splitRowOf(ctx, ptID) // a board split id is a person; the row it stands for
	pt, err := s.store.GetPT(ctx, ptID)
	if err != nil {
		return nil, err
	}
	if pt.ProjectID != projectID {
		return nil, ErrPTNotFound
	}
	if _, err := s.store.AssertCanWrite(ctx, projectID, callerUserID); err != nil {
		return nil, err
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	var originTxID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM transactions
		WHERE source_project_transaction_id = $1 AND user_id = $2
		ORDER BY created_at LIMIT 1`, ptID, callerUserID).Scan(&originTxID)
	switch {
	case err == nil:
		origin, err := isBoardOriginTx(ctx, tx, callerUserID, ptID)
		if err != nil {
			return nil, err
		}
		if origin {
			if _, err := s.detachBillTx(ctx, tx, callerUserID, originTxID, true); err != nil {
				return nil, err
			}
			break
		}
		if err := s.deleteBoardRowTx(ctx, tx, callerUserID, projectID, ptID); err != nil {
			return nil, err
		}
	case errors.Is(err, pgx.ErrNoRows):
		if err := s.deleteBoardRowTx(ctx, tx, callerUserID, projectID, ptID); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("origin bill: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	if pt.ParentProjectTransactionID != nil {
		return s.patch(ctx, callerUserID, projectID, pt.ParentProjectTransactionID, nil, nil)
	}
	return s.patch(ctx, callerUserID, projectID, nil, &ptID, nil)
}

func (s *Service) ListPT(ctx context.Context, callerUserID, projectID uuid.UUID, f BoardFilter) (*BoardRowList, error) {
	if _, err := s.store.GetByIDForCaller(ctx, callerUserID, projectID); err != nil {
		return nil, err
	}
	e, err := loadEvent(ctx, s.store.db, callerUserID, projectID)
	if err != nil {
		return nil, err
	}
	f.Page, f.PerPage = clampPage(f.Page, f.PerPage)
	rows, total, err := loadBoard(ctx, s.store.db, callerUserID, projectID, e, f, nil)
	if err != nil {
		return nil, err
	}
	return &BoardRowList{Data: rows, Pagination: shared.NewPage(f.Page, f.PerPage, total)}, nil
}

func (s *Service) ToggleMark(
	ctx context.Context, callerUserID, projectID, ptID uuid.UUID, marked bool,
) (*EventPatch, error) {
	if err := s.store.AssertWritable(ctx, projectID, "mark"); err != nil {
		return nil, err
	}
	member, err := s.store.AssertCanWrite(ctx, projectID, callerUserID)
	if err != nil {
		return nil, err
	}
	ptID = s.store.splitRowOf(ctx, ptID) // a board split id is a person; the row it stands for
	current, err := s.store.GetPT(ctx, ptID)
	if err != nil {
		return nil, err
	}
	if current.ProjectID != projectID {
		return nil, ErrPTNotFound
	}
	if err := s.store.ToggleMark(ctx, projectID, ptID, member.ID, marked); err != nil {
		return nil, err
	}
	rowID := current.ID
	if current.ParentProjectTransactionID != nil {
		rowID = *current.ParentProjectTransactionID
	}
	return s.patch(ctx, callerUserID, projectID, &rowID, nil, nil)
}

func buildDiff(before, after *ProjectTransaction) map[string][2]interface{} {
	diff := make(map[string][2]interface{})
	if before.Amount != after.Amount {
		diff["amount"] = [2]interface{}{before.Amount, after.Amount}
	}
	if before.Date != after.Date {
		diff["date"] = [2]interface{}{before.Date, after.Date}
	}
	if (before.Description == nil) != (after.Description == nil) ||
		(before.Description != nil && after.Description != nil && *before.Description != *after.Description) {
		diff["description"] = [2]interface{}{ptr(before.Description), ptr(after.Description)}
	}
	if (before.Note == nil) != (after.Note == nil) ||
		(before.Note != nil && after.Note != nil && *before.Note != *after.Note) {
		diff["note"] = [2]interface{}{ptr(before.Note), ptr(after.Note)}
	}
	return diff
}

func ptr(s *string) interface{} {
	if s == nil {
		return nil
	}
	return *s
}
