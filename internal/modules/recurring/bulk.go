package recurring

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// One action on picked occurrences of any settings ("ยืนยัน / ข้าม / จ่ายแล้วที่เลือก").

const (
	BulkConfirm  = "confirm"
	BulkSkip     = "skip"
	BulkMarkPaid = "mark_paid"

	bulkMaxItems = 100
)

type OccBulkRequest struct {
	Action string        `json:"action"`
	Items  []OccBulkItem `json:"items"`
}

type OccBulkItem struct {
	RuleID uuid.UUID `json:"rule_id"`
	Slot   string    `json:"slot"`
	// Amount — confirm only: the amount typed for an occurrence that has none (or overrides it).
	Amount *float64 `json:"amount"`
}

type OccLeftOut struct {
	RuleID uuid.UUID `json:"rule_id"`
	Slot   string    `json:"slot"`
	Reason string    `json:"reason"` // needs_amount | refused | not_found | already_recorded
}

type OccBulkResult struct {
	Items           []Item                 `json:"items"`
	LeftOut         []OccLeftOut           `json:"left_out"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
	Settings        []Recurring            `json:"settings"`
}

func validateBulk(req *OccBulkRequest) ([]time.Time, error) {
	switch req.Action {
	case BulkConfirm, BulkSkip, BulkMarkPaid:
	default:
		return nil, fmt.Errorf("%w: action must be confirm, skip or mark_paid", ErrValidation)
	}
	if len(req.Items) == 0 || len(req.Items) > bulkMaxItems {
		return nil, fmt.Errorf("%w: items must hold 1 to %d occurrences", ErrValidation, bulkMaxItems)
	}
	slots := make([]time.Time, len(req.Items))
	for i, it := range req.Items {
		if it.RuleID == uuid.Nil {
			return nil, fmt.Errorf("%w: items[%d].rule_id is required", ErrValidation, i)
		}
		d, err := time.Parse(dateLayout, it.Slot)
		if err != nil {
			return nil, fmt.Errorf("%w: items[%d].slot must be YYYY-MM-DD", ErrValidation, i)
		}
		if it.Amount != nil && (req.Action != BulkConfirm || *it.Amount <= 0) {
			return nil, fmt.Errorf("%w: items[%d].amount must be > 0 and only comes with confirm", ErrValidation, i)
		}
		slots[i] = d
	}
	return slots, nil
}

// SettleOccurrences runs one action on the picked occurrences in one DB
// transaction, each on its own savepoint: one that can't take it is left out
// with the reason, the others still go through. Confirm needs an amount (the
// occurrence's own or the one sent); skip and mark_paid take any open one.
func (s *Service) SettleOccurrences(ctx context.Context, userID uuid.UUID, req OccBulkRequest, tz string) (*OccBulkResult, error) {
	slots, err := validateBulk(&req)
	if err != nil {
		return nil, err
	}
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	lang := s.lang(ctx, userID)
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	out := &OccBulkResult{Items: []Item{}, LeftOut: []OccLeftOut{}, Settings: []Recurring{}}
	rules := map[uuid.UUID]*Rule{}
	var touched []uuid.UUID
	type done struct {
		rule uuid.UUID
		slot time.Time
	}
	var settled []done
	for i, it := range req.Items {
		r, ok := rules[it.RuleID]
		if !ok {
			r, err = getRule(ctx, tx, userID, it.RuleID, false)
			if errors.Is(err, ErrNotFound) {
				r = nil
			} else if err != nil {
				return nil, err
			}
			rules[it.RuleID] = r
		}
		reason := "not_found"
		if r != nil {
			var t []uuid.UUID
			t, reason, err = s.settleOne(ctx, tx, r, slots[i], req.Action, it.Amount, today, lang)
			if err != nil {
				return nil, err
			}
			touched = append(touched, t...)
		}
		if reason != "" {
			out.LeftOut = append(out.LeftOut, OccLeftOut{RuleID: it.RuleID, Slot: it.Slot, Reason: reason})
			continue
		}
		settled = append(settled, done{it.RuleID, slots[i]})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	if out.AccountBalances, err = shared.LoadWalletBalances(ctx, s.db, uniqueIDs(touched)); err != nil {
		return nil, err
	}
	var hit []*Rule
	seen := map[uuid.UUID]bool{}
	for _, d := range settled {
		item, err := s.slotItem(ctx, userID, d.rule, d.slot)
		if err != nil && !errors.Is(err, ErrItemNotFound) {
			return nil, err
		}
		if item != nil {
			out.Items = append(out.Items, *item)
		}
		if !seen[d.rule] {
			seen[d.rule] = true
			hit = append(hit, rules[d.rule])
		}
	}
	if len(hit) > 0 {
		views, err := s.recurringViews(ctx, userID, today, hit)
		if err != nil {
			return nil, err
		}
		out.Settings = append(out.Settings, views...)
	}
	return out, nil
}

// settleOne applies the action to one occurrence on a savepoint. reason is
// empty when it went through, else why it was left out (the savepoint is
// rolled back); err is only for a failure that breaks the whole call.
func (s *Service) settleOne(ctx context.Context, tx pgx.Tx, r *Rule, slot time.Time, action string, amount *float64, today time.Time, lang string) ([]uuid.UUID, string, error) {
	o, err := s.loadOcc(ctx, tx, r.UserID, r, slot, today)
	if errors.Is(err, ErrItemNotFound) {
		return nil, "not_found", nil
	}
	if err != nil {
		return nil, "", err
	}
	if o.status == StatusDone {
		return nil, "already_recorded", nil
	}
	if action == BulkConfirm && amount == nil && (o.draft.Amount == nil || *o.draft.Amount <= 0) {
		return nil, "needs_amount", nil
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	touched, reason := s.settleRow(ctx, sp, r, o, action, amount, lang)
	if reason != "" {
		if err := sp.Rollback(ctx); err != nil {
			return nil, "", err
		}
		return nil, reason, nil
	}
	return touched, "", sp.Commit(ctx)
}

func (s *Service) settleRow(ctx context.Context, sp pgx.Tx, r *Rule, o *occ, action string, amount *float64, lang string) ([]uuid.UUID, string) {
	row, err := s.store(ctx, sp, o)
	if err != nil {
		return nil, "refused"
	}
	if row.TransactionID != nil || row.SavingEntryID != nil {
		return nil, "already_recorded"
	}
	switch action {
	case BulkConfirm:
		if amount != nil {
			row.Draft.Amount, row.Edited = amount, true
		}
		touched, _, err := s.recordOccurrenceTx(ctx, sp, r, row, nil, false, lang)
		switch {
		case errors.Is(err, ErrAlreadyRecorded):
			return nil, "already_recorded"
		case errors.Is(err, ErrAmountRequired):
			return nil, "needs_amount"
		case err != nil:
			return nil, "refused"
		}
		return touched, ""
	case BulkSkip:
		if row.PendingID != nil { // a legacy draft must not outlive the skip
			if err := s.pending.Store().DeleteTx(ctx, sp, r.UserID, *row.PendingID); err != nil {
				return nil, "refused"
			}
			row.PendingID = nil
		}
		row.AutoCreatedAt, row.Archived, row.ClearedAt = nil, true, nil
	default: // BulkMarkPaid
		now := time.Now()
		row.Archived, row.ClearedAt = false, &now
	}
	if err := saveItemTx(ctx, sp, row); err != nil {
		return nil, "refused"
	}
	return nil, ""
}
