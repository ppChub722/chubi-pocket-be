package transactions

// Page-ready rows (API spec v2, schema Transaction): every list / detail /
// write response builds its TransactionDetails here, so the shape and the
// rules behind the flags live in one place. One query per kind for the whole
// slice (row facts, account / category / event refs, tags, splits, persons)
// — never per row.

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// rowFacts — what one query learns about each row besides its columns.
type rowFacts struct {
	currency      string
	myShare       *float64
	splitCount    int
	billable      bool // expense / income, not a repayment or a system row
	eventBill     bool // shared.EventBillPredicate
	closed        bool // a split of it is labelled paid
	activeMembers int  // on the row's wallet
	callerActive  bool // the caller is an active member of the row's wallet
	authorName    string
	authorIcon    []byte
	peerID        *uuid.UUID // the transfer twin
	peerAccountID *uuid.UUID
	kind          *string // this row's category system_kind
	peerKind      *string
	// The debt entry the row is the wallet side of (author's book).
	debtContactID *uuid.UUID
	debtName      *string
	debtDirection *string
	debtPaid      *bool
	// The board bill (parent) the row belongs to.
	billID *uuid.UUID
	// The recurring item linked to the row.
	recItemID   *uuid.UUID
	recItemUser *uuid.UUID
	recItemSeq  *int
	recID       *uuid.UUID
	recName     *string
	recIcon     []byte
	recTotal    *int
}

