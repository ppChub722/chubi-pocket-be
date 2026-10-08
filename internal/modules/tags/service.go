package tags

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AttachRequest carries the tag IDs to attach to a transaction.
type AttachRequest struct {
	TagIDs []uuid.UUID `json:"tag_ids" binding:"required,min=1,dive,required"`
}

type Service struct {
	store *Store
}

func NewService(s *Store) *Service {
	return &Service{store: s}
}

func (s *Service) Create(ctx context.Context, userID uuid.UUID, req CreateTagRequest) (*Tag, error) {
	return s.store.Create(ctx, userID, strings.TrimSpace(req.Name), req.IconCode)
}

func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (*Tag, error) {
	return s.store.GetByID(ctx, userID, id)
}

func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]Tag, error) {
	return s.store.List(ctx, userID)
}

func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, req UpdateTagRequest) (*Tag, error) {
	var name *string
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		name = &trimmed
	}
	if _, err := s.store.Update(ctx, userID, id, name, req.IconCode); err != nil {
		return nil, err
	}
	// Return via GetByID so usage_count is populated consistently.
	return s.store.GetByID(ctx, userID, id)
}

func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	return s.store.Delete(ctx, userID, id)
}

// AttachTags validates ownership of the transaction and all tag IDs, then
// inserts (transaction_id, tag_id) rows. Returns the full attached-tag list.
func (s *Service) AttachTags(ctx context.Context, userID, txID uuid.UUID, tagIDs []uuid.UUID) ([]Tag, error) {
	if err := s.store.IsTransactionOwnedByUser(ctx, txID, userID); err != nil {
		return nil, err
	}
	if err := s.store.VerifyOwnership(ctx, userID, tagIDs); err != nil {
		return nil, err
	}
	if err := s.store.AttachTags(ctx, txID, userID, tagIDs); err != nil {
		return nil, err
	}
	return s.store.TagsForTransaction(ctx, txID, userID)
}

// DetachTag removes a single tag from a transaction. Idempotent — if the
// pairing didn't exist, returns success.
func (s *Service) DetachTag(ctx context.Context, userID, txID, tagID uuid.UUID) error {
	if err := s.store.IsTransactionOwnedByUser(ctx, txID, userID); err != nil {
		return err
	}
	return s.store.DetachTag(ctx, txID, tagID)
}

// AttachTagsTx attaches inside the caller's DB transaction — used when a
// pending draft is submitted, so the transaction and its tags land (or
// roll back) together. Ownership of the tags is checked first.
func (s *Service) AttachTagsTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID, tagIDs []uuid.UUID) error {
	if len(tagIDs) == 0 {
		return nil
	}
	if err := s.store.VerifyOwnership(ctx, userID, tagIDs); err != nil {
		return err
	}
	for _, tagID := range tagIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO transaction_tags (transaction_id, tag_id, created_by_user_id)
			VALUES ($1, $2, $3)
			ON CONFLICT (transaction_id, tag_id) DO NOTHING`,
			txID, tagID, userID); err != nil {
			return fmt.Errorf("attach tag %s: %w", tagID, err)
		}
	}
	return nil
}
