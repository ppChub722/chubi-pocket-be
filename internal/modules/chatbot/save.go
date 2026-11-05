package chatbot

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
)

// SaveRef says where a card's rows came from (pending_transactions.source_ref).
type SaveRef struct {
	ChatID    int64
	CardID    string
	MessageID int64 // the user's message the card answers
	Text      string
}

// Saver writes a card's items as pending rows (source = telegram) and settles them: ✓ confirms, ✗
// discards. Nil on the Bot = the fake mode, cards live in memory only.
type Saver interface {
	Save(ctx context.Context, userID uuid.UUID, ref SaveRef, items []Item) ([]uuid.UUID, error)
	Replace(ctx context.Context, userID uuid.UUID, old []uuid.UUID, ref SaveRef, items []Item) ([]uuid.UUID, error)
	Alive(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) (int, error)
	// Confirm books the rows; saved = how many went through (a failed row stays pending in the app).
	Confirm(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) (saved int, err error)
	Discard(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) error
}

// PendingSaver is the Saver over the pending service.
type PendingSaver struct{ svc *pending.Service }

func NewPendingSaver(svc *pending.Service) *PendingSaver { return &PendingSaver{svc: svc} }

func drafts(items []Item) []pending.Draft {
	out := make([]pending.Draft, 0, len(items))
	for _, it := range items {
		typ, amount, date := it.Type, it.Amount, it.Date
		out = append(out, pending.Draft{Type: &typ, Amount: &amount, AccountID: it.AccountID, CategoryID: it.CategoryID,
			Date: &date, Description: it.Description, Note: it.Note, TransferToAccountID: it.TransferToID})
	}
	return out
}

func refJSON(r SaveRef) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"channel": "telegram", "chat_id": r.ChatID, "card_id": r.CardID,
		"message_id": r.MessageID, "text": r.Text})
	return b
}

func (p *PendingSaver) Save(ctx context.Context, userID uuid.UUID, ref SaveRef, items []Item) ([]uuid.UUID, error) {
	return p.svc.CreateFrom(ctx, userID, pending.SourceTelegram, refJSON(ref), drafts(items))
}

func (p *PendingSaver) Replace(ctx context.Context, userID uuid.UUID, old []uuid.UUID, ref SaveRef, items []Item) ([]uuid.UUID, error) {
	return p.svc.Replace(ctx, userID, old, pending.SourceTelegram, refJSON(ref), drafts(items))
}

func (p *PendingSaver) Alive(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) (int, error) {
	return p.svc.Alive(ctx, userID, ids)
}

func (p *PendingSaver) Confirm(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) (int, error) {
	res, err := p.svc.Submit(ctx, userID, pending.SubmitRequest{IDs: ids})
	if err != nil {
		return 0, err
	}
	return len(res.Submitted), nil
}

func (p *PendingSaver) Discard(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) error {
	_, err := p.svc.Discard(ctx, userID, ids)
	return err
}
