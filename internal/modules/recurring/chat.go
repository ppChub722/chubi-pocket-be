package recurring

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNoSlot — the rule has no slot left to record (a finished installment, nothing due or upcoming).
var ErrNoSlot = errors.New("recurring: no slot to record")

// ChatRule is what the chatbot needs of a rule: its name to match, and the defaults a payment takes.
type ChatRule struct {
	ID          uuid.UUID
	Name        string
	Type        string // expense | income | transfer
	Amount      float64 // 0 = the rule has no amount
	AccountID   *uuid.UUID
	CategoryID  *uuid.UUID
	Description *string
}

// ChatRules lists the rules a transaction can be tied to: active or paused, not a saving (that is
// recorded by confirming) and not a debt (its slot takes a person-book entry).
func (s *Service) ChatRules(ctx context.Context, userID uuid.UUID) ([]ChatRule, error) {
	rules, err := s.activeRules(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	out := make([]ChatRule, 0, len(rules))
	for _, r := range rules {
		if r.Type == typeSaving || r.isDebt() {
			continue
		}
		out = append(out, ChatRule{ID: r.ID, Name: r.Name, Type: r.Type, Amount: r.Amount, AccountID: r.AccountID,
			CategoryID: r.CategoryID, Description: r.Description})
	}
	return out, nil
}

// PickSlot chooses the slot a payment records (owner 2026-11-05): the latest slot that has passed and is
// still unpaid (not recorded, not skipped); when every past one is settled, the next upcoming slot.
// ok false = nothing to record.
func (s *Service) PickSlot(ctx context.Context, userID, ruleID uuid.UUID, today time.Time) (time.Time, bool, error) {
	rules, err := s.rulesByID(ctx, s.db, userID, []uuid.UUID{ruleID})
	if err != nil {
		return time.Time{}, false, err
	}
	r, ok := rules[ruleID]
	if !ok {
		return time.Time{}, false, nil
	}
	occs, err := s.occsInRange(ctx, s.db, userID, []*Rule{r}, today.AddDate(-1, 0, 0), today.AddDate(1, 0, 0), today, true)
	if err != nil {
		return time.Time{}, false, err
	}
	var past, next *occ
	for _, o := range occs { // sorted by date
		switch {
		case o.status == StatusDue && !o.date.After(today):
			past = o // the last one wins: the latest
		case o.status == StatusUpcoming && next == nil:
			next = o
		}
	}
	switch {
	case past != nil:
		return past.slot, true, nil
	case next != nil:
		return next.slot, true, nil
	}
	return time.Time{}, false, nil
}

// LinkNext makes txID the record of the slot PickSlot chooses (the tx keeps its own date and amount).
func (s *Service) LinkNext(ctx context.Context, userID, ruleID, txID uuid.UUID) error {
	today, err := s.today(ctx, userID, "")
	if err != nil {
		return err
	}
	slot, ok, err := s.PickSlot(ctx, userID, ruleID, today)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNoSlot
	}
	_, err = s.LinkOcc(ctx, userID, ruleID, slot, LinkSlotRequest{TransactionID: txID}, "")
	return err
}
