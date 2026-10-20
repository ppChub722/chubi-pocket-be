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

func DebtKeySQL(a string) string {
	return fmt.Sprintf(`(CASE WHEN %[1]s.counterparty_contact_id IS NOT NULL
		THEN 'c:' || %[1]s.counterparty_contact_id::text
		ELSE 'n:' || LOWER(TRIM(%[1]s.counterparty_person_name)) END)`, a)
}

func DebtSignedSQL(a string) string {
	return fmt.Sprintf(`(CASE %[1]s.direction WHEN 'paid' THEN %[1]s.amount ELSE -%[1]s.amount END)`, a)
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
