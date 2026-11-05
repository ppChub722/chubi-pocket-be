package pending

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/saving_goals"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Debt sides of a debt draft.
const (
	SideTheyOweMe = "they_owe_me"
	SideIOweThem  = "i_owe_them"
	SideTheyRepay = "they_repay"
	SideIRepay    = "i_repay"
)

// Saving actions of a saving draft ("" = save).
const (
	ActionSave     = "save"
	ActionAllocate = "allocate"
	ActionMove     = "move"
	ActionWithdraw = "withdraw"
)

// RecurringLink links a booked transaction to the next slot of a recurring rule ("จ่ายค่าไฟ 500").
type RecurringLink func(ctx context.Context, userID, ruleID, txID uuid.UUID) error

func (s *Service) WithRecurringLink(fn RecurringLink) { s.recurringLink = fn }

// entryRequest turns a saving draft that is not a plain save into the saving service's request:
// allocate = spare -> goal, move = goal -> goal / spare, withdraw = back to the wallet it names.
func entryRequest(d Draft, note *string) saving_goals.EntryRequest {
	r := saving_goals.EntryRequest{Amount: *d.Amount, Date: d.Date, Note: note}
	switch d.SavingAction {
	case ActionAllocate:
		r.Kind, r.ToGoalID = saving_goals.KindMove, d.GoalID
	case ActionMove:
		r.Kind, r.GoalID, r.ToGoalID = saving_goals.KindMove, d.FromGoalID, d.GoalID
	case ActionWithdraw:
		r.Kind, r.GoalID = saving_goals.KindWithdraw, d.FromGoalID
		if d.AccountID != nil {
			r.Splits = []saving_goals.Split{{WalletID: *d.AccountID, Amount: *d.Amount}}
		}
	}
	return r
}

// submitDebt books a debt draft in the person book (with a transaction when it says so), or a repayment.
func (s *Service) submitDebt(ctx context.Context, userID uuid.UUID, p *PendingTransaction) (*uuid.UUID, error) {
	if s.debts == nil {
		return nil, errors.New("debts not available")
	}
	d, db := p.Draft, p.Draft.Debt
	if d.Amount == nil || *d.Amount <= 0 {
		return nil, &submitErr{apierr.MissingAmount, "amount must be more than 0"}
	}
	if d.Date == nil {
		return nil, &submitErr{apierr.MissingDate, "date is required"}
	}
	name := ""
	if db.PersonName != nil {
		name = strings.TrimSpace(*db.PersonName)
	}
	if db.ContactID == nil && name == "" {
		return nil, &submitErr{apierr.ValidationError, "a debt needs a person"}
	}
	var nameArg *string
	if db.ContactID == nil {
		nameArg = &name
	}
	money := db.WithMoney
	var account *uuid.UUID
	if money {
		account = d.AccountID
	}
	key := shared.DebtKey(db.ContactID, name)
	var res *personal_debts.WriteResult
	var err error
	switch db.Side {
	case SideTheyOweMe, SideIOweThem:
		dir := "received"
		if db.Side == SideTheyOweMe {
			dir = "paid"
		}
		res, err = s.debts.CreateEntry(ctx, userID, key, personal_debts.CreateEntryRequest{
			Direction: dir, Amount: *d.Amount, Date: d.Date, AccountID: account, CreateTransaction: &money,
			Description: d.Description, Note: d.Note, TagIDs: d.TagIDs, PersonName: nameArg,
		})
	default:
		kind := "pay"
		if db.Side == SideTheyRepay {
			kind = "receive"
		}
		res, err = s.debts.CreateRepayment(ctx, userID, key, personal_debts.RepaymentRequest{
			Kind: kind, Amount: *d.Amount, Date: d.Date, AccountID: account, CreateTransaction: &money,
			Description: d.Description, Note: d.Note, TagIDs: d.TagIDs, PersonName: nameArg,
		})
	}
	if err != nil {
		return nil, err
	}
	_ = s.store.Delete(ctx, userID, p.ID)
	if res != nil && res.Transaction != nil {
		return &res.Transaction.ID, nil
	}
	return nil, nil
}
