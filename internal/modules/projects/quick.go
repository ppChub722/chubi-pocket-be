package projects

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

var (
	// ErrQuickTxNotFound — a listed transaction_id doesn't exist or isn't the caller's (404
	// TX_NOT_FOUND; not-owned = not-found, no info leak).
	ErrQuickTxNotFound         = errors.New("transaction not found or not owned")
	ErrQuickTxAlreadyInProject = errors.New("transaction is already in a project")
	// ErrQuickTxNotBillable — transfer rows can't become board parents (project_transactions.type is
	// expense|income only).
	ErrQuickTxNotBillable = errors.New("only expense and income transactions can be pulled into a project")
	// ErrQuickTxIsRepayment — a debt entry's wallet transaction can't join a project (it already has
	// a source; 422 TX_NOT_BILLABLE).
	ErrQuickTxIsRepayment = errors.New("a debt entry's transaction can't be pulled into a project")
	// ErrQuickTxSystemRow — a row in a system category (opening balance, balance adjustment) can't
	// join a project (422 TX_NOT_BILLABLE).
	ErrQuickTxSystemRow  = errors.New("an opening balance or balance adjustment can't be pulled into a project")
	ErrQuickNothingToAdd = errors.New("new_transaction or transaction_ids is required")
	// ErrQuickTxSplitPaid — a split on the bill is labelled paid (its repayment is in the person's
	// ledger); its splits can't move onto a board (422 TX_SPLIT_PAID).
	ErrQuickTxSplitPaid = errors.New("a split on this transaction is labelled paid")
)

type quickBill struct {
	TxID        uuid.UUID
	AccountID   *uuid.UUID // nil: a wallet-less bill
	Type        string     // expense | income
	Amount      float64
	Currency    string
	Date        string // YYYY-MM-DD
	Description *string
	Note        *string
	TagNames    []string

	splits        []quickDebt
	clearPersonal bool
}

type quickDebt struct {
	ContactID    *uuid.UUID
	PersonName   string
	Amount       float64
	LinkedUserID *uuid.UUID // contacts.linked_user_id when ContactID is set
}

type pulled struct {
	newTxID *uuid.UUID
	rowIDs  []uuid.UUID // the board parents created, in order
}

