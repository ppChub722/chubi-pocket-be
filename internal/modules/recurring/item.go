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

// 0.4.3: a recurring ITEM (recurrings) as the user sees it: one name, type, category, counterparty
// and plan, with any number of LINES (recurring_reminders: each its own source, amount and schedule).
// The round engine still runs per line; the item's own fields are copied onto its lines on every
// item write (syncLinesTx), so a line reads like a whole setting.

var (
	ErrLineNotFound  = errors.New("no such reminder on this recurring item")
	ErrLastLine      = errors.New("a recurring item keeps at least one reminder")
	ErrSourceInvalid = errors.New("a line pays from exactly one source: a wallet, or a person paying for me")
	ErrTypeFixed     = errors.New("the type of a recurring item is fixed once created: delete it and create a new one")
)

// Item types (the 5 the user picks). Fixed once the item is created: the type decides what its
// reminders can do.
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

// lineType — the money type of a reminder of an item (a card bill: a transfer into the card).
func lineType(it *itemRecord) string {
	if it.isCardBill() {
		return TypeTransfer
	}
	return it.Type
}

// isCardBill — a debt item paying a card / PayLater wallet's bill.
func (it *itemRecord) isCardBill() bool {
	return it.Type == TypeDebt && it.AccountID != nil && it.DebtPlanID == nil
}

