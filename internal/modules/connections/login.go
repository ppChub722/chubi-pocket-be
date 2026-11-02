package connections

// Shared by the Telegram OIDC login / sign-up (telegram_oidc.go): usernames for new accounts and
// the last-login-method guard.

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
)

var (
	ErrRegistrationClosed  = errors.New("registration is closed")
	ErrAlreadyLinked       = errors.New("this Telegram is linked to an account already")
	usernameUnsafeChars    = regexp.MustCompile(`[^a-z0-9_]`)
	errUsernameUnavailable = errors.New("no free username found")
)

// WithAuth hands the service the account side (token issue, passwordless sign-up).
func (s *Service) WithAuth(a *auth.Service) { s.auth = a }

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
