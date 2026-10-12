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

// WithNotificationService wires the notifications module post-construction
// (same pattern as projects ↔ notifications in main.go).
func (s *Service) WithNotificationService(n *notifications.Service) { s.notifs = n }

// MembersResult is the final state every member write returns: the wallet
// (as the caller sees it) and the whole membership history. A caller who is
// no longer a member (they just left) gets {account: null, members: []}.
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

// --- Invite (POST /v1/accounts/:id/members) ---

// InviteMember invites a user by email via the notification pattern —
// pending account_members row + account_invite notification, mirroring
// the project member invite flow (spec §14/8.1). The conversion warning
// is client-side; the BE just records the invite.
func (s *Service) InviteMember(
	ctx context.Context, callerUserID, accountID uuid.UUID, req InviteMemberRequest,
) (*AccountMember, error) {
	if s.notifs == nil {
		return nil, errors.New("notifications module not wired")
	}
	// Owner only (can_edit): the owner invites.
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

	// Pending row: joined_at NULL until the invitee accepts. New members
	// always join at report_scope='none' (set again on activation).
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

// inviteTarget resolves who an invite is for: my contact's linked app user
// (contact_id), or the user with that email.
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

// --- Leave / remove (DELETE /v1/accounts/:id/members/:member_id) ---

// RemoveMember sets left_at (never hard-deletes). v1 rule: a member may
// remove THEMSELVES (leave); the owner may remove anyone. The owner's
// own row can never be removed — transfer ownership first (spec §14/2
// rule 5); with other active members present that's OWNER_MUST_TRANSFER.
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
		// Owner exit always requires an ownership transfer first — a
		// wallet must never be left without an active owner.
		return nil, ErrOwnerMustTransfer
	}
	if target.UserID != callerUserID && !callerIsOwner {
		return nil, ErrNotOwner
	}

	return s.store.SoftRemoveMember(ctx, accountID, memberID, callerUserID)
}

// --- Transfer ownership (POST /v1/accounts/:id/transfer-ownership) ---

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

// --- Report scope (PUT /v1/accounts/:id/report-scope) ---

// UpdateReportScope updates the CALLER's own membership row. Ex-members
// (left_at set) keep the toggle but capped at 'own' — requesting 'all'
// is SCOPE_NOT_ALLOWED (spec §14/5).
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
		return s.store.GetByID(ctx, callerUserID, accountID)
	}
	// An ex-member still owns their scope toggle but no longer sees the wallet
	// through the membership gate: read it directly, as a plain member.
	a, err := s.store.getAnyMember(ctx, callerUserID, accountID)
	if err != nil {
		return nil, err
	}
	a.MyReportScope = updated.ReportScope
	return a, nil
}

// --- Invite accept / reject (POST /v1/accounts/invites/:notification_id/...) ---

// AcceptInvite activates the pending member row referenced by an
// account_invite notification. The new member starts at
// report_scope='none'; when this acceptance CONVERTS the wallet
// (1 active member → 2), every live member's scope is auto-reset to
// 'none' — the tick-out of spec §14/4.
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
		// Invite was cancelled / already voided.
		return nil, ErrMemberNotFound
	}
	if member.JoinedAt != nil {
		// Idempotent re-accept.
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
		// Conversion moment: personal → shared. Auto tick-out for every
		// live member, including the owner (spec §14/4).
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

// RejectInvite dismisses the notification and voids the pending member
// row so a later re-invite is possible.
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
	return &NotificationActionResult{Notification: nv, Badges: badges}, nil
}

// --- Helpers ---

// requireActiveMember loads the account when the caller is an active
// member. Unknown account OR non-member → ErrAccountNotFound from
// GetByID would leak nothing; but member-management endpoints pin a 403
// NOT_MEMBER for callers who can see that the account exists yet hold no
// membership — we translate NotFound to NOT_MEMBER only when the account
// row exists at all.
func (s *Service) requireActiveMember(ctx context.Context, callerUserID, accountID uuid.UUID) (*Account, error) {
	a, err := s.store.GetByID(ctx, callerUserID, accountID)
	if err == nil {
		return a, nil
	}
	if !errors.Is(err, ErrAccountNotFound) {
		return nil, err
	}
	// Distinguish "no such account" (404) from "not a member" (403).
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

// notificationState reads the notification in its new state and the badges
// (after the action committed).
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

// inviteResult is the AcceptInviteResult of an accepted invite.
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
	return &AcceptInviteResult{Account: acc, Notification: nv, Badges: badges}, nil
}
