package projects

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// The bill sheet's save, in ONE call and ONE DB transaction (0.4.1-1 E8): the bill (or payback),
// the payback rows of the people marked "คืนแล้ว", and every part of MY book. It replaces the app's
// chain of addBills / createProjectTransaction + resolvePeople + my-books + debt entries + repayments
// + transactions, which left half-booked rows when a call failed halfway. E2–E5 are fixed here:
//   - E2: "คืนแล้ว" when I paid = the debt + an immediate repayment (their balance stays 0);
//   - E3: me "คืนแล้ว" when someone else paid = my share from my wallet, NO debt;
//   - E4: someone else paid, I did not pay back = my share with NO wallet + the debt, never both;
//   - E5: a payback's default "จดหนี้" looks at THIS event's debt with that person only;
//   - E6: me the payer booking later = my tx becomes the bill's origin (the whole bill), not a copy.

var (
	ErrBookBillTarget = errors.New("project_id or new_event: exactly one")
	ErrBookBillKind   = errors.New("bill.kind can't change")
	ErrBookBillInput  = errors.New("invalid bill")
)

// WithDebts wires the person book (E8 writes debts and repayments through it).
func (s *Service) WithDebts(d *personal_debts.Service) { s.debts = d }

type EventBillPerson struct {
	BillMember
	// PaidBack — "คืนแล้ว": paid the payer back on the spot.
	PaidBack bool `json:"paid_back"`
}

type EventBillInput struct {
	Kind        string            `json:"kind"`
	Amount      float64           `json:"amount"`
	Date        string            `json:"date"`
	Description *string           `json:"description"`
	Note        *string           `json:"note"`
	TagIDs      *[]uuid.UUID      `json:"tag_ids"`
	Payer       *BillPerson       `json:"payer"`
	Members     []EventBillPerson `json:"members"`
	From        *BillPerson       `json:"from"`
	To          *BillPerson       `json:"to"`
	// PaybackKind (0.4.1-2 V11) — kind payback only: payback (default) | loan.
	PaybackKind *string `json:"payback_kind"`
	// Payers (multi-payer, owner 2026-10-28) — expense / income: who paid what (Σ = amount); never
	// with payer.
	Payers []BillPayerInput `json:"payers"`
}

// BillPayerInput — a payer of a bill (a bill person) and what they paid.
type BillPayerInput struct {
	BillPerson
	Amount float64 `json:"amount"`
}

// ErrPayersSum — the payers don't add up to the bill (400 PAYERS_SUM).
var ErrPayersSum = errors.New("payers must add up to the bill")

type EventBillBooks struct {
	On         bool        `json:"on"`
	Tx         bool        `json:"tx"`
	Debt       *bool       `json:"debt"`
	AccountID  *uuid.UUID  `json:"account_id"`
	CategoryID *uuid.UUID  `json:"category_id"`
	TagIDs     []uuid.UUID `json:"tag_ids"`
	// PrivateNote (0.4.1-2 S11) — my private note on my copy (absent = unchanged, null / "" = clear).
	PrivateNote *string `json:"private_note"`

	privateNoteSet bool
}

func (b *EventBillBooks) UnmarshalJSON(data []byte) error {
	type alias EventBillBooks
	present, err := shared.DecodeTracked(data, (*alias)(b))
	b.privateNoteSet = present["private_note"]
	return err
}

type NewEventInput struct {
	Name     string           `json:"name"`
	IconCode *shared.IconCode `json:"icon_code"`
}

type EventBillBookRequest struct {
	// Split (0.4.1-2 S1 / S2) — a split bill (no event): exactly one of split / project_id / new_event.
	Split     bool           `json:"split"`
	ProjectID *uuid.UUID     `json:"project_id"`
	NewEvent  *NewEventInput `json:"new_event"`
	Bill      EventBillInput `json:"bill"`
	MyBooks   EventBillBooks `json:"my_books"`
}

type EventBillRebookRequest struct {
	Bill    *EventBillInput `json:"bill"`
	MyBooks EventBillBooks  `json:"my_books"`
	// ApplyToMyTx — ignored since 0.4.1-2 (my copy always follows the bill I save).
	ApplyToMyTx bool `json:"apply_to_my_tx"`
	// ExpectedUpdatedAt (0.4.1-2 S14) — the bill's updated_at as the form loaded it; changed since →
	// 409 BILL_CHANGED.
	ExpectedUpdatedAt *time.Time     `json:"expected_updated_at"`
	Move              *EventBillMove `json:"move"`
}

// EventBillMove (0.4.1-2 S17) — move a bill to another event / into a split bill (exactly one).
type EventBillMove struct {
	ProjectID *uuid.UUID     `json:"project_id"`
	NewEvent  *NewEventInput `json:"new_event"`
	Split     bool           `json:"split"`
}

type EventBillBookResult struct {
	EventPatch
	MyBooks       MyBooks        `json:"my_books"`
	Paybacks      []BoardRow     `json:"paybacks"`
	TransactionID *uuid.UUID     `json:"transaction_id"`
	Effects       shared.Effects `json:"effects"`
}

type EventBillBookPreview struct {
	Preview     bool                       `json:"preview"`
	People      []transactions.PersonDelta `json:"people"`
	Wallets     []transactions.WalletDelta `json:"wallets"`
	DebtDefault bool                       `json:"debt_default"`
}

