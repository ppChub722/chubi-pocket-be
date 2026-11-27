package recurring

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// A recurring (recurrings) is who it is: name, icon, text, a tracking type (a filter) and a status.
// Its rules (recurring_rules) each own what a round books: money type, category, tags, wallet or
// from -> to, goal, debt target, amount and schedule. Pages pull rules by those columns; nothing
// records where a rule was made (spec §1, §3).

var (
	ErrLineNotFound  = errors.New("no such rule on this recurring")
	ErrLastLine      = errors.New("a recurring keeps at least one rule")
	ErrSourceInvalid = errors.New("a rule pays from exactly one source: a wallet, or a person paying for me")
)

// Rule money types.
const (
	TypeExpense  = "expense"
	TypeIncome   = "income"
	TypeTransfer = "transfer"
	TypeSaving   = "saving"
	TypeDebt     = "debt"
)

func validItemType(t string) bool {
	switch t {
	case TypeExpense, TypeIncome, TypeTransfer, TypeSaving, TypeDebt:
		return true
	}
	return false
}

// Tracking types: a filter on the list, no effect on the rules.
var trackingTypes = map[string]bool{"bill": true, "subscription": true, "installment": true, "credit_card": true,
	"person_debt": true, "income": true, "saving": true, "other": true}

// lineKind — the round engine's kind of a rule (an expense asking its amount each round, or a
// transfer = the engine's reminder kind: may start without an amount).
func lineKind(t *ruleTarget, endKind, amountMode string) string {
	switch t.Type {
	case TypeExpense:
		if amountMode == AmountAsk {
			return KindReminder
		}
		if endKind == "count" {
			return KindInstallment
		}
		return KindSubscription
	case TypeTransfer:
		return KindReminder
	case TypeIncome:
		return KindIncome
	case TypeSaving:
		return KindSaving
	}
	return KindDebt
}

// paidByAllowed — the rule types that may be paid by a person (my expense, their money).
func paidByAllowed(ruleType string) bool { return ruleType == TypeExpense }

// Rule actions (what a round does), from the rule's type and fields.
const (
	ActionExpense     = "expense"
	ActionIncome      = "income"
	ActionTransfer    = "transfer"
	ActionSaving      = "saving"
	ActionPaidBy      = "paid_by"       // someone paid for me: my expense + my debt to them
	ActionRepayPaidBy = "repay_paid_by" // I pay back the one who paid another rule of the recurring
	ActionRepay       = "repay"         // I pay a debt back
	ActionCollect     = "collect"       // they pay me back
	ActionBorrow      = "borrow"        // I owe more (a debt row, money in when a wallet is set)
	ActionLend        = "lend"          // they owe me more
)

// action — what a round of r does.
func (r *Rule) action() string {
	switch {
	case r.PaidByPersonName != nil:
		return ActionPaidBy
	case r.RepaysRuleID != nil:
		return ActionRepayPaidBy
	case r.Type == typeDebt:
		received := r.DebtDirection != nil && *r.DebtDirection == "received"
		if r.DebtKind != nil && *r.DebtKind == DebtKindDebt {
			if received {
				return ActionBorrow
			}
			return ActionLend
		}
		if received {
			return ActionCollect
		}
		return ActionRepay
	}
	return r.Type
}

