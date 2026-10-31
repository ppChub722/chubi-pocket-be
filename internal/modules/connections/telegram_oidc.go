package connections

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
)

// Telegram login, sign-up and linking with an OIDC id_token (0.4.2-4). No /start codes: the app
// gets the id_token from Telegram and sends it here.

// Welcomer DMs the starter guide to a Telegram chat; it logs its own failures (a user who turned
// "Allow Messages" off gets a 403 from Telegram, and the link stays).
type Welcomer func(ctx context.Context, chatID int64)

// TokenLogin — POST /auth/telegram/token. Status signup = the account was just made.
type TokenLogin struct {
	Status string             `json:"status"` // login | signup
	Auth   *auth.AuthResponse `json:"auth"`
}

// WithTelegramLogin turns the id_token routes on; welcome may be nil.
func (s *Service) WithTelegramLogin(v *IDTokenVerifier, welcome Welcomer) {
	s.idTokens, s.welcome = v, welcome
}

// TelegramTokenLogin logs in the account linked to that Telegram, or makes one: no password,
// username from Telegram, THB, the Telegram photo, the chat linked with chat on.
func (s *Service) TelegramTokenLogin(ctx context.Context, idToken string) (*TokenLogin, error) {
	if s.auth == nil || s.idTokens == nil {
		return nil, ErrUnavailable
	}
	who, err := s.idTokens.Verify(ctx, idToken)
	if err != nil {
		return nil, err
	}
	userID, _, linked, err := s.store.byExternal(ctx, ProviderTelegram, who.ID)
	if err != nil {
		return nil, err
	}
	status := "login"
	if !linked {
		if !s.auth.RegistrationOpen() {
			return nil, ErrRegistrationClosed
		}
		userID, err = s.telegramSignup(ctx, who)
		if errors.Is(err, ErrAlreadyLinked) { // linked by another call meanwhile
			if userID, _, linked, err = s.store.byExternal(ctx, ProviderTelegram, who.ID); err == nil && !linked {
				err = ErrAlreadyLinked
			}
		} else {
			status = "signup"
		}
		if err != nil {
			return nil, err
		}
	}
	resp, err := s.auth.IssueFor(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &TokenLogin{Status: status, Auth: resp}, nil
}

func (s *Service) telegramSignup(ctx context.Context, who *TelegramIdentity) (uuid.UUID, error) {
	account := telegramAccount(who)
	display := who.Name
	if display == "" {
		display = strings.TrimPrefix(account, "@")
	}
	if utf8.RuneCountInString(display) > 100 {
		display = string([]rune(display)[:100])
	}
	for try := 0; try < 3; try++ {
		username, err := s.freeUsername(ctx, usernameBase(account, who.Name))
		if err != nil {
			return uuid.Nil, err
		}
		userID, err := s.createTelegramUser(ctx, who.ID, account, username, display)
		if errors.Is(err, auth.ErrUsernameExists) { // taken between the check and the insert
			continue
		}
		if err != nil {
			return uuid.Nil, err
		}
		// best effort: the Telegram photo becomes the avatar
		if _, err := s.ImportPhoto(ctx, userID, ProviderTelegram); err == nil {
			_, _ = s.store.db.Exec(ctx, `UPDATE users SET avatar_settings = jsonb_set(avatar_settings, '{source}', '"telegram"')
				WHERE id = $1`, userID)
		}
		s.sendWelcome(ctx, who.ID)
		return userID, nil
	}
	return uuid.Nil, errUsernameUnavailable
}

func (s *Service) createTelegramUser(ctx context.Context, externalID, account, username, display string) (uuid.UUID, error) {
	tx, err := s.store.db.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx)
	userID, err := s.auth.CreateExternalUserTx(ctx, tx, auth.ExternalSignup{Username: username,
		DisplayName: display, CreatedVia: auth.CreatedViaTelegram})
	if err != nil {
		return uuid.Nil, err
	}
	var label *string
	if account != "" {
		label = &account
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_connections (user_id, provider, external_id, account_label, permissions)
		VALUES ($1, 'telegram', $2, $3, '{"chat": true}'::jsonb)`, userID, externalID, label); err != nil {
		if strings.Contains(err.Error(), "user_connections_provider_external_id_key") {
			return uuid.Nil, ErrAlreadyLinked
		}
		return uuid.Nil, fmt.Errorf("link: %w", err)
	}
	return userID, tx.Commit(ctx)
}

// LinkTelegram — POST /connections/telegram: the signed-in user links the Telegram the id_token
// names (a Telegram linked to another user moves here, as the old link codes did).
func (s *Service) LinkTelegram(ctx context.Context, userID uuid.UUID, key, idToken string) (*Connection, error) {
	p, ok := providerOf(key)
	if !ok {
		return nil, ErrUnknownProvider
	}
	if p.key != ProviderTelegram || !s.available(p) || s.idTokens == nil {
		return nil, ErrUnavailable
	}
	who, err := s.idTokens.Verify(ctx, idToken)
	if err != nil {
		return nil, err
	}
	owner, _, linked, err := s.store.byExternal(ctx, ProviderTelegram, who.ID)
	if err != nil {
		return nil, err
	}
	if !linked || owner != userID {
		tx, err := s.store.db.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback(ctx)
		if err := linkTx(ctx, tx, userID, ProviderTelegram, who.ID, telegramAccount(who),
			map[string]bool{PermChat: true}); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		s.sendWelcome(ctx, who.ID)
	}
	return s.Get(ctx, userID, key)
}

// telegramAccount — how the account shows in the app: "@username", else the name.
func telegramAccount(who *TelegramIdentity) string {
	if who.Username != "" {
		return "@" + who.Username
	}
	return who.Name
}

func (s *Service) sendWelcome(ctx context.Context, externalID string) {
	chatID, err := strconv.ParseInt(externalID, 10, 64)
	if s.welcome == nil || err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	s.welcome(ctx, chatID)
}
