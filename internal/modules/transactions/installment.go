package transactions

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
)

// 0.4.3 quick create "ผ่อน": an expense on a credit_card / pay_later wallet with `installment` also
// makes a debt item (the wallet's institution, a plan on the card, no line), in the same write.

var (
	// ErrInstallmentNotCard — `installment` on anything but an expense on a card wallet.
	ErrInstallmentNotCard = apierr.Invalid("installment needs an expense on a credit_card / pay_later wallet", "installment", apierr.FieldInvalid)
	// ErrInstallmentNeedsInstitution — the card wallet is not linked to an institution yet.
	ErrInstallmentNeedsInstitution = errors.New("link the wallet to an institution first")
	// ErrInstallmentInvalid — the installment plan is invalid (the recurring module wraps its fields).
	ErrInstallmentInvalid = errors.New("the installment plan is invalid")
)

// InstallmentRequest — the "ผ่อน" card of quick create.
type InstallmentRequest struct {
	Count             int      `json:"count"`
	InterestMethod    string   `json:"interest_method"`
	InterestRate      *float64 `json:"interest_rate"`
	InstallmentAmount *float64 `json:"installment_amount"`
	// FirstDue — the first installment's due date (default: the card's next statement day after the
	// purchase).
	FirstDue *string `json:"first_due"`
}

// Installments — the recurring module's maker of the installment item (wired from main).
type Installments func(ctx context.Context, tx pgx.Tx, userID uuid.UUID, purchase TransactionDetail, in InstallmentRequest) (uuid.UUID, error)

func (s *Service) WithInstallments(fn Installments) { s.installments = fn }

// installmentTx — the item of a purchase with `installment`.
func (s *Service) installmentTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, req CreateRequest, res any) (*uuid.UUID, error) {
	if req.Installment == nil {
		return nil, nil
	}
	row, ok := res.(*TransactionDetail)
	if !ok || req.Type != TypeExpense || req.AccountID == nil || s.installments == nil {
		return nil, ErrInstallmentNotCard
	}
	var typ string
	if err := tx.QueryRow(ctx, `SELECT type FROM accounts WHERE id = $1`, *req.AccountID).Scan(&typ); err != nil {
		return nil, err
	}
	if typ != "credit_card" && typ != "pay_later" {
		return nil, ErrInstallmentNotCard
	}
	id, err := s.installments(ctx, tx, userID, *row, *req.Installment)
	if err != nil {
		return nil, err
	}
	return &id, nil
}
