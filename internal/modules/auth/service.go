package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth/utils"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/config"
)

// RegistrationHook is invoked inside the Register tx after the user +
// preferences rows are inserted. Hooks seed per-user data (e.g., categories
// in Phase 1a; notification settings in 1b). Func-typed instead of an
// interface to avoid a categories↔auth import cycle.
type RegistrationHook func(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error

type Service struct {
	store *Store
	cfg   *config.Config
	hooks []RegistrationHook
}

// NewService accepts zero or more registration hooks. main.go composes the
// hook list (categories seeder is the only one in 1a).
func NewService(s *Store, c *config.Config, hooks ...RegistrationHook) *Service {
	return &Service{store: s, cfg: c, hooks: hooks}
}

var ErrWrongPassword = errors.New("current password is incorrect")
var ErrSamePassword = errors.New("new password must differ from current")

// Register orchestrates the full registration tx: user insert + preferences
// insert + every registration hook (e.g., categories seed). Atomic — any
// failure rolls back the whole thing.
func (s *Service) Register(ctx context.Context, req RegisterRequest) (*AuthResponse, error) {
	hash, err := utils.HashPassword(req.Password)
	if err != nil {
		return nil, err
	}

	currency := req.Currency
	if currency == "" {
		currency = "THB"
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin register tx: %w", err)
	}
	defer tx.Rollback(ctx)

	user, err := s.store.InsertUserTx(ctx, tx, &User{
		Username:     req.Username,
		Email:        req.Email,
		DisplayName:  req.DisplayName,
		PasswordHash: hash,
		Currency:     currency,
	})
	if err != nil {
		return nil, err
	}
	if err := s.store.InsertPreferencesTx(ctx, tx, user.ID); err != nil {
		return nil, err
	}
	for _, hook := range s.hooks {
		if err := hook(ctx, tx, user.ID); err != nil {
			return nil, fmt.Errorf("registration hook: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit register tx: %w", err)
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
