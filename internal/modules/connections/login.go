package connections

// 0.4.2 1b-2: sign up / log in with Telegram. The app starts with a PKCE S256 challenge, the user
// confirms in the bot (/start login_<code> → a button), the app exchanges code + verifier for
// tokens, or signs up a passwordless account linked to that chat.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
)

// LoginCodeTTL — a login code works for 5 minutes, once.
const LoginCodeTTL = 5 * time.Minute

var (
	ErrLoginCodeInvalid    = errors.New("login code unknown, used, expired or the verifier does not match")
	ErrBadLoginRequest     = errors.New("invalid request")
	ErrRegistrationClosed  = errors.New("registration is closed")
	ErrAlreadyLinked       = errors.New("this Telegram is linked to an account already")
	ErrUsernameTaken       = errors.New("username already taken")
	challengeFormat        = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	verifierFormat         = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
	usernameUnsafeChars    = regexp.MustCompile(`[^a-z0-9_]`)
	currencyFormat         = regexp.MustCompile(`^[A-Za-z]{3}$`)
	errUsernameUnavailable = errors.New("no free username found")
)

// LoginBot is the bot's extra: whether the user has a photo (sign-up prefill).
type LoginBot interface {
	HasProfilePhoto(ctx context.Context, userID int64) (bool, error)
}

