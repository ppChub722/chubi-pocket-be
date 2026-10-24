package shared

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const DefaultTimezone = "Asia/Bangkok"

type tzKey struct{}

func WithHeaderTZ(ctx context.Context, tz string) context.Context {
	return context.WithValue(ctx, tzKey{}, tz)
}

func HeaderTZ(ctx context.Context) string {
	tz, _ := ctx.Value(tzKey{}).(string)
	return tz
}

func UserLocation(ctx context.Context, q Querier, userID uuid.UUID, headerTZ string) (*time.Location, error) {
	prefs, err := UserPrefs(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("user timezone: %w", err)
	}
	for _, name := range []string{headerTZ, prefs.Timezone, DefaultTimezone} {
		if name == "" {
			continue
		}
		if loc, err := time.LoadLocation(name); err == nil {
			return loc, nil
		}
	}
	return time.UTC, nil
}

func UserToday(ctx context.Context, q Querier, userID uuid.UUID, headerTZ string) (time.Time, error) {
	loc, err := UserLocation(ctx, q, userID, headerTZ)
	if err != nil {
		return time.Time{}, err
	}
	n := time.Now().In(loc)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC), nil
}

func MonthBounds(day time.Time) (first, last time.Time) {
	first = time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
	return first, first.AddDate(0, 1, -1)
}
