package projects

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

var (
	ErrMergeSame      = apierr.Invalid("a member can't be merged into itself", "into", apierr.FieldInvalid)
	ErrMergeTwoUsers  = errors.New("two different users can't be merged")
	ErrMoveSameEvent  = apierr.Invalid("the bill is already there", "project_id", apierr.FieldInvalid)
	ErrMoveNotAMember = errors.New("you are not a member of that event")
)

// --- move ---

// movePaper — the paper's owner moves it into an event, out of one (target nil), or across. Same
// paper, same books; the people are matched to the target's members (new ones join it).
func (s *Service) movePaper(ctx context.Context, owner, paperID uuid.UUID, fromEvent *uuid.UUID, target *uuid.UUID) (*PaperWriteResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	a, err := s.accessTx(ctx, tx, owner, paperID, fromEvent)
	if err != nil {
		return nil, err
	}
	if !a.isOwner(owner) {
		return nil, ErrNotPaperOwner
	}
	if a.event != nil {
		if err := writable(a.event.Status, "edit_pt"); err != nil {
			return nil, err
		}
	}
	if (target == nil && a.event == nil) || (target != nil && a.event != nil && *target == a.event.ID) {
		return nil, ErrMoveSameEvent
	}
	from := a.row.ProjectID
	if target == nil {
		if err := s.moveOutOfEventTx(ctx, tx, paperID); err != nil {
			return nil, err
		}
	} else {
		ev, err := s.store.GetByID(ctx, *target)
		if err != nil {
			return nil, err
		}
		if err := writable(ev.Status, "create_pt"); err != nil {
			return nil, err
		}
		m, err := s.store.MemberByUserIDTx(ctx, tx, ev.ID, owner)
		if err != nil || m.Status != MemberStatusActive || m.Role == RoleViewer {
			return nil, ErrMoveNotAMember
		}
		if err := s.moveIntoEventTx(ctx, tx, owner, paperID, ev); err != nil {
			return nil, err
		}
	}
	if err := bumpPaperTx(ctx, tx, paperID, owner); err != nil {
		return nil, err
	}
	paper, _, err := shared.LoadPaper(ctx, tx, owner, paperID)
	if err != nil {
		return nil, err
	}
	var event *Project
	if target != nil {
		if event, err = s.store.GetByID(ctx, *target); err != nil {
			return nil, err
		}
	}
	for _, p := range paper.People {
		if p.UserID != nil && *p.UserID != owner && p.Status == shared.PersonJoined {
			if err := s.notifyPaper(ctx, tx, notifications.TypeBillMoved, *p.UserID, owner, paper, event, true,
				&notifications.BillPayload{FromProjectID: from}); err != nil {
				return nil, err
			}
		}
	}
	return s.finishWrite(ctx, tx, owner, paperID, shared.NewEffects())
}

