package shared

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// DefaultLanguage is the language when the user has none stored.
const DefaultLanguage = "th"

type prefsKey struct{}

// UserPrefsValue is what a request needs of the caller's preferences: the
// stored timezone ("" = none) and the language (never empty).
type UserPrefsValue struct {
	Timezone string
	Language string
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
	v := UserPrefsValue{Language: DefaultLanguage}
	rows, err := q.Query(ctx, `SELECT COALESCE(preferences->>'timezone', ''), COALESCE(preferences->>'language', '')
		FROM user_preferences WHERE user_id = $1`, userID)
	if err != nil {
		return v, err
	}
	if rows.Next() {
		var tz, lang string
		if err := rows.Scan(&tz, &lang); err != nil {
			rows.Close()
			return v, err
		}
		v.Timezone = tz
		if lang != "" {
			v.Language = lang
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
