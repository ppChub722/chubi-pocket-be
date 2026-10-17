package recurring

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
)

// NextUp — RecurringList.summary.next.
type NextUp struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Date   string    `json:"date"`
	Amount *float64  `json:"amount"` // null = a reminder with no amount yet
	Type   string    `json:"type"`
}

// ListSummary — RecurringList.summary: the list's summary card.
type ListSummary struct {
	MonthPay     float64 `json:"month_pay"`
	MonthReceive float64 `json:"month_receive"`
	Next         *NextUp `json:"next"`
	// 0.4.0: due_30d_total = what is still to pay in the next 30 days (expense items, known amounts); awaiting_amount_count = occurrences (not done, due within 30 days) with no amount yet; by_kind = per kind, over the active settings.
	Due30dTotal         float64  `json:"due_30d_total"`
	AwaitingAmountCount int      `json:"awaiting_amount_count"`
	ByKind              KindsSum `json:"by_kind"`
}

const (
	daysPerMonth  = 30.4375
	weeksPerMonth = 52.1775 / 12
)

// monthlyCount — how many times a setting happens in an average month
// (custom dates: the dates inside the month of `today`).
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

// summary — the list's summary card over the ACTIVE settings (see the spec).
func (s *Service) summary(ctx context.Context, userID uuid.UUID, today time.Time) (ListSummary, error) {
	out := ListSummary{}
	var err error
	if out.Due30dTotal, out.AwaitingAmountCount, err = s.dueWindow(ctx, userID, today); err != nil {
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
	next := map[uuid.UUID]time.Time{}
	nr, err := s.db.Query(ctx, `SELECT rule_id, MIN(date) FROM recurring_items
		WHERE rule_id = ANY($1) AND produced_at IS NULL AND pending_id IS NULL
		  AND transaction_id IS NULL AND saving_entry_id IS NULL AND NOT archived AND date >= $2
		GROUP BY rule_id`, ids, today)
	if err != nil {
		return out, fmt.Errorf("summary next: %w", err)
	}
	for nr.Next() {
		var (
			id uuid.UUID
			d  time.Time
		)
		if err := nr.Scan(&id, &d); err != nil {
			nr.Close()
			return out, err
		}
		next[id] = d
	}
	nr.Close()
	if err := nr.Err(); err != nil {
		return out, err
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
	for _, k := range []*KindTotals{&out.ByKind.Subscription, &out.ByKind.Installment, &out.ByKind.Income, &out.ByKind.Reminder} {
		k.Monthly, k.Yearly = math.Round(k.Monthly*100)/100, math.Round(k.Yearly*100)/100
	}
	return out, nil
}

// KindTotals — one kind's card: how many active settings, their per-month
// equivalent and their yearly cost.
type KindTotals struct {
	Count   int     `json:"count"`
	Monthly float64 `json:"monthly"`
	Yearly  float64 `json:"yearly"`
}

// KindsSum — by_kind.
type KindsSum struct {
	Subscription KindTotals `json:"subscription"`
	Installment  KindTotals `json:"installment"`
	Income       KindTotals `json:"income"`
	Reminder     KindTotals `json:"reminder"`
}

func (k *KindsSum) of(kind string) *KindTotals {
	switch kind {
	case KindInstallment:
		return &k.Installment
	case KindIncome:
		return &k.Income
	case KindReminder:
		return &k.Reminder
	}
	return &k.Subscription
}

// dueWindow — the occurrences of the next 30 days that are still to happen.
func (s *Service) dueWindow(ctx context.Context, userID uuid.UUID, today time.Time) (total float64, awaiting int, err error) {
	err = s.db.QueryRow(ctx, `
		SELECT COALESCE(SUM((i.draft->>'amount')::numeric) FILTER (WHERE i.draft->>'type' = 'expense'
		                   AND COALESCE((i.draft->>'amount')::numeric, 0) > 0), 0)::float8,
		       COUNT(*) FILTER (WHERE COALESCE((i.draft->>'amount')::numeric, 0) <= 0)
		FROM recurring_items i LEFT JOIN recurring_rules r ON r.id = i.rule_id
		WHERE i.user_id = $1 AND i.transaction_id IS NULL AND i.saving_entry_id IS NULL AND NOT i.archived
		  AND i.date <= $2 AND (i.rule_id IS NULL OR r.status = 'active')`, userID, today.AddDate(0, 0, 30)).Scan(&total, &awaiting)
	total = math.Round(total*100) / 100
	return
}
