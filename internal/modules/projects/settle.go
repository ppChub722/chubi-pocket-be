package projects

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// SettleLine — From owes To Amount. Recorded (0.4.1-2 V8, a line with me in it): a real pair whose
// bills are all claimed into my book (listClaimableBills), or — any line — I booked the event's
// fewest-transfers settle (bookEventSettle). The 0.4.0-4 "{event_id}" debt per line is retired.
type SettleLine struct {
	From     shared.MemberRef `json:"from"`
	To       shared.MemberRef `json:"to"`
	Amount   float64          `json:"amount"`
	Recorded bool             `json:"recorded"`
}

// settleQ — the pool or a DB transaction.
type settleQ interface {
	shared.Querier
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// EventSettle — who pays whom to clear the event: Pairs nets each two members' own bills and
// paybacks; Simplified is the fewest transfers over everyone's net.
type EventSettle struct {
	Pairs      []SettleLine `json:"pairs"`
	Simplified []SettleLine `json:"simplified"`
	// V12: the simplified lines may be booked only after completion; my one-time booking, if done.
	SimplifiedAvailable bool             `json:"simplified_available"`
	MySettleBooking     *MySettleBooking `json:"my_settle_booking"`
}

type memberPair struct{ a, b uuid.UUID }

type settleMove struct {
	from, to uuid.UUID
	amount   float64
}

func (s *Service) Settle(ctx context.Context, userID, id uuid.UUID) (*EventSettle, error) {
	p, err := s.store.GetByIDForCaller(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	pairs, simplified, err := settleMoves(ctx, s.store.db, id)
	if err != nil {
		return nil, err
	}
	out, err := s.settleView(ctx, s.store.db, userID, id, pairs, simplified)
	if err != nil {
		return nil, err
	}
	out.SimplifiedAvailable = p.CompletedAt != nil
	if out.MySettleBooking, err = mySettleBooking(ctx, s.store.db, id, userID); err != nil {
		return nil, err
	}
	return out, nil
}

// settleMoves — the real pairs and the fewest-transfer lines of an event.
func settleMoves(ctx context.Context, q shared.Querier, id uuid.UUID) ([]settleMove, []settleMove, error) {
	// Every split child owes its parent's member (bills and paybacks alike); on an income bill the
	// member who received owes each split their share (V14, Q-E3: the mirror of an expense).
	// Multi-payer (migration 105): the view splits each share over the payers by what they paid.
	rows, err := q.Query(ctx, `SELECT debtor, creditor, SUM(amount)::float8 FROM bill_pairs
		WHERE project_id = $1 GROUP BY 1, 2`, id)
	if err != nil {
		return nil, nil, fmt.Errorf("settle: %w", err)
	}
	// owes[{a,b}] > 0: a owes b (a < b by string order).
	owes := map[memberPair]float64{}
	net := map[uuid.UUID]float64{}
	for rows.Next() {
		var debtor, creditor uuid.UUID
		var amount float64
		if err := rows.Scan(&debtor, &creditor, &amount); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if debtor.String() < creditor.String() {
			owes[memberPair{debtor, creditor}] += amount
		} else {
			owes[memberPair{creditor, debtor}] -= amount
		}
		net[debtor] -= amount
		net[creditor] += amount
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	var pairs []settleMove
	for k, v := range owes {
		switch {
		case v > 0.005:
			pairs = append(pairs, settleMove{k.a, k.b, round2(v)})
		case v < -0.005:
			pairs = append(pairs, settleMove{k.b, k.a, round2(-v)})
		}
	}
	return pairs, simplifyNets(net), nil
}

func (s *Service) settleView(ctx context.Context, q settleQ, userID, id uuid.UUID, pairs, simplified []settleMove) (*EventSettle, error) {
	ids := map[uuid.UUID]bool{}
	for _, l := range pairs {
		ids[l.from], ids[l.to] = true, true
	}
	var memberIDs []uuid.UUID
	for m := range ids {
		memberIDs = append(memberIDs, m)
	}
	refs, err := LoadMemberRefs(ctx, q, userID, memberIDs)
	if err != nil {
		return nil, err
	}
	// V8: recorded = claimed (every bill of that pair in my book), or my settle booking
	booked, err := mySettleBooking(ctx, q, id, userID)
	if err != nil {
		return nil, err
	}
	var me uuid.UUID
	if err := q.QueryRow(ctx, `SELECT id FROM project_members WHERE project_id = $1 AND user_id = $2`, id, userID).Scan(&me); err != nil {
		return nil, ErrNotMember
	}
	bills, err := claimablePairsTx(ctx, q, userID, id, me, nil)
	if err != nil {
		return nil, err
	}
	open := map[uuid.UUID]bool{}
	for _, b := range bills {
		if !b.Claimed {
			open[b.other] = true
		}
	}
	line := func(m settleMove, simplified bool) SettleLine {
		l := SettleLine{From: refs[m.from], To: refs[m.to], Amount: m.amount}
		other := m.to
		if m.to == me {
			other = m.from
		}
		mine := m.from == me || m.to == me
		l.Recorded = mine && (booked != nil || (!simplified && !open[other]))
		return l
	}
	out := &EventSettle{Pairs: []SettleLine{}, Simplified: []SettleLine{}}
	for _, l := range pairs {
		out.Pairs = append(out.Pairs, line(l, false))
	}
	for _, l := range simplified {
		out.Simplified = append(out.Simplified, line(l, true))
	}
	sortSettle(out.Pairs)
	sortSettle(out.Simplified)
	return out, nil
}

// simplifyNets pairs the largest debtor with the largest creditor until everyone is even.
func simplifyNets(net map[uuid.UUID]float64) []settleMove {
	type bal struct {
		id uuid.UUID
		v  float64
	}
	var debtors, creditors []bal
	for id, v := range net {
		switch {
		case v < -0.005:
			debtors = append(debtors, bal{id, -v})
		case v > 0.005:
			creditors = append(creditors, bal{id, v})
		}
	}
	byAmount := func(xs []bal) {
		sort.Slice(xs, func(i, j int) bool {
			if xs[i].v != xs[j].v {
				return xs[i].v > xs[j].v
			}
			return xs[i].id.String() < xs[j].id.String()
		})
	}
	var out []settleMove
	for len(debtors) > 0 && len(creditors) > 0 {
		byAmount(debtors)
		byAmount(creditors)
		d, c := &debtors[0], &creditors[0]
		pay := math.Min(d.v, c.v)
		out = append(out, settleMove{d.id, c.id, round2(pay)})
		d.v -= pay
		c.v -= pay
		if d.v <= 0.005 {
			debtors = debtors[1:]
		}
		if c.v <= 0.005 {
			creditors = creditors[1:]
		}
	}
	return out
}

func sortSettle(ls []SettleLine) {
	sort.SliceStable(ls, func(i, j int) bool { return ls[i].Amount > ls[j].Amount })
}
