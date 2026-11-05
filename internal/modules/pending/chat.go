package pending

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// SourceTelegram — rows the Telegram chatbot writes. The app treats it like manual (a person typed it).
const SourceTelegram = "telegram"

// BookResult says how many rows were booked and how many of those could not be tied to their recurring slot.
type BookResult struct{ Booked, Unlinked int }

// ErrBookFailed — Book could not book every row; the rows that failed are gone, nothing is left pending.
var ErrBookFailed = errors.New("pending: booking failed")

// BookError is ErrBookFailed with the code of the first row that failed (for example INSUFFICIENT_SAVING).
type BookError struct{ Code string }

func (e *BookError) Error() string { return "pending: booking failed: " + e.Code }
func (e *BookError) Unwrap() error { return ErrBookFailed }

// Check lists, per draft, what it still needs before it can be confirmed (the pending page's own
// readiness check, hard ones only). An empty list = ready. A saving draft is a `saving` row, any other a `create`.
func (s *Service) Check(drafts []Draft) [][]string {
	out := make([][]string, len(drafts))
	for i, d := range drafts {
		kind := KindCreate
		if d.Type != nil && *d.Type == string(typeSaving) {
			kind = KindSaving
		}
		_, hard := readinessDetail(&PendingTransaction{Kind: kind, Draft: d})
		out[i] = hard
	}
	return out
}

// CreateFrom inserts one pending row per draft with a source and source_ref, all or nothing: the user
// reviews them on the pending page.
func (s *Service) CreateFrom(ctx context.Context, userID uuid.UUID, source string, ref json.RawMessage, drafts []Draft) ([]uuid.UUID, error) {
	for _, d := range drafts {
		if err := checkDraft(d); err != nil {
			return nil, err
		}
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	ids := make([]uuid.UUID, 0, len(drafts))
	for _, d := range drafts {
		kind := KindCreate
		if d.Type != nil && *d.Type == string(typeSaving) {
			kind = KindSaving // a saving entry is booked by the saving service, never as a transaction
		}
		p, err := s.store.InsertTx(ctx, tx, userID, source, kind, d, ref)
		if err != nil {
			return nil, fmt.Errorf("insert row: %w", err)
		}
		ids = append(ids, p.ID)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return ids, nil
}

// Book creates the rows and submits them at once (the user's "auto submit"): straight to the ledger.
// Each row books in its own transaction, so a row that fails cannot undo the ones before it; the
// failed rows are deleted (nothing stays pending) and ErrBookFailed comes back with the result so far.
func (s *Service) Book(ctx context.Context, userID uuid.UUID, source string, ref json.RawMessage, drafts []Draft) (BookResult, error) {
	ids, err := s.CreateFrom(ctx, userID, source, ref, drafts)
	if err != nil {
		return BookResult{}, err
	}
	res, err := s.Submit(ctx, userID, SubmitRequest{IDs: ids})
	if err != nil {
		_, _ = s.Discard(ctx, userID, ids)
		return BookResult{}, err
	}
	out := BookResult{Booked: len(res.Submitted)}
	for _, sub := range res.Submitted {
		if sub.Unlinked {
			out.Unlinked++
		}
	}
	if len(res.Failed) == 0 {
		return out, nil
	}
	failed := make([]uuid.UUID, 0, len(res.Failed))
	for _, f := range res.Failed {
		failed = append(failed, f.ID)
	}
	if _, derr := s.Discard(ctx, userID, failed); derr != nil {
		return out, errors.Join(ErrBookFailed, derr)
	}
	return out, &BookError{Code: string(res.Failed[0].Error.Code)}
}
