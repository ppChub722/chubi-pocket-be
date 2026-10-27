package personal_debts

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// ledgerLine is one line of a person's ledger in book order, with the running balance
// right after it. A person page reads them ONCE and derives the cycle boundary, the
// counts and every page of the statement from them.
type ledgerLine struct {
	Kind    string
	ID      uuid.UUID
	Date    string
	Created time.Time
	Run     float64
	Money   bool // a row with a transaction
}

func (s *Store) ledgerLines(ctx context.Context, q shared.Querier, userID uuid.UUID, key string) ([]ledgerLine, error) {
	rows, err := q.Query(ctx, `SELECT l.kind, l.id, to_char(l.date, 'YYYY-MM-DD'), l.created_at, l.run,
		       EXISTS (SELECT 1 FROM transactions t WHERE t.source_personal_debt_id = l.id)
		FROM (SELECT l.*, SUM(l.signed) OVER (ORDER BY l.date, l.created_at, l.id) AS run
		      FROM `+ledgerSQL+` l WHERE l.key = $2) l
		ORDER BY l.date, l.created_at, l.id`, userID, key)
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	defer rows.Close()
	var out []ledgerLine
	for rows.Next() {
		var l ledgerLine
		if err := rows.Scan(&l.Kind, &l.ID, &l.Date, &l.Created, &l.Run, &l.Money); err != nil {
			return nil, fmt.Errorf("ledger scan: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// cycleMarkOf is the last line (book order) that brought the balance back to zero.
func cycleMarkOf(lines []ledgerLine) *cycleMark {
	for i := len(lines) - 1; i >= 0; i-- {
		if r := lines[i].Run; r < 0.005 && r > -0.005 {
			return &cycleMark{Date: lines[i].Date, Created: lines[i].Created, ID: lines[i].ID}
		}
	}
	return nil
}

// countsOf — over the statement (debts + repayments): money = lines with a transaction;
// current = the lines after the cycle boundary.
func countsOf(lines []ledgerLine, mark *cycleMark) EntryCounts {
	c := EntryCounts{Entries: len(lines)}
	for _, l := range lines {
		if l.Money {
			c.Money++
		}
		if mark.after(l.Date, l.Created, l.ID) {
			c.Current++
		}
	}
	c.Earlier = c.Entries - c.Current
	return c
}

// cycleLines are the lines of a cycle (current | earlier | anything else = all), book order.
func cycleLines(lines []ledgerLine, mark *cycleMark, cycle string) []ledgerLine {
	switch {
	case mark != nil && (cycle == "current" || cycle == "earlier"):
		var out []ledgerLine
		for _, l := range lines {
			if mark.after(l.Date, l.Created, l.ID) == (cycle == "current") {
				out = append(out, l)
			}
		}
		return out
	case mark == nil && cycle == "earlier":
		return nil
	}
	return lines
}

// pageNewestFirst is one page of the lines, newest first.
func pageNewestFirst(lines []ledgerLine, page, perPage int) []ledgerLine {
	var out []ledgerLine
	for i, skip := len(lines)-1, (page-1)*perPage; i >= 0 && len(out) < perPage; i-- {
		if skip > 0 {
			skip--
			continue
		}
		out = append(out, lines[i])
	}
	return out
}

// statementItems builds the items of the lines (debts and repayments) in one read.
func (s *Store) statementItems(ctx context.Context, q shared.Querier, userID uuid.UUID, lines []ledgerLine, mark *cycleMark) (map[uuid.UUID]StatementItem, error) {
	ids := make([]uuid.UUID, 0, len(lines))
	for _, l := range lines {
		ids = append(ids, l.ID)
	}
	byID := map[uuid.UUID]StatementItem{}
	if len(ids) > 0 {
		rows, err := loadViews(ctx, q, `WHERE pd.user_id = $1 AND pd.id = ANY($2)`, userID, ids)
		if err != nil {
			return nil, err
		}
		for _, d := range rows {
			byID[d.ID] = debtItem(d)
		}
	}
	for _, l := range lines {
		it, ok := byID[l.ID]
		if !ok {
			continue
		}
		cur := mark.after(it.Date, it.CreatedAt, it.ID)
		it.InCurrentCycle = &cur
		it.RunningBalance = round2(l.Run)
		byID[l.ID] = it
	}
	return byID, nil
}

func itemsOf(lines []ledgerLine, byID map[uuid.UUID]StatementItem) []StatementItem {
	out := make([]StatementItem, 0, len(lines))
	for _, l := range lines {
		if it, ok := byID[l.ID]; ok {
			out = append(out, it)
		}
	}
	return out
}