func (s *Service) QuickCreate(
	ctx context.Context, callerUserID uuid.UUID, req QuickCreateRequest,
) (*QuickCreateEventResult, error) {
	if err := s.checkNewBill(req.NewTransaction, req.TransactionIDs); err != nil {
		return nil, err
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	p, err := s.createTx(ctx, tx, callerUserID, CreateProjectRequest{Name: req.Name, IconCode: req.IconCode})
	if err != nil {
		return nil, err
	}
	owner, err := s.store.MemberByUserIDTx(ctx, tx, p.ID, callerUserID)
	if err != nil {
		return nil, err
	}
	mb := &quickMemberBuilder{
		svc: s, tx: tx, projectID: p.ID, callerUserID: callerUserID, invite: true,
		byUser:    map[uuid.UUID]uuid.UUID{callerUserID: owner.ID},
		byContact: map[uuid.UUID]uuid.UUID{},
		byName:    map[string]uuid.UUID{},
	}
	res, err := s.pullBillsTx(ctx, tx, p, owner.ID, owner.DisplayName, mb,
		req.NewTransaction, req.TransactionIDs, req.Move, req.BillPeople)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	d, err := s.detail(ctx, callerUserID, p.ID, 1, 20)
	if err != nil {
		return nil, err
	}
	balances, err := s.newBillBalances(ctx, req.NewTransaction)
	if err != nil {
		return nil, err
	}
	return &QuickCreateEventResult{Detail: *d, NewTransactionID: res.newTxID, AccountBalances: balances}, nil
}

func (s *Service) newBillBalances(ctx context.Context, bill *transactions.CreateRequest) ([]shared.WalletBalance, error) {
	var ids []uuid.UUID
	if bill != nil && bill.AccountID != nil {
		ids = append(ids, *bill.AccountID)
	}
	return shared.LoadWalletBalances(ctx, s.store.Pool(), ids)
}

func (s *Service) AddBills(
	ctx context.Context, callerUserID, projectID uuid.UUID, req AddBillsRequest,
) (*AddBillsResult, error) {
	if err := s.checkNewBill(req.NewTransaction, req.TransactionIDs); err != nil {
		return nil, err
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	res, err := s.addBillsTx(ctx, tx, callerUserID, projectID, req)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	balances, err := s.newBillBalances(ctx, req.NewTransaction)
	if err != nil {
		return nil, err
	}
	var last *uuid.UUID
	if n := len(res.rowIDs); n > 0 {
		last = &res.rowIDs[n-1]
	}
	p, err := s.patch(ctx, callerUserID, projectID, last, nil, nil)
	if err != nil {
		return nil, err
	}
	p.AccountBalances = balances
	rows, err := s.boardRows(ctx, callerUserID, projectID, res.rowIDs)
	if err != nil {
		return nil, err
	}
	return &AddBillsResult{EventPatch: *p, NewTransactionID: res.newTxID, Rows: rows}, nil
}

// AddNewBillTx — a pending event draft: one new bill (with its payer / members when set). It answers
// ErrNoShareToCopy when someone else paid and I have no share (no transaction of mine to record).
func (s *Service) AddNewBillTx(
	ctx context.Context, tx pgx.Tx, callerUserID, projectID uuid.UUID, bill transactions.CreateRequest, people BillPeople,
) (uuid.UUID, error) {
	if err := s.checkNewBill(&bill, nil); err != nil {
		return uuid.Nil, err
	}
	res, err := s.addBillsTx(ctx, tx, callerUserID, projectID, AddBillsRequest{NewTransaction: &bill, BillPeople: people})
	if err != nil {
		return uuid.Nil, err
	}
	if res.newTxID == nil {
		return uuid.Nil, ErrNoShareToCopy
	}
	return *res.newTxID, nil
}

func (s *Service) addBillsTx(
	ctx context.Context, tx pgx.Tx, callerUserID, projectID uuid.UUID, req AddBillsRequest,
) (*pulled, error) {
	p, me, mb, err := s.billWriterTx(ctx, tx, callerUserID, projectID)
	if err != nil {
		return nil, err
	}
	return s.pullBillsTx(ctx, tx, p, me.ID, me.DisplayName, mb,
		req.NewTransaction, req.TransactionIDs, req.Move, req.BillPeople)
}

// billWriterTx — the event, my member row and a member builder seeded with its members, for a
// caller who may add bills.
func (s *Service) billWriterTx(
	ctx context.Context, tx pgx.Tx, callerUserID, projectID uuid.UUID,
) (*Project, *ProjectMember, *quickMemberBuilder, error) {
	if err := s.store.AssertWritable(ctx, projectID, "create_pt"); err != nil {
		return nil, nil, nil, err
	}
	me, err := s.store.AssertCanWrite(ctx, projectID, callerUserID)
	if err != nil {
		return nil, nil, nil, err
	}
	p, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return nil, nil, nil, err
	}
	existing, err := s.store.ListMembers(ctx, projectID)
	if err != nil {
		return nil, nil, nil, err
	}
	mb := &quickMemberBuilder{
		svc: s, tx: tx, projectID: projectID, callerUserID: callerUserID,
		byUser:    map[uuid.UUID]uuid.UUID{},
		byContact: map[uuid.UUID]uuid.UUID{},
		byName:    map[string]uuid.UUID{},
	}
	for _, m := range existing {
		if m.Status == MemberStatusLeft {
			continue
		}
		if m.UserID != nil {
			mb.byUser[*m.UserID] = m.ID
		} else {
			mb.byName[strings.ToLower(strings.TrimSpace(m.DisplayName))] = m.ID
		}
	}
	var kind string
	if err := tx.QueryRow(ctx, `SELECT kind FROM projects WHERE id = $1`, projectID).Scan(&kind); err != nil {
		return nil, nil, nil, fmt.Errorf("event kind: %w", err)
	}
	mb.invite = kind == KindEvent
	// Invited, not accepted yet (F2): the same person is never invited twice, also after a reject.
	invited, err := tx.Query(ctx, `SELECT invited_user_id, id FROM project_members
		WHERE project_id = $1 AND user_id IS NULL AND invited_user_id IS NOT NULL AND status <> 'left'`, projectID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("invited members: %w", err)
	}
	defer invited.Close()
	for invited.Next() {
		var u, id uuid.UUID
		if err := invited.Scan(&u, &id); err != nil {
			return nil, nil, nil, err
		}
		if _, ok := mb.byUser[u]; !ok {
			mb.byUser[u] = id
		}
	}
	if err := invited.Err(); err != nil {
		return nil, nil, nil, err
	}
	return p, me, mb, nil
}

// dispatchAddedTx — ONE project_invite to each linked person a member builder added to an event
// (F2; project_added is retired): already answered when their auto-accept switch linked them.
func (s *Service) dispatchAddedTx(ctx context.Context, tx pgx.Tx, p *Project, mb *quickMemberBuilder, actorName string) error {
	if s.notifs == nil {
		return nil
	}
	for _, in := range mb.invites {
		if err := s.notifs.DispatchProjectInvite(ctx, tx, in.userID, mb.callerUserID,
			notifications.ProjectInvitePayload{
				ProjectMemberID:    in.memberID,
				ProjectID:          p.ID,
				ProjectName:        p.Name,
				InviterUserID:      mb.callerUserID,
				InviterDisplayName: actorName,
				Role:               RoleContributor,
			}, in.accepted); err != nil {
			return err
		}
	}
	mb.invites = nil
	return nil
}

// checkNewBill — at least one bill; a new bill must be a plain expense / income.
func (s *Service) checkNewBill(req *transactions.CreateRequest, txIDs []uuid.UUID) error {
	if req == nil {
		if len(txIDs) == 0 {
			return ErrQuickNothingToAdd
		}
		return nil
	}
	if s.txs == nil {
		return errors.New("transactions module not wired")
	}
	if req.Type != transactions.TypeExpense && req.Type != transactions.TypeIncome {
		return ErrQuickTxNotBillable
	}
	if req.SourceProjectTransactionID != nil {
		return transactions.ErrProjectIDNotAllowed
	}
	return nil
}

func (s *Service) pullBillsTx(
	ctx context.Context, tx pgx.Tx, p *Project, actorMemberID uuid.UUID, actorName string,
	mb *quickMemberBuilder, newReq *transactions.CreateRequest, txIDs []uuid.UUID, move bool,
	people BillPeople,
) (*pulled, error) {
	callerUserID := mb.callerUserID
	bills := []quickBill{}
	seen := map[uuid.UUID]bool{}
	out := &pulled{}

	if people.Set() {
		if newReq == nil {
			return nil, ErrBadBillPeople
		}
		rowID, mine, err := s.addPeopleBillTx(ctx, tx, p, actorMemberID, mb, *newReq, people, nil)
		if err != nil {
			return nil, err
		}
		out.rowIDs = append(out.rowIDs, rowID)
		out.newTxID = mine
		if mine != nil {
			seen[*mine] = true
		}
		newReq = nil
	}

	if newReq != nil {
		req := *newReq
		req.Splits, req.MyShare = nil, nil
		boardSplits, err := quickDebtsFromInputsTx(ctx, tx, callerUserID, newReq.Splits)
		if err != nil {
			return nil, err
		}
		created, err := s.txs.CreateInTx(ctx, tx, callerUserID, req)
		if err != nil {
			return nil, err
		}
		newDetail, ok := created.(*transactions.TransactionDetail)
		if !ok {
			return nil, ErrQuickTxNotBillable // unreachable: transfer rejected above
		}
		newBill, err := s.quickBillFromNew(ctx, tx, newDetail)
		if err != nil {
			return nil, err
		}
		if newBill.TagNames, err = personalTagNamesTx(ctx, tx, callerUserID, newBill.TxID); err != nil {
			return nil, err
		}
		newBill.splits = boardSplits
		bills = append(bills, *newBill)
		seen[newBill.TxID] = true
		out.newTxID = &newBill.TxID
	}

	for _, txID := range txIDs {
		if seen[txID] {
			continue
		}
		seen[txID] = true
		bill, err := s.loadQuickBillTx(ctx, tx, callerUserID, txID, p.ID, move)
		if err != nil {
			return nil, err
		}
		if bill.TagNames, err = personalTagNamesTx(ctx, tx, callerUserID, txID); err != nil {
			return nil, err
		}
		bills = append(bills, *bill)
	}

	for _, bill := range bills {
		for _, d := range bill.splits {
			if _, err := mb.memberForDebt(ctx, d); err != nil {
				return nil, err
			}
		}
		if bill.AccountID != nil {
			if err := mb.addSharedWalletMembers(ctx, *bill.AccountID); err != nil {
				return nil, err
			}
		}
	}

	for _, bill := range bills {
		splits := make([]ProjectSplitInput, 0, len(bill.splits))
		for _, d := range bill.splits {
			memberID, err := mb.memberForDebt(ctx, d) // cached — no re-insert
			if err != nil {
				return nil, err
			}
			splits = append(splits, ProjectSplitInput{MemberID: memberID, Amount: d.Amount})
		}
		pt, err := s.store.InsertPTTx(ctx, tx, p.ID, callerUserID, CreateProjectTransactionRequest{
			TransactionMemberID: actorMemberID,
			Type:                bill.Type,
			Amount:              bill.Amount,
			Date:                bill.Date,
			Description:         bill.Description,
			Note:                bill.Note,
			TagNames:            bill.TagNames,
			Splits:              splits,
			LinkedTransactionID: &bill.TxID,
		})
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE transactions
			SET project_id = $1, source_project_transaction_id = $2, updated_by_user_id = $3
			WHERE id = $4`,
			p.ID, pt.ID, callerUserID, bill.TxID); err != nil {
			return nil, fmt.Errorf("link bill %s: %w", bill.TxID, err)
		}
		if bill.clearPersonal {
			if err := s.txs.ClearSplitsTx(ctx, tx, callerUserID, bill.TxID); err != nil {
				return nil, fmt.Errorf("move splits of bill %s: %w", bill.TxID, err)
			}
		}
		out.rowIDs = append(out.rowIDs, pt.ID)
	}

	if err := s.dispatchAddedTx(ctx, tx, p, mb, actorName); err != nil {
		return nil, err
	}
	return out, nil
}

const billCurrencySQL = `SELECT COALESCE(
	(SELECT currency FROM accounts WHERE id = $1),
	(SELECT currency FROM users WHERE id = $2), 'THB')`

func (s *Service) quickBillFromNew(
	ctx context.Context, tx pgx.Tx, d *transactions.TransactionDetail,
) (*quickBill, error) {
	var currency string
	if err := tx.QueryRow(ctx, billCurrencySQL, d.AccountID, d.UserID).Scan(&currency); err != nil {
		return nil, fmt.Errorf("new bill currency: %w", err)
	}
	return &quickBill{
		TxID:        d.ID,
		AccountID:   d.AccountID,
		Type:        string(d.Type),
		Amount:      d.Amount,
		Currency:    currency,
		Date:        d.Date,
		Description: d.Description,
		Note:        d.Note,
	}, nil
}

// loadQuickBillTx loads + validates one listed bill under FOR UPDATE so a concurrent quick create
// can't pull the same bill into two projects.
func (s *Service) loadQuickBillTx(
	ctx context.Context, tx pgx.Tx, callerUserID, txID, targetProjectID uuid.UUID, move bool,
) (*quickBill, error) {
	var (
		bill      quickBill
		ownerID   uuid.UUID
		projectID *uuid.UUID
		repaysID  *uuid.UUID
		isSystem  bool
		paidBy    bool
	)
	err := tx.QueryRow(ctx, `
		SELECT t.id, t.user_id, t.project_id, t.account_id, t.type, t.amount,
		       COALESCE(a.currency, u.currency, 'THB'), to_char(t.date, 'YYYY-MM-DD'),
		       t.description, t.note, t.source_personal_debt_id,
		       COALESCE(c.is_system, FALSE), t.paid_by_amount IS NOT NULL
		FROM transactions t
		JOIN users u ON u.id = t.user_id
		LEFT JOIN accounts a ON a.id = t.account_id
		LEFT JOIN categories c ON c.id = t.category_id
		WHERE t.id = $1
		FOR UPDATE OF t`, txID).Scan(
		&bill.TxID, &ownerID, &projectID, &bill.AccountID, &bill.Type,
		&bill.Amount, &bill.Currency, &bill.Date, &bill.Description, &bill.Note,
		&repaysID, &isSystem, &paidBy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrQuickTxNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load bill %s: %w", txID, err)
	}
	if ownerID != callerUserID {
		return nil, ErrQuickTxNotFound // not-owned = not-found (no info leak)
	}
	if bill.Type != PTTypeExpense && bill.Type != PTTypeIncome {
		return nil, ErrQuickTxNotBillable
	}
	if repaysID != nil {
		return nil, ErrQuickTxIsRepayment
	}
	if isSystem {
		return nil, ErrQuickTxSystemRow
	}
	if paidBy {
		return nil, transactions.ErrLinkModeConflict // someone paid it for me: one way at a time
	}
	if projectID != nil {
		if !move || *projectID == targetProjectID {
			return nil, ErrQuickTxAlreadyInProject
		}
		carried, err := s.detachBillTx(ctx, tx, callerUserID, txID, false)
		if err != nil {
			return nil, err
		}
		if carried != nil {
			bill.splits = carried
			return &bill, nil
		}
	}
	debts, err := s.loadQuickDebtsTx(ctx, tx, callerUserID, txID)
	if err != nil {
		return nil, err
	}
	bill.splits, bill.clearPersonal = debts, len(debts) > 0
	return &bill, nil
}

func quickDebtsFromInputsTx(
	ctx context.Context, tx pgx.Tx, callerUserID uuid.UUID, inputs []transactions.SplitInput,
) ([]quickDebt, error) {
	out := make([]quickDebt, 0, len(inputs))
	for _, in := range inputs {
		d := quickDebt{ContactID: in.ContactID, PersonName: in.PersonName, Amount: in.OwedAmount}
		if in.ContactID != nil {
			err := tx.QueryRow(ctx, `SELECT linked_user_id FROM contacts WHERE id = $1 AND user_id = $2`,
				*in.ContactID, callerUserID).Scan(&d.LinkedUserID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, transactions.ErrSplitContactNotFound
			}
			if err != nil {
				return nil, fmt.Errorf("split contact: %w", err)
			}
		}
		out = append(out, d)
	}
	return out, nil
}

func (s *Service) loadQuickDebtsTx(
	ctx context.Context, tx pgx.Tx, callerUserID, txID uuid.UUID,
) ([]quickDebt, error) {
	rows, err := tx.Query(ctx, `
		SELECT pd.counterparty_contact_id, pd.counterparty_person_name, pd.amount,
		       c.linked_user_id, pd.paid
		FROM personal_debts pd
		LEFT JOIN contacts c ON c.id = pd.counterparty_contact_id
		WHERE pd.user_id = $1
		  AND pd.source_transaction_id = $2
		ORDER BY pd.created_at`, callerUserID, txID)
	if err != nil {
		return nil, fmt.Errorf("debts of bill %s: %w", txID, err)
	}
	defer rows.Close()
	out := make([]quickDebt, 0)
	for rows.Next() {
		var (
			d    quickDebt
			paid bool
		)
		if err := rows.Scan(&d.ContactID, &d.PersonName, &d.Amount, &d.LinkedUserID, &paid); err != nil {
			return nil, err
		}
		if paid {
			return nil, ErrQuickTxSplitPaid
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

type quickMemberBuilder struct {
	svc          *Service
	tx           pgx.Tx
	projectID    uuid.UUID
	callerUserID uuid.UUID

	byUser    map[uuid.UUID]uuid.UUID // user_id    -> member id
	byContact map[uuid.UUID]uuid.UUID // contact_id -> member id
	byName    map[string]uuid.UUID    // lower(name)-> member id

	// invite — an event (F2): a linked person joins as a plain member and gets an invite; a split
	// bill links them at once.
	invite  bool
	invites []pendingInvite
}

type pendingInvite struct {
	userID, memberID uuid.UUID
	accepted         bool
}

func (b *quickMemberBuilder) memberForDebt(ctx context.Context, d quickDebt) (uuid.UUID, error) {
	if d.LinkedUserID != nil && *d.LinkedUserID != b.callerUserID {
		return b.ensureLinked(ctx, *d.LinkedUserID, d.PersonName)
	}
	if d.ContactID != nil {
		if id, ok := b.byContact[*d.ContactID]; ok {
			return id, nil
		}
		id, err := b.ensureAdHoc(ctx, d.PersonName)
		if err != nil {
			return uuid.Nil, err
		}
		b.byContact[*d.ContactID] = id
		return id, nil
	}
	return b.ensureAdHoc(ctx, d.PersonName)
}

func (b *quickMemberBuilder) ensureLinked(
	ctx context.Context, userID uuid.UUID, displayName string,
) (uuid.UUID, error) {
	if id, ok := b.byUser[userID]; ok {
		return id, nil
	}
	if strings.TrimSpace(displayName) == "" {
		displayName = userDisplayOrEmpty(ctx, b.tx, userID)
		if displayName == "" {
			displayName = "Member"
		}
	}
	linked, accepted := &userID, false
	if b.invite {
		accepted = b.autoAcceptsInvite(ctx, userID)
		if !accepted {
			linked = nil
		}
	}
	m, err := b.svc.store.InsertMemberTx(ctx, b.tx,
		b.projectID, linked, displayName, RoleContributor, MemberStatusActive,
		b.callerUserID, nil,
	)
	if err != nil {
		return uuid.Nil, err
	}
	b.byUser[userID] = m.ID
	if b.invite {
		if _, err := b.tx.Exec(ctx, `UPDATE project_members SET invited_user_id = $2 WHERE id = $1`, m.ID, userID); err != nil {
			return uuid.Nil, fmt.Errorf("invited member: %w", err)
		}
		b.invites = append(b.invites, pendingInvite{userID: userID, memberID: m.ID, accepted: accepted})
	}
	return m.ID, nil
}

// autoAcceptsInvite — the invitee's "เชิญเข้าอีเวนต์" auto-accept switch (F2).
func (b *quickMemberBuilder) autoAcceptsInvite(ctx context.Context, userID uuid.UUID) bool {
	if b.svc.notifs == nil {
		return false
	}
	d, err := b.svc.notifs.DeliveryFor(ctx, b.tx, userID, &b.callerUserID, notifications.TypeProjectInvite)
	return err == nil && d.AcceptRequest
}

func (b *quickMemberBuilder) ensureAdHoc(ctx context.Context, name string) (uuid.UUID, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Member"
	}
	key := strings.ToLower(name)
	if id, ok := b.byName[key]; ok {
		return id, nil
	}
	m, err := b.svc.store.InsertMemberTx(ctx, b.tx,
		b.projectID, nil, name, RoleContributor, MemberStatusActive,
		b.callerUserID, nil,
	)
	if err != nil {
		return uuid.Nil, err
	}
	b.byName[key] = m.ID
	return m.ID, nil
}

func (b *quickMemberBuilder) addSharedWalletMembers(ctx context.Context, accountID uuid.UUID) error {
	rows, err := b.tx.Query(ctx, `
		SELECT am.user_id, u.display_name
		FROM account_members am
		JOIN users u ON u.id = am.user_id
		WHERE am.account_id = $1
		  AND am.joined_at IS NOT NULL AND am.left_at IS NULL`, accountID)
	if err != nil {
		return fmt.Errorf("wallet members of %s: %w", accountID, err)
	}
	defer rows.Close()
	type walletMember struct {
		userID uuid.UUID
		name   string
	}
	members := make([]walletMember, 0, 2)
	for rows.Next() {
		var m walletMember
		if err := rows.Scan(&m.userID, &m.name); err != nil {
			return err
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(members) <= 1 {
		return nil // personal wallet — nothing to add
	}
	for _, m := range members {
		if m.userID == b.callerUserID {
			continue
		}
		if _, err := b.ensureLinked(ctx, m.userID, m.name); err != nil {
			return err
		}
	}
	return nil
}