// lineKind — the round engine's kind of a reminder of an item (an expense asking its amount
// each round, or a transfer = the engine's reminder kind: may start without an amount).
func lineKind(it *itemRecord, endKind, amountMode string) string {
	switch lineType(it) {
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

// paidByAllowed — the item types whose reminders may be paid by a person (my expense, their money).
func paidByAllowed(itemType string) bool { return itemType == TypeExpense }

// Reminder actions (what a round does), derived from the item type and the reminder.
const (
	ActionExpense     = "expense"
	ActionIncome      = "income"
	ActionTransfer    = "transfer"
	ActionSaving      = "saving"
	ActionPaidBy      = "paid_by"       // someone paid for me: my expense + my debt to them
	ActionRepayPaidBy = "repay_paid_by" // I pay back the one who paid another reminder of the item
	ActionRepay       = "repay"         // I pay a debt (a card bill: a transfer into the card)
	ActionCollect     = "collect"       // they pay me back
	ActionNotify      = "notify"        // แค่แจ้งเตือน: nothing is booked
)

// action — what a round of r does.
func (r *Rule) action() string {
	switch {
	case r.NotifyOnly:
		return ActionNotify
	case r.PaidByPersonName != nil:
		return ActionPaidBy
	case r.RepaysReminderID != nil:
		return ActionRepayPaidBy
	case r.Type == typeDebt:
		if r.DebtDirection != nil && *r.DebtDirection == "received" {
			return ActionCollect
		}
		return ActionRepay
	}
	return r.Type
}

type itemRecord struct {
	ID          uuid.UUID
	UserID      uuid.UUID
	Name        string
	Description *string
	Note        *string
	IconCode    *shared.IconCode
	Type        string
	CategoryID  *uuid.UUID
	TagIDs      []uuid.UUID
	GoalID      *uuid.UUID
	Status      string
	// type debt — the debt it pays: a plan (DebtPlanID; the counterparty fields below are then the
	// plan's, in memory only), a card / PayLater wallet's bill (AccountID), or a counterparty: a
	// contact, else PersonName, else InstitutionID.
	ContactID       *uuid.UUID
	PersonName      *string
	InstitutionID   *uuid.UUID
	AccountID       *uuid.UUID
	DebtDirection   *string // paid (they owe me) | received (I owe them)
	DebtPlanID      *uuid.UUID
	Plan            *planRecord
	CreatedAt       time.Time
	UpdatedAt       time.Time
	counterpartName string // resolved on save (contact / institution name)
}

const itemRecordColumns = `id, user_id, name, description, note, icon_code, type, category_id, tag_ids, goal_id, status,
	counterparty_contact_id, counterparty_person_name, counterparty_institution_id, debt_direction,
	debt_plan_id, counterparty_account_id, created_at, updated_at`

func scanItemRecord(row pgx.Row) (*itemRecord, error) {
	var (
		it   itemRecord
		icon []byte
	)
	if err := row.Scan(&it.ID, &it.UserID, &it.Name, &it.Description, &it.Note, &icon, &it.Type, &it.CategoryID, &it.TagIDs,
		&it.GoalID, &it.Status, &it.ContactID, &it.PersonName, &it.InstitutionID, &it.DebtDirection,
		&it.DebtPlanID, &it.AccountID, &it.CreatedAt, &it.UpdatedAt); err != nil {
		return nil, err
	}
	it.IconCode = shared.IconFromJSON(icon)
	if it.TagIDs == nil {
		it.TagIDs = []uuid.UUID{}
	}
	return &it, nil
}

// attachPlans — the plans of the items that pay one: it.Plan, and the plan's creditor and direction
// as the item's counterparty (in memory).
func attachPlans(ctx context.Context, q shared.Querier, userID uuid.UUID, items ...*itemRecord) error {
	var ids []uuid.UUID
	for _, it := range items {
		if it.DebtPlanID != nil {
			ids = append(ids, *it.DebtPlanID)
		}
	}
	plans, err := plansByID(ctx, q, userID, ids)
	if err != nil {
		return err
	}
	for _, it := range items {
		if it.DebtPlanID == nil {
			continue
		}
		if p, ok := plans[*it.DebtPlanID]; ok {
			it.setPlan(p)
		}
	}
	return nil
}

// setPlan — the plan an item pays, its creditor as the item's counterparty.
func (it *itemRecord) setPlan(p *planRecord) {
	it.Plan, it.DebtPlanID = p, &p.ID
	it.ContactID, it.PersonName, it.InstitutionID, it.AccountID = p.ContactID, p.PersonName, p.InstitutionID, nil
	it.DebtDirection = ptr(p.Direction)
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
		return nil, fmt.Errorf("recurring item: %w", err)
	}
	if it.DebtPlanID != nil {
		p, err := getPlan(ctx, q, userID, *it.DebtPlanID, false)
		if err != nil {
			return nil, err
		}
		it.setPlan(p)
	}
	return it, nil
}

func saveItemRecordTx(ctx context.Context, tx pgx.Tx, it *itemRecord) error {
	icon, err := iconJSON(it.IconCode)
	if err != nil {
		return err
	}
	tags := it.TagIDs
	if tags == nil {
		tags = []uuid.UUID{}
	}
	contact, personName, inst, acc := it.ContactID, it.PersonName, it.InstitutionID, it.AccountID
	switch {
	case it.DebtPlanID != nil: // the plan holds the creditor
		contact, personName, inst, acc = nil, nil, nil, nil
	case inst != nil || acc != nil:
		contact, personName = nil, nil
	}
	args := []any{it.ID, it.UserID, it.Name, shared.CleanText(it.Description), shared.CleanText(it.Note), icon, it.Type,
		it.CategoryID, tags, it.GoalID, it.Status, contact, personName, inst, it.DebtDirection, it.DebtPlanID, acc}
	if it.ID == uuid.Nil {
		it.ID, _ = uuid.NewV7()
		args[0] = it.ID
		_, err = tx.Exec(ctx, `INSERT INTO recurrings (id, user_id, name, description, note, icon_code, type, category_id, tag_ids,
			goal_id, status, counterparty_contact_id, counterparty_person_name, counterparty_institution_id, debt_direction,
			debt_plan_id, counterparty_account_id)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`, args...)
	} else {
		_, err = tx.Exec(ctx, `UPDATE recurrings SET name = $3, description = $4, note = $5, icon_code = $6::jsonb, type = $7,
			category_id = $8, tag_ids = $9, goal_id = $10, status = $11, counterparty_contact_id = $12,
			counterparty_person_name = $13, counterparty_institution_id = $14, debt_direction = $15,
			debt_plan_id = $16, counterparty_account_id = $17
			WHERE id = $1 AND user_id = $2`, args...)
	}
	if err != nil {
		return fmt.Errorf("save recurring item: %w", err)
	}
	return nil
}

// linesOf — the item's lines (oldest first).
func linesOf(ctx context.Context, q shared.Querier, userID uuid.UUID, itemIDs []uuid.UUID, lock bool) ([]*Rule, error) {
	if len(itemIDs) == 0 {
		return nil, nil
	}
	sql := `SELECT ` + ruleColumns + ` FROM recurring_reminders WHERE user_id = $1 AND recurring_id = ANY($2) ORDER BY created_at, id`
	if lock {
		sql += ` FOR UPDATE`
	}
	rows, err := q.Query(ctx, sql, userID, itemIDs)
	if err != nil {
		return nil, fmt.Errorf("recurring lines: %w", err)
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

// itemToLine — the item's own fields onto one of its lines (the engine reads the line only).
func itemToLine(it *itemRecord, r *Rule) {
	r.RecurringID, r.UserID = it.ID, it.UserID
	r.Name, r.Description, r.Note, r.IconCode = it.Name, it.Description, it.Note, it.IconCode
	r.Type, r.Kind = lineType(it), lineKind(it, r.EndKind, r.AmountMode)
	r.TagIDs = append([]uuid.UUID{}, it.TagIDs...)
	r.CategoryID, r.GoalID, r.TrialUntil = nil, nil, nil
	switch lineType(it) {
	case TypeSaving:
		r.GoalID, r.TagIDs = it.GoalID, []uuid.UUID{}
	case TypeTransfer:
	default:
		r.CategoryID = it.CategoryID
	}
	switch {
	case it.isCardBill():
		r.ToAccountID = it.AccountID
	case it.Type != TypeTransfer:
		r.ToAccountID = nil
	}
	if r.RepaysReminderID != nil { // a payback: a debt repayment to the person (set from the reminder it repays)
		r.Type, r.Kind, r.CategoryID, r.TagIDs = typeDebt, KindDebt, nil, []uuid.UUID{}
		r.DebtKind, r.DebtDirection, r.InstitutionID = ptr(DebtKindRepayment), ptr("paid"), nil
		return
	}
	r.ContactID, r.PersonName, r.DebtDirection, r.InstitutionID = nil, nil, nil, nil
	if it.Type == TypeDebt && !it.isCardBill() {
		r.ContactID, r.InstitutionID = it.ContactID, it.InstitutionID
		r.DebtDirection = lineDirection(it.DebtDirection, r.DebtKind)
		name := it.counterpartName
		if name == "" && it.PersonName != nil {
			name = *it.PersonName
		}
		r.PersonName = &name
		r.DebtKind = ptr(DebtKindRepayment) // a debt item pays its debt back (repay / collect)
		r.DebtDirection = lineDirection(it.DebtDirection, r.DebtKind)
	} else {
		r.DebtKind, r.ZeroRounds = nil, 0
	}
}

// refreshItemStatusTx — an item is active while one line is; paused while one is paused; archived
// when every line is. An item archived by hand stays archived. A card plan has no line: its status
// stays as set.
func refreshItemStatusTx(ctx context.Context, tx pgx.Tx, userID, itemID uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE recurrings g SET status = CASE
			WHEN g.status = 'archived' AND NOT EXISTS (SELECT 1 FROM recurring_reminders r WHERE r.recurring_id = g.id AND r.status <> 'archived') THEN 'archived'
			WHEN EXISTS (SELECT 1 FROM recurring_reminders r WHERE r.recurring_id = g.id AND r.status = 'active') THEN 'active'
			WHEN EXISTS (SELECT 1 FROM recurring_reminders r WHERE r.recurring_id = g.id AND r.status = 'paused') THEN 'paused'
			WHEN EXISTS (SELECT 1 FROM recurring_reminders r WHERE r.recurring_id = g.id) THEN 'archived'
			ELSE g.status END
		WHERE g.id = $1 AND g.user_id = $2`, itemID, userID)
	return err
}

// itemOfLine — the item id a line belongs to.
func itemOfLine(ctx context.Context, q rowQuerier, userID, lineID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx, `SELECT recurring_id FROM recurring_reminders WHERE id = $1 AND user_id = $2`, lineID, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrLineNotFound
	}
	return id, err
}

// lineOfItem — the line, checked to belong to the item.
func lineOfItem(ctx context.Context, q rowQuerier, userID, itemID, lineID uuid.UUID) error {
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM recurring_reminders WHERE id = $1 AND recurring_id = $2 AND user_id = $3)`,
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

// effectiveStatus — the item's status from its lines (the engine pauses a line on its own: the
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

// lineDirection — a debt line's row direction from the item's (paid = they owe me): a debt line
// books the debt that way, a repayment line the way back (they pay me back = received).
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
