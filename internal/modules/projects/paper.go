package projects

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// The paper core: one engine for split bills (project_id NULL) and event bills. A paper holds the
// money facts only; each user's books (a transaction, debts) are made by claim and follow the paper
// one way. Handlers check the route's contract (split bill or event bill) and call these.

// WithDebts wires the person book (claims write debts and repayments through it).
func (s *Service) WithDebts(d *personal_debts.Service) { s.debts = d }

// paperAccess — what the caller is to a paper.
type paperAccess struct {
	row    *paperRow
	event  *Project // nil = a split bill
	person *paperPerson
	member *ProjectMember // event bills: my membership
}

func (a *paperAccess) isOwner(userID uuid.UUID) bool { return a.row.Owner == userID }

// canWrite — joined on a split bill; an active, non-viewer member of an open event.
func (a *paperAccess) canWrite() bool {
	if a.event == nil {
		return a.person != nil && a.person.Status == shared.PersonJoined
	}
	return a.member != nil && a.member.Status == MemberStatusActive && a.member.Role != RoleViewer
}

// accessTx — the caller's standing on a paper (the paper locked). wantEvent: nil = either kind;
// else the paper must be a bill of that event (true) / a split bill (uuid.Nil).
func (s *Service) accessTx(ctx context.Context, tx pgx.Tx, userID, paperID uuid.UUID, eventID *uuid.UUID) (*paperAccess, error) {
	row, err := lockPaperTx(ctx, tx, paperID)
	if err != nil {
		return nil, err
	}
	if eventID != nil {
		if (*eventID == uuid.Nil) != (row.ProjectID == nil) || (row.ProjectID != nil && *row.ProjectID != *eventID) {
			return nil, ErrPaperNotFound
		}
	}
	a := &paperAccess{row: row}
	people, err := peopleTx(ctx, tx, paperID)
	if err != nil {
		return nil, err
	}
	for i := range people {
		if u := people[i].UserID; u != nil && *u == userID {
			a.person = &people[i]
		}
	}
	if row.ProjectID != nil {
		if a.event, err = s.store.GetByID(ctx, *row.ProjectID); err != nil {
			return nil, err
		}
		m, err := s.store.MemberByUserIDTx(ctx, tx, *row.ProjectID, userID)
		if err != nil || m.Status != MemberStatusActive {
			return nil, ErrPaperNotFound
		}
		a.member = m
		return a, nil
	}
	if a.person == nil || a.person.Status == shared.PersonLeft || a.person.Status == shared.PersonDeclined {
		return nil, ErrPaperNotFound
	}
	return a, nil
}

// --- write ---

// writePaperTx — the paper's people resolved and written (the paper row itself is the caller's).
// Answers the rows that went (removed by this write) and the rows that are new.
func (s *Service) writePeopleTx(ctx context.Context, tx pgx.Tx, writer uuid.UUID, paperID uuid.UUID, owner uuid.UUID,
	event *Project, in PaperInput) (gone, added []paperPerson, err error) {
	cur, err := peopleTx(ctx, tx, paperID)
	if err != nil {
		return nil, nil, err
	}
	var m *matcher
	if event != nil {
		m = newMatcher(s, tx, event, writer)
		if err := m.load(ctx); err != nil {
			return nil, nil, err
		}
	}
	want, err := s.resolvePeople(ctx, tx, writer, in.People, byID(cur), event, m)
	if err != nil {
		return nil, nil, err
	}
	if in.Type == shared.PaperPayback && event == nil {
		return nil, nil, ErrPaybackSplitOnly
	}
	if splitModeOr(in.SplitMode) == shared.SplitEqual && in.Type != shared.PaperPayback {
		equalSplit(in.Amount, want, owner)
	}
	if !balanced(in.Amount, want) {
		return nil, nil, ErrPaperNotEven
	}
	// A split bill: a person linked to another user waits for them (unless they auto-accept).
	for i := range want {
		p := &want[i]
		if p.ID != uuid.Nil || p.UserID == nil || *p.UserID == writer {
			continue
		}
		if event == nil {
			p.Status = shared.PersonInvited
			if s.autoAccept(ctx, tx, *p.UserID, writer, false) {
				p.Status = shared.PersonJoined
			}
		}
	}
	gone, err = putPeopleTx(ctx, tx, paperID, writer, want)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range want {
		if _, ok := byID(cur)[p.ID]; !ok {
			added = append(added, p)
		}
	}
	if m != nil {
		if err := m.dispatchInvites(ctx); err != nil {
			return nil, nil, err
		}
	}
	return gone, added, nil
}

// autoAccept — the user's auto-accept switch for a new paper (event bills / split bills).
func (s *Service) autoAccept(ctx context.Context, tx pgx.Tx, userID, actor uuid.UUID, event bool) bool {
	if s.notifs == nil {
		return false
	}
	d, err := s.notifs.DeliveryFor(ctx, tx, userID, &actor, notifications.TypeBillInvite)
	if err != nil {
		return false
	}
	if event {
		return d.AcceptEvents
	}
	return d.AcceptDebts
}

