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
}

func NewService(store *Store, txs *transactions.Service) *Service {
	return &Service{store: store, txs: txs}
}

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

// --- reads ---

// People — GET /personal-debts/people: one page of people + the totals.
func (s *Service) People(ctx context.Context, userID uuid.UUID, f PeopleFilter) (*PeopleList, error) {
	f.Page, f.PerPage = clampPage(f.Page, f.PerPage, 30)
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

// Totals — the debts tile numbers (dashboard, /me/counts).
func (s *Service) Totals(ctx context.Context, userID uuid.UUID) (DebtTotals, error) {
	return s.store.totals(ctx, s.store.db, userID)
}

// Person — GET /personal-debts/people/:key.
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
	items, total, err := s.store.listEntries(ctx, userID, k.Key(), EntryFilter{Page: page, PerPage: perPage, Cycle: cycle})
	if err != nil {
		return nil, err
	}
	counts, err := s.store.entryCounts(ctx, userID, k.Key())
	if err != nil {
		return nil, err
	}
	return &PersonPage{DebtPerson: bal, Counts: counts, Items: items, Pagination: shared.NewPage(page, perPage, total)}, nil
}

// Entries — GET /personal-debts/people/:key/entries.
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

// GetEntry — GET /personal-debts/:id (the tx-detail page in debt mode).
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

// --- writes ---

func today(ctx context.Context, tx pgx.Tx) (string, error) {
	var d string
	err := tx.QueryRow(ctx, `SELECT to_char(CURRENT_DATE, 'YYYY-MM-DD')`).Scan(&d)
	return d, err
}

// resolvePerson — the (contact, typed name) a key stands for. A contact must
// be the caller's; a typed name takes personName when given (the shown
// spelling), else the key's text.
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
		name = strings.TrimSpace(*personName)
	}
	return nil, name, nil
}

