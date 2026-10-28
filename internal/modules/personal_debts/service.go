package personal_debts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/contacts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type Service struct {
	store  *Store
	txs    *transactions.Service
	notifs *notifications.Service // optional — see split_flow.go
	// recurringFor — the person's "หนี้" recurring settings for the person page (0.4.1-2 K13;
	// a hook: recurring imports this module).
	recurringFor func(ctx context.Context, userID uuid.UUID, key string) ([]shared.PersonRecurring, error)
}

// WithRecurring wires the person page's list of "หนี้" recurring settings.
func (s *Service) WithRecurring(fn func(ctx context.Context, userID uuid.UUID, key string) ([]shared.PersonRecurring, error)) {
	s.recurringFor = fn
}

func NewService(store *Store, txs *transactions.Service) *Service {
	return &Service{store: store, txs: txs}
}

const unlimitedPerPage = 100000

func clampPage(page, perPage, dflt int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if perPage <= 0 {
		perPage = dflt
	}
	if perPage > 100 {
		perPage = 100
	}
	return page, perPage
}

func (s *Service) People(ctx context.Context, userID uuid.UUID, f PeopleFilter) (*PeopleList, error) {
	return s.people(ctx, userID, f, false)
}

// PeopleAll — everyone, settled people included, in one page (the sync dataset); no 100-row cap.
func (s *Service) PeopleAll(ctx context.Context, userID uuid.UUID) (*PeopleList, error) {
	return s.people(ctx, userID, PeopleFilter{All: true}, true)
}

func (s *Service) people(ctx context.Context, userID uuid.UUID, f PeopleFilter, unlimited bool) (*PeopleList, error) {
	if unlimited {
		f.Page, f.PerPage = 1, unlimitedPerPage
	} else {
		f.Page, f.PerPage = clampPage(f.Page, f.PerPage, 30)
	}
	rows, total, err := s.store.listPeople(ctx, userID, f)
	if err != nil {
		return nil, err
	}
	totals, err := s.store.totals(ctx, s.store.db, userID)
	if err != nil {
		return nil, err
	}
	return &PeopleList{Data: rows, Totals: totals, Pagination: shared.NewPage(f.Page, f.PerPage, total)}, nil
}

func (s *Service) Totals(ctx context.Context, userID uuid.UUID) (DebtTotals, error) {
	return s.store.totals(ctx, s.store.db, userID)
}

