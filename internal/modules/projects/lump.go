package projects

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Settling an event (agreed 0.4.1-2): the summary nets every paper of the event by real pairs. "ลง
// หนี้" with one person books ONE debt per direction made of every bill pair not booked yet (a lump,
// remembering its bills), plus a repayment row per payback paper between us; any time. After
// completion "จ่ายน้อยครั้งสุด" may be booked once instead (real pairs booked first are subtracted).

var (
	ErrSettleNothing       = errors.New("nothing left to book with that member")
	ErrSettleSimplifiedOff = errors.New("จ่ายน้อยครั้งสุด opens once the event is completed")
	ErrSettleBookedOnce    = errors.New("the simplified lines are booked already")
	ErrSettleSimplifiedSet = errors.New("the simplified lines are booked: real pairs can't be booked any more")
)

// SettleLine — one move: From pays To Amount. For my lines: Booked = what my books hold of it now,
// Unbooked = what a "ลงหนี้" would add, InSync = my lump debts with them match the papers.
type SettleLine struct {
	From     shared.MemberRef `json:"from"`
	To       shared.MemberRef `json:"to"`
	Amount   float64          `json:"amount"`
	Mine     bool             `json:"mine"`
	Booked   float64          `json:"booked"`
	Unbooked float64          `json:"unbooked"`
	InSync   bool             `json:"in_sync"`
}

type EventSettle struct {
	Pairs               []SettleLine     `json:"pairs"`
	Simplified          []SettleLine     `json:"simplified"`
	SimplifiedAvailable bool             `json:"simplified_available"`
	MySettleBooking     *MySettleBooking `json:"my_settle_booking"`
}

type MySettleBooking struct {
	BookedAt time.Time `json:"booked_at"`
}

// SettleBookRequest — POST /projects/{id}/settle/book: mode pairs (with one member) or simplified
// (my lines, once). AccountID — the wallet of the payback repayments booked with it (optional).
type SettleBookRequest struct {
	Mode      string     `json:"mode"      binding:"omitempty,oneof=pairs simplified"`
	With      *uuid.UUID `json:"with"`
	AccountID *uuid.UUID `json:"account_id"`
}

type SettleBookResult struct {
	Settle  *EventSettle   `json:"settle"`
	Effects shared.Effects `json:"effects"`
}

type settleMove struct {
	from, to uuid.UUID
	amount   float64
}

// eventPapersTx — every paper of the event as viewer sees it.
func eventPapers(ctx context.Context, q shared.Querier, viewer, projectID uuid.UUID) (map[uuid.UUID]shared.Paper, error) {
	ids, err := collectIDs(ctx, q, `SELECT id FROM project_transactions WHERE project_id = $1`, projectID)
	if err != nil {
		return nil, err
	}
	return shared.LoadPapers(ctx, q, viewer, ids)
}

// memberNets — real pairs over the papers by member: owes[a][b] > 0 = a owes b.
func memberNets(papers map[uuid.UUID]shared.Paper) (map[[2]uuid.UUID]float64, map[uuid.UUID]float64) {
	pairs := map[[2]uuid.UUID]float64{}
	net := map[uuid.UUID]float64{}
	for _, p := range papers {
		for _, pr := range p.Pairs() {
			d, c := p.Person(pr.Debtor), p.Person(pr.Creditor)
			if d == nil || c == nil || d.MemberID == nil || c.MemberID == nil {
				continue
			}
			a, b := *d.MemberID, *c.MemberID
			if a.String() < b.String() {
				pairs[[2]uuid.UUID{a, b}] += pr.Amount
			} else {
				pairs[[2]uuid.UUID{b, a}] -= pr.Amount
			}
			net[a] -= pr.Amount
			net[b] += pr.Amount
		}
	}
	return pairs, net
}

