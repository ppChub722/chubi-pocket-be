package projects

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// SettleLine — From owes To Amount.
type SettleLine struct {
	From   shared.MemberRef `json:"from"`
	To     shared.MemberRef `json:"to"`
	Amount float64          `json:"amount"`
}

// EventSettle — who pays whom to clear the event: Pairs nets each two members' own bills and
// paybacks; Simplified is the fewest transfers over everyone's net.
type EventSettle struct {
	Pairs      []SettleLine `json:"pairs"`
	Simplified []SettleLine `json:"simplified"`
}

type memberPair struct{ a, b uuid.UUID }

type settleMove struct {
	from, to uuid.UUID
	amount   float64
}

func (s *Service) Settle(ctx context.Context, userID, id uuid.UUID) (*EventSettle, error) {
	if _, err := s.store.GetByIDForCaller(ctx, userID, id); err != nil {
		return nil, err
	}
	// Every split child owes its parent's member (bills and paybacks alike).
	rows, err := s.store.db.Query(ctx, `
		SELECT c.transaction_member_id, p.transaction_member_id, SUM(c.amount)::float8
		FROM project_transactions c
		JOIN project_transactions p ON p.id = c.parent_project_transaction_id
		WHERE p.project_id = $1 AND p.type IN ('expense', 'payback')
		  AND c.transaction_member_id <> p.transaction_member_id
		GROUP BY 1, 2`, id)
	if err != nil {
		return nil, fmt.Errorf("settle: %w", err)
	}
	// owes[{a,b}] > 0: a owes b (a < b by string order).
	owes := map[memberPair]float64{}
	net := map[uuid.UUID]float64{}
	for rows.Next() {
		var debtor, creditor uuid.UUID
		var amount float64
		if err := rows.Scan(&debtor, &creditor, &amount); err != nil {
			rows.Close()
			return nil, err
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
		return nil, err
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
	simplified := simplifyNets(net)

	ids := map[uuid.UUID]bool{}
	for _, l := range pairs {
		ids[l.from], ids[l.to] = true, true
	}
	var memberIDs []uuid.UUID
	for m := range ids {
		memberIDs = append(memberIDs, m)
	}
	refs, err := LoadMemberRefs(ctx, s.store.db, userID, memberIDs)
	if err != nil {
		return nil, err
	}
	out := &EventSettle{Pairs: []SettleLine{}, Simplified: []SettleLine{}}
	for _, l := range pairs {
		out.Pairs = append(out.Pairs, SettleLine{From: refs[l.from], To: refs[l.to], Amount: l.amount})
	}
	for _, l := range simplified {
		out.Simplified = append(out.Simplified, SettleLine{From: refs[l.from], To: refs[l.to], Amount: l.amount})
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
