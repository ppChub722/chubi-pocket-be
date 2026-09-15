package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
)

// WithNotificationService wires the notifications module post-construction
// (same pattern as projects ↔ notifications in main.go).
func (s *Service) WithNotificationService(n *notifications.Service) { s.notifs = n }

// --- List (GET /v1/accounts/:id/members) ---

// ListMembers returns the account's full membership history (active +
// pending + left) for the members screen. Any active member may view;
// ex-members and outsiders get NOT_MEMBER.
func (s *Service) ListMembers(
	ctx context.Context, callerUserID, accountID uuid.UUID,
) ([]MemberHistoryView, error) {
	if _, err := s.requireActiveMember(ctx, callerUserID, accountID); err != nil {
		return nil, err
	}
	return s.store.ListMembersByAccount(ctx, accountID)
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
	// Caller must be an active member (any member may invite).
	a, err := s.requireActiveMember(ctx, callerUserID, accountID)
	if err != nil {
		return nil, err
	}

	targetID, err := s.notifs.LookupUserByEmail(ctx, req.Email)
	if err != nil {
		return nil, err
	}
	if targetID == nil {
		return nil, ErrUserNotFound
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

	callerIsOwner := a.UserID == callerUserID
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
	if a.UserID != callerUserID {
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
) (*ReportScopeResponse, error) {
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
	return &ReportScopeResponse{AccountID: accountID, ReportScope: updated.ReportScope}, nil
}

// --- Invite accept / reject (POST /v1/accounts/invites/:notification_id/...) ---

// AcceptInvite activates the pending member row referenced by an
// account_invite notification. The new member starts at
// report_scope='none'; when this acceptance CONVERTS the wallet
// (1 active member → 2), every live member's scope is auto-reset to
// 'none' — the tick-out of spec §14/4.
func (s *Service) AcceptInvite(ctx context.Context, userID, notificationID uuid.UUID) (*AccountMember, error) {
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
		return nil, fmt.Errorf("notification is not an account_invite")
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
		return member, nil
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
	return activated, nil
}

// RejectInvite dismisses the notification and voids the pending member
// row so a later re-invite is possible.
func (s *Service) RejectInvite(ctx context.Context, userID, notificationID uuid.UUID) error {
	if s.notifs == nil {
		return errors.New("notifications module not wired")
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	n, err := s.notifs.GetByIDForCallerTx(ctx, tx, userID, notificationID)
	if err != nil {
		return err
	}
	if n.Type != notifications.TypeAccountInvite {
		return fmt.Errorf("notification is not an account_invite")
	}
	var p notifications.AccountInvitePayload
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		return fmt.Errorf("payload: %w", err)
	}
	if err := s.store.VoidPendingMemberTx(ctx, tx, p.AccountMemberID, userID); err != nil {
		return err
	}
	if _, err := s.notifs.MarkDismissedTx(ctx, tx, userID, notificationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
