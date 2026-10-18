package users

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

type Service struct {
	store *Store
	feeCategoryCheck func(ctx context.Context, userID, categoryID uuid.UUID) error
}

func (s *Service) WithFeeCategoryCheck(fn func(ctx context.Context, userID, categoryID uuid.UUID) error) {
	s.feeCategoryCheck = fn
}

var ErrInvalidFeeCategory = errors.New("fee_category_id must be one of your expense categories")

func (s *Service) FeeCategoryID(ctx context.Context, userID uuid.UUID) (*uuid.UUID, error) {
	prof, err := s.store.GetProfile(ctx, userID)
	if err != nil {
		return nil, err
	}
	str := ""
	if prof.Preferences.FeeCategoryID != nil {
		str = *prof.Preferences.FeeCategoryID
	}
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
		for k, v := range req.Preferences {
			if _, isStr := v.(string); v != nil && !isStr {
				return nil, fmt.Errorf("%w: %q must be a string or null", ErrUnknownPreferenceKey, k)
			}
		}
		if v, ok := req.Preferences[PrefMoneyStyle]; ok && v != nil {
			if str, _ := v.(string); !MoneyStyles[str] {
				return nil, fmt.Errorf("%w: %q is not a money style", ErrUnknownPreferenceKey, str)
			}
		}
		if v, ok := req.Preferences[PrefMoneyDecimals]; ok && v != nil {
			if str, _ := v.(string); !MoneyDecimals[str] {
				return nil, fmt.Errorf("%w: %q is not a decimals mode", ErrUnknownPreferenceKey, str)
			}
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

func (s *Service) Reactivate(ctx context.Context, id uuid.UUID, currentStatus string) error {
	if currentStatus == "active" {
		return ErrAlreadyActive
	}
	return s.store.SetStatus(ctx, id, "active")
}
