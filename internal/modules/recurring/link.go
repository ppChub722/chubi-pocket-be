package recurring

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"net/http"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// ErrTxNotFound — the transaction to link isn't the caller's.
var ErrTxNotFound = errors.New("transaction not found")

// LinkRequest — POST /recurring-items/:id/link.
type LinkRequest struct {
	TransactionID uuid.UUID `json:"transaction_id"`
}

// LinkResult — spec RecurringLinkResult: the item (done, with the
// transaction) and whether the transaction's amount differs from the
// setting's, so the app can ask "อัปเดตยอดรายการประจำด้วยไหม?" (answer: PUT
// /recurring/:id {amount}).
type LinkResult struct {
	Item              Item                   `json:"item"`
	Transaction       any                    `json:"transaction"`
	AmountDiffers     bool                   `json:"amount_differs"`
	RuleAmount        *float64               `json:"rule_amount"`
	TransactionAmount float64                `json:"transaction_amount"`
	AccountBalances   []shared.WalletBalance `json:"account_balances"`
}

// Link ties an existing transaction to an occurrence (normally the current
// one): the item becomes done with the transaction, the occurrence's pending
// draft goes, and the item takes the real amount (for a reminder that IS the
// period's amount; the setting is untouched).
func (s *Service) Link(ctx context.Context, userID, itemID uuid.UUID, req LinkRequest) (*LinkResult, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	it, err := getItem(ctx, tx, userID, itemID, true)
	if err != nil {
		return nil, err
	}
	if it.TransactionID != nil || it.SavingEntryID != nil || it.Archived {
		return nil, ErrAlreadyLinked
	}
	var (
		txType   string
		txAmount float64
		accID    *uuid.UUID
	)
	err = tx.QueryRow(ctx, `SELECT type, amount::float8, account_id FROM transactions WHERE id = $1 AND user_id = $2`,
		req.TransactionID, userID).Scan(&txType, &txAmount, &accID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTxNotFound
	}
	if err != nil {
		return nil, err
	}
	var taken bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM recurring_items WHERE transaction_id = $1)`, req.TransactionID).Scan(&taken); err != nil {
		return nil, err
	}
	if taken {
		return nil, ErrAlreadyLinked
	}
	if it.Draft.Type == nil || *it.Draft.Type != txType {
		return nil, ErrValidation
	}
	if it.PendingID != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM pending_transactions WHERE id = $1 AND user_id = $2`, *it.PendingID, userID); err != nil {
			return nil, err
		}
		it.PendingID = nil
	}
	now := time.Now()
	if it.ProducedAt == nil {
		it.ProducedAt = &now
	}
	it.TransactionID = &req.TransactionID
	a := txAmount
	it.Draft.Amount = &a
	it.Edited = true
	if err := saveItemTx(ctx, tx, it); err != nil {
		return nil, err
	}
	out := &LinkResult{TransactionAmount: math.Round(txAmount*100) / 100}
	if it.RuleID != nil {
		r, err := getRule(ctx, tx, userID, *it.RuleID, false)
		if err != nil {
			return nil, err
		}
		if r.Amount > 0 {
			ra := r.Amount
			out.RuleAmount = &ra
			out.AmountDiffers = math.Abs(r.Amount-txAmount) > 0.004
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	v, err := s.itemView(ctx, userID, itemID)
	if err != nil {
		return nil, err
	}
	out.Item = *v
	if s.txs != nil {
		if row, err := s.txs.Get(ctx, userID, req.TransactionID); err == nil {
			out.Transaction = row
		}
	}
	var touched []uuid.UUID
	if accID != nil {
		touched = append(touched, *accID)
	}
	if out.AccountBalances, err = shared.LoadWalletBalances(ctx, s.db, uniqueIDs(touched)); err != nil {
		return nil, err
	}
	return out, nil
}

var _ = transactions.TypeExpense

// POST /v1/recurring-items/:id/link
func (h *Handler) Link(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	var req LinkRequest
	if _, ok := bind(c, &req); !ok {
		return
	}
	out, err := h.service.Link(c.Request.Context(), uid, id, req)
	reply(c, http.StatusOK, out, err)
}