func (s *Service) Person(ctx context.Context, userID uuid.UUID, rawKey, cycle string, page, perPage int) (*PersonPage, error) {
	k, err := shared.ParseDebtKey(rawKey)
	if err != nil {
		return nil, err
	}
	bal, found, err := s.store.personBalance(ctx, s.store.db, userID, k)
	if err != nil {
		return nil, err
	}
	if !found {
		if k.ContactID == nil || bal.Person.ContactID == nil {
			return nil, ErrPersonNotFound
		}
	}
	page, perPage = clampPage(page, perPage, 30)
	if cycle == "" {
		cycle = "current" // the page shows the current cycle; earlier ones are "load more"
	}
	lines, err := s.store.ledgerLines(ctx, s.store.db, userID, k.Key())
	if err != nil {
		return nil, err
	}
	mark := cycleMarkOf(lines)
	counts := countsOf(lines, mark)
	picked := cycleLines(lines, mark, cycle)
	pageLines := pageNewestFirst(picked, page, perPage)
	current := cycleLines(lines, mark, "current")
	if len(current) > summaryLines {
		current = current[:summaryLines]
	}
	byID, err := s.store.statementItems(ctx, s.store.db, userID, append(append([]ledgerLine{}, pageLines...), current...), mark)
	if err != nil {
		return nil, err
	}
	items, total := itemsOf(pageLines, byID), len(picked)
	msg, err := s.summaryMessage(ctx, userID, bal, itemsOf(current, byID))
	if err != nil {
		return nil, err
	}
	prefs, err := shared.UserPrefs(ctx, s.store.db, userID) // cached: the summary message read it
	if err != nil {
		return nil, err
	}
	out := &PersonPage{DebtPerson: bal, Counts: counts, Items: items, Days: statementDays(items), SummaryMessage: msg,
		Pagination: shared.NewPage(page, perPage, total), AutoSyncDebts: prefs.AutoSyncDebts, AutoAcceptDebts: prefs.AutoAcceptDebts, Recurring: []shared.PersonRecurring{}}
	if s.recurringFor != nil {
		if out.Recurring, err = s.recurringFor(ctx, userID, bal.Key); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Service) Entries(ctx context.Context, userID uuid.UUID, rawKey string, f EntryFilter) (*EntryList, error) {
	k, err := shared.ParseDebtKey(rawKey)
	if err != nil {
		return nil, err
	}
	f.Page, f.PerPage = clampPage(f.Page, f.PerPage, 30)
	items, total, err := s.store.listEntries(ctx, userID, k.Key(), f)
	if err != nil {
		return nil, err
	}
	return &EntryList{Data: items, Pagination: shared.NewPage(f.Page, f.PerPage, total)}, nil
}

func (s *Service) GetEntry(ctx context.Context, userID, id uuid.UUID) (*EntryDetail, error) {
	v, err := entryView(ctx, s.store.db, userID, id)
	if err != nil {
		return nil, err
	}
	bal, err := s.balanceOfKey(ctx, s.store.db, userID, v.Key)
	if err != nil {
		return nil, err
	}
	out := &EntryDetail{Entry: *v, PersonBalance: bal}
	if v.Transaction != nil {
		if out.Transaction, err = s.txs.Get(ctx, userID, v.Transaction.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Service) balanceOfKey(ctx context.Context, q shared.Querier, userID uuid.UUID, key string) (DebtPerson, error) {
	k, err := shared.ParseDebtKey(key)
	if err != nil {
		return DebtPerson{}, err
	}
	bal, _, err := s.store.personBalance(ctx, q, userID, k)
	return bal, err
}

func today(ctx context.Context, tx pgx.Tx) (string, error) {
	var d string
	err := tx.QueryRow(ctx, `SELECT to_char(CURRENT_DATE, 'YYYY-MM-DD')`).Scan(&d)
	return d, err
}

func resolvePerson(ctx context.Context, q querier, userID uuid.UUID, rawKey string, personName *string) (*uuid.UUID, string, error) {
	k, err := shared.ParseDebtKey(rawKey)
	if err != nil {
		return nil, "", err
	}
	if k.ContactID != nil {
		var name string
		err := q.QueryRow(ctx, `SELECT display_name FROM contacts WHERE id = $1 AND user_id = $2`,
			*k.ContactID, userID).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", ErrContactNotFound
		}
		if err != nil {
			return nil, "", fmt.Errorf("contact: %w", err)
		}
		return k.ContactID, name, nil
	}
	name := k.Name
	if personName != nil && strings.EqualFold(strings.TrimSpace(*personName), k.Name) {
		return nil, strings.TrimSpace(*personName), nil
	}
	// No spelling sent: keep the person's current one (the latest line's snapshot), so writing to
	// "n:lee" doesn't rename "Lee".
	var snap string
	err = q.QueryRow(ctx, `SELECT l.snap FROM `+ledgerSQL+` l WHERE l.key = $2
		ORDER BY l.created_at DESC LIMIT 1`, userID, k.Key()).Scan(&snap)
	switch {
	case err == nil:
		name = snap
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, "", fmt.Errorf("person name: %w", err)
	}
	return nil, name, nil
}

func (s *Service) CreateEntry(ctx context.Context, userID uuid.UUID, rawKey string, req CreateEntryRequest) (*WriteResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	e, touched, err := s.createEntryTx(ctx, tx, userID, rawKey, req)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.createdResult(ctx, userID, e, touched)
}

// createdResult — a create's answer: the write result + balance_change + the statement line.
func (s *Service) createdResult(ctx context.Context, userID uuid.UUID, e *Entry, wallets []uuid.UUID) (*WriteResult, error) {
	out, err := s.writeResult(ctx, userID, e.ID, touch{wallets: wallets}, nil)
	if err != nil {
		return nil, err
	}
	// An overpaid repayment is allowed: the balance simply crosses 0 (balance_change.flipped says so).
	bc := transactions.NewPersonDelta(out.PersonBalance.Key, out.PersonBalance.Person,
		out.PersonBalance.Net-e.Signed(), out.PersonBalance.Net)
	out.BalanceChange = &bc
	if out.Item, err = s.itemOf(ctx, userID, e.Key(), e.ID); err != nil {
		return nil, err
	}
	return out, nil
}

// PreviewEntry — POST …/entries?preview=true (the debt quick create's result box): the write runs
// and is rolled back; the answer is the person's balance before → after and the wallet's.
func (s *Service) PreviewEntry(ctx context.Context, userID uuid.UUID, rawKey string, req CreateEntryRequest) (*DebtPreview, error) {
	return s.preview(ctx, userID, nonNil(req.AccountID), func(tx pgx.Tx) (string, float64, error) {
		e, _, err := s.createEntryTx(ctx, tx, userID, rawKey, req)
		if err != nil {
			return "", 0, err
		}
		return e.Key(), e.Signed(), nil
	})
}

// wantsTx — the transaction side of a create: create_transaction when sent, else a transaction
// exactly when a wallet is given.
func wantsTx(create *bool, accountID *uuid.UUID) bool {
	if create != nil {
		return *create
	}
	return accountID != nil
}

// createEntryTx — POST …/entries' write: the debt (and its transaction, new or connected).
func (s *Service) createEntryTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, rawKey string, req CreateEntryRequest) (*Entry, []uuid.UUID, error) {
	if req.CreateTransaction == nil && req.AccountID == nil && req.LinkTransactionID == nil && len(req.TagIDs) > 0 {
		return nil, nil, ErrTagsNeedWallet // the old contract, for clients that don't send create_transaction
	}
	if req.BillID != nil || req.EventID != nil {
		return nil, nil, ErrUseClaim // 0.4.1-2 V8 / V9
	}
	contactID, name, err := resolvePerson(ctx, tx, userID, rawKey, req.PersonName)
	if err != nil {
		return nil, nil, err
	}
	date := ""
	if req.Date != nil {
		date = *req.Date
	} else if date, err = today(ctx, tx); err != nil {
		return nil, nil, err
	}
	e, wallets, err := s.insertRowTx(ctx, tx, userID, EntryInput{
		Kind: KindDebt, Direction: req.Direction, ContactID: contactID, PersonName: name,
		Amount: req.Amount, Date: date, Description: req.Description, Note: req.Note, DueDate: req.DueDate,
		CreateTransaction: wantsTx(req.CreateTransaction, req.AccountID), AccountID: req.AccountID, TagIDs: req.TagIDs,
		LinkTransactionID: req.LinkTransactionID, BillID: req.BillID, EventID: req.EventID,
	})
	if err != nil {
		return nil, nil, err
	}
	if err := markPaidTx(ctx, tx, userID, e.Key(), req.MarkPaidIDs); err != nil {
		return nil, nil, err
	}
	return e, wallets, nil
}

// preview — run write in a DB transaction that is always rolled back, and read the person's balance
// and the wallets before / after (write answers the person's key and the signed amount it added).
func (s *Service) preview(ctx context.Context, userID uuid.UUID, wallets []uuid.UUID,
	write func(tx pgx.Tx) (string, float64, error)) (*DebtPreview, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	before, err := shared.LoadWalletBalances(ctx, tx, wallets)
	if err != nil {
		return nil, err
	}
	key, signed, err := write(tx)
	if err != nil {
		return nil, err
	}
	after, err := s.balanceOfKey(ctx, tx, userID, key)
	if err != nil {
		return nil, err
	}
	afterW, err := shared.LoadWalletBalances(ctx, tx, wallets)
	if err != nil {
		return nil, err
	}
	out := &DebtPreview{Preview: true,
		People:  []transactions.PersonDelta{transactions.NewPersonDelta(key, after.Person, after.Net-signed, after.Net)},
		Wallets: []transactions.WalletDelta{}}
	was := map[uuid.UUID]float64{}
	for _, b := range before {
		was[b.AccountID] = b.Balance
	}
	for _, a := range afterW {
		out.Wallets = append(out.Wallets, transactions.WalletDelta{AccountID: a.AccountID, Before: was[a.AccountID],
			After: a.Balance, Delta: round2(a.Balance - was[a.AccountID])})
	}
	return out, nil
}

func markPaidTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, key string, ids []uuid.UUID) error {
	ids = uniqueIDs(ids)
	if len(ids) == 0 {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE personal_debts SET paid = TRUE, updated_by_user_id = $1
		WHERE user_id = $1 AND id = ANY($2) AND kind = 'debt' AND `+shared.DebtKeySQL("personal_debts")+` = $3`, userID, ids, key)
	if err != nil {
		return fmt.Errorf("mark paid: %w", err)
	}
	if int(tag.RowsAffected()) != len(ids) {
		return ErrMarkPaidNotFound
	}
	return nil
}

func uniqueIDs(ids []uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// writeResult — a write's answer (after commit): the entry, its transaction, the person's balance
// (and the previous person's when it moved), the wallets and the effects.
func (s *Service) writeResult(ctx context.Context, userID, entryID uuid.UUID, t touch, previousKey *string) (*WriteResult, error) {
	v, err := entryView(ctx, s.store.db, userID, entryID)
	if err != nil {
		return nil, err
	}
	out := &WriteResult{Entry: *v}
	if out.PersonBalance, err = s.balanceOfKey(ctx, s.store.db, userID, v.Key); err != nil {
		return nil, err
	}
	t.debts = append(t.debts, entryID)
	if previousKey != nil && *previousKey != v.Key {
		prev, err := s.balanceOfKey(ctx, s.store.db, userID, *previousKey)
		if err != nil {
			return nil, err
		}
		out.PreviousPersonBalance = &prev
		t.keys = append(t.keys, *previousKey)
	}
	if v.Transaction != nil {
		if out.Transaction, err = s.txs.Get(ctx, userID, v.Transaction.ID); err != nil {
			return nil, err
		}
	}
	if out.AccountBalances, err = shared.LoadWalletBalances(ctx, s.store.db, t.wallets); err != nil {
		return nil, err
	}
	if out.Effects, err = s.effects(ctx, s.store.db, userID, t); err != nil {
		return nil, err
	}
	return out, nil
}

// LinkTx — POST /personal-debts/{id}/link (0.4.1-1 D2): one of my plain transactions of the entry's
// direction becomes its transaction, connected + synced. The entry takes its amount and date
// (description / note: the entry's when set, else the transaction's); the transaction takes the kind
// category (its own is kept for a disconnect).
func (s *Service) LinkTx(ctx context.Context, userID, id, txID uuid.UUID) (*WriteResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	e, err := getEntry(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	if e.SourceKind != nil && *e.SourceKind != SourceEvent {
		return nil, ErrManagedBySource
	}
	own, err := entryTxID(ctx, tx, e.ID)
	if err != nil {
		return nil, err
	}
	if own != nil {
		return nil, ErrEntryHasTx
	}
	row, err := linkableTx(ctx, tx, userID, txID, e.Direction)
	if err != nil {
		return nil, err
	}
	descr, note := firstText(e.Description, row.Description), firstText(e.Note, row.Note)
	if _, err := tx.Exec(ctx, `UPDATE personal_debts
		SET amount = $2, date = $3::date, description = $4, note = $5, updated_by_user_id = $6
		WHERE id = $1`, e.ID, row.Amount, row.Date, descr, note, userID); err != nil {
		return nil, fmt.Errorf("link tx: %w", err)
	}
	if _, err := s.txs.ConnectDebtTxInTx(ctx, tx, userID, e.ID, txID, e.Kind, descr, note); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.writeResult(ctx, userID, e.ID, touch{}, nil)
}

// UnlinkTx — POST /personal-debts/{id}/unlink (0.4.1-1 D2): a connected transaction goes back to a
// plain row with its old category; the entry stays, ledger-only.
func (s *Service) UnlinkTx(ctx context.Context, userID, id uuid.UUID) (*WriteResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	e, err := getEntry(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	var (
		txID      uuid.UUID
		connected bool
	)
	err = tx.QueryRow(ctx, `SELECT id, debt_connected FROM transactions WHERE source_personal_debt_id = $1`, e.ID).
		Scan(&txID, &connected)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !connected) {
		return nil, ErrTxNotDisconnectable
	}
	if err != nil {
		return nil, fmt.Errorf("unlink tx: %w", err)
	}
	if err := s.txs.DisconnectDebtTxInTx(ctx, tx, userID, txID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.writeResult(ctx, userID, e.ID, touch{txs: []uuid.UUID{txID}}, nil)
}

func (s *Service) UpdateEntry(ctx context.Context, userID, id uuid.UUID, req UpdateEntryRequest) (*WriteResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	e, err := getEntry(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	// 0.4.1-2 S9 / S11: a row of a shared bill follows the bill; only paid / due_date / private_note
	// are mine.
	if err := billRowGuardTx(ctx, tx, userID, id, &req); err != nil {
		return nil, err
	}
	// A split-sourced entry's money belongs to its source; an event-resolve debt (source event) is
	// my own record of the bill and stays editable.
	if e.SourceKind != nil && *e.SourceKind != SourceEvent && req.moneyChange() {
		return nil, ErrManagedBySource
	}
	if e.Kind == KindRepayment && (req.Paid != nil || req.has("due_date")) {
		return nil, invalid("a repayment has no paid label or due_date")
	}
	prevKey := e.Key()
	next := *e
	if req.Direction != nil {
		next.Direction = *req.Direction
	}
	if req.Amount != nil {
		next.Amount = *req.Amount
	}
	if req.Date != nil {
		next.Date = *req.Date
	}
	if req.has("description") {
		next.Description = shared.CleanText(req.Description)
	}
	if req.has("note") {
		next.Note = shared.CleanText(req.Note)
	}
	if req.Paid != nil {
		next.Paid = *req.Paid
	}
	if req.has("due_date") {
		next.DueDate = req.DueDate
	}
	if next.DueDate != nil && *next.DueDate < next.Date {
		return nil, ErrDueBeforeDate
	}
	if req.Key != nil {
		if next.ContactID, next.PersonName, err = resolvePerson(ctx, tx, userID, *req.Key, req.PersonName); err != nil {
			return nil, err
		}
	} else if req.PersonName != nil && e.ContactID == nil && strings.EqualFold(strings.TrimSpace(*req.PersonName), strings.TrimSpace(e.PersonName)) {
		next.PersonName = strings.TrimSpace(*req.PersonName) // a respelling of the same typed name
	}

	txID, err := entryTxID(ctx, tx, e.ID)
	if err != nil {
		return nil, err
	}
	var (
		wallet    *uuid.UUID
		connected bool
	)
	if txID != nil {
		if err := tx.QueryRow(ctx, `SELECT account_id, debt_connected FROM transactions WHERE id = $1`, *txID).
			Scan(&wallet, &connected); err != nil {
			return nil, fmt.Errorf("entry wallet: %w", err)
		}
	}
	// A connected (existing) transaction keeps its own wallet / tags / type: edit it there.
	if connected && (req.has("account_id") || req.CreateTransaction != nil || req.has("tag_ids") ||
		(req.Direction != nil && *req.Direction != e.Direction)) {
		return nil, invalid("a connected transaction's wallet, tags and type change on the transaction (or disconnect it first)")
	}
	walletChange := req.has("account_id")
	if walletChange {
		wallet = req.AccountID
	}
	if req.CreateTransaction == nil && req.has("tag_ids") && len(req.TagIDs) > 0 && wallet == nil {
		return nil, ErrTagsNeedWallet // the old contract
	}
	wantTx := txID != nil
	switch {
	case req.CreateTransaction != nil:
		wantTx = *req.CreateTransaction
	case walletChange:
		wantTx = req.AccountID != nil
	}
	if !wantTx {
		wallet = nil
	}
	if !wantTx && ((walletChange && req.AccountID != nil) || (req.has("tag_ids") && len(req.TagIDs) > 0)) {
		return nil, ErrLedgerOnlyNoWallet
	}

	if _, err := tx.Exec(ctx, `UPDATE personal_debts
		SET direction = $2, amount = $3, date = $4::date, description = $5, note = $6, paid = $7,
		    counterparty_contact_id = $8, counterparty_person_name = $9, updated_by_user_id = $10,
		    due_date = $11::date
		WHERE id = $1`, e.ID, next.Direction, next.Amount, next.Date, next.Description, next.Note,
		next.Paid, next.ContactID, next.PersonName, userID, next.DueDate); err != nil {
		return nil, fmt.Errorf("update entry: %w", err)
	}

	var touched []uuid.UUID
	in := transactions.DebtTxInput{
		Kind: e.Kind, Direction: next.Direction, Amount: next.Amount, Date: next.Date,
		Description: next.Description, Note: next.Note, TagIDs: req.TagIDs, TagsSet: req.has("tag_ids"),
	}
	in.AccountID = wallet
	switch {
	case txID != nil && !wantTx:
		acc, err := s.txs.DeleteDebtTxInTx(ctx, tx, userID, *txID)
		if err != nil {
			return nil, err
		}
		touched = nonNil(acc)
	case txID != nil && (e.SourceKind == nil || *e.SourceKind == SourceEvent):
		if touched, err = s.txs.SyncDebtTxInTx(ctx, tx, userID, *txID, in); err != nil {
			return nil, err
		}
	case txID == nil && wantTx:
		if _, err := s.txs.CreateDebtTxInTx(ctx, tx, userID, e.ID, in); err != nil {
			return nil, err
		}
		touched = nonNil(wallet)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	t := touch{wallets: touched}
	if txID != nil && !wantTx {
		t.removedTxs = []uuid.UUID{*txID}
	}
	return s.writeResult(ctx, userID, e.ID, t, &prevKey)
}

func nonNil(id *uuid.UUID) []uuid.UUID {
	if id == nil {
		return nil
	}
	return []uuid.UUID{*id}
}

func (s *Service) DeleteEntry(ctx context.Context, userID, id uuid.UUID) (*DeleteResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	e, err := getEntry(ctx, tx, userID, id, true)
	if err != nil {
		return nil, err
	}
	if e.SourceKind != nil && *e.SourceKind == SourcePaidBy {
		return nil, ErrManagedByTx
	}
	txID, err := entryTxID(ctx, tx, e.ID)
	if err != nil {
		return nil, err
	}
	var (
		touched   []uuid.UUID
		connected bool
		t         = touch{removedDebts: []uuid.UUID{e.ID}, keys: []string{e.Key()}}
	)
	if txID != nil {
		if err := tx.QueryRow(ctx, `SELECT debt_connected FROM transactions WHERE id = $1`, *txID).Scan(&connected); err != nil {
			return nil, fmt.Errorf("entry tx: %w", err)
		}
	}
	switch {
	case txID != nil && connected:
		// A connected (existing) transaction stays, disconnected, with its old category (D2).
		if err := s.txs.DisconnectDebtTxInTx(ctx, tx, userID, *txID); err != nil {
			return nil, err
		}
		t.txs = []uuid.UUID{*txID}
		txID = nil
	case txID != nil:
		acc, err := s.txs.DeleteDebtTxInTx(ctx, tx, userID, *txID)
		if err != nil {
			return nil, err
		}
		touched = nonNil(acc)
		t.removedTxs = []uuid.UUID{*txID}
	}
	if e.PaidForTransactionID != nil {
		// A partner's split on me: my expense of it goes with it (it has no wallet).
		acc, err := s.txs.DeleteInTx(ctx, tx, userID, *e.PaidForTransactionID)
		if err != nil && !errors.Is(err, transactions.ErrTxNotFound) {
			return nil, err
		}
		touched = append(touched, acc...)
		if err == nil {
			t.removedTxs = append(t.removedTxs, *e.PaidForTransactionID)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM personal_debts WHERE id = $1`, e.ID); err != nil {
		return nil, fmt.Errorf("delete entry: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	out := &DeleteResult{ID: e.ID, TransactionID: txID}
	if out.PersonBalance, err = s.balanceOfKey(ctx, s.store.db, userID, e.Key()); err != nil {
		return nil, err
	}
	if out.AccountBalances, err = shared.LoadWalletBalances(ctx, s.store.db, touched); err != nil {
		return nil, err
	}
	t.wallets = touched
	if out.Effects, err = s.effects(ctx, s.store.db, userID, t); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateRepayment — POST /personal-debts/people/{key}/repayments (0.4.1-1 D3): a person-book row of
// kind repayment (pay = direction paid, net +; receive = received, net −), tied to the person, its
// transaction optional (new, connected, or none = ledger-only). mark_paid_ids / mark_all_paid only
// label debts paid.
func (s *Service) CreateRepayment(ctx context.Context, userID uuid.UUID, rawKey string, req RepaymentRequest) (*WriteResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	e, wallets, err := s.createRepaymentTx(ctx, tx, userID, rawKey, req)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.createdResult(ctx, userID, e, wallets)
}

// PreviewRepayment — POST …/repayments?preview=true: the settle's before → after, nothing saved.
func (s *Service) PreviewRepayment(ctx context.Context, userID uuid.UUID, rawKey string, req RepaymentRequest) (*DebtPreview, error) {
	return s.preview(ctx, userID, nonNil(req.AccountID), func(tx pgx.Tx) (string, float64, error) {
		e, _, err := s.createRepaymentTx(ctx, tx, userID, rawKey, req)
		if err != nil {
			return "", 0, err
		}
		return e.Key(), e.Signed(), nil
	})
}

// repayDirection — pay (I pay back) = paid, net +; receive = received, net −.
func repayDirection(kind string) string {
	if kind == RepayPay {
		return DirectionPaid
	}
	return DirectionReceived
}

func (s *Service) createRepaymentTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, rawKey string, req RepaymentRequest) (*Entry, []uuid.UUID, error) {
	if req.BillID != nil {
		return nil, nil, ErrUseClaim // 0.4.1-2 V9: a payback is claimed through POST /projects/{id}/claim
	}
	contactID, name, err := resolvePerson(ctx, tx, userID, rawKey, req.PersonName)
	if err != nil {
		return nil, nil, err
	}
	date := ""
	if req.Date != nil {
		date = *req.Date
	} else if date, err = today(ctx, tx); err != nil {
		return nil, nil, err
	}
	e, wallets, err := s.insertRowTx(ctx, tx, userID, EntryInput{
		Kind: KindRepayment, Direction: repayDirection(req.Kind), ContactID: contactID, PersonName: strings.TrimSpace(name),
		Amount: req.Amount, Date: date, Description: req.Description, Note: req.Note,
		CreateTransaction: wantsTx(req.CreateTransaction, req.AccountID), AccountID: req.AccountID, TagIDs: req.TagIDs,
		LinkTransactionID: req.LinkTransactionID, BillID: req.BillID,
	})
	if err != nil {
		return nil, nil, err
	}
	if err := markPaidTx(ctx, tx, userID, e.Key(), req.MarkPaidIDs); err != nil {
		return nil, nil, err
	}
	if req.MarkAllPaid {
		if err := markAllPaidTx(ctx, tx, userID, e.Key(), e.Direction); err != nil {
			return nil, nil, err
		}
	}
	return e, wallets, nil
}

// RepayEntry — a pending settle_debt draft: a repayment to / from the debt's person (a paid debt →
// they pay me back = receive; a received debt → I pay back = pay), with a transaction when a wallet
// is given. The debt itself is not touched (the paid label stays the user's call).
func (s *Service) RepayEntry(ctx context.Context, userID, targetID uuid.UUID, amount float64, accountID *uuid.UUID,
	date, description, note *string) (*WriteResult, error) {
	e, err := getEntry(ctx, s.store.db, userID, targetID, false)
	if err != nil {
		return nil, err
	}
	kind := RepayPay
	if e.Direction == DirectionPaid {
		kind = RepayReceive
	}
	name := e.PersonName
	return s.CreateRepayment(ctx, userID, e.Key(), RepaymentRequest{
		Kind: kind, Amount: amount, Date: date, AccountID: accountID,
		Description: description, Note: note, PersonName: &name,
	})
}

func (s *Service) TxEditedTx(ctx context.Context, tx pgx.Tx, userID, entryID uuid.UUID, row *transactions.Transaction) error {
	_, err := tx.Exec(ctx, `UPDATE personal_debts
		SET amount = $3, date = $4::date, description = $5, note = $6, updated_by_user_id = $2
		WHERE id = $1 AND user_id = $2`, entryID, userID, row.Amount, row.Date, row.Description, row.Note)
	if err != nil {
		return fmt.Errorf("sync entry: %w", err)
	}
	return nil
}

// TxDeletedTx — the entry's transaction was deleted (0.4.1-1 D1: deleting my tx = disconnecting it):
// the entry stays, ledger-only, so the person's balance doesn't move.
func (s *Service) TxDeletedTx(ctx context.Context, tx pgx.Tx, userID, entryID uuid.UUID) (string, error) {
	e, err := getEntry(ctx, tx, userID, entryID, false)
	if errors.Is(err, ErrEntryNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return e.Key(), nil
}

func (s *Service) PersonBalanceByEntry(ctx context.Context, userID, entryID uuid.UUID) (any, error) {
	e, err := getEntry(ctx, s.store.db, userID, entryID, false)
	if err != nil {
		return nil, err
	}
	return s.balanceOfKey(ctx, s.store.db, userID, e.Key())
}

func (s *Service) PersonBalanceByKey(ctx context.Context, userID uuid.UUID, key string) (any, error) {
	if key == "" {
		return nil, nil
	}
	return s.balanceOfKey(ctx, s.store.db, userID, key)
}

var _ transactions.DebtLinks = (*Service)(nil)

func (s *Service) CreateForTransactionTx(
	ctx context.Context, tx pgx.Tx,
	parentTxID, userID uuid.UUID,
	parentType, parentCurrency string, parentDescription *string,
	inputs []transactions.SplitInput,
) error {
	if len(inputs) == 0 {
		return nil
	}
	direction := DirectionPaid
	if parentType == "income" {
		direction = DirectionReceived
	}
	// Every split contact must be one of the splitter's own contacts — otherwise a foreign contact
	// id would mirror a debt into a stranger's account.
	for i := range inputs {
		if err := assertContactOwned(ctx, tx, userID, inputs[i].ContactID); err != nil {
			if errors.Is(err, ErrContactNotFound) {
				return transactions.ErrSplitContactNotFound
			}
			return err
		}
	}
	var date string
	if err := tx.QueryRow(ctx, `SELECT to_char(date, 'YYYY-MM-DD') FROM transactions WHERE id = $1`, parentTxID).Scan(&date); err != nil {
		return fmt.Errorf("bill date: %w", err)
	}
	kind := SourceTransaction
	for i := range inputs {
		in := inputs[i]
		if _, err := insertEntryTx(ctx, tx, &Entry{
			UserID: userID, Direction: direction, ContactID: in.ContactID, PersonName: in.PersonName,
			SourceTransactionID: &parentTxID, Amount: in.OwedAmount,
			Date: date, SourceKind: &kind, Description: parentDescription,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) UnlinkedNames(ctx context.Context, userID uuid.UUID) ([]contacts.UnlinkedName, error) {
	rows, err := s.store.UnlinkedNames(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]contacts.UnlinkedName, 0, len(rows))
	for _, r := range rows {
		out = append(out, contacts.UnlinkedName{Name: r.Name, Count: r.Count})
	}
	return out, nil
}

func (s *Service) AbsorbForContact(ctx context.Context, userID, contactID uuid.UUID, names []string) (int, error) {
	return s.store.AbsorbForContact(ctx, userID, contactID, names)
}

func (s *Service) RestoreOnContactDeleteTx(ctx context.Context, tx pgx.Tx, userID, contactID uuid.UUID) (int, error) {
	return s.store.RestoreOnContactDeleteTx(ctx, tx, userID, contactID)
}