// autoSync — the user's auto-sync switch for a paper of this kind.
func (s *Service) autoSync(ctx context.Context, tx pgx.Tx, userID, actor uuid.UUID, event bool) bool {
	if s.notifs == nil {
		return true
	}
	d, err := s.notifs.DeliveryFor(ctx, tx, userID, &actor, notifications.TypeBillChanged)
	if err != nil {
		return false
	}
	if event {
		return d.SyncEvents
	}
	return d.SyncDebts
}

// createPaper — a new paper; the creator owns it and claims it at once (my_books, else auto).
func (s *Service) createPaper(ctx context.Context, writer uuid.UUID, projectID *uuid.UUID, req CreatePaperRequest) (*PaperWriteResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	var event *Project
	if projectID != nil {
		if event, err = s.store.GetByID(ctx, *projectID); err != nil {
			return nil, err
		}
		if err := writable(event.Status, "create_pt"); err != nil {
			return nil, err
		}
		m, err := s.store.MemberByUserIDTx(ctx, tx, *projectID, writer)
		if err != nil || m.Status != MemberStatusActive {
			return nil, ErrProjectNotFound
		}
		if m.Role == RoleViewer {
			return nil, ErrViewerReadOnly
		}
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}
	if err := insertPaperTx(ctx, tx, id, projectID, writer, req.Paper); err != nil {
		return nil, err
	}
	_, added, err := s.writePeopleTx(ctx, tx, writer, id, writer, event, req.Paper)
	if err != nil {
		return nil, err
	}
	if event != nil {
		if err := setPaperTagsTx(ctx, tx, id, event.ID, req.Paper.TagIDs); err != nil {
			return nil, err
		}
	}
	eff := shared.NewEffects()
	paper, _, err := shared.LoadPaper(ctx, tx, writer, id)
	if err != nil {
		return nil, err
	}
	if paper.PersonOfUser(writer) != nil {
		claim := ClaimInput{Pay: "debt"}
		if req.MyBooks != nil {
			claim = *req.MyBooks
		}
		if _, err := s.claimTx(ctx, tx, writer, paper, event, claim, req.MyBooks == nil, &eff); err != nil {
			return nil, err
		}
	}
	if err := s.welcomeTx(ctx, tx, writer, paper, event, added); err != nil {
		return nil, err
	}
	return s.finishWrite(ctx, tx, writer, id, eff)
}

// welcomeTx — the people new on a paper hear about it: a split bill invites (or, auto-accepted,
// joins and claims); an event bill asks the members to claim (or claims for them).
func (s *Service) welcomeTx(ctx context.Context, tx pgx.Tx, writer uuid.UUID, paper shared.Paper, event *Project, added []paperPerson) error {
	for _, p := range added {
		if p.UserID == nil || *p.UserID == writer {
			continue
		}
		user := *p.UserID
		auto := s.autoAccept(ctx, tx, user, writer, event != nil)
		applied := false
		if auto && (event != nil || p.Status == shared.PersonJoined) {
			view, _, err := shared.LoadPaper(ctx, tx, user, paper.ID)
			if err != nil {
				return err
			}
			if _, err := s.claimTx(ctx, tx, user, view, event, ClaimInput{Pay: "debt"}, true, nil); err != nil &&
				!errors.Is(err, ErrNothingToClaim) {
				return err
			}
			applied = true
		}
		typ := notifications.TypeBillNew
		if event == nil {
			typ = notifications.TypeBillInvite
		}
		if err := s.notifyPaper(ctx, tx, typ, user, writer, paper, event, applied, nil); err != nil {
			return err
		}
	}
	return nil
}

// updatePaper — anyone who may write saves the paper; everyone's books follow (auto) or are told.
func (s *Service) updatePaper(ctx context.Context, writer, paperID uuid.UUID, eventID *uuid.UUID, req UpdatePaperRequest) (*PaperWriteResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	a, err := s.accessTx(ctx, tx, writer, paperID, eventID)
	if err != nil {
		return nil, err
	}
	if !a.canWrite() {
		return nil, ErrViewerReadOnly
	}
	if a.event != nil {
		if err := writable(a.event.Status, "edit_pt"); err != nil {
			return nil, err
		}
	}
	if a.row.Version != req.Version {
		return nil, ErrPaperChanged
	}
	if err := updatePaperTx(ctx, tx, paperID, req.Version, writer, req.Paper); err != nil {
		return nil, err
	}
	gone, added, err := s.writePeopleTx(ctx, tx, writer, paperID, a.row.Owner, a.event, req.Paper)
	if err != nil {
		return nil, err
	}
	for _, g := range gone {
		if g.UserID != nil && *g.UserID == a.row.Owner {
			return nil, ErrNotPaperOwner // the owner leaves through leave
		}
		if g.UserID != nil && writer != a.row.Owner && g.Status == shared.PersonJoined {
			return nil, ErrNotPaperOwner // only the owner removes people
		}
	}
	if a.event != nil {
		if err := setPaperTagsTx(ctx, tx, paperID, a.event.ID, req.Paper.TagIDs); err != nil {
			return nil, err
		}
	}
	eff := shared.NewEffects()
	for _, g := range gone {
		if g.UserID != nil {
			if err := s.booksOutTx(ctx, tx, *g.UserID, writer, paperID, a.event, "", true, &eff); err != nil {
				return nil, err
			}
		}
	}
	paper, _, err := shared.LoadPaper(ctx, tx, writer, paperID)
	if err != nil {
		return nil, err
	}
	if err := s.followTx(ctx, tx, writer, paper, a.event, &eff); err != nil {
		return nil, err
	}
	if err := s.welcomeTx(ctx, tx, writer, paper, a.event, added); err != nil {
		return nil, err
	}
	return s.finishWrite(ctx, tx, writer, paperID, eff)
}

