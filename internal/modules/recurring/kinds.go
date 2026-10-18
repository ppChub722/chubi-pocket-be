package recurring

import (
	"math"
	"strings"
	"time"
)

const (
	KindSubscription = "subscription"
	KindInstallment  = "installment"
	KindIncome       = "income"
	KindReminder     = "reminder"
)

func validKind(k string) bool {
	switch k {
	case KindSubscription, KindInstallment, KindIncome, KindReminder:
		return true
	}
	return false
}

func deriveKind(typ, endKind string) string {
	switch typ {
	case "income":
		return KindIncome
	case "transfer":
		return KindReminder
	}
	if endKind == "count" {
		return KindInstallment
	}
	return KindSubscription
}

func typeForKind(k string) string {
	if k == KindIncome {
		return "income"
	}
	return "expense"
}

func (r *Rule) finishKind(kind *string) error {
	switch {
	case kind != nil:
		if !validKind(*kind) {
			return ErrValidation
		}
		r.Kind = *kind
	case r.Kind == "":
		r.Kind = deriveKind(r.Type, r.EndKind)
	}
	switch r.Kind {
	case KindSubscription, KindInstallment:
		if r.Type != "expense" || r.Amount <= 0 {
			return ErrValidation
		}
		if r.Kind == KindInstallment && (r.EndKind != "count" || r.EndCount == nil || *r.EndCount < 1) {
			return ErrValidation
		}
	case KindIncome:
		if r.Type != "income" || r.Amount <= 0 {
			return ErrValidation
		}
	case KindReminder:
		if (r.Type != "expense" && r.Type != "transfer") || r.Amount < 0 {
			return ErrValidation
		}
	}
	if r.Kind != KindSubscription {
		r.TrialUntil = nil
	}
	return nil
}

func (r *Rule) setTrial(v *string) error {
	if v == nil || strings.TrimSpace(*v) == "" {
		r.TrialUntil = nil
		return nil
	}
	d, err := time.Parse(dateLayout, *v)
	if err != nil {
		return ErrValidation
	}
	r.TrialUntil = &d
	return nil
}

func yearlyCost(r *Rule, today time.Time) float64 {
	if r.Amount <= 0 {
		return 0
	}
	perMonth := monthlyCount(r, today)
	y := r.Amount * perMonth * 12
	if r.Freq == "dates" {
		y = r.Amount * float64(len(r.Dates))
	}
	if r.Kind == KindInstallment && r.EndCount != nil {
		total := r.Amount * float64(*r.EndCount)
		if total < y {
			y = total
		}
	}
	return math.Round(y*100) / 100
}

func amountPtr(a float64) *float64 {
	if a <= 0 {
		return nil
	}
	return &a
}

func amountPtrFrom(a *float64) *float64 {
	if a == nil {
		return nil
	}
	return amountPtr(*a)
}

func dstr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(dateLayout)
	return &s
}

// monthlyAmount — amount × the times it happens in an average month (what the hero shows as
// "ต่อเดือน"); nil without an amount.
func monthlyAmount(r *Rule, today time.Time) *float64 {
	if r.Amount <= 0 {
		return nil
	}
	v := math.Round(r.Amount*monthlyCount(r, today)*100) / 100
	return &v
}
