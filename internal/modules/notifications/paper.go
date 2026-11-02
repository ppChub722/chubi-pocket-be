package notifications

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Papers (split bills and event bills, one shared paper each) and the debts made from them.
const (
	TypeBillInvite       = "bill_invite"        // I was put on a split bill: join or decline
	TypeBillNew          = "bill_new"           // a new event bill I am on: claim it
	TypeBillChanged      = "bill_changed"       // a paper I have books of changed (applied or "อัปเดต")
	TypeBillRemoved      = "bill_removed"       // I am off a paper (removed, or it was deleted): keep / delete my books
	TypeBillOwnerChanged = "bill_owner_changed" // the owner left: I own the paper now
	TypeBillMoved        = "bill_moved"         // a paper I am on moved into / out of an event
	TypeDebtRepaid       = "debt_repaid"        // my partner recorded a repayment on our pair
)

// BillPayload — what every bill notice carries: the paper, its event (nil = a split bill), and
// what happened to my books.
type BillPayload struct {
	PaperID     uuid.UUID  `json:"split_transaction_id"`
	ProjectID   *uuid.UUID `json:"project_id"`
	ProjectName *string    `json:"project_name"`
	Description *string    `json:"description"`
	Amount      float64    `json:"amount"`
	MyShare     float64    `json:"my_share"`
	// Applied — my switch already did it (claimed / synced / kept or deleted my books).
	Applied bool `json:"applied"`
	// MyTransactionID / MyDebtIDs — my books of it (bill_removed: what keep / delete would act on).
	MyTransactionID *uuid.UUID  `json:"my_transaction_id"`
	MyDebtIDs       []uuid.UUID `json:"my_debt_ids"`
	// FromProjectID — bill_moved: the event it left (nil = it was a split bill).
	FromProjectID *uuid.UUID `json:"from_project_id"`
}

// DebtRepaidPayload — my partner paid me back (or I paid them, as they recorded it).
type DebtRepaidPayload struct {
	DebtID      uuid.UUID `json:"debt_id"` // my partner debt
	Key         string    `json:"key"`
	PersonName  string    `json:"person_name"`
	Amount      float64   `json:"amount"`
	Direction   string    `json:"direction"` // as my repayment row would be: paid | received
	Date        string    `json:"date"`
	Description *string   `json:"description"`
	// RepaymentID — my repayment written by the auto switch (nil = it asks).
	RepaymentID *uuid.UUID `json:"repayment_id"`
}

func paperDeepLink(p BillPayload) string {
	if p.ProjectID != nil {
		return fmt.Sprintf("/projects/%s/bills/%s", *p.ProjectID, p.PaperID)
	}
	return fmt.Sprintf("/split-bills/%s", p.PaperID)
}

// DispatchBill — one bill notice (applied = my switch did it: an info row).
func (s *Service) DispatchBill(ctx context.Context, tx pgx.Tx, notifType string, recipientUserID, actorUserID uuid.UUID,
	p BillPayload) error {
	link := paperDeepLink(p)
	return s.dispatchTx(ctx, tx, notifType, recipientUserID, &actorUserID, p, &link, p.Applied)
}

// DispatchDebtRepaid — my partner's repayment, applied or asking.
func (s *Service) DispatchDebtRepaid(ctx context.Context, tx pgx.Tx, recipientUserID, actorUserID uuid.UUID,
	p DebtRepaidPayload) error {
	link := debtDeepLink(p.DebtID)
	return s.dispatchTx(ctx, tx, TypeDebtRepaid, recipientUserID, &actorUserID, p, &link, p.RepaymentID != nil)
}

// SupersedePaperNoticesTx — a newer notice about a paper replaces my older unanswered ones.
func (s *Service) SupersedePaperNoticesTx(ctx context.Context, tx pgx.Tx, recipientUserID, paperID uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE notifications SET actioned_at = NOW()
		WHERE recipient_user_id = $1 AND actioned_at IS NULL
		  AND type IN ('bill_invite', 'bill_new', 'bill_changed')
		  AND payload->>'split_transaction_id' = $2`, recipientUserID, paperID.String())
	if err != nil {
		return fmt.Errorf("supersede paper notices: %w", err)
	}
	return nil
}
