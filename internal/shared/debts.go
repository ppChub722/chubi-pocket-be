package shared

// The debt person ledger (0.3.1-10): every personal_debts row is an entry
// {person, direction paid | received, amount > 0}. A person is keyed
// `c:<contact_id>` or `n:<lower(trimmed name)>`; net = Σ paid − Σ received
// (> 0 = they owe me). These live here because transactions (the wallet side
// of an entry, the splits of a bill) needs them too and can't import
// personal_debts.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// ErrInvalidPersonKey — a key that is neither `c:<uuid>` nor `n:<name>`.
var ErrInvalidPersonKey = errors.New("person key must be c:<contact_id> or n:<name>")

// DebtPersonKey — a parsed person key. Name is the typed text as sent (the
// shown spelling of a new typed person); Key() is the canonical form.
type DebtPersonKey struct {
	ContactID *uuid.UUID
	Name      string
}

// Key — the canonical key (n: names lower-cased and trimmed).
func (k DebtPersonKey) Key() string { return DebtKey(k.ContactID, k.Name) }

// ParseDebtKey parses `c:<uuid>` / `n:<name>`.
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

// DebtKey — the key of a stored (contact, typed name) pair.
func DebtKey(contactID *uuid.UUID, name string) string {
	if contactID != nil {
		return "c:" + contactID.String()
	}
	return "n:" + strings.ToLower(strings.TrimSpace(name))
}

// DebtKeySQL — DebtKey as SQL over the personal_debts row aliased a.
func DebtKeySQL(a string) string {
	return fmt.Sprintf(`(CASE WHEN %[1]s.counterparty_contact_id IS NOT NULL
		THEN 'c:' || %[1]s.counterparty_contact_id::text
		ELSE 'n:' || LOWER(TRIM(%[1]s.counterparty_person_name)) END)`, a)
}

// DebtSignedSQL — the entry's effect on net (paid +, received −).
func DebtSignedSQL(a string) string {
	return fmt.Sprintf(`(CASE %[1]s.direction WHEN 'paid' THEN %[1]s.amount ELSE -%[1]s.amount END)`, a)
}

// DebtLink — spec schema DebtLink: on a transaction that is a debt entry's
// wallet side (one record with the entry).
type DebtLink struct {
	EntryID   uuid.UUID `json:"entry_id"`
	Key       string    `json:"key"`
	Person    PersonRef `json:"person"`
	Direction string    `json:"direction"`
	Paid      bool      `json:"paid"`
}

// DueChecker — the recurring due check (produce the due items into pending
// drafts, notify once). Runs inside the boot reads (badge counts, dashboard,
// notifications) and the recurring reads; wired in main.go.
type DueChecker func(ctx context.Context, userID uuid.UUID, headerTZ string) error

// RecurringRef — spec schema RecurringRef.
type RecurringRef struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	IconCode *IconCode `json:"icon_code"`
}

// RecurringItemLink — spec schema RecurringItemLink: on a transaction a
// recurring item is linked to.
type RecurringItemLink struct {
	ItemID    uuid.UUID     `json:"item_id"`
	Recurring *RecurringRef `json:"recurring"`
	Seq       *int          `json:"seq"`
	Total     *int          `json:"total"`
}
