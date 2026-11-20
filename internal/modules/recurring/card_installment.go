package recurring

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
)

// CardInstallmentTx — quick create "ผ่อน" (0.4.3): the purchase is on the card at full price; this
// makes its debt item: the card's institution, a plan of `count` installments on the card
// (card_account_id: no opening row, no line, it is paid with the card's statement). Wired into
// transactions.WithInstallments.
func (s *Service) CardInstallmentTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, purchase transactions.TransactionDetail,
	in transactions.InstallmentRequest) (uuid.UUID, error) {
	if purchase.AccountID == nil {
		return uuid.Nil, transactions.ErrInstallmentNeedsInstitution
	}
	var (
		inst      *uuid.UUID
		statement *int32
	)
	if err := tx.QueryRow(ctx, `SELECT institution_id, statement_date FROM accounts WHERE id = $1`, *purchase.AccountID).
		Scan(&inst, &statement); err != nil {
		return uuid.Nil, err
	}
	if inst == nil {
		return uuid.Nil, transactions.ErrInstallmentNeedsInstitution
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
	it := &itemRecord{UserID: userID, Name: name, Type: TypeDebt, Status: "active", InstitutionID: inst,
		DebtDirection: ptr("received"), CategoryID: purchase.CategoryID, TagIDs: []uuid.UUID{}}
	if err := resolveCounterpartyTx(ctx, tx, userID, it); err != nil {
		return uuid.Nil, err
	}
	if err := saveItemRecordTx(ctx, tx, it); err != nil {
		return uuid.Nil, err
	}
	plan := &PlanInput{Principal: purchase.Amount, Count: in.Count, StartDate: first.Format(dateLayout), InterestMethod: method,
		InterestRate: in.InterestRate, InstallmentAmount: in.InstallmentAmount, CardAccountID: purchase.AccountID}
	if _, err := s.setPlanTx(ctx, tx, it, plan); err != nil {
		if errors.Is(err, ErrPlanInvalid) {
			fields := apierr.FieldsOf(err)
			for i := range fields {
				fields[i].Field = "installment." + fields[i].Field[len("plan."):]
			}
			return uuid.Nil, apierr.Wrap(transactions.ErrInstallmentInvalid, "invalid installment", fields...)
		}
		return uuid.Nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE recurrings SET purchase_transaction_id = $1 WHERE id = $2`, purchase.ID, it.ID); err != nil {
		return uuid.Nil, err
	}
	return it.ID, nil
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
