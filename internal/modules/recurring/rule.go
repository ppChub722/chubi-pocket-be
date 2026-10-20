package recurring

import (
	"time"

	"github.com/ppChub722/chubi-pocket-be/internal/shared/recurrence"
)

var (
	// ErrInvalidRule — the rule can't produce dates (400 INVALID_RULE).
	ErrInvalidRule = recurrence.ErrInvalidRule
	ErrEndTooFar   = recurrence.ErrEndTooFar
)

func parseDate(s string) (time.Time, error) { return recurrence.ParseDate(s) }

func (r *Rule) occurrences(until time.Time, limit int) []time.Time {
	return r.Spec.Occurrences(until, limit)
}

func (r *Rule) horizonSpan(from time.Time) time.Time { return r.Spec.HorizonSpan(from) }

func (r *Rule) limits(today time.Time) error { return r.Spec.Limits(today) }

func (r *Rule) applyRule(in RecurrenceRule) error { return r.Spec.ApplyRule(in) }

func (r *Rule) applyEnd(in *RecurrenceEnd) error { return r.Spec.ApplyEnd(in) }

func (r *Rule) apiRule() RecurrenceRule { return r.Spec.APIRule() }

func (r *Rule) apiEnd() RecurrenceEnd { return r.Spec.APIEnd() }
