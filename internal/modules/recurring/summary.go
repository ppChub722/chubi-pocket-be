package recurring

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type NextUp struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Date   string    `json:"date"`
	Amount *float64  `json:"amount"` // null = a reminder with no amount yet
	Type   string    `json:"type"`
}

type ListSummary struct {
	MonthPay            float64 `json:"month_pay"`
	MonthReceive        float64 `json:"month_receive"`
	Next                *NextUp `json:"next"`
	Due30dTotal         float64 `json:"due_30d_total"`
	AwaitingAmountCount int     `json:"awaiting_amount_count"`
	// DueCount / DueTotal — the occurrences due and not recorded (overdue + today).
	DueCount int      `json:"due_count"`
	DueTotal float64  `json:"due_total"`
	ByKind   KindsSum `json:"by_kind"`
	// Month — the list hero (spec §3): this calendar month's rounds.
	Month MonthHero `json:"month"`
}

// MonthHero — this month's rounds by what they book. Expense leaves out saving and installments; a
// round with no amount yet counts its rule's average (ExpenseApprox = some are estimates, shown
// "~"). Installment = the debt rules' rounds + card plans' installments billed this month.
type MonthHero struct {
	Expense       float64 `json:"expense"`
	ExpenseApprox bool    `json:"expense_approx"`
	ExpensePaid   float64 `json:"expense_paid"`
	Income        float64 `json:"income"`
	Saving        float64 `json:"saving"`
	Installment   float64 `json:"installment"`
}

// monthHero — the hero figures over the active rules' rounds of today's month.
func (s *Service) monthHero(ctx context.Context, userID uuid.UUID, rules []*Rule, today time.Time) (MonthHero, error) {
	var out MonthHero
	first, last := shared.MonthBounds(today)
	occs, err := s.occsInRange(ctx, s.db, userID, rules, first, last, today, true)
	if err != nil {
		return out, err
	}
	var ids []uuid.UUID
	for _, r := range rules {
		ids = append(ids, r.ID)
	}
	// each rule's average recorded amount (the estimate of an unknown one) and, in the same read, the card
	// plans' installments billed this month (the null row)
	avg := map[uuid.UUID]float64{}
	var cards float64
	rows, err := s.db.Query(ctx, `SELECT recurring_rule_id, AVG((draft->>'amount')::numeric)::float8 FROM recurring_items
		WHERE user_id = $1 AND recurring_rule_id = ANY($2) AND (draft->>'amount') IS NOT NULL
		  AND (transaction_id IS NOT NULL OR debt_entry_id IS NOT NULL OR saving_entry_id IS NOT NULL OR cleared_at IS NOT NULL)
		GROUP BY recurring_rule_id
		UNION ALL
		SELECT NULL, COALESCE(SUM(i.amount), 0)::float8 FROM debt_installments i JOIN debt_plans p ON p.id = i.debt_plan_id
		WHERE p.user_id = $1 AND p.account_id IS NOT NULL AND NOT i.skipped AND i.due_date BETWEEN $3 AND $4`, userID, ids, first, last)
	if err != nil {
		return out, fmt.Errorf("rule averages: %w", err)
	}
	for rows.Next() {
		var (
			id *uuid.UUID
			v  float64
		)
		if err := rows.Scan(&id, &v); err != nil {
			rows.Close()
			return out, err
		}
		if id == nil {
			cards = v
		} else {
			avg[*id] = v
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	for _, o := range occs {
		if o.status == StatusSkipped {
			continue
		}
		r := o.rule
		amt, known := 0.0, o.draft.Amount != nil && *o.draft.Amount > 0
		if known {
			amt = *o.draft.Amount
		} else if v, ok := avg[r.ID]; ok {
			amt = v
		} else {
			amt = r.Amount
		}
		done := o.status == StatusDone || o.status == StatusPaid
		switch {
		case r.Type == TypeSaving:
			out.Saving += amt
		case r.Type == typeDebt:
			if r.moneyWay() == "expense" {
				out.Installment += amt
			}
		case r.Type == TypeIncome:
			out.Income += amt
		case r.Type == TypeExpense:
			out.Expense += amt
			if !known {
				out.ExpenseApprox = true
			}
			if done {
				out.ExpensePaid += amt
			}
		}
	}
	out.Installment += cards
	for _, v := range []*float64{&out.Expense, &out.ExpensePaid, &out.Income, &out.Saving, &out.Installment} {
		*v = math.Round(*v*100) / 100
	}
	return out, nil
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
		WHERE user_id = $1 AND status = 'active'`, userID)
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
	if out.Month, err = s.monthHero(ctx, userID, rules, today); err != nil {
		return out, err
	}
	if len(rules) == 0 {
		return out, nil
	}
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
		switch r.moneyWay() {
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
	Debt         KindTotals `json:"debt"`
}

func (k *KindsSum) of(kind string) *KindTotals {
	switch kind {
	case KindDebt:
		return &k.Debt
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
		pay := o.draft.Type != nil && (*o.draft.Type == "expense" || (*o.draft.Type == typeDebt && o.rule.moneyWay() == "expense"))
		if o.status == StatusDue {
			out.DueCount++
			if has && pay {
				dueTotal += *o.draft.Amount
			}
		}
		if has && pay {
			total += *o.draft.Amount
		}
	}
	out.Due30dTotal = math.Round(total*100) / 100
	out.DueTotal = math.Round(dueTotal*100) / 100
	return nil
}
