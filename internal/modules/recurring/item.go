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
// and plan, with any number of LINES (recurring_rules: each its own source, amount and schedule).
// The round engine still runs per line; the item's own fields are copied onto its lines on every
// item write (syncLinesTx), so a line reads like a whole setting.

var (
	ErrLineNotFound  = errors.New("no such line on this recurring item")
	ErrLastLine      = errors.New("a recurring item keeps at least one line")
	ErrSourceInvalid = errors.New("a line pays from exactly one source: a wallet, or a person paying for me")
)

// Item types (the 7 the user picks).
const (
	TypeExpense      = "expense"
	TypeSubscription = "subscription"
	TypeBill         = "bill"
	TypeIncome       = "income"
	TypeTransfer     = "transfer"
	TypeSaving       = "saving"
	TypeDebt         = "debt"
)

func validItemType(t string) bool {
	switch t {
	case TypeExpense, TypeSubscription, TypeBill, TypeIncome, TypeTransfer, TypeSaving, TypeDebt:
		return true
	}
	return false
}

// lineType — the money type of a line of an item type.
func lineType(itemType string) string {
	switch itemType {
	case TypeExpense, TypeSubscription, TypeBill:
		return "expense"
	}
	return itemType
}

// lineKind — the round engine's kind of a line of an item type (bill / transfer = a reminder: may
// start without an amount).
func lineKind(itemType, endKind string) string {
	switch itemType {
	case TypeExpense:
		if endKind == "count" {
			return KindInstallment
		}
		return KindSubscription
	case TypeSubscription:
		return KindSubscription
	case TypeBill, TypeTransfer:
		return KindReminder
	case TypeIncome:
		return KindIncome
	case TypeSaving:
		return KindSaving
	}
	return KindDebt
}

// paidByAllowed — the item types whose lines may be paid by a person (my expense, their money).
func paidByAllowed(itemType string) bool {
	switch itemType {
	case TypeExpense, TypeSubscription, TypeBill, TypeDebt:
		return true
	}
	return false
}

type itemRecord struct {
	ID              uuid.UUID
	UserID          uuid.UUID
	Name            string
	Description     *string
	Note            *string
	IconCode        *shared.IconCode
	Type            string
	CategoryID      *uuid.UUID
	TagIDs          []uuid.UUID
	GoalID          *uuid.UUID
	Status          string
	ContactID       *uuid.UUID // counterparty (type debt): a contact, else PersonName, else InstitutionID
	PersonName      *string
	InstitutionID   *uuid.UUID
	DebtDirection   *string // paid (they owe me) | received (I owe them)
	Plan            *planRecord
	CreatedAt       time.Time
	UpdatedAt       time.Time
	counterpartName string // resolved on save (contact / institution name)
}

type planRecord struct {
	Principal         float64
	Count             int
	Start             time.Time
	InterestMethod    string
	InterestRate      *float64
	InstallmentAmount *float64
	CardAccountID     *uuid.UUID
	ClosedAt          *time.Time
}

const itemRecordColumns = `id, user_id, name, description, note, icon_code, type, category_id, tag_ids, goal_id, status,
	counterparty_contact_id, counterparty_person_name, counterparty_institution_id, debt_direction,
	plan_principal::float8, plan_count, plan_start, plan_interest_method, plan_interest_rate::float8,
	plan_installment_amount::float8, card_account_id, plan_closed_at, created_at, updated_at`

