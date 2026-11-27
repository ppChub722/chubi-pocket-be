package recurring

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Card / PayLater summary (0.4.3): the debt is the wallet's balance. Its bill = what was spent on it
// in the last closed cycle (plan purchases left out: their installments are billed instead) + the
// plan installments due in that cycle; paid = what went into the card after the cycle closed. A
// card bill reminder (amount asked) starts each round with what is left of that bill.

// CardBill — one statement cycle of a card.
type CardBill struct {
	CycleStart   string  `json:"cycle_start"`
	CycleEnd     string  `json:"cycle_end"` // the statement date
	DueDate      *string `json:"due_date"`  // the payment due date after it (null without payment_due_date)
	Spending     float64 `json:"spending"`
	Installments float64 `json:"installments"`
	Total        float64 `json:"total"`
	Paid         float64 `json:"paid"`
	Left         float64 `json:"left"`
}

// CardSummary — GET /card-summaries(/{account_id}).
type CardSummary struct {
	Account      shared.AccountRef `json:"account"`
	Owed         float64           `json:"owed"` // what the card holds now (its negative balance)
	CreditLimit  *float64          `json:"credit_limit"`
	StatementDay *int              `json:"statement_day"`
	DueDay       *int              `json:"due_day"`
	Bill         CardBill          `json:"bill"`         // the last closed cycle
	Unbilled     float64           `json:"unbilled"`     // plan principal not billed yet (inside owed)
	Plans        []PlanView        `json:"plans"`        // its open plans
	RecurringID  *uuid.UUID        `json:"recurring_id"` // the item that pays its bill
}

type CardSummaryList struct {
	Data []CardSummary `json:"data"`
}

type cardRow struct {
	id                uuid.UUID
	balance           float64
	limit             *float64
	statement, dueDay *int16
}

