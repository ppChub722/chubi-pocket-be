package notifications

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Shared bills (0.4.1-2, BE-1, migration 092): an event bill and a split bill are one thing; every
// member holds a copy. The notices of the round; each can be muted (muting never stops an apply).
const (
	TypeSharedBillCreated      = "shared_bill_created"
	TypeSharedBillDeleted      = "shared_bill_deleted"
	TypeSharedBillDisconnected = "shared_bill_disconnected"
)

// SharedBillCreatedPayload — another member saved a new shared bill I have a part in. Applied = my
// auto-sync switch put my copy in my book already (info); false = it asks ("เพิ่มในสมุด").
type SharedBillCreatedPayload struct {
	ProjectTransactionID  uuid.UUID  `json:"project_transaction_id"`
	ProjectID             uuid.UUID  `json:"project_id"`
	ProjectKind           string     `json:"project_kind"`
	ProjectName           *string    `json:"project_name"`
	Description           *string    `json:"description"`
	Amount                float64    `json:"amount"`
	MyRole                string     `json:"my_role"`
	MyAmount              float64    `json:"my_amount"`
	PayerName             string     `json:"payer_name"`
	Applied               bool       `json:"applied"`
	PersonalTransactionID *uuid.UUID `json:"personal_transaction_id"`
}

// SharedBillDeletedPayload — a shared bill I hold a copy of was deleted.
type SharedBillDeletedPayload struct {
	ProjectTransactionID uuid.UUID   `json:"project_transaction_id"`
	ProjectID            *uuid.UUID  `json:"project_id"`
	ProjectKind          string      `json:"project_kind"`
	ProjectName          *string     `json:"project_name"`
	Description          *string     `json:"description"`
	Amount               float64     `json:"amount"`
	MyTransactionIDs     []uuid.UUID `json:"my_transaction_ids"`
	MyDebtIDs            []uuid.UUID `json:"my_debt_ids"`
	Applied              bool        `json:"applied"`
}

// SharedBillDisconnectedPayload — another member disconnected their copy of a split bill (info).
type SharedBillDisconnectedPayload struct {
	ProjectTransactionID uuid.UUID `json:"project_transaction_id"`
	ProjectKind          string    `json:"project_kind"`
	Description          *string   `json:"description"`
	Amount               float64   `json:"amount"`
	PersonName           string    `json:"person_name"`
}

func billDeepLink(ptID uuid.UUID) string { return fmt.Sprintf("/event-bills/%s", ptID) }

func (s *Service) DispatchSharedBillCreated(ctx context.Context, tx pgx.Tx, recipientUserID, actorUserID uuid.UUID,
	p SharedBillCreatedPayload) error {
	link := billDeepLink(p.ProjectTransactionID)
	return s.dispatchTx(ctx, tx, TypeSharedBillCreated, recipientUserID, &actorUserID, p, &link, p.Applied)
}

func (s *Service) DispatchSharedBillDeleted(ctx context.Context, tx pgx.Tx, recipientUserID, actorUserID uuid.UUID,
	p SharedBillDeletedPayload) error {
	var link *string
	if len(p.MyTransactionIDs) > 0 {
		l := fmt.Sprintf("/transactions/%s", p.MyTransactionIDs[0])
		link = &l
	}
	return s.dispatchTx(ctx, tx, TypeSharedBillDeleted, recipientUserID, &actorUserID, p, link, p.Applied)
}

func (s *Service) DispatchSharedBillDisconnected(ctx context.Context, tx pgx.Tx, recipientUserID, actorUserID uuid.UUID,
	p SharedBillDisconnectedPayload) error {
	link := billDeepLink(p.ProjectTransactionID)
	return s.dispatchTx(ctx, tx, TypeSharedBillDisconnected, recipientUserID, &actorUserID, p, &link, true)
}

// SupersedeBillNoticesTx — a newer notice about the bill replaces my older unanswered ones (they are
// marked actioned).
func (s *Service) SupersedeBillNoticesTx(ctx context.Context, tx pgx.Tx, recipientUserID, ptID uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE notifications SET actioned_at = NOW()
		WHERE recipient_user_id = $1 AND actioned_at IS NULL
		  AND type IN ('shared_bill_created', 'project_tx_changed', 'project_tx_recorded_for_you')
		  AND payload->>'project_transaction_id' = $2`, recipientUserID, ptID.String())
	if err != nil {
		return fmt.Errorf("supersede bill notices: %w", err)
	}
	return nil
}
