package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth/utils"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/config"
)

type RegistrationHook func(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error

type Service struct {
	store *Store
	cfg   *config.Config
	hooks []RegistrationHook
}

func NewService(s *Store, c *config.Config, hooks ...RegistrationHook) *Service {
	return &Service{store: s, cfg: c, hooks: hooks}
}

var ErrWrongPassword = errors.New("current password is incorrect")
var ErrSamePassword = errors.New("new password must differ from current")

func (s *Service) Register(ctx context.Context, req RegisterRequest) (*AuthResponse, error) {
	hash, err := utils.HashPassword(req.Password)
	if err != nil {
		return nil, err
	}

	currency := strings.ToUpper(req.Currency)
	if currency == "" {
		currency = "THB"
	}

	if !s.cfg.App.RegistrationOpen {
		id, err := uuid.NewV7()
		if err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		return s.issueAuthResponse(&User{
			ID:          id,
			Username:    req.Username,
			Email:       req.Email,
			DisplayName: req.DisplayName,
			Currency:    currency,
			Status:      "active",
			CreatedAt:   now,
			UpdatedAt:   now,
		})
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
	if req.Identifier == "" || req.Password == "" {
		return nil, ErrUserNotFound
	}
	user, err := s.store.GetByIdentifier(ctx, req.Identifier)
	if err != nil {
		return nil, err
	}
	// no password (made through Telegram) = refused like a wrong password (0.4.2 1b-2)
	if user.PasswordHash == "" || !utils.CheckPasswordHash(req.Password, user.PasswordHash) {
		return nil, ErrUserNotFound
	}
	return s.issueAuthResponse(user)
}

func (s *Service) ChangePassword(ctx context.Context, userID uuid.UUID, req ChangePasswordRequest) error {
	user, err := s.store.GetByID(ctx, userID)
	if err != nil {
		return err
	}
	// a passwordless account sets its first password without a current one (0.4.2 1b-2)
	if user.PasswordHash != "" {
		if req.CurrentPassword == "" || !utils.CheckPasswordHash(req.CurrentPassword, user.PasswordHash) {
			return ErrWrongPassword
		}
		if utils.CheckPasswordHash(req.NewPassword, user.PasswordHash) {
			return ErrSamePassword
		}
	}

	newHash, err := utils.HashPassword(req.NewPassword)
	if err != nil {
		return err
	}
	return s.store.UpdatePassword(ctx, userID, newHash)
}

// Where an account came from (users.created_via, migration 123).
const (
	CreatedViaPassword = "password"
	CreatedViaTelegram = "telegram"
)

// ExternalSignup — an account made through an outside app (0.4.2 1b-2): no password.
type ExternalSignup struct {
	Username    string
	DisplayName string
	Currency    string // "" = THB
	CreatedVia  string // telegram | line | google
}

// RegistrationOpen — REGISTRATION_OPEN (the outside-app sign-up refuses when closed).
func (s *Service) RegistrationOpen() bool { return s.cfg.App.RegistrationOpen }

// CreateExternalUserTx makes a passwordless account inside the caller's transaction, with the same
// preferences and registration hooks as POST /auth/register. ErrUsernameExists on a taken username.
func (s *Service) CreateExternalUserTx(ctx context.Context, tx pgx.Tx, in ExternalSignup) (uuid.UUID, error) {
	if in.CreatedVia == "" || in.CreatedVia == CreatedViaPassword {
		return uuid.Nil, errors.New("an external account needs created_via")
	}
	currency := strings.ToUpper(in.Currency)
	if currency == "" {
		currency = "THB"
	}
	user, err := s.store.InsertUserTx(ctx, tx, &User{
		Username: in.Username, DisplayName: in.DisplayName, Currency: currency, CreatedVia: in.CreatedVia,
	})
	if err != nil {
		return uuid.Nil, err
	}
	if err := s.store.InsertPreferencesTx(ctx, tx, user.ID); err != nil {
		return uuid.Nil, err
	}
	for _, hook := range s.hooks {
		if err := hook(ctx, tx, user.ID); err != nil {
			return uuid.Nil, fmt.Errorf("registration hook: %w", err)
		}
	}
	return user.ID, nil
}

// UsernameTaken — for the sign-up suggestion.
func (s *Service) UsernameTaken(ctx context.Context, username string) (bool, error) {
	var taken bool
	err := s.store.Pool().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE LOWER(username) = LOWER($1))`,
		username).Scan(&taken)
	return taken, err
}

// IssueFor — the signed-in answer (same as login) for a user found another way (Telegram).
func (s *Service) IssueFor(ctx context.Context, userID uuid.UUID) (*AuthResponse, error) {
	user, err := s.store.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.issueAuthResponse(user)
}

func (s *Service) issueAuthResponse(user *User) (*AuthResponse, error) {
	token, err := utils.GenerateToken(user.ID, s.cfg.JWT.Secret, s.cfg.JWT.ExpirationTime)
	if err != nil {
		return nil, err
	}
	return &AuthResponse{
		User: user.AuthView(),
		Token: TokenInfo{
			AccessToken: token,
			ExpiresIn:   int(s.cfg.JWT.ExpirationTime.Seconds()),
		},
	}, nil
}
