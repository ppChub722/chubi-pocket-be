package recurring

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// CounterpartyItem — a recurring on a counterparty's debt page: the list row, the plan of its rules
// and only the rules that touch this counterparty (spec §3: pages pull rules by their columns).
type CounterpartyItem struct {
	RecurringSummary
	Plan  *PlanView `json:"plan"`
	Lines []Line    `json:"rules"`
}

// CounterpartyItems — the recurrings (not archived) with a debt rule of a counterparty key (c: / n:
// / i:), newest first. The debts page reads it through personal_debts' hook.
func (s *Service) CounterpartyItems(ctx context.Context, userID uuid.UUID, key string) (any, error) {
	out := []CounterpartyItem{}
	rows, err := s.db.Query(ctx, `SELECT `+itemRecordColumns+` FROM recurrings g WHERE user_id = $1
		AND status <> 'archived' AND EXISTS (SELECT 1 FROM recurring_rules r WHERE r.recurring_id = g.id AND r.type = 'debt'
			AND `+ruleKeySQL+` = $2) ORDER BY created_at DESC, id`, userID, key)
	if err != nil {
		return nil, fmt.Errorf("counterparty items: %w", err)
	}
	var items []*itemRecord
	for rows.Next() {
		it, err := scanItemRecord(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(items) == 0 {
		return out, err
	}
	today, err := s.today(ctx, userID, shared.HeaderTZ(ctx))
	if err != nil {
		return nil, err
	}
	sums, _, err := s.summaries(ctx, userID, today, items)
	if err != nil {
		return nil, err
	}
	pages, err := s.itemPages(ctx, userID, today, items, false)
	if err != nil {
		return nil, err
	}
	for i := range pages {
		lines := []Line{}
		for _, ln := range pages[i].Lines {
			if ln.Counterparty != nil && ln.Counterparty.Key == key {
				lines = append(lines, ln)
			}
		}
		out = append(out, CounterpartyItem{RecurringSummary: sums[i], Plan: pages[i].Plan, Lines: lines})
	}
	return out, nil
}

// ruleKeySQL — a debt rule's counterparty key (alias r), as shared.DebtKey writes it.
const ruleKeySQL = `(CASE WHEN r.institution_id IS NOT NULL THEN 'i:' || r.institution_id::text
	WHEN r.contact_id IS NOT NULL THEN 'c:' || r.contact_id::text
	ELSE 'n:' || LOWER(TRIM(r.person_name)) END)`

// DebtMonth — this month's rounds of the active debt lines still to book (not person-paid ones:
// they move no money of mine): money out (the rows I pay: lend / pay back) and in.
func (s *Service) DebtMonth(ctx context.Context, userID uuid.UUID) (pay, receive float64, err error) {
	today, err := s.today(ctx, userID, shared.HeaderTZ(ctx))
	if err != nil {
		return 0, 0, err
	}
	rules, err := s.activeRules(ctx, s.db, userID)
	if err != nil {
		return 0, 0, err
	}
	var debt []*Rule
	for _, r := range rules {
		if r.isDebt() && r.PaidByPersonName == nil && r.Status == "active" {
			debt = append(debt, r)
		}
	}
	if len(debt) == 0 {
		return 0, 0, nil
	}
	first, last := shared.MonthBounds(today)
	occs, err := s.occsInRange(ctx, s.db, userID, debt, first, last, today, true)
	if err != nil {
		return 0, 0, err
	}
	for _, o := range occs {
		if !o.expected() || o.draft.Amount == nil {
			continue
		}
		if o.rule.DebtDirection != nil && *o.rule.DebtDirection == "paid" {
			pay += *o.draft.Amount
		} else {
			receive += *o.draft.Amount
		}
	}
	return pay, receive, nil
}
