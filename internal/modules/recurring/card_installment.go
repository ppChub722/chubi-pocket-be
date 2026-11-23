package recurring

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
)

// CardInstallmentTx — quick create "ผ่อน" (0.4.3): the purchase is on the card at full price; this
// makes its debt plan: the card is the creditor (the debt is its balance: no debt row, no reminder;
// the card's bill pays the rounds), count installments from the card's next statement. Wired into
// transactions.WithInstallments; answers the plan's id.
func (s *Service) CardInstallmentTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, purchase transactions.TransactionDetail,
	in transactions.InstallmentRequest) (uuid.UUID, error) {
	if purchase.AccountID == nil {
		return uuid.Nil, transactions.ErrInstallmentInvalid
	}
	var statement *int32
	if err := tx.QueryRow(ctx, `SELECT statement_date FROM accounts WHERE id = $1`, *purchase.AccountID).Scan(&statement); err != nil {
		return uuid.Nil, err
	}
	bought, err := parseDate(purchase.Date)
	if err != nil {
		return uuid.Nil, err
	}
	first := addMonths(bought, 1)
	if statement != nil {
		first = nextDay(bought, int(*statement))
	}
	if in.FirstDue != nil {
		if first, err = parseDate(*in.FirstDue); err != nil {
			return uuid.Nil, apierr.Wrap(transactions.ErrInstallmentInvalid, "invalid first_due", apierr.Field("installment.first_due", apierr.FieldInvalid))
		}
	}
	method := in.InterestMethod
	if method == "" {
		method = "none"
	}
	name := "ผ่อน"
	switch {
	case purchase.Description != nil && *purchase.Description != "":
		name = *purchase.Description
	case purchase.Category != nil:
		name = purchase.Category.Name
	}
	if r := []rune(name); len(r) > 100 {
		name = string(r[:100])
	}
	pin := &DebtPlanInput{Name: &name, CategoryID: purchase.CategoryID, Creditor: CreditorInput{AccountID: purchase.AccountID},
		Plan: PlanInput{Principal: purchase.Amount, Count: in.Count, StartDate: first.Format(dateLayout), InterestMethod: method,
			InterestRate: in.InterestRate, InstallmentAmount: in.InstallmentAmount}}
	p, _, err := s.createPlanTx(ctx, tx, userID, pin, &purchase.ID)
	if err != nil {
		if errors.Is(err, ErrPlanInvalid) {
			fields := apierr.FieldsOf(err)
			for i := range fields {
				fields[i].Field = "installment." + strings.TrimPrefix(fields[i].Field, "plan.")
			}
			return uuid.Nil, apierr.Wrap(transactions.ErrInstallmentInvalid, "invalid installment", fields...)
		}
		return uuid.Nil, err
	}
	return p.ID, nil
}

// nextDay — the first date after d on day-of-month day (31 = the month's last day).
func nextDay(d time.Time, day int) time.Time {
	c := clampDay(d.Year(), d.Month(), day)
	if !c.After(d) {
		c = clampDay(d.Year(), d.Month()+1, day)
	}
	return c
}

func clampDay(y int, m time.Month, day int) time.Time {
	first := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	if day > last {
		day = last
	}
	return time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, time.UTC)
}
