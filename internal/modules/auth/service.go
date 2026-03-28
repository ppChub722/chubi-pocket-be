package auth

import (
	"context"
	"errors"
	"time"

	"github.com/ppChub722/finna-bbear-be/internal/modules/auth/utils"
	"github.com/ppChub722/finna-bbear-be/internal/platform/config"
)

// Service handles all the business logic for Auth
type Service struct {
	store *Store
	cfg   *config.Config
}

// NewService initializes the business logic layer
func NewService(s *Store, c *config.Config) *Service {
	return &Service{
		store: s,
		cfg:   c,
	}
}

// RegisterUser handles hashing password and saving user
func (s *Service) RegisterUser(ctx context.Context, req RegisterRequest) (string, *User, error) {
	// 1. Hash Password
	hashedPwd, err := utils.HashPassword(req.Password)
	if err != nil {
		return "", nil, err
	}

	// 2. Save to DB
	user, err := s.store.CreateUser(ctx, req.Username, req.Email, hashedPwd, req.Currency)
	if err != nil {
		return "", nil, err
	}

	// 3. Generate Token
	token, err := utils.GenerateToken(user.ID, user.Username, user.Email, s.cfg.JWT.Secret, s.cfg.JWT.ExpirationTime)
	if err != nil {
		return "", nil, err
	}

	return token, user, nil
}

// LoginUser handles finding the user and verifying password
func (s *Service) LoginUser(ctx context.Context, req LoginRequest) (string, *User, error) {
	// 1. Find User
	user, err := s.store.GetUserByIdentifier(ctx, req.Identifier)
	if err != nil {
		return "", nil, err
	}

	// 2. Check Password
	if !utils.CheckPasswordHash(req.Password, user.PasswordHash) {
		return "", nil, errors.New("invalid password")
	}

	// 3. Determine Duration
	tokenDuration := s.cfg.JWT.ExpirationTime

	// Backdoor: If superuser, give 10 years
	if user.Username == "chubPPond" {
		tokenDuration = time.Hour * 87600
	}

	// 4. Generate Token
	token, err := utils.GenerateToken(user.ID, user.Username, user.Email, s.cfg.JWT.Secret, tokenDuration)
	if err != nil {
		return "", nil, err
	}

	return token, user, nil
}

// GetProfile returns the authenticated user's profile
func (s *Service) GetProfile(ctx context.Context, userID int64) (*User, error) {
	return s.store.GetUserByID(ctx, userID)
}

// UpdateProfile updates user profile fields
func (s *Service) UpdateProfile(ctx context.Context, userID int64, req UpdateProfileRequest) (*User, error) {
	return s.store.UpdateUser(ctx, userID, req.Name, req.Currency, req.AvatarURL)
}

// ChangePassword verifies current password and updates to new one
func (s *Service) ChangePassword(ctx context.Context, userID int64, req ChangePasswordRequest) error {
	// 1. Get user to verify current password
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}

	// 2. Verify current password
	if !utils.CheckPasswordHash(req.CurrentPassword, user.PasswordHash) {
		return errors.New("current password is incorrect")
	}

	// 3. Hash new password
	newHash, err := utils.HashPassword(req.NewPassword)
	if err != nil {
		return err
	}

	// 4. Update in DB
	return s.store.UpdatePassword(ctx, userID, newHash)
}
