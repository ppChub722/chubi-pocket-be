package projects

// Quick create from existing bills (spec §10/4.24; pinned contract in
// design/api/api-document.md §10 "Quick create").
//
// One atomic database transaction:
//  1. create the project (+ caller's owner member row)
//  2. create the new bill through the NORMAL personal-transaction path
//     (account balance moves, splits create personal_debts as usual)
//  3. validate every listed bill: exists, caller's own, project_id IS NULL
//  4. auto-add members — union of split counterparties across all included
//     bills (linked → active linked member + 'project_added' notification,
//     contact/free-text → ad-hoc member) plus active members of any shared
//     wallet a bill lives on; deduped
//  5. every included bill gets a project_transactions parent + split
//     children mirroring its split debts (either direction), then the personal row is
//     linked back (project_id + source_project_transaction_id) — born
//     auto-claimed. Settled debt state is never modified; the bills'
//     personal_debts gain project_id only.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

var (
	// ErrQuickTxNotFound — a listed transaction_id doesn't exist or isn't
	// the caller's (404 TX_NOT_FOUND; not-owned = not-found, no info leak).
	ErrQuickTxNotFound = errors.New("transaction not found or not owned")
	// ErrQuickTxAlreadyInProject — a listed bill already carries a
	// project_id (409 TX_ALREADY_IN_PROJECT).
	ErrQuickTxAlreadyInProject = errors.New("transaction is already in a project")
	// ErrQuickTxNotBillable — transfer rows can't become board parents
	// (project_transactions.type is expense|income only).
	ErrQuickTxNotBillable = errors.New("only expense and income transactions can be pulled into a project")
)

// quickDefaultType is the project type stamped on quick-created projects.
// The FE flow has no type picker; 'other' is the neutral bucket and the
// owner can change it later via the regular PUT /v1/projects/:id.
const quickDefaultType = "other"

// quickBill is one included bill (the new one or a listed one) normalized
// for board-parent creation.
type quickBill struct {
	TxID      uuid.UUID
	AccountID uuid.UUID
	Type      string // expense | income
	Amount    float64
	Currency  string
	Date      string // YYYY-MM-DD
	Description *string
	Note      *string
}

// quickDebt is one of the caller's personal_debts rows hanging off a bill.
type quickDebt struct {
	ContactID    *uuid.UUID
	PersonName   string
	Amount       float64
	LinkedUserID *uuid.UUID // contacts.linked_user_id when ContactID is set
}