type LoginStart struct {
	Code      string    `json:"code"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type SignupInfo struct {
	DisplayName        string `json:"display_name"`
	UsernameSuggestion string `json:"username_suggestion"`
	HasPhoto           bool   `json:"has_photo"`
}

type Exchange struct {
	Status string             `json:"status"` // pending | login | signup
	Auth   *auth.AuthResponse `json:"auth"`
	Signup *SignupInfo        `json:"signup"`
}

type SignupRequest struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
	DisplayName  string `json:"display_name"`
	Username     string `json:"username"`
	Currency     string `json:"currency"`
}

// UsernameTakenError carries a free username for the 409.
type UsernameTakenError struct{ Suggestion string }

func (e *UsernameTakenError) Error() string { return ErrUsernameTaken.Error() }
func (e *UsernameTakenError) Unwrap() error { return ErrUsernameTaken }

// WithAuth hands the service the account side (token issue, passwordless sign-up).
func (s *Service) WithAuth(a *auth.Service) { s.auth = a }

// StartLogin — POST /auth/telegram/start.
func (s *Service) StartLogin(ctx context.Context, challenge string) (*LoginStart, error) {
	if !challengeFormat.MatchString(challenge) {
		return nil, fmt.Errorf("%w: code_challenge must be base64url(SHA-256(verifier)), 43 chars", ErrBadLoginRequest)
	}
	if s.bot == nil || s.auth == nil {
		return nil, ErrUnavailable
	}
	bot, err := s.bot.Username(ctx)
	if err != nil || bot == "" {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	code, err := newCode()
	if err != nil {
		return nil, err
	}
	expires := s.now().Add(LoginCodeTTL).UTC().Truncate(time.Second)
	if _, err := s.store.db.Exec(ctx, `INSERT INTO connection_link_codes (code_hash, purpose, provider, code_challenge, expires_at)
		VALUES ($1, 'login', 'telegram', $2, $3)`, hashCode(code), challenge, expires); err != nil {
		return nil, fmt.Errorf("login code: %w", err)
	}
	return &LoginStart{Code: code, URL: "https://t.me/" + bot + "?start=login_" + code, ExpiresAt: expires}, nil
}

func newCode() (string, error) {
	raw := make([]byte, 16) // 128 bits
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("code: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

type loginRow struct {
	ID          uuid.UUID
	Challenge   string
	ExternalID  *string
	Account     *string
	Name        *string
	ConfirmedAt *time.Time
}

// loginCode — a usable login code (unused, unexpired) whose challenge matches the verifier.
func (s *Service) loginCode(ctx context.Context, code, verifier string) (*loginRow, error) {
	if !codeFormat.MatchString(code) || !verifierFormat.MatchString(verifier) {
		return nil, ErrLoginCodeInvalid
	}
	var r loginRow
	err := s.store.db.QueryRow(ctx, `SELECT id, code_challenge, external_id, account_label, external_name, confirmed_at
		FROM connection_link_codes
		WHERE code_hash = $1 AND purpose = 'login' AND used_at IS NULL AND expires_at > NOW()`, hashCode(code)).
		Scan(&r.ID, &r.Challenge, &r.ExternalID, &r.Account, &r.Name, &r.ConfirmedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrLoginCodeInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("login code: %w", err)
	}
	sum := sha256.Sum256([]byte(verifier))
	if subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(r.Challenge)) != 1 {
		return nil, ErrLoginCodeInvalid
	}
	return &r, nil
}

// useCode marks a login code used (once); false = someone used it first.
func useCode(ctx context.Context, q execer, id uuid.UUID) (bool, error) {
	tag, err := q.Exec(ctx, `UPDATE connection_link_codes SET used_at = NOW() WHERE id = $1 AND used_at IS NULL`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// Exchange — POST /auth/telegram/exchange.
func (s *Service) Exchange(ctx context.Context, code, verifier string) (*Exchange, error) {
	if s.auth == nil {
		return nil, ErrUnavailable
	}
	r, err := s.loginCode(ctx, code, verifier)
	if err != nil {
		return nil, err
	}
	if r.ConfirmedAt == nil || r.ExternalID == nil {
		return &Exchange{Status: "pending"}, nil
	}
	userID, _, linked, err := s.store.byExternal(ctx, ProviderTelegram, *r.ExternalID)
	if err != nil {
		return nil, err
	}
	if linked {
		ok, err := useCode(ctx, s.store.db, r.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrLoginCodeInvalid
		}
		resp, err := s.auth.IssueFor(ctx, userID)
		if err != nil {
			return nil, err
		}
		return &Exchange{Status: "login", Auth: resp}, nil
	}
	info := &SignupInfo{DisplayName: deref(r.Name)}
	if info.DisplayName == "" {
		info.DisplayName = strings.TrimPrefix(deref(r.Account), "@")
	}
	if info.UsernameSuggestion, err = s.freeUsername(ctx, usernameBase(deref(r.Account), info.DisplayName)); err != nil {
		return nil, err
	}
	if lb, ok := s.bot.(LoginBot); ok {
		if tg, err := strconv.ParseInt(*r.ExternalID, 10, 64); err == nil {
			info.HasPhoto, _ = lb.HasProfilePhoto(ctx, tg) // a Telegram error = no photo shown
		}
	}
	return &Exchange{Status: "signup", Signup: info}, nil
}

// Signup — POST /auth/telegram/signup: a passwordless account, linked to the chat, with its photo.
func (s *Service) Signup(ctx context.Context, req SignupRequest) (*auth.AuthResponse, error) {
	if s.auth == nil {
		return nil, ErrUnavailable
	}
	req.DisplayName, req.Username = strings.TrimSpace(req.DisplayName), strings.TrimSpace(req.Username)
	if n := utf8.RuneCountInString(req.DisplayName); n < 1 || n > 100 {
		return nil, fmt.Errorf("%w: display_name is 1-100 characters", ErrBadLoginRequest)
	}
	if n := utf8.RuneCountInString(req.Username); n < 3 || n > 50 {
		return nil, fmt.Errorf("%w: username is 3-50 characters", ErrBadLoginRequest)
	}
	if req.Currency != "" && !currencyFormat.MatchString(req.Currency) {
		return nil, fmt.Errorf("%w: currency is 3 letters", ErrBadLoginRequest)
	}
	r, err := s.loginCode(ctx, req.Code, req.CodeVerifier)
	if err != nil {
		return nil, err
	}
	if r.ConfirmedAt == nil || r.ExternalID == nil {
		return nil, ErrLoginCodeInvalid
	}
	if !s.auth.RegistrationOpen() {
		return nil, ErrRegistrationClosed
	}
	if _, _, linked, err := s.store.byExternal(ctx, ProviderTelegram, *r.ExternalID); err != nil {
		return nil, err
	} else if linked {
		return nil, ErrAlreadyLinked
	}

	tx, err := s.store.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	userID, err := s.auth.CreateExternalUserTx(ctx, tx, auth.ExternalSignup{Username: req.Username,
		DisplayName: req.DisplayName, Currency: req.Currency, CreatedVia: auth.CreatedViaTelegram})
	if errors.Is(err, auth.ErrUsernameExists) {
		sugg, serr := s.freeUsername(ctx, usernameBase("@"+req.Username, req.DisplayName))
		if serr != nil {
			return nil, serr
		}
		return nil, &UsernameTakenError{Suggestion: sugg}
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_connections (user_id, provider, external_id, account_label, permissions)
		VALUES ($1, 'telegram', $2, $3, '{"chat": true}'::jsonb)`, userID, *r.ExternalID, r.Account); err != nil {
		if strings.Contains(err.Error(), "user_connections_provider_external_id_key") {
			return nil, ErrAlreadyLinked
		}
		return nil, fmt.Errorf("link: %w", err)
	}
	if ok, err := useCode(ctx, tx, r.ID); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrLoginCodeInvalid
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	// best effort: the Telegram photo becomes the avatar
	if _, err := s.ImportPhoto(ctx, userID, ProviderTelegram); err == nil {
		_, _ = s.store.db.Exec(ctx, `UPDATE users SET avatar_settings = jsonb_set(avatar_settings, '{source}', '"telegram"')
			WHERE id = $1`, userID)
	}
	return s.auth.IssueFor(ctx, userID)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// usernameBase — the Telegram @username, else the name in a-z 0-9 _, else "user".
