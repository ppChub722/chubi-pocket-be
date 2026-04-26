package auth

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth/utils"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/config"
)

type Service struct {
	store *Store
	cfg   *config.Config
}

func NewService(s *Store, c *config.Config) *Service {
	return &Service{store: s, cfg: c}
}

var ErrWrongPassword = errors.New("current password is incorrect")
var ErrSamePassword = errors.New("new password must differ from current")

func (s *Service) Register(ctx context.Context, req RegisterRequest) (*AuthResponse, error) {
	hash, err := utils.HashPassword(req.Password)
	if err != nil {
		return nil, err
	}

	currency := req.Currency
	if currency == "" {
		currency = "THB"
	}

	user, err := s.store.CreateUser(ctx, &User{
		Username:     req.Username,
		Email:        req.Email,
		DisplayName:  req.DisplayName,
		PasswordHash: hash,
		Currency:     currency,
	})
	if err != nil {
		return nil, err
	}

	return s.issueAuthResponse(user)
}

func (s *Service) Login(ctx context.Context, req LoginRequest) (*AuthResponse, error) {
	user, err := s.store.GetByIdentifier(ctx, req.Identifier)
	if err != nil {
		return nil, err
	}
	if !utils.CheckPasswordHash(req.Password, user.PasswordHash) {
		return nil, ErrUserNotFound // intentionally vague — see spec §3.8
	}
	return s.issueAuthResponse(user)
}

func (s *Service) ChangePassword(ctx context.Context, userID uuid.UUID, req ChangePasswordRequest) error {
	user, err := s.store.GetByID(ctx, userID)
	if err != nil {
		return err
	}
	if !utils.CheckPasswordHash(req.CurrentPassword, user.PasswordHash) {
		return ErrWrongPassword
	}
	if utils.CheckPasswordHash(req.NewPassword, user.PasswordHash) {
		return ErrSamePassword
	}

	newHash, err := utils.HashPassword(req.NewPassword)
	if err != nil {
		return err
	}
	return s.store.UpdatePassword(ctx, userID, newHash)
}

func (s *Service) issueAuthResponse(user *User) (*AuthResponse, error) {
	token, err := utils.GenerateToken(user.ID, s.cfg.JWT.Secret, s.cfg.JWT.ExpirationTime)
	if err != nil {
		return nil, err
	}
	return &AuthResponse{
		User: *user,
		Token: TokenInfo{
			AccessToken: token,
			ExpiresIn:   int(s.cfg.JWT.ExpirationTime.Seconds()),
		},
	}, nil
}
