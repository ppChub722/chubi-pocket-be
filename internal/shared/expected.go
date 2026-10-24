package shared

import (
	"context"
	"sync"
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

type expectedMemoKey struct{}

// ExpectedMemo keeps the occurrences one request already asked for, so a second ask for the
// same or a narrower window (the counts and the budgets of one page) is answered without
// reading again.
type ExpectedMemo struct {
	mu       sync.Mutex
	set      bool
	from, to time.Time
	occs     []ExpectedOccurrence
}

// WithExpectedMemo installs the memo; use it only on a request that reads and never writes.
func WithExpectedMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, expectedMemoKey{}, &ExpectedMemo{})
}

// ExpectedMemoOf is the request's memo, or nil.
func ExpectedMemoOf(ctx context.Context) *ExpectedMemo {
	m, _ := ctx.Value(expectedMemoKey{}).(*ExpectedMemo)
	return m
}

// Lookup answers [from, to] when the memo holds a window that covers it.
func (m *ExpectedMemo) Lookup(from, to time.Time) ([]ExpectedOccurrence, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.set || from.Before(m.from) || to.After(m.to) {
		return nil, false
	}
	out := []ExpectedOccurrence{}
	for _, o := range m.occs {
		if !o.Date.Before(from) && !o.Date.After(to) {
			out = append(out, o)
		}
	}
	return out, true
}

// Remember keeps the widest window it has been given.
func (m *ExpectedMemo) Remember(from, to time.Time, occs []ExpectedOccurrence) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.set && (from.After(m.from) || to.Before(m.to)) {
		return
	}
	m.set, m.from, m.to, m.occs = true, from, to, occs
}
