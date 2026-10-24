package shared

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// DefaultLanguage is the language when the user has none stored.
const DefaultLanguage = "th"

type prefsKey struct{}

// DefaultCurrency is the currency when the user has none stored.
const DefaultCurrency = "THB"

// UserPrefsValue is what a request needs of the caller's settings: the stored
// timezone ("" = none), the language and the currency (never empty).
type UserPrefsValue struct {
	Timezone string
	Language string
	Currency string
}

type prefsHolder struct {
	mu   sync.Mutex
	byID map[uuid.UUID]UserPrefsValue
}

// WithUserPrefs installs a per-request cache: the first UserPrefs call per user
// reads user_preferences, every later one in the same request reuses it.
func WithUserPrefs(ctx context.Context) context.Context {
	return context.WithValue(ctx, prefsKey{}, &prefsHolder{byID: map[uuid.UUID]UserPrefsValue{}})
}

// UserPrefs reads the timezone and language of userID, once per request when
// ctx came through WithUserPrefs (a plain query otherwise).
func UserPrefs(ctx context.Context, q Querier, userID uuid.UUID) (UserPrefsValue, error) {
	h, _ := ctx.Value(prefsKey{}).(*prefsHolder)
	if h != nil {
		h.mu.Lock()
		defer h.mu.Unlock()
		if v, ok := h.byID[userID]; ok {
			return v, nil
		}
	}
	v := UserPrefsValue{Language: DefaultLanguage, Currency: DefaultCurrency}
	rows, err := q.Query(ctx, `SELECT COALESCE(p.preferences->>'timezone', ''), COALESCE(p.preferences->>'language', ''),
			COALESCE(u.currency, '')
		FROM users u LEFT JOIN user_preferences p ON p.user_id = u.id WHERE u.id = $1`, userID)
	if err != nil {
		return v, err
	}
	if rows.Next() {
		var tz, lang, cur string
		if err := rows.Scan(&tz, &lang, &cur); err != nil {
			rows.Close()
			return v, err
		}
		v.Timezone = tz
		if lang != "" {
			v.Language = lang
		}
		if cur != "" {
			v.Currency = cur
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return v, err
	}
	if h != nil {
		h.byID[userID] = v
	}
	return v, nil
}
