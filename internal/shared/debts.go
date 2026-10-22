package shared

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

var ErrInvalidPersonKey = errors.New("person key must be c:<contact_id> or n:<name>")

type DebtPersonKey struct {
	ContactID *uuid.UUID
	Name      string
}

func (k DebtPersonKey) Key() string { return DebtKey(k.ContactID, k.Name) }

func ParseDebtKey(s string) (DebtPersonKey, error) {
	switch {
	case strings.HasPrefix(s, "c:"):
		id, err := uuid.Parse(strings.TrimSpace(s[2:]))
		if err != nil {
			return DebtPersonKey{}, ErrInvalidPersonKey
		}
		return DebtPersonKey{ContactID: &id}, nil
	case strings.HasPrefix(s, "n:"):
		name := strings.TrimSpace(s[2:])
		if name == "" || len([]rune(name)) > 100 {
			return DebtPersonKey{}, ErrInvalidPersonKey
		}
		return DebtPersonKey{Name: name}, nil
	}
	return DebtPersonKey{}, ErrInvalidPersonKey
}

func DebtKey(contactID *uuid.UUID, name string) string {
	if contactID != nil {
		return "c:" + contactID.String()
	}
	return "n:" + strings.ToLower(strings.TrimSpace(name))
}

func personKeySQL(contactCol, nameCol string) string {
	return fmt.Sprintf(`(CASE WHEN %[1]s IS NOT NULL
		THEN 'c:' || %[1]s::text
		ELSE 'n:' || LOWER(TRIM(%[2]s)) END)`, contactCol, nameCol)
}

// DebtKeySQL — the person key of a personal_debts row (alias a).
func DebtKeySQL(a string) string {
	return personKeySQL(a+".counterparty_contact_id", a+".counterparty_person_name")
}

// RepayKeySQL — the person key of a repayment transaction (alias a; repay_person_name set).
func RepayKeySQL(a string) string {
	return personKeySQL(a+".repay_contact_id", a+".repay_person_name")
}

func DebtSignedSQL(a string) string {
	return fmt.Sprintf(`(CASE %[1]s.direction WHEN 'paid' THEN %[1]s.amount ELSE -%[1]s.amount END)`, a)
}

// RepaySignedSQL — a repayment I PAY (expense) = + (it lowers what I owe / raises what they owe);
// one I RECEIVE (income) = −.
func RepaySignedSQL(a string) string {
	return fmt.Sprintf(`(CASE %[1]s.type WHEN 'expense' THEN %[1]s.amount ELSE -%[1]s.amount END)`, a)
}

// DebtLedgerSQL — every signed line of the user's person ledger (userParam = the user id): the
// debts (personal_debts) and the repayments (transactions with a repay person). A debt's own
// linked transaction is not a line. Columns: kind (debt | repayment), key, id, cid, snap (the
// typed-name snapshot), signed, paid (always TRUE on a repayment: never "open"), date, created_at,
// descr (the line's description).
// Wrap it: SELECT … FROM (` + DebtLedgerSQL("$1") + `) l.
func DebtLedgerSQL(userParam string) string {
	return `SELECT 'debt' AS kind, ` + DebtKeySQL("lpd") + ` AS key, lpd.id,
		       lpd.counterparty_contact_id AS cid, lpd.counterparty_person_name AS snap,
		       ` + DebtSignedSQL("lpd") + ` AS signed, lpd.paid, lpd.date, lpd.created_at, lpd.description AS descr
		FROM personal_debts lpd WHERE lpd.user_id = ` + userParam + `
		UNION ALL
		SELECT 'repayment', ` + RepayKeySQL("lrt") + `, lrt.id,
		       lrt.repay_contact_id, lrt.repay_person_name,
		       ` + RepaySignedSQL("lrt") + `, TRUE, lrt.date, lrt.created_at, lrt.description
		FROM transactions lrt WHERE lrt.user_id = ` + userParam + ` AND lrt.repay_person_name IS NOT NULL`
}

// DebtSide — receivable (they owe me) | payable (I owe them) | clear, from a person's net.
func DebtSide(net float64) string {
	switch {
	case net > 0.005:
		return "receivable"
	case net < -0.005:
		return "payable"
	}
	return "clear"
}

type DebtLink struct {
	EntryID   uuid.UUID `json:"entry_id"`
	Key       string    `json:"key"`
	Person    PersonRef `json:"person"`
	Direction string    `json:"direction"`
	Paid      bool      `json:"paid"`
}

type DueChecker func(ctx context.Context, userID uuid.UUID, headerTZ string) error

type RecurringRef struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	IconCode *IconCode `json:"icon_code"`
	// Kind — subscription | installment | income | reminder (set by the recurring module only).
	Kind string `json:"kind,omitempty"`
}

type RecurringItemLink struct {
	ItemID    uuid.UUID     `json:"item_id"`
	Recurring *RecurringRef `json:"recurring"`
	Seq       *int          `json:"seq"`
	Total     *int          `json:"total"`
}