func scanItemRecord(row pgx.Row) (*itemRecord, error) {
	var (
		it    itemRecord
		icon  []byte
		prin  *float64
		count *int32
		start *time.Time
		meth  *string
		p     planRecord
	)
	if err := row.Scan(&it.ID, &it.UserID, &it.Name, &it.Description, &it.Note, &icon, &it.Type, &it.CategoryID, &it.TagIDs,
		&it.GoalID, &it.Status, &it.ContactID, &it.PersonName, &it.InstitutionID, &it.DebtDirection,
		&prin, &count, &start, &meth, &p.InterestRate, &p.InstallmentAmount, &p.CardAccountID, &p.ClosedAt,
		&it.CreatedAt, &it.UpdatedAt); err != nil {
		return nil, err
	}
	it.IconCode = shared.IconFromJSON(icon)
	if it.TagIDs == nil {
		it.TagIDs = []uuid.UUID{}
	}
	if prin != nil && count != nil && start != nil && meth != nil {
		p.Principal, p.Count, p.Start, p.InterestMethod = *prin, int(*count), *start, *meth
		it.Plan = &p
	}
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
		return nil, fmt.Errorf("recurring item: %w", err)
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
	var (
		prin, rate, inst *float64
		count            *int
		start            *time.Time
		meth             *string
		card             *uuid.UUID
		closed           *time.Time
	)
	if p := it.Plan; p != nil {
		prin, count, start, meth, rate, inst, card, closed = &p.Principal, &p.Count, &p.Start, &p.InterestMethod,
			p.InterestRate, p.InstallmentAmount, p.CardAccountID, p.ClosedAt
	}
	personName := it.PersonName
	if it.InstitutionID != nil {
		personName = nil
	}
	args := []any{it.ID, it.UserID, it.Name, shared.CleanText(it.Description), shared.CleanText(it.Note), icon, it.Type,
		it.CategoryID, tags, it.GoalID, it.Status, it.ContactID, personName, it.InstitutionID, it.DebtDirection,
		prin, count, start, meth, rate, inst, card, closed}
	if it.ID == uuid.Nil {
		it.ID, _ = uuid.NewV7()
		args[0] = it.ID
		_, err = tx.Exec(ctx, `INSERT INTO recurrings (id, user_id, name, description, note, icon_code, type, category_id, tag_ids,
			goal_id, status, counterparty_contact_id, counterparty_person_name, counterparty_institution_id, debt_direction,
			plan_principal, plan_count, plan_start, plan_interest_method, plan_interest_rate, plan_installment_amount,
			card_account_id, plan_closed_at)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23)`, args...)
	} else {
		_, err = tx.Exec(ctx, `UPDATE recurrings SET name = $3, description = $4, note = $5, icon_code = $6::jsonb, type = $7,
			category_id = $8, tag_ids = $9, goal_id = $10, status = $11, counterparty_contact_id = $12,
			counterparty_person_name = $13, counterparty_institution_id = $14, debt_direction = $15,
			plan_principal = $16, plan_count = $17, plan_start = $18, plan_interest_method = $19, plan_interest_rate = $20,
			plan_installment_amount = $21, card_account_id = $22, plan_closed_at = $23
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
	sql := `SELECT ` + ruleColumns + ` FROM recurring_rules WHERE user_id = $1 AND recurring_id = ANY($2) ORDER BY created_at, id`
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
	r.Type, r.Kind = lineType(it.Type), lineKind(it.Type, r.EndKind)
	r.TagIDs = append([]uuid.UUID{}, it.TagIDs...)
	r.CategoryID, r.GoalID, r.TrialUntil = nil, nil, nil
	switch it.Type {
	case TypeSaving:
		r.GoalID, r.TagIDs = it.GoalID, []uuid.UUID{}
	case TypeTransfer:
	default:
		r.CategoryID = it.CategoryID
	}
	if it.Type != TypeTransfer {
		r.ToAccountID = nil
	}
	r.ContactID, r.PersonName, r.DebtDirection, r.InstitutionID = nil, nil, nil, nil
	if it.Type == TypeDebt {
		r.ContactID, r.InstitutionID = it.ContactID, it.InstitutionID
		r.DebtDirection = lineDirection(it.DebtDirection, r.DebtKind)
		name := it.counterpartName
		if name == "" && it.PersonName != nil {
			name = *it.PersonName
		}
		r.PersonName = &name
		if r.DebtKind == nil {
			r.DebtKind = ptr(DebtKindDebt)
		}
	} else {
		r.DebtKind, r.ZeroRounds = nil, 0
	}
}

// refreshItemStatusTx — an item is active while one line is; paused while one is paused; archived
// when every line is. An item archived by hand stays archived. A card plan has no line: its status
// stays as set.
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

// itemOfLine — the item id a line belongs to.
func itemOfLine(ctx context.Context, q rowQuerier, userID, lineID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx, `SELECT recurring_id FROM recurring_rules WHERE id = $1 AND user_id = $2`, lineID, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrLineNotFound
	}
	return id, err
}

// lineOfItem — the line, checked to belong to the item.
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
