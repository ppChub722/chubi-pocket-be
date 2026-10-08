package notifications

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Delivery is what a producer does for one recipient (contract §5):
//
//   - Deliver false → the recipient muted this type: no notification and
//     no auto-action, whatever their auto switch says.
//   - Auto true → run the notification's action right away, then dispatch
//     it already actioned ("ทำให้แล้ว").
//   - otherwise → dispatch it pending; the inbox offers the action button.
type Delivery struct {
	Deliver bool
	Auto    bool
}

// DeliveryFor reads the recipient's switches inside the producer's tx.
// Self-targeted events never deliver (solo projects etc. stay quiet).
func (s *Service) DeliveryFor(
	ctx context.Context, tx pgx.Tx, recipientUserID uuid.UUID, actorUserID *uuid.UUID, notifType string,
) (Delivery, error) {
	if actorUserID != nil && *actorUserID == recipientUserID {
		return Delivery{}, nil
	}
	if actionTypes[notifType] {
		return Delivery{Deliver: true}, nil // requests: never muted, never auto
	}
	st, err := s.store.GetSettingsTx(ctx, tx, recipientUserID)
	if err != nil {
		return Delivery{}, err
	}
	return Delivery{Deliver: st.Deliver(notifType), Auto: st.Auto(notifType)}, nil
}

// SettingsTx exposes the recipient's settings to producers (e.g. the
// split_paid auto-action needs default_account_id).
func (s *Service) SettingsTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) (*Settings, error) {
	return s.store.GetSettingsTx(ctx, tx, userID)
}

// dispatchTx is the single in-tx insert path. It re-checks delivery, so a
// producer that skips DeliveryFor still honours mutes and the self guard.
// actioned = the auto-action already ran.
func (s *Service) dispatchTx(
	ctx context.Context, tx pgx.Tx,
	notifType string, recipientUserID uuid.UUID, actorUserID *uuid.UUID,
	payload any, deepLink *string, actioned bool,
) error {
	d, err := s.DeliveryFor(ctx, tx, recipientUserID, actorUserID, notifType)
	if err != nil || !d.Deliver {
		return err
	}
	bytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s payload: %w", notifType, err)
	}
	_, err = s.store.InsertTx(ctx, tx, recipientUserID, notifType, actorUserID, bytes, deepLink, actioned)
	return err
}

// --- Typed wrappers (one per trigger). Producers call these from inside
// their own action's tx. ---

func (s *Service) DispatchSplitCreated(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p SplitCreatedPayload, actioned bool,
) error {
	deepLink := splitDeepLink(p.RecipientDebtID)
	return s.dispatchTx(ctx, tx, TypeSplitCreated, recipientUserID, &actorUserID, p, &deepLink, actioned)
}

func (s *Service) DispatchSplitPaid(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p SplitPaidPayload, actioned bool,
) error {
	deepLink := splitDeepLink(p.RecipientDebtID)
	return s.dispatchTx(ctx, tx, TypeSplitPaid, recipientUserID, &actorUserID, p, &deepLink, actioned)
}

func (s *Service) DispatchProjectTxRecorded(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p ProjectTxRecordedPayload, actioned bool,
) error {
	deepLink := fmt.Sprintf("/projects/%s/transactions/%s", p.ProjectID, p.ProjectTransactionID)
	return s.dispatchTx(ctx, tx, TypeProjectTxRecordedForYou, recipientUserID, &actorUserID, p, &deepLink, actioned)
}

func (s *Service) DispatchProjectTxChanged(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p ProjectTxChangedPayload, actioned bool,
) error {
	deepLink := fmt.Sprintf("/projects/%s/transactions/%s", p.ProjectID, p.ProjectTransactionID)
	if p.ChangeKind == "deleted" {
		deepLink = fmt.Sprintf("/projects/%s", p.ProjectID)
	}
	return s.dispatchTx(ctx, tx, TypeProjectTxChanged, recipientUserID, &actorUserID, p, &deepLink, actioned)
}

func (s *Service) DispatchProjectInvite(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p ProjectInvitePayload,
) error {
	deepLink := fmt.Sprintf("/projects/link-requests/%s", p.ProjectMemberID)
	return s.dispatchTx(ctx, tx, TypeProjectInvite, recipientUserID, &actorUserID, p, &deepLink, false)
}

func (s *Service) DispatchContactLinkRequest(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p ContactLinkRequestPayload,
) error {
	deepLink := fmt.Sprintf("/contacts/link-requests/%s", p.ContactID)
	return s.dispatchTx(ctx, tx, TypeContactLinkRequest, recipientUserID, &actorUserID, p, &deepLink, false)
}

// DispatchProjectAdded is informational only (no accept/reject actions) —
// quick create auto-adds members whose consent is implied by the underlying
// splits / shared wallet (spec §10/4.24).
func (s *Service) DispatchProjectAdded(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p ProjectAddedPayload,
) error {
	deepLink := fmt.Sprintf("/projects/%s", p.ProjectID)
	return s.dispatchTx(ctx, tx, TypeProjectAdded, recipientUserID, &actorUserID, p, &deepLink, false)
}

func (s *Service) DispatchAccountInvite(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p AccountInvitePayload,
) error {
	deepLink := fmt.Sprintf("/accounts/invites/%s", p.AccountMemberID)
	return s.dispatchTx(ctx, tx, TypeAccountInvite, recipientUserID, &actorUserID, p, &deepLink, false)
}

// splitDeepLink opens the recipient's own debt row when they have one;
// otherwise (split_created awaiting "add to my debts") the debts list.
func splitDeepLink(recipientDebtID *uuid.UUID) string {
	if recipientDebtID != nil {
		return fmt.Sprintf("/personal-debts/%s", *recipientDebtID)
	}
	return "/personal-debts"
}