// buildDetails turns rows into page-ready TransactionDetails for callerID.
// withSplits fills `splits` (detail and write responses; [] when none);
// list rows leave it nil (null). q is the pool, or the write's pgx.Tx when
// the rows aren't committed yet.
func buildDetails(
	ctx context.Context, q shared.Querier, callerID uuid.UUID, rows []Transaction, withSplits bool,
) ([]TransactionDetail, error) {
	out := make([]TransactionDetail, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	facts, err := loadRowFacts(ctx, q, callerID, ids)
	if err != nil {
		return nil, err
	}

	var accIDs, catIDs, eventIDs, debtContacts []uuid.UUID
	for i := range rows {
		r := &rows[i]
		if f, ok := facts[r.ID]; ok && f.debtContactID != nil && r.UserID == callerID {
			debtContacts = append(debtContacts, *f.debtContactID)
		}
		if r.AccountID != nil {
			accIDs = append(accIDs, *r.AccountID)
		}
		if r.CategoryID != nil {
			catIDs = append(catIDs, *r.CategoryID)
		}
		if r.ProjectID != nil {
			eventIDs = append(eventIDs, *r.ProjectID)
		}
		if f, ok := facts[r.ID]; ok && f.peerAccountID != nil {
			accIDs = append(accIDs, *f.peerAccountID)
		}
	}
	accounts, err := shared.LoadAccountRefs(ctx, q, accIDs)
	if err != nil {
		return nil, err
	}
	cats, err := shared.LoadCategoryRefs(ctx, q, catIDs)
	if err != nil {
		return nil, err
	}
	events, err := shared.LoadEventRefs(ctx, q, eventIDs)
	if err != nil {
		return nil, err
	}
	debtPersons, err := shared.LoadPersons(ctx, q, callerID, debtContacts)
	if err != nil {
		return nil, err
	}
	tags, err := loadRowTags(ctx, q, callerID, ids)
	if err != nil {
		return nil, err
	}
	var splits map[uuid.UUID][]SplitRef
	if withSplits {
		if splits, err = loadSplits(ctx, q, callerID, ids); err != nil {
			return nil, err
		}
	}

	for i := range rows {
		r := rows[i]
		d := TransactionDetail{Transaction: r, Tags: []shared.TagRef{}}
		if r.AccountID != nil {
			if a, ok := accounts[*r.AccountID]; ok {
				d.Account = &a
			}
		}
		if r.CategoryID != nil {
			if c, ok := cats[*r.CategoryID]; ok {
				d.Category = &c
			}
		}
		if r.ProjectID != nil {
			if e, ok := events[*r.ProjectID]; ok {
				d.Project = &e
			}
		}
		if t := tags[r.ID]; t != nil {
			d.Tags = t
		}
		if withSplits {
			s := splits[r.ID]
			if s == nil {
				s = []SplitRef{}
			}
			d.Splits = &s
		}
		f := facts[r.ID]
		// The debt entry lives in the author's book: only they see it.
		if r.SourcePersonalDebtID != nil && f.debtDirection != nil && r.UserID == callerID {
			name := ""
			if f.debtName != nil {
				name = *f.debtName
			}
			d.Counterparty = &shared.DebtLink{
				EntryID:   *r.SourcePersonalDebtID,
				Key:       shared.DebtKey(f.debtContactID, name),
				Person:    shared.ResolvePerson(debtPersons, shared.PersonKey{ContactID: f.debtContactID, Snapshot: name}),
				Direction: *f.debtDirection,
				Paid:      f.debtPaid != nil && *f.debtPaid,
			}
		}
		if f.recItemID != nil && f.recItemUser != nil && *f.recItemUser == callerID {
			link := &shared.RecurringItemLink{ItemID: *f.recItemID, Seq: f.recItemSeq, Total: f.recTotal}
			if f.recID != nil {
				link.Recurring = &shared.RecurringRef{ID: *f.recID, IconCode: shared.IconFromJSON(f.recIcon)}
				if f.recName != nil {
					link.Recurring.Name = *f.recName
				}
			}
			d.RecurringItem = link
		}
		if d.Project != nil && f.billID != nil {
			d.EventBill = &EventBillLink{Event: *d.Project, BillID: *f.billID, Linked: f.eventBill}
		}
		d.Currency = f.currency
		d.MyShare = f.myShare
		d.SplitCount, d.HasSplits = f.splitCount, f.splitCount > 0
		own := r.UserID == callerID
		d.CanSplit = f.billable
		d.CanEditSplits = f.billable && own
		// An event bill's own splits stay on it when it moves (only the
		// board splits travel), so a repaid one doesn't block the move.
		d.CanJoinEvent = f.billable && own && (f.eventBill || !f.closed)
		d.IsLocked = r.AccountID != nil && !f.callerActive
		d.CanEditCategory = own && !d.IsLocked && f.billable
		if r.AccountID != nil && f.activeMembers > 1 {
			d.CreatedBy = &shared.UserRef{
				UserID: r.UserID, DisplayName: f.authorName, IconCode: shared.IconFromJSON(f.authorIcon),
			}
		}
		if r.Type == TypeTransfer && f.peerID != nil {
			p := &TransferPeer{ID: *f.peerID, Direction: transferDirection(f)}
			if f.peerAccountID != nil {
				if a, ok := accounts[*f.peerAccountID]; ok {
					p.Account = &a
				}
			}
			d.TransferPeer = p
		}
		out[i] = d
	}
	return out, nil
}

// transferDirection — out when this row carries the Transfer OUT system
// category (or, without one, its twin carries Transfer IN).
func transferDirection(f rowFacts) string {
	switch {
	case f.kind != nil && *f.kind == "TRANSFER_OUT":
		return "out"
	case f.kind != nil && *f.kind == "TRANSFER_IN":
		return "in"
	case f.peerKind != nil && *f.peerKind == "TRANSFER_IN":
		return "out"
	}
	return "in"
}

func loadRowFacts(ctx context.Context, q shared.Querier, callerID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]rowFacts, error) {
	rows, err := q.Query(ctx, `
		SELECT t.id,
		       COALESCE(a.currency, u.currency, 'THB'),
		       CASE WHEN t.type IN ('income', 'expense') THEN `+shared.ShareAmountExpr("t")+` END,
		       (SELECT COUNT(*) FROM personal_debts d WHERE d.source_transaction_id = t.id),
		       t.type IN ('income', 'expense') AND t.source_personal_debt_id IS NULL
		         AND NOT COALESCE(c.is_system, FALSE),
		       `+shared.EventBillPredicate("t")+`,
		       EXISTS (SELECT 1 FROM personal_debts d
		               WHERE d.source_transaction_id = t.id AND d.user_id = t.user_id AND d.paid),
		       (SELECT COUNT(*) FROM account_members am
		         WHERE am.account_id = t.account_id
		           AND am.joined_at IS NOT NULL AND am.left_at IS NULL),
		       `+shared.ActiveMembershipPredicate("t.account_id", "$2")+`,
		       u.display_name, u.icon_code,
		       peer.id, peer.account_id, c.system_kind, pc.system_kind,
		       de.counterparty_contact_id, de.counterparty_person_name, de.direction, de.paid,
		       COALESCE(bp.parent_project_transaction_id, bp.id),
		       ri.id, ri.user_id, ri.seq, rr.id, rr.name, rr.icon_code, rr.total_count
		FROM transactions t
		LEFT JOIN recurring_items ri ON ri.transaction_id = t.id
		LEFT JOIN recurring_rules rr ON rr.id = ri.rule_id
		LEFT JOIN personal_debts de ON de.id = t.source_personal_debt_id
		LEFT JOIN project_transactions bp ON bp.id = t.source_project_transaction_id
		JOIN users u ON u.id = t.user_id
		LEFT JOIN accounts a ON a.id = t.account_id
		LEFT JOIN categories c ON c.id = t.category_id
		LEFT JOIN LATERAL (
			SELECT p.id, p.account_id, p.category_id FROM transactions p
			WHERE t.transfer_group_id IS NOT NULL
			  AND p.transfer_group_id = t.transfer_group_id AND p.id <> t.id
			ORDER BY p.created_at, p.id LIMIT 1) peer ON TRUE
		LEFT JOIN categories pc ON pc.id = peer.category_id
		WHERE t.id = ANY($1)`, ids, callerID)
	if err != nil {
		return nil, fmt.Errorf("row facts: %w", err)
	}
	defer rows.Close()
	out := make(map[uuid.UUID]rowFacts, len(ids))
	for rows.Next() {
		var (
			id uuid.UUID
			f  rowFacts
		)
		if err := rows.Scan(&id, &f.currency, &f.myShare, &f.splitCount, &f.billable, &f.eventBill,
			&f.closed, &f.activeMembers, &f.callerActive, &f.authorName, &f.authorIcon,
			&f.peerID, &f.peerAccountID, &f.kind, &f.peerKind,
			&f.debtContactID, &f.debtName, &f.debtDirection, &f.debtPaid, &f.billID,
			&f.recItemID, &f.recItemUser, &f.recItemSeq, &f.recID, &f.recName, &f.recIcon, &f.recTotal); err != nil {
			return nil, fmt.Errorf("row facts scan: %w", err)
		}
		out[id] = f
	}
	return out, rows.Err()
}