// booking — one save's state while it runs in its DB transaction.
type booking struct {
	s       *Service
	tx      pgx.Tx
	me      uuid.UUID // the caller (user)
	project *Project
	kind    string    // event | split (0.4.1-2 S1)
	meMem   uuid.UUID // my member row
	mb      *quickMemberBuilder

	rowID        uuid.UUID
	paybacks     []uuid.UUID
	myTx         *uuid.UUID
	keys         map[string]shared.PersonRef // people whose balance my book moved
	beforeKey    func(key string) error      // remembers a person's balance before the first write (preview)
	beforeWallet func(id uuid.UUID) error    // the same for a wallet
	wallets      []uuid.UUID
	effects      shared.Effects
	debtDflt     bool
	copyDebts    []uuid.UUID // my person-book rows the copy wrote / kept (effects, read after commit)
	copied       bool        // my copy went through applyCopyTx
	moved        bool        // S17: the bill moved to b.project
}

func validBillInput(b *EventBillInput) error {
	switch b.Kind {
	case PTTypeExpense, PTTypeIncome, PTTypePayback:
	default:
		return fmt.Errorf("%w: bill.kind must be expense, income or payback", ErrBookBillInput)
	}
	if b.Amount <= 0 || math.IsNaN(b.Amount) {
		return fmt.Errorf("%w: bill.amount must be > 0", ErrBookBillInput)
	}
	if b.Description != nil && len([]rune(*b.Description)) > 200 || b.Note != nil && len([]rune(*b.Note)) > 500 {
		return ErrBookBillInput
	}
	if b.PaybackKind != nil && (b.Kind != PTTypePayback || (*b.PaybackKind != "payback" && *b.PaybackKind != "loan")) {
		return fmt.Errorf("%w: payback_kind is payback or loan, on a payback only", ErrBookBillInput)
	}
	if b.Kind == PTTypePayback {
		if b.From == nil || b.To == nil || b.From.Empty() || b.To.Empty() {
			return ErrBadPayback
		}
		return nil
	}
	if b.From != nil || b.To != nil {
		return ErrBadBillPeople
	}
	for _, m := range b.Members {
		if m.Empty() || m.Share < 0 {
			return ErrBadBillPeople
		}
	}
	return nil
}

// BookBill — POST /event-bills.
func (s *Service) BookBill(ctx context.Context, userID uuid.UUID, req EventBillBookRequest, preview bool) (any, error) {
	targets := 0
	for _, set := range []bool{req.ProjectID != nil, req.NewEvent != nil, req.Split} {
		if set {
			targets++
		}
	}
	if targets != 1 {
		return nil, ErrBookBillTarget
	}
	if err := validBillInput(&req.Bill); err != nil {
		return nil, err
	}
	if req.NewEvent != nil && (strings.TrimSpace(req.NewEvent.Name) == "" || len([]rune(req.NewEvent.Name)) > 100) {
		return nil, ErrBookBillInput
	}
	return s.runBooking(ctx, userID, preview, func(b *booking) error {
		switch {
		case req.Split:
			if err := b.newSplit(ctx, req.Bill); err != nil {
				return err
			}
			req.Bill.TagIDs = nil // a split bill has no event tags
		case req.NewEvent != nil:
			if err := b.newEvent(ctx, *req.NewEvent); err != nil {
				return err
			}
		default:
			if err := b.open(ctx, *req.ProjectID, "create_pt"); err != nil {
				return err
			}
			if b.kind != KindEvent {
				return ErrProjectNotFound
			}
		}
		return b.writeNew(ctx, req.Bill, req.MyBooks)
	})
}

// RebookBill — PUT /event-bills/{pt_id}.
func (s *Service) RebookBill(ctx context.Context, userID, ptID uuid.UUID, req EventBillRebookRequest, preview bool) (any, error) {
	if req.Bill != nil {
		if err := validBillInput(req.Bill); err != nil {
			return nil, err
		}
	}
	current, err := s.store.GetPT(ctx, ptID)
	if err != nil {
		return nil, err
	}
	if current.ParentProjectTransactionID != nil {
		return nil, ErrPTIsChild
	}
	if req.Bill != nil && (req.Bill.Kind == PTTypePayback) != (current.Type == PTTypePayback) {
		return nil, ErrBookBillKind
	}
	out, err := s.runBooking(ctx, userID, preview, func(b *booking) error {
		op := "resolve"
		if req.Bill != nil {
			op = "edit_pt"
		}
		if err := b.open(ctx, current.ProjectID, op); err != nil {
			return err
		}
		return b.writeEdit(ctx, current, req)
	})
	if errors.Is(err, ErrBillChanged) { // S14: the form gets the bill as it is now
		now, verr := s.billView(ctx, userID, current)
		if verr != nil {
			return nil, verr
		}
		return nil, &BillChangedError{Bill: now}
	}
	return out, err
}

