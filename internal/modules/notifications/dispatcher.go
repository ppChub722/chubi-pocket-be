package notifications

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Delivery struct {
	Deliver bool
	Auto    bool
}

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

func (s *Service) SettingsTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) (*Settings, error) {
	return s.store.GetSettingsTx(ctx, tx, userID)
}

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

func (s *Service) DispatchSplitCreated(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p SplitCreatedPayload, actioned bool,
) error {
	deepLink := splitDeepLink(p.RecipientDebtID)
	return s.dispatchTx(ctx, tx, TypeSplitCreated, recipientUserID, &actorUserID, p, &deepLink, actioned)
}

func (s *Service) DispatchProjectTxRecorded(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p ProjectTxRecordedPayload, actioned bool,
) error {
	deepLink := fmt.Sprintf("/projects/%s", p.ProjectID) // the app has no per-bill route: open the event
	return s.dispatchTx(ctx, tx, TypeProjectTxRecordedForYou, recipientUserID, &actorUserID, p, &deepLink, actioned)
}

func (s *Service) DispatchProjectTxChanged(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p ProjectTxChangedPayload, actioned bool,
) error {
	deepLink := fmt.Sprintf("/projects/%s", p.ProjectID) // the app has no per-bill route: open the event
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

// splitDeepLink opens the recipient's own debt row when they have one; otherwise (split_created
// awaiting "add to my debts") the debts list.
func splitDeepLink(recipientDebtID *uuid.UUID) string {
	if recipientDebtID != nil {
		return fmt.Sprintf("/personal-debts/%s", *recipientDebtID)
	}
	return "/personal-debts"
}

func (s *Service) DispatchSplitChanged(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p SplitChangedPayload, actioned bool,
) error {
	if err := s.store.SupersedeSplitNoticesTx(ctx, tx, recipientUserID, TypeSplitChanged, p.SplitID); err != nil {
		return err
	}
	deepLink := splitDeepLink(&p.RecipientDebtID)
	return s.dispatchTx(ctx, tx, TypeSplitChanged, recipientUserID, &actorUserID, p, &deepLink, actioned)
}

func (s *Service) SupersedeSplitNoticesTx(ctx context.Context, tx pgx.Tx, recipientUserID uuid.UUID, notifType string, splitID uuid.UUID) error {
	return s.store.SupersedeSplitNoticesTx(ctx, tx, recipientUserID, notifType, splitID)
}

func (s *Service) RefreshSplitCreatedTx(ctx context.Context, tx pgx.Tx, recipientUserID, splitID uuid.UUID, amount float64) (bool, error) {
	return s.store.RefreshSplitCreatedTx(ctx, tx, recipientUserID, splitID, amount)
}

// DebtDisconnectedPayload (0.4.1-1 D9 / B3) — "X ตัดการเชื่อมหนี้ ฿300 แล้ว": the other user
// disconnected their copy; DebtID is the recipient's own entry, now a plain debt.
type DebtDisconnectedPayload struct {
	DebtID      uuid.UUID `json:"debt_id"`
	Key         string    `json:"key"`
	PersonName  string    `json:"person_name"`
	Amount      float64   `json:"amount"`
	Direction   string    `json:"direction"`
	Description *string   `json:"description"`
}

// DispatchDebtDisconnected — info only (no action); pending split notices about the pair are
// superseded first.
func (s *Service) DispatchDebtDisconnected(ctx context.Context, tx pgx.Tx, recipientUserID, actorUserID uuid.UUID,
	splitID uuid.UUID, p DebtDisconnectedPayload) error {
	for _, t := range []string{TypeSplitCreated, TypeSplitChanged} {
		if err := s.store.SupersedeSplitNoticesTx(ctx, tx, recipientUserID, t, splitID); err != nil {
			return err
		}
	}
	deepLink := splitDeepLink(&p.DebtID)
	return s.dispatchTx(ctx, tx, TypeDebtDisconnected, recipientUserID, &actorUserID, p, &deepLink, false)
}