// loadRowTags — the caller's tags on each row (tags are per user), by name.
func loadRowTags(ctx context.Context, q shared.Querier, callerID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID][]shared.TagRef, error) {
	rows, err := q.Query(ctx, `
		SELECT tt.transaction_id, t.id, t.name, t.icon_code
		FROM tags t
		JOIN transaction_tags tt ON tt.tag_id = t.id
		WHERE tt.transaction_id = ANY($1) AND t.user_id = $2
		ORDER BY tt.transaction_id, LOWER(t.name), t.id`, ids, callerID)
	if err != nil {
		return nil, fmt.Errorf("row tags: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID][]shared.TagRef{}
	for rows.Next() {
		var (
			txID uuid.UUID
			tag  shared.TagRef
			icon []byte
		)
		if err := rows.Scan(&txID, &tag.ID, &tag.Name, &icon); err != nil {
			return nil, fmt.Errorf("row tags scan: %w", err)
		}
		tag.IconCode = shared.IconFromJSON(icon)
		out[txID] = append(out[txID], tag)
	}
	return out, rows.Err()
}

// loadSplits — the split entries each row made in the caller's book (a shared-wallet
// member who isn't the author gets none: the debts are the author's), with
// the person resolved by the name rule (live contact name).
func loadSplits(ctx context.Context, q shared.Querier, callerID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID][]SplitRef, error) {
	rows, err := q.Query(ctx, `
		SELECT source_transaction_id, id, counterparty_person_name, counterparty_contact_id,
		       direction, amount, paid
		FROM personal_debts
		WHERE source_transaction_id = ANY($1) AND user_id = $2
		ORDER BY created_at, id`, ids, callerID)
	if err != nil {
		return nil, fmt.Errorf("splits: %w", err)
	}
	type row struct {
		txID uuid.UUID
		ref  SplitRef
	}
	var (
		list       []row
		contactIDs []uuid.UUID
	)
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.txID, &r.ref.DebtID, &r.ref.PersonName, &r.ref.ContactID,
			&r.ref.Direction, &r.ref.Amount, &r.ref.Paid); err != nil {
			rows.Close()
			return nil, fmt.Errorf("splits scan: %w", err)
		}
		if r.ref.ContactID != nil {
			contactIDs = append(contactIDs, *r.ref.ContactID)
		}
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	persons, err := shared.LoadPersons(ctx, q, callerID, contactIDs)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID][]SplitRef{}
	for _, r := range list {
		r.ref.Person = shared.ResolvePerson(persons, shared.PersonKey{ContactID: r.ref.ContactID, Snapshot: r.ref.PersonName})
		r.ref.DisplayName = r.ref.Person.Name
		r.ref.Key = shared.DebtKey(r.ref.ContactID, r.ref.PersonName)
		out[r.txID] = append(out[r.txID], r.ref)
	}
	return out, nil
}

// detailOf builds one row with its splits (detail / write responses).
func detailOf(ctx context.Context, q shared.Querier, callerID uuid.UUID, t *Transaction) (*TransactionDetail, error) {
	ds, err := buildDetails(ctx, q, callerID, []Transaction{*t}, true)
	if err != nil {
		return nil, err
	}
	return &ds[0], nil
}
