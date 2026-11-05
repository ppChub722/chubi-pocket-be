package chatbot

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/projects"
)

// groupTx is the setting group of "create a transaction" (auto submit, per group).
const groupTx = "tx"

// SaveRef says where a card's rows came from (pending_transactions.source_ref). No message text:
// the database keeps no user text.
type SaveRef struct {
	ChatID    int64
	CardID    string
	MessageID int64 // the user's message the card answers
}

// BookOutcome is how a Book went: rows booked, and how many of those could not be tied to their recurring slot.
type BookOutcome struct{ Booked, Unlinked int }

// Saver settles a card. Nothing is written before ✓: Queue puts the rows on the pending page for the
// user to review (source = telegram), Book writes them to the ledger at once (the user's auto
// submit). Nil on the Bot = the fake mode, cards live in memory only.
type Saver interface {
	// Check lists, per item, what is still missing before it can be confirmed (empty = ready).
	Check(items []Item) [][]string
	// Auto: the user turned auto submit on for this group.
	Auto(ctx context.Context, userID uuid.UUID, group string) bool
	Queue(ctx context.Context, userID uuid.UUID, ref SaveRef, items []Item) error
	// Book returns how many rows were booked; an error with Booked > 0 means only part went through.
	Book(ctx context.Context, userID uuid.UUID, ref SaveRef, items []Item) (BookOutcome, error)
}

// PendingSaver is the Saver over the pending service.
type PendingSaver struct{ svc *pending.Service }

func NewPendingSaver(svc *pending.Service) *PendingSaver { return &PendingSaver{svc: svc} }

func ptr[T any](v T) *T { return &v }

// person turns a person of a bill into the paper's input; paid is what they paid.
func person(p PersonRef, paid float64) projects.PaperPersonInput {
	in := projects.PaperPersonInput{MemberID: p.MemberID, ContactID: p.ContactID, Paid: paid}
	if p.MemberID == nil && p.ContactID == nil && p.Name != "" {
		in.PersonName = ptr(p.Name)
	}
	return in
}

// paperOf builds the draft's paper: me, the others, who paid (the payer paid it all), equal split.
func paperOf(p *PaperItem, amount float64) *pending.DraftPaper {
	out := &pending.DraftPaper{ProjectID: p.EventID, SplitMode: "equal", Pay: "wallet"}
	if p.NewEvent != "" {
		out.NewEvent = &projects.NewEventInput{Name: p.NewEvent}
	}
	me := projects.PaperPersonInput{Me: true}
	if p.PaidBy == nil {
		me.Paid = amount
	} else {
		out.Pay = "debt" // someone else paid: I owe them my share
	}
	out.People = append(out.People, me)
	for _, x := range p.People {
		paid := 0.0
		if p.PaidBy != nil && hasPerson([]PersonRef{*p.PaidBy}, x) {
			paid = amount
		}
		out.People = append(out.People, person(x, paid))
	}
	return out
}

// drafts maps the checked items to pending drafts: the row kind is the draft's own (saving, debt, paper, recurring).
func drafts(items []Item) []pending.Draft {
	out := make([]pending.Draft, 0, len(items))
	for _, it := range items {
		amount, date := it.Amount, it.Date
		d := pending.Draft{Amount: &amount, Date: &date, AccountID: it.AccountID, CategoryID: it.CategoryID,
			Description: it.Description, Note: it.Note, TransferToAccountID: it.TransferToID}
		switch it.Kind {
		case KindSaving:
			d.Type = ptr("saving")
			d.SavingAction, d.GoalID, d.FromGoalID = it.SavingAction, it.GoalID, it.FromGoalID
			d.CategoryID, d.TransferToAccountID = nil, nil
		case KindDebt:
			db := it.Debt
			dd := &pending.DraftDebt{Side: db.Side, WithMoney: db.WithMoney}
			if db.ContactID != nil {
				dd.ContactID = db.ContactID
			} else if db.Name != "" {
				dd.PersonName = ptr(db.Name)
			}
			d.Debt, d.CategoryID = dd, nil
		default:
			d.Type = ptr(it.Type)
			d.RecurringID = it.RecurringID
			if it.Paper != nil {
				d.Paper = paperOf(it.Paper, it.Amount)
			}
		}
		out = append(out, d)
	}
	return out
}

func refJSON(r SaveRef) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"channel": "telegram", "chat_id": r.ChatID, "card_id": r.CardID, "message_id": r.MessageID})
	return b
}

// Check is the pending page's readiness plus what a chat row needs on top: a saving that touches a wallet names
// it (the main wallet is for transactions), and a bill has someone to share with.
func (p *PendingSaver) Check(items []Item) [][]string {
	out := p.svc.Check(drafts(items))
	for i, it := range items {
		switch {
		case it.Kind == KindSaving && (it.SavingAction == ActSave || it.SavingAction == ActWithdraw) && it.AccountID == nil:
			out[i] = append(out[i], "wallet")
		case it.Paper != nil && len(it.Paper.People) == 0:
			out[i] = append(out[i], "people")
		}
	}
	return out
}

// Auto is off for everyone until the user setting exists (migration + app switch).
func (p *PendingSaver) Auto(context.Context, uuid.UUID, string) bool { return false }

func (p *PendingSaver) Queue(ctx context.Context, userID uuid.UUID, ref SaveRef, items []Item) error {
	_, err := p.svc.CreateFrom(ctx, userID, pending.SourceTelegram, refJSON(ref), drafts(items))
	return err
}

func (p *PendingSaver) Book(ctx context.Context, userID uuid.UUID, ref SaveRef, items []Item) (BookOutcome, error) {
	res, err := p.svc.Book(ctx, userID, pending.SourceTelegram, refJSON(ref), drafts(items))
	return BookOutcome{Booked: res.Booked, Unlinked: res.Unlinked}, err
}
