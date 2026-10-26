package accounts

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

func (s *Service) decorate(ctx context.Context, userID uuid.UUID, accs []Account, tz string) error {
	if len(accs) == 0 {
		return nil
	}
	today, err := shared.UserToday(ctx, s.store.Pool(), userID, tz)
	if err != nil {
		return err
	}
	first, last := shared.MonthBounds(today)
	if last.After(today) {
		last = today // a future-dated transaction counts from its day
	}
	ids := make([]uuid.UUID, len(accs))
	for i, a := range accs {
		ids[i] = a.ID
	}
	rows, err := s.store.Pool().Query(ctx, `
		SELECT t.account_id,
		       COALESCE(SUM(`+shared.CashAmountSQL("t")+`) FILTER (WHERE t.type IN ('income', 'transfer')), 0)::float8,
		       COALESCE(SUM(`+shared.CashAmountSQL("t")+`) FILTER (WHERE t.type = 'expense'), 0)::float8
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id
		LEFT JOIN categories c ON c.id = t.category_id
		WHERE t.account_id = ANY($1)
		  AND (t.type IN ('income', 'expense')
		       OR (t.type = 'transfer' AND c.system_kind = 'TRANSFER_IN' AND a.type IN ('credit_card', 'pay_later')))
		  AND t.date BETWEEN $2 AND $3
		  AND COALESCE(c.system_kind NOT IN ('OPENING_IN', 'OPENING_OUT', 'ADJUST_IN', 'ADJUST_OUT',
		                                     'DEBT_PAID', 'DEBT_RECEIVED'), TRUE)
		GROUP BY t.account_id`, ids, first, last)
	if err != nil {
		return fmt.Errorf("wallet month flow: %w", err)
	}
	in, out := map[uuid.UUID]float64{}, map[uuid.UUID]float64{}
	for rows.Next() {
		var (
			id   uuid.UUID
			i, o float64
		)
		if err := rows.Scan(&id, &i, &o); err != nil {
			rows.Close()
			return err
		}
		in[id], out[id] = round2(i), round2(o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range accs {
		accs[i].MonthIn, accs[i].MonthOut = in[accs[i].ID], out[accs[i].ID]
		if IsCreditType(accs[i].Type) && accs[i].CreditLimit != nil && *accs[i].CreditLimit > 0 {
			used := math.Max(0, -accs[i].Balance)
			pct := int(math.Round(used / *accs[i].CreditLimit * 100))
			avail := round2(math.Max(0, *accs[i].CreditLimit-used))
			accs[i].CreditUsedPct, accs[i].CreditAvailable = &pct, &avail
		}
		if accs[i].PaymentDueDate != nil && IsCreditType(accs[i].Type) {
			next := nextDayOfMonth(today, *accs[i].PaymentDueDate)
			d := next.Format("2006-01-02")
			days := int(math.Round(next.Sub(today).Hours() / 24))
			accs[i].DueDateNext, accs[i].DaysUntilDue = &d, &days
		}
	}
	return nil
}

func nextDayOfMonth(today time.Time, day int) time.Time {
	pick := func(y int, m time.Month) time.Time {
		last := time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
		d := day
		if d > last {
			d = last
		}
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	c := pick(today.Year(), today.Month())
	if c.Before(today) {
		nm := today.AddDate(0, 1, 0)
		c = pick(nm.Year(), nm.Month())
	}
	return c
}

func (s *Service) getDecorated(ctx context.Context, userID, id uuid.UUID, tz string) (*Account, error) {
	a, err := s.store.GetByID(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	one := []Account{*a}
	if err := s.decorate(ctx, userID, one, tz); err != nil {
		return nil, err
	}
	return &one[0], nil
}
