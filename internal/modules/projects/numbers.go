package projects

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// MyShareTotal / PaidTotal: active events. ReceivableTotal / PayableTotal and
// every count: all events but archived.
type EventListSummary struct {
	MyShareTotal    float64     `json:"my_share_total"`
	PaidTotal       float64     `json:"paid_total"`
	ReceivableTotal float64     `json:"receivable_total"`
	PayableTotal    float64     `json:"payable_total"`
	Counts          EventCounts `json:"counts"`
}

type EventCounts struct {
	All        int `json:"all"`
	Active     int `json:"active"`
	Unsettled  int `json:"unsettled"`
	Receivable int `json:"receivable"`
	Payable    int `json:"payable"`
	Ended      int `json:"ended"`
}

func fillMyNumbers(ctx context.Context, q shared.Querier, callerID uuid.UUID, events map[uuid.UUID]Event, ids []uuid.UUID) error {
	rows, err := q.Query(ctx, `
		SELECT p.id,
		  COALESCE((SELECT SUM(sp.paid) FROM project_transactions x
		            JOIN split_transaction_people sp ON sp.split_transaction_id = x.id AND sp.event_member_id = me.id
		            WHERE x.project_id = p.id AND x.type = 'expense'), 0)::float8,
		  COALESCE((SELECT SUM(sp.share) FROM project_transactions x
		            JOIN split_transaction_people sp ON sp.split_transaction_id = x.id AND sp.event_member_id = me.id
		            WHERE x.project_id = p.id AND x.type = 'expense'), 0)::float8
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

func (s *Service) listSummary(ctx context.Context, userID uuid.UUID) (EventListSummary, error) {
	var out EventListSummary
	rows, err := s.store.db.Query(ctx, `SELECT p.id FROM projects p
		JOIN project_members m ON m.project_id = p.id AND m.user_id = $1 AND m.status = 'active'
		WHERE p.status <> 'archived' AND p.kind = 'event'`, userID)
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
		out.Counts.All++
		switch e.Status {
		case StatusActive:
			out.Counts.Active++
			out.MyShareTotal += e.MyShare
			out.PaidTotal += e.MyPaid
		case StatusCompleted, StatusCancelled:
			out.Counts.Ended++
		}
		if e.MyReceivable > 0 {
			out.Counts.Receivable++
			out.ReceivableTotal += e.MyReceivable
		}
		if e.MyPayable > 0 {
			out.Counts.Payable++
			out.PayableTotal += e.MyPayable
		}
		if e.MyReceivable > 0 || e.MyPayable > 0 {
			out.Counts.Unsettled++
		}
	}
	out.MyShareTotal, out.PaidTotal = round2(out.MyShareTotal), round2(out.PaidTotal)
	out.ReceivableTotal, out.PayableTotal = round2(out.ReceivableTotal), round2(out.PayableTotal)
	return out, nil
}