// runBooking runs fn in one DB transaction: a preview rolls it back and answers the deltas.
func (s *Service) runBooking(ctx context.Context, userID uuid.UUID, preview bool, fn func(*booking) error) (any, error) {
	if s.txs == nil || s.debts == nil {
		return nil, errors.New("transactions / debts module not wired")
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	b := &booking{s: s, tx: tx, me: userID, keys: map[string]shared.PersonRef{}, effects: shared.NewEffects()}
	before := map[string]float64{}
	b.beforeKey = func(key string) error {
		if _, ok := before[key]; ok {
			return nil
		}
		v, err := personNetTx(ctx, tx, userID, key)
		before[key] = v
		return err
	}
	walletsBefore := map[uuid.UUID]float64{}
	b.beforeWallet = func(id uuid.UUID) error {
		if _, ok := walletsBefore[id]; ok {
			return nil
		}
		bs, err := shared.LoadWalletBalances(ctx, tx, []uuid.UUID{id})
		if err == nil && len(bs) == 1 {
			walletsBefore[id] = bs[0].Balance
		}
		return err
	}
	if err := fn(b); err != nil {
		return nil, err
	}
	if preview {
		out := &EventBillBookPreview{Preview: true, People: []transactions.PersonDelta{}, Wallets: []transactions.WalletDelta{},
			DebtDefault: b.debtDflt}
		for key, ref := range b.keys {
			after, err := personNetTx(ctx, tx, userID, key)
			if err != nil {
				return nil, err
			}
			out.People = append(out.People, transactions.NewPersonDelta(key, ref, before[key], after))
		}
		for id, was := range walletsBefore {
			bs, err := shared.LoadWalletBalances(ctx, tx, []uuid.UUID{id})
			if err != nil {
				return nil, err
			}
			if len(bs) == 1 {
				out.Wallets = append(out.Wallets, transactions.WalletDelta{AccountID: id, Before: was, After: bs[0].Balance,
					Delta: math.Round((bs[0].Balance-was)*100) / 100})
			}
		}
		return out, nil // the deferred rollback drops everything
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return b.result(ctx)
}

// personNetTx — my balance with a person (Σ signed person-book rows), read through tx.
func personNetTx(ctx context.Context, q pgx.Tx, userID uuid.UUID, key string) (float64, error) {
	var v float64
	err := q.QueryRow(ctx, `SELECT COALESCE(SUM(l.signed), 0)::float8 FROM (`+shared.DebtLedgerSQL("$1")+`) l
		WHERE l.key = $2`, userID, key).Scan(&v)
	return v, err
}

func (b *booking) open(ctx context.Context, projectID uuid.UUID, op string) error {
	if err := b.s.store.AssertWritable(ctx, projectID, op); err != nil {
		return err
	}
	p, me, mb, err := b.s.billWriterTx(ctx, b.tx, b.me, projectID)
	if err != nil {
		return err
	}
	b.project, b.meMem, b.mb = p, me.ID, mb
	if err := b.tx.QueryRow(ctx, `SELECT kind FROM projects WHERE id = $1`, projectID).Scan(&b.kind); err != nil {
		return fmt.Errorf("event kind: %w", err)
	}
	return nil
}

// newSplit — a split bill (0.4.1-2 S1 / S2): its hidden one-bill "event" (kind split, never listed),
// owned by me; its people join as members as the bill resolves them (linked ones active at once).
func (b *booking) newSplit(ctx context.Context, in EventBillInput) error {
	name := ""
	if d := shared.CleanText(in.Description); d != nil {
		name = *d
		if len([]rune(name)) > 100 {
			name = string([]rune(name)[:100])
		}
	}
	if err := b.newEvent(ctx, NewEventInput{Name: name}); err != nil {
		return err
	}
	if _, err := b.tx.Exec(ctx, `UPDATE projects SET kind = $2 WHERE id = $1`, b.project.ID, KindSplit); err != nil {
		return fmt.Errorf("split bill: %w", err)
	}
	b.kind = KindSplit
	return nil
}

func (b *booking) newEvent(ctx context.Context, in NewEventInput) error {
	p, err := b.s.createTx(ctx, b.tx, b.me, CreateProjectRequest{Name: strings.TrimSpace(in.Name), IconCode: in.IconCode})
	if err != nil {
		return err
	}
	owner, err := b.s.store.MemberByUserIDTx(ctx, b.tx, p.ID, b.me)
	if err != nil {
		return err
	}
	b.project, b.meMem, b.kind = p, owner.ID, KindEvent
	b.mb = &quickMemberBuilder{svc: b.s, tx: b.tx, projectID: p.ID, callerUserID: b.me,
		byUser: map[uuid.UUID]uuid.UUID{b.me: owner.ID}, byContact: map[uuid.UUID]uuid.UUID{}, byName: map[string]uuid.UUID{}}
	return nil
}

// person — who a member is in MY book: my contact for them, else their name.
func (b *booking) person(ctx context.Context, memberID uuid.UUID) (*uuid.UUID, string, error) {
	var (
		cid  *uuid.UUID
		name string
	)
	err := b.tx.QueryRow(ctx, `SELECT mc.id, COALESCE(u.display_name, pm.display_name)
		FROM project_members pm LEFT JOIN users u ON u.id = pm.user_id
		LEFT JOIN LATERAL (
			SELECT c.id FROM contacts c
			WHERE c.user_id = $2 AND ((pm.user_id IS NOT NULL AND c.linked_user_id = pm.user_id)
			   OR (pm.user_id IS NULL AND LOWER(BTRIM(c.display_name)) = LOWER(BTRIM(pm.display_name))))
			ORDER BY (c.status = 'active') DESC, c.created_at, c.id LIMIT 1) mc ON TRUE
		WHERE pm.id = $1`, memberID, b.me).Scan(&cid, &name)
	if err != nil {
		return nil, "", fmt.Errorf("bill person: %w", err)
	}
	return cid, name, nil
}

// entry writes one person-book row of the bill (a debt or a repayment) and notes what it moved.
func (b *booking) entry(ctx context.Context, memberID, billID uuid.UUID, kind, direction string, amount float64,
	date string, desc, note *string, account *uuid.UUID) error {
	cid, name, err := b.person(ctx, memberID)
	if err != nil {
		return err
	}
	key := shared.DebtKey(cid, name)
	if err := b.beforeKey(key); err != nil {
		return err
	}
	in := personal_debts.EntryInput{Kind: kind, Direction: direction, ContactID: cid, PersonName: name, Amount: round2(amount),
		Date: date, Description: desc, Note: note, BillID: &billID, EventID: &b.project.ID}
	if account != nil {
		if err := b.beforeWallet(*account); err != nil {
			return err
		}
		in.CreateTransaction, in.AccountID = true, account
		b.wallets = append(b.wallets, *account)
	}
	_, eff, err := b.s.debts.CreateEntryTx(ctx, b.tx, b.me, in)
	if err != nil {
		return err
	}
	b.keys[key] = shared.PersonRef{Name: name, ContactID: cid}
	b.effects.Merge(eff)
	return nil
}

// myTxFor makes my transaction for a bill: amount on account (nil = no wallet), linked to source.
func (b *booking) myTxFor(ctx context.Context, typ string, amount float64, bill EventBillInput, books EventBillBooks,
	account *uuid.UUID, source uuid.UUID) (uuid.UUID, error) {
	if account != nil {
		if err := b.beforeWallet(*account); err != nil {
			return uuid.Nil, err
		}
		b.wallets = append(b.wallets, *account)
	}
	created, err := b.s.txs.CreateInTx(ctx, b.tx, b.me, transactions.CreateRequest{
		Type: transactions.TxType(typ), Amount: round2(amount), Date: bill.Date, AccountID: account,
		CategoryID: books.CategoryID, Description: bill.Description, Note: bill.Note, TagIDs: books.TagIDs,
		SourceProjectTransactionID: &source,
	})
	if err != nil {
		return uuid.Nil, err
	}
	d, ok := created.(*transactions.TransactionDetail)
	if !ok {
		return uuid.Nil, ErrQuickTxNotBillable
	}
	b.myTx = &d.ID
	return d.ID, nil
}

// asOrigin makes my transaction the bill's origin (I paid it): the bill links to it (E6).
func (b *booking) asOrigin(ctx context.Context, billID, txID uuid.UUID) error {
	if _, err := b.tx.Exec(ctx, `UPDATE project_transactions SET linked_transaction_id = $2, unlinked_transaction_id = NULL
		WHERE id = $1`, billID, txID); err != nil {
		return fmt.Errorf("bill origin: %w", err)
	}
	_, err := b.tx.Exec(ctx, `UPDATE transactions SET project_id = $2, source_project_transaction_id = $3 WHERE id = $1`,
		txID, b.project.ID, billID)
	return err
}

// mySplitRow — my split row of a bill (a copy of my share points at it).
func (b *booking) mySplitRow(ctx context.Context, billID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := b.tx.QueryRow(ctx, `SELECT id FROM project_transactions WHERE parent_project_transaction_id = $1
		AND transaction_member_id = $2 LIMIT 1`, billID, b.meMem).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return billID, nil
	}
	return id, err
}

// payback adds a board payback row: from paid to back amount; forBill = the bill it was ticked
// "คืนแล้ว" on (nil: a payback of its own).
func (b *booking) payback(ctx context.Context, from, to uuid.UUID, amount float64, date string, forBill *uuid.UUID) (uuid.UUID, error) {
	pt, err := b.s.store.InsertPTTx(ctx, b.tx, b.project.ID, b.me, CreateProjectTransactionRequest{
		TransactionMemberID: from, Type: PTTypePayback, Amount: round2(amount), Date: date,
		Splits: []ProjectSplitInput{{MemberID: to, Amount: round2(amount)}},
	})
	if err != nil {
		return uuid.Nil, err
	}
	if forBill != nil {
		if _, err := b.tx.Exec(ctx, `UPDATE project_transactions SET paid_back_for_id = $2 WHERE id = $1`, pt.ID, *forBill); err != nil {
			return uuid.Nil, fmt.Errorf("payback of bill: %w", err)
		}
	}
	b.paybacks = append(b.paybacks, pt.ID)
	return pt.ID, nil
}

// resolvedBill — a bill input with its people as event members.
type resolvedBill struct {
	payer    uuid.UUID             // the main (biggest) payer
	payers   map[uuid.UUID]float64 // multi-payer: what each paid (one payer = the bill)
	splits   []ProjectSplitInput
	shares   map[uuid.UUID]float64 // every member's share, the payer's = the rest
	paidBack map[uuid.UUID]bool
	from, to uuid.UUID // payback
}

func (b *booking) resolve(ctx context.Context, in EventBillInput, fixedPayer *uuid.UUID) (*resolvedBill, error) {
	r := &resolvedBill{shares: map[uuid.UUID]float64{}, paidBack: map[uuid.UUID]bool{}}
	if in.Kind == PTTypePayback {
		var err error
		if r.from, err = b.mb.memberForPerson(ctx, *in.From, b.meMem); err != nil {
			return nil, err
		}
		if r.to, err = b.mb.memberForPerson(ctx, *in.To, b.meMem); err != nil {
			return nil, err
		}
		if r.from == r.to {
			return nil, ErrBadPayback
		}
		r.splits = []ProjectSplitInput{{MemberID: r.to, Amount: in.Amount}}
		return r, nil
	}
	r.payers = map[uuid.UUID]float64{}
	switch {
	case len(in.Payers) > 0: // multi-payer (owner 2026-10-28)
		if in.Payer != nil {
			return nil, fmt.Errorf("%w: payer or payers, not both", ErrBookBillInput)
		}
		var total float64
		for _, p := range in.Payers {
			if p.Amount <= 0 || math.IsNaN(p.Amount) {
				return nil, fmt.Errorf("%w: a payer pays > 0", ErrBookBillInput)
			}
			id, err := b.mb.memberForPerson(ctx, p.BillPerson, b.meMem)
			if err != nil {
				return nil, err
			}
			if _, dup := r.payers[id]; dup {
				return nil, ErrBadBillPeople
			}
			r.payers[id] = round2(p.Amount)
			total += p.Amount
		}
		if math.Abs(total-in.Amount) > 0.005 {
			return nil, ErrPayersSum
		}
		var best float64
		for id, a := range r.payers {
			if a > best || (a == best && id.String() < r.payer.String()) {
				r.payer, best = id, a
			}
		}
	case fixedPayer != nil: // an edit that sent no payer: the payers stay
		r.payer = *fixedPayer
		current, err := BillPayersTx(ctx, b.tx, b.rowID)
		if err != nil {
			return nil, err
		}
		var total float64
		for _, p := range current {
			r.payers[p.Creditor] = p.Amount
			total += p.Amount
		}
		if len(current) <= 1 {
			r.payers = map[uuid.UUID]float64{r.payer: in.Amount}
		} else if math.Abs(total-in.Amount) > 0.005 {
			return nil, ErrPayersSum
		}
	default:
		payer := BillPerson{Me: true}
		if in.Payer != nil {
			payer = *in.Payer
		}
		id, err := b.mb.memberForPerson(ctx, payer, b.meMem)
		if err != nil {
			return nil, err
		}
		r.payer = id
		r.payers[id] = in.Amount
	}
	// 0.4.1-2 Q-E3: someone else may receive an income bill too ("รับแทน").
	var sum float64
	for _, m := range in.Members {
		id, err := b.mb.memberForPerson(ctx, m.BillPerson, b.meMem)
		if err != nil {
			return nil, err
		}
		if _, dup := r.shares[id]; dup {
			return nil, ErrBadBillPeople
		}
		r.shares[id] = 0
		if id == r.payer {
			continue
		}
		r.paidBack[id] = m.PaidBack
		if m.Share > 0.005 {
			r.shares[id] = round2(m.Share)
			r.splits = append(r.splits, ProjectSplitInput{MemberID: id, Amount: round2(m.Share)})
			sum += m.Share
		}
	}
	if sum-in.Amount > 0.005 {
		return nil, ErrSplitsExceedParent
	}
	r.shares[r.payer] = round2(in.Amount - sum)
	return r, nil
}

// writeNew — a new bill / payback and my book.
func (b *booking) writeNew(ctx context.Context, in EventBillInput, books EventBillBooks) error {
	r, err := b.resolve(ctx, in, nil)
	if err != nil {
		return err
	}
	payer := r.payer
	if in.Kind == PTTypePayback {
		payer = r.from
	}
	req := CreateProjectTransactionRequest{TransactionMemberID: payer, Type: in.Kind, Amount: in.Amount, Date: in.Date,
		Description: in.Description, Note: in.Note, Splits: r.splits}
	if in.TagIDs != nil && in.Kind != PTTypePayback {
		req.TagIDs = *in.TagIDs
	} else if in.Kind != PTTypePayback && payer == b.meMem && books.On && books.Tx {
		if req.TagNames, err = personalTagNamesByIDTx(ctx, b.tx, b.me, books.TagIDs); err != nil {
			return err
		}
	}
	pt, err := b.s.store.InsertPTTx(ctx, b.tx, b.project.ID, b.me, req)
	if err != nil {
		return err
	}
	b.rowID = pt.ID
	if err := b.setPaybackKind(ctx, in); err != nil {
		return err
	}
	if in.Kind != PTTypePayback {
		if err := SetBillPayersTx(ctx, b.tx, b.rowID, r.payers); err != nil {
			return err
		}
	}
	if err := b.paidBackRows(ctx, in, r, books); err != nil {
		return err
	}
	if err := b.books(ctx, in, r, books, MyBooks{}); err != nil {
		return err
	}
	return b.notifyNew(ctx)
}

// paidBackRows makes the board follow the "คืนแล้ว" ticks of the bill's members: a ticked member
// gets a payback row (them → the payer) made for this bill; a member un-ticked (or gone, or with a
// share that changed) loses the one made before, with my repayment of it, in the same DB
// transaction. When I paid and my book records the debts, a new payback also books the repayment
// into my wallet (E2: their balance stays 0).
func (b *booking) paidBackRows(ctx context.Context, in EventBillInput, r *resolvedBill, books EventBillBooks) error {
	if in.Kind == PTTypePayback {
		return nil
	}
	type made struct {
		id     uuid.UUID
		amount float64
	}
	type pair struct{ by, to uuid.UUID }
	// multi-payer: a ticked member pays EACH payer their pair (share × paid / bill), one row per payer
	want := map[pair]float64{}
	for id, back := range r.paidBack {
		share := r.shares[id]
		if !back || share <= 0.005 {
			continue
		}
		for p, paid := range r.payers {
			if a := round2(share * paid / in.Amount); p != id && a > 0.005 {
				want[pair{id, p}] = a
			}
		}
	}
	have := map[pair]made{}
	rows, err := b.tx.Query(ctx, `SELECT pb.id, pb.transaction_member_id, c.transaction_member_id, pb.amount::float8
		FROM project_transactions pb JOIN project_transactions c ON c.parent_project_transaction_id = pb.id
		WHERE pb.paid_back_for_id = $1 AND pb.parent_project_transaction_id IS NULL`, b.rowID)
	if err != nil {
		return fmt.Errorf("paybacks of bill: %w", err)
	}
	for rows.Next() {
		var (
			m      made
			by, to uuid.UUID
		)
		if err := rows.Scan(&m.id, &by, &to, &m.amount); err != nil {
			rows.Close()
			return err
		}
		have[pair{by, to}] = m
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for k, m := range have {
		if a, ok := want[k]; ok && math.Abs(a-m.amount) <= 0.005 {
			delete(want, k) // as it was
			continue
		}
		if err := b.removePayback(ctx, m.id); err != nil {
			return err
		}
	}
	for k, a := range want {
		if _, err := b.payback(ctx, k.by, k.to, a, in.Date, &b.rowID); err != nil {
			return err
		}
	}
	return nil // the payers' repayments of them are part of the payers' copies (applyCopyTx)
}

// removePayback deletes a payback row made for a bill, and every member's repayment of it (their
// wallet transactions too): the tick is gone for the whole bill.
func (b *booking) removePayback(ctx context.Context, rowID uuid.UUID) error {
	rows, err := b.tx.Query(ctx, `SELECT pd.id, pd.user_id, `+shared.DebtKeySQL("pd")+`, t.id, t.account_id, pd.counterparty_contact_id, pd.counterparty_person_name
		FROM personal_debts pd LEFT JOIN transactions t ON t.source_personal_debt_id = pd.id
		WHERE pd.source_project_transaction_id = $1`, rowID)
	if err != nil {
		return fmt.Errorf("repayments of payback: %w", err)
	}
	type rep struct {
		id, owner uuid.UUID
		txID      *uuid.UUID
		key       string
		acc       *uuid.UUID
		person    shared.PersonRef
	}
	var reps []rep
	for rows.Next() {
		var r rep
		if err := rows.Scan(&r.id, &r.owner, &r.key, &r.txID, &r.acc, &r.person.ContactID, &r.person.Name); err != nil {
			rows.Close()
			return err
		}
		reps = append(reps, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range reps {
		mine := r.owner == b.me
		if mine {
			if err := b.beforeKey(r.key); err != nil {
				return err
			}
		}
		if r.txID != nil {
			if mine && r.acc != nil {
				if err := b.beforeWallet(*r.acc); err != nil {
					return err
				}
				b.wallets = append(b.wallets, *r.acc)
			}
			if _, err := b.s.txs.DeleteDebtTxInTx(ctx, b.tx, r.owner, *r.txID); err != nil {
				return err
			}
			if mine {
				b.effects.RemovedTransactionIDs = append(b.effects.RemovedTransactionIDs, *r.txID)
			}
		}
		if _, err := b.tx.Exec(ctx, `DELETE FROM personal_debts WHERE id = $1`, r.id); err != nil {
			return fmt.Errorf("delete repayment: %w", err)
		}
		if mine {
			b.effects.RemovedDebtIDs = append(b.effects.RemovedDebtIDs, r.id)
			b.keys[r.key] = r.person
		}
	}
	if err := b.s.deleteBoardRowTx(ctx, b.tx, b.me, b.project.ID, rowID); err != nil {
		return err
	}
	b.effects.RemovedBillIDs = append(b.effects.RemovedBillIDs, rowID)
	return nil
}

// books writes the parts of my book the bill should have and doesn't (have = what is linked now).
func (b *booking) books(ctx context.Context, in EventBillInput, r *resolvedBill, books EventBillBooks, have MyBooks) error {
	if in.Kind == PTTypePayback {
		return b.paybackBooks(ctx, in, r, books, have)
	}
	b.debtDflt = true
	if !books.On {
		return nil
	}
	return b.myCopy(ctx, books)
}

// myCopy writes MY copy of the bill (0.4.1-2 S5: the same engine as every member's, plus my own
// wallet / category / tags / private note): I paid → the whole bill from my wallet (the origin, E6)
// + the debts of the others (and a "คืนแล้ว" member's repayment into my wallet, E2); someone else
// paid → my share with no wallet + my debt (E4), or from my wallet with no debt when I paid back (E3).
func (b *booking) myCopy(ctx context.Context, books EventBillBooks) error {
	if _, err := b.tx.Exec(ctx, `DELETE FROM shared_bill_opt_outs WHERE project_transaction_id = $1 AND user_id = $2`,
		b.rowID, b.me); err != nil {
		return fmt.Errorf("connect my copy: %w", err)
	}
	st, err := b.s.loadBillTx(ctx, b.tx, b.rowID)
	if err != nil {
		return err
	}
	if role, _ := st.role(b.meMem); role == roleNone {
		return ErrNoShareToCopy
	}
	opts := &copyOptions{wantTx: books.Tx, wantDebt: books.Debt == nil || *books.Debt,
		account: books.AccountID, category: books.CategoryID, tagIDs: books.TagIDs, tagsSet: books.TagIDs != nil,
		privateNote: books.PrivateNote, privateNoteSet: books.privateNoteSet || books.PrivateNote != nil, repayAccount: books.AccountID,
		beforeKey: b.beforeKey, beforeWallet: b.beforeWallet}
	res, err := b.s.applyCopyTx(ctx, b.tx, st, b.me, opts)
	if err != nil {
		return err
	}
	b.copied = true
	if res.txID != nil {
		b.myTx = res.txID
	}
	for k, p := range res.keys {
		b.keys[k] = p
	}
	b.wallets = append(b.wallets, res.wallets...)
	b.copyDebts = append(b.copyDebts, res.debts...)
	b.effects.RemovedDebtIDs = append(b.effects.RemovedDebtIDs, res.removedDebts...)
	b.effects.RemovedTransactionIDs = append(b.effects.RemovedTransactionIDs, res.removedTxs...)
	return nil
}

// paybackBooks — a payback with me in it: the debt it clears and the money.
func (b *booking) paybackBooks(ctx context.Context, in EventBillInput, r *resolvedBill, books EventBillBooks, have MyBooks) error {
	if r.from != b.meMem && r.to != b.meMem {
		if books.On {
			return ErrNoShareToCopy
		}
		return nil
	}
	iPay := r.from == b.meMem
	other := r.from
	if iPay {
		other = r.to
	}
	// E5: the default looks at THIS event's debt with that person only
	cid, name, err := b.person(ctx, other)
	if err != nil {
		return err
	}
	held, err := eventDebtWithTx(ctx, b.tx, b.me, b.project.ID, shared.DebtKey(cid, name), b.rowID)
	if err != nil {
		return err
	}
	b.debtDflt = !(iPay && held <= -in.Amount+0.005 || !iPay && held >= in.Amount-0.005)
	if !books.On {
		return nil
	}
	wantDebt := b.debtDflt
	if books.Debt != nil {
		wantDebt = *books.Debt
	}
	debtDir, repayDir := "paid", "received" // they paid me back: they owed me
	if iPay {
		debtDir, repayDir = "received", "paid"
	}
	if wantDebt && !have.Debt {
		if err := b.entry(ctx, other, b.rowID, personal_debts.KindDebt, debtDir, in.Amount, in.Date, in.Description, in.Note, nil); err != nil {
			return err
		}
	}
	if books.Tx && !have.Repay {
		return b.entry(ctx, other, b.rowID, personal_debts.KindRepayment, repayDir, in.Amount, in.Date, in.Description, in.Note,
			books.AccountID)
	}
	return nil
}

// eventDebtWithTx — my net with a person over this event's rows (the payback being written left out).
func eventDebtWithTx(ctx context.Context, q pgx.Tx, userID, projectID uuid.UUID, key string, except uuid.UUID) (float64, error) {
	var v float64
	err := q.QueryRow(ctx, `SELECT COALESCE(SUM(`+shared.DebtSignedSQL("pd")+`), 0)::float8 FROM personal_debts pd
		WHERE pd.user_id = $1 AND pd.project_id = $2 AND `+shared.DebtKeySQL("pd")+` = $3
		  AND pd.source_project_transaction_id IS DISTINCT FROM $4`, userID, projectID, key, except).Scan(&v)
	return v, err
}

// writeEdit — an existing bill: the bill (when sent), the new "คืนแล้ว" paybacks, my book.
func (b *booking) writeEdit(ctx context.Context, current *ProjectTransaction, req EventBillRebookRequest) error {
	b.rowID = current.ID
	if req.ExpectedUpdatedAt != nil &&
		!current.UpdatedAt.Truncate(time.Microsecond).Equal(req.ExpectedUpdatedAt.Truncate(time.Microsecond)) {
		return ErrBillChanged // S14
	}
	if req.Move != nil {
		if err := b.moveBill(ctx, current, *req.Move); err != nil { // S17
			return err
		}
	}
	in := EventBillInput{Kind: current.Type, Amount: current.Amount, Date: current.Date,
		Description: current.Description, Note: current.Note}
	var (
		r    *resolvedBill
		err  error
		diff map[string][2]interface{}
	)
	if req.Bill != nil {
		in = *req.Bill
		if b.kind == KindSplit {
			in.TagIDs = nil
		}
		// 0.4.1-2 Q-B4: the payer may change; no payer sent = it stays.
		var fixed *uuid.UUID
		if in.Payer == nil && current.Type != PTTypePayback {
			payer := current.TransactionMemberID
			fixed = &payer
		}
		if r, err = b.resolve(ctx, in, fixed); err != nil {
			return err
		}
		up := UpdateProjectTransactionRequest{Amount: &in.Amount, Date: &in.Date, Splits: &r.splits}
		up.SetText(in.Description, in.Note)
		if in.TagIDs != nil && current.Type != PTTypePayback {
			up.TagIDs = in.TagIDs
		}
		switch {
		case current.Type == PTTypePayback:
			up.TransactionMemberID = &r.from
		case r.payer != current.TransactionMemberID:
			up.TransactionMemberID = &r.payer
		}
		// The others hear once the paybacks are made (fan-out below).
		updated, err := b.s.store.UpdatePTTx(ctx, b.tx, b.project.ID, current.ID, b.me, up)
		if err != nil {
			return err
		}
		diff = buildDiff(current, updated)
		if err := b.setPaybackKind(ctx, in); err != nil {
			return err
		}
		if in.Kind != PTTypePayback {
			if err := SetBillPayersTx(ctx, b.tx, b.rowID, r.payers); err != nil {
				return err
			}
		}
		if err := b.paidBackRows(ctx, in, r, req.MyBooks); err != nil {
			return err
		}
	} else if r, err = b.currentPeople(ctx, current); err != nil {
		return err
	}
	if err := b.editMine(ctx, current, in, r, req.MyBooks); err != nil {
		return err
	}
	if b.moved && b.kind == KindEvent {
		if err := b.s.dispatchAddedTx(ctx, b.tx, b.project, b.mb, userDisplayOrEmpty(ctx, b.tx, b.me)); err != nil {
			return err
		}
	}
	if req.Bill != nil || b.moved {
		return b.s.fanOutTx(ctx, b.tx, b.me, current.ID, false, diff)
	}
	return nil
}

// setPaybackKind — V11 (BE-2): a payback's kind (payback | loan) on its parent row.
func (b *booking) setPaybackKind(ctx context.Context, in EventBillInput) error {
	if in.Kind != PTTypePayback {
		return nil
	}
	kind := "payback"
	if in.PaybackKind != nil {
		kind = *in.PaybackKind
	}
	if _, err := b.tx.Exec(ctx, `UPDATE project_transactions SET payback_kind = $2 WHERE id = $1`, b.rowID, kind); err != nil {
		return fmt.Errorf("payback kind: %w", err)
	}
	return nil
}

// editMine — my book after an edit: off = my copy disconnected; a payback as before (E5); a bill =
// my copy follows it (S5).
func (b *booking) editMine(ctx context.Context, current *ProjectTransaction, in EventBillInput, r *resolvedBill,
	books EventBillBooks) error {
	if !books.On {
		res, err := b.s.disconnectCopyTx(ctx, b.tx, current.ID, b.me)
		if err != nil {
			return err
		}
		b.copyDebts = append(b.copyDebts, res.debts...)
		b.effects.Transactions = nil
		return nil
	}
	if current.Type != PTTypePayback {
		if err := b.s.reconnectCopyTx(ctx, b.tx, current.ID, b.me); err != nil {
			return err
		}
		return b.myCopy(ctx, books)
	}
	have, err := myBooksOfRowTx(ctx, b.tx, b.me, current.ID)
	if err != nil {
		return err
	}
	want := wantedBooks(in.Kind, books, r, b.meMem)
	var unlink, relink []string
	for _, k := range []string{BookTx, BookDebt, BookRepay} {
		switch has, w := have.has(k), want.has(k); {
		case has && !w:
			unlink = append(unlink, k)
		case !has && w:
			relink = append(relink, k)
		}
	}
	for _, k := range unlink {
		if err := unlinkMineTx(ctx, b.tx, b.me, current.ID, k); err != nil {
			return err
		}
	}
	for _, k := range relink {
		found, err := relinkMineTx(ctx, b.tx, b.me, b.project.ID, current.ID, k)
		if err != nil {
			return err
		}
		if found {
			have.set(k)
		}
	}
	return b.books(ctx, in, r, books, have)
}

// currentPeople — the bill's people as they are (a books-only save).
func (b *booking) currentPeople(ctx context.Context, current *ProjectTransaction) (*resolvedBill, error) {
	r := &resolvedBill{shares: map[uuid.UUID]float64{}, paidBack: map[uuid.UUID]bool{}, payer: current.TransactionMemberID}
	shares := splitSharesTx(ctx, b.tx, current.ID)
	var sum float64
	for id, v := range shares {
		r.shares[id] = v
		r.splits = append(r.splits, ProjectSplitInput{MemberID: id, Amount: v})
		sum += v
	}
	r.shares[current.TransactionMemberID] = round2(current.Amount - sum)
	if current.Type == PTTypePayback {
		r.from = current.TransactionMemberID
		for id := range shares {
			r.to = id
		}
	}
	return r, nil
}

func (m MyBooks) has(k string) bool {
	switch k {
	case BookTx:
		return m.Tx
	case BookDebt:
		return m.Debt
	}
	return m.Repay
}

func (m *MyBooks) set(k string) {
	switch k {
	case BookTx:
		m.Tx = true
	case BookDebt:
		m.Debt = true
	default:
		m.Repay = true
	}
}

// wantedBooks — the kinds my book should hold of the bill after the save.
func wantedBooks(kind string, books EventBillBooks, r *resolvedBill, me uuid.UUID) MyBooks {
	if !books.On {
		return MyBooks{}
	}
	debt := books.Debt == nil || *books.Debt
	if kind == PTTypePayback {
		return MyBooks{Debt: debt, Repay: books.Tx}
	}
	if r.payer != me && r.paidBack[me] {
		debt = false // E3: paid back on the spot, no debt
	}
	return MyBooks{Tx: books.Tx, Debt: debt}
}

// myBooksOfRowTx — which parts of my book hold the row now.
func myBooksOfRowTx(ctx context.Context, tx pgx.Tx, userID, ptID uuid.UUID) (MyBooks, error) {
	var m MyBooks
	err := tx.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM transactions t WHERE `+myTxOfRow+`),
		EXISTS (SELECT 1 FROM personal_debts WHERE user_id = $2 AND kind = 'debt' AND source_project_transaction_id = $1),
		EXISTS (SELECT 1 FROM personal_debts WHERE user_id = $2 AND kind = 'repayment' AND source_project_transaction_id = $1)`,
		ptID, userID).Scan(&m.Tx, &m.Debt, &m.Repay)
	return m, err
}

// notifyNew — every other member with a part gets the bill by their switch (0.4.1-2 S6:
// shared_bill_created); linked people added to an EVENT hear about it (project_added).
func (b *booking) notifyNew(ctx context.Context) error {
	if err := b.s.fanOutTx(ctx, b.tx, b.me, b.rowID, true, nil); err != nil {
		return err
	}
	if b.kind == KindSplit {
		return nil
	}
	name := userDisplayOrEmpty(ctx, b.tx, b.me)
	return b.s.dispatchAddedTx(ctx, b.tx, b.project, b.mb, name)
}

// result — the answer after the commit, read fresh.
func (b *booking) result(ctx context.Context) (*EventBillBookResult, error) {
	p, err := b.s.patch(ctx, b.me, b.project.ID, &b.rowID, nil, uniqueIDs(b.wallets))
	if err != nil {
		return nil, err
	}
	out := &EventBillBookResult{EventPatch: *p, Paybacks: []BoardRow{}, Effects: b.effects}
	if p.Row != nil {
		out.MyBooks = p.Row.MyBooks
		out.Effects.Bills = append(out.Effects.Bills, *p.Row)
	}
	if len(b.paybacks) > 0 {
		if out.Paybacks, err = b.s.boardRows(ctx, b.me, b.project.ID, b.paybacks); err != nil {
			return nil, err
		}
		for _, r := range out.Paybacks {
			out.Effects.Bills = append(out.Effects.Bills, r)
		}
	}
	tx, err := b.s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if out.TransactionID, err = myLinkedTxTx(ctx, tx, b.me, b.rowID); err != nil {
		return nil, err
	}
	if b.myTx != nil {
		if d, err := b.s.txs.Get(ctx, b.me, *b.myTx); err == nil {
			out.Effects.Transactions = append(out.Effects.Transactions, *d)
		}
	}
	if b.copied { // my copy's debts / repayments and the people they move
		keys := make([]string, 0, len(b.keys))
		for k := range b.keys {
			keys = append(keys, k)
		}
		eff, err := b.s.debts.RowEffects(ctx, b.s.store.Pool(), b.me, uniqueIDs(b.copyDebts), nil, keys)
		if err != nil {
			return nil, err
		}
		eff.RemovedDebtIDs, eff.RemovedTransactionIDs = nil, nil
		eff.AccountBalances = nil
		out.Effects.Merge(eff)
	}
	out.Effects.AccountBalances = p.AccountBalances
	return out, nil
}
