package recurring

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/projects"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// WithPapers wires the split-bill papers: a round of a line paid by a person (คนจ่ายแทน) is written
// as quick create's จ่ายแทน is (0.4.3).
func (s *Service) WithPapers(p *projects.Service) *Service {
	s.papers = p
	return s
}

// recordPaidByTx — a round of a person-paid line: ONE paper the person paid in full and that is
// all mine (my share = the amount): my transaction in the item's category with no wallet money,
// and a debt to that person. The debt row belongs to the item (recurring_id). No wallet moves.
func (s *Service) recordPaidByTx(ctx context.Context, tx pgx.Tx, r *Rule, row *itemRow, date *time.Time, auto bool, lang string) ([]uuid.UUID, any, error) {
	if s.papers == nil || r.PaidByPersonName == nil {
		return nil, nil, ErrValidation
	}
	d := row.Draft
	amount := 0.0
	if d.Amount != nil {
		amount = *d.Amount
	}
	if amount <= 0 {
		return nil, nil, ErrAmountRequired // a paper needs an amount, auto-create too
	}
	slot := row.Date
	if row.Slot != nil {
		slot = *row.Slot
	}
	when := row.Date
	if date != nil {
		when = *date
	}
	desc := d.Description
	if desc == nil || *desc == "" {
		desc = ptr(occName(r, slot, seqPtr(seqIndex(r), slot), lang))
	}
	payer := projects.PaperPersonInput{Paid: amount}
	if r.PaidByContactID != nil {
		payer.ContactID = r.PaidByContactID
	} else {
		payer.PersonName = r.PaidByPersonName
	}
	req := projects.CreatePaperRequest{
		Paper: projects.PaperInput{Type: "expense", Amount: amount, Date: when.Format(dateLayout),
			Description: truncRunes(desc, 200), Note: d.Note, SplitMode: "custom",
			People: []projects.PaperPersonInput{{Me: true, Share: amount}, payer}},
		MyBooks: &projects.ClaimInput{Pay: "debt", CategoryID: d.CategoryID, TagIDs: d.TagIDs},
	}
	eff := shared.NewEffects()
	paperID, err := s.papers.CreatePaperTx(ctx, tx, r.UserID, nil, req, &eff)
	if err != nil {
		return nil, nil, err
	}
	books, err := transactions.PaperBooksTx(ctx, tx, paperID)
	if err != nil {
		return nil, nil, err
	}
	mine, ok := books[r.UserID]
	if !ok {
		return nil, nil, ErrValidation
	}
	if _, err := tx.Exec(ctx, `UPDATE personal_debts d SET recurring_id = $1 FROM personal_debt_bills b
		WHERE b.debt_id = d.id AND b.split_transaction_id = $2 AND d.user_id = $3`, r.RecurringID, paperID, r.UserID); err != nil {
		return nil, nil, err
	}
	now := time.Now()
	id := mine.ID
	row.TransactionID, row.ProducedAt = &id, &now
	row.Draft.Amount = &amount
	row.Archived, row.ClearedAt = false, nil
	if auto {
		row.AutoCreatedAt = &now
	}
	if err := saveItemTx(ctx, tx, row); err != nil {
		return nil, nil, err
	}
	return nil, nil, nil
}