// Settle — GET /projects/{id}/settle.
func (s *Service) Settle(ctx context.Context, userID, id uuid.UUID) (*EventSettle, error) {
	p, err := s.store.GetByIDForCaller(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	return s.settleView(ctx, s.store.db, userID, p)
}

func (s *Service) settleView(ctx context.Context, q shared.Querier, userID uuid.UUID, p *Project) (*EventSettle, error) {
	papers, err := eventPapers(ctx, q, userID, p.ID)
	if err != nil {
		return nil, err
	}
	pairs, net := memberNets(papers)
	var me uuid.UUID
	mids, err := collectIDs(ctx, q, `SELECT id FROM project_members WHERE project_id = $1 AND user_id = $2`, p.ID, userID)
	if err != nil {
		return nil, err
	}
	if len(mids) > 0 {
		me = mids[0]
	}
	var moves []settleMove
	for k, v := range pairs {
		switch {
		case v > 0.005:
			moves = append(moves, settleMove{k[0], k[1], round2(v)})
		case v < -0.005:
			moves = append(moves, settleMove{k[1], k[0], round2(-v)})
		}
	}
	simplified := simplifyNets(net)
	ids := map[uuid.UUID]bool{}
	for _, m := range append(append([]settleMove{}, moves...), simplified...) {
		ids[m.from], ids[m.to] = true, true
	}
	var memberIDs []uuid.UUID
	for m := range ids {
		memberIDs = append(memberIDs, m)
	}
	refs, err := LoadMemberRefs(ctx, q, userID, memberIDs)
	if err != nil {
		return nil, err
	}
	parts, err := personal_debts.MyPaperPartsTx(ctx, q, userID, paperIDs(papers))
	if err != nil {
		return nil, err
	}
	booking, err := mySettleBooking(ctx, q, p.ID, userID)
	if err != nil {
		return nil, err
	}
	out := &EventSettle{Pairs: []SettleLine{}, Simplified: []SettleLine{}, SimplifiedAvailable: p.CompletedAt != nil,
		MySettleBooking: booking}
	for _, m := range moves {
		l := SettleLine{From: refs[m.from], To: refs[m.to], Amount: m.amount, InSync: true}
		if m.from == me || m.to == me {
			other := m.to
			if m.to == me {
				other = m.from
			}
			l.Mine = true
			l.Booked, l.Unbooked, l.InSync = bookedWith(papers, parts, me, other)
		}
		out.Pairs = append(out.Pairs, l)
	}
	for _, m := range simplified {
		l := SettleLine{From: refs[m.from], To: refs[m.to], Amount: m.amount, Mine: m.from == me || m.to == me, InSync: true}
		out.Simplified = append(out.Simplified, l)
	}
	sortSettle(out.Pairs)
	sortSettle(out.Simplified)
	return out, nil
}

func paperIDs(papers map[uuid.UUID]shared.Paper) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(papers))
	for id := range papers {
		out = append(out, id)
	}
	return out
}

// bookedWith — between me and other (members): what my parts hold (signed: + they owe me), what
// is still unbooked, and whether the booked parts match the papers.
func bookedWith(papers map[uuid.UUID]shared.Paper, parts []personal_debts.PaperPart, me, other uuid.UUID) (float64, float64, bool) {
	booked, unbooked, inSync := 0.0, 0.0, true
	partOf := map[[2]uuid.UUID]personal_debts.PaperPart{}
	for _, p := range parts {
		partOf[[2]uuid.UUID{p.PaperID, p.PersonID}] = p
	}
	for _, paper := range papers {
		mine, theirs := personOfMember(paper, me), personOfMember(paper, other)
		if mine == nil || theirs == nil {
			continue
		}
		v := -paper.NetOf(mine.ID)[theirs.ID] // + they owe me
		if part, ok := partOf[[2]uuid.UUID{paper.ID, theirs.ID}]; ok {
			booked += math.Copysign(part.Amount, v)
			if math.Abs(math.Abs(v)-part.Amount) >= 0.005 {
				inSync = false
			}
		} else {
			unbooked += v
		}
	}
	return round2(booked), round2(unbooked), inSync
}

func personOfMember(p shared.Paper, member uuid.UUID) *shared.PaperPerson {
	for i := range p.People {
		if m := p.People[i].MemberID; m != nil && *m == member {
			return &p.People[i]
		}
	}
	return nil
}