// followTx — after a paper changed: every user with books of it gets them rewritten (auto sync on,
// event not completed) or a notice. The writer's own books always follow.
func (s *Service) followTx(ctx context.Context, tx pgx.Tx, writer uuid.UUID, paper shared.Paper, event *Project, eff *shared.Effects) error {
	if event != nil && event.CompletedAt != nil {
		return nil
	}
	users, err := collectIDs(ctx, tx, `SELECT user_id FROM transactions WHERE split_transaction_id = $1
		UNION SELECT user_id FROM personal_debt_bills WHERE split_transaction_id = $1`, paper.ID)
	if err != nil {
		return err
	}
	for _, u := range users {
		auto := u == writer || s.autoSync(ctx, tx, u, writer, event != nil)
		if auto {
			view, _, err := shared.LoadPaper(ctx, tx, u, paper.ID)
			if err != nil {
				return err
			}
			var into *shared.Effects
			if u == writer {
				into = eff
			}
			if err := s.syncBooksTx(ctx, tx, u, view, into); err != nil {
				return err
			}
		}
		if u != writer {
			if err := s.notifyPaper(ctx, tx, notifications.TypeBillChanged, u, writer, paper, event, auto, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// --- claim ---

// myBooksTx — my transaction of a paper (nil = none) and my parts on it.
func (s *Service) myBooksTx(ctx context.Context, tx pgx.Tx, userID, paperID uuid.UUID) (*transactions.Transaction, []personal_debts.PaperPart, error) {
	books, err := transactions.PaperBooksTx(ctx, tx, paperID)
	if err != nil {
		return nil, nil, err
	}
	parts, err := personal_debts.MyPaperPartsTx(ctx, tx, userID, []uuid.UUID{paperID})
	if err != nil {
		return nil, nil, err
	}
	if t, ok := books[userID]; ok {
		return &t, parts, nil
	}
	return nil, parts, nil
}

// claimTx — my books from the paper: the transaction and (split bills) debts I don't hold yet; a
// payback paper (events) books repayment rows. auto = the claim nobody asked for (event defaults,
// debt branch). Answers what it wrote (nothing = ErrNothingToClaim).
func (s *Service) claimTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, paper shared.Paper, event *Project,
	in ClaimInput, auto bool, eff *shared.Effects) (int, error) {
	if s.txs == nil || s.debts == nil {
		return 0, errors.New("claims need the transactions and debts modules")
	}
	me := paper.PersonOfUser(userID)
	if me == nil {
		return 0, ErrNotOnPaper
	}
	payDebt := in.Pay != "wallet"
	plan, ok := paper.PlanFor(me.ID, payDebt)
	if !ok {
		return 0, ErrNothingToClaim
	}
	have, parts, err := s.myBooksTx(ctx, tx, userID, paper.ID)
	if err != nil {
		return 0, err
	}
	booked := map[uuid.UUID]bool{}
	for _, p := range parts {
		booked[p.PersonID] = true
	}
	if auto && event != nil {
		var d MyDefaults
		_ = tx.QueryRow(ctx, `SELECT category_id, account_id FROM project_my_defaults WHERE project_id = $1 AND user_id = $2`,
			event.ID, userID).Scan(&d.CategoryID, &d.AccountID)
		if in.CategoryID == nil {
			in.CategoryID = d.CategoryID
		}
		if in.AccountID == nil {
			in.AccountID = d.AccountID
		}
	}
	wrote := 0
	var txID *uuid.UUID
	if have != nil {
		txID = &have.ID
	}
	if plan.Tx && have == nil {
		var account *uuid.UUID
		if plan.UseWallet {
			account = in.AccountID
		}
		var myShare *float64
		if math.Abs(plan.MyShare-plan.Amount) >= 0.005 {
			v := plan.MyShare
			myShare = &v
		}
		row, err := s.txs.InsertPaperBookTx(ctx, tx, userID, transactions.PaperBook{
			PaperID: paper.ID, PersonID: me.ID, Type: transactions.TxType(plan.TxType), Amount: plan.Amount,
			MyShare: myShare, AccountID: account, CategoryID: in.CategoryID, Date: paper.Date,
			Description: paper.Description, Note: paper.Note, TagIDs: in.TagIDs,
		})
		if err != nil {
			return 0, err
		}
		txID = &row.ID
		if !payDebt && event == nil {
			if err := s.tellPaidTx(ctx, tx, userID, paper, me.ID); err != nil {
				return 0, err
			}
		}
		if in.PrivateNote != nil {
			if _, err := tx.Exec(ctx, `UPDATE transactions SET private_note = $2 WHERE id = $1`, row.ID,
				shared.CleanText(in.PrivateNote)); err != nil {
				return 0, fmt.Errorf("private note: %w", err)
			}
		}
		wrote++
		if eff != nil {
			if d, err := transactions.GetDetailTx(ctx, tx, userID, row.ID); err == nil {
				eff.Transactions = append(eff.Transactions, *d)
			}
		}
	}
	var debtIDs []uuid.UUID
	for _, d := range plan.Debts {
		if booked[d.PersonID] {
			continue
		}
		other := paper.Person(d.PersonID)
		if other == nil {
			continue
		}
		e, err := s.debts.CreatePaperDebtTx(ctx, tx, userID, personal_debts.PaperDebtInput{
			IOwe: d.IOwe, ContactID: other.Person.ContactID, PersonName: other.Person.Name, Date: paper.Date,
			Description: paper.Description,
			Parts:       []personal_debts.PaperPart{{PaperID: paper.ID, PersonID: d.PersonID, TransactionID: txID, Amount: d.Amount}},
		})
		if err != nil {
			return 0, err
		}
		if err := s.partnerTx(ctx, tx, e.ID, paper, me.ID, *other); err != nil {
			return 0, err
		}
		debtIDs = append(debtIDs, e.ID)
		wrote++
	}
	for _, r := range plan.Repayments {
		if booked[r.PersonID] {
			continue
		}
		other := paper.Person(r.PersonID)
		if other == nil {
			continue
		}
		dir := "received"
		if r.IOwe {
			dir = "paid"
		}
		e, _, err := s.debts.CreateEntryTx(ctx, tx, userID, personal_debts.EntryInput{
			Kind: personal_debts.KindRepayment, Direction: dir, ContactID: other.Person.ContactID, PersonName: other.Person.Name,
			Amount: r.Amount, Date: paper.Date, Description: paper.Description,
			CreateTransaction: in.AccountID != nil, AccountID: in.AccountID, ProjectID: paper.ProjectID,
		})
		if err != nil {
			return 0, err
		}
		if err := personal_debts.LinkPaperPartTx(ctx, tx, userID, e.ID, personal_debts.PaperPart{
			PaperID: paper.ID, PersonID: r.PersonID, Amount: r.Amount}); err != nil {
			return 0, err
		}
		debtIDs = append(debtIDs, e.ID)
		wrote++
	}
	if eff != nil && len(debtIDs) > 0 {
		debts, people, err := s.debts.DebtEffectsTx(ctx, tx, userID, debtIDs, nil)
		if err != nil {
			return 0, err
		}
		eff.Debts = append(eff.Debts, debts...)
		eff.PersonBalances = append(eff.PersonBalances, people...)
	}
	if wrote == 0 {
		if have != nil || len(parts) > 0 {
			return 0, ErrAlreadyClaimed
		}
		return 0, ErrNothingToClaim
	}
	if s.notifs != nil {
		if err := s.notifs.SupersedePaperNoticesTx(ctx, tx, userID, paper.ID); err != nil {
			return 0, err
		}
	}
	return wrote, nil
}

// tellPaidTx — I claimed a split bill as paid from my wallet: every payer I owed who already holds
// the debt "I owe them" in their book hears that I paid (debt_repaid).
func (s *Service) tellPaidTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, paper shared.Paper, me uuid.UUID) error {
	for id, v := range paper.NetOf(me) {
		other := paper.Person(id)
		if v < 0.005 || other == nil || other.UserID == nil {
			continue
		}
		var debtID uuid.UUID
		err := tx.QueryRow(ctx, `SELECT b.debt_id FROM personal_debt_bills b JOIN personal_debts d ON d.id = b.debt_id
			WHERE b.user_id = $1 AND b.split_transaction_id = $2 AND b.split_person_id = $3 AND d.kind = 'debt'
			LIMIT 1`, *other.UserID, paper.ID, me).Scan(&debtID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("payer's debt: %w", err)
		}
		if err := s.debts.TellRepaidTx(ctx, tx, userID, *other.UserID, debtID, v, paper.Date, paper.Description); err != nil {
			return err
		}
	}
	return nil
}

// partnerTx — the other person's debt of the same pair on this paper (their book) becomes my debt's
// partner.
func (s *Service) partnerTx(ctx context.Context, tx pgx.Tx, debtID uuid.UUID, paper shared.Paper, me uuid.UUID, other shared.PaperPerson) error {
	if other.UserID == nil {
		return nil
	}
	var partner uuid.UUID
	err := tx.QueryRow(ctx, `SELECT b.debt_id FROM personal_debt_bills b JOIN personal_debts d ON d.id = b.debt_id
		WHERE b.user_id = $1 AND b.split_transaction_id = $2 AND b.split_person_id = $3 AND d.kind = 'debt'
		  AND d.counterpart_debt_id IS NULL
		  AND (SELECT COUNT(*) FROM personal_debt_bills x WHERE x.debt_id = b.debt_id) = 1
		LIMIT 1`, *other.UserID, paper.ID, me).Scan(&partner)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("partner: %w", err)
	}
	return personal_debts.SetPartnersTx(ctx, tx, debtID, partner)
}

// syncBooksTx — my books of the paper take what it says now: my transaction (amount, my share, date,
// description; the claim's branch kept) and each part (the pair's amount; a pair gone drops out).
func (s *Service) syncBooksTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, paper shared.Paper, eff *shared.Effects) error {
	have, parts, err := s.myBooksTx(ctx, tx, userID, paper.ID)
	if err != nil {
		return err
	}
	me := paper.PersonOfUser(userID)
	if me == nil {
		return nil
	}
	if have != nil {
		payDebt := have.AccountID == nil
		for _, p := range parts {
			payDebt = payDebt || paper.NetOf(me.ID)[p.PersonID] > 0
		}
		if plan, ok := paper.PlanFor(me.ID, payDebt); ok && plan.Tx {
			var myShare *float64
			if math.Abs(plan.MyShare-plan.Amount) >= 0.005 {
				v := plan.MyShare
				myShare = &v
			}
			if err := s.txs.SyncPaperBookTx(ctx, tx, userID, have.ID, plan.Amount, myShare, paper.Date, paper.Description); err != nil {
				return err
			}
			if eff != nil {
				if d, err := transactions.GetDetailTx(ctx, tx, userID, have.ID); err == nil {
					eff.Transactions = append(eff.Transactions, *d)
				}
			}
		}
	}
	net := paper.NetOf(me.ID)
	var (
		touched, removed []uuid.UUID
		keys             []string
	)
	for _, p := range parts {
		now := math.Abs(net[p.PersonID])
		if now < 0.005 {
			// nothing owed on this pair any more; a debt left with no paper goes (its amount would be 0)
			var left int
			if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM personal_debt_bills WHERE debt_id = $1`, p.DebtID).Scan(&left); err != nil {
				return fmt.Errorf("debt parts: %w", err)
			}
			if left == 1 {
				e, err := personal_debts.GetEntryTx(ctx, tx, userID, p.DebtID)
				if err != nil {
					return err
				}
				if _, err := s.debts.DeleteEntryTx(ctx, tx, userID, p.DebtID); err != nil {
					return err
				}
				removed, keys = append(removed, p.DebtID), append(keys, e.Key())
				continue
			}
			if _, err := s.debts.DropPaperPartTx(ctx, tx, userID, p.ID); err != nil {
				return err
			}
		} else if math.Abs(now-p.Amount) >= 0.005 {
			if err := s.debts.SetPaperPartAmountTx(ctx, tx, userID, p.ID, now); err != nil {
				return err
			}
		} else {
			continue
		}
		touched = append(touched, p.DebtID)
	}
	if eff != nil && len(touched)+len(removed) > 0 {
		debts, people, err := s.debts.DebtEffectsTx(ctx, tx, userID, uniqueIDs(touched), keys)
		if err != nil {
			return err
		}
		eff.Debts = append(eff.Debts, debts...)
		eff.PersonBalances = append(eff.PersonBalances, people...)
		eff.RemovedDebtIDs = append(eff.RemovedDebtIDs, removed...)
	}
	return nil
}

// --- leaving ---

// booksOutTx — a user is off a paper (removed, left, or it is gone): their books stop following it
// at once (kept as plain records), then mode decides: keep | delete | "" = their on_split_removed
// switch (ask = a notice with keep / delete). notify = tell them.
func (s *Service) booksOutTx(ctx context.Context, tx pgx.Tx, userID, actor, paperID uuid.UUID, event *Project, mode string,
	notify bool, eff *shared.Effects) error {
	have, parts, err := s.myBooksTx(ctx, tx, userID, paperID)
	if err != nil {
		return err
	}
	var txID *uuid.UUID
	if have != nil {
		if _, err := s.txs.UnlinkPaperBookTx(ctx, tx, userID, have.ID); err != nil {
			return err
		}
		txID = &have.ID
	}
	var debtIDs []uuid.UUID
	for _, p := range parts {
		debtID, err := s.debts.DropPaperPartTx(ctx, tx, userID, p.ID)
		if err != nil {
			return err
		}
		debtIDs = append(debtIDs, debtID)
	}
	if mode == "" {
		mode = notifications.OnRemovedKeep
		if s.notifs != nil {
			if st, err := s.notifs.SettingsTx(ctx, tx, userID); err == nil {
				mode = st.OnSplitRemoved
			}
		}
	}
	applied := mode != notifications.OnRemovedAsk
	if mode == notifications.OnRemovedDelete {
		if txID != nil {
			if _, err := s.txs.DeleteRowInTx(ctx, tx, userID, *txID); err != nil && !errors.Is(err, transactions.ErrTxNotFound) {
				return err
			}
		}
		for _, d := range uniqueIDs(debtIDs) {
			var left int
			if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM personal_debt_bills WHERE debt_id = $1`, d).Scan(&left); err != nil {
				return fmt.Errorf("debt parts: %w", err)
			}
			if left == 0 { // a lump of other bills stays
				if _, err := s.debts.DeleteEntryTx(ctx, tx, userID, d); err != nil {
					return err
				}
			}
		}
		if eff != nil {
			if txID != nil {
				eff.RemovedTransactionIDs = append(eff.RemovedTransactionIDs, *txID)
			}
			eff.RemovedDebtIDs = append(eff.RemovedDebtIDs, debtIDs...)
		}
	}
	if notify && userID != actor {
		paper, ok, err := shared.LoadPaper(ctx, tx, userID, paperID)
		if err != nil {
			return err
		}
		if !ok {
			paper = shared.Paper{ID: paperID}
		}
		return s.notifyPaper(ctx, tx, notifications.TypeBillRemoved, userID, actor, paper, event, applied,
			&notifications.BillPayload{MyTransactionID: txID, MyDebtIDs: uniqueIDs(debtIDs)})
	}
	return nil
}

