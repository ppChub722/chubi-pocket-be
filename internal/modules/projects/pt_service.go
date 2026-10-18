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
	if s.notifs != nil && member.UserID != nil {
		if *member.UserID == callerUserID {
			if s.txs != nil {
				st, err := s.notifs.SettingsTx(ctx, tx, callerUserID)
				if err == nil && st.AutoResolveOwnInProjects {
					if _, err := s.copyToPersonalTx(ctx, tx, callerUserID, member.ID, pt); err != nil &&
						!errors.Is(err, ErrNoShareToCopy) {
						return nil, err
					}
				}
			}
		} else if err := s.notifyRecordedTx(ctx, tx, *member.UserID, member.ID, callerUserID, pt); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.patch(ctx, callerUserID, projectID, &pt.ID, nil, nil)
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

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	amountChange := req.Amount != nil && math.Abs(*req.Amount-current.Amount) > 0.005
	dateChange := req.Date != nil && *req.Date != current.Date
	origin, err := linkedOriginTx(ctx, tx, ptID)
	if err != nil {
		return nil, err
	}

	updated, err := s.applyBillEditTx(ctx, tx, callerUserID, projectID, current, req)
	if err != nil {
		return nil, err
	}
	var touched []uuid.UUID
	if origin != nil && origin.userID == callerUserID && req.AlsoUpdateLink && (amountChange || dateChange) {
		if s.txs == nil {
			return nil, errors.New("transactions module not wired")
		}
		up := transactions.UpdateRequest{Amount: req.Amount, Date: req.Date}
		if err := s.txs.UpdateInTx(ctx, tx, callerUserID, origin.id, up); err != nil {
			return nil, fmt.Errorf("linked transaction: %w", err)
		}
		if origin.accountID != nil {
			touched = append(touched, *origin.accountID)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.patch(ctx, callerUserID, projectID, &updated.ID, nil, touched)
}

func (s *Service) applyBillEditTx(
	ctx context.Context, tx pgx.Tx, editorUserID, projectID uuid.UUID, current *ProjectTransaction,
	req UpdateProjectTransactionRequest,
) (*ProjectTransaction, error) {
	beforeShares := splitSharesTx(ctx, tx, current.ID)
	updated, err := s.store.UpdatePTTx(ctx, tx, projectID, current.ID, editorUserID, req)
	if err != nil {
		return nil, err
	}
	if s.notifs != nil {
		recipients, err := s.store.PTRecipientsForChange(ctx, current.ID, editorUserID)
		if err == nil && len(recipients) > 0 {
			afterShares := splitSharesTx(ctx, tx, current.ID)
			diff := buildDiff(current, updated)
			for _, recipient := range recipients {
				if err := s.notifyChangedTx(ctx, tx, recipient, editorUserID,
					current, updated, beforeShares, afterShares, diff); err != nil {
					return nil, err
				}
			}
		}
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
	if err := s.store.AssertWritable(ctx, projectID, "resolve"); err != nil {
		return nil, err
	}
	member, err := s.store.AssertCanWrite(ctx, projectID, callerUserID)
	if err != nil {
		return nil, err
	}
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
