package notifications

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Event lifecycle notices (0.4.1-2 V2 / V15, BE-2). Info only; both can be muted.
const (
	TypeEventCompleted     = "event_completed"
	TypeEventMemberRemoved = "event_member_removed"
	TypeEventDeleted       = "event_deleted" // owner 2026-10-28: the bills stay as split bills
)

// EventDeletedPayload — the owner deleted an event I was in; its bills stay as standalone split
// bills, my copies / debts still connected.
type EventDeletedPayload struct {
	ProjectID      uuid.UUID `json:"project_id"` // gone
	ProjectName    string    `json:"project_name"`
	SplitBillCount int       `json:"split_bill_count"`
}

func (s *Service) DispatchEventDeleted(ctx context.Context, tx pgx.Tx, recipientUserID, actorUserID uuid.UUID,
	p EventDeletedPayload) error {
	deepLink := "/projects"
	return s.dispatchTx(ctx, tx, TypeEventDeleted, recipientUserID, &actorUserID, p, &deepLink, false)
}

// EventCompletedPayload — the owner completed an event I'm a linked member of: locked for good, my
// connected records there stopped syncing (their links stay).
type EventCompletedPayload struct {
	ProjectID   uuid.UUID `json:"project_id"`
	ProjectName string    `json:"project_name"`
	OwnerName   string    `json:"owner_name"`
}

// EventMemberRemovedPayload — the owner removed me from an event: what I booked from it stays in my
// book, disconnected.
type EventMemberRemovedPayload struct {
	ProjectID        uuid.UUID `json:"project_id"`
	ProjectName      string    `json:"project_name"`
	TransactionCount int       `json:"transaction_count"`
	DebtCount        int       `json:"debt_count"`
}

func (s *Service) DispatchEventCompleted(ctx context.Context, tx pgx.Tx, recipientUserID, actorUserID uuid.UUID,
	p EventCompletedPayload) error {
	deepLink := fmt.Sprintf("/projects/%s", p.ProjectID)
	return s.dispatchTx(ctx, tx, TypeEventCompleted, recipientUserID, &actorUserID, p, &deepLink, false)
}

func (s *Service) DispatchEventMemberRemoved(ctx context.Context, tx pgx.Tx, recipientUserID, actorUserID uuid.UUID,
	p EventMemberRemovedPayload) error {
	deepLink := "/projects" // the event can't be opened any more
	return s.dispatchTx(ctx, tx, TypeEventMemberRemoved, recipientUserID, &actorUserID, p, &deepLink, false)
}

// SupersedeEventNoticesTx — an event stopped syncing (completed): the pending "อัปเดตตาม" / copy
// notices about its bills no longer ask anything.
func (s *Service) SupersedeEventNoticesTx(ctx context.Context, tx pgx.Tx, projectID uuid.UUID, types []string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE notifications SET payload = payload || '{"superseded": true}'::jsonb, updated_at = NOW()
		WHERE type = ANY($2) AND payload->>'project_id' = $1::text AND actioned_at IS NULL`,
		projectID.String(), types); err != nil {
		return fmt.Errorf("supersede event notices: %w", err)
	}
	return nil
}

// SupersedeEventNoticesForTx — the same for one recipient (they left / were removed).
func (s *Service) SupersedeEventNoticesForTx(ctx context.Context, tx pgx.Tx, recipientUserID, projectID uuid.UUID, types []string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE notifications SET payload = payload || '{"superseded": true}'::jsonb, updated_at = NOW()
		WHERE recipient_user_id = $1 AND type = ANY($3) AND payload->>'project_id' = $2::text AND actioned_at IS NULL`,
		recipientUserID, projectID.String(), types); err != nil {
		return fmt.Errorf("supersede event notices: %w", err)
	}
	return nil
}