// removePerson — the owner takes a person off: equal re-splits, custom gives their share and what
// they paid to the owner's row.
func (s *Service) removePerson(ctx context.Context, owner, paperID, personID uuid.UUID, eventID *uuid.UUID) (*PaperWriteResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	a, err := s.accessTx(ctx, tx, owner, paperID, eventID)
	if err != nil {
		return nil, err
	}
	if !a.isOwner(owner) {
		return nil, ErrNotPaperOwner
	}
	people, err := peopleTx(ctx, tx, paperID)
	if err != nil {
		return nil, err
	}
	var (
		gone *paperPerson
		keep []paperPerson
	)
	for i := range people {
		if people[i].ID == personID {
			gone = &people[i]
		} else {
			keep = append(keep, people[i])
		}
	}
	if gone == nil || (gone.UserID != nil && *gone.UserID == owner) || len(keep) == 0 {
		return nil, ErrBadPaperPerson
	}
	if err := s.reshareWithout(ctx, tx, a.row, keep, *gone, owner); err != nil {
		return nil, err
	}
	if err := bumpPaperTx(ctx, tx, paperID, owner); err != nil {
		return nil, err
	}
	eff := shared.NewEffects()
	if gone.UserID != nil {
		if err := s.booksOutTx(ctx, tx, *gone.UserID, owner, paperID, a.event, "", true, &eff); err != nil {
			return nil, err
		}
	}
	paper, _, err := shared.LoadPaper(ctx, tx, owner, paperID)
	if err != nil {
		return nil, err
	}
	if err := s.followTx(ctx, tx, owner, paper, a.event, &eff); err != nil {
		return nil, err
	}
	return s.finishWrite(ctx, tx, owner, paperID, eff)
}