func loadCards(ctx context.Context, q shared.Querier, userID uuid.UUID, only *uuid.UUID) ([]cardRow, error) {
	rows, err := q.Query(ctx, `SELECT a.id, a.balance::float8, a.credit_limit::float8, a.statement_date, a.payment_due_date
		FROM accounts a WHERE a.type IN ('credit_card', 'pay_later') AND a.status = 'active'
		AND ($2::uuid IS NULL OR a.id = $2) AND `+shared.ActiveMembershipPredicate("a.id", "$1")+`
		ORDER BY a.sort_order, a.created_at`, userID, only)
	if err != nil {
		return nil, fmt.Errorf("cards: %w", err)
	}
	defer rows.Close()
	var out []cardRow
	for rows.Next() {
		var c cardRow
		if err := rows.Scan(&c.id, &c.balance, &c.limit, &c.statement, &c.dueDay); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// cardCycle — the last cycle that closed before ref (its statement date < ref): with no statement
// day, the calendar month before ref's.
func cardCycle(statement *int16, ref time.Time) (time.Time, time.Time) {
	if statement == nil {
		first := time.Date(ref.Year(), ref.Month(), 1, 0, 0, 0, 0, time.UTC)
		return first.AddDate(0, -1, 0), first.AddDate(0, 0, -1)
	}
	s := int(*statement)
	end := clampDay(ref.Year(), ref.Month(), s)
	if !end.Before(ref) {
		end = clampDay(ref.Year(), ref.Month()-1, s)
	}
	prev := clampDay(end.Year(), end.Month()-1, s)
	return prev.AddDate(0, 0, 1), end
}

// cardBill — the bill of card c for the cycle that closed before ref.
func cardBill(ctx context.Context, q rowQuerier, userID uuid.UUID, c cardRow, ref time.Time) (CardBill, error) {
	start, end := cardCycle(c.statement, ref)
	b := CardBill{CycleStart: start.Format(dateLayout), CycleEnd: end.Format(dateLayout)}
	if c.dueDay != nil {
		d := nextDay(end, int(*c.dueDay))
		s := d.Format(dateLayout)
		b.DueDate = &s
	}
	err := q.QueryRow(ctx, `SELECT
		COALESCE((SELECT SUM(t.amount) FROM transactions t WHERE t.account_id = $1 AND t.type = 'expense'
			AND t.date BETWEEN $2 AND $3 AND NOT EXISTS (SELECT 1 FROM debt_plans p WHERE p.purchase_transaction_id = t.id)
			AND NOT EXISTS (SELECT 1 FROM categories c WHERE c.id = t.category_id AND c.system_kind IN ('TRANSFER_OUT', 'WITHDRAW'))), 0)::float8,
		COALESCE((SELECT SUM(i.amount) FROM debt_installments i JOIN debt_plans p ON p.id = i.debt_plan_id
			WHERE p.account_id = $1 AND p.user_id = $4 AND NOT i.skipped AND i.due_date BETWEEN $2 AND $3), 0)::float8,
		COALESCE((SELECT SUM(t.amount) FROM transactions t JOIN categories c ON c.id = t.category_id
			WHERE t.account_id = $1 AND t.type IN ('transfer', 'income') AND t.date > $3 AND c.system_kind IN ('TRANSFER_IN', 'DEPOSIT')), 0)::float8`,
		c.id, start, end, userID).Scan(&b.Spending, &b.Installments, &b.Paid)
	if err != nil {
		return b, fmt.Errorf("card bill: %w", err)
	}
	b.Spending, b.Installments, b.Paid = round2(b.Spending), round2(b.Installments), round2(b.Paid)
	b.Total = round2(b.Spending + b.Installments)
	if b.Left = round2(b.Total - b.Paid); b.Left < 0 {
		b.Left = 0
	}
	return b, nil
}

func (s *Service) cardSummaries(ctx context.Context, userID uuid.UUID, only *uuid.UUID, tz string) ([]CardSummary, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	cards, err := loadCards(ctx, s.db, userID, only)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(cards))
	for i, c := range cards {
		ids[i] = c.id
	}
	refs, err := shared.LoadAccountRefs(ctx, s.db, ids)
	if err != nil {
		return nil, err
	}
	out := []CardSummary{}
	for _, c := range cards {
		sm := CardSummary{Account: refs[c.id], CreditLimit: c.limit, Plans: []PlanView{}}
		if c.balance < 0 {
			sm.Owed = round2(-c.balance)
		}
		if c.statement != nil {
			v := int(*c.statement)
			sm.StatementDay = &v
		}
		if c.dueDay != nil {
			v := int(*c.dueDay)
			sm.DueDay = &v
		}
		if sm.Bill, err = cardBill(ctx, s.db, userID, c, today.AddDate(0, 0, 1)); err != nil {
			return nil, err
		}
		billed, _ := time.Parse(dateLayout, sm.Bill.CycleEnd)
		if err := s.db.QueryRow(ctx, `SELECT COALESCE(SUM(i.principal), 0)::float8 FROM debt_installments i
			JOIN debt_plans p ON p.id = i.debt_plan_id WHERE p.account_id = $1 AND p.user_id = $2
			AND i.paid_at IS NULL AND NOT i.skipped AND i.due_date > $3`, c.id, userID, billed).Scan(&sm.Unbilled); err != nil {
			return nil, err
		}
		sm.Unbilled = round2(sm.Unbilled)
		plans, err := s.ListDebtPlans(ctx, userID, &c.id, tz)
		if err != nil {
			return nil, err
		}
		for _, p := range plans.Data {
			if !p.Closed {
				sm.Plans = append(sm.Plans, p)
			}
		}
		var item uuid.UUID
		err = s.db.QueryRow(ctx, `SELECT r.recurring_id FROM recurring_rules r WHERE r.user_id = $1 AND r.type = 'transfer'
			AND r.to_account_id = $2 AND r.status <> 'archived' ORDER BY r.created_at LIMIT 1`, userID, c.id).Scan(&item)
		switch {
		case err == nil:
			sm.RecurringID = &item
		case !errors.Is(err, pgx.ErrNoRows):
			return nil, err
		}
		out = append(out, sm)
	}
	return out, nil
}

// CardSummaries — GET /card-summaries: every card / PayLater wallet of mine.
func (s *Service) CardSummaries(ctx context.Context, userID uuid.UUID, tz string) (*CardSummaryList, error) {
	list, err := s.cardSummaries(ctx, userID, nil, tz)
	if err != nil {
		return nil, err
	}
	return &CardSummaryList{Data: list}, nil
}

// CardSummaryOf — GET /card-summaries/{account_id}.
func (s *Service) CardSummaryOf(ctx context.Context, userID, accountID uuid.UUID, tz string) (*CardSummary, error) {
	list, err := s.cardSummaries(ctx, userID, &accountID, tz)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrAccountNotFound
	}
	return &list[0], nil
}

// cardBillPrefill — what is left of the card's bill for a bill round on date (nil: not a card,
// nothing left).
func cardBillPrefill(ctx context.Context, q shared.Querier, userID, card uuid.UUID, date time.Time) (*float64, error) {
	cards, err := loadCards(ctx, q, userID, &card)
	if err != nil || len(cards) == 0 {
		return nil, err
	}
	rq, ok := q.(rowQuerier)
	if !ok {
		return nil, nil
	}
	b, err := cardBill(ctx, rq, userID, cards[0], date)
	if err != nil || b.Left <= 0 {
		return nil, err
	}
	return &b.Left, nil
}
