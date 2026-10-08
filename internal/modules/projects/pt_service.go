package projects

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
)

// CreateProjectTransaction inserts a parent project_transaction row plus
// any split children, all atomic.
func (s *Service) CreateProjectTransaction(
	ctx context.Context, callerUserID, projectID uuid.UUID, req CreateProjectTransactionRequest,
) (*ProjectTransaction, error) {
	if err := s.store.AssertWritable(ctx, projectID, "create_pt"); err != nil {
		return nil, err
	}
	if _, err := s.store.AssertCanWrite(ctx, projectID, callerUserID); err != nil {
		return nil, err
	}

	// Validate transaction_member_id belongs to this project.
	member, err := s.store.GetMember(ctx, req.TransactionMemberID)
	if err != nil {
		return nil, err
	}
	if member.ProjectID != projectID {
		return nil, ErrMemberNotInProject
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

	// Contract §5. My own row → my personal copy when I switched that on
	// (no notification: I did it). Someone else recorded it as mine →
	// project_tx_recorded_for_you, whose auto copies it for me.
	if s.notifs != nil && member.UserID != nil {
		if *member.UserID == callerUserID {
			if s.txs != nil {
				st, err := s.notifs.SettingsTx(ctx, tx, callerUserID)
				if err == nil && st.AutoResolveOwnInProjects {
					if _, err := s.copyToPersonalTx(ctx, tx, callerUserID, member.ID, pt); err != nil {
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
	return pt, nil
}

// UpdateProjectTransaction edits a parent row and (optionally) full-replaces
// its split children. Children cannot be edited directly.
func (s *Service) UpdateProjectTransaction(
	ctx context.Context, callerUserID, projectID, ptID uuid.UUID, req UpdateProjectTransactionRequest,
) (*ProjectTransaction, error) {
	if err := s.store.AssertWritable(ctx, projectID, "edit_pt"); err != nil {
		return nil, err
	}
	current, err := s.store.GetPT(ctx, ptID)
	if err != nil {
		return nil, err
	}
	if current.ParentProjectTransactionID != nil {
		return nil, ErrPTIsChild
	}
	// Members can edit any project_tx (full edit rights, see plan §1).
	if _, err := s.store.AssertCanWrite(ctx, projectID, callerUserID); err != nil {
		return nil, err
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	beforeShares := splitSharesTx(ctx, tx, ptID)
	updated, err := s.store.UpdatePTTx(ctx, tx, projectID, ptID, callerUserID, req)
	if err != nil {
		return nil, err
	}

	if s.notifs != nil {
		recipients, err := s.store.PTRecipientsForChange(ctx, ptID, callerUserID)
		if err == nil && len(recipients) > 0 {
			afterShares := splitSharesTx(ctx, tx, ptID)
			diff := buildDiff(current, updated)
			for _, recipient := range recipients {
				if err := s.notifyChangedTx(ctx, tx, recipient, callerUserID,
					current, updated, beforeShares, afterShares, diff); err != nil {
					return nil, err
				}
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return updated, nil
}

// DeleteProjectTransaction removes a parent (FK-cascading its children) or
// a single child split row.
func (s *Service) DeleteProjectTransaction(
	ctx context.Context, callerUserID, projectID, ptID uuid.UUID,
) error {
	if err := s.store.AssertWritable(ctx, projectID, "edit_pt"); err != nil {
		return err
	}
	if _, err := s.store.GetPT(ctx, ptID); err != nil {
		return err
	}
	if _, err := s.store.AssertCanWrite(ctx, projectID, callerUserID); err != nil {
		return err
	}

	// Capture recipients BEFORE delete (FK cascade nukes the children).
	recipients, _ := s.store.PTRecipientsForChange(ctx, ptID, callerUserID)

	if err := s.store.DeletePT(ctx, projectID, ptID); err != nil {
		return err
	}

	if s.notifs != nil && len(recipients) > 0 {
		tx, err := s.store.Pool().Begin(ctx)
		if err == nil {
			editorName := userDisplayOrEmpty(ctx, tx, callerUserID)
			for _, recipient := range recipients {
				_ = s.notifs.DispatchProjectTxChanged(ctx, tx, recipient, callerUserID,
					notifications.ProjectTxChangedPayload{
						ProjectTransactionID: ptID,
						ProjectID:            projectID,
						EditorUserID:         callerUserID,
						EditorDisplayName:    editorName,
						ChangeKind:           "deleted",
					}, false)
			}
			_ = tx.Commit(ctx)
		}
	}
	return nil
}

func (s *Service) ListPT(ctx context.Context, callerUserID, projectID uuid.UUID, page, perPage int) (*ListPTResponse, error) {
	if _, err := s.store.GetByIDForCaller(ctx, callerUserID, projectID); err != nil {
		return nil, err
	}
	if page <= 0 {
		page = 1
	}
	if perPage <= 0 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}
	rows, total, err := s.store.ListPT(ctx, projectID, page, perPage)
	if err != nil {
		return nil, err
	}
	totalPages := (total + perPage - 1) / perPage
	if totalPages == 0 {
		totalPages = 1
	}
	return &ListPTResponse{
		Data: rows,
		Pagination: Pagination{Page: page, PerPage: perPage, Total: total, TotalPages: totalPages},
	}, nil
}

// ToggleMark adds/removes the caller's project_member id from the row's
// `marks` array. Idempotent.
func (s *Service) ToggleMark(
	ctx context.Context, callerUserID, projectID, ptID uuid.UUID, marked bool,
) (*ProjectTransaction, error) {
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
	return s.store.ToggleMark(ctx, projectID, ptID, member.ID, marked)
}

func (s *Service) Summary(ctx context.Context, callerUserID, projectID uuid.UUID) (*SummaryResponse, error) {
	if _, err := s.store.GetByIDForCaller(ctx, callerUserID, projectID); err != nil {
		return nil, err
	}
	return s.store.SummaryAggregate(ctx, projectID, callerUserID)
}

// buildDiff produces a small map of changed numeric/string fields for the
// project_tx_changed payload. Only fields that actually changed are included.
func buildDiff(before, after *ProjectTransaction) map[string][2]interface{} {
	diff := make(map[string][2]interface{})
	if before.Amount != after.Amount {
		diff["amount"] = [2]interface{}{before.Amount, after.Amount}
	}
	if before.Date != after.Date {
		diff["date"] = [2]interface{}{before.Date, after.Date}
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