// reshareWithout — keep becomes the paper's people (gone's row deleted): equal re-splits; custom adds
// gone's share and paid to the owner's row.
func (s *Service) reshareWithout(ctx context.Context, tx pgx.Tx, row *paperRow, keep []paperPerson, gone paperPerson, owner uuid.UUID) error {
	lead := 0
	for i, p := range keep {
		if p.UserID != nil && *p.UserID == owner {
			lead = i
		}
	}
	keep[lead].Paid = round2(keep[lead].Paid + gone.Paid)
	if row.SplitMode == shared.SplitEqual && row.Type != shared.PaperPayback {
		equalSplit(row.Amount, keep, owner)
	} else {
		keep[lead].Share = round2(keep[lead].Share + gone.Share)
	}
	_, err := putPeopleTx(ctx, tx, row.ID, owner, keep)
	return err
}

// leave — I leave a paper: my row stays as a name (left); my books are kept or deleted. The owner
// leaving hands the paper to the first joined user; the last user leaving deletes it.
func (s *Service) leave(ctx context.Context, userID, paperID uuid.UUID, eventID *uuid.UUID, req LeaveRequest) (*PaperDeleteResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	a, err := s.accessTx(ctx, tx, userID, paperID, eventID)
	if err != nil {
		return nil, err
	}
	if a.person == nil {
		return nil, ErrNotOnPaper
	}
	if a.event != nil {
		if err := writable(a.event.Status, "resolve"); err != nil {
			return nil, err
		}
	}
	mode := req.Books
	if mode == "" {
		mode = notifications.OnRemovedKeep
	}
	eff := shared.NewEffects()
	if err := s.booksOutTx(ctx, tx, userID, userID, paperID, a.event, mode, false, &eff); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE split_transaction_people SET status = 'left' WHERE id = $1`, a.person.ID); err != nil {
		return nil, fmt.Errorf("leave: %w", err)
	}
	out := &PaperDeleteResult{ID: paperID, Effects: eff}
	out.Effects.RemovedBillIDs = append(out.Effects.RemovedBillIDs, paperID)
	if a.isOwner(userID) {
		var next uuid.UUID
		err := tx.QueryRow(ctx, `SELECT user_id FROM split_transaction_people
			WHERE split_transaction_id = $1 AND user_id IS NOT NULL AND user_id <> $2 AND status = 'joined'
			ORDER BY created_at, sort, id LIMIT 1`, paperID, userID).Scan(&next)
		switch {
		case errors.Is(err, pgx.ErrNoRows) && a.event == nil:
			if err := s.deletePaperTx(ctx, tx, userID, paperID); err != nil {
				return nil, err
			}
			out.Deleted = true
		case errors.Is(err, pgx.ErrNoRows):
			// an event bill with no other user on it: the event's owner takes it
			if _, err := tx.Exec(ctx, `UPDATE project_transactions SET owner_user_id = $2 WHERE id = $1`,
				paperID, a.event.OwnerUserID); err != nil {
				return nil, fmt.Errorf("owner: %w", err)
			}
		case err != nil:
			return nil, fmt.Errorf("next owner: %w", err)
		default:
			if _, err := tx.Exec(ctx, `UPDATE project_transactions SET owner_user_id = $2 WHERE id = $1`, paperID, next); err != nil {
				return nil, fmt.Errorf("owner: %w", err)
			}
			paper, _, err := shared.LoadPaper(ctx, tx, next, paperID)
			if err != nil {
				return nil, err
			}
			if err := s.notifyPaper(ctx, tx, notifications.TypeBillOwnerChanged, next, userID, paper, a.event, true, nil); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return out, nil
}

// deletePaperTx — the paper goes; everyone's books were unlinked before (the rows cascade).
func (s *Service) deletePaperTx(ctx context.Context, tx pgx.Tx, actor, paperID uuid.UUID) error {
	users, err := collectIDs(ctx, tx, `SELECT user_id FROM transactions WHERE split_transaction_id = $1
		UNION SELECT user_id FROM personal_debt_bills WHERE split_transaction_id = $1`, paperID)
	if err != nil {
		return err
	}
	for _, u := range users {
		if err := s.booksOutTx(ctx, tx, u, actor, paperID, nil, "", u != actor, nil); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM project_transactions WHERE id = $1`, paperID); err != nil {
		return fmt.Errorf("delete paper: %w", err)
	}
	return nil
}

