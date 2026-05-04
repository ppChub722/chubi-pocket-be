package notifications

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// dispatchTx is the single in-tx insert path. Every typed wrapper below
// funnels through here so the self-target guard fires exactly once for
// every trigger.
//
// Self-target guard: if recipient == actor, the dispatch is silently
// skipped. The producer doesn't need to gate this — keeps "solo project
// no-notification" working for free across all triggers.
func (s *Service) dispatchTx(
	ctx context.Context, tx pgx.Tx,
	notifType string, recipientUserID uuid.UUID, actorUserID *uuid.UUID,
	payload any, deepLink *string,
) error {
	if actorUserID != nil && *actorUserID == recipientUserID {
		return nil
	}
	bytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s payload: %w", notifType, err)
	}
	_, err = s.store.InsertTx(ctx, tx, recipientUserID, notifType, actorUserID, bytes, deepLink)
	return err
}

// --- Typed wrappers (one per trigger). Producers call these from inside
// their own action's tx. Each wrapper marshals its typed payload and
// inserts a row. ---

func (s *Service) DispatchSplitCreated(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p SplitCreatedPayload,
) error {
	deepLink := splitDeepLink(p.SplitID, p.ProjectID)
	return s.dispatchTx(ctx, tx, TypeSplitCreated, recipientUserID, &actorUserID, p, &deepLink)
}

func (s *Service) DispatchSplitPaid(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p SplitPaidPayload,
) error {
	deepLink := splitDeepLink(p.SplitID, p.ProjectID)
	return s.dispatchTx(ctx, tx, TypeSplitPaid, recipientUserID, &actorUserID, p, &deepLink)
}

func (s *Service) DispatchSplitReceived(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p SplitReceivedPayload,
) error {
	deepLink := splitDeepLink(p.SplitID, p.ProjectID)
	return s.dispatchTx(ctx, tx, TypeSplitReceived, recipientUserID, &actorUserID, p, &deepLink)
}

func (s *Service) DispatchProjectTxRecorded(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p ProjectTxRecordedPayload,
) error {
	deepLink := fmt.Sprintf("/projects/%s/transactions/%s", p.ProjectID, p.ProjectTransactionID)
	return s.dispatchTx(ctx, tx, TypeProjectTxRecordedForYou, recipientUserID, &actorUserID, p, &deepLink)
}

func (s *Service) DispatchProjectTxChanged(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p ProjectTxChangedPayload,
) error {
	deepLink := fmt.Sprintf("/projects/%s/transactions/%s", p.ProjectID, p.ProjectTransactionID)
	if p.ChangeKind == "deleted" {
		deepLink = fmt.Sprintf("/projects/%s", p.ProjectID)
	}
	return s.dispatchTx(ctx, tx, TypeProjectTxChanged, recipientUserID, &actorUserID, p, &deepLink)
}

func (s *Service) DispatchProjectInvite(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p ProjectInvitePayload,
) error {
	deepLink := fmt.Sprintf("/projects/link-requests/%s", p.ProjectMemberID)
	return s.dispatchTx(ctx, tx, TypeProjectInvite, recipientUserID, &actorUserID, p, &deepLink)
}

func (s *Service) DispatchContactLinkRequest(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p ContactLinkRequestPayload,
) error {
	deepLink := fmt.Sprintf("/contacts/link-requests/%s", p.ContactID)
	return s.dispatchTx(ctx, tx, TypeContactLinkRequest, recipientUserID, &actorUserID, p, &deepLink)
}

func splitDeepLink(splitID uuid.UUID, projectID *uuid.UUID) string {
	if projectID != nil {
		return fmt.Sprintf("/projects/%s/splits/%s", *projectID, splitID)
	}
	return fmt.Sprintf("/shared-expenses/splits/%s", splitID)
}