func usernameBase(account, name string) string {
	base := ""
	if strings.HasPrefix(account, "@") {
		base = strings.ToLower(strings.TrimPrefix(account, "@"))
	}
	if base == "" {
		base = usernameUnsafeChars.ReplaceAllString(strings.ToLower(strings.ReplaceAll(name, " ", "_")), "")
	}
	base = usernameUnsafeChars.ReplaceAllString(base, "")
	if len(base) < 3 {
		base = "user"
	}
	if len(base) > 44 {
		base = base[:44]
	}
	return base
}

// freeUsername — base when free, else base + 2-5 random digits.
func (s *Service) freeUsername(ctx context.Context, base string) (string, error) {
	try := base
	for i := 0; i < 12; i++ {
		taken, err := s.auth.UsernameTaken(ctx, try)
		if err != nil {
			return "", err
		}
		if !taken && len(try) >= 3 {
			return try, nil
		}
		digits := 2 + i/3
		n, err := rand.Int(rand.Reader, big.NewInt(int64(pow10(digits))))
		if err != nil {
			return "", err
		}
		try = fmt.Sprintf("%s%0*d", base, digits, n.Int64())
	}
	return "", errUsernameUnavailable
}

func pow10(n int) int {
	p := 1
	for i := 0; i < n; i++ {
		p *= 10
	}
	return p
}

// ── The bot's side ──

// TelegramLoginOpen — /start login_<code>: the chat takes the code (a code another chat took
// can't be taken). linkedName = the account this chat is linked to ("" = sign up).
func (s *Service) TelegramLoginOpen(ctx context.Context, code string, chatID int64, account, name string) (uuid.UUID, string, bool, error) {
	if !codeFormat.MatchString(code) {
		return uuid.Nil, "", false, nil
	}
	ext := strconv.FormatInt(chatID, 10)
	var id uuid.UUID
	err := s.store.db.QueryRow(ctx, `UPDATE connection_link_codes
		SET external_id = $2, account_label = NULLIF($3, ''), external_name = NULLIF($4, '')
		WHERE code_hash = $1 AND purpose = 'login' AND used_at IS NULL AND expires_at > NOW()
		  AND confirmed_at IS NULL AND (external_id IS NULL OR external_id = $2)
		RETURNING id`, hashCode(code), ext, account, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", false, nil
	}
	if err != nil {
		return uuid.Nil, "", false, fmt.Errorf("login open: %w", err)
	}
	var linkedName string
	err = s.store.db.QueryRow(ctx, `SELECT u.display_name FROM user_connections c JOIN users u ON u.id = c.user_id
		WHERE c.provider = 'telegram' AND c.external_id = $1`, ext).Scan(&linkedName)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", false, fmt.Errorf("login open: %w", err)
	}
	return id, linkedName, true, nil
}

// TelegramLoginConfirm — the chat tapped its button; false = gone or another chat's code.
func (s *Service) TelegramLoginConfirm(ctx context.Context, codeID uuid.UUID, chatID int64) (bool, error) {
	ext := strconv.FormatInt(chatID, 10)
	tag, err := s.store.db.Exec(ctx, `UPDATE connection_link_codes
		SET confirmed_at = NOW(),
		    user_id = (SELECT c.user_id FROM user_connections c WHERE c.provider = 'telegram' AND c.external_id = $2)
		WHERE id = $1 AND purpose = 'login' AND used_at IS NULL AND expires_at > NOW() AND external_id = $2`,
		codeID, ext)
	if err != nil {
		return false, fmt.Errorf("login confirm: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// lastLoginMethod — the user has no password and this is their only linked app: removing it
// would lock them out (0.4.2 1b-2).
func lastLoginMethod(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, userID uuid.UUID) (bool, error) {
	var last bool
	err := q.QueryRow(ctx, `SELECT u.password_hash IS NULL
			AND (SELECT count(*) FROM user_connections c WHERE c.user_id = u.id) <= 1
		FROM users u WHERE u.id = $1`, userID).Scan(&last)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return last, err
}
