package recurring

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
)

type NextUp struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Date   string    `json:"date"`
	Amount *float64  `json:"amount"` // null = a reminder with no amount yet
	Type   string    `json:"type"`
}

type ListSummary struct {
	MonthPay     float64 `json:"month_pay"`
	MonthReceive float64 `json:"month_receive"`
	Next         *NextUp `json:"next"`
	Due30dTotal         float64  `json:"due_30d_total"`
	AwaitingAmountCount int      `json:"awaiting_amount_count"`
	// DueCount / DueTotal — the occurrences due and not recorded (overdue + today).
	DueCount int     `json:"due_count"`
	DueTotal float64 `json:"due_total"`
	ByKind              KindsSum `json:"by_kind"`
}

const (
	daysPerMonth  = 30.4375
	weeksPerMonth = 52.1775 / 12
)

func monthlyCount(r *Rule, today time.Time) float64 {
	n := float64(r.Interval)
	if n < 1 {
		n = 1
	}
	switch r.Freq {
	case "daily":
		return daysPerMonth / n
	case "weekly":
		days := float64(len(r.Weekdays))
		if days < 1 {
			days = 1
		}
		return days * weeksPerMonth / n
	case "monthly":
		return 1 / n
	case "yearly":
		return 1 / (12 * n)
	case "dates":
		c := 0
		for _, d := range r.Dates {
			if d.Year() == today.Year() && d.Month() == today.Month() {
				c++
			}
		}
		return float64(c)
	}
	return 0
}

func (s *Service) summary(ctx context.Context, userID uuid.UUID, today time.Time) (ListSummary, error) {
	out := ListSummary{}
	var err error
	if err := s.dueWindow(ctx, userID, today, &out); err != nil {
		return out, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+ruleColumns+` FROM recurring_rules r
		WHERE user_id = $1 AND status = 'active'
		  AND NOT EXISTS (SELECT 1 FROM accounts a WHERE a.status = 'archived'
		                  AND a.id IN (r.account_id, r.to_account_id))`, userID)
	if err != nil {
		return out, fmt.Errorf("summary rules: %w", err)
	}
	var rules []*Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			rows.Close()
			return out, err
		}
		rules = append(rules, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(rules) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(rules))
	for i, r := range rules {
		ids[i] = r.ID
	}
	_ = ids
	next := map[uuid.UUID]time.Time{}
	ahead, err := s.occsInRange(ctx, s.db, userID, rules, today, today.AddDate(0, 0, horizonDays), today, true)
	if err != nil {
		return out, fmt.Errorf("summary next: %w", err)
	}
	for _, o := range ahead { // oldest first: the first expected one of a rule is its next
		if _, seen := next[o.rule.ID]; !seen && o.expected() {
			next[o.rule.ID] = o.date
		}
	}
	sort.SliceStable(rules, func(i, j int) bool {
		di, oki := next[rules[i].ID]
		dj, okj := next[rules[j].ID]
		switch {
		case oki && okj && !di.Equal(dj):
			return di.Before(dj)
		case oki != okj:
			return oki
		}
		return rules[i].Name < rules[j].Name
	})
	for _, r := range rules {
		d, upcoming := next[r.ID]
		if !upcoming && r.Freq != "dates" {
			continue // ended: nothing left to happen
		}
		switch r.Type {
		case "expense":
			out.MonthPay += r.Amount * monthlyCount(r, today)
		case "income":
			out.MonthReceive += r.Amount * monthlyCount(r, today)
		}
		k := out.ByKind.of(r.Kind)
		k.Count++
		k.Monthly += r.Amount * monthlyCount(r, today)
		k.Yearly += yearlyCost(r, today)
		if upcoming && out.Next == nil {
			out.Next = &NextUp{ID: r.ID, Name: r.Name, Date: d.Format(dateLayout), Amount: amountPtr(r.Amount), Type: r.Type}
		}
	}
	out.MonthPay = math.Round(out.MonthPay*100) / 100
	out.MonthReceive = math.Round(out.MonthReceive*100) / 100
	for _, k := range []*KindTotals{&out.ByKind.Subscription, &out.ByKind.Installment, &out.ByKind.Income, &out.ByKind.Reminder, &out.ByKind.Saving} {
		k.Monthly, k.Yearly = math.Round(k.Monthly*100)/100, math.Round(k.Yearly*100)/100
	}
	return out, nil
}

type KindTotals struct {
	Count   int     `json:"count"`
	Monthly float64 `json:"monthly"`
	Yearly  float64 `json:"yearly"`
}

type KindsSum struct {
	Subscription KindTotals `json:"subscription"`
	Installment  KindTotals `json:"installment"`
	Income       KindTotals `json:"income"`
	Reminder     KindTotals `json:"reminder"`
	Saving       KindTotals `json:"saving"`
}

func (k *KindsSum) of(kind string) *KindTotals {
	switch kind {
	case KindInstallment:
		return &k.Installment
	case KindIncome:
		return &k.Income
	case KindReminder:
		return &k.Reminder
	case KindSaving:
		return &k.Saving
	}
	return &k.Subscription
}

// dueWindow fills what is due and what falls in the next 30 days: the expense
// total, the occurrences still without an amount, and the overdue ones.
func (s *Service) dueWindow(ctx context.Context, userID uuid.UUID, today time.Time, out *ListSummary) error {
	rules, err := s.activeRules(ctx, s.db, userID)
	if err != nil {
		return err
	}
	occs, err := s.occsInRange(ctx, s.db, userID, rules, today.AddDate(-5, 0, 0), today.AddDate(0, 0, 30), today, true)
	if err != nil {
		return err
	}
	var total, dueTotal float64
	for _, o := range occs {
		if !o.expected() {
			continue
		}
		has := o.draft.Amount != nil && *o.draft.Amount > 0
		if !has {
			out.AwaitingAmountCount++
		}
		if o.status == StatusDue {
			out.DueCount++
			if has && o.draft.Type != nil && *o.draft.Type == "expense" {
				dueTotal += *o.draft.Amount
			}
		}
		if has && o.draft.Type != nil && *o.draft.Type == "expense" {
			total += *o.draft.Amount
		}
	}
	out.Due30dTotal = math.Round(total*100) / 100
	out.DueTotal = math.Round(dueTotal*100) / 100
	return nil
}
