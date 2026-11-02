package transactions

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type rowFacts struct {
	currency      string
	billable      bool // expense / income, not a debt row or a system row
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
	recItemID     *uuid.UUID
	recItemUser   *uuid.UUID
	recItemSeq    *int
	recID         *uuid.UUID
	recName       *string
	recIcon       []byte
	recTotal      *int
}

func buildDetails(
	ctx context.Context, q shared.Querier, callerID uuid.UUID, rows []Transaction, _ bool,
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

	var accIDs, catIDs, paperIDs, debtContacts []uuid.UUID
	for i := range rows {
		r := &rows[i]
		if r.UserID != callerID {
			r.PrivateNote = nil // mine only (0.4.1-2 S11)
		}
		if f, ok := facts[r.ID]; ok && f.debtContactID != nil && r.UserID == callerID {
			debtContacts = append(debtContacts, *f.debtContactID)
		}
		if r.AccountID != nil {
			accIDs = append(accIDs, *r.AccountID)
		}
		if r.CategoryID != nil {
			catIDs = append(catIDs, *r.CategoryID)
		}
		if r.SplitTransactionID != nil && r.UserID == callerID {
			paperIDs = append(paperIDs, *r.SplitTransactionID)
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
	papers, err := shared.LoadPapers(ctx, q, callerID, paperIDs)
	if err != nil {
		return nil, err
	}
	var eventIDs []uuid.UUID
	for _, p := range papers {
		if p.ProjectID != nil {
			eventIDs = append(eventIDs, *p.ProjectID)
		}
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
		if t := tags[r.ID]; t != nil {
			d.Tags = t
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
		if r.SplitTransactionID != nil && r.UserID == callerID {
			if p, ok := papers[*r.SplitTransactionID]; ok {
				ref := p.BookRef(callerID, r.Amount, r.MyShare, r.Date, r.Description)
				if p.ProjectID != nil {
					if e, ok := events[*p.ProjectID]; ok {
						ref.Event = &e
						d.Project = &e
					}
					d.EventBill = &ref
				} else {
					d.SplitBill = &ref
				}
			}
		}
		d.Currency = f.currency
		if r.MyShare != nil {
			share := *r.MyShare
			d.MyShare = &share
		}
		own := r.UserID == callerID
		if r.AccountID != nil {
			d.WalletAmount = r.Cash()
		}
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

func loadRowFacts(ctx context.Context, q shared.Querier, callerID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]rowFacts, error) {
	rows, err := q.Query(ctx, `
		SELECT t.id,
		       COALESCE(a.currency, u.currency, 'THB'),
		       t.type IN ('income', 'expense') AND t.source_personal_debt_id IS NULL
		         AND NOT COALESCE(c.is_system, FALSE),
		       (SELECT COUNT(*) FROM account_members am
		         WHERE am.account_id = t.account_id
		           AND am.joined_at IS NOT NULL AND am.left_at IS NULL),
		       `+shared.ActiveMembershipPredicate("t.account_id", "$2")+`,
		       u.display_name, `+shared.AvatarSQL("u.id", "NULL")+`,
		       peer.id, peer.account_id, c.system_kind, pc.system_kind,
		       de.counterparty_contact_id, de.counterparty_person_name, de.direction, de.paid, de.kind,
		       ri.id, ri.user_id, ri.seq, rr.id, rr.name, rr.icon_code, rr.total_count
		FROM transactions t
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
		if err := rows.Scan(&id, &f.currency, &f.billable, &f.activeMembers, &f.callerActive,
			&f.authorName, &f.authorIcon, &f.peerID, &f.peerAccountID, &f.kind, &f.peerKind,
			&f.debtContactID, &f.debtName, &f.debtDirection, &f.debtPaid, &f.debtKind,
			&f.recItemID, &f.recItemUser, &f.recItemSeq, &f.recID, &f.recName, &f.recIcon, &f.recTotal); err != nil {
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
