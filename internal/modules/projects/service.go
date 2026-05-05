package projects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// --- Service / wiring ---

type Service struct {
	store  *Store
	notifs *notifications.Service
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

func (s *Service) WithNotificationService(n *notifications.Service) { s.notifs = n }

// --- Project CRUD ---

func (s *Service) Create(ctx context.Context, ownerUserID uuid.UUID, req CreateProjectRequest) (*Project, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	p, err := s.store.CreateInTx(ctx, tx, ownerUserID, req)
	if err != nil {
		return nil, err
	}

	// Owner gets an auto active member row with role=owner.
	var ownerName string
	_ = tx.QueryRow(ctx, `SELECT display_name FROM users WHERE id = $1`, ownerUserID).Scan(&ownerName)
	if ownerName == "" {
		ownerName = "Owner"
	}
	if _, err := s.store.InsertMemberTx(ctx, tx,
		p.ID, &ownerUserID, ownerName, RoleOwner, MemberStatusActive, ownerUserID, nil,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	// Re-fetch via GetByID so `members_count` is populated (the in-tx insert
	// returned the bare project before the owner-member row landed).
	return s.store.GetByID(ctx, p.ID)
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, f ListFilter) (*ListResponse, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PerPage <= 0 {
		f.PerPage = 20
	}
	if f.PerPage > 100 {
		f.PerPage = 100
	}
	rows, total, err := s.store.ListForUser(ctx, userID, f)
	if err != nil {
		return nil, err
	}
	totalPages := (total + f.PerPage - 1) / f.PerPage
	if totalPages == 0 {
		totalPages = 1
	}
	return &ListResponse{
		Data: rows,
		Pagination: Pagination{
			Page: f.Page, PerPage: f.PerPage, Total: total, TotalPages: totalPages,
		},
	}, nil
}

func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (*Project, error) {
	return s.store.GetByIDForCaller(ctx, userID, id)
}

func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, req UpdateProjectRequest) (*Project, error) {
	p, err := s.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.OwnerUserID != userID {
		return nil, ErrNotOwner
	}
	return s.store.Update(ctx, userID, id, req)
}

func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	p, err := s.store.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if p.OwnerUserID != userID {
		return ErrNotOwner
	}
	hasTx, err := s.store.HasTransactions(ctx, id)
	if err != nil {
		return err
	}
	if hasTx {
		return ErrProjectHasTxs
	}
	return s.store.Delete(ctx, userID, id)
}

func (s *Service) Leave(ctx context.Context, userID, projectID uuid.UUID) error {
	p, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return err
	}
	if p.OwnerUserID == userID {
		return ErrOwnerCannotLeave
	}
	// Find caller's member row.
	var memberID uuid.UUID
	if err := s.store.db.QueryRow(ctx,
		`SELECT id FROM project_members WHERE project_id = $1 AND user_id = $2 AND status = 'active'`,
		projectID, userID).Scan(&memberID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotMember
		}
		return fmt.Errorf("find member: %w", err)
	}
	_, err = s.store.SoftRemoveMember(ctx, projectID, memberID, userID)
	return err
}

