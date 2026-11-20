package recurring

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// A plan on a debt item (0.4.3): count installments from a first due date, each with its principal
// and interest, worked out at creation (none / flat / reducing / fixed) or typed (manual), editable
// later. Information and hand-set ticks only: the balance counts debt rows, never installments.

var (
	ErrPlanInvalid         = errors.New("the plan is invalid")
	ErrInstallmentNotFound = errors.New("no such installment on this recurring item")
)

func planInvalid(field, code string) error {
	return apierr.Wrap(ErrPlanInvalid, "invalid plan", apierr.Field("plan."+field, code))
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// addMonths — the same day n months on (31 = the month's last day).
func addMonths(d time.Time, n int) time.Time {
	first := time.Date(d.Year(), d.Month()+time.Month(n), 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	day := d.Day()
	if day > last {
		day = last
	}
	return time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, time.UTC)
}

// computeInstallments — the installments of a plan input (validated).
func computeInstallments(in *PlanInput) ([]Installment, time.Time, error) {
	if in.Principal <= 0 {
		return nil, time.Time{}, planInvalid("principal", apierr.FieldInvalid)
	}
	if in.Count < 1 || in.Count > 600 {
		return nil, time.Time{}, planInvalid("count", apierr.FieldInvalid)
	}
	start, err := parseDate(in.StartDate)
	if err != nil {
		return nil, time.Time{}, planInvalid("start_date", apierr.FieldInvalid)
	}
	rate := 0.0
	if in.InterestRate != nil {
		if *in.InterestRate < 0 || *in.InterestRate > 1000 {
			return nil, time.Time{}, planInvalid("interest_rate", apierr.FieldInvalid)
		}
		rate = *in.InterestRate
	}
	n, p := in.Count, round2(in.Principal)
	out := make([]Installment, n)
	for i := range out {
		out[i] = Installment{Seq: i + 1, DueDate: addMonths(start, i).Format(dateLayout), Status: "upcoming"}
	}
	// principal split evenly, the last one takes the rounding
	even := func() {
		per := round2(p / float64(n))
		for i := range out {
			out[i].Principal = per
		}
		out[n-1].Principal = round2(p - per*float64(n-1))
	}
	switch in.InterestMethod {
	case "none":
		even()
	case "flat": // annual flat rate on the full principal
		if in.InterestRate == nil {
			return nil, time.Time{}, planInvalid("interest_rate", apierr.FieldRequired)
		}
		even()
		total := round2(p * rate / 100 * float64(n) / 12)
		per := round2(total / float64(n))
		for i := range out {
			out[i].Interest = per
		}
		out[n-1].Interest = round2(total - per*float64(n-1))
	case "reducing": // annual rate on the balance left, equal payments
		if in.InterestRate == nil {
			return nil, time.Time{}, planInvalid("interest_rate", apierr.FieldRequired)
		}
		r := rate / 100 / 12
		pay := p / float64(n)
		if r > 0 {
			pay = p * r / (1 - math.Pow(1+r, -float64(n)))
		}
		pay = round2(pay)
		bal := p
		for i := range out {
			in := round2(bal * r)
			pr := round2(pay - in)
			if i == n-1 || pr > bal {
				pr = round2(bal)
			}
			out[i].Interest, out[i].Principal = in, pr
			bal = round2(bal - pr)
		}
	case "fixed": // the lender's installment: what is above the principal is interest
		if in.InstallmentAmount == nil || *in.InstallmentAmount <= 0 {
			return nil, time.Time{}, planInvalid("installment_amount", apierr.FieldRequired)
		}
		amt := round2(*in.InstallmentAmount)
		total := round2(amt*float64(n) - p)
		if total < 0 {
			return nil, time.Time{}, planInvalid("installment_amount", apierr.FieldInvalid)
		}
		even()
		for i := range out {
			out[i].Interest = round2(amt - out[i].Principal)
		}
		if out[n-1].Interest < 0 {
			out[n-1].Interest = 0
		}
	case "manual":
		if len(in.Installments) != n {
			return nil, time.Time{}, planInvalid("installments", apierr.FieldInvalid)
		}
		for i, x := range in.Installments {
			if x.DueDate != nil {
				d, err := parseDate(*x.DueDate)
				if err != nil {
					return nil, time.Time{}, planInvalid(fmt.Sprintf("installments[%d].due_date", i), apierr.FieldInvalid)
				}
				out[i].DueDate = d.Format(dateLayout)
			}
			switch {
			case x.Principal != nil && x.Interest != nil:
				out[i].Principal, out[i].Interest = round2(*x.Principal), round2(*x.Interest)
			case x.Amount != nil:
				out[i].Principal = round2(*x.Amount)
				if x.Interest != nil {
					out[i].Interest = round2(*x.Interest)
					out[i].Principal = round2(*x.Amount - *x.Interest)
				}
			default:
				return nil, time.Time{}, planInvalid(fmt.Sprintf("installments[%d].amount", i), apierr.FieldRequired)
			}
			if out[i].Principal < 0 || out[i].Interest < 0 {
				return nil, time.Time{}, planInvalid(fmt.Sprintf("installments[%d]", i), apierr.FieldInvalid)
			}
		}
	default:
		return nil, time.Time{}, planInvalid("interest_method", apierr.FieldInvalid)
	}
	for i := range out {
		out[i].Amount = round2(out[i].Principal + out[i].Interest)
	}
	return out, start, nil
}

// PlanPreview — POST /recurring/plan-preview: the installments of a plan, nothing saved.
func (s *Service) PlanPreview(in PlanInput) (*InstallmentList, error) {
	rows, _, err := computeInstallments(&in)
	if err != nil {
		return nil, err
	}
	return &InstallmentList{Data: rows}, nil
}

type InstallmentList struct {
	Data []Installment `json:"data"`
}

// setPlanTx — the item's plan from in: its installments (ticks kept by seq) and, when the item
// gets a plan for the first time and it is not on a card, its opening debt row. Answers the
// wallets it moved.
func (s *Service) setPlanTx(ctx context.Context, tx pgx.Tx, it *itemRecord, in *PlanInput) ([]uuid.UUID, error) {
	if it.Type != TypeDebt {
		return nil, ErrValidation
	}
	rows, start, err := computeInstallments(in)
	if err != nil {
		return nil, err
	}
	if in.CardAccountID != nil {
		var typ string
		err := tx.QueryRow(ctx, `SELECT a.type FROM accounts a WHERE a.id = $1 AND `+shared.ActiveMembershipPredicate("a.id", "$2"),
			*in.CardAccountID, it.UserID).Scan(&typ)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAccountNotFound
		}
		if err != nil {
			return nil, err
		}
		if typ != "credit_card" && typ != "pay_later" {
			return nil, planInvalid("card_account_id", apierr.FieldInvalid)
		}
	}
	first := it.Plan == nil
	it.Plan = &planRecord{Principal: round2(in.Principal), Count: in.Count, Start: start, InterestMethod: in.InterestMethod,
		InterestRate: in.InterestRate, InstallmentAmount: in.InstallmentAmount, CardAccountID: in.CardAccountID}
	if err := saveItemRecordTx(ctx, tx, it); err != nil {
		return nil, err
	}
	for _, x := range rows {
		if _, err := tx.Exec(ctx, `INSERT INTO debt_installments (user_id, recurring_id, seq, due_date, amount, principal, interest)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (recurring_id, seq) DO UPDATE SET due_date = EXCLUDED.due_date, amount = EXCLUDED.amount,
				principal = EXCLUDED.principal, interest = EXCLUDED.interest, edited = FALSE`,
			it.UserID, it.ID, x.Seq, x.DueDate, x.Amount, x.Principal, x.Interest); err != nil {
			return nil, fmt.Errorf("installments: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM debt_installments WHERE recurring_id = $1 AND seq > $2`, it.ID, in.Count); err != nil {
		return nil, err
	}
	if !first || in.CardAccountID != nil || s.debts == nil {
		return nil, nil
	}
	// the opening row: the whole principal counts at once (a card plan: the card holds it)
	name := it.counterpartName
	if name == "" {
		name = derefStr(it.PersonName)
	}
	entry := personal_debts.EntryInput{Kind: personal_debts.KindDebt, Direction: *it.DebtDirection, ContactID: it.ContactID,
		PersonName: name, Amount: it.Plan.Principal, Description: &it.Name,
		InstitutionID: it.InstitutionID, RecurringID: &it.ID}
	today, err := shared.UserToday(ctx, tx, it.UserID, shared.HeaderTZ(ctx))
	if err != nil {
		return nil, err
	}
	entry.Date = today.Format(dateLayout)
	var touched []uuid.UUID
	if in.OpeningAccountID != nil {
		entry.CreateTransaction, entry.AccountID = true, in.OpeningAccountID
		touched = append(touched, *in.OpeningAccountID)
	}
	if in.OpeningDate != nil {
		if _, err := parseDate(*in.OpeningDate); err != nil {
			return nil, planInvalid("opening_date", apierr.FieldInvalid)
		}
		entry.Date = *in.OpeningDate
	}
	if _, _, err := s.debts.CreateEntryTx(ctx, tx, it.UserID, entry); err != nil {
		return nil, err
	}
	return touched, nil
}

// dropPlanTx — the plan and its installments go; the item and its debt rows stay.
func dropPlanTx(ctx context.Context, tx pgx.Tx, it *itemRecord) error {
	if _, err := tx.Exec(ctx, `DELETE FROM debt_installments WHERE recurring_id = $1`, it.ID); err != nil {
		return err
	}
	it.Plan = nil
	return nil
}

func installmentStatus(paidAt *time.Time, skipped bool, due, today time.Time) string {
	switch {
	case paidAt != nil:
		return "paid"
	case skipped:
		return "skipped"
	case !due.After(today):
		return "due"
	}
	return "upcoming"
}

func (s *Service) loadInstallments(ctx context.Context, q shared.Querier, userID, itemID uuid.UUID, today time.Time) ([]Installment, error) {
	rows, err := q.Query(ctx, `SELECT seq, due_date, amount::float8, principal::float8, interest::float8, paid_at, skipped,
			personal_debt_id, edited
		FROM debt_installments WHERE recurring_id = $1 AND user_id = $2 ORDER BY seq`, itemID, userID)
	if err != nil {
		return nil, fmt.Errorf("installments: %w", err)
	}
	defer rows.Close()
	out := []Installment{}
	for rows.Next() {
		var (
			x       Installment
			due     time.Time
			skipped bool
		)
		if err := rows.Scan(&x.Seq, &due, &x.Amount, &x.Principal, &x.Interest, &x.PaidAt, &skipped, &x.PersonalDebtID, &x.Edited); err != nil {
			return nil, err
		}
		x.DueDate, x.Status = due.Format(dateLayout), installmentStatus(x.PaidAt, skipped, due, today)
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Service) planView(ctx context.Context, q shared.Querier, userID uuid.UUID, it *itemRecord, today time.Time) (*PlanView, error) {
	p := it.Plan
	if p == nil {
		return nil, nil
	}
	rows, err := s.loadInstallments(ctx, q, userID, it.ID, today)
	if err != nil {
		return nil, err
	}
	v := &PlanView{Principal: p.Principal, Count: p.Count, StartDate: p.Start.Format(dateLayout), InterestMethod: p.InterestMethod,
		InterestRate: p.InterestRate, InstallmentAmount: p.InstallmentAmount, CardAccountID: p.CardAccountID, Closed: p.ClosedAt != nil,
		Due: []Installment{}, Chain: []InstallmentStatus{}}
	if p.CardAccountID != nil {
		refs, err := shared.LoadAccountRefs(ctx, q, []uuid.UUID{*p.CardAccountID})
		if err != nil {
			return nil, err
		}
		if x, ok := refs[*p.CardAccountID]; ok {
			v.CardAccount = &x
		}
		if v.Purchase, err = loadPurchase(ctx, q, userID, it.ID); err != nil {
			return nil, err
		}
	}
	for i := range rows {
		x := rows[i]
		v.Chain = append(v.Chain, InstallmentStatus{Seq: x.Seq, Status: x.Status})
		v.LastDate = x.DueDate
		switch x.Status {
		case "paid":
			v.Done++
			v.PaidPrincipal += x.Principal
			v.PaidTotal += x.Amount
		case "skipped":
		default:
			v.LeftCount++
			v.LeftPrincipal += x.Principal
			v.LeftInterest += x.Interest
			if x.Status == "due" {
				v.Due = append(v.Due, x)
			} else if v.Next == nil {
				n := x
				v.Next = &n
			}
		}
	}
	if v.Next == nil && len(v.Due) > 0 {
		n := v.Due[0]
		v.Next = &n
	}
	v.PaidPrincipal, v.LeftPrincipal, v.LeftInterest = round2(v.PaidPrincipal), round2(v.LeftPrincipal), round2(v.LeftInterest)
	v.LeftTotal, v.PaidTotal = round2(v.LeftPrincipal+v.LeftInterest), round2(v.PaidTotal)
	return v, nil
}

// planItem — the item, which must have a plan.
func (s *Service) planItem(ctx context.Context, q rowQuerier, userID, id uuid.UUID, lock bool) (*itemRecord, error) {
	it, err := getItemRecord(ctx, q, userID, id, lock)
	if err != nil {
		return nil, err
	}
	if it.Plan == nil {
		return nil, ErrInstallmentNotFound
	}
	return it, nil
}

func (s *Service) Installments(ctx context.Context, userID, id uuid.UUID, tz string) (*InstallmentList, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	if _, err := s.planItem(ctx, s.db, userID, id, false); err != nil {
		return nil, err
	}
	rows, err := s.loadInstallments(ctx, s.db, userID, id, today)
	if err != nil {
		return nil, err
	}
	return &InstallmentList{Data: rows}, nil
}

type InstallmentEdit struct {
	DueDate   *string  `json:"due_date"`
	Amount    *float64 `json:"amount"`
	Principal *float64 `json:"principal"`
	Interest  *float64 `json:"interest"`
}

// EditInstallment — "แก้เฉพาะงวดนี้": its date and / or its money (amount alone = all principal
// unless interest is sent; principal + interest = the amount).
func (s *Service) EditInstallment(ctx context.Context, userID, id uuid.UUID, seq int, in InstallmentEdit, tz string) (*ItemPage, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := s.planItem(ctx, tx, userID, id, true); err != nil {
		return nil, err
	}
	var (
		due              time.Time
		amt, prin, inter float64
	)
	err = tx.QueryRow(ctx, `SELECT due_date, amount::float8, principal::float8, interest::float8 FROM debt_installments
		WHERE recurring_id = $1 AND seq = $2 FOR UPDATE`, id, seq).Scan(&due, &amt, &prin, &inter)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInstallmentNotFound
	}
	if err != nil {
		return nil, err
	}
	if in.DueDate != nil {
		if due, err = parseDate(*in.DueDate); err != nil {
			return nil, ErrValidation
		}
	}
	for _, v := range []*float64{in.Amount, in.Principal, in.Interest} {
		if v != nil && *v < 0 {
			return nil, ErrValidation
		}
	}
	switch {
	case in.Principal != nil && in.Interest != nil:
		prin, inter = *in.Principal, *in.Interest
	case in.Amount != nil && in.Interest != nil:
		prin, inter = *in.Amount-*in.Interest, *in.Interest
	case in.Amount != nil && in.Principal != nil:
		prin, inter = *in.Principal, *in.Amount-*in.Principal
	case in.Amount != nil:
		prin = *in.Amount - inter // the interest stays, the principal takes the change
		if prin < 0 {
			prin, inter = *in.Amount, 0
		}
	case in.Principal != nil:
		prin = *in.Principal
	case in.Interest != nil:
		inter = *in.Interest
	}
	if prin < 0 || inter < 0 {
		return nil, ErrValidation
	}
	prin, inter = round2(prin), round2(inter)
	if _, err := tx.Exec(ctx, `UPDATE debt_installments SET due_date = $3, principal = $4, interest = $5, amount = $4::numeric + $5::numeric, edited = TRUE
		WHERE recurring_id = $1 AND seq = $2`, id, seq, due, prin, inter); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetRecurring(ctx, userID, id, tz)
}

type MarkInstallmentRequest struct {
	Paid           bool       `json:"paid"`
	PersonalDebtID *uuid.UUID `json:"personal_debt_id"`
}

// MarkInstallment — the "จ่ายงวดแล้ว" tick (no money moves); paid false clears it.
func (s *Service) MarkInstallment(ctx context.Context, userID, id uuid.UUID, seq int, req MarkInstallmentRequest, tz string) (*ItemPage, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := s.planItem(ctx, tx, userID, id, true); err != nil {
		return nil, err
	}
	if req.PersonalDebtID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM personal_debts WHERE id = $1 AND user_id = $2)`,
			*req.PersonalDebtID, userID).Scan(&ok); err != nil {
			return nil, err
		}
		if !ok {
			return nil, apierr.Wrap(ErrValidation, "unknown personal_debt_id", apierr.Field("personal_debt_id", apierr.FieldInvalid))
		}
	}
	if err := markInstallmentsTx(ctx, tx, userID, id, []int{seq}, req.Paid, req.PersonalDebtID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetRecurring(ctx, userID, id, tz)
}

func markInstallmentsTx(ctx context.Context, tx pgx.Tx, userID, itemID uuid.UUID, seqs []int, paid bool, debtID *uuid.UUID) error {
	for _, seq := range seqs {
		var tag interface{ RowsAffected() int64 }
		var err error
		if paid {
			tag, err = tx.Exec(ctx, `UPDATE debt_installments SET paid_at = COALESCE(paid_at, NOW()), skipped = FALSE,
				personal_debt_id = COALESCE($4, personal_debt_id) WHERE recurring_id = $1 AND user_id = $2 AND seq = $3`,
				itemID, userID, seq, debtID)
		} else {
			tag, err = tx.Exec(ctx, `UPDATE debt_installments SET paid_at = NULL, personal_debt_id = NULL
				WHERE recurring_id = $1 AND user_id = $2 AND seq = $3`, itemID, userID, seq)
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrInstallmentNotFound
		}
	}
	return nil
}

// MarkInstallmentsTx — the ticks a repayment asks for (seqs; nil = the oldest unticked one) on the
// item's plan; nothing when the item has no plan. The debt page's repayment calls it.
func MarkInstallmentsTx(ctx context.Context, tx pgx.Tx, userID, itemID uuid.UUID, seqs []int, debtID *uuid.UUID) error {
	if seqs == nil {
		var seq int
		err := tx.QueryRow(ctx, `SELECT seq FROM debt_installments WHERE recurring_id = $1 AND user_id = $2
			AND paid_at IS NULL AND NOT skipped ORDER BY seq LIMIT 1`, itemID, userID).Scan(&seq)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		seqs = []int{seq}
	}
	return markInstallmentsTx(ctx, tx, userID, itemID, seqs, true, debtID)
}

// ClosePlan — "ปิดแผนก่อนกำหนด": the unticked installments are skipped; the ticked ones stay as
// history.
func (s *Service) ClosePlan(ctx context.Context, userID, id uuid.UUID, tz string) (*ItemPage, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	it, err := s.planItem(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	if it.Plan.ClosedAt != nil {
		return nil, ErrInvalidTransition
	}
	if _, err := tx.Exec(ctx, `UPDATE debt_installments SET skipped = TRUE WHERE recurring_id = $1 AND paid_at IS NULL`, id); err != nil {
		return nil, err
	}
	now := time.Now()
	it.Plan.ClosedAt = &now
	if err := saveItemRecordTx(ctx, tx, it); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetRecurring(ctx, userID, id, tz)
}

// DeletePlan — the plan goes, the item and its debt rows stay.
func (s *Service) DeletePlan(ctx context.Context, userID, id uuid.UUID, tz string) (*ItemPage, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	it, err := s.planItem(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	var lines int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM recurring_rules WHERE recurring_id = $1`, id).Scan(&lines); err != nil {
		return nil, err
	}
	if lines == 0 {
		return nil, ErrLastLine // a card plan's item has nothing left without its plan: delete the item
	}
	if err := dropPlanTx(ctx, tx, it); err != nil {
		return nil, err
	}
	if err := saveItemRecordTx(ctx, tx, it); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetRecurring(ctx, userID, id, tz)
}

