package transactions

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

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
	debtContactID *uuid.UUID
	debtName      *string
	debtDirection *string
	debtPaid      *bool
	debtKind      *string
	billID        *uuid.UUID
	billScope     *uuid.UUID // the bill's event, or the split bill itself
	recItemID     *uuid.UUID
	recItemUser   *uuid.UUID
	recItemSeq    *int
	recID         *uuid.UUID
	recName       *string
	recIcon       []byte
	recTotal      *int
	// the paid-by debt (personal_debts.paid_for_transaction_id = the row)
	pbID        *uuid.UUID
	pbKind      *string
	pbContactID *uuid.UUID
	pbName      *string
	pbPaid      *bool
}

// linkMode — how the row's bill is shared (one way at a time).
func linkMode(r *Transaction, f rowFacts) string {
	switch {
	case inEvent(r) || f.eventBill:
		return LinkEvent
	case f.splitCount > 0:
		return LinkSplit
	case r.PaidByAmount != nil:
		return LinkPaid
	}
	return LinkPersonal
}

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
		if r.UserID != callerID {
			r.PrivateNote = nil // mine only (0.4.1-2 S11)
		}
		if f, ok := facts[r.ID]; ok && f.debtContactID != nil && r.UserID == callerID {
			debtContacts = append(debtContacts, *f.debtContactID)
		}
		if f, ok := facts[r.ID]; ok && f.pbContactID != nil && r.UserID == callerID {
			debtContacts = append(debtContacts, *f.pbContactID)
		}
		if r.AccountID != nil {
			accIDs = append(accIDs, *r.AccountID)
		}
		if r.CategoryID != nil {
			catIDs = append(catIDs, *r.CategoryID)
		}
		if e := eventOf(r, facts[r.ID]); e != nil {
			eventIDs = append(eventIDs, *e)
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
	// 0.4.1-2 S8: the bill of every row in one (in_sync / differences on list rows too).
	var billIDs []uuid.UUID
	for _, f := range facts {
		if f.billID != nil {
			billIDs = append(billIDs, *f.billID)
		}
	}
	billPeople, err := shared.LoadEventBillPeople(ctx, q, callerID, billIDs)
	if err != nil {
		return nil, err
	}
	pending, err := loadPendingBills(ctx, q, callerID, billIDs)
	if err != nil {
		return nil, err
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
		if id := eventOf(&r, facts[r.ID]); id != nil {
			if e, ok := events[*id]; ok {
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
		if r.SourcePersonalDebtID != nil && f.debtDirection != nil && r.UserID == callerID {
			name := ""
			if f.debtName != nil {
				name = *f.debtName
			}
			d.Counterparty = &shared.DebtLink{
				EntryID:   *r.SourcePersonalDebtID,
				Kind:      kindOr(f.debtKind, "debt"),
				Connected: r.DebtConnected,
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
			d.EventBill = &EventBillLink{Event: *d.Project, BillID: *f.billID, Linked: f.eventBill, Kind: "copy"}
			if f.eventBill {
				d.EventBill.Kind = "origin"
			}
			if bp, ok := billPeople[*f.billID]; ok {
				eb := d.EventBill
				if withSplits {
					payer := bp.Payer
					eb.Payer, eb.BillAmount, eb.Members = &payer, bp.Amount, bp.Members
				}
				if r.UserID == callerID {
					eb.Differences = bp.Differences(f.eventBill, r.Amount, r.Date, r.Description, r.Note)
					inSync := len(eb.Differences) == 0
					eb.InSync = &inSync
					// SA1: a connected copy of a bill whose event still syncs opens the bill form.
					eb.EditInBill = d.Project.Status == "active"
					eb.PendingUpdate = pending[*f.billID]
				}
			}
			if d.Project.Kind == "split" {
				d.Project = nil // a split bill shows no event chip (event_bill.event.kind = split)
			}
		}
		d.Currency = f.currency
		d.MyShare = f.myShare
		d.SplitCount, d.HasSplits = f.splitCount, f.splitCount > 0
		own := r.UserID == callerID
		d.WalletAmount = r.Cash()
		d.LinkMode = linkMode(&r, f)
		if r.PaidByAmount != nil && f.pbID != nil && own {
			name := ""
			if f.pbName != nil {
				name = *f.pbName
			}
			src := PaidBySourceMe
			d.PaidBy = &PaidBy{
				DebtID: *f.pbID, Key: shared.DebtKey(f.pbContactID, name),
				Person: shared.ResolvePerson(debtPersons, shared.PersonKey{ContactID: f.pbContactID, Snapshot: name}),
				Amount: *r.PaidByAmount, MyPaid: r.Cash(), Source: src, Paid: f.pbPaid != nil && *f.pbPaid,
			}
		}
		d.CanSplit = f.billable
		d.CanEditSplits = f.billable && own
		d.CanJoinEvent = f.billable && own && (f.eventBill || !f.closed) && r.PaidByAmount == nil
		d.IsLocked = r.AccountID != nil && !f.callerActive
		d.CanEditCategory = own && !d.IsLocked && f.billable
		if r.AccountID != nil && f.activeMembers > 1 {
			d.CreatedBy = &shared.UserRef{
				UserID: r.UserID, DisplayName: f.authorName, Avatar: shared.AvatarFromSrc(f.authorName, f.authorIcon),
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

// transferDirection — out when this row carries the Transfer OUT system category (or, without
// one, its twin carries Transfer IN).
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

// eventOf — the event a row shows: its own, else (a copy of a split bill) the bill's scope.
func eventOf(r *Transaction, f rowFacts) *uuid.UUID {
	if r.ProjectID != nil {
		return r.ProjectID
	}
	if f.billID != nil {
		return f.billScope
	}
	return nil
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
		       u.display_name, `+shared.AvatarSQL("u.id", "NULL")+`,
		       peer.id, peer.account_id, c.system_kind, pc.system_kind,
		       de.counterparty_contact_id, de.counterparty_person_name, de.direction, de.paid, de.kind,
		       CASE WHEN t.source_project_transaction_id IS NOT NULL THEN t.split_transaction_id END,
		       (SELECT b.scope_id FROM project_transactions b WHERE b.id = t.split_transaction_id),
		       ri.id, ri.user_id, ri.seq, rr.id, rr.name, rr.icon_code, rr.total_count,
		       pb.id, pb.source_kind, pb.counterparty_contact_id, pb.counterparty_person_name, pb.paid
		FROM transactions t
		LEFT JOIN personal_debts pb ON pb.paid_for_transaction_id = t.id
		LEFT JOIN recurring_items ri ON ri.transaction_id = t.id
		LEFT JOIN recurring_rules rr ON rr.id = ri.rule_id
		LEFT JOIN personal_debts de ON de.id = t.source_personal_debt_id
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
			&f.debtContactID, &f.debtName, &f.debtDirection, &f.debtPaid, &f.debtKind, &f.billID, &f.billScope,
			&f.recItemID, &f.recItemUser, &f.recItemSeq, &f.recID, &f.recName, &f.recIcon, &f.recTotal,
			&f.pbID, &f.pbKind, &f.pbContactID, &f.pbName, &f.pbPaid); err != nil {
			return nil, fmt.Errorf("row facts scan: %w", err)
		}
		out[id] = f
	}
	return out, rows.Err()
}

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

func detailOf(ctx context.Context, q shared.Querier, callerID uuid.UUID, t *Transaction) (*TransactionDetail, error) {
	ds, err := buildDetails(ctx, q, callerID, []Transaction{*t}, true)
	if err != nil {
		return nil, err
	}
	return &ds[0], nil
}

// kindOr — *s, or dflt when nil.
func kindOr(s *string, dflt string) string {
	if s == nil {
		return dflt
	}
	return *s
}
