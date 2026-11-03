package shared

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
)

// A paper is one shared bill (project_transactions): a split bill when ProjectID is nil, an event
// bill otherwise. It holds only the money facts; each user's own books (a transaction, debts) are
// made from it by claim and follow it one way (paper → books).

const (
	PaperExpense = "expense"
	PaperIncome  = "income"
	PaperPayback = "payback"

	SplitEqual  = "equal"
	SplitCustom = "custom"

	PersonInvited  = "invited"
	PersonJoined   = "joined"
	PersonDeclined = "declined"
	PersonLeft     = "left"
)

const paperEps = 0.005

type PaperPerson struct {
	ID       uuid.UUID  `json:"id"`
	Name     string     `json:"name"`
	UserID   *uuid.UUID `json:"-"`
	MemberID *uuid.UUID `json:"member_id"`
	// Person — this person as the viewer knows them: their contact (by the viewer's own binding or a
	// contact linked to the same user), else just the name.
	Person   PersonRef `json:"person"`
	IsMe     bool      `json:"is_me"`
	Status   string    `json:"status"`
	Share    float64   `json:"share"`
	Paid     float64   `json:"paid"`
	PaidBack bool      `json:"paid_back"`
	// PaidBackBy — who ticked "คืนแล้ว" (a name), nil when not ticked.
	PaidBackBy *string    `json:"paid_back_by"`
	PaidBackAt *time.Time `json:"paid_back_at"`
}

type Paper struct {
	ID          uuid.UUID     `json:"id"`
	ProjectID   *uuid.UUID    `json:"project_id"`
	OwnerUserID uuid.UUID     `json:"-"`
	IsOwner     bool          `json:"is_owner"`
	Type        string        `json:"type"`
	PaybackKind *string       `json:"payback_kind"`
	Amount      float64       `json:"amount"`
	Date        string        `json:"date"`
	Description *string       `json:"description"`
	Note        *string       `json:"note"`
	SplitMode   string        `json:"split_mode"`
	Version     int           `json:"version"`
	UpdatedAt   time.Time     `json:"updated_at"`
	UpdatedBy   *string       `json:"updated_by"`
	People      []PaperPerson `json:"people"`
}

// Person — the paper person with this id.
func (p Paper) Person(id uuid.UUID) *PaperPerson {
	for i := range p.People {
		if p.People[i].ID == id {
			return &p.People[i]
		}
	}
	return nil
}

// PersonOfUser — the user's row on the paper (any status), nil when they are not on it.
func (p Paper) PersonOfUser(userID uuid.UUID) *PaperPerson {
	for i := range p.People {
		if u := p.People[i].UserID; u != nil && *u == userID {
			return &p.People[i]
		}
	}
	return nil
}

// PaperPair — debtor owes creditor amount on this paper.
type PaperPair struct {
	Debtor, Creditor uuid.UUID
	Amount           float64
}

// Pairs — who owes whom (the bill_pairs view, in Go): everyone with a share owes each payer
// share × paid / amount; income is flipped (the receiver owes the others).
func (p Paper) Pairs() []PaperPair {
	out := []PaperPair{}
	if p.Amount <= 0 {
		return out
	}
	for _, pa := range p.People {
		if pa.Paid <= 0 {
			continue
		}
		for _, s := range p.People {
			if s.ID == pa.ID || s.Share <= 0 {
				continue
			}
			amt := round2(s.Share * pa.Paid / p.Amount)
			if amt <= 0 {
				continue
			}
			if p.Type == PaperIncome {
				out = append(out, PaperPair{Debtor: pa.ID, Creditor: s.ID, Amount: amt})
			} else {
				out = append(out, PaperPair{Debtor: s.ID, Creditor: pa.ID, Amount: amt})
			}
		}
	}
	return out
}

// NetOf — what person me owes each other person net on this paper (> 0 = me owes them, < 0 = they
// owe me), zero nets left out.
func (p Paper) NetOf(me uuid.UUID) map[uuid.UUID]float64 {
	net := map[uuid.UUID]float64{}
	for _, pr := range p.Pairs() {
		switch me {
		case pr.Debtor:
			net[pr.Creditor] += pr.Amount
		case pr.Creditor:
			net[pr.Debtor] -= pr.Amount
		}
	}
	for k, v := range net {
		if math.Abs(v) < paperEps {
			delete(net, k)
		} else {
			net[k] = round2(v)
		}
	}
	return net
}

// PlanDebt — one person-book row a claim writes: IOwe = I owe them (else they owe me).
type PlanDebt struct {
	PersonID uuid.UUID
	Amount   float64
	IOwe     bool
}

