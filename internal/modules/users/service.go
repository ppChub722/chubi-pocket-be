package users

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

type Service struct {
	store *Store
	// feeCategoryCheck — is this one of the user's expense categories?
	// Wired in main (users can't import categories).
	feeCategoryCheck func(ctx context.Context, userID, categoryID uuid.UUID) error
}

// WithFeeCategoryCheck sets how `fee_category_id` is validated.
func (s *Service) WithFeeCategoryCheck(fn func(ctx context.Context, userID, categoryID uuid.UUID) error) {
	s.feeCategoryCheck = fn
}

// ErrInvalidFeeCategory — fee_category_id isn't one of the user's expense
// categories.
var ErrInvalidFeeCategory = errors.New("fee_category_id must be one of your expense categories")

// FeeCategoryID — the user's fee category, if set (not checked: it may
// have been deleted since; callers verify).
func (s *Service) FeeCategoryID(ctx context.Context, userID uuid.UUID) (*uuid.UUID, error) {
	prof, err := s.store.GetProfile(ctx, userID)
	if err != nil {
		return nil, err
	}
	str, _ := prof.Preferences[PrefFeeCategoryID].(string)
	if str == "" {
		return nil, nil
	}
	id, err := uuid.Parse(str)
	if err != nil {
		return nil, nil
	}
	return &id, nil
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
		if v, ok := req.Preferences[PrefFeeCategoryID]; ok && v != nil {
			str, _ := v.(string)
			catID, err := uuid.Parse(str)
			if err != nil {
				return nil, ErrInvalidFeeCategory
			}
			if s.feeCategoryCheck != nil {
				if err := s.feeCategoryCheck(ctx, id, catID); err != nil {
					return nil, ErrInvalidFeeCategory
				}
			}
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
