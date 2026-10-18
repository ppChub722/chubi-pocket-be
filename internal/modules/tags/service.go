package tags

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

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
	return s.store.Create(ctx, userID, strings.TrimSpace(req.Name), req.Description, req.Note, req.IconCode)
}

var ErrBatchInvalid = errors.New("a tag id appears more than once in the batch")

func (s *Service) Batch(ctx context.Context, userID uuid.UUID, req BatchRequest) (*BatchResult, error) {
	seen := map[uuid.UUID]bool{}
	for _, id := range req.Delete {
		if seen[id] {
			return nil, ErrBatchInvalid
		}
		seen[id] = true
	}
	for _, u := range req.Update {
		if u.ID == uuid.Nil || seen[u.ID] {
			return nil, ErrBatchInvalid
		}
		seen[u.ID] = true
	}
	createdIDs, updatedIDs, err := s.store.Batch(ctx, userID, req)
	if err != nil {
		return nil, err
	}
	all, err := s.store.List(ctx, userID)
	if err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]Tag, len(all))
	for _, t := range all {
		byID[t.ID] = t
	}
	pick := func(ids []uuid.UUID) []Tag {
		out := make([]Tag, 0, len(ids))
		for _, id := range ids {
			out = append(out, byID[id])
		}
		return out
	}
	deleted := req.Delete
	if deleted == nil {
		deleted = []uuid.UUID{}
	}
	return &BatchResult{Created: pick(createdIDs), Updated: pick(updatedIDs), Deleted: deleted, Data: all}, nil
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
	if _, err := s.store.Update(ctx, userID, id, name, req); err != nil {
		return nil, err
	}
	return s.store.GetByID(ctx, userID, id)
}

func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	return s.store.Delete(ctx, userID, id)
}

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

func (s *Service) DetachTag(ctx context.Context, userID, txID, tagID uuid.UUID) error {
	if err := s.store.IsTransactionOwnedByUser(ctx, txID, userID); err != nil {
		return err
	}
	return s.store.DetachTag(ctx, txID, tagID)
}

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