// CreateEntry — POST /personal-debts/people/:key/entries.
func (s *Service) CreateEntry(ctx context.Context, userID uuid.UUID, rawKey string, req CreateEntryRequest) (*WriteResult, error) {
	if len(req.TagIDs) > 0 && req.AccountID == nil {
		return nil, ErrTagsNeedWallet
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	contactID, name, err := resolvePerson(ctx, tx, userID, rawKey, req.PersonName)
	if err != nil {
		return nil, err
	}
	date := ""
	if req.Date != nil {
		date = *req.Date
	} else if date, err = today(ctx, tx); err != nil {
		return nil, err
	}
	e, err := insertEntryTx(ctx, tx, &Entry{
		UserID: userID, Direction: req.Direction, ContactID: contactID, PersonName: name,
		Amount: req.Amount, Date: date, Description: req.Description, Note: req.Note,
	})
	if err != nil {
		return nil, err
	}
	var touched []uuid.UUID
	if req.AccountID != nil {
		if _, err := s.txs.CreateDebtTxInTx(ctx, tx, userID, e.ID, transactions.DebtTxInput{
			Direction: e.Direction, AccountID: *req.AccountID, Amount: e.Amount, Date: e.Date,
			Description: e.Description, Note: e.Note, TagIDs: req.TagIDs,
		}); err != nil {
			return nil, err
		}
		touched = append(touched, *req.AccountID)
	}
	if err := markPaidTx(ctx, tx, userID, e.Key(), req.MarkPaidIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.writeResult(ctx, userID, e.ID, touched, nil)
}

// markPaidTx labels ids (entries of the person key) paid.
func markPaidTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, key string, ids []uuid.UUID) error {
	ids = uniqueIDs(ids)
	if len(ids) == 0 {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE personal_debts SET paid = TRUE, updated_by_user_id = $1
		WHERE user_id = $1 AND id = ANY($2) AND `+shared.DebtKeySQL("personal_debts")+` = $3`, userID, ids, key)
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

// writeResult — the final state after a committed write.
func (s *Service) writeResult(ctx context.Context, userID, entryID uuid.UUID, touched []uuid.UUID, previousKey *string) (*WriteResult, error) {
	v, err := entryView(ctx, s.store.db, userID, entryID)
	if err != nil {
		return nil, err
	}
	out := &WriteResult{Entry: *v}
	if out.PersonBalance, err = s.balanceOfKey(ctx, s.store.db, userID, v.Key); err != nil {
		return nil, err
	}
	if previousKey != nil && *previousKey != v.Key {
		prev, err := s.balanceOfKey(ctx, s.store.db, userID, *previousKey)
		if err != nil {
			return nil, err
		}
		out.PreviousPersonBalance = &prev
	}
	if v.Transaction != nil {
		if out.Transaction, err = s.txs.Get(ctx, userID, v.Transaction.ID); err != nil {
			return nil, err
		}
	}
	if out.AccountBalances, err = shared.LoadWalletBalances(ctx, s.store.db, touched); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateEntry — PUT /personal-debts/:id. The entry and its wallet
// transaction are one record: every money / text change is written to both.
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
	if e.SourceKind != nil && req.moneyChange() {
		return nil, ErrManagedBySource
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
	if req.Key != nil {
		if next.ContactID, next.PersonName, err = resolvePerson(ctx, tx, userID, *req.Key, req.PersonName); err != nil {
			return nil, err
		}
	} else if req.PersonName != nil && e.ContactID == nil && strings.EqualFold(strings.TrimSpace(*req.PersonName), strings.TrimSpace(e.PersonName)) {
		next.PersonName = strings.TrimSpace(*req.PersonName) // a respelling of the same typed name
	}

	// The wallet: present null = ledger-only; a value = create / move.
	txID, err := entryTxID(ctx, tx, e.ID)
	if err != nil {
		return nil, err
	}
	var wallet *uuid.UUID
	if txID != nil {
		if err := tx.QueryRow(ctx, `SELECT account_id FROM transactions WHERE id = $1`, *txID).Scan(&wallet); err != nil {
			return nil, fmt.Errorf("entry wallet: %w", err)
		}
	}
	walletChange := req.has("account_id")
	if walletChange {
		wallet = req.AccountID
	}
	if req.has("tag_ids") && len(req.TagIDs) > 0 && wallet == nil {
		return nil, ErrTagsNeedWallet
	}

	if _, err := tx.Exec(ctx, `UPDATE personal_debts
		SET direction = $2, amount = $3, date = $4::date, description = $5, note = $6, paid = $7,
		    counterparty_contact_id = $8, counterparty_person_name = $9, updated_by_user_id = $10
		WHERE id = $1`, e.ID, next.Direction, next.Amount, next.Date, next.Description, next.Note,
		next.Paid, next.ContactID, next.PersonName, userID); err != nil {
		return nil, fmt.Errorf("update entry: %w", err)
	}

	var touched []uuid.UUID
	in := transactions.DebtTxInput{
		Direction: next.Direction, Amount: next.Amount, Date: next.Date,
		Description: next.Description, Note: next.Note, TagIDs: req.TagIDs, TagsSet: req.has("tag_ids"),
	}
	switch {
	case txID != nil && wallet == nil:
		acc, err := s.txs.DeleteDebtTxInTx(ctx, tx, userID, *txID)
		if err != nil {
			return nil, err
		}
		touched = nonNil(acc)
	case txID != nil && e.SourceKind == nil:
		in.AccountID = *wallet
		if touched, err = s.txs.SyncDebtTxInTx(ctx, tx, userID, *txID, in); err != nil {
			return nil, err
		}
	case txID == nil && wallet != nil:
		in.AccountID = *wallet
		if _, err := s.txs.CreateDebtTxInTx(ctx, tx, userID, e.ID, in); err != nil {
			return nil, err
		}
		touched = []uuid.UUID{*wallet}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.writeResult(ctx, userID, e.ID, touched, &prevKey)
}

func nonNil(id *uuid.UUID) []uuid.UUID {
	if id == nil {
		return nil
	}
	return []uuid.UUID{*id}
}

// DeleteEntry — DELETE /personal-debts/:id: the entry and its wallet
// transaction (the money comes back). A split on my bill is taken off the
// bill (the linked partner hears "removed", like a split edit).
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
	txID, err := entryTxID(ctx, tx, e.ID)
	if err != nil {
		return nil, err
	}
	var touched []uuid.UUID
	if txID != nil {
		acc, err := s.txs.DeleteDebtTxInTx(ctx, tx, userID, *txID)
		if err != nil {
			return nil, err
		}
		touched = nonNil(acc)
	}
	if e.SourceKind != nil && *e.SourceKind == SourceTransaction {
		if err := s.notifySplitChangeTx(ctx, tx, e, 0, notifications.SplitChangeRemoved); err != nil {
			return nil, err
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
	return out, nil
}

// RepayEntry — a new entry with targetID's person in the opposite direction
// (paying it back), into accountID (nil = ledger-only). The pending
// settle_debt draft submits through it.
func (s *Service) RepayEntry(ctx context.Context, userID, targetID uuid.UUID, amount float64, accountID *uuid.UUID,
	date, description, note *string) (*WriteResult, error) {
	e, err := getEntry(ctx, s.store.db, userID, targetID, false)
	if err != nil {
		return nil, err
	}
	name := e.PersonName
	return s.CreateEntry(ctx, userID, e.Key(), CreateEntryRequest{
		Direction: opposite(e.Direction), Amount: amount, Date: date, AccountID: accountID,
		Description: description, Note: note, PersonName: &name,
	})
}

// --- transactions.DebtLinks (the wallet side was edited / deleted) ---

// TxEditedTx copies an edited wallet transaction onto its entry.
func (s *Service) TxEditedTx(ctx context.Context, tx pgx.Tx, userID, entryID uuid.UUID, row *transactions.Transaction) error {
	_, err := tx.Exec(ctx, `UPDATE personal_debts
		SET amount = $3, date = $4::date, description = $5, note = $6, updated_by_user_id = $2
		WHERE id = $1 AND user_id = $2`, entryID, userID, row.Amount, row.Date, row.Description, row.Note)
	if err != nil {
		return fmt.Errorf("sync entry: %w", err)
	}
	return nil
}

// TxDeletedTx deletes the entry of a deleted wallet transaction.
func (s *Service) TxDeletedTx(ctx context.Context, tx pgx.Tx, userID, entryID uuid.UUID) (string, error) {
	e, err := getEntry(ctx, tx, userID, entryID, true)
	if errors.Is(err, ErrEntryNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM personal_debts WHERE id = $1`, entryID); err != nil {
		return "", fmt.Errorf("delete entry: %w", err)
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

// --- splits (hooks for the transactions module) ---

// CreateForTransactionTx is called by transactions.Service when a bill is
// created with `splits`: one split entry per person on the splitter's side
// (expense → paid: they owe me; income → received: I owe them), plus the
// linked partner's side per their notification flags (split_flow.go).
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
	// Every split contact must be one of the splitter's own contacts —
	// otherwise a foreign contact id would mirror a debt into a stranger's
	// account. Reject the whole transaction.
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
		splitter, err := insertEntryTx(ctx, tx, &Entry{
			UserID: userID, Direction: direction, ContactID: in.ContactID, PersonName: in.PersonName,
			SourceTransactionID: &parentTxID, Amount: in.OwedAmount,
			Date: date, SourceKind: &kind, Description: parentDescription,
		})
		if err != nil {
			return err
		}
		if partner := linkedPartnerTx(ctx, tx, splitter); partner != nil {
			if err := s.splitToPartnerTx(ctx, tx, splitter, *partner, parentTxID); err != nil {
				return err
			}
		}
	}
	return nil
}

// --- contacts hooks (wired in main.go) ---

// UnlinkedNames implements contacts.UnlinkedNamesProvider.
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

// AbsorbForContact implements contacts.SplitsAbsorber.
func (s *Service) AbsorbForContact(ctx context.Context, userID, contactID uuid.UUID, names []string) (int, error) {
	return s.store.AbsorbForContact(ctx, userID, contactID, names)
}

// RestoreOnContactDeleteTx implements contacts.SplitsRestorer.
func (s *Service) RestoreOnContactDeleteTx(ctx context.Context, tx pgx.Tx, userID, contactID uuid.UUID) (int, error) {
	return s.store.RestoreOnContactDeleteTx(ctx, tx, userID, contactID)
}