// BookPlan — what my books hold for this paper.
type BookPlan struct {
	// Tx — false for a payback paper (its claim is a repayment row, not a transaction).
	Tx        bool
	TxType    string
	Amount    float64
	MyShare   float64
	UseWallet bool
	Debts     []PlanDebt
	// Repayments — a payback paper: I paid them back (IOwe) or they paid me back.
	Repayments []PlanDebt
}

// PlanFor — my books for this paper. payDebt = the claim took the debt branch (I owe the payers
// rather than having settled from my wallet). An event bill gets debts only when eventDebts (the
// claim asked for this bill's debts now); otherwise the event settles as a lump from the summary,
// which skips the pairs already booked.
func (p Paper) PlanFor(me uuid.UUID, payDebt, eventDebts bool) (BookPlan, bool) {
	person := p.Person(me)
	if person == nil {
		return BookPlan{}, false
	}
	net := p.NetOf(me)
	ids := make([]uuid.UUID, 0, len(net))
	for k := range net {
		ids = append(ids, k)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	if p.Type == PaperPayback {
		plan := BookPlan{}
		for _, id := range ids {
			// on a payback the giver paid; net says the receiver "owes" the giver: the receiver got money back.
			if net[id] < 0 {
				plan.Repayments = append(plan.Repayments, PlanDebt{PersonID: id, Amount: -net[id], IOwe: true})
			} else {
				plan.Repayments = append(plan.Repayments, PlanDebt{PersonID: id, Amount: net[id]})
			}
		}
		return plan, len(plan.Repayments) > 0
	}
	if person.Share <= 0 && person.Paid <= 0 {
		return BookPlan{}, false
	}
	owe := 0.0
	for _, id := range ids {
		if net[id] > 0 {
			owe += net[id]
		}
	}
	plan := BookPlan{Tx: true, TxType: p.Type, MyShare: round2(person.Share)}
	event := p.ProjectID != nil
	switch {
	case person.Paid > paperEps && (event || payDebt || owe < paperEps):
		plan.Amount, plan.UseWallet = round2(person.Paid), true
	case person.Paid > paperEps:
		plan.Amount, plan.UseWallet = round2(person.Paid+owe), true
	case payDebt:
		plan.Amount = round2(person.Share)
	default:
		// Settled from my wallet ("คืน X แล้ว"), a split or an event bill.
		plan.Amount, plan.UseWallet = round2(person.Share), true
	}
	if !event || eventDebts {
		for _, id := range ids {
			v := net[id]
			if v > 0 && !payDebt {
				continue // settled from my wallet
			}
			plan.Debts = append(plan.Debts, PlanDebt{PersonID: id, Amount: math.Abs(v), IOwe: v > 0})
		}
	}
	return plan, true
}

// Balanced — Σ paid and Σ share both equal the amount.
func (p Paper) Balanced() bool {
	var paid, share float64
	for _, x := range p.People {
		paid += x.Paid
		share += x.Share
	}
	return math.Abs(paid-p.Amount) < paperEps && math.Abs(share-p.Amount) < paperEps
}

// EqualShares — amount split over n people; the cents left over go to index lead.
func EqualShares(amount float64, n, lead int) []float64 {
	if n <= 0 {
		return nil
	}
	cents := int64(math.Round(amount * 100))
	each := cents / int64(n)
	out := make([]float64, n)
	for i := range out {
		out[i] = float64(each) / 100
	}
	if lead < 0 || lead >= n {
		lead = 0
	}
	out[lead] = float64(each+cents-each*int64(n)) / 100
	return out
}

// LoadPapers — papers by id as viewer sees them (people with the viewer's contacts).
func LoadPapers(ctx context.Context, q Querier, viewer uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]Paper, error) {
	out := map[uuid.UUID]Paper{}
	ids = uniqueIDs(ids)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
		SELECT b.id, b.project_id, b.owner_user_id, b.type, b.payback_kind, b.amount::float8,
		       to_char(b.date, 'YYYY-MM-DD'), b.description, b.note, b.split_mode, b.version, b.updated_at,
		       uu.display_name
		FROM project_transactions b LEFT JOIN users uu ON uu.id = b.updated_by_user_id
		WHERE b.id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("papers: %w", err)
	}
	for rows.Next() {
		var p Paper
		if err := rows.Scan(&p.ID, &p.ProjectID, &p.OwnerUserID, &p.Type, &p.PaybackKind, &p.Amount, &p.Date,
			&p.Description, &p.Note, &p.SplitMode, &p.Version, &p.UpdatedAt, &p.UpdatedBy); err != nil {
			rows.Close()
			return nil, err
		}
		p.IsOwner = p.OwnerUserID == viewer
		p.People = []PaperPerson{}
		out[p.ID] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	prow, err := q.Query(ctx, `
		SELECT sp.split_transaction_id, sp.id, sp.name, sp.user_id, sp.project_member_id, sp.status,
		       sp.share::float8, sp.paid::float8, sp.paid_back, pb.display_name, sp.paid_back_at,
		       COALESCE(spc.contact_id, pmc.contact_id,
		         (SELECT c.id FROM contacts c WHERE c.user_id = $2 AND c.linked_user_id = sp.user_id
		            AND c.status = 'active' ORDER BY c.created_at LIMIT 1))
		FROM split_transaction_people sp
		LEFT JOIN users pb ON pb.id = sp.paid_back_by_user_id
		LEFT JOIN split_person_contacts spc ON spc.split_person_id = sp.id AND spc.user_id = $2
		LEFT JOIN project_member_contacts pmc ON pmc.project_member_id = sp.project_member_id AND pmc.user_id = $2
		WHERE sp.split_transaction_id = ANY($1)
		ORDER BY sp.split_transaction_id, sp.sort, sp.created_at, sp.id`, ids, viewer)
	if err != nil {
		return nil, fmt.Errorf("paper people: %w", err)
	}
	var contactIDs []uuid.UUID
	type ref struct {
		paper   uuid.UUID
		contact *uuid.UUID
	}
	var refs []ref
	for prow.Next() {
		var (
			paperID uuid.UUID
			x       PaperPerson
			contact *uuid.UUID
		)
		if err := prow.Scan(&paperID, &x.ID, &x.Name, &x.UserID, &x.MemberID, &x.Status, &x.Share, &x.Paid,
			&x.PaidBack, &x.PaidBackBy, &x.PaidBackAt, &contact); err != nil {
			prow.Close()
			return nil, err
		}
		x.IsMe = x.UserID != nil && *x.UserID == viewer
		if !x.PaidBack {
			x.PaidBackBy, x.PaidBackAt = nil, nil
		}
		p := out[paperID]
		p.People = append(p.People, x)
		out[paperID] = p
		refs = append(refs, ref{paper: paperID, contact: contact})
		if contact != nil {
			contactIDs = append(contactIDs, *contact)
		}
	}
	prow.Close()
	if err := prow.Err(); err != nil {
		return nil, err
	}
	persons, err := LoadPersons(ctx, q, viewer, contactIDs)
	if err != nil {
		return nil, err
	}
	idx := map[uuid.UUID]int{}
	for _, r := range refs {
		p := out[r.paper]
		i := idx[r.paper]
		idx[r.paper] = i + 1
		x := &p.People[i]
		if r.contact != nil {
			if pr, ok := persons[*r.contact]; ok {
				x.Person = pr
				continue
			}
		}
		x.Person = NamePerson(x.Name)
	}
	return out, nil
}