// BookSettle — "ลงหนี้": pairs with one member (any time), or my simplified lines (completed, once).
func (s *Service) BookSettle(ctx context.Context, userID, id uuid.UUID, req SettleBookRequest) (*SettleBookResult, error) {
	if s.debts == nil {
		return nil, errors.New("settle booking needs the debts module")
	}
	p, err := s.store.GetByIDForCaller(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if err := writable(p.Status, "resolve"); err != nil {
		return nil, err
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	me, err := s.store.MemberByUserIDTx(ctx, tx, id, userID)
	if err != nil {
		return nil, err
	}
	booking, err := mySettleBooking(ctx, tx, id, userID)
	if err != nil {
		return nil, err
	}
	papers, err := eventPapers(ctx, tx, userID, id)
	if err != nil {
		return nil, err
	}
	eff := shared.NewEffects()
	var debtIDs []uuid.UUID
	if req.Mode == "simplified" {
		if p.CompletedAt == nil {
			return nil, ErrSettleSimplifiedOff
		}
		if booking != nil {
			return nil, ErrSettleBookedOnce
		}
		if debtIDs, err = s.bookSimplifiedTx(ctx, tx, userID, me.ID, p, papers); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO event_settle_bookings (project_id, user_id, lines) VALUES ($1, $2, '[]'::jsonb)`,
			id, userID); err != nil {
			return nil, fmt.Errorf("settle booking: %w", err)
		}
	} else {
		if booking != nil {
			return nil, ErrSettleSimplifiedSet
		}
		if req.With == nil {
			return nil, ErrMemberNotFound
		}
		if debtIDs, err = s.bookPairsTx(ctx, tx, userID, me.ID, *req.With, p, papers, req.AccountID); err != nil {
			return nil, err
		}
	}
	if len(debtIDs) > 0 {
		debts, people, err := s.debts.DebtEffects(ctx, userID, debtIDs, nil)
		if err == nil {
			eff.Debts, eff.PersonBalances = debts, people
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	view, err := s.settleView(ctx, s.store.db, userID, p)
	if err != nil {
		return nil, err
	}
	return &SettleBookResult{Settle: view, Effects: eff}, nil
}

// bookPairsTx — every unbooked pair between me and other: bills into one lump debt per direction,
// paybacks as repayment rows.
func (s *Service) bookPairsTx(ctx context.Context, tx pgx.Tx, userID, me, other uuid.UUID, p *Project,
	papers map[uuid.UUID]shared.Paper, account *uuid.UUID) ([]uuid.UUID, error) {
	parts, err := personal_debts.MyPaperPartsTx(ctx, tx, userID, paperIDs(papers))
	if err != nil {
		return nil, err
	}
	booked := map[[2]uuid.UUID]bool{}
	for _, part := range parts {
		booked[[2]uuid.UUID{part.PaperID, part.PersonID}] = true
	}
	var (
		owe, owed []personal_debts.PaperPart
		person    *shared.PaperPerson
		ids       []uuid.UUID
	)
	myTxs, err := collectIDs(ctx, tx, `SELECT id FROM transactions WHERE user_id = $1 AND split_transaction_id = ANY($2)`,
		userID, paperIDs(papers))
	if err != nil {
		return nil, err
	}
	txOf := map[uuid.UUID]uuid.UUID{}
	if len(myTxs) > 0 {
		rows, err := tx.Query(ctx, `SELECT split_transaction_id, id FROM transactions WHERE id = ANY($1)`, myTxs)
		if err != nil {
			return nil, fmt.Errorf("my txs: %w", err)
		}
		for rows.Next() {
			var pid, tid uuid.UUID
			if err := rows.Scan(&pid, &tid); err != nil {
				rows.Close()
				return nil, err
			}
			txOf[pid] = tid
		}
		rows.Close()
	}
	for _, paper := range papers {
		mine, theirs := personOfMember(paper, me), personOfMember(paper, other)
		if mine == nil || theirs == nil || booked[[2]uuid.UUID{paper.ID, theirs.ID}] {
			continue
		}
		v := paper.NetOf(mine.ID)[theirs.ID] // + I owe them
		if math.Abs(v) < 0.005 {
			continue
		}
		person = theirs
		if paper.Type == shared.PaperPayback {
			dir := "received"
			if v < 0 {
				dir = "paid" // I paid them back
			}
			e, _, err := s.debts.CreateEntryTx(ctx, tx, userID, personal_debts.EntryInput{
				Kind: personal_debts.KindRepayment, Direction: dir, ContactID: theirs.Person.ContactID,
				PersonName: theirs.Person.Name, Amount: math.Abs(v), Date: paper.Date, Description: paper.Description,
				CreateTransaction: account != nil, AccountID: account, ProjectID: &p.ID,
			})
			if err != nil {
				return nil, err
			}
			if err := personal_debts.LinkPaperPartTx(ctx, tx, userID, e.ID, personal_debts.PaperPart{
				PaperID: paper.ID, PersonID: theirs.ID, Amount: math.Abs(v)}); err != nil {
				return nil, err
			}
			ids = append(ids, e.ID)
			continue
		}
		part := personal_debts.PaperPart{PaperID: paper.ID, PersonID: theirs.ID, Amount: math.Abs(v)}
		if t, ok := txOf[paper.ID]; ok {
			t := t
			part.TransactionID = &t
		}
		if v > 0 {
			owe = append(owe, part)
		} else {
			owed = append(owed, part)
		}
	}
	if person == nil {
		return nil, ErrSettleNothing
	}
	name := p.Name
	for _, side := range []struct {
		parts []personal_debts.PaperPart
		iOwe  bool
	}{{owe, true}, {owed, false}} {
		if len(side.parts) == 0 {
			continue
		}
		e, err := s.debts.CreatePaperDebtTx(ctx, tx, userID, personal_debts.PaperDebtInput{
			IOwe: side.iOwe, ContactID: person.Person.ContactID, PersonName: person.Person.Name,
			Date: time.Now().Format("2006-01-02"), Description: &name, ProjectID: &p.ID, Parts: side.parts,
		})
		if err != nil {
			return nil, err
		}
		ids = append(ids, e.ID)
	}
	return ids, nil
}

// bookSimplifiedTx — my simplified lines, minus what my books already hold with that member.
func (s *Service) bookSimplifiedTx(ctx context.Context, tx pgx.Tx, userID, me uuid.UUID, p *Project,
	papers map[uuid.UUID]shared.Paper) ([]uuid.UUID, error) {
	_, net := memberNets(papers)
	parts, err := personal_debts.MyPaperPartsTx(ctx, tx, userID, paperIDs(papers))
	if err != nil {
		return nil, err
	}
	refs, err := LoadMemberRefs(ctx, tx, userID, memberKeys(net))
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	name := p.Name
	for _, m := range simplifyNets(net) {
		if m.from != me && m.to != me {
			continue
		}
		other := m.to
		sign := -1.0 // I owe them
		if m.to == me {
			other, sign = m.from, 1.0
		}
		booked, _, _ := bookedWith(papers, parts, me, other)
		left := round2(sign*m.amount - booked)
		if math.Abs(left) < 0.005 {
			continue
		}
		ref := refs[other]
		var contact *uuid.UUID
		for _, paper := range papers {
			if pp := personOfMember(paper, other); pp != nil {
				contact = pp.Person.ContactID
				break
			}
		}
		dir := "paid"
		if left < 0 {
			dir = "received"
		}
		e, _, err := s.debts.CreateEntryTx(ctx, tx, userID, personal_debts.EntryInput{
			Kind: personal_debts.KindDebt, Direction: dir, ContactID: contact, PersonName: ref.Name,
			Amount: math.Abs(left), Date: time.Now().Format("2006-01-02"), Description: &name, ProjectID: &p.ID,
		})
		if err != nil {
			return nil, err
		}
		ids = append(ids, e.ID)
	}
	return ids, nil
}

func memberKeys(m map[uuid.UUID]float64) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func mySettleBooking(ctx context.Context, q shared.Querier, projectID, userID uuid.UUID) (*MySettleBooking, error) {
	rows, err := q.Query(ctx, `SELECT booked_at FROM event_settle_bookings WHERE project_id = $1 AND user_id = $2`,
		projectID, userID)
	if err != nil {
		return nil, fmt.Errorf("settle booking: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var b MySettleBooking
	if err := rows.Scan(&b.BookedAt); err != nil {
		return nil, err
	}
	return &b, nil
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
