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
// It returns the generated token and the user data
func (s *Service) RegisterUser(ctx context.Context, req RegisterRequest) (string, *User, error) {
	// 1. Hash Password
	hashedPwd, err := utils.HashPassword(req.Password)
	if err != nil {
		return "", nil, err
	}

	// 2. Save to DB
	user, err := s.store.CreateUser(ctx, req.Username, req.Email, hashedPwd)
	if err != nil {
		return "", nil, err
	}

	// 3. Generate Token
	// ✅ FIXED: Added user.Email here!
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
	// Default: From Config (24 hours)
	tokenDuration := s.cfg.JWT.ExpirationTime

	// ⭐️ BACKDOOR: If I am the superuser, give me 10 years
	if user.Username == "chubPPond" {
		tokenDuration = time.Hour * 87600 // 10 Years
	}

	// 4. Generate Token
	token, err := utils.GenerateToken(user.ID, user.Username, user.Email, s.cfg.JWT.Secret, tokenDuration)
	if err != nil {
		return "", nil, err
	}

	return token, user, nil
}