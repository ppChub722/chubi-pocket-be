package shared

import (
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

func DebtSignedSQL(a string) string {
	return fmt.Sprintf(`(CASE %[1]s.direction WHEN 'paid' THEN %[1]s.amount ELSE -%[1]s.amount END)`, a)
}

// DebtLedgerSQL — every signed line of the user's person ledger (userParam = the user id), ONE
// table since 0.4.1-1 D3: every personal_debts row, kind debt | repayment, paid + / received −.
// Transactions never count. Columns: kind, key, id, cid, snap (the typed-name snapshot), signed,
// paid (always TRUE on a repayment: never "open"), date, created_at, descr (the line's
// description), due_date (debts only).
// Wrap it: SELECT … FROM (` + DebtLedgerSQL("$1") + `) l.
func DebtLedgerSQL(userParam string) string {
	return `SELECT lpd.kind, ` + DebtKeySQL("lpd") + ` AS key, lpd.id,
		       lpd.counterparty_contact_id AS cid, lpd.counterparty_person_name AS snap,
		       ` + DebtSignedSQL("lpd") + ` AS signed, (lpd.paid OR lpd.kind = 'repayment') AS paid,
		       lpd.date, lpd.created_at, lpd.description AS descr,
		       CASE WHEN lpd.kind = 'debt' THEN lpd.due_date END AS due_date
		FROM personal_debts lpd WHERE lpd.user_id = ` + userParam
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
	Kind      string    `json:"kind"` // debt | repayment (0.4.1-1 D3)
	Key       string    `json:"key"`
	Person    PersonRef `json:"person"`
	Direction string    `json:"direction"`
	Paid      bool      `json:"paid"`
	// Connected (0.4.1-1 D2) — an existing transaction connected later (can be disconnected);
	// false = made from the entry.
	Connected bool `json:"connected"`
}

type RecurringRef struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	IconCode *IconCode `json:"icon_code"`
	// Kind — subscription | installment | income | reminder (set by the recurring module only).
	Kind string `json:"kind,omitempty"`
	// PauseReason — manual | overdue_limit | goal_inactive while paused (0.4.1-1 R7; recurring
	// module only, absent elsewhere).
	PauseReason *string `json:"pause_reason,omitempty"`
}

type RecurringItemLink struct {
	ItemID    uuid.UUID     `json:"item_id"`
	Recurring *RecurringRef `json:"recurring"`
	Seq       *int          `json:"seq"`
	Total     *int          `json:"total"`
}
