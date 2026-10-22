package projects

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

// A new event bill with a free payer and members (0.4.0-3, quick create tab อีเวนต์). The payer is
// me, a member, a contact or a typed name; the members are who is in the bill with their shares.
// People who aren't members yet are added to the event. The board keeps who owes whom:
//   - I paid: my transaction is the whole bill on my wallet (as before), the other members' shares
//     are the bill's splits;
//   - someone else paid: the bill's payer is them, every other member's share is a split, and my
//     transaction is MY share only, an expense with no wallet (a copy of the bill).

var (
	ErrBadBillPeople = errors.New("payer / members: each person is me, a member_id, a contact_id or a person_name, listed once; they go with new_transaction")
	ErrPayerIncome   = errors.New("only an expense can be paid by another member")
)

// memberForPerson — the event member a bill person is, adding a contact / typed name that isn't one.
func (b *quickMemberBuilder) memberForPerson(ctx context.Context, p BillPerson, me uuid.UUID) (uuid.UUID, error) {
	switch {
	case p.Me:
		return me, nil
	case p.MemberID != nil:
		var ok bool
		if err := b.tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members
			WHERE id = $1 AND project_id = $2 AND status <> $3)`, *p.MemberID, b.projectID, MemberStatusLeft).Scan(&ok); err != nil {
			return uuid.Nil, fmt.Errorf("bill member: %w", err)
		}
		if !ok {
			return uuid.Nil, ErrMemberNotInProject
		}
		return *p.MemberID, nil
	case p.ContactID != nil:
		d := quickDebt{ContactID: p.ContactID}
		err := b.tx.QueryRow(ctx, `SELECT linked_user_id, display_name FROM contacts WHERE id = $1 AND user_id = $2`,
			*p.ContactID, b.callerUserID).Scan(&d.LinkedUserID, &d.PersonName)
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, transactions.ErrSplitContactNotFound
		}
		if err != nil {
			return uuid.Nil, fmt.Errorf("bill contact: %w", err)
		}
		return b.memberForDebt(ctx, d)
	case strings.TrimSpace(p.PersonName) != "":
		return b.memberForDebt(ctx, quickDebt{PersonName: p.PersonName})
	}
	return uuid.Nil, ErrBadBillPeople
}

// addPeopleBillTx writes one new bill from new_transaction + payer / members. It returns the board
// row and my transaction (nil when someone else paid and I have no share). existing (0.4.0-3 B3, a
// mode switch into an event) is my transaction to turn into the bill or my copy instead of a new one.
func (s *Service) addPeopleBillTx(
	ctx context.Context, tx pgx.Tx, p *Project, actorMemberID uuid.UUID, mb *quickMemberBuilder,
	req transactions.CreateRequest, people BillPeople, existing *transactions.Transaction,
) (uuid.UUID, *uuid.UUID, error) {
	callerUserID := mb.callerUserID
	if len(req.Splits) > 0 || req.PaidBy != nil {
		return uuid.Nil, nil, transactions.ErrLinkModeConflict
	}
	payer := BillPerson{Me: true}
	if people.Payer != nil {
		payer = *people.Payer
	}
	if payer.Empty() {
		return uuid.Nil, nil, ErrBadBillPeople
	}
	payerID, err := mb.memberForPerson(ctx, payer, actorMemberID)
	if err != nil {
		return uuid.Nil, nil, err
	}
	iPaid := payerID == actorMemberID
	if !iPaid && req.Type != transactions.TypeExpense {
		return uuid.Nil, nil, ErrPayerIncome
	}

	seen := map[uuid.UUID]bool{}
	var splits []ProjectSplitInput
	var sum, myShare float64
	for _, m := range people.Members {
		if m.Empty() {
			return uuid.Nil, nil, ErrBadBillPeople
		}
		id, err := mb.memberForPerson(ctx, m.BillPerson, actorMemberID)
		if err != nil {
			return uuid.Nil, nil, err
		}
		if seen[id] {
			return uuid.Nil, nil, ErrBadBillPeople
		}
		seen[id] = true
		if id == payerID || m.Share <= 0 {
			continue // the payer's share is the rest of the bill
		}
		if id == actorMemberID {
			myShare = m.Share
		}
		splits = append(splits, ProjectSplitInput{MemberID: id, Amount: m.Share})
		sum += m.Share
	}
	if sum-req.Amount > 0.005 {
		return uuid.Nil, nil, ErrSplitsExceedParent
	}

	pt := CreateProjectTransactionRequest{
		TransactionMemberID: payerID, Type: string(req.Type), Amount: req.Amount, Date: req.Date,
		Description: req.Description, Note: req.Note, Splits: splits,
	}
	if iPaid {
		// My transaction is the bill, on my wallet; the board row links to it.
		var d *transactions.Transaction
		if existing != nil {
			d = existing
		} else {
			req.Splits, req.MyShare = nil, nil
			created, err := s.txs.CreateInTx(ctx, tx, callerUserID, req)
			if err != nil {
				return uuid.Nil, nil, err
			}
			detail, ok := created.(*transactions.TransactionDetail)
			if !ok {
				return uuid.Nil, nil, ErrQuickTxNotBillable
			}
			d = &detail.Transaction
		}
		if req.AccountID != nil {
			if err := mb.addSharedWalletMembers(ctx, *req.AccountID); err != nil {
				return uuid.Nil, nil, err
			}
		}
		if pt.TagNames, err = personalTagNamesTx(ctx, tx, callerUserID, d.ID); err != nil {
			return uuid.Nil, nil, err
		}
		pt.LinkedTransactionID = &d.ID
		row, err := s.store.InsertPTTx(ctx, tx, p.ID, callerUserID, pt)
		if err != nil {
			return uuid.Nil, nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE transactions
			SET project_id = $1, source_project_transaction_id = $2, updated_by_user_id = $3
			WHERE id = $4`, p.ID, row.ID, callerUserID, d.ID); err != nil {
			return uuid.Nil, nil, fmt.Errorf("link bill %s: %w", d.ID, err)
		}
		return row.ID, &d.ID, nil
	}

	// Someone else paid: the board row is theirs; my transaction is my share, no wallet.
	if existing != nil {
		pt.TagNames, err = personalTagNamesTx(ctx, tx, callerUserID, existing.ID)
	} else {
		pt.TagNames, err = personalTagNamesByIDTx(ctx, tx, callerUserID, req.TagIDs)
	}
	if err != nil {
		return uuid.Nil, nil, err
	}
	row, err := s.store.InsertPTTx(ctx, tx, p.ID, callerUserID, pt)
	if err != nil {
		return uuid.Nil, nil, err
	}
	var mine *uuid.UUID
	switch {
	case existing != nil && myShare <= 0.005:
		return uuid.Nil, nil, ErrNoShareToCopy // my transaction would have nothing left
	case existing != nil:
		// My transaction becomes my copy: my share, no wallet (its money comes back).
		up := transactions.UpdateRequest{Amount: &myShare}
		up.MarkPresent("account_id")
		if err := s.txs.UpdateInTx(ctx, tx, callerUserID, existing.ID, up); err != nil {
			return uuid.Nil, nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE transactions
			SET project_id = $1, source_project_transaction_id = $2, updated_by_user_id = $3
			WHERE id = $4`, p.ID, row.ID, callerUserID, existing.ID); err != nil {
			return uuid.Nil, nil, fmt.Errorf("link copy %s: %w", existing.ID, err)
		}
		mine = &existing.ID
	case myShare > 0.005:
		created, err := s.txs.CreateInTx(ctx, tx, callerUserID, transactions.CreateRequest{
			Type: transactions.TypeExpense, Amount: myShare, CategoryID: req.CategoryID, Date: req.Date,
			Description: req.Description, Note: req.Note, TagIDs: req.TagIDs,
			SourceProjectTransactionID: &row.ID,
		})
		if err != nil {
			return uuid.Nil, nil, err
		}
		if d, ok := created.(*transactions.TransactionDetail); ok {
			mine = &d.ID
		}
	}
	// A linked payer hears that a bill was recorded for them (as with any bill recorded for someone).
	if s.notifs != nil {
		var payerUser *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT user_id FROM project_members WHERE id = $1`, payerID).Scan(&payerUser); err != nil {
			return uuid.Nil, nil, fmt.Errorf("payer: %w", err)
		}
		if payerUser != nil && *payerUser != callerUserID {
			full, err := s.store.GetPTTx(ctx, tx, row.ID)
			if err != nil {
				return uuid.Nil, nil, err
			}
			if err := s.notifyRecordedTx(ctx, tx, *payerUser, payerID, callerUserID, full); err != nil {
				return uuid.Nil, nil, err
			}
		}
	}
	return row.ID, mine, nil
}
