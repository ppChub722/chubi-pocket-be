package recurring

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/institutions"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Debt plans (0.4.3): ผ่อน / a loan repaid in installments, owned by the debts page. The creditor
// is a card / PayLater wallet (the debt is the card's balance: no debt row, no payment here — the
// card bill pays it), an institution or a person. count installments from a first due date, each
// with its principal and interest, worked out at creation (none / flat / reducing / fixed) or typed
// (manual), editable later. Paying one books a repayment (the principal) and the interest (an
// expense in the system category ดอกเบี้ย). A recurring item of type debt may pay it by reminder.

var (
	ErrPlanInvalid         = errors.New("the plan is invalid")
	ErrPlanNotFound        = errors.New("debt plan not found")
	ErrInstallmentNotFound = errors.New("no such installment on this plan")
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

// PlanPreview — POST /debt-plans/preview: the installments of a plan, nothing saved.
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

// ---- storage ----

type planRecord struct {
	ID                    uuid.UUID
	UserID                uuid.UUID
	Name                  string
	Description           *string
	Note                  *string
	CategoryID            *uuid.UUID
	Direction             string // paid (they owe me) | received (I owe them)
	AccountID             *uuid.UUID
	InstitutionID         *uuid.UUID
	ContactID             *uuid.UUID
	PersonName            *string
	Origin                string // purchase | cash
	Principal             float64
	Count                 int
	Start                 time.Time
	InterestMethod        string
	InterestRate          *float64
	InstallmentAmount     *float64
	PurchaseTransactionID *uuid.UUID
	ClosedAt              *time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
	creditorName          string // resolved: the card / institution / contact name
}

// isCard — a card / PayLater plan: the card holds the debt and its bill pays the rounds.
func (p *planRecord) isCard() bool { return p.AccountID != nil }

const planColumns = `id, user_id, name, description, note, category_id, direction, account_id, institution_id,
	contact_id, person_name, origin, principal::float8, count, start_date, interest_method, interest_rate::float8,
	installment_amount::float8, purchase_transaction_id, closed_at, created_at, updated_at`

func scanPlan(row pgx.Row) (*planRecord, error) {
	var (
		p     planRecord
		count int32
	)
	if err := row.Scan(&p.ID, &p.UserID, &p.Name, &p.Description, &p.Note, &p.CategoryID, &p.Direction, &p.AccountID,
		&p.InstitutionID, &p.ContactID, &p.PersonName, &p.Origin, &p.Principal, &count, &p.Start, &p.InterestMethod,
		&p.InterestRate, &p.InstallmentAmount, &p.PurchaseTransactionID, &p.ClosedAt, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Count = int(count)
	return &p, nil
}

func getPlan(ctx context.Context, q rowQuerier, userID, id uuid.UUID, lock bool) (*planRecord, error) {
	sql := `SELECT ` + planColumns + ` FROM debt_plans WHERE id = $1 AND user_id = $2`
	if lock {
		sql += ` FOR UPDATE`
	}
	p, err := scanPlan(q.QueryRow(ctx, sql, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPlanNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("debt plan: %w", err)
	}
	return p, nil
}

// plansByID — the plans of ids (any user's rows are filtered by userID).
func plansByID(ctx context.Context, q shared.Querier, userID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]*planRecord, error) {
	out := map[uuid.UUID]*planRecord{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT `+planColumns+` FROM debt_plans WHERE user_id = $1 AND id = ANY($2)`, userID, ids)
	if err != nil {
		return nil, fmt.Errorf("debt plans: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out[p.ID] = p
	}
	return out, rows.Err()
}

func savePlanTx(ctx context.Context, tx pgx.Tx, p *planRecord) error {
	args := []any{p.ID, p.UserID, p.Name, shared.CleanText(p.Description), shared.CleanText(p.Note), p.CategoryID, p.Direction,
		p.AccountID, p.InstitutionID, p.ContactID, p.PersonName, p.Origin, p.Principal, p.Count, p.Start, p.InterestMethod,
		p.InterestRate, p.InstallmentAmount, p.PurchaseTransactionID, p.ClosedAt}
	var err error
	if p.ID == uuid.Nil {
		p.ID, _ = uuid.NewV7()
		args[0] = p.ID
		_, err = tx.Exec(ctx, `INSERT INTO debt_plans (id, user_id, name, description, note, category_id, direction, account_id,
			institution_id, contact_id, person_name, origin, principal, count, start_date, interest_method, interest_rate,
			installment_amount, purchase_transaction_id, closed_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)`, args...)
	} else {
		_, err = tx.Exec(ctx, `UPDATE debt_plans SET name = $3, description = $4, note = $5, category_id = $6, direction = $7,
			account_id = $8, institution_id = $9, contact_id = $10, person_name = $11, origin = $12, principal = $13, count = $14,
			start_date = $15, interest_method = $16, interest_rate = $17, installment_amount = $18,
			purchase_transaction_id = $19, closed_at = $20
			WHERE id = $1 AND user_id = $2`, args...)
	}
	if err != nil {
		return fmt.Errorf("save debt plan: %w", err)
	}
	return nil
}

// writeInstallmentsTx — the plan's installments become rows (ticks kept by seq).
func writeInstallmentsTx(ctx context.Context, tx pgx.Tx, p *planRecord, rows []Installment) error {
	for _, x := range rows {
		if _, err := tx.Exec(ctx, `INSERT INTO debt_installments (user_id, debt_plan_id, seq, due_date, amount, principal, interest)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (debt_plan_id, seq) DO UPDATE SET due_date = EXCLUDED.due_date, amount = EXCLUDED.amount,
				principal = EXCLUDED.principal, interest = EXCLUDED.interest, edited = FALSE`,
			p.UserID, p.ID, x.Seq, x.DueDate, x.Amount, x.Principal, x.Interest); err != nil {
			return fmt.Errorf("installments: %w", err)
		}
	}
	_, err := tx.Exec(ctx, `DELETE FROM debt_installments WHERE debt_plan_id = $1 AND seq > $2`, p.ID, len(rows))
	return err
}

// ---- input ----

// CreditorInput — who a plan is owed to (or by): exactly one of a card / PayLater wallet, an
// institution, a contact or a typed name.
type CreditorInput struct {
	AccountID     *uuid.UUID `json:"account_id"`
	InstitutionID *uuid.UUID `json:"institution_id"`
	ContactID     *uuid.UUID `json:"contact_id"`
	PersonName    *string    `json:"person_name"`
}

// DebtPlanInput — POST /debt-plans.
type DebtPlanInput struct {
	Name        *string       `json:"name"`
	Description *string       `json:"description"`
	Note        *string       `json:"note"`
	CategoryID  *uuid.UUID    `json:"category_id"`
	Direction   *string       `json:"direction"` // received (I owe; default) | paid (they owe me)
	Origin      *string       `json:"origin"`    // purchase (default) | cash
	Creditor    CreditorInput `json:"creditor"`
	Plan        PlanInput     `json:"plan"`
}

// DebtPlanUpdate — PUT /debt-plans/{id}: its own fields; plan = the installments worked out again
// (ticks kept by seq). The creditor, direction and origin stay.
type DebtPlanUpdate struct {
	Name        *string    `json:"name"`
	Description *string    `json:"description"`
	Note        *string    `json:"note"`
	CategoryID  *uuid.UUID `json:"category_id"`
	Plan        *PlanInput `json:"plan"`
}

func cleanName(v *string) (string, bool) {
	if v == nil {
		return "", false
	}
	n := strings.TrimSpace(*v)
	return n, n != "" && len([]rune(n)) <= 100
}

// resolveCreditorTx — the creditor of in onto p, checked to be mine (a card / PayLater wallet I am a
// member of, my active institution, my contact) with its name.
func resolveCreditorTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, p *planRecord, in CreditorInput) error {
	n := 0
	for _, set := range []bool{in.AccountID != nil, in.InstitutionID != nil, in.ContactID != nil || in.PersonName != nil} {
		if set {
			n++
		}
	}
	if n != 1 {
		return planInvalid("creditor", apierr.FieldInvalid)
	}
	p.AccountID, p.InstitutionID, p.ContactID, p.PersonName = nil, nil, nil, nil
	switch {
	case in.AccountID != nil:
		var typ, name string
		err := tx.QueryRow(ctx, `SELECT a.type, a.name FROM accounts a WHERE a.id = $1 AND `+shared.ActiveMembershipPredicate("a.id", "$2"),
			*in.AccountID, userID).Scan(&typ, &name)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAccountNotFound
		}
		if err != nil {
			return err
		}
		if typ != "credit_card" && typ != "pay_later" {
			return planInvalid("creditor.account_id", apierr.FieldInvalid)
		}
		p.AccountID, p.creditorName = in.AccountID, name
	case in.InstitutionID != nil:
		if err := institutions.CheckOwned(ctx, tx, userID, *in.InstitutionID, true); err != nil {
			return err
		}
		p.InstitutionID = in.InstitutionID
		return tx.QueryRow(ctx, `SELECT name FROM institutions WHERE id = $1`, *in.InstitutionID).Scan(&p.creditorName)
	case in.ContactID != nil:
		r := &Rule{ContactID: in.ContactID}
		if err := checkDebtPersonTx(ctx, tx, userID, r); err != nil {
			return err
		}
		p.ContactID, p.PersonName, p.creditorName = in.ContactID, r.PersonName, *r.PersonName
	default:
		name, ok := cleanName(in.PersonName)
		if !ok {
			return planInvalid("creditor.person_name", apierr.FieldInvalid)
		}
		p.PersonName, p.creditorName = &name, name
	}
	return nil
}

// ---- create ----

// createPlanTx — a new plan and its installments, and (not on a card) its opening:
//   - cash: the debt row of the money lent / borrowed (with its wallet transaction when
//     plan.opening_account_id is sent);
//   - purchase (I owe only): the full price as my expense on the day (no wallet: the lender paid)
//     and the debt row, ledger-only.
//
// purchaseTx — a card plan's purchase on the card (quick create ผ่อน). Answers the wallets it moved.
func (s *Service) createPlanTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, in *DebtPlanInput, purchaseTx *uuid.UUID) (*planRecord, []uuid.UUID, error) {
	name, ok := cleanName(in.Name)
	if !ok {
		return nil, nil, planInvalid("name", apierr.FieldInvalid)
	}
	if in.Description != nil && len([]rune(*in.Description)) > 200 || in.Note != nil && len([]rune(*in.Note)) > 500 {
		return nil, nil, ErrValidation
	}
	p := &planRecord{UserID: userID, Name: name, Description: in.Description, Note: in.Note, CategoryID: in.CategoryID,
		Direction: "received", Origin: "purchase", PurchaseTransactionID: purchaseTx}
	if in.Direction != nil {
		if *in.Direction != "paid" && *in.Direction != "received" {
			return nil, nil, planInvalid("direction", apierr.FieldInvalid)
		}
		p.Direction = *in.Direction
	}
	if in.Origin != nil {
		if *in.Origin != "purchase" && *in.Origin != "cash" {
			return nil, nil, planInvalid("origin", apierr.FieldInvalid)
		}
		p.Origin = *in.Origin
	}
	if p.Origin == "purchase" && p.Direction == "paid" {
		return nil, nil, planInvalid("origin", apierr.FieldInvalid) // a purchase on credit is mine to owe
	}
	if err := resolveCreditorTx(ctx, tx, userID, p, in.Creditor); err != nil {
		return nil, nil, err
	}
	if p.isCard() && (p.Direction != "received" || p.Origin != "purchase") {
		return nil, nil, planInvalid("origin", apierr.FieldInvalid)
	}
	if p.CategoryID != nil {
		if err := checkRefsTx(ctx, tx, userID, "expense", nil, nil, p.CategoryID, nil); err != nil {
			return nil, nil, err
		}
	}
	rows, start, err := computeInstallments(&in.Plan)
	if err != nil {
		return nil, nil, err
	}
	p.Principal, p.Count, p.Start, p.InterestMethod = round2(in.Plan.Principal), in.Plan.Count, start, in.Plan.InterestMethod
	p.InterestRate, p.InstallmentAmount = in.Plan.InterestRate, in.Plan.InstallmentAmount
	if err := savePlanTx(ctx, tx, p); err != nil {
		return nil, nil, err
	}
	if err := writeInstallmentsTx(ctx, tx, p, rows); err != nil {
		return nil, nil, err
	}
	if p.isCard() || s.debts == nil {
		return p, nil, nil
	}
	// the opening
	today, err := shared.UserToday(ctx, tx, userID, shared.HeaderTZ(ctx))
	if err != nil {
		return nil, nil, err
	}
	date := today.Format(dateLayout)
	if in.Plan.OpeningDate != nil {
		if _, err := parseDate(*in.Plan.OpeningDate); err != nil {
			return nil, nil, planInvalid("opening_date", apierr.FieldInvalid)
		}
		date = *in.Plan.OpeningDate
	}
	entry := personal_debts.EntryInput{Kind: personal_debts.KindDebt, Direction: p.Direction, ContactID: p.ContactID,
		PersonName: p.creditorName, InstitutionID: p.InstitutionID, Amount: p.Principal, Date: date,
		Description: &p.Name, DebtPlanID: &p.ID}
	var touched []uuid.UUID
	switch p.Origin {
	case "cash":
		if in.Plan.OpeningAccountID != nil {
			entry.CreateTransaction, entry.AccountID = true, in.Plan.OpeningAccountID
			touched = append(touched, *in.Plan.OpeningAccountID)
		}
	case "purchase":
		if in.Plan.OpeningAccountID != nil {
			return nil, nil, planInvalid("opening_account_id", apierr.FieldInvalid) // the lender paid: no wallet of mine moves
		}
		res, err := s.txs.CreateInTx(ctx, tx, userID, transactions.CreateRequest{Type: transactions.TypeExpense,
			Amount: p.Principal, CategoryID: p.CategoryID, Date: date, Description: &p.Name})
		if err != nil {
			return nil, nil, err
		}
		if d, ok := res.(*transactions.TransactionDetail); ok {
			p.PurchaseTransactionID = &d.ID
			if err := savePlanTx(ctx, tx, p); err != nil {
				return nil, nil, err
			}
		}
	}
	if _, _, err := s.debts.CreateEntryTx(ctx, tx, userID, entry); err != nil {
		return nil, nil, err
	}
	return p, touched, nil
}

// CreateDebtPlan — POST /debt-plans.
func (s *Service) CreateDebtPlan(ctx context.Context, userID uuid.UUID, in DebtPlanInput, tz string) (*PlanView, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	p, _, err := s.createPlanTx(ctx, tx, userID, &in, nil)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetDebtPlan(ctx, userID, p.ID, tz)
}

// UpdateDebtPlan — PUT /debt-plans/{id}.
func (s *Service) UpdateDebtPlan(ctx context.Context, userID, id uuid.UUID, in DebtPlanUpdate, tz string) (*PlanView, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	p, err := getPlan(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		name, ok := cleanName(in.Name)
		if !ok {
			return nil, planInvalid("name", apierr.FieldInvalid)
		}
		p.Name = name
	}
	if in.Description != nil && len([]rune(*in.Description)) > 200 || in.Note != nil && len([]rune(*in.Note)) > 500 {
		return nil, ErrValidation
	}
	p.Description, p.Note, p.CategoryID = in.Description, in.Note, in.CategoryID
	if p.CategoryID != nil {
		if err := checkRefsTx(ctx, tx, userID, "expense", nil, nil, p.CategoryID, nil); err != nil {
			return nil, err
		}
	}
	if in.Plan != nil {
		rows, start, err := computeInstallments(in.Plan)
		if err != nil {
			return nil, err
		}
		p.Principal, p.Count, p.Start, p.InterestMethod = round2(in.Plan.Principal), in.Plan.Count, start, in.Plan.InterestMethod
		p.InterestRate, p.InstallmentAmount = in.Plan.InterestRate, in.Plan.InstallmentAmount
		if err := writeInstallmentsTx(ctx, tx, p, rows); err != nil {
			return nil, err
		}
	}
	if err := savePlanTx(ctx, tx, p); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetDebtPlan(ctx, userID, id, tz)
}

// DeleteDebtPlan — the plan, its installments and its recurring item go; its debt rows stay as
// general debts of the creditor.
func (s *Service) DeleteDebtPlan(ctx context.Context, userID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM debt_plans WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrPlanNotFound
	}
	return nil
}

// ---- read ----

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

func loadInstallments(ctx context.Context, q shared.Querier, userID, planID uuid.UUID, today time.Time) ([]Installment, error) {
	rows, err := q.Query(ctx, `SELECT seq, due_date, amount::float8, principal::float8, interest::float8, paid_at, skipped,
			personal_debt_id, edited
		FROM debt_installments WHERE debt_plan_id = $1 AND user_id = $2 ORDER BY seq`, planID, userID)
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

// creditorOf — the plan's creditor, page-ready.
func creditorOf(ctx context.Context, q shared.Querier, userID uuid.UUID, p *planRecord) (Counterparty, error) {
	c := Counterparty{}
	switch {
	case p.AccountID != nil:
		c.Kind = "card"
		refs, err := shared.LoadAccountRefs(ctx, q, []uuid.UUID{*p.AccountID})
		if err != nil {
			return c, err
		}
		if a, ok := refs[*p.AccountID]; ok {
			c.Account, c.Name = &a, a.Name
		}
	case p.InstitutionID != nil:
		c.Kind, c.Key = "institution", shared.InstitutionKey(*p.InstitutionID)
		refs, err := shared.LoadInstitutionRefs(ctx, q, []uuid.UUID{*p.InstitutionID})
		if err != nil {
			return c, err
		}
		if x, ok := refs[*p.InstitutionID]; ok {
			c.Institution, c.Name = &x, x.Name
		}
	default:
		name := derefStr(p.PersonName)
		c.Kind, c.Key = "person", shared.DebtKey(p.ContactID, name)
		pr := shared.NamePerson(name)
		if p.ContactID != nil {
			persons, err := shared.LoadPersons(ctx, q, userID, []uuid.UUID{*p.ContactID})
			if err != nil {
				return c, err
			}
			pr = shared.ResolvePerson(persons, shared.PersonKey{ContactID: p.ContactID, Snapshot: name})
		}
		c.Person, c.Name = &pr, pr.Name
	}
	return c, nil
}

// planView — a plan, page-ready: its numbers over the installments, its creditor, its purchase,
// the recurring item that pays it, and every installment.
func (s *Service) planView(ctx context.Context, q shared.Querier, userID uuid.UUID, p *planRecord, today time.Time) (*PlanView, error) {
	rows, err := loadInstallments(ctx, q, userID, p.ID, today)
	if err != nil {
		return nil, err
	}
	v := &PlanView{ID: p.ID, Name: p.Name, Description: p.Description, Note: p.Note, CategoryID: p.CategoryID,
		Direction: p.Direction, Origin: p.Origin, Principal: p.Principal, Count: p.Count, StartDate: p.Start.Format(dateLayout),
		InterestMethod: p.InterestMethod, InterestRate: p.InterestRate, InstallmentAmount: p.InstallmentAmount,
		Closed: p.ClosedAt != nil, Due: []Installment{}, Chain: []InstallmentStatus{}, Installments: rows, CreatedAt: p.CreatedAt}
	if v.Creditor, err = creditorOf(ctx, q, userID, p); err != nil {
		return nil, err
	}
	if p.CategoryID != nil {
		refs, err := shared.LoadCategoryRefs(ctx, q, []uuid.UUID{*p.CategoryID})
		if err != nil {
			return nil, err
		}
		if c, ok := refs[*p.CategoryID]; ok {
			v.Category = &c
		}
	}
	if p.PurchaseTransactionID != nil {
		if v.Purchase, err = loadPurchase(ctx, q, userID, *p.PurchaseTransactionID); err != nil {
			return nil, err
		}
	}
	items, err := q.Query(ctx, `SELECT id FROM recurrings WHERE debt_plan_id = $1 AND user_id = $2 ORDER BY created_at LIMIT 1`,
		p.ID, userID)
	if err != nil {
		return nil, err
	}
	for items.Next() {
		var id uuid.UUID
		if err := items.Scan(&id); err != nil {
			items.Close()
			return nil, err
		}
		v.RecurringID = &id
	}
	items.Close()
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
	if len(v.Due) > 0 {
		n := v.Due[0]
		v.Next = &n
	}
	v.PaidPrincipal, v.LeftPrincipal, v.LeftInterest = round2(v.PaidPrincipal), round2(v.LeftPrincipal), round2(v.LeftInterest)
	v.LeftTotal, v.PaidTotal = round2(v.LeftPrincipal+v.LeftInterest), round2(v.PaidTotal)
	return v, nil
}

// GetDebtPlan — GET /debt-plans/{id}.
func (s *Service) GetDebtPlan(ctx context.Context, userID, id uuid.UUID, tz string) (*PlanView, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	p, err := getPlan(ctx, s.db, userID, id, false)
	if err != nil {
		return nil, err
	}
	return s.planView(ctx, s.db, userID, p, today)
}

type PlanList struct {
	Data []PlanView `json:"data"`
}

// ListDebtPlans — GET /debt-plans: my plans, open first then newest; ?account_id= only the plans on
// that card.
func (s *Service) ListDebtPlans(ctx context.Context, userID uuid.UUID, accountID *uuid.UUID, tz string) (*PlanList, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+planColumns+` FROM debt_plans WHERE user_id = $1 AND ($2::uuid IS NULL OR account_id = $2)
		ORDER BY closed_at IS NOT NULL, created_at DESC, id`, userID, accountID)
	if err != nil {
		return nil, fmt.Errorf("debt plans: %w", err)
	}
	var plans []*planRecord
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		plans = append(plans, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := &PlanList{Data: []PlanView{}}
	for _, p := range plans {
		v, err := s.planView(ctx, s.db, userID, p, today)
		if err != nil {
			return nil, err
		}
		out.Data = append(out.Data, *v)
	}
	return out, nil
}

func (s *Service) Installments(ctx context.Context, userID, id uuid.UUID, tz string) (*InstallmentList, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	if _, err := getPlan(ctx, s.db, userID, id, false); err != nil {
		return nil, err
	}
	rows, err := loadInstallments(ctx, s.db, userID, id, today)
	if err != nil {
		return nil, err
	}
	return &InstallmentList{Data: rows}, nil
}

// ---- installments ----

type InstallmentEdit struct {
	DueDate   *string  `json:"due_date"`
	Amount    *float64 `json:"amount"`
	Principal *float64 `json:"principal"`
	Interest  *float64 `json:"interest"`
}

// EditInstallment — "แก้เฉพาะงวดนี้": its date and / or its money (amount alone = all principal
// unless interest is sent; principal + interest = the amount).
func (s *Service) EditInstallment(ctx context.Context, userID, id uuid.UUID, seq int, in InstallmentEdit, tz string) (*PlanView, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := getPlan(ctx, tx, userID, id, true); err != nil {
		return nil, err
	}
	var (
		due              time.Time
		amt, prin, inter float64
	)
	err = tx.QueryRow(ctx, `SELECT due_date, amount::float8, principal::float8, interest::float8 FROM debt_installments
		WHERE debt_plan_id = $1 AND seq = $2 FOR UPDATE`, id, seq).Scan(&due, &amt, &prin, &inter)
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
		WHERE debt_plan_id = $1 AND seq = $2`, id, seq, due, prin, inter); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetDebtPlan(ctx, userID, id, tz)
}

type MarkInstallmentRequest struct {
	Paid           bool       `json:"paid"`
	PersonalDebtID *uuid.UUID `json:"personal_debt_id"`
}

// MarkInstallment — the "จ่ายงวดแล้ว" tick (no money moves); paid false clears it.
func (s *Service) MarkInstallment(ctx context.Context, userID, id uuid.UUID, seq int, req MarkInstallmentRequest, tz string) (*PlanView, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := getPlan(ctx, tx, userID, id, true); err != nil {
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
	return s.GetDebtPlan(ctx, userID, id, tz)
}

func markInstallmentsTx(ctx context.Context, tx pgx.Tx, userID, planID uuid.UUID, seqs []int, paid bool, debtID *uuid.UUID) error {
	for _, seq := range seqs {
		var tag interface{ RowsAffected() int64 }
		var err error
		if paid {
			tag, err = tx.Exec(ctx, `UPDATE debt_installments SET paid_at = COALESCE(paid_at, NOW()), skipped = FALSE,
				personal_debt_id = COALESCE($4, personal_debt_id) WHERE debt_plan_id = $1 AND user_id = $2 AND seq = $3`,
				planID, userID, seq, debtID)
		} else {
			tag, err = tx.Exec(ctx, `UPDATE debt_installments SET paid_at = NULL, personal_debt_id = NULL
				WHERE debt_plan_id = $1 AND user_id = $2 AND seq = $3`, planID, userID, seq)
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

// ClosePlan — "ปิดแผนก่อนกำหนด": the unticked installments are skipped; the ticked ones stay as
// history.
func (s *Service) ClosePlan(ctx context.Context, userID, id uuid.UUID, tz string) (*PlanView, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	p, err := getPlan(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	if p.ClosedAt != nil {
		return nil, ErrInvalidTransition
	}
	if _, err := tx.Exec(ctx, `UPDATE debt_installments SET skipped = TRUE WHERE debt_plan_id = $1 AND paid_at IS NULL`, id); err != nil {
		return nil, err
	}
	now := time.Now()
	p.ClosedAt = &now
	if err := savePlanTx(ctx, tx, p); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE recurring_reminders SET status = 'archived', paused_from = NULL, pause_reason = NULL
		WHERE recurring_id IN (SELECT id FROM recurrings WHERE debt_plan_id = $1) AND status <> 'archived'`, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE recurrings SET status = 'archived' WHERE debt_plan_id = $1`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetDebtPlan(ctx, userID, id, tz)
}

// ---- paying ----

type PayInstallmentRequest struct {
	Seq       *int       `json:"seq"`        // absent = the oldest open one
	AccountID *uuid.UUID `json:"account_id"` // the wallet it is paid from (absent = ledger only, no interest row)
	Date      *string    `json:"date"`       // default today
}

// PayInstallment — POST /debt-plans/{id}/pay: one installment paid now (a card plan is paid by its
// card's bill instead).
func (s *Service) PayInstallment(ctx context.Context, userID, id uuid.UUID, req PayInstallmentRequest, tz string) (*PlanView, error) {
	today, err := s.today(ctx, userID, tz)
	if err != nil {
		return nil, err
	}
	when := today
	if req.Date != nil {
		if when, err = parseDate(*req.Date); err != nil {
			return nil, ErrValidation
		}
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	p, err := getPlan(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	seq := 0
	if req.Seq != nil {
		seq = *req.Seq
	}
	if _, err := s.payPlanRoundTx(ctx, tx, p, seq, req.AccountID, when, nil, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetDebtPlan(ctx, userID, id, tz)
}

// planPayment — what paying one installment booked.
type planPayment struct {
	Seq      int
	EntryID  *uuid.UUID // the repayment row (nil when the installment had no principal)
	Amount   float64    // principal + interest
	Interest float64
	Touched  []uuid.UUID
}

// payPlanRoundTx — installment seq (0 = the oldest open one) of p paid: a repayment row of the
// principal (with its wallet transaction when accountID is set) and, with a wallet, the interest as
// a ดอกเบี้ย transaction (an expense when I owe, an income when they owe me); then the tick.
// recurringID — the item whose reminder paid it.
func (s *Service) payPlanRoundTx(ctx context.Context, tx pgx.Tx, p *planRecord, seq int, accountID *uuid.UUID, when time.Time,
	desc *string, recurringID *uuid.UUID) (*planPayment, error) {
	if p.isCard() || p.ClosedAt != nil || s.debts == nil {
		return nil, ErrInvalidTransition
	}
	q := `SELECT seq, principal::float8, interest::float8 FROM debt_installments
		WHERE debt_plan_id = $1 AND paid_at IS NULL AND NOT skipped AND ($2 = 0 OR seq = $2) ORDER BY seq LIMIT 1 FOR UPDATE`
	var pay planPayment
	err := tx.QueryRow(ctx, q, p.ID, seq).Scan(&pay.Seq, &pay.Amount, &pay.Interest)
	if errors.Is(err, pgx.ErrNoRows) {
		if seq != 0 {
			return nil, ErrInstallmentNotFound
		}
		return nil, ErrNothingOwed
	}
	if err != nil {
		return nil, err
	}
	principal := pay.Amount
	pay.Amount = round2(principal + pay.Interest)
	if desc == nil {
		desc = ptr(fmt.Sprintf("%s %d/%d", p.Name, pay.Seq, p.Count))
	}
	name := derefStr(p.PersonName)
	if p.InstitutionID != nil {
		if err := tx.QueryRow(ctx, `SELECT name FROM institutions WHERE id = $1`, *p.InstitutionID).Scan(&name); err != nil {
			return nil, err
		}
	}
	back := "paid" // a repayment books the way back: I pay them back = paid
	if p.Direction == "paid" {
		back = "received"
	}
	if principal > 0 {
		in := personal_debts.EntryInput{Kind: personal_debts.KindRepayment, Direction: back, ContactID: p.ContactID,
			PersonName: name, InstitutionID: p.InstitutionID, Amount: principal, Date: when.Format(dateLayout),
			Description: truncRunes(desc, 200), RecurringID: recurringID, DebtPlanID: &p.ID}
		if accountID != nil {
			in.CreateTransaction, in.AccountID = true, accountID
		}
		e, _, err := s.debts.CreateEntryTx(ctx, tx, p.UserID, in)
		if err != nil {
			return nil, err
		}
		pay.EntryID = &e.ID
	}
	if accountID != nil {
		pay.Touched = append(pay.Touched, *accountID)
		if pay.Interest > 0 {
			kind := categories.SystemInterestPaid
			if p.Direction == "paid" {
				kind = categories.SystemInterestReceived
			}
			if _, _, err := s.txs.CreateSystemInTx(ctx, tx, p.UserID, *accountID, kind, pay.Interest, when.Format(dateLayout),
				truncRunes(desc, 200), nil); err != nil {
				return nil, err
			}
		}
	}
	if err := markInstallmentsTx(ctx, tx, p.UserID, p.ID, []int{pay.Seq}, true, pay.EntryID); err != nil {
		return nil, err
	}
	return &pay, nil
}

// ---- engine hooks ----

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

// nextInstallmentAmount — the amount of the plan's oldest open installment (0 = none left).
func nextInstallmentAmount(ctx context.Context, q rowQuerier, planID uuid.UUID) (float64, error) {
	var v float64
	err := q.QueryRow(ctx, `SELECT amount::float8 FROM debt_installments WHERE debt_plan_id = $1 AND paid_at IS NULL
		AND NOT skipped ORDER BY seq LIMIT 1`, planID).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return v, err
}

// loadPurchase — a plan's purchase transaction (nil when it was deleted).
func loadPurchase(ctx context.Context, q shared.Querier, userID, txID uuid.UUID) (*PlanPurchase, error) {
	rows, err := q.Query(ctx, `SELECT t.id, t.amount::float8, t.date, t.description, t.category_id
		FROM transactions t WHERE t.id = $1 AND t.user_id = $2`, txID, userID)
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