type marksKey struct{}

// withMarks — the installments a confirm asks to tick (an empty list = none); absent = the oldest
// open one.
func withMarks(ctx context.Context, seqs *[]int) context.Context {
	if seqs == nil {
		return ctx
	}
	return context.WithValue(ctx, marksKey{}, *seqs)
}

func marksOf(ctx context.Context) []int {
	if v, ok := ctx.Value(marksKey{}).([]int); ok {
		if v == nil {
			return []int{}
		}
		return v
	}
	return nil
}

// loadPurchase — a card plan's purchase (nil when it was not made by quick create, or was deleted).
func loadPurchase(ctx context.Context, q shared.Querier, userID, itemID uuid.UUID) (*PlanPurchase, error) {
	rows, err := q.Query(ctx, `SELECT t.id, t.amount::float8, t.date, t.description, t.category_id
		FROM recurrings r JOIN transactions t ON t.id = r.purchase_transaction_id
		WHERE r.id = $1 AND r.user_id = $2`, itemID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var (
		x    PlanPurchase
		date time.Time
		cat  *uuid.UUID
	)
	if err := rows.Scan(&x.ID, &x.Amount, &date, &x.Description, &cat); err != nil {
		return nil, err
	}
	rows.Close()
	x.Date = date.Format(dateLayout)
	if cat != nil {
		refs, err := shared.LoadCategoryRefs(ctx, q, []uuid.UUID{*cat})
		if err != nil {
			return nil, err
		}
		if c, ok := refs[*cat]; ok {
			x.Category = &c
		}
	}
	return &x, nil
}