// --- marks, join ---

// markPaidBack — "คืนแล้ว" on a person (a shared mark only; it books nothing).
func (s *Service) markPaidBack(ctx context.Context, userID, paperID, personID uuid.UUID, eventID *uuid.UUID, on bool) (*PaperWriteResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	a, err := s.accessTx(ctx, tx, userID, paperID, eventID)
	if err != nil {
		return nil, err
	}
	if !a.canWrite() {
		return nil, ErrViewerReadOnly
	}
	tag, err := tx.Exec(ctx, `UPDATE split_transaction_people SET paid_back = $3,
		paid_back_by_user_id = CASE WHEN $3 THEN $4::uuid END, paid_back_at = CASE WHEN $3 THEN NOW() END
		WHERE id = $1 AND split_transaction_id = $2`, personID, paperID, on, userID)
	if err != nil {
		return nil, fmt.Errorf("paid back: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrBadPaperPerson
	}
	return s.finishWrite(ctx, tx, userID, paperID, shared.NewEffects())
}

// answerInvite — a split bill I am invited to: join (auto claim when my switch says so) or decline.
func (s *Service) answerInvite(ctx context.Context, userID, paperID uuid.UUID, join bool) (*PaperWriteResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	row, err := lockPaperTx(ctx, tx, paperID)
	if err != nil {
		return nil, err
	}
	if row.ProjectID != nil {
		return nil, ErrPaperNotFound
	}
	status := shared.PersonDeclined
	if join {
		status = shared.PersonJoined
	}
	tag, err := tx.Exec(ctx, `UPDATE split_transaction_people SET status = $3
		WHERE split_transaction_id = $1 AND user_id = $2 AND status = 'invited'`, paperID, userID, status)
	if err != nil {
		return nil, fmt.Errorf("answer invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrPaperNotFound
	}
	if s.notifs != nil {
		if err := s.notifs.SupersedePaperNoticesTx(ctx, tx, userID, paperID); err != nil {
			return nil, err
		}
	}
	if !join {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit: %w", err)
		}
		return &PaperWriteResult{Effects: shared.NewEffects()}, nil
	}
	return s.finishWrite(ctx, tx, userID, paperID, shared.NewEffects())
}

// claim — "ลงสมุดของฉัน" on a paper I'm on.
func (s *Service) claim(ctx context.Context, userID, paperID uuid.UUID, eventID *uuid.UUID, in ClaimInput) (*PaperWriteResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	a, err := s.accessTx(ctx, tx, userID, paperID, eventID)
	if err != nil {
		return nil, err
	}
	if a.person == nil || a.person.Status != shared.PersonJoined {
		return nil, ErrNotOnPaper
	}
	if a.event != nil {
		if err := writable(a.event.Status, "resolve"); err != nil {
			return nil, err
		}
	}
	paper, _, err := shared.LoadPaper(ctx, tx, userID, paperID)
	if err != nil {
		return nil, err
	}
	eff := shared.NewEffects()
	if _, err := s.claimTx(ctx, tx, userID, paper, a.event, in, false, &eff); err != nil {
		return nil, err
	}
	return s.finishWrite(ctx, tx, userID, paperID, eff)
}

// SyncMyBooks — "อัปเดต": my books of the paper take what it says now.
func (s *Service) SyncMyBooks(ctx context.Context, userID, paperID uuid.UUID) (*PaperWriteResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	row, err := lockPaperTx(ctx, tx, paperID)
	if err != nil {
		return nil, err
	}
	if row.ProjectID != nil {
		ev, err := s.store.GetByID(ctx, *row.ProjectID)
		if err != nil {
			return nil, err
		}
		if ev.CompletedAt != nil {
			return nil, ErrEventCompleted
		}
	}
	paper, _, err := shared.LoadPaper(ctx, tx, userID, paperID)
	if err != nil {
		return nil, err
	}
	if paper.PersonOfUser(userID) == nil {
		return nil, ErrNotOnPaper
	}
	eff := shared.NewEffects()
	if err := s.syncBooksTx(ctx, tx, userID, paper, &eff); err != nil {
		return nil, err
	}
	if s.notifs != nil {
		if err := s.notifs.SupersedePaperNoticesTx(ctx, tx, userID, paperID); err != nil {
			return nil, err
		}
	}
	return s.finishWrite(ctx, tx, userID, paperID, eff)
}

// UnlinkMyTransaction — my transaction stops following its paper (a plain row).
func (s *Service) UnlinkMyTransaction(ctx context.Context, userID, txID uuid.UUID) (*transactions.TransactionDetail, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := s.txs.UnlinkPaperBookTx(ctx, tx, userID, txID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE personal_debt_bills SET transaction_id = NULL WHERE transaction_id = $1`, txID); err != nil {
		return nil, fmt.Errorf("unlink parts: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.txs.Get(ctx, userID, txID)
}

// SyncMyTransaction — "อัปเดต" from a transaction: its paper's books (mine) follow it.
func (s *Service) SyncMyTransaction(ctx context.Context, userID, txID uuid.UUID) (*PaperWriteResult, error) {
	var paperID *uuid.UUID
	if err := s.store.db.QueryRow(ctx, `SELECT split_transaction_id FROM transactions WHERE id = $1 AND user_id = $2`,
		txID, userID).Scan(&paperID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, transactions.ErrTxNotFound
		}
		return nil, fmt.Errorf("transaction: %w", err)
	}
	if paperID == nil {
		return nil, transactions.ErrNotPaperBook
	}
	return s.SyncMyBooks(ctx, userID, *paperID)
}

// SyncMyDebt — "อัปเดต" from a debt: every paper it covers brings it up to date.
func (s *Service) SyncMyDebt(ctx context.Context, userID, debtID uuid.UUID) (*PaperWriteResult, error) {
	papers, err := collectIDs(ctx, s.store.db, `SELECT DISTINCT split_transaction_id FROM personal_debt_bills
		WHERE debt_id = $1 AND user_id = $2`, debtID, userID)
	if err != nil {
		return nil, err
	}
	if len(papers) == 0 {
		return nil, transactions.ErrNotPaperBook
	}
	var last *PaperWriteResult
	for _, p := range papers {
		if last, err = s.SyncMyBooks(ctx, userID, p); err != nil {
			return nil, err
		}
	}
	return last, nil
}

// UnlinkMyDebt — my debt stops following its papers (a plain debt).
func (s *Service) UnlinkMyDebt(ctx context.Context, userID, debtID uuid.UUID) error {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := s.debts.UnlinkPaperDebtTx(ctx, tx, userID, debtID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// --- answers ---

// finishWrite — commit, then the paper as the writer sees it and the write's effects (wallets).
func (s *Service) finishWrite(ctx context.Context, tx pgx.Tx, userID, paperID uuid.UUID, eff shared.Effects) (*PaperWriteResult, error) {
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	views, err := s.paperViews(ctx, s.store.db, userID, []uuid.UUID{paperID})
	if err != nil {
		return nil, err
	}
	out := &PaperWriteResult{Effects: eff}
	if len(views) == 1 {
		out.Paper = &views[0]
		out.Effects.Bills = append(out.Effects.Bills, views[0])
	} else {
		out.Effects.RemovedBillIDs = append(out.Effects.RemovedBillIDs, paperID)
	}
	var wallets []uuid.UUID
	for _, t := range eff.Transactions {
		if d, ok := t.(transactions.TransactionDetail); ok && d.AccountID != nil {
			wallets = append(wallets, *d.AccountID)
		}
	}
	if len(wallets) > 0 {
		if out.Effects.AccountBalances, err = shared.LoadWalletBalances(ctx, s.store.db, uniqueIDs(wallets)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// notifyPaper — a bill notice to user about paper (extra fills the book fields).
func (s *Service) notifyPaper(ctx context.Context, tx pgx.Tx, typ string, user, actor uuid.UUID, paper shared.Paper, event *Project,
	applied bool, extra *notifications.BillPayload) error {
	if s.notifs == nil || user == actor {
		return nil
	}
	p := notifications.BillPayload{PaperID: paper.ID, ProjectID: paper.ProjectID, Description: paper.Description,
		Amount: paper.Amount, Applied: applied}
	if event != nil {
		name := event.Name
		p.ProjectName = &name
	}
	if me := paper.PersonOfUser(user); me != nil {
		p.MyShare = me.Share
	}
	if extra != nil {
		p.MyTransactionID, p.MyDebtIDs, p.FromProjectID = extra.MyTransactionID, extra.MyDebtIDs, extra.FromProjectID
	}
	return s.notifs.DispatchBill(ctx, tx, typ, user, actor, p)
}