type itemRecord struct {
	ID           uuid.UUID
	UserID       uuid.UUID
	Name         string
	Description  *string
	Note         *string
	IconCode     *shared.IconCode
	TrackingType string
	Status       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

const itemRecordColumns = `id, user_id, name, description, note, icon_code, tracking_type, status, created_at, updated_at`

func scanItemRecord(row pgx.Row) (*itemRecord, error) {
	var (
		it   itemRecord
		icon []byte
	)
	if err := row.Scan(&it.ID, &it.UserID, &it.Name, &it.Description, &it.Note, &icon, &it.TrackingType, &it.Status,
		&it.CreatedAt, &it.UpdatedAt); err != nil {
		return nil, err
	}
	it.IconCode = shared.IconFromJSON(icon)
	return &it, nil
}

func getItemRecord(ctx context.Context, q rowQuerier, userID, id uuid.UUID, lock bool) (*itemRecord, error) {
	sql := `SELECT ` + itemRecordColumns + ` FROM recurrings WHERE id = $1 AND user_id = $2`
	if lock {
		sql += ` FOR UPDATE`
	}
	it, err := scanItemRecord(q.QueryRow(ctx, sql, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("recurring: %w", err)
	}
	return it, nil
}

func saveItemRecordTx(ctx context.Context, tx pgx.Tx, it *itemRecord) error {
	icon, err := iconJSON(it.IconCode)
	if err != nil {
		return err
	}
	if it.TrackingType == "" {
		it.TrackingType = "other"
	}
	args := []any{it.ID, it.UserID, it.Name, shared.CleanText(it.Description), shared.CleanText(it.Note), icon, it.TrackingType, it.Status}
	if it.ID == uuid.Nil {
		it.ID, _ = uuid.NewV7()
		args[0] = it.ID
		_, err = tx.Exec(ctx, `INSERT INTO recurrings (id, user_id, name, description, note, icon_code, tracking_type, status)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8)`, args...)
	} else {
		_, err = tx.Exec(ctx, `UPDATE recurrings SET name = $3, description = $4, note = $5, icon_code = $6::jsonb,
			tracking_type = $7, status = $8 WHERE id = $1 AND user_id = $2`, args...)
	}
	if err != nil {
		return fmt.Errorf("save recurring: %w", err)
	}
	return nil
}

// linesOf — the recurrings' rules (oldest first: "Rule n" is the order).
func linesOf(ctx context.Context, q shared.Querier, userID uuid.UUID, itemIDs []uuid.UUID, lock bool) ([]*Rule, error) {
	if len(itemIDs) == 0 {
		return nil, nil
	}
	sql := `SELECT ` + ruleColumns + ` FROM recurring_rules WHERE user_id = $1 AND recurring_id = ANY($2) ORDER BY created_at, id`
	if lock {
		sql += ` FOR UPDATE`
	}
	rows, err := q.Query(ctx, sql, userID, itemIDs)
	if err != nil {
		return nil, fmt.Errorf("recurring rules: %w", err)
	}
	defer rows.Close()
	var out []*Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ruleTarget — what a rule books besides its money: type, category, tags, goal and, for a debt
// rule, its counterparty (a plan: the plan's creditor; else exactly one of a contact / typed name /
// institution) and direction (paid = they owe me, received = I owe them). A card bill is a plain
// transfer into the card wallet (to_account_id).
type ruleTarget struct {
	Type            string
	CategoryID      *uuid.UUID
	TagIDs          []uuid.UUID
	GoalID          *uuid.UUID
	ContactID       *uuid.UUID
	PersonName      *string
	InstitutionID   *uuid.UUID
	DebtDirection   *string
	DebtPlanID      *uuid.UUID
	Plan            *planRecord
	counterpartName string // resolved on save (contact / institution name)
}

// applyTargetInput — the target fields of a rule input (a full replace). Checks what needs no DB.
func applyTargetInput(t *ruleTarget, in *LineInput) error {
	if in.Type == nil || !validItemType(*in.Type) {
		return ErrValidation
	}
	*t = ruleTarget{Type: *in.Type, TagIDs: uniqueIDs(append([]uuid.UUID{}, in.TagIDs...))}
	switch t.Type {
	case TypeSaving:
		t.GoalID, t.TagIDs = in.GoalID, []uuid.UUID{}
	case TypeTransfer:
	default:
		t.CategoryID = in.CategoryID
	}
	if t.Type != TypeDebt {
		if in.Counterparty != nil || in.DebtPlanID != nil {
			return ErrValidation
		}
		return nil
	}
	c := in.Counterparty
	if in.DebtPlanID != nil { // a plan: its creditor and direction (resolveTargetTx)
		if c != nil {
			return ErrValidation
		}
		t.DebtPlanID = in.DebtPlanID
		return nil
	}
	if c == nil || in.DebtDirection == nil || (*in.DebtDirection != "paid" && *in.DebtDirection != "received") {
		return ErrValidation
	}
	n := 0
	if c.ContactID != nil {
		n++
	}
	if c.InstitutionID != nil {
		n++
	}
	if c.PersonName != nil && strings.TrimSpace(*c.PersonName) != "" && c.ContactID == nil {
		n++
	}
	if n != 1 {
		return ErrValidation
	}
	t.ContactID, t.InstitutionID, t.DebtDirection = c.ContactID, c.InstitutionID, in.DebtDirection
	if c.ContactID == nil && c.InstitutionID == nil {
		pn := strings.TrimSpace(*c.PersonName)
		if len([]rune(pn)) > 100 {
			return ErrValidation
		}
		t.PersonName = &pn
	}
	return nil
}

// setPlan — the plan a debt rule pays; its creditor and direction become the rule's.
func (t *ruleTarget) setPlan(p *planRecord) {
	t.Plan, t.DebtPlanID = p, &p.ID
	t.ContactID, t.PersonName, t.InstitutionID = p.ContactID, p.PersonName, p.InstitutionID
	t.DebtDirection = ptr(p.Direction)
}

// targetOf — the target a stored rule carries (its debt direction back on the debt's side).
func targetOf(r *Rule) ruleTarget {
	t := ruleTarget{Type: r.Type, CategoryID: r.CategoryID, TagIDs: append([]uuid.UUID{}, r.TagIDs...), GoalID: r.GoalID,
		ContactID: r.ContactID, PersonName: r.PersonName, InstitutionID: r.InstitutionID, DebtPlanID: r.DebtPlanID}
	if r.DebtDirection != nil {
		t.DebtDirection = ptr(*r.DebtDirection)
		if r.DebtKind != nil && *r.DebtKind == DebtKindRepayment { // a repayment books the way back
			t.DebtDirection = lineDirection(r.DebtDirection, r.DebtKind)
		}
	}
	if t.PersonName != nil {
		t.counterpartName = *t.PersonName
	}
	return t
}

// itemToLine — the recurring's text (read only, rounds are named from it) and the target onto a rule.
func itemToLine(it *itemRecord, t *ruleTarget, r *Rule) {
	r.RecurringID, r.UserID = it.ID, it.UserID
	r.Name, r.Description, r.Note, r.IconCode = it.Name, it.Description, it.Note, it.IconCode
	r.Type, r.Kind = t.Type, lineKind(t, r.EndKind, r.AmountMode)
	r.TagIDs = append([]uuid.UUID{}, t.TagIDs...)
	r.CategoryID, r.GoalID, r.TrialUntil, r.DebtPlanID = nil, nil, nil, nil
	switch t.Type {
	case TypeSaving:
		r.GoalID, r.TagIDs = t.GoalID, []uuid.UUID{}
	case TypeTransfer:
	default:
		r.CategoryID = t.CategoryID
	}
	if t.Type != TypeTransfer {
		r.ToAccountID = nil
	}
	if r.RepaysRuleID != nil { // a payback: a debt repayment to the person (set from the rule it repays)
		r.Type, r.Kind, r.CategoryID, r.TagIDs = typeDebt, KindDebt, nil, []uuid.UUID{}
		r.DebtKind, r.DebtDirection, r.InstitutionID = ptr(DebtKindRepayment), ptr("paid"), nil
		return
	}
	r.ContactID, r.PersonName, r.DebtDirection, r.InstitutionID = nil, nil, nil, nil
	if t.Type == TypeDebt {
		r.ContactID, r.InstitutionID, r.DebtPlanID = t.ContactID, t.InstitutionID, t.DebtPlanID
		name := t.counterpartName
		if name == "" && t.PersonName != nil {
			name = *t.PersonName
		}
		r.PersonName = &name
		if r.DebtKind == nil || t.DebtPlanID != nil {
			r.DebtKind = ptr(DebtKindRepayment) // a plan's rounds pay it back
		}
		r.DebtDirection = lineDirection(t.DebtDirection, r.DebtKind)
	} else {
		r.DebtKind, r.ZeroRounds = nil, 0
	}
}

// refreshItemStatusTx — a recurring is active while one rule is; paused while one is paused;
// archived when every rule is. One archived by hand stays archived.
func refreshItemStatusTx(ctx context.Context, tx pgx.Tx, userID, itemID uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE recurrings g SET status = CASE
			WHEN g.status = 'archived' AND NOT EXISTS (SELECT 1 FROM recurring_rules r WHERE r.recurring_id = g.id AND r.status <> 'archived') THEN 'archived'
			WHEN EXISTS (SELECT 1 FROM recurring_rules r WHERE r.recurring_id = g.id AND r.status = 'active') THEN 'active'
			WHEN EXISTS (SELECT 1 FROM recurring_rules r WHERE r.recurring_id = g.id AND r.status = 'paused') THEN 'paused'
			WHEN EXISTS (SELECT 1 FROM recurring_rules r WHERE r.recurring_id = g.id) THEN 'archived'
			ELSE g.status END
		WHERE g.id = $1 AND g.user_id = $2`, itemID, userID)
	return err
}

// itemOfLine — the recurring a rule belongs to.
func itemOfLine(ctx context.Context, q rowQuerier, userID, lineID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx, `SELECT recurring_id FROM recurring_rules WHERE id = $1 AND user_id = $2`, lineID, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrLineNotFound
	}
	return id, err
}

// lineOfItem — the rule, checked to belong to the recurring.
func lineOfItem(ctx context.Context, q rowQuerier, userID, itemID, lineID uuid.UUID) error {
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM recurring_rules WHERE id = $1 AND recurring_id = $2 AND user_id = $3)`,
		lineID, itemID, userID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		var exists bool
		_ = q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM recurrings WHERE id = $1 AND user_id = $2)`, itemID, userID).Scan(&exists)
		if !exists {
			return ErrNotFound
		}
		return ErrLineNotFound
	}
	return nil
}

// effectiveStatus — the recurring's status from its rules (the engine pauses a rule on its own: the
// overdue limit, a closed goal, nothing owed), the rule refreshItemStatusTx stores.
func effectiveStatus(stored string, lines []*Rule) string {
	if len(lines) == 0 {
		return stored
	}
	active, paused, live := false, false, false
	for _, r := range lines {
		switch r.Status {
		case "active":
			active, live = true, true
		case "paused":
			paused, live = true, true
		}
	}
	switch {
	case stored == "archived" && !live:
		return "archived"
	case active:
		return "active"
	case paused:
		return "paused"
	}
	return "archived"
}

// lineDirection — a debt rule's row direction from the debt's (paid = they owe me): a debt rule
// books the debt that way, a repayment rule the way back (they pay me back = received).
func lineDirection(itemDir, debtKind *string) *string {
	if itemDir == nil {
		return nil
	}
	if debtKind != nil && *debtKind == DebtKindRepayment {
		if *itemDir == "paid" {
			return ptr("received")
		}
		return ptr("paid")
	}
	return ptr(*itemDir)
}
