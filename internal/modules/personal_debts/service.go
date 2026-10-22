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
	items, total, err := s.store.listStatement(ctx, userID, k.Key(), cycle, false, page, perPage)
	if err != nil {
		return nil, err
	}
	counts, err := s.store.entryCounts(ctx, userID, k.Key())
	if err != nil {
		return nil, err
	}
	msg, err := s.summaryMessage(ctx, userID, k, bal)
	if err != nil {
		return nil, err
	}
	return &PersonPage{DebtPerson: bal, Counts: counts, Items: items, SummaryMessage: msg, Pagination: shared.NewPage(page, perPage, total)}, nil
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
	createTx := req.AccountID != nil
	if req.CreateTransaction != nil {
		createTx = *req.CreateTransaction
	}
	if req.CreateTransaction == nil && req.AccountID == nil && len(req.TagIDs) > 0 {
		return nil, ErrTagsNeedWallet // the old contract, for clients that don't send create_transaction
	}
	if !createTx && (req.AccountID != nil || len(req.TagIDs) > 0) {
		return nil, ErrLedgerOnlyNoWallet
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
	entry := &Entry{
		UserID: userID, Direction: req.Direction, ContactID: contactID, PersonName: name,
		Amount: req.Amount, Date: date, Description: req.Description, Note: req.Note,
	}
	if req.BillID != nil {
		// The event resolve sheet's "as debt" (0.4.0-3 B3): the debt keeps its bill and event.
		var projectID uuid.UUID
		err := tx.QueryRow(ctx, `SELECT pt.project_id FROM project_transactions pt
			JOIN project_members pm ON pm.project_id = pt.project_id AND pm.user_id = $2 AND pm.status = 'active'
			WHERE pt.id = $1`, *req.BillID, userID).Scan(&projectID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrBillNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("debt bill: %w", err)
		}
		kind := SourceEvent
		entry.SourceKind, entry.SourceProjectTransactionID, entry.ProjectID = &kind, req.BillID, &projectID
	}
	e, err := insertEntryTx(ctx, tx, entry)
	if err != nil {
		return nil, err
	}
	var touched []uuid.UUID
	if createTx {
		if _, err := s.txs.CreateDebtTxInTx(ctx, tx, userID, e.ID, transactions.DebtTxInput{
			Direction: e.Direction, AccountID: req.AccountID, Amount: e.Amount, Date: e.Date,
			Description: e.Description, Note: e.Note, TagIDs: req.TagIDs,
		}); err != nil {
			return nil, err
		}
		if req.AccountID != nil {
			touched = append(touched, *req.AccountID)
		}
	}
	if err := markPaidTx(ctx, tx, userID, e.Key(), req.MarkPaidIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.writeResult(ctx, userID, e.ID, touched, nil)
}

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
	// A split-sourced entry's money belongs to its source; an event-resolve debt (source event) is
	// my own record of the bill and stays editable.
	if e.SourceKind != nil && *e.SourceKind != SourceEvent && req.moneyChange() {
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
	return s.writeResult(ctx, userID, e.ID, touched, &prevKey)
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
	var touched []uuid.UUID
	if txID != nil {
		acc, err := s.txs.DeleteDebtTxInTx(ctx, tx, userID, *txID)
		if err != nil {
			return nil, err
		}
		touched = nonNil(acc)
	}
	if e.PaidForTransactionID != nil {
		// A partner's split on me: my expense of it goes with it (it has no wallet).
		acc, err := s.txs.DeleteInTx(ctx, tx, userID, *e.PaidForTransactionID)
		if err != nil && !errors.Is(err, transactions.ErrTxNotFound) {
			return nil, err
		}
		touched = append(touched, acc...)
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

// CreateRepayment — POST /personal-debts/people/{key}/repayments: ONE transaction tied to the person
// (pay = expense in Debt Paid, receive = income in Debt Received). It counts in the person's
// balance (pay +, receive −); mark_paid_ids only label debts paid.
func (s *Service) CreateRepayment(ctx context.Context, userID uuid.UUID, rawKey string, req RepaymentRequest) (*RepaymentResult, error) {
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
	row, err := s.txs.CreateRepaymentInTx(ctx, tx, userID, transactions.RepayInput{
		Kind: req.Kind, ContactID: contactID, PersonName: strings.TrimSpace(name), AccountID: req.AccountID,
		Amount: req.Amount, Date: date, Description: req.Description, Note: req.Note, TagIDs: req.TagIDs,
	})
	if err != nil {
		return nil, err
	}
	key := shared.DebtKey(contactID, name)
	if err := markPaidTx(ctx, tx, userID, key, req.MarkPaidIDs); err != nil {
		return nil, err
	}
	if contactID != nil {
		_, _ = tx.Exec(ctx, `UPDATE contacts SET last_used_at = NOW() WHERE id = $1 AND user_id = $2`, *contactID, userID)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.repaymentResult(ctx, userID, row.ID, key, nonNil(req.AccountID))
}

func (s *Service) repaymentResult(ctx context.Context, userID, txID uuid.UUID, key string, touched []uuid.UUID) (*RepaymentResult, error) {
	out := &RepaymentResult{}
	var err error
	if out.Transaction, err = s.txs.Get(ctx, userID, txID); err != nil {
		return nil, err
	}
	items, err := loadRepayments(ctx, s.store.db, userID, []uuid.UUID{txID})
	if err != nil {
		return nil, err
	}
	if len(items) == 1 {
		out.Item = items[0]
		mark, err := s.store.cycleBoundary(ctx, s.store.db, userID, key)
		if err != nil {
			return nil, err
		}
		cur := mark.after(out.Item.Date, out.Item.CreatedAt, out.Item.ID)
		out.Item.InCurrentCycle = &cur
	}
	if out.PersonBalance, err = s.balanceOfKey(ctx, s.store.db, userID, key); err != nil {
		return nil, err
	}
	if out.AccountBalances, err = shared.LoadWalletBalances(ctx, s.store.db, touched); err != nil {
		return nil, err
	}
	return out, nil
}

// RepayEntry — a pending settle_debt draft: a repayment TRANSACTION to / from the debt's person
// (a paid debt → they pay me back = receive; a received debt → I pay back = pay). The debt itself
// is not touched (the paid label stays the user's call).
func (s *Service) RepayEntry(ctx context.Context, userID, targetID uuid.UUID, amount float64, accountID *uuid.UUID,
	date, description, note *string) (*RepaymentResult, error) {
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