// moveIntoEventTx — every person on the paper becomes (or matches) a member of ev. People who
// declined or left the split bill stay names (the owner's contact or name, no user).
func (s *Service) moveIntoEventTx(ctx context.Context, tx pgx.Tx, owner, paperID uuid.UUID, ev *Project) error {
	people, err := peopleTx(ctx, tx, paperID)
	if err != nil {
		return err
	}
	m := newMatcher(s, tx, ev, owner)
	if err := m.load(ctx); err != nil {
		return err
	}
	for i := range people {
		p := &people[i]
		var (
			id  uuid.UUID
			err error
		)
		var contact *uuid.UUID
		_ = tx.QueryRow(ctx, `SELECT contact_id FROM split_person_contacts WHERE split_person_id = $1 AND user_id = $2`,
			p.ID, owner).Scan(&contact)
		active := p.UserID != nil && (p.Status == shared.PersonJoined || p.Status == shared.PersonInvited)
		switch {
		case active:
			id, err = m.forUser(ctx, *p.UserID, p.Name)
		case contact != nil:
			id, err = m.forContact(ctx, *contact)
		default:
			id, err = m.forName(ctx, p.Name)
		}
		if err != nil {
			return err
		}
		mid := id
		p.MemberID = &mid
		p.UserID, p.Name, _ = memberInfoTx(ctx, tx, ev.ID, id)
		p.Status = shared.PersonJoined
	}
	if _, err := tx.Exec(ctx, `UPDATE project_transactions SET project_id = $2 WHERE id = $1`, paperID, ev.ID); err != nil {
		return fmt.Errorf("move in: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM project_transaction_tags WHERE project_transaction_id = $1`, paperID); err != nil {
		return fmt.Errorf("clear tags: %w", err)
	}
	if _, err := putPeopleTx(ctx, tx, paperID, owner, people); err != nil {
		return err
	}
	return m.dispatchInvites(ctx)
}

// moveOutOfEventTx — the paper becomes a split bill: no event, no members, no event tags; the users
// on it stay joined.
func (s *Service) moveOutOfEventTx(ctx context.Context, tx pgx.Tx, paperID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `DELETE FROM project_transaction_tags WHERE project_transaction_id = $1`, paperID); err != nil {
		return fmt.Errorf("clear tags: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE split_transaction_people SET project_member_id = NULL,
		status = CASE WHEN status = 'left' THEN 'left' ELSE 'joined' END WHERE split_transaction_id = $1`, paperID); err != nil {
		return fmt.Errorf("people out: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE project_transactions SET project_id = NULL WHERE id = $1`, paperID); err != nil {
		return fmt.Errorf("move out: %w", err)
	}
	return nil
}

// --- members ---

// MergeMember — "รวมกับ…": member goes into into (the better identified stays: user > contact > name;
// never two users). Every bill of the event follows; a bill holding both adds them up (equal
// re-splits).
func (s *Service) MergeMember(ctx context.Context, userID, projectID, memberID uuid.UUID, req MergeMemberRequest) (*EventPatch, error) {
	if memberID == req.Into {
		return nil, ErrMergeSame
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	p, err := s.store.GetByIDForCaller(ctx, userID, projectID)
	if err != nil {
		return nil, err
	}
	if err := writable(p.Status, "member_crud"); err != nil {
		return nil, err
	}
	if _, err := s.store.AssertCanWrite(ctx, projectID, userID); err != nil {
		return nil, err
	}
	from, err := s.store.GetMemberTx(ctx, tx, memberID)
	if err != nil {
		return nil, err
	}
	into, err := s.store.GetMemberTx(ctx, tx, req.Into)
	if err != nil {
		return nil, err
	}
	if from.ProjectID != projectID || into.ProjectID != projectID {
		return nil, ErrMemberNotInProject
	}
	if from.UserID != nil && into.UserID != nil {
		return nil, ErrMergeTwoUsers
	}
	if from.UserID != nil { // the user stays
		from, into = into, from
	}
	if from.Role == RoleOwner {
		return nil, ErrCannotRemoveOwner
	}
	bills, err := collectIDs(ctx, tx, `SELECT DISTINCT split_transaction_id FROM split_transaction_people
		WHERE project_member_id = $1`, from.ID)
	if err != nil {
		return nil, err
	}
	for _, b := range bills {
		if err := s.mergeOnPaperTx(ctx, tx, userID, b, from.ID, into.ID); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO project_member_contacts (project_member_id, user_id, contact_id)
		SELECT $2, user_id, contact_id FROM project_member_contacts WHERE project_member_id = $1
		ON CONFLICT (project_member_id, user_id) DO NOTHING`, from.ID, into.ID); err != nil {
		return nil, fmt.Errorf("merge contacts: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM project_members WHERE id = $1`, from.ID); err != nil {
		return nil, fmt.Errorf("merge: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.patch(ctx, userID, projectID)
}

// mergeOnPaperTx — on one bill: from's row becomes into's (or adds into the row into already has).
func (s *Service) mergeOnPaperTx(ctx context.Context, tx pgx.Tx, writer, paperID, from, into uuid.UUID) error {
	row, err := lockPaperTx(ctx, tx, paperID)
	if err != nil {
		return err
	}
	people, err := peopleTx(ctx, tx, paperID)
	if err != nil {
		return err
	}
	var fromRow, intoRow *paperPerson
	for i := range people {
		switch {
		case people[i].MemberID != nil && *people[i].MemberID == from:
			fromRow = &people[i]
		case people[i].MemberID != nil && *people[i].MemberID == into:
			intoRow = &people[i]
		}
	}
	if fromRow == nil {
		return nil
	}
	user, name, err := memberInfoTx(ctx, tx, *row.ProjectID, into)
	if err != nil {
		return err
	}
	if intoRow == nil {
		id := into
		if _, err := tx.Exec(ctx, `UPDATE split_transaction_people SET project_member_id = $2, user_id = $3, name = $4
			WHERE id = $1`, fromRow.ID, id, user, name); err != nil {
			return fmt.Errorf("merge person: %w", err)
		}
		return bumpPaperTx(ctx, tx, paperID, writer)
	}
	var keep []paperPerson
	for _, p := range people {
		if p.ID == fromRow.ID {
			continue
		}
		if p.ID == intoRow.ID {
			p.Paid = round2(p.Paid + fromRow.Paid)
			p.Share = round2(p.Share + fromRow.Share)
		}
		keep = append(keep, p)
	}
	if row.SplitMode == shared.SplitEqual && row.Type != shared.PaperPayback {
		equalSplit(row.Amount, keep, row.Owner)
	}
	if _, err := tx.Exec(ctx, `UPDATE personal_debt_bills SET split_person_id = $2 WHERE split_person_id = $1
		AND NOT EXISTS (SELECT 1 FROM personal_debt_bills x WHERE x.user_id = personal_debt_bills.user_id
		                AND x.split_transaction_id = personal_debt_bills.split_transaction_id AND x.split_person_id = $2)`,
		fromRow.ID, intoRow.ID); err != nil {
		return fmt.Errorf("merge parts: %w", err)
	}
	if _, err := putPeopleTx(ctx, tx, paperID, writer, keep); err != nil {
		return err
	}
	return bumpPaperTx(ctx, tx, paperID, writer)
}

// unlinked — what a member's leaving took out of their books.
type unlinked struct{ txs, debts int }

// memberOutTx — a member leaves the event (or the owner removes them: removed = their spots stay
// "( ) ____" until merged into someone). Their books of its bills stop following; their switch
// decides keep / delete.
func (s *Service) memberOutTx(ctx context.Context, tx pgx.Tx, projectID, memberID, actor uuid.UUID, removed bool) (*ProjectMember, unlinked, error) {
	var out unlinked
	m, err := s.store.GetMemberTx(ctx, tx, memberID)
	if err != nil {
		return nil, out, err
	}
	col := "left_at"
	if removed {
		col = "removed_at"
	}
	if _, err := tx.Exec(ctx, `UPDATE project_members SET status = 'left', left_at = NOW(), `+col+` = NOW(),
		updated_by_user_id = $2 WHERE id = $1`, memberID, actor); err != nil {
		return nil, out, fmt.Errorf("member out: %w", err)
	}
	if m.UserID == nil || s.txs == nil {
		return m, out, nil
	}
	papers, err := collectIDs(ctx, tx, `SELECT x.id FROM project_transactions x WHERE x.project_id = $1 AND (
		EXISTS (SELECT 1 FROM transactions t WHERE t.split_transaction_id = x.id AND t.user_id = $2)
		OR EXISTS (SELECT 1 FROM personal_debt_bills b WHERE b.split_transaction_id = x.id AND b.user_id = $2))`,
		projectID, *m.UserID)
	if err != nil {
		return nil, out, err
	}
	ev, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return nil, out, err
	}
	mode := ""
	if !removed {
		mode = notifications.OnRemovedKeep
	}
	for _, p := range papers {
		have, parts, err := s.myBooksTx(ctx, tx, *m.UserID, p)
		if err != nil {
			return nil, out, err
		}
		if have != nil {
			out.txs++
		}
		out.debts += len(parts)
		if err := s.booksOutTx(ctx, tx, *m.UserID, actor, p, ev, mode, false, nil); err != nil {
			return nil, out, err
		}
	}
	return m, out, nil
}

// SetMyDefaults — my category / wallet for auto claims in an event.
func (s *Service) SetMyDefaults(ctx context.Context, userID, projectID uuid.UUID, d MyDefaults) (*Event, error) {
	if _, err := s.store.GetByIDForCaller(ctx, userID, projectID); err != nil {
		return nil, err
	}
	if _, err := s.store.db.Exec(ctx, `INSERT INTO project_my_defaults (project_id, user_id, category_id, account_id)
		SELECT $1, $2, (SELECT id FROM categories WHERE id = $3 AND user_id = $2),
		       (SELECT a.id FROM accounts a WHERE a.id = $4 AND `+shared.ActiveMembershipPredicate("a.id", "$2")+`)
		ON CONFLICT (project_id, user_id) DO UPDATE SET category_id = EXCLUDED.category_id, account_id = EXCLUDED.account_id`,
		projectID, userID, d.CategoryID, d.AccountID); err != nil {
		return nil, fmt.Errorf("my defaults: %w", err)
	}
	e, err := loadEvent(ctx, s.store.db, userID, projectID)
	return &e, err
}

// paperEventOf — the event of a paper (nil = a split bill).
func paperEventOf(ctx context.Context, q pgx.Tx, paperID uuid.UUID) (*uuid.UUID, error) {
	var id *uuid.UUID
	err := q.QueryRow(ctx, `SELECT project_id FROM project_transactions WHERE id = $1`, paperID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPaperNotFound
	}
	return id, err
}
