package notifications

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Delivery — how a notice reaches its recipient: Deliver = not muted (Insert says whether it is
// written: a notice that still asks comes even when muted); Auto = apply it
// at once by the type's auto-sync switch, muted or not (0.4.1-2 S4: a muted auto type is applied
// silently). SyncEvents / SyncDebts are the recipient's two switches, for types whose switch
// depends on the bill kind (shared bills).
type Delivery struct {
	Deliver    bool
	Auto       bool
	SyncEvents bool
	SyncDebts  bool
	// AcceptEvents / AcceptDebts — the recipient's auto-accept switches for a NEW shared bill
	// (pick by the bill kind, like SyncEvents / SyncDebts).
	AcceptEvents bool
	AcceptDebts  bool
	// AcceptRequest — the recipient's auto-accept switch for this request type (F2).
	AcceptRequest bool
	// self — the recipient made the change: never a notice.
	self bool
}

// Insert — whether the notice is written: แจ้ง and อัตโนมัติ are independent (owner 2026-10-30,
// F1). Muted = no notice, applied or not; muted with auto off = nothing happens at all.
func (d Delivery) Insert(string, bool) bool {
	return !d.self && d.Deliver
}

// DeliveryFor — one settings read per recipient (call it once per member inside the write's DB tx).
// Yourself as the recipient: nothing.
func (s *Service) DeliveryFor(
	ctx context.Context, tx pgx.Tx, recipientUserID uuid.UUID, actorUserID *uuid.UUID, notifType string,
) (Delivery, error) {
	if actorUserID != nil && *actorUserID == recipientUserID {
		return Delivery{self: true}, nil
	}
	st, err := s.store.GetSettingsTx(ctx, tx, recipientUserID)
	if err != nil {
		return Delivery{}, err
	}
	return Delivery{
		Deliver: st.Deliver(notifType), Auto: st.Auto(notifType),
		SyncEvents: st.AutoSyncEvents, SyncDebts: st.AutoSyncDebts,
		AcceptEvents: st.AutoAcceptEvents, AcceptDebts: st.AutoAcceptDebts,
		AcceptRequest: st.AcceptRequest(notifType),
	}, nil
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
	// actioned = my auto switch already applied it (ask types pass their payload's Applied).
	if err != nil || !d.Insert(notifType, actioned) {
		return err
	}
	bytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s payload: %w", notifType, err)
	}
	_, err = s.store.InsertTx(ctx, tx, recipientUserID, notifType, actorUserID, bytes, deepLink, actioned)
	return err
}

// (DispatchSplitCreated / DispatchSplitChanged / DispatchProjectTxRecorded, the one-way mirror's
// notices, were retired in 0.4.1-2 S16: shared bills notify through shared_bill.go.)

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
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p ProjectInvitePayload, accepted bool,
) error {
	deepLink := fmt.Sprintf("/projects/link-requests/%s", p.ProjectMemberID)
	return s.dispatchTx(ctx, tx, TypeProjectInvite, recipientUserID, &actorUserID, p, &deepLink, accepted)
}

func (s *Service) DispatchContactLinkRequest(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p ContactLinkRequestPayload, accepted bool,
) error {
	deepLink := fmt.Sprintf("/contacts/link-requests/%s", p.ContactID)
	return s.dispatchTx(ctx, tx, TypeContactLinkRequest, recipientUserID, &actorUserID, p, &deepLink, accepted)
}

func (s *Service) DispatchAccountInvite(
	ctx context.Context, tx pgx.Tx,
	recipientUserID uuid.UUID, actorUserID uuid.UUID, p AccountInvitePayload, accepted bool,
) error {
	deepLink := fmt.Sprintf("/accounts/invites/%s", p.AccountMemberID)
	return s.dispatchTx(ctx, tx, TypeAccountInvite, recipientUserID, &actorUserID, p, &deepLink, accepted)
}

// debtDeepLink opens the recipient's own debt row.
func debtDeepLink(debtID uuid.UUID) string {
	return fmt.Sprintf("/personal-debts/%s", debtID)
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

// DispatchDebtDisconnected — info only (no action). splitID is the other side's entry (kept for the
// callers; the split notices it once superseded are gone since 0.4.1-2 S16).
func (s *Service) DispatchDebtDisconnected(ctx context.Context, tx pgx.Tx, recipientUserID, actorUserID uuid.UUID,
	splitID uuid.UUID, p DebtDisconnectedPayload) error {
	deepLink := debtDeepLink(p.DebtID)
	return s.dispatchTx(ctx, tx, TypeDebtDisconnected, recipientUserID, &actorUserID, p, &deepLink, false)
}
