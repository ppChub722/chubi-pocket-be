package projects

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// SettleLine — From owes To Amount. Recorded (a line with me in it): I already have a debt
// for exactly this line (POST /personal-debts/…/entries with event_id).
type SettleLine struct {
	From     shared.MemberRef `json:"from"`
	To       shared.MemberRef `json:"to"`
	Amount   float64          `json:"amount"`
	Recorded bool             `json:"recorded"`
}

// eventDebt — one of my debts recording an event's จัดการ line (source event, no bill).
type eventDebt struct {
	direction    string
	amount       float64
	personName   string
	linkedUserID *uuid.UUID
	contactName  *string
}

func loadEventDebts(ctx context.Context, q shared.Querier, userID, eventID uuid.UUID) ([]eventDebt, error) {
	rows, err := q.Query(ctx, `SELECT d.direction, d.amount::float8, d.counterparty_person_name,
			c.linked_user_id, c.display_name
		FROM personal_debts d
		LEFT JOIN contacts c ON c.id = d.counterparty_contact_id
		WHERE d.user_id = $1 AND d.project_id = $2 AND d.source_kind = 'event'
		  AND d.source_project_transaction_id IS NULL`, userID, eventID)
	if err != nil {
		return nil, fmt.Errorf("settle debts: %w", err)
	}
	defer rows.Close()
	var out []eventDebt
	for rows.Next() {
		var d eventDebt
		if err := rows.Scan(&d.direction, &d.amount, &d.personName, &d.linkedUserID, &d.contactName); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// recorded: the line has me in it and one of debts records it (my side, the other member,
// the same amount).
func (l SettleLine) recorded(debts []eventDebt) bool {
	var other shared.MemberRef
	dir := ""
	switch {
	case l.From.IsMe:
		other, dir = l.To, "received" // I owe them
	case l.To.IsMe:
		other, dir = l.From, "paid" // they owe me
	default:
		return false
	}
	for _, d := range debts {
		if d.direction != dir || math.Abs(d.amount-l.Amount) > 0.005 {
			continue
		}
		if (d.linkedUserID != nil && other.UserID != nil && *d.linkedUserID == *other.UserID) ||
			strings.EqualFold(strings.TrimSpace(d.personName), strings.TrimSpace(other.Name)) ||
			(d.contactName != nil && strings.EqualFold(strings.TrimSpace(*d.contactName), strings.TrimSpace(other.Name))) {
			return true
		}
	}
	return false
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
	debts, err := loadEventDebts(ctx, s.store.db, userID, id)
	if err != nil {
		return nil, err
	}
	line := func(m settleMove) SettleLine {
		l := SettleLine{From: refs[m.from], To: refs[m.to], Amount: m.amount}
		l.Recorded = l.recorded(debts)
		return l
	}
	out := &EventSettle{Pairs: []SettleLine{}, Simplified: []SettleLine{}}
	for _, l := range pairs {
		out.Pairs = append(out.Pairs, line(l))
	}
	for _, l := range simplified {
		out.Simplified = append(out.Simplified, line(l))
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
