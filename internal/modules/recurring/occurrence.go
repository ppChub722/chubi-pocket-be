package recurring

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Occurrence struct {
	ItemID    uuid.UUID `json:"item_id"`
	Date      string    `json:"date"`
	DaysUntil int       `json:"days_until"`
	Amount    *float64  `json:"amount"` // null = not known yet (a reminder)
	Status    string    `json:"status"` // upcoming | pending | not_created
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

func (s *Service) currentOccurrences(ctx context.Context, userID uuid.UUID, today time.Time, rules []*Rule) (map[uuid.UUID]*Occurrence, error) {
	out := map[uuid.UUID]*Occurrence{}
	if len(rules) == 0 {
		return out, nil
	}
	byID := map[uuid.UUID]*Rule{}
	ids := make([]uuid.UUID, len(rules))
	for i, r := range rules {
		ids[i], byID[r.ID] = r.ID, r
	}
	rows, err := s.db.Query(ctx, `
		SELECT rule_id, id, date, NULLIF((draft->>'amount')::numeric, 0)::float8, st FROM (
			SELECT i.rule_id, i.id, i.date, i.draft, `+statusSQL+` AS st,
			       ROW_NUMBER() OVER (PARTITION BY i.rule_id ORDER BY i.date, i.id) AS rn
			FROM recurring_items i
			WHERE i.rule_id = ANY($1) AND i.user_id = $2 AND NOT i.archived
			  AND i.transaction_id IS NULL AND i.saving_entry_id IS NULL) x
		WHERE rn <= 3 ORDER BY rule_id, date, id`, ids, userID)
	if err != nil {
		return nil, fmt.Errorf("current occurrences: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			rid, iid uuid.UUID
			d        time.Time
			amt      *float64
			st       string
		)
		if err := rows.Scan(&rid, &iid, &d, &amt, &st); err != nil {
			return nil, err
		}
		if out[rid] != nil {
			continue // the earliest one wins
		}
		r := byID[rid]
		days := int(d.Sub(today).Hours() / 24)
		if days > windowDays(r) {
			continue
		}
		out[rid] = &Occurrence{ItemID: iid, Date: d.Format(dateLayout), DaysUntil: days, Amount: amt, Status: st}
	}
	return out, rows.Err()
}
