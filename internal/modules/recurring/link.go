package recurring

import (
	"context"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

var ErrTxNotFound = errors.New("transaction not found")

type LinkRequest struct {
	TransactionID uuid.UUID `json:"transaction_id"`
	// Replace — swap (see LinkSlotRequest).
	Replace bool `json:"replace"`
}

type LinkResult struct {
	Item              Item                   `json:"item"`
	Transaction       any                    `json:"transaction"`
	AmountDiffers     bool                   `json:"amount_differs"`
	RuleAmount        *float64               `json:"rule_amount"`
	TransactionAmount float64                `json:"transaction_amount"`
	AccountBalances   []shared.WalletBalance `json:"account_balances"`
	Setting           *Recurring             `json:"setting"`
}

// Link — the old call by item id (a stored row): it becomes LinkOcc at the row's slot.
func (s *Service) Link(ctx context.Context, userID, itemID uuid.UUID, req LinkRequest) (*LinkResult, error) {
	it, err := getItem(ctx, s.db, userID, itemID, false)
	if err != nil {
		return nil, err
	}
	if it.RuleID == nil {
		return nil, ErrItemNotFound
	}
	slot := it.Date
	if it.Slot != nil {
		slot = *it.Slot
	}
	return s.linkOcc(ctx, userID, *it.RuleID, slot, LinkSlotRequest{TransactionID: req.TransactionID, Replace: req.Replace}, "", it)
}

// LinkOcc makes an existing transaction the record of the occurrence: one
// occurrence ↔ one transaction. The transaction keeps its own date and amount
// (early / late payment is fine). An occurrence that already has a transaction
// (or a transaction that is another occurrence's) needs Replace, else the 409
// says which.
func (s *Service) LinkOcc(ctx context.Context, userID, ruleID uuid.UUID, slot time.Time, req LinkSlotRequest, tz string) (*LinkResult, error) {
	return s.linkOcc(ctx, userID, ruleID, slot, req, tz, nil)
}

// linkOcc — byRow set: the occurrence IS that stored row (a manual item has no slot
// of its own, so it can only be reached by its id).
func (s *Service) linkOcc(ctx context.Context, userID, ruleID uuid.UUID, slot time.Time, req LinkSlotRequest, tz string, byRow *itemRow) (*LinkResult, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	r, o, err := s.occOf(ctx, tx, userID, ruleID, slot, tz, byRow)
	if err != nil {
		return nil, err
	}
	if o.status == StatusSkipped {
		return nil, ErrInvalidTransition
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
	if o.draft.Type == nil || *o.draft.Type != txType {
		return nil, ErrValidation
	}
	// the transaction may belong to another occurrence
	var otherID *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM recurring_items WHERE transaction_id = $1 AND user_id = $2 FOR UPDATE`,
		req.TransactionID, userID).Scan(&otherID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if otherID != nil && (o.row == nil || *otherID != o.row.ID) {
		other, err := getItem(ctx, tx, userID, *otherID, true)
		if err != nil {
			return nil, err
		}
		if !req.Replace {
			os := other.Date
			if other.Slot != nil {
				os = *other.Slot
			}
			return nil, &LinkConflict{Other: &OccRef{RuleID: *other.RuleID, Slot: os.Format(dateLayout)}}
		}
		if _, err := s.release(ctx, tx, other); err != nil {
			return nil, err
		}
	}
	if o.row != nil && o.row.TransactionID != nil && *o.row.TransactionID != req.TransactionID && !req.Replace {
		lt := &LinkedTx{ID: *o.row.TransactionID}
		var d *string
		var amt float64
		var dd string
		if err := tx.QueryRow(ctx, `SELECT description, amount::float8, to_char(date, 'YYYY-MM-DD') FROM transactions WHERE id = $1`,
			*o.row.TransactionID).Scan(&d, &amt, &dd); err == nil {
			lt.Description, lt.Amount, lt.Date = d, &amt, &dd
		}
		return nil, &LinkConflict{Linked: lt}
	}
	row, err := s.store(ctx, tx, o)
	if err != nil {
		return nil, err
	}
	if row.PendingID != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM pending_transactions WHERE id = $1 AND user_id = $2`, *row.PendingID, userID); err != nil {
			return nil, err
		}
		row.PendingID = nil
	}
	now := time.Now()
	row.TransactionID, row.AutoCreatedAt, row.Archived = &req.TransactionID, nil, false
	if row.ProducedAt == nil {
		row.ProducedAt = &now
	}
	if err := saveItemTx(ctx, tx, row); err != nil {
		return nil, err
	}
	out := &LinkResult{TransactionAmount: math.Round(txAmount*100) / 100}
	if r.Amount > 0 {
		ra := r.Amount
		out.RuleAmount = &ra
		out.AmountDiffers = math.Abs(r.Amount-txAmount) > 0.004
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	it, err := s.itemAfter(ctx, userID, ruleID, slot, byRow)
	if err != nil {
		return nil, err
	}
	out.Item = *it
	out.Setting = s.settingOf(ctx, userID, ruleOf(it))
	if s.txs != nil {
		if txRow, err := s.txs.Get(ctx, userID, req.TransactionID); err == nil {
			out.Transaction = txRow
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

type UnlinkResult struct {
	Item    Item       `json:"item"`
	Setting *Recurring `json:"setting"`
}

// Unlink — the old call by item id.
func (s *Service) Unlink(ctx context.Context, userID, itemID uuid.UUID) (*UnlinkResult, error) {
	it, err := getItem(ctx, s.db, userID, itemID, false)
	if err != nil {
		return nil, err
	}
	if it.RuleID == nil {
		return nil, ErrItemNotFound
	}
	slot := it.Date
	if it.Slot != nil {
		slot = *it.Slot
	}
	return s.unlinkOcc(ctx, userID, *it.RuleID, slot, "", it)
}

// UnlinkOcc lets go of the occurrence's transaction (the transaction stays):
// the occurrence goes back to computed (due / upcoming), or to paid when it was
// cleared first.
func (s *Service) UnlinkOcc(ctx context.Context, userID, ruleID uuid.UUID, slot time.Time, tz string) (*UnlinkResult, error) {
	return s.unlinkOcc(ctx, userID, ruleID, slot, tz, nil)
}

func (s *Service) unlinkOcc(ctx context.Context, userID, ruleID uuid.UUID, slot time.Time, tz string, byRow *itemRow) (*UnlinkResult, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	_, o, err := s.occOf(ctx, tx, userID, ruleID, slot, tz, byRow)
	if err != nil {
		return nil, err
	}
	if o.row == nil || o.row.TransactionID == nil {
		return nil, ErrValidation
	}
	row, err := getItem(ctx, tx, userID, o.row.ID, true)
	if err != nil {
		return nil, err
	}
	if _, err := s.release(ctx, tx, row); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	it, err := s.itemAfter(ctx, userID, ruleID, slot, byRow)
	if err != nil {
		return nil, err
	}
	return &UnlinkResult{Item: *it, Setting: s.settingOf(ctx, userID, ruleOf(it))}, nil
}

func (h *Handler) Unlink(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.Unlink(c.Request.Context(), uid, id)
	reply(c, http.StatusOK, out, err)
}
