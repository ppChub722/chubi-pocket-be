package users

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

type Service struct {
	store *Store
}

func NewService(s *Store) *Service {
	return &Service{store: s}
}

var ErrUnknownPreferenceKey = errors.New("unknown preference key")

func (s *Service) GetProfile(ctx context.Context, id uuid.UUID) (*Profile, error) {
	return s.store.GetProfile(ctx, id)
}

func (s *Service) UpdateProfile(ctx context.Context, id uuid.UUID, req UpdateProfileRequest) (*Profile, error) {
	if len(req.Preferences) > 0 {
		if k, ok := ValidatePreferenceKeys(req.Preferences); !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownPreferenceKey, k)
		}
	}
	if err := s.store.UpdateProfile(ctx, id, req); err != nil {
		return nil, err
	}
	return s.store.GetProfile(ctx, id)
}

func (s *Service) Deactivate(ctx context.Context, id uuid.UUID) error {
	return s.store.SetStatus(ctx, id, "inactive")
}

// Reactivate is one of the two endpoints accessible to non-active users.
// Returns ErrAlreadyActive if the user's status is already 'active'.
func (s *Service) Reactivate(ctx context.Context, id uuid.UUID, currentStatus string) error {
	if currentStatus == "active" {
		return ErrAlreadyActive
	}
	return s.store.SetStatus(ctx, id, "active")
}
