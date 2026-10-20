package shared

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ExpectedOccurrence is one recurring occurrence that is still to be recorded
// (computed from its rule, or a stored row of it that the user edited): what
// budgets (รอจ่าย), the dashboard and the counts read instead of materialized
// items.
type ExpectedOccurrence struct {
	RuleID     uuid.UUID
	RuleName   string
	RuleIcon   *IconCode
	ItemID     *uuid.UUID // the stored row, nil while the occurrence is only computed
	Slot       time.Time  // the occurrence's original date
	Date       time.Time  // its date now (an edit may have moved it)
	Type       string     // expense | income | transfer
	Amount     float64    // 0 = not known yet
	AccountID  *uuid.UUID
	CategoryID *uuid.UUID
	TagIDs     []uuid.UUID
	Seq        *int // n of N, only for a setting with an end
	Total      *int
	Name       string // the occurrence's name (the transaction description it would get)
	Due        bool   // date <= today
}

// Expecter lists the occurrences still to be recorded whose date is in
// [from, to] (skipped, paid and recorded ones are not in it; paused / archived
// settings have none). recurring.Service implements it.
type Expecter interface {
	ExpectedBetween(ctx context.Context, userID uuid.UUID, from, to time.Time) ([]ExpectedOccurrence, error)
}
