package shared

import "github.com/google/uuid"

// PersonRecurring — a "หนี้" recurring setting as the person page lists it (0.4.1-2 K13): the
// debts module embeds the rows the recurring module answers through a hook (no import cycle).
type PersonRecurring struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	IconCode    *IconCode `json:"icon_code"`
	DebtKind    string    `json:"debt_kind"` // debt | repayment
	Direction   string    `json:"direction"` // paid | received
	Amount      float64   `json:"amount"`
	NextDate    *string   `json:"next_date"`
	NextAmount  *float64  `json:"next_amount"`
	DueCount    int       `json:"due_count"`
	Status      string    `json:"status"`
	PauseReason *string   `json:"pause_reason"`
}
