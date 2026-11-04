package transactions

import (
	"context"

	"github.com/google/uuid"
)

// PaperReader is what the transactions module asks of projects: the paper this row is my book of,
// as I see it, so GET /transactions/:id carries it (one call for the tx page, owner 2026-11-04).
type PaperReader interface {
	PaperFor(ctx context.Context, userID, paperID uuid.UUID) (any, error)
}

func (s *Service) WithPaperReader(r PaperReader) { s.papers = r }

// withPaper fills d.Paper when the row is my book of a bill. A paper that can't be read leaves it
// out: the row is still shown, the app reads the paper on its own.
func (s *Service) withPaper(ctx context.Context, userID uuid.UUID, d *TransactionDetail) {
	if s.papers == nil {
		return
	}
	ref := d.SplitBill
	if ref == nil {
		ref = d.EventBill
	}
	if ref == nil {
		return
	}
	if p, err := s.papers.PaperFor(ctx, userID, ref.ID); err == nil {
		d.Paper = p
	}
}
