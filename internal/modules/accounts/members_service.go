package accounts

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

func (s *Service) WithNotificationService(n *notifications.Service) { s.notifs = n }

func (s *Service) MembersResult(ctx context.Context, callerUserID, accountID uuid.UUID) (*MembersResult, error) {
	a, err := s.store.GetByID(ctx, callerUserID, accountID)
	if errors.Is(err, ErrAccountNotFound) {
		return &MembersResult{Members: []MemberHistoryView{}}, nil
	}
	if err != nil {
		return nil, err
	}
	members, err := s.store.ListMembersByAccount(ctx, callerUserID, accountID)
	if err != nil {
		return nil, err
	}
	return &MembersResult{Account: a, Members: members}, nil
}

func (s *Service) InviteMember(
	ctx context.Context, callerUserID, accountID uuid.UUID, req InviteMemberRequest,
) (*AccountMember, error) {
	if s.notifs == nil {
		return nil, errors.New("notifications module not wired")
	}
	a, err := s.requireActiveMember(ctx, callerUserID, accountID)
	if err != nil {
		return nil, err
	}
	if !a.CanEdit {
		return nil, ErrNotOwner
	}

	targetID, err := s.inviteTarget(ctx, callerUserID, req)
	if err != nil {
		return nil, err
	}
	if *targetID == callerUserID {
		return nil, ErrAlreadyMember
	}
	if live, err := s.store.HasLiveMembership(ctx, accountID, *targetID); err != nil {
		return nil, err
	} else if live {
		return nil, ErrAlreadyMember
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	member, err := s.store.InsertMemberTx(ctx, tx,
		accountID, *targetID, MemberRoleMember, ReportScopeNone, nil, callerUserID,
	)
	if err != nil {
		return nil, err
	}

	var inviterName string
	_ = tx.QueryRow(ctx, `SELECT display_name FROM users WHERE id = $1`, callerUserID).Scan(&inviterName)
	if err := s.notifs.DispatchAccountInvite(ctx, tx, *targetID, callerUserID,
		notifications.AccountInvitePayload{
			AccountMemberID:    member.ID,
			AccountID:          accountID,
			AccountName:        a.Name,
			InviterUserID:      callerUserID,
			InviterDisplayName: inviterName,
		}); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return member, nil
}

func (s *Service) inviteTarget(
	ctx context.Context, callerUserID uuid.UUID, req InviteMemberRequest,
) (*uuid.UUID, error) {
	switch {
	case (req.ContactID == nil) == (req.Email == nil):
		return nil, ErrInviteTarget
	case req.ContactID != nil:
		var (
			linked *uuid.UUID
			status string
		)
		err := s.store.db.QueryRow(ctx,
			`SELECT linked_user_id, status FROM contacts WHERE id = $1 AND user_id = $2`,
			*req.ContactID, callerUserID).Scan(&linked, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrContactNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("invite contact: %w", err)
		}
		if status == "archived" {
			return nil, ErrContactArchived
		}
		if linked == nil {
			return nil, ErrContactNotLinked
		}
		return linked, nil
	default:
		targetID, err := s.notifs.LookupUserByEmail(ctx, *req.Email)
		if err != nil {
			return nil, err
		}
		if targetID == nil {
			return nil, ErrUserNotFound
		}
		return targetID, nil
	}
}

// RemoveMember sets left_at (never hard-deletes).
func (s *Service) RemoveMember(
	ctx context.Context, callerUserID, accountID, memberID uuid.UUID,
) (*AccountMember, error) {
	a, err := s.requireActiveMember(ctx, callerUserID, accountID)
	if err != nil {
		return nil, err
	}
	target, err := s.store.GetMember(ctx, memberID)
	if err != nil {
		return nil, err
	}
	if target.AccountID != accountID || target.LeftAt != nil {
		return nil, ErrMemberNotFound
	}

	callerIsOwner := a.CanEdit
	if target.Role == MemberRoleOwner {
		return nil, ErrOwnerMustTransfer
	}
	if target.UserID != callerUserID && !callerIsOwner {
		return nil, ErrNotOwner
	}

	return s.store.SoftRemoveMember(ctx, accountID, memberID, callerUserID)
}

func (s *Service) TransferOwnership(
	ctx context.Context, callerUserID, accountID, newOwnerUserID uuid.UUID,
) error {
	a, err := s.requireActiveMember(ctx, callerUserID, accountID)
	if err != nil {
		return err
	}
	if !a.CanEdit {
		return ErrNotOwner
	}
	if newOwnerUserID == callerUserID {
		return nil // no-op self-transfer
	}
	active, err := s.store.IsActiveMember(ctx, accountID, newOwnerUserID)
	if err != nil {
		return err
	}
	if !active {
		return ErrMemberNotFound
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := s.store.TransferOwnershipTx(ctx, tx, accountID, callerUserID, newOwnerUserID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) UpdateReportScope(
	ctx context.Context, callerUserID, accountID uuid.UUID, scope string,
) (*Account, error) {
	m, err := s.store.GoverningMembership(ctx, accountID, callerUserID)
	if err != nil {
		if errors.Is(err, ErrMemberNotFound) {
			return nil, ErrNotMember
		}
		return nil, err
	}
	if m.LeftAt != nil && scope == ReportScopeAll {
		return nil, ErrScopeNotAllowed
	}
	updated, err := s.store.UpdateReportScope(ctx, m.ID, scope, callerUserID)
	if err != nil {
		return nil, err
	}
	if m.LeftAt == nil {
		return s.getDecorated(ctx, callerUserID, accountID, "")
	}
	a, err := s.store.getAnyMember(ctx, callerUserID, accountID)
	if err != nil {
		return nil, err
	}
	a.MyReportScope = updated.ReportScope
	return a, nil
}

func (s *Service) AcceptInvite(ctx context.Context, userID, notificationID uuid.UUID) (*AcceptInviteResult, error) {
	if s.notifs == nil {
		return nil, errors.New("notifications module not wired")
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	n, err := s.notifs.GetByIDForCallerTx(ctx, tx, userID, notificationID)
	if err != nil {
		return nil, err
	}
	if n.Type != notifications.TypeAccountInvite {
		return nil, ErrNotInvite
	}
	var p notifications.AccountInvitePayload
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}

	member, err := s.store.GetMemberTx(ctx, tx, p.AccountMemberID)
	if err != nil {
		return nil, err
	}
	if member.UserID != userID {
		return nil, ErrMemberNotFound
	}
	if member.LeftAt != nil {
		return nil, ErrMemberNotFound
	}
	if member.JoinedAt != nil {
		_, _ = s.notifs.MarkActionedTx(ctx, tx, userID, notificationID)
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit: %w", err)
		}
		return s.inviteResult(ctx, userID, notificationID, member.AccountID)
	}

	activeBefore, err := s.store.CountActiveMembersTx(ctx, tx, member.AccountID)
	if err != nil {
		return nil, err
	}
	activated, err := s.store.ActivateMemberTx(ctx, tx, member.ID, userID)
	if err != nil {
		return nil, err
	}
	if activeBefore == 1 {
		if err := s.store.ResetScopesToNoneTx(ctx, tx, member.AccountID, userID); err != nil {
			return nil, err
		}
		activated.ReportScope = ReportScopeNone
	}
	if _, err := s.notifs.MarkActionedTx(ctx, tx, userID, notificationID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.inviteResult(ctx, userID, notificationID, activated.AccountID)
}

func (s *Service) RejectInvite(ctx context.Context, userID, notificationID uuid.UUID) (*NotificationActionResult, error) {
	if s.notifs == nil {
		return nil, errors.New("notifications module not wired")
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	n, err := s.notifs.GetByIDForCallerTx(ctx, tx, userID, notificationID)
	if err != nil {
		return nil, err
	}
	if n.Type != notifications.TypeAccountInvite {
		return nil, ErrNotInvite
	}
	var p notifications.AccountInvitePayload
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	if err := s.store.VoidPendingMemberTx(ctx, tx, p.AccountMemberID, userID); err != nil {
		return nil, err
	}
	if _, err := s.notifs.MarkDismissedTx(ctx, tx, userID, notificationID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	nv, badges, err := s.notificationState(ctx, userID, notificationID)
	if err != nil {
		return nil, err
	}
	sum, err := notifications.LoadInboxSummary(ctx, s.store.Pool(), userID)
	if err != nil {
		return nil, err
	}
	return &NotificationActionResult{Notification: nv, Badges: badges, Summary: sum}, nil
}

func (s *Service) requireActiveMember(ctx context.Context, callerUserID, accountID uuid.UUID) (*Account, error) {
	a, err := s.store.GetByID(ctx, callerUserID, accountID)
	if err == nil {
		return a, nil
	}
	if !errors.Is(err, ErrAccountNotFound) {
		return nil, err
	}
	var exists bool
	if qErr := s.store.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM accounts WHERE id = $1)`, accountID,
	).Scan(&exists); qErr != nil && !errors.Is(qErr, pgx.ErrNoRows) {
		return nil, fmt.Errorf("account exists check: %w", qErr)
	}
	if exists {
		return nil, ErrNotMember
	}
	return nil, ErrAccountNotFound
}

func (s *Service) notificationState(
	ctx context.Context, userID, notificationID uuid.UUID,
) (*shared.NotificationView, shared.Badges, error) {
	nv, err := shared.LoadNotificationView(ctx, s.store.Pool(), userID, notificationID)
	if err != nil {
		return nil, shared.Badges{}, err
	}
	if nv == nil {
		return nil, shared.Badges{}, notifications.ErrNotificationNotFound
	}
	badges, err := shared.LoadBadges(ctx, s.store.Pool(), userID)
	if err != nil {
		return nil, shared.Badges{}, err
	}
	return nv, badges, nil
}

func (s *Service) inviteResult(
	ctx context.Context, userID, notificationID, accountID uuid.UUID,
) (*AcceptInviteResult, error) {
	acc, err := s.store.GetByID(ctx, userID, accountID)
	if err != nil {
		return nil, err
	}
	nv, badges, err := s.notificationState(ctx, userID, notificationID)
	if err != nil {
		return nil, err
	}
	sum, err := notifications.LoadInboxSummary(ctx, s.store.Pool(), userID)
	if err != nil {
		return nil, err
	}
	return &AcceptInviteResult{Account: acc, Notification: nv, Badges: badges, Summary: sum}, nil
}