func (s *Service) TransferOwnership(ctx context.Context, callerUserID, projectID, newOwnerUserID uuid.UUID) error {
	p, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return err
	}
	if p.OwnerUserID != callerUserID {
		return ErrNotOwner
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := s.store.TransferOwnershipTx(ctx, tx, projectID, callerUserID, newOwnerUserID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// --- Members ---

func (s *Service) ListMembers(ctx context.Context, userID, projectID uuid.UUID) ([]ProjectMember, error) {
	if _, err := s.store.GetByIDForCaller(ctx, userID, projectID); err != nil {
		return nil, err
	}
	return s.store.ListMembers(ctx, projectID)
}

// AddMember dispatches across the 2 variants:
//
//	(a) Email + DisplayName            → invite-by-email
//	(b) AdHoc=true + DisplayName       → ad-hoc
//
// The from-contact variant was removed when contacts (user-scoped) were
// dropped from project_members. FE looks up the contact's linked_user_id
// locally and either invites by email or adds ad-hoc.
func (s *Service) AddMember(ctx context.Context, callerUserID, projectID uuid.UUID, req AddMemberRequest) (*ProjectMember, error) {
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
	if role == RoleOwner {
		return nil, errors.New("owner role is set via transfer-ownership; cannot be assigned directly")
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	var member *ProjectMember

	switch {
	case req.Email != nil && req.DisplayName != nil:
		member, err = s.addMemberByEmailTx(ctx, tx, projectID, callerUserID, *req.DisplayName, *req.Email, role, req.IconCode)
	case req.AdHoc && req.DisplayName != nil:
		member, err = s.store.InsertMemberTx(ctx, tx,
			projectID, nil, *req.DisplayName, role, MemberStatusActive, callerUserID, req.IconCode,
		)
	default:
		return nil, errors.New("AddMember requires one of: (email + display_name), or (ad_hoc=true + display_name)")
	}
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return member, nil
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
		projectName := projectNameOrEmpty(ctx, tx, projectID)
		actorName := userDisplayOrEmpty(ctx, tx, callerUserID)
		_ = s.notifs.DispatchProjectInvite(ctx, tx, *matchedUser, callerUserID,
			notifications.ProjectInvitePayload{
				ProjectMemberID:    member.ID,
				ProjectID:          projectID,
				ProjectName:        projectName,
				InviterUserID:      callerUserID,
				InviterDisplayName: actorName,
				Role:               role,
			})
	}
	return member, nil
}

// UpdateMember changes role. Owner-only operation.
func (s *Service) UpdateMember(
	ctx context.Context, callerUserID, projectID, memberID uuid.UUID, req UpdateMemberRequest,
) (*ProjectMember, error) {
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
	if req.Role != nil && *req.Role == RoleOwner {
		return nil, errors.New("use transfer-ownership to change owner")
	}
	if req.Role != nil {
		if _, err := s.store.UpdateMemberRole(ctx, projectID, memberID, *req.Role, callerUserID); err != nil {
			return nil, err
		}
	}
	if req.IconCode != nil {
		if _, err := s.store.UpdateMemberIconCode(ctx, projectID, memberID, req.IconCode, callerUserID); err != nil {
			return nil, err
		}
	}
	return s.store.GetMember(ctx, memberID)
}

// RemoveMember soft-removes a member. Owner-only; cannot remove the owner.
func (s *Service) RemoveMember(ctx context.Context, callerUserID, projectID, memberID uuid.UUID) error {
	p, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return err
	}
	if p.OwnerUserID != callerUserID {
		return ErrNotOwner
	}
	if err := s.store.AssertWritable(ctx, projectID, "member_crud"); err != nil {
		return err
	}
	target, err := s.store.GetMember(ctx, memberID)
	if err != nil {
		return err
	}
	if target.Role == RoleOwner {
		return ErrCannotRemoveOwner
	}
	_, err = s.store.SoftRemoveMember(ctx, projectID, memberID, callerUserID)
	return err
}

// RequestLink re-fires the project_invite notification for an existing
// pending member. Post-migration 27, project_members has no contact_id, so
// there's no email to look up server-side. The owner re-invites by removing
// the pending member and adding again with the corrected email.
func (s *Service) RequestLink(ctx context.Context, callerUserID, projectID, memberID uuid.UUID) error {
	if s.notifs == nil {
		return errors.New("notifications module not wired")
	}
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
	if target.UserID != nil {
		return errors.New("member is already linked")
	}
	// No email source on the member row anymore — silent void, same response
	// shape as a miss. Owner can re-invite by add-member with the email.
	return nil
}

// AcceptLinkRequest accepts a project_invite notification — populates
// project_members.user_id for the recipient.
func (s *Service) AcceptLinkRequest(ctx context.Context, userID, notificationID uuid.UUID) (*ProjectMember, error) {
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
		return nil, fmt.Errorf("notification is not a project_invite")
	}

	var p notifications.ProjectInvitePayload
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}

	member, err := s.store.GetMemberTx(ctx, tx, p.ProjectMemberID)
	if err != nil {
		return nil, err
	}
	if member.UserID != nil && *member.UserID != userID {
		return nil, ErrMemberAlreadyExists
	}
	if member.UserID != nil && *member.UserID == userID {
		// Idempotent re-accept.
		_, _ = s.notifs.MarkActionedTx(ctx, tx, userID, notificationID)
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit: %w", err)
		}
		return member, nil
	}

	updated, err := s.store.LinkMemberToUserTx(ctx, tx, p.ProjectMemberID, userID)
	if err != nil {
		return nil, err
	}
	if _, err := s.notifs.MarkActionedTx(ctx, tx, userID, notificationID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return updated, nil
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
		return fmt.Errorf("notification is not a project_invite")
	}
	if _, err := s.notifs.MarkDismissedTx(ctx, tx, userID, notificationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// --- Public exports for other modules ---

// GetMember exposes the row to other modules for project_member_id ownership
// validation.
func (s *Service) GetMember(ctx context.Context, memberID uuid.UUID) (*ProjectMember, error) {
	return s.store.GetMember(ctx, memberID)
}

// IsActiveMember is a fast yes/no for notifications dispatchers and other
// cross-module callers.
func (s *Service) IsActiveMember(ctx context.Context, projectID, userID uuid.UUID) (bool, error) {
	return s.store.IsActiveMember(ctx, projectID, userID)
}

// OwnerUserID returns just the owner id — used by project_tx_changed
// recipient resolution.
func (s *Service) OwnerUserID(ctx context.Context, projectID uuid.UUID) (uuid.UUID, error) {
	p, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return uuid.Nil, err
	}
	return p.OwnerUserID, nil
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