// LoadPaper — one paper (ok = false when it does not exist).
func LoadPaper(ctx context.Context, q Querier, viewer, id uuid.UUID) (Paper, bool, error) {
	m, err := LoadPapers(ctx, q, viewer, []uuid.UUID{id})
	if err != nil {
		return Paper{}, false, err
	}
	p, ok := m[id]
	return p, ok, nil
}

// TxInSync — my transaction holds what the paper says now (either claim branch).
func (p Paper) TxInSync(userID uuid.UUID, amount float64, myShare *float64, date string, description *string) bool {
	person := p.PersonOfUser(userID)
	if person == nil {
		return false
	}
	if p.Date != date || TextOr(CleanText(p.Description)) != TextOr(CleanText(description)) {
		return false
	}
	share := amount
	if myShare != nil {
		share = *myShare
	}
	for _, payDebt := range []bool{true, false} {
		plan, ok := p.PlanFor(person.ID, payDebt, false)
		if ok && plan.Tx && math.Abs(plan.Amount-amount) < paperEps && math.Abs(plan.MyShare-share) < paperEps {
			return true
		}
	}
	return false
}

// BookRef — the paper as my transaction shows it.
func (p Paper) BookRef(userID uuid.UUID, amount float64, myShare *float64, date string, description *string) PaperBookRef {
	ref := PaperBookRef{ID: p.ID, Type: p.Type, Amount: p.Amount, Description: p.Description, Date: p.Date,
		IsOwner: p.OwnerUserID == userID, InSync: p.TxInSync(userID, amount, myShare, date, description)}
	if me := p.PersonOfUser(userID); me != nil {
		ref.MyShare = me.Share
	}
	return ref
}

// PaperBookRef — what a transaction / debt says about the paper it follows.
type PaperBookRef struct {
	ID          uuid.UUID `json:"id"`
	Type        string    `json:"type"`
	Amount      float64   `json:"amount"`
	Description *string   `json:"description"`
	Date        string    `json:"date"`
	MyShare     float64   `json:"my_share"`
	// InSync — my book holds what the paper says now (amount, my share, date, description).
	InSync  bool       `json:"in_sync"`
	Event   *EventRef  `json:"event,omitempty"`
	IsOwner bool       `json:"is_owner"`
	Owner   *uuid.UUID `json:"-"`
}