// QuickCreate implements POST /v1/projects/quick. Everything runs in ONE
// database transaction — the first failure rolls back the project, the new
// bill, the members, and every board row.
func (s *Service) QuickCreate(
	ctx context.Context, callerUserID uuid.UUID, req QuickCreateRequest,
) (*QuickCreateResponse, error) {
	if err := s.checkNewBill(&req.NewTransaction); err != nil {
		return nil, err
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	// --- 1. Project + caller's owner member row (same path as Create). ---
	defaultType := quickDefaultType
	p, err := s.store.CreateInTx(ctx, tx, callerUserID, CreateProjectRequest{
		Name:     req.Name,
		Type:     &defaultType,
		IconCode: req.IconCode,
	})
	if err != nil {
		return nil, err
	}
	ownerName := userDisplayOrEmpty(ctx, tx, callerUserID)
	if ownerName == "" {
		ownerName = "Owner"
	}
	ownerMember, err := s.store.InsertMemberTx(ctx, tx,
		p.ID, &callerUserID, ownerName, RoleOwner, MemberStatusActive, callerUserID, nil,
	)
	if err != nil {
		return nil, err
	}

	mb := &quickMemberBuilder{
		svc: s, tx: tx, projectID: p.ID, callerUserID: callerUserID,
		byUser:    map[uuid.UUID]uuid.UUID{callerUserID: ownerMember.ID},
		byContact: map[uuid.UUID]uuid.UUID{},
		byName:    map[string]uuid.UUID{},
	}
	newTxID, linked, err := s.pullBillsTx(ctx, tx, p, ownerMember.ID, ownerName, mb,
		req.NewTransaction, req.TransactionIDs)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	// Re-fetch so members_count is populated, same as Create.
	full, err := s.store.GetByID(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	return &QuickCreateResponse{Project: *full, LinkedCount: linked, TransactionID: newTxID}, nil
}

// AddBills implements POST /v1/projects/:id/bills — the same bill pull as
// quick create, into a project that already exists ("add to an event",
// owner request 2026-10-08). The caller must be an active, non-viewer
// member of a writable project; the new bill's actor is the caller's own
// member row. Split counterparties not yet in the project are added.
func (s *Service) AddBills(
	ctx context.Context, callerUserID, projectID uuid.UUID, req AddBillsRequest,
) (*QuickCreateResponse, error) {
	if err := s.checkNewBill(&req.NewTransaction); err != nil {
		return nil, err
	}
	if err := s.store.AssertWritable(ctx, projectID, "create_pt"); err != nil {
		return nil, err
	}
	me, err := s.store.AssertCanWrite(ctx, projectID, callerUserID)
	if err != nil {
		return nil, err
	}
	p, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	existing, err := s.store.ListMembers(ctx, projectID)
	if err != nil {
		return nil, err
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	// Seed the builder with the current roster so a counterparty already in
	// the project (linked user, or the same name) is reused, not duplicated.
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
	newTxID, linked, err := s.pullBillsTx(ctx, tx, p, me.ID, me.DisplayName, mb,
		req.NewTransaction, req.TransactionIDs)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	full, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return &QuickCreateResponse{Project: *full, LinkedCount: linked, TransactionID: newTxID}, nil
}

// checkNewBill — the new bill must be a plain expense / income.
func (s *Service) checkNewBill(req *transactions.CreateRequest) error {
	if s.txs == nil {
		return errors.New("transactions module not wired")
	}
	if req.Type != transactions.TypeExpense && req.Type != transactions.TypeIncome {
		return ErrQuickTxNotBillable
	}
	// Linking to the board is done by this flow itself, not by the
	// claim-style create parameter.
	if req.SourceProjectTransactionID != nil {
		return transactions.ErrProjectIDNotAllowed
	}
	return nil
}

// pullBillsTx creates the new bill through the normal personal path, then
// pulls it plus the listed loose bills into project p (steps 2–6 of the
// file comment). actorMemberID is the caller's member row in p. Returns the
// new bill's id and how many bills were linked.
func (s *Service) pullBillsTx(
	ctx context.Context, tx pgx.Tx, p *Project, actorMemberID uuid.UUID, actorName string,
	mb *quickMemberBuilder, newReq transactions.CreateRequest, txIDs []uuid.UUID,
) (uuid.UUID, int, error) {
	callerUserID := mb.callerUserID

	// --- 2. New bill through the normal create path (balance + debts). ---
	created, err := s.txs.CreateInTx(ctx, tx, callerUserID, newReq)
	if err != nil {
		return uuid.Nil, 0, err
	}
	newDetail, ok := created.(*transactions.TransactionDetail)
	if !ok {
		return uuid.Nil, 0, ErrQuickTxNotBillable // unreachable: transfer rejected above
	}
	newBill, err := s.quickBillFromNew(ctx, tx, newDetail)
	if err != nil {
		return uuid.Nil, 0, err
	}

	// --- 3. Load + validate listed bills (deduped, order-preserving). ---
	bills := []quickBill{*newBill}
	seen := map[uuid.UUID]bool{newBill.TxID: true}
	for _, txID := range txIDs {
		if seen[txID] {
			continue
		}
		seen[txID] = true
		bill, err := s.loadQuickBillTx(ctx, tx, callerUserID, txID)
		if err != nil {
			return uuid.Nil, 0, err
		}
		bills = append(bills, *bill)
	}

	// --- 4. Member set: union across all included bills. ---
	billDebts := make([][]quickDebt, len(bills))
	for i, bill := range bills {
		debts, err := s.loadQuickDebtsTx(ctx, tx, callerUserID, bill.TxID)
		if err != nil {
			return uuid.Nil, 0, err
		}
		billDebts[i] = debts
		for _, d := range debts {
			if _, err := mb.memberForDebt(ctx, d); err != nil {
				return uuid.Nil, 0, err
			}
		}
		if err := mb.addSharedWalletMembers(ctx, bill.AccountID); err != nil {
			return uuid.Nil, 0, err
		}
	}

	// --- 5. Board parents + split children; link the personal rows. ---
	linked := 0
	for i, bill := range bills {
		splits := make([]ProjectSplitInput, 0, len(billDebts[i]))
		for _, d := range billDebts[i] {
			memberID, err := mb.memberForDebt(ctx, d) // cached — no re-insert
			if err != nil {
				return uuid.Nil, 0, err
			}
			splits = append(splits, ProjectSplitInput{MemberID: memberID, Amount: d.Amount})
		}
		pt, err := s.store.InsertPTTx(ctx, tx, p.ID, callerUserID, CreateProjectTransactionRequest{
			TransactionMemberID: actorMemberID,
			Type:                bill.Type,
			Amount:              bill.Amount,
			Currency:            bill.Currency,
			Date:                bill.Date,
			Description:         bill.Description,
			Note:                bill.Note,
			Splits:              splits,
		})
		if err != nil {
			return uuid.Nil, 0, err
		}
		// Auto-claim: link the personal row back to its board parent. The
		// board row is born already resolved — no money moves here.
		if _, err := tx.Exec(ctx, `
			UPDATE transactions
			SET project_id = $1, source_project_transaction_id = $2, updated_by_user_id = $3
			WHERE id = $4`,
			p.ID, pt.ID, callerUserID, bill.TxID); err != nil {
			return uuid.Nil, 0, fmt.Errorf("link bill %s: %w", bill.TxID, err)
		}
		// Settled means settled: the bill's debt rows gain project_id for
		// traceability ONLY — settled_amount/status are never touched.
		if _, err := tx.Exec(ctx, `
			UPDATE personal_debts
			SET project_id = $1, updated_by_user_id = $2
			WHERE user_id = $2 AND source_transaction_id = $3`,
			p.ID, callerUserID, bill.TxID); err != nil {
			return uuid.Nil, 0, fmt.Errorf("tag debts of bill %s: %w", bill.TxID, err)
		}
		linked++
	}

	// --- 6. Informational 'project_added' notifications (no actions). ---
	if s.notifs != nil {
		for _, added := range mb.notifyLinked {
			if err := s.notifs.DispatchProjectAdded(ctx, tx, added.userID, callerUserID,
				notifications.ProjectAddedPayload{
					ProjectMemberID:  added.memberID,
					ProjectID:        p.ID,
					ProjectName:      p.Name,
					AdderUserID:      callerUserID,
					AdderDisplayName: actorName,
				}); err != nil {
				return uuid.Nil, 0, err
			}
		}
	}
	return newBill.TxID, linked, nil
}


// quickBillFromNew normalizes the just-created personal transaction. The
// transactions row carries no currency column — it's the account's.
func (s *Service) quickBillFromNew(
	ctx context.Context, tx pgx.Tx, d *transactions.TransactionDetail,
) (*quickBill, error) {
	if d.AccountID == nil {
		return nil, fmt.Errorf("quick create requires a transaction with an account")
	}
	var currency string
	if err := tx.QueryRow(ctx,
		`SELECT currency FROM accounts WHERE id = $1`, *d.AccountID).Scan(&currency); err != nil {
		return nil, fmt.Errorf("new bill currency: %w", err)
	}
	return &quickBill{
		TxID:      d.ID,
		AccountID: *d.AccountID,
		Type:      string(d.Type),
		Amount:    d.Amount,
		Currency:  currency,
		Date:      d.Date,
		Description: d.Description,
		Note:      d.Note,
	}, nil
}

// loadQuickBillTx loads + validates one listed bill under FOR UPDATE so a
// concurrent quick create can't pull the same bill into two projects.
func (s *Service) loadQuickBillTx(
	ctx context.Context, tx pgx.Tx, callerUserID, txID uuid.UUID,
) (*quickBill, error) {
	var (
		bill      quickBill
		ownerID   uuid.UUID
		projectID *uuid.UUID
	)
	err := tx.QueryRow(ctx, `
		SELECT t.id, t.user_id, t.project_id, t.account_id, t.type, t.amount,
		       a.currency, to_char(t.date, 'YYYY-MM-DD'), t.description, t.note
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id
		WHERE t.id = $1
		FOR UPDATE OF t`, txID).Scan(
		&bill.TxID, &ownerID, &projectID, &bill.AccountID, &bill.Type,
		&bill.Amount, &bill.Currency, &bill.Date, &bill.Description, &bill.Note,
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
	if projectID != nil {
		return nil, ErrQuickTxAlreadyInProject
	}
	if bill.Type != PTTypeExpense && bill.Type != PTTypeIncome {
		return nil, ErrQuickTxNotBillable
	}
	return &bill, nil
}

// loadQuickDebtsTx returns the caller's personal_debts hanging off the
// bill (both directions — expense splits are 'owed_to_me', income
// splits 'i_owe') with the counterparty contact's linked user resolved.
func (s *Service) loadQuickDebtsTx(
	ctx context.Context, tx pgx.Tx, callerUserID, txID uuid.UUID,
) ([]quickDebt, error) {
	rows, err := tx.Query(ctx, `
		SELECT pd.counterparty_contact_id, pd.counterparty_person_name, pd.amount,
		       c.linked_user_id
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
		var d quickDebt
		if err := rows.Scan(&d.ContactID, &d.PersonName, &d.Amount, &d.LinkedUserID); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// quickMemberBuilder dedupes the auto-added member set: the same linked
// user / contact / free-text name maps to one project_members row no matter
// how many bills it appears on. Linked members are collected for the
// 'project_added' notification fan-out.
type quickMemberBuilder struct {
	svc          *Service
	tx           pgx.Tx
	projectID    uuid.UUID
	callerUserID uuid.UUID

	byUser    map[uuid.UUID]uuid.UUID // user_id    -> member id
	byContact map[uuid.UUID]uuid.UUID // contact_id -> member id
	byName    map[string]uuid.UUID    // lower(name)-> member id

	notifyLinked []struct{ userID, memberID uuid.UUID }
}

// memberForDebt resolves (or creates) the member row for one debt's
// counterparty. Consent is implied by the underlying split — linked users
// join active immediately, no invite handshake (spec §10/4.24 step 2).
func (b *quickMemberBuilder) memberForDebt(ctx context.Context, d quickDebt) (uuid.UUID, error) {
	// Linked counterparty → active linked member. A contact linked to the
	// caller themself falls through to the contact/name branches (a member
	// row for the caller already exists — the owner).
	if d.LinkedUserID != nil && *d.LinkedUserID != b.callerUserID {
		return b.ensureLinked(ctx, *d.LinkedUserID, d.PersonName)
	}
	// Contact without a linked user → member from the contact (post-
	// migration 27 project_members carries no contact_id, so this is an
	// ad-hoc row deduped by the contact's id and carrying its name).
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
	// Bare free-text counterparty → ad-hoc member.
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
	m, err := b.svc.store.InsertMemberTx(ctx, b.tx,
		b.projectID, &userID, displayName, RoleContributor, MemberStatusActive,
		b.callerUserID, nil,
	)
	if err != nil {
		return uuid.Nil, err
	}
	b.byUser[userID] = m.ID
	b.notifyLinked = append(b.notifyLinked, struct{ userID, memberID uuid.UUID }{userID, m.ID})
	return m.ID, nil
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

// addSharedWalletMembers — when a bill's account has more than one active
// account_member, every OTHER active member joins as an active linked
// member (consent implied by the shared wallet), deduped with the split-
// derived linked members.
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
