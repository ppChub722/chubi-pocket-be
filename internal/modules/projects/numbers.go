package projects

// 0.4.0 numbers for the event pages: my paid / share / receivable / payable
// on every event row, and the list summary card.

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// EventListSummary — EventList.summary: over the caller's own events (member,
// not archived), the same whatever the list's filter. The three totals are
// over the ACTIVE events.
type EventListSummary struct {
	MyShareTotal    float64     `json:"my_share_total"`
	PaidTotal       float64     `json:"paid_total"`
	ReceivableTotal float64     `json:"receivable_total"`
	Counts          EventCounts `json:"counts"`
}

// EventCounts — active events, events where money is still open for me
// (my receivable / payable ≠ 0, active or ended) and ended ones
// (completed / cancelled).
type EventCounts struct {
	Active    int `json:"active"`
	Unsettled int `json:"unsettled"`
	Ended     int `json:"ended"`
}

// fillMyNumbers sets MyPaid / MyShare / MyReceivable / MyPayable / Remaining
// on the events (called by loadEvents after the base rows).
func fillMyNumbers(ctx context.Context, q shared.Querier, callerID uuid.UUID, events map[uuid.UUID]Event, ids []uuid.UUID) error {
	rows, err := q.Query(ctx, `
		SELECT p.id,
		  COALESCE((SELECT SUM(x.amount) FROM project_transactions x
		            WHERE x.project_id = p.id AND x.parent_project_transaction_id IS NULL
		              AND x.type = 'expense' AND x.transaction_member_id = me.id), 0)::float8,
		  COALESCE((SELECT SUM(CASE WHEN x.transaction_member_id = me.id
		              THEN x.amount - COALESCE((SELECT SUM(c.amount) FROM project_transactions c
		                                        WHERE c.parent_project_transaction_id = x.id), 0)
		              ELSE COALESCE((SELECT SUM(c.amount) FROM project_transactions c
		                             WHERE c.parent_project_transaction_id = x.id AND c.transaction_member_id = me.id), 0)
		            END)
		            FROM project_transactions x
		            WHERE x.project_id = p.id AND x.parent_project_transaction_id IS NULL AND x.type = 'expense'), 0)::float8
		FROM projects p
		JOIN project_members me ON me.project_id = p.id AND me.user_id = $2
		WHERE p.id = ANY($1)`, ids, callerID)
	if err != nil {
		return fmt.Errorf("event my numbers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id          uuid.UUID
			paid, share float64
		)
		if err := rows.Scan(&id, &paid, &share); err != nil {
			return err
		}
		e := events[id]
		e.MyPaid, e.MyShare = round2(paid), round2(share)
		events[id] = e
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for id, e := range events {
		if e.MyNet > 0.005 {
			e.MyReceivable = e.MyNet
		} else if e.MyNet < -0.005 {
			e.MyPayable = -e.MyNet
		}
		if e.PlannedAmount != nil {
			r := round2(*e.PlannedAmount - (e.TotalExpense - e.TotalIncome))
			e.Remaining = &r
		}
		events[id] = e
	}
	return nil
}

// listSummary — the events list's summary card.
func (s *Service) listSummary(ctx context.Context, userID uuid.UUID) (EventListSummary, error) {
	var out EventListSummary
	rows, err := s.store.db.Query(ctx, `SELECT p.id FROM projects p
		JOIN project_members m ON m.project_id = p.id AND m.user_id = $1 AND m.status = 'active'
		WHERE p.status <> 'archived'`, userID)
	if err != nil {
		return out, fmt.Errorf("event summary ids: %w", err)
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	events, err := loadEvents(ctx, s.store.db, userID, ids)
	if err != nil {
		return out, err
	}
	for _, e := range events {
		switch e.Status {
		case StatusActive:
			out.Counts.Active++
			out.MyShareTotal += e.MyShare
			out.PaidTotal += e.MyPaid
			out.ReceivableTotal += e.MyReceivable
		case StatusCompleted, StatusCancelled:
			out.Counts.Ended++
		}
		if e.MyReceivable > 0 || e.MyPayable > 0 {
			out.Counts.Unsettled++
		}
	}
	out.MyShareTotal, out.PaidTotal, out.ReceivableTotal = round2(out.MyShareTotal), round2(out.PaidTotal), round2(out.ReceivableTotal)
	return out, nil
}
