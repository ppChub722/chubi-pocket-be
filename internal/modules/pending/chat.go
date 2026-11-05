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

// CreateFrom inserts one pending row per draft with a source and source_ref, all or nothing.
func (s *Service) CreateFrom(ctx context.Context, userID uuid.UUID, source string, ref json.RawMessage, drafts []Draft) ([]uuid.UUID, error) {
	return s.replace(ctx, userID, nil, source, ref, drafts)
}

// Replace deletes the rows oldIDs (those still there) and inserts the drafts in their place, in one
// database transaction: a Reply edit on a chat card.
func (s *Service) Replace(ctx context.Context, userID uuid.UUID, oldIDs []uuid.UUID, source string, ref json.RawMessage, drafts []Draft) ([]uuid.UUID, error) {
	return s.replace(ctx, userID, oldIDs, source, ref, drafts)
}

func (s *Service) replace(ctx context.Context, userID uuid.UUID, oldIDs []uuid.UUID, source string, ref json.RawMessage, drafts []Draft) ([]uuid.UUID, error) {
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
	for _, id := range oldIDs {
		if err := s.store.DeleteTx(ctx, tx, userID, id); err != nil {
			return nil, fmt.Errorf("delete old row: %w", err)
		}
	}
	ids := make([]uuid.UUID, 0, len(drafts))
	for _, d := range drafts {
		p, err := s.store.InsertTx(ctx, tx, userID, source, KindCreate, d, ref)
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

// Alive counts how many of ids are still pending (a row confirmed or discarded in the app is gone).
func (s *Service) Alive(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) (int, error) {
	n := 0
	for _, id := range ids {
		if _, err := s.store.Get(ctx, userID, id); err != nil {
			if errors.Is(err, ErrPendingNotFound) {
				continue
			}
			return 0, err
		}
		n++
	}
	return n, nil
}
