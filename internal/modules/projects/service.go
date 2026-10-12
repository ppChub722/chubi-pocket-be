package projects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// --- Service / wiring ---

type Service struct {
	store  *Store
	notifs *notifications.Service
	txs    *transactions.Service
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

func (s *Service) WithNotificationService(n *notifications.Service) { s.notifs = n }

// WithTransactionsService wires the personal-book paths (quick create, bills,
// copies, linked money). Direct import is safe — transactions does not
// import projects.
func (s *Service) WithTransactionsService(t *transactions.Service) { s.txs = t }

// --- Event CRUD ---

// Create — POST /projects: the event, my owner member row and my personal
// tags in one tx; returns the page-ready detail.
func (s *Service) Create(ctx context.Context, ownerUserID uuid.UUID, req CreateProjectRequest) (*EventDetail, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	p, err := s.createTx(ctx, tx, ownerUserID, req)
	if err != nil {
		return nil, err
	}
	if err := setMyTagsTx(ctx, tx, p.ID, ownerUserID, req.TagIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.detail(ctx, ownerUserID, p.ID, 1, 20)
}

// createTx — the project + the caller's owner member row.
func (s *Service) createTx(ctx context.Context, tx pgx.Tx, ownerUserID uuid.UUID, req CreateProjectRequest) (*Project, error) {
	p, err := s.store.CreateInTx(ctx, tx, ownerUserID, req)
	if err != nil {
		return nil, err
	}
	ownerName := userDisplayOrEmpty(ctx, tx, ownerUserID)
	if ownerName == "" {
		ownerName = "Owner"
	}
	if _, err := s.store.InsertMemberTx(ctx, tx,
		p.ID, &ownerUserID, ownerName, RoleOwner, MemberStatusActive, ownerUserID, nil,
	); err != nil {
		return nil, err
	}
	return p, nil
}

// List — GET /projects.
func (s *Service) List(ctx context.Context, userID uuid.UUID, f ListFilter) (*EventList, error) {
	f.Page, f.PerPage = clampPage(f.Page, f.PerPage)
	ids, total, err := s.store.ListIDs(ctx, userID, f)
	if err != nil {
		return nil, err
	}
	events, err := loadEvents(ctx, s.store.db, userID, ids)
	if err != nil {
		return nil, err
	}
	out := &EventList{Data: make([]Event, 0, len(ids)), Pagination: shared.NewPage(f.Page, f.PerPage, total)}
	for _, id := range ids {
		out.Data = append(out.Data, events[id])
	}
	return out, nil
}

// Get — GET /projects/:id (the Event row).
func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (*Event, error) {
	if _, err := s.store.GetByIDForCaller(ctx, userID, id); err != nil {
		return nil, err
	}
	e, err := loadEvent(ctx, s.store.db, userID, id)
	return &e, err
}

// Detail — GET /projects/:id/detail.
func (s *Service) Detail(ctx context.Context, userID, id uuid.UUID, page, perPage int) (*EventDetail, error) {
	return s.detail(ctx, userID, id, page, perPage)
}

// Summary — GET /projects/:id/summary.
func (s *Service) Summary(ctx context.Context, userID, id uuid.UUID) (*EventSummary, error) {
	if _, err := s.store.GetByIDForCaller(ctx, userID, id); err != nil {
		return nil, err
	}
	e, err := loadEvent(ctx, s.store.db, userID, id)
	if err != nil {
		return nil, err
	}
	sum, err := loadSummary(ctx, s.store.db, userID, id, e)
	return &sum, err
}

// Update — PUT /projects/:id. The owner edits the event; MY tags (tag_ids)
// are any member's own.
func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, req UpdateProjectRequest) (*EventPatch, error) {
	p, err := s.store.GetByIDForCaller(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if p.OwnerUserID != userID && !req.onlyMyTags() {
		return nil, ErrNotOwner
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if !req.onlyMyTags() {
		if err := s.store.UpdateTx(ctx, tx, userID, id, p, req); err != nil {
			return nil, err
		}
	}
	if req.has("tag_ids") {
		if err := setMyTagsTx(ctx, tx, id, userID, req.TagIDs); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.patch(ctx, userID, id, nil, nil, nil)
}

// Delete — DELETE /projects/:id (owner). Other members' personal
// transactions stay in their books, unlinked; mine too unless deleteLinked
// (owner 2026-10-13: "ยืนยันและลบ x รายการ"), then they go (balances back).
func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID, deleteLinked bool) (*EventDeleteResult, error) {
	p, err := s.store.GetByIDForCaller(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if p.OwnerUserID != userID {
		return nil, ErrNotOwner
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	out := &EventDeleteResult{ID: id}
	var touched []uuid.UUID
	if deleteLinked {
		if s.txs == nil {
			return nil, errors.New("transactions module not wired")
		}
		rows, err := tx.Query(ctx, `SELECT id FROM transactions WHERE project_id = $1 AND user_id = $2`, id, userID)
		if err != nil {
			return nil, fmt.Errorf("linked txs: %w", err)
		}
		var ids []uuid.UUID
		for rows.Next() {
			var tid uuid.UUID
			if err := rows.Scan(&tid); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, tid)
		}
		rows.Close()
		// Unlink first (the CHECK needs both columns cleared together).
		if _, err := tx.Exec(ctx, `UPDATE transactions SET project_id = NULL, source_project_transaction_id = NULL
			WHERE id = ANY($1)`, ids); err != nil {
			return nil, fmt.Errorf("unlink: %w", err)
		}
		for _, tid := range ids {
			acc, err := s.txs.DeleteInTx(ctx, tx, userID, tid)
			if err != nil {
				return nil, err
			}
			touched = append(touched, acc...)
		}
		out.DeletedTransactionCount = len(ids)
	}
	if err := s.store.DeleteTx(ctx, tx, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	var err2 error
	if out.AccountBalances, err2 = shared.LoadWalletBalances(ctx, s.store.db, uniqueIDs(touched)); err2 != nil {
		return nil, err2
	}
	return out, nil
}

// Leave — POST /projects/:id/leave.
func (s *Service) Leave(ctx context.Context, userID, projectID uuid.UUID) (*DeletedResult, error) {
	p, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if p.OwnerUserID == userID {
		return nil, ErrOwnerCannotLeave
	}
	var memberID uuid.UUID
	if err := s.store.db.QueryRow(ctx,
		`SELECT id FROM project_members WHERE project_id = $1 AND user_id = $2 AND status = 'active'`,
		projectID, userID).Scan(&memberID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotMember
		}
		return nil, fmt.Errorf("find member: %w", err)
	}
	if _, err := s.store.SoftRemoveMember(ctx, projectID, memberID, userID); err != nil {
		return nil, err
	}
	return &DeletedResult{ID: projectID}, nil
}

// TransferOwnership — POST /projects/:id/transfer-ownership.
func (s *Service) TransferOwnership(ctx context.Context, callerUserID, projectID, newOwnerUserID uuid.UUID) (*EventPatch, error) {
	p, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if p.OwnerUserID != callerUserID {
		return nil, ErrNotOwner
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := s.store.TransferOwnershipTx(ctx, tx, projectID, callerUserID, newOwnerUserID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.patch(ctx, callerUserID, projectID, nil, nil, nil)
}

// --- Members ---

func (s *Service) ListMembers(ctx context.Context, userID, projectID uuid.UUID) (*EventMemberList, error) {
	if _, err := s.store.GetByIDForCaller(ctx, userID, projectID); err != nil {
		return nil, err
	}
	m, err := loadMembers(ctx, s.store.db, userID, projectID)
	if err != nil {
		return nil, err
	}
	return &EventMemberList{Data: m}, nil
}

// AddMember — (a) ad_hoc + display_name; (b) email + display_name (invite).
// Owner only.
func (s *Service) AddMember(ctx context.Context, callerUserID, projectID uuid.UUID, req AddMemberRequest) (*EventPatch, error) {
	p, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if p.OwnerUserID != callerUserID {
		return nil, ErrNotOwner
	}
	if err := s.store.AssertWritable(ctx, projectID, "member_crud"); err != nil {
		return nil, err
	}
	role := req.Role
	if role == "" {
		role = RoleContributor
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	switch {
	case req.Email != nil && req.DisplayName != nil:
		_, err = s.addMemberByEmailTx(ctx, tx, projectID, callerUserID, *req.DisplayName, *req.Email, role, req.IconCode)
	case req.AdHoc && req.DisplayName != nil:
		_, err = s.store.InsertMemberTx(ctx, tx,
			projectID, nil, *req.DisplayName, role, MemberStatusActive, callerUserID, req.IconCode,
		)
	default:
		return nil, ErrBadMemberRequest
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.patch(ctx, callerUserID, projectID, nil, nil, nil)
}

func (s *Service) addMemberByEmailTx(
	ctx context.Context, tx pgx.Tx, projectID, callerUserID uuid.UUID,
	displayName, email, role string, iconCode *shared.IconCode,
) (*ProjectMember, error) {
	// Always-execute lookup. Privacy preservation.
	var matchedUser *uuid.UUID
	if s.notifs != nil {
		var err error
		matchedUser, err = s.notifs.LookupUserByEmail(ctx, email)
		if err != nil {
			return nil, err
		}
	}
	member, err := s.store.InsertMemberTx(ctx, tx,
		projectID, nil, displayName, role, MemberStatusPending, callerUserID, iconCode,
	)
	if err != nil {
		return nil, err
	}
	if matchedUser != nil && *matchedUser != callerUserID && s.notifs != nil {
		_ = s.notifs.DispatchProjectInvite(ctx, tx, *matchedUser, callerUserID,
			notifications.ProjectInvitePayload{
				ProjectMemberID:    member.ID,
				ProjectID:          projectID,
				ProjectName:        projectNameOrEmpty(ctx, tx, projectID),
				InviterUserID:      callerUserID,
				InviterDisplayName: userDisplayOrEmpty(ctx, tx, callerUserID),
				Role:               role,
			})
	}
	return member, nil
}

// UpdateMember changes role / icon. Owner-only operation.
func (s *Service) UpdateMember(
	ctx context.Context, callerUserID, projectID, memberID uuid.UUID, req UpdateMemberRequest,
) (*EventPatch, error) {
	p, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if p.OwnerUserID != callerUserID {
		return nil, ErrNotOwner
	}
	if err := s.store.AssertWritable(ctx, projectID, "member_crud"); err != nil {
		return nil, err
	}
	target, err := s.store.GetMember(ctx, memberID)
	if err != nil {
		return nil, err
	}
	if target.ProjectID != projectID {
		return nil, ErrMemberNotInProject
	}
	if req.Role != nil {
		// Contract §6b: the owner's own row can't be demoted here.
		if target.Role == RoleOwner {
			return nil, ErrCannotChangeOwner
		}
		if _, err := s.store.UpdateMemberRole(ctx, projectID, memberID, *req.Role, callerUserID); err != nil {
			return nil, err
		}
	}
	if req.IconCode != nil {
		if _, err := s.store.UpdateMemberIconCode(ctx, projectID, memberID, req.IconCode, callerUserID); err != nil {
			return nil, err
		}
	}
	return s.patch(ctx, callerUserID, projectID, nil, nil, nil)
}

// RemoveMember soft-removes a member. Owner-only; cannot remove the owner.
func (s *Service) RemoveMember(ctx context.Context, callerUserID, projectID, memberID uuid.UUID) (*EventPatch, error) {
	p, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if p.OwnerUserID != callerUserID {
		return nil, ErrNotOwner
	}
	if err := s.store.AssertWritable(ctx, projectID, "member_crud"); err != nil {
		return nil, err
	}
	target, err := s.store.GetMember(ctx, memberID)
	if err != nil {
		return nil, err
	}
	if target.ProjectID != projectID {
		return nil, ErrMemberNotInProject
	}
	if target.Role == RoleOwner {
		return nil, ErrCannotRemoveOwner
	}
	if _, err := s.store.SoftRemoveMember(ctx, projectID, memberID, callerUserID); err != nil {
		return nil, err
	}
	return s.patch(ctx, callerUserID, projectID, nil, nil, nil)
}

// RequestLink — re-send a pending member's invite. Members carry no email
// since migration 27, so there is nothing to re-send server-side: a pending
// member is acknowledged, anything else is refused.
func (s *Service) RequestLink(ctx context.Context, callerUserID, projectID, memberID uuid.UUID) error {
	p, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return err
	}
	if p.OwnerUserID != callerUserID {
		return ErrNotOwner
	}
	target, err := s.store.GetMember(ctx, memberID)
	if err != nil {
		return err
	}
	if target.ProjectID != projectID {
		return ErrMemberNotInProject
	}
	if target.UserID != nil || target.Status != MemberStatusPending {
		return ErrNotPending
	}
	return nil
}

// ErrNotPending — request-link on a member that isn't a pending invite.
var ErrNotPending = errors.New("this member isn't waiting for an invite")

// AcceptLinkRequest accepts a project_invite notification — the pending
// member row becomes the caller's. Returns the event detail.
func (s *Service) AcceptLinkRequest(ctx context.Context, userID, notificationID uuid.UUID) (*EventDetail, error) {
	if s.notifs == nil {
		return nil, errors.New("notifications module not wired")
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	n, err := s.notifs.GetByIDForCallerTx(ctx, tx, userID, notificationID)
	if err != nil {
		return nil, err
	}
	if n.Type != notifications.TypeProjectInvite {
		return nil, ErrLinkRequestNotForYou
	}
	var p notifications.ProjectInvitePayload
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	member, err := s.store.GetMemberTx(ctx, tx, p.ProjectMemberID)
	if err != nil {
		return nil, err
	}
	switch {
	case member.UserID != nil && *member.UserID != userID:
		return nil, ErrMemberAlreadyExists
	case member.UserID == nil:
		if _, err := s.store.LinkMemberToUserTx(ctx, tx, p.ProjectMemberID, userID); err != nil {
			return nil, err
		}
	}
	if _, err := s.notifs.MarkActionedTx(ctx, tx, userID, notificationID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.detail(ctx, userID, member.ProjectID, 1, 20)
}

func (s *Service) RejectLinkRequest(ctx context.Context, userID, notificationID uuid.UUID) error {
	if s.notifs == nil {
		return errors.New("notifications module not wired")
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	n, err := s.notifs.GetByIDForCallerTx(ctx, tx, userID, notificationID)
	if err != nil {
		return err
	}
	if n.Type != notifications.TypeProjectInvite {
		return ErrLinkRequestNotForYou
	}
	// Contract §6b: the invite's still-unlinked member row stops showing as
	// "pending" — it becomes a left row (its history, if any, stays).
	var p notifications.ProjectInvitePayload
	if err := json.Unmarshal(n.Payload, &p); err == nil && p.ProjectMemberID != uuid.Nil {
		if _, err := tx.Exec(ctx, `
			UPDATE project_members SET status = 'left', left_at = NOW(), updated_by_user_id = $1
			WHERE id = $2 AND user_id IS NULL AND status = 'pending'`,
			userID, p.ProjectMemberID); err != nil {
			return fmt.Errorf("drop pending member: %w", err)
		}
	}
	if _, err := s.notifs.MarkDismissedTx(ctx, tx, userID, notificationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// --- Public exports for other modules ---

// GetMember exposes the row to other modules.
func (s *Service) GetMember(ctx context.Context, memberID uuid.UUID) (*ProjectMember, error) {
	return s.store.GetMember(ctx, memberID)
}

// IsActiveMember is a fast yes/no for other modules.
func (s *Service) IsActiveMember(ctx context.Context, projectID, userID uuid.UUID) (bool, error) {
	return s.store.IsActiveMember(ctx, projectID, userID)
}

// --- Helpers ---

func projectNameOrEmpty(ctx context.Context, tx pgx.Tx, projectID uuid.UUID) string {
	var name string
	_ = tx.QueryRow(ctx, `SELECT name FROM projects WHERE id = $1`, projectID).Scan(&name)
	return name
}

func userDisplayOrEmpty(ctx context.Context, tx pgx.Tx, userID uuid.UUID) string {
	var name string
	_ = tx.QueryRow(ctx, `SELECT display_name FROM users WHERE id = $1`, userID).Scan(&name)
	return name
}
