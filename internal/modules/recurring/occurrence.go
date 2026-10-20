package recurring

import (
	"github.com/google/uuid"
)

// Occurrence is a setting's current occurrence: the oldest one due, else the
// next one coming (within the setting's window).
type Occurrence struct {
	ItemID    *uuid.UUID `json:"item_id"` // null while it is only computed
	RuleID    uuid.UUID  `json:"rule_id"`
	Slot      string     `json:"slot"`
	Date      string     `json:"date"`
	DaysUntil int        `json:"days_until"`
	Amount    *float64   `json:"amount"` // null = not known yet (a reminder)
	Status    string     `json:"status"` // upcoming | due
}

func windowDays(r *Rule) int {
	n := r.Interval
	if n < 1 {
		n = 1
	}
	switch r.Freq {
	case "daily":
		return n
	case "weekly":
		return 7 * n
	case "yearly":
		return 366 * n
	}
	return 31 * n // monthly, custom dates
}
