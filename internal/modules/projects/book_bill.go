package projects

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

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
}

type EventBillBooks struct {
	On         bool        `json:"on"`
	Tx         bool        `json:"tx"`
	Debt       *bool       `json:"debt"`
	AccountID  *uuid.UUID  `json:"account_id"`
	CategoryID *uuid.UUID  `json:"category_id"`
	TagIDs     []uuid.UUID `json:"tag_ids"`
}

type NewEventInput struct {
	Name     string           `json:"name"`
	IconCode *shared.IconCode `json:"icon_code"`
}

type EventBillBookRequest struct {
	ProjectID *uuid.UUID     `json:"project_id"`
	NewEvent  *NewEventInput `json:"new_event"`
	Bill      EventBillInput `json:"bill"`
	MyBooks   EventBillBooks `json:"my_books"`
}

type EventBillRebookRequest struct {
	Bill        *EventBillInput `json:"bill"`
	MyBooks     EventBillBooks  `json:"my_books"`
	ApplyToMyTx bool            `json:"apply_to_my_tx"`
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
	if (req.ProjectID == nil) == (req.NewEvent == nil) {
		return nil, ErrBookBillTarget
	}
	if err := validBillInput(&req.Bill); err != nil {
		return nil, err
	}
	if req.NewEvent != nil && (strings.TrimSpace(req.NewEvent.Name) == "" || len([]rune(req.NewEvent.Name)) > 100) {
		return nil, ErrBookBillInput
	}
	return s.runBooking(ctx, userID, preview, func(b *booking) error {
		if req.NewEvent != nil {
			if err := b.newEvent(ctx, *req.NewEvent); err != nil {
				return err
			}
		} else if err := b.open(ctx, *req.ProjectID, "create_pt"); err != nil {
			return err
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
	return s.runBooking(ctx, userID, preview, func(b *booking) error {
		op := "resolve"
		if req.Bill != nil {
			op = "edit_pt"
		}
		if err := b.open(ctx, current.ProjectID, op); err != nil {
			return err
		}
		return b.writeEdit(ctx, current, req)
	})
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
	b.project, b.meMem = p, owner.ID
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
	payer    uuid.UUID
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
	if fixedPayer != nil {
		r.payer = *fixedPayer
		if in.Payer != nil {
			id, err := b.mb.memberForPerson(ctx, *in.Payer, b.meMem)
			if err != nil {
				return nil, err
			}
			if id != *fixedPayer {
				return nil, ErrPayerFixed
			}
		}
	} else {
		payer := BillPerson{Me: true}
		if in.Payer != nil {
			payer = *in.Payer
		}
		id, err := b.mb.memberForPerson(ctx, payer, b.meMem)
		if err != nil {
			return nil, err
		}
		r.payer = id
	}
	if r.payer != b.meMem && in.Kind != PTTypeExpense {
		return nil, ErrPayerIncome
	}
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
	if err := b.paidBackRows(ctx, in, r, books); err != nil {
		return err
	}
	if err := b.books(ctx, in, r, books, MyBooks{}); err != nil {
		return err
	}
	return b.notifyNew(ctx, payer, in.Kind)
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
	have := map[uuid.UUID]made{}
	rows, err := b.tx.Query(ctx, `SELECT id, transaction_member_id, amount::float8 FROM project_transactions
		WHERE paid_back_for_id = $1 AND parent_project_transaction_id IS NULL`, b.rowID)
	if err != nil {
		return fmt.Errorf("paybacks of bill: %w", err)
	}
	for rows.Next() {
		var (
			m  made
			by uuid.UUID
		)
		if err := rows.Scan(&m.id, &by, &m.amount); err != nil {
			rows.Close()
			return err
		}
		have[by] = m
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	repay := r.payer == b.meMem && books.On && (books.Debt == nil || *books.Debt)
	for id, m := range have {
		share := r.shares[id]
		if r.paidBack[id] && share > 0.005 && math.Abs(share-m.amount) <= 0.005 {
			continue // as it was
		}
		if err := b.removePayback(ctx, m.id); err != nil {
			return err
		}
		delete(have, id)
	}
	for id, back := range r.paidBack {
		share := r.shares[id]
		if _, ok := have[id]; ok || !back || share <= 0.005 || id == r.payer {
			continue
		}
		row, err := b.payback(ctx, id, r.payer, share, in.Date, &b.rowID)
		if err != nil {
			return err
		}
		if repay {
			if err := b.entry(ctx, id, row, personal_debts.KindRepayment, "received", share, in.Date, in.Description,
				in.Note, books.AccountID); err != nil {
				return err
			}
		}
	}
	return nil
}

// removePayback deletes a payback row made for a bill, and my repayments of it (their wallet
// transactions too).
func (b *booking) removePayback(ctx context.Context, rowID uuid.UUID) error {
	rows, err := b.tx.Query(ctx, `SELECT pd.id, `+shared.DebtKeySQL("pd")+`, t.id, t.account_id, pd.counterparty_contact_id, pd.counterparty_person_name
		FROM personal_debts pd LEFT JOIN transactions t ON t.source_personal_debt_id = pd.id
		WHERE pd.user_id = $1 AND pd.source_project_transaction_id = $2`, b.me, rowID)
	if err != nil {
		return fmt.Errorf("repayments of payback: %w", err)
	}
	type rep struct {
		id, txID *uuid.UUID
		key      string
		acc      *uuid.UUID
		person   shared.PersonRef
	}
	var reps []rep
	for rows.Next() {
		var (
			r  rep
			id uuid.UUID
		)
		if err := rows.Scan(&id, &r.key, &r.txID, &r.acc, &r.person.ContactID, &r.person.Name); err != nil {
			rows.Close()
			return err
		}
		r.id = &id
		reps = append(reps, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range reps {
		if err := b.beforeKey(r.key); err != nil {
			return err
		}
		if r.txID != nil {
			if r.acc != nil {
				if err := b.beforeWallet(*r.acc); err != nil {
					return err
				}
				b.wallets = append(b.wallets, *r.acc)
			}
			if _, err := b.s.txs.DeleteDebtTxInTx(ctx, b.tx, b.me, *r.txID); err != nil {
				return err
			}
			b.effects.RemovedTransactionIDs = append(b.effects.RemovedTransactionIDs, *r.txID)
		}
		if _, err := b.tx.Exec(ctx, `DELETE FROM personal_debts WHERE id = $1`, *r.id); err != nil {
			return fmt.Errorf("delete repayment: %w", err)
		}
		b.effects.RemovedDebtIDs = append(b.effects.RemovedDebtIDs, *r.id)
		b.keys[r.key] = r.person
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
	iPaid := r.payer == b.meMem
	share, inBill := r.shares[b.meMem]
	b.debtDflt = true
	if !books.On || (!iPaid && (!inBill || share <= 0.005)) {
		if books.On {
			return ErrNoShareToCopy
		}
		return nil
	}
	wantDebt := books.Debt == nil || *books.Debt
	if iPaid {
		if books.Tx && !have.Tx {
			id, err := b.myTxFor(ctx, in.Kind, in.Amount, in, books, books.AccountID, b.rowID)
			if err != nil {
				return err
			}
			if err := b.asOrigin(ctx, b.rowID, id); err != nil { // E6
				return err
			}
		}
		if !wantDebt || have.Debt {
			return nil
		}
		for id, amount := range r.shares {
			if id == b.meMem || amount <= 0.005 {
				continue
			}
			if err := b.entry(ctx, id, b.rowID, personal_debts.KindDebt, "paid", amount, in.Date, in.Description, in.Note, nil); err != nil {
				return err
			}
		}
		return nil
	}
	// someone else paid; I have a share
	if r.paidBack[b.meMem] { // E3: my share from my wallet, no debt
		if books.Tx && !have.Tx {
			src, err := b.mySplitRow(ctx, b.rowID)
			if err != nil {
				return err
			}
			if _, err := b.myTxFor(ctx, PTTypeExpense, share, in, books, books.AccountID, src); err != nil {
				return err
			}
		}
		return nil
	}
	if books.Tx && !have.Tx { // E4: "จ่ายโดย X": no wallet
		src, err := b.mySplitRow(ctx, b.rowID)
		if err != nil {
			return err
		}
		if _, err := b.myTxFor(ctx, PTTypeExpense, share, in, books, nil, src); err != nil {
			return err
		}
	}
	if wantDebt && !have.Debt {
		return b.entry(ctx, r.payer, b.rowID, personal_debts.KindDebt, "received", share, in.Date, in.Description, in.Note, nil)
	}
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
	in := EventBillInput{Kind: current.Type, Amount: current.Amount, Date: current.Date,
		Description: current.Description, Note: current.Note}
	var r *resolvedBill
	var err error
	if req.Bill != nil {
		in = *req.Bill
		payer := current.TransactionMemberID
		fixed := &payer
		if current.Type == PTTypePayback {
			fixed = nil
		}
		if r, err = b.resolve(ctx, in, fixed); err != nil {
			return err
		}
		up := UpdateProjectTransactionRequest{Amount: &in.Amount, Date: &in.Date, Splits: &r.splits}
		up.SetText(in.Description, in.Note)
		if in.TagIDs != nil && current.Type != PTTypePayback {
			up.TagIDs = in.TagIDs
		}
		if current.Type == PTTypePayback {
			up.TransactionMemberID = &r.from
		}
		if _, err := b.s.applyBillEditTx(ctx, b.tx, b.me, b.project.ID, current, up); err != nil {
			return err
		}
		if err := b.paidBackRows(ctx, in, r, req.MyBooks); err != nil {
			return err
		}
		if req.ApplyToMyTx {
			if mine, err := myLinkedTxTx(ctx, b.tx, b.me, current.ID); err != nil {
				return err
			} else if mine != nil && current.Type != PTTypePayback {
				t, err := b.s.applyBillToTxTx(ctx, b.tx, b.me, *mine)
				if err != nil {
					return err
				}
				b.wallets = append(b.wallets, t...)
			}
		}
	} else if r, err = b.currentPeople(ctx, current); err != nil {
		return err
	}
	have, err := myBooksOfRowTx(ctx, b.tx, b.me, current.ID)
	if err != nil {
		return err
	}
	want := wantedBooks(in.Kind, req.MyBooks, r, b.meMem)
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
	return b.books(ctx, in, r, req.MyBooks, have)
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

// notifyNew — a linked payer hears about a bill recorded for them; linked people added hear about it.
func (b *booking) notifyNew(ctx context.Context, payer uuid.UUID, kind string) error {
	if b.s.notifs != nil && kind != PTTypePayback {
		var payerUser *uuid.UUID
		if err := b.tx.QueryRow(ctx, `SELECT user_id FROM project_members WHERE id = $1`, payer).Scan(&payerUser); err != nil {
			return fmt.Errorf("payer: %w", err)
		}
		if payerUser != nil && *payerUser != b.me {
			full, err := b.s.store.GetPTTx(ctx, b.tx, b.rowID)
			if err != nil {
				return err
			}
			if err := b.s.notifyRecordedTx(ctx, b.tx, *payerUser, payer, b.me, full); err != nil {
				return err
			}
		}
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
	out.Effects.AccountBalances = p.AccountBalances
	return out, nil
}
