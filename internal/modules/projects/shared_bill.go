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

// Shared bills (0.4.1-2 S1 / S5 / S6): an event bill and a split bill (a bill with no event, kept in
// a hidden one-bill "event", projects.kind = 'split') are the same thing. Every member who is an app
// user holds their own COPY of it in their book:
//   - the payer: a transaction of the whole bill (the bill's origin) + a debt per other member with a
//     share ("X owes me"), and for a member ticked "คืนแล้ว" also the repayment of it (E2);
//   - a member with a share S, someone else paid: a transaction of S with no wallet ("จ่ายโดย P")
//     + the debt "I owe P S"; ticked "คืนแล้ว": the transaction of S (wallet mine), no debt (E3);
//   - a payback (from → to): the repayment row.
// Category, wallet, tags and private_note are each member's own; amount, date, description and note
// follow the bill. applyCopyTx writes one member's copy from the bill as it is now (create or
// re-sync); fanOutTx sends a saved bill to every other member by THEIR switch.

const (
	KindEvent = "event"
	KindSplit = "split"

	roleNone        = ""
	rolePayer       = "payer"
	roleShare       = "share"
	rolePaybackFrom = "payback_from"
	rolePaybackTo   = "payback_to"
)

var (
	ErrBillDisconnected = errors.New("you disconnected your copy of this bill")
	ErrBillGone         = errors.New("this bill was deleted")
	ErrBillChanged      = errors.New("the bill was changed since you opened it: reload")
	ErrMoveTarget       = errors.New("move: exactly one of project_id, new_event, split (not the bill's own event)")
)

// billState — a bill as every copy follows it.
type billState struct {
	pt       *ProjectTransaction
	kind     string // event | split
	status   string
	name     string
	payer    uuid.UUID                             // the main (biggest) payer
	payers   map[uuid.UUID]float64                 // multi-payer (BE-2, 105): what each payer paid; one payer = the bill
	shares   map[uuid.UUID]float64                 // members with a part; the main payer's = the rest
	pairs    []BillPair                            // the real pairs: debtor owes creditor (income already flipped)
	backRows map[uuid.UUID]map[uuid.UUID]uuid.UUID // "คืนแล้ว": member → payer paid back → the payback row
	from, to uuid.UUID                             // a payback
	backFor  *uuid.UUID                            // a payback made for a bill's "คืนแล้ว" tick: that bill
}

// paidBack — the member ticked "คืนแล้ว" (paid every payer their pair on the spot).
func (b *billState) paidBack(m uuid.UUID) bool { return len(b.backRows[m]) > 0 }

// wantDebts — the debts member m's book holds of the bill, netted per other member from the real
// pairs: + = they owe me (paid), − = I owe them (received). A member who paid back holds none of
// their own; a payer keeps the debts of members who paid back (their repayments offset them, E2).
func (b *billState) wantDebts(m uuid.UUID) map[uuid.UUID]wantDebt {
	net := map[uuid.UUID]float64{}
	for _, p := range b.pairs {
		switch {
		case p.Creditor == m && p.Debtor != m:
			net[p.Debtor] += p.Amount
		case p.Debtor == m && p.Creditor != m && !b.paidBack(m):
			net[p.Creditor] -= p.Amount
		}
	}
	out := map[uuid.UUID]wantDebt{}
	for x, v := range net {
		switch {
		case v > 0.005:
			out[x] = wantDebt{personal_debts.DirectionPaid, round2(v)}
		case v < -0.005:
			out[x] = wantDebt{personal_debts.DirectionReceived, round2(-v)}
		}
	}
	return out
}

// pairAmount — what debtor owes creditor on the bill.
func (b *billState) pairAmount(debtor, creditor uuid.UUID) float64 {
	for _, p := range b.pairs {
		if p.Debtor == debtor && p.Creditor == creditor {
			return p.Amount
		}
	}
	return 0
}

func (s *Service) loadBillTx(ctx context.Context, tx pgx.Tx, ptID uuid.UUID) (*billState, error) {
	pt, err := s.store.GetPTTx(ctx, tx, ptID)
	if err != nil {
		return nil, err
	}
	if pt.ParentProjectTransactionID != nil {
		if pt, err = s.store.GetPTTx(ctx, tx, *pt.ParentProjectTransactionID); err != nil {
			return nil, err
		}
	}
	st := &billState{pt: pt, shares: map[uuid.UUID]float64{}, payers: map[uuid.UUID]float64{},
		backRows: map[uuid.UUID]map[uuid.UUID]uuid.UUID{}}
	if err := tx.QueryRow(ctx, `SELECT p.kind, p.status, p.name, b.paid_back_for_id
		FROM projects p JOIN project_transactions b ON b.project_id = p.id WHERE b.id = $1`, pt.ID).
		Scan(&st.kind, &st.status, &st.name, &st.backFor); err != nil {
		return nil, fmt.Errorf("bill event: %w", err)
	}
	shares := splitSharesTx(ctx, tx, pt.ID)
	if pt.Type == PTTypePayback {
		st.from = pt.TransactionMemberID
		for id := range shares {
			st.to = id
		}
		return st, nil
	}
	st.payer = pt.TransactionMemberID
	var sum float64
	for id, v := range shares {
		if v > 0.005 {
			st.shares[id] = round2(v)
			sum += v
		}
	}
	st.shares[st.payer] = round2(pt.Amount - sum)
	payers, err := BillPayersTx(ctx, tx, pt.ID)
	if err != nil {
		return nil, err
	}
	for _, p := range payers {
		st.payers[p.Creditor] = round2(p.Amount)
	}
	if st.pairs, err = BillPairsTx(ctx, tx, pt.ID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT pb.id, pb.transaction_member_id, c.transaction_member_id FROM project_transactions pb
		JOIN project_transactions c ON c.parent_project_transaction_id = pb.id
		WHERE pb.paid_back_for_id = $1 AND pb.parent_project_transaction_id IS NULL`, pt.ID)
	if err != nil {
		return nil, fmt.Errorf("bill paybacks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, by, to uuid.UUID
		if err := rows.Scan(&id, &by, &to); err != nil {
			return nil, err
		}
		if st.backRows[by] == nil {
			st.backRows[by] = map[uuid.UUID]uuid.UUID{}
		}
		st.backRows[by][to] = id
	}
	return st, rows.Err()
}

// role — a member's place in the bill and what their copy transaction holds.
func (b *billState) role(m uuid.UUID) (string, float64) {
	if b.pt.Type == PTTypePayback {
		switch m {
		case b.from:
			return rolePaybackFrom, b.pt.Amount
		case b.to:
			return rolePaybackTo, b.pt.Amount
		}
		return roleNone, 0
	}
	// A payer's copy holds max(what I paid, my share): the wallet moves what I paid, reports count my
	// share (shared.ShareAmountExpr); one payer = the whole bill, as before.
	if paid := b.payers[m]; paid > 0.005 {
		return rolePayer, math.Max(paid, b.shares[m])
	}
	if v := b.shares[m]; v > 0.005 {
		return roleShare, v
	}
	return roleNone, 0
}

// copyOptions — the editor's own choices for their copy (nil for another member: no wallet,
// category or tags; they pick later).
type copyOptions struct {
	wantTx, wantDebt bool
	account          *uuid.UUID // my wallet (when the money is mine to place)
	category         *uuid.UUID
	tagIDs           []uuid.UUID
	tagsSet          bool
	privateNote      *string
	privateNoteSet   bool
	repayAccount     *uuid.UUID // a NEW "คืนแล้ว" repayment of mine goes into this wallet (E2)
	beforeKey        func(key string) error
	beforeWallet     func(id uuid.UUID) error
}

// copyResult — what a copy write touched (the caller's effects).
type copyResult struct {
	txID         *uuid.UUID
	txs          []uuid.UUID
	removedTxs   []uuid.UUID
	debts        []uuid.UUID
	removedDebts []uuid.UUID
	keys         map[string]shared.PersonRef
	wallets      []uuid.UUID
	role         string
	amount       float64
}

func (r *copyResult) key(k string, p shared.PersonRef) {
	if r.keys == nil {
		r.keys = map[string]shared.PersonRef{}
	}
	r.keys[k] = p
}

// activeMemberTx — the user's active member row in the event.
func activeMemberTx(ctx context.Context, tx pgx.Tx, projectID, userID uuid.UUID) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM project_members WHERE project_id = $1 AND user_id = $2 AND status = 'active'`,
		projectID, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("member: %w", err)
	}
	return id, true, nil
}

func optedOutTx(ctx context.Context, tx pgx.Tx, ptID, userID uuid.UUID) (bool, error) {
	var out bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM shared_bill_opt_outs WHERE project_transaction_id = $1 AND user_id = $2)`,
		ptID, userID).Scan(&out)
	return out, err
}

// personOfTx — who a member is in the viewer's book: the viewer's contact for them, else their name.
func personOfTx(ctx context.Context, tx pgx.Tx, viewer, memberID uuid.UUID) (*uuid.UUID, string, error) {
	var (
		cid  *uuid.UUID
		name string
	)
	err := tx.QueryRow(ctx, `SELECT mc.id, COALESCE(mc.display_name, u.display_name, pm.display_name)
		FROM project_members pm LEFT JOIN users u ON u.id = pm.user_id
		LEFT JOIN LATERAL (
			SELECT c.id, c.display_name FROM contacts c
			WHERE c.user_id = $2 AND ((pm.user_id IS NOT NULL AND c.linked_user_id = pm.user_id)
			   OR (pm.user_id IS NULL AND LOWER(BTRIM(c.display_name)) = LOWER(BTRIM(pm.display_name))))
			ORDER BY (c.status = 'active') DESC, c.created_at, c.id LIMIT 1) mc ON TRUE
		WHERE pm.id = $1`, memberID, viewer).Scan(&cid, &name)
	if err != nil {
		return nil, "", fmt.Errorf("bill person: %w", err)
	}
	return cid, name, nil
}

// applyCopyTx writes the user's copy of the bill from the bill as it is now: made when missing,
// re-synced when it differs, removed when the user has no part any more. opts nil = another member
// (no wallet / category / tags are chosen for them).
func (s *Service) applyCopyTx(ctx context.Context, tx pgx.Tx, st *billState, userID uuid.UUID, opts *copyOptions) (*copyResult, error) {
	out := &copyResult{}
	m, ok, err := activeMemberTx(ctx, tx, st.pt.ProjectID, userID)
	if err != nil || !ok {
		return out, err
	}
	if opts == nil {
		opts = &copyOptions{wantTx: true, wantDebt: true}
	}
	if opts.beforeKey == nil {
		opts.beforeKey = func(string) error { return nil }
	}
	if opts.beforeWallet == nil {
		opts.beforeWallet = func(uuid.UUID) error { return nil }
	}
	out.role, out.amount = st.role(m)
	copyID, err := myLinkedTxTx(ctx, tx, userID, st.pt.ID)
	if err != nil {
		return nil, err
	}
	if out.role == roleNone {
		return out, s.dropCopyTx(ctx, tx, st, userID, copyID, opts, out)
	}
	if st.pt.Type == PTTypePayback {
		return out, s.paybackCopyTx(ctx, tx, st, userID, m, opts, out)
	}
	if opts.wantTx {
		if copyID, err = s.copyTxTx(ctx, tx, st, userID, m, copyID, opts, out); err != nil {
			return nil, err
		}
	}
	out.txID = copyID
	if opts.wantDebt {
		if err := s.copyDebtsTx(ctx, tx, st, userID, m, copyID, opts, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// copyTxTx — the copy transaction: made or re-synced; the wallet rule ("จ่ายโดย P" = no wallet);
// the payer's copy is the bill's origin.
func (s *Service) copyTxTx(ctx context.Context, tx pgx.Tx, st *billState, userID, m uuid.UUID, copyID *uuid.UUID,
	opts *copyOptions, out *copyResult) (*uuid.UUID, error) {
	paidBy := out.role == roleShare && !st.paidBack(m)
	desc, note := shared.CleanText(st.pt.Description), shared.CleanText(st.pt.Note)
	if copyID == nil {
		account := opts.account
		if paidBy {
			account = nil
		}
		if account != nil {
			if err := opts.beforeWallet(*account); err != nil {
				return nil, err
			}
			out.wallets = append(out.wallets, *account)
		}
		created, err := s.txs.CreateInTx(ctx, tx, userID, transactions.CreateRequest{
			Type: transactions.TxType(st.pt.Type), Amount: round2(out.amount), Date: st.pt.Date, AccountID: account,
			CategoryID: opts.category, Description: desc, Note: note, TagIDs: opts.tagIDs,
			SourceProjectTransactionID: &st.pt.ID,
		})
		if err != nil {
			return nil, err
		}
		d, ok := created.(*transactions.TransactionDetail)
		if !ok {
			return nil, ErrQuickTxNotBillable
		}
		copyID = &d.ID
	} else {
		cur, err := transactions.CopyRowTx(ctx, tx, *copyID)
		if err != nil {
			return nil, err
		}
		var up transactions.UpdateRequest
		changed := false
		if math.Abs(cur.Amount-out.amount) > 0.005 {
			a := round2(out.amount)
			up.Amount, changed = &a, true
		}
		if cur.Date != st.pt.Date {
			d := st.pt.Date
			up.Date, changed = &d, true
		}
		if !sameText(cur.Description, desc) || !sameText(cur.Note, note) {
			up.SetText(desc, note)
			changed = true
		}
		switch {
		case paidBy && cur.AccountID != nil:
			up.AccountID = nil
			up.MarkPresent("account_id")
			changed = true
		case !paidBy && opts.account != nil && (cur.AccountID == nil || *cur.AccountID != *opts.account):
			up.AccountID, changed = opts.account, true
		}
		if opts.category != nil && (cur.CategoryID == nil || *cur.CategoryID != *opts.category) {
			up.CategoryID, changed = opts.category, true
		}
		if opts.tagsSet {
			up.TagIDs = opts.tagIDs
			up.MarkPresent("tag_ids")
			changed = true
		}
		for _, w := range []*uuid.UUID{cur.AccountID, up.AccountID} {
			if w != nil && changed {
				if err := opts.beforeWallet(*w); err != nil {
					return nil, err
				}
				out.wallets = append(out.wallets, *w)
			}
		}
		if changed {
			if err := s.txs.UpdateCopyInTx(ctx, tx, userID, *copyID, up); err != nil {
				return nil, err
			}
		}
	}
	// Multi-payer: I paid less than my share and didn't pay the rest back → the wallet moves only what
	// I paid (paid_by_amount = the rest, "จ่ายโดย" the other payers).
	var rest *float64
	if paid := st.payers[m]; out.role == rolePayer && !st.paidBack(m) && out.amount-paid > 0.005 {
		v := round2(out.amount - paid)
		rest = &v
	}
	if w, err := s.txs.SetCopyPaidByTx(ctx, tx, userID, *copyID, rest); err != nil {
		return nil, err
	} else if w != nil {
		if err := opts.beforeWallet(*w); err != nil {
			return nil, err
		}
		out.wallets = append(out.wallets, *w)
	}
	if opts.privateNoteSet {
		if err := transactions.SetPrivateNoteTx(ctx, tx, userID, *copyID, opts.privateNote); err != nil {
			return nil, err
		}
	}
	// The payer's copy is the bill's origin; a copy that is no longer the payer's isn't.
	if out.role == rolePayer && m == st.payer { // the main payer's copy is the origin
		if _, err := tx.Exec(ctx, `UPDATE project_transactions SET linked_transaction_id = $2, unlinked_transaction_id = NULL
			WHERE id = $1 AND linked_transaction_id IS DISTINCT FROM $2`, st.pt.ID, *copyID); err != nil {
			return nil, fmt.Errorf("bill origin: %w", err)
		}
	} else if _, err := tx.Exec(ctx, `UPDATE project_transactions SET linked_transaction_id = NULL
		WHERE id = $1 AND linked_transaction_id = $2`, st.pt.ID, *copyID); err != nil {
		return nil, fmt.Errorf("bill origin: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE transactions SET source_project_transaction_id = $2, project_id = $3
		WHERE id = $1 AND (source_project_transaction_id IS DISTINCT FROM $2 OR project_id IS DISTINCT FROM $3)`,
		*copyID, st.pt.ID, st.pt.ProjectID); err != nil {
		return nil, fmt.Errorf("copy link: %w", err)
	}
	out.txs = append(out.txs, *copyID)
	return copyID, nil
}

type wantDebt struct {
	dir    string
	amount float64
}

// copyDebtsTx — the user's debts of the bill (one per other member with a part) and the payer's
// "คืนแล้ว" repayments, made / re-amounted / removed to match the bill.
func (s *Service) copyDebtsTx(ctx context.Context, tx pgx.Tx, st *billState, userID, m uuid.UUID, copyID *uuid.UUID,
	opts *copyOptions, out *copyResult) error {
	want := st.wantDebts(m)
	parts, err := myBillPartsTx(ctx, tx, userID, st.pt.ID)
	if err != nil {
		return err
	}
	for _, p := range parts {
		e, err := personal_debts.GetEntryTx(ctx, tx, userID, p.DebtID)
		if err != nil {
			return err
		}
		if e == nil {
			continue
		}
		w, ok := want[p.MemberID]
		if !ok || w.dir != e.Direction {
			if err := s.dropBillPartTx(ctx, tx, userID, p, e, opts, out); err != nil {
				return err
			}
			continue
		}
		delete(want, p.MemberID)
		if err := opts.beforeKey(e.Key()); err != nil {
			return err
		}
		if math.Abs(p.Amount-w.amount) > 0.005 {
			if err := s.debts.SetBillPartAmountTx(ctx, tx, userID, p.ID, w.amount); err != nil {
				return err
			}
		}
		if p.TransactionID == nil && copyID != nil {
			if err := personal_debts.SetBillPartTransactionTx(ctx, tx, userID, p.ID, copyID); err != nil {
				return err
			}
		}
		if err := s.debts.SyncBillRowTx(ctx, tx, userID, e.ID, nil, st.pt.Date, st.pt.Description, st.pt.Note); err != nil {
			return err
		}
		out.debts = append(out.debts, e.ID)
		out.key(e.Key(), shared.PersonRef{Name: e.PersonName, ContactID: e.ContactID})
	}
	for x, w := range want {
		if err := s.billEntryTx(ctx, tx, st, userID, x, personal_debts.KindDebt, w.dir, w.amount, st.pt.ID, copyID, nil, opts, out); err != nil {
			return err
		}
	}
	if out.role == rolePayer {
		return s.copyRepaymentsTx(ctx, tx, st, userID, m, opts, out)
	}
	return nil
}

// copyRepaymentsTx — the payer's "คืนแล้ว" repayments: one per payback row made for the bill.
func (s *Service) copyRepaymentsTx(ctx context.Context, tx pgx.Tx, st *billState, userID, m uuid.UUID,
	opts *copyOptions, out *copyResult) error {
	have := map[uuid.UUID]uuid.UUID{} // payback row → my repayment
	mine := map[uuid.UUID]uuid.UUID{} // a member who paid ME back → that payback row
	for x, byPayer := range st.backRows {
		if row, ok := byPayer[m]; ok && x != m {
			mine[x] = row
		}
	}
	if len(mine) > 0 {
		rows := make([]uuid.UUID, 0, len(mine))
		for _, id := range mine {
			rows = append(rows, id)
		}
		r, err := tx.Query(ctx, `SELECT source_project_transaction_id, id FROM personal_debts
			WHERE user_id = $1 AND kind = 'repayment' AND source_project_transaction_id = ANY($2)`, userID, rows)
		if err != nil {
			return fmt.Errorf("bill repayments: %w", err)
		}
		for r.Next() {
			var row, id uuid.UUID
			if err := r.Scan(&row, &id); err != nil {
				r.Close()
				return err
			}
			have[row] = id
		}
		r.Close()
		if err := r.Err(); err != nil {
			return err
		}
	}
	for x, row := range mine {
		amount := st.pairAmount(x, m)
		if amount <= 0.005 {
			continue
		}
		if id, ok := have[row]; ok {
			a := amount
			if err := s.debts.SyncBillRowTx(ctx, tx, userID, id, &a, st.pt.Date, st.pt.Description, st.pt.Note); err != nil {
				return err
			}
			out.debts = append(out.debts, id)
			continue
		}
		if err := s.billEntryTx(ctx, tx, st, userID, x, personal_debts.KindRepayment, personal_debts.DirectionReceived, amount, row, nil,
			opts.repayAccount, opts, out); err != nil {
			return err
		}
	}
	return nil
}

// billEntryTx writes one person-book row of the bill in the user's book.
func (s *Service) billEntryTx(ctx context.Context, tx pgx.Tx, st *billState, userID, memberID uuid.UUID, kind, dir string,
	amount float64, source uuid.UUID, copyID, account *uuid.UUID, opts *copyOptions, out *copyResult) error {
	cid, name, err := personOfTx(ctx, tx, userID, memberID)
	if err != nil {
		return err
	}
	key := shared.DebtKey(cid, name)
	if err := opts.beforeKey(key); err != nil {
		return err
	}
	in := personal_debts.EntryInput{Kind: kind, Direction: dir, ContactID: cid, PersonName: name, Amount: round2(amount),
		Date: st.pt.Date, Description: shared.CleanText(st.pt.Description), Note: shared.CleanText(st.pt.Note),
		BillID: &source, EventID: &st.pt.ProjectID}
	if kind == personal_debts.KindDebt {
		in.MemberID, in.BillTransactionID = &memberID, copyID
	}
	if account != nil {
		if err := opts.beforeWallet(*account); err != nil {
			return err
		}
		in.CreateTransaction, in.AccountID = true, account
		out.wallets = append(out.wallets, *account)
	}
	e, _, err := s.debts.CreateEntryTx(ctx, tx, userID, in)
	if err != nil {
		return err
	}
	out.debts = append(out.debts, e.ID)
	out.key(key, shared.PersonRef{Name: name, ContactID: cid})
	return nil
}

// dropBillPartTx — a debt of the bill that is no longer wanted: its part goes; a debt left with no
// part (it covered only this bill) is deleted.
func (s *Service) dropBillPartTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, p personal_debts.BillPart,
	e *personal_debts.Entry, opts *copyOptions, out *copyResult) error {
	if err := opts.beforeKey(e.Key()); err != nil {
		return err
	}
	out.key(e.Key(), shared.PersonRef{Name: e.PersonName, ContactID: e.ContactID})
	if _, err := s.debts.UnlinkBillPartTx(ctx, tx, userID, p.ID); err != nil {
		return err
	}
	var left int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM personal_debt_bills WHERE debt_id = $1`, e.ID).Scan(&left); err != nil {
		return fmt.Errorf("parts left: %w", err)
	}
	if left > 0 {
		out.debts = append(out.debts, e.ID)
		return nil
	}
	w, err := s.debts.DeleteEntryTx(ctx, tx, userID, e.ID)
	if err != nil {
		return err
	}
	if w != nil {
		out.wallets = append(out.wallets, *w)
	}
	out.removedDebts = append(out.removedDebts, e.ID)
	return nil
}

// dropCopyTx — the user has no part in the bill any more: their copy, their debts and repayments
// of it go.
func (s *Service) dropCopyTx(ctx context.Context, tx pgx.Tx, st *billState, userID uuid.UUID, copyID *uuid.UUID,
	opts *copyOptions, out *copyResult) error {
	parts, err := myBillPartsTx(ctx, tx, userID, st.pt.ID)
	if err != nil {
		return err
	}
	for _, p := range parts {
		e, err := personal_debts.GetEntryTx(ctx, tx, userID, p.DebtID)
		if err != nil {
			return err
		}
		if e != nil {
			if err := s.dropBillPartTx(ctx, tx, userID, p, e, opts, out); err != nil {
				return err
			}
		}
	}
	ids, err := collectIDs(ctx, tx, `SELECT id FROM personal_debts WHERE user_id = $1 AND kind = 'repayment'
		AND source_project_transaction_id IN (SELECT id FROM project_transactions WHERE paid_back_for_id = $2 OR id = $2)`,
		userID, st.pt.ID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.dropEntryTx(ctx, tx, userID, id, opts, out); err != nil {
			return err
		}
	}
	if copyID == nil {
		return nil
	}
	var wallet *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT account_id FROM transactions WHERE id = $1`, *copyID).Scan(&wallet); err != nil {
		return fmt.Errorf("copy wallet: %w", err)
	}
	if wallet != nil {
		if err := opts.beforeWallet(*wallet); err != nil {
			return err
		}
		out.wallets = append(out.wallets, *wallet)
	}
	if _, err := tx.Exec(ctx, `UPDATE project_transactions SET linked_transaction_id = NULL WHERE linked_transaction_id = $1`,
		*copyID); err != nil {
		return fmt.Errorf("unlink origin: %w", err)
	}
	if _, err := s.txs.DeleteInTx(ctx, tx, userID, *copyID); err != nil {
		return err
	}
	out.removedTxs = append(out.removedTxs, *copyID)
	return nil
}

func (s *Service) dropEntryTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID, opts *copyOptions, out *copyResult) error {
	e, err := personal_debts.GetEntryTx(ctx, tx, userID, id)
	if err != nil || e == nil {
		return err
	}
	if err := opts.beforeKey(e.Key()); err != nil {
		return err
	}
	out.key(e.Key(), shared.PersonRef{Name: e.PersonName, ContactID: e.ContactID})
	w, err := s.debts.DeleteEntryTx(ctx, tx, userID, id)
	if err != nil {
		return err
	}
	if w != nil {
		out.wallets = append(out.wallets, *w)
	}
	out.removedDebts = append(out.removedDebts, id)
	return nil
}

// paybackCopyTx — a payback bill in a member's book: the repayment row (no transaction; the editor's
// own book goes through paybackBooks).
func (s *Service) paybackCopyTx(ctx context.Context, tx pgx.Tx, st *billState, userID, m uuid.UUID,
	opts *copyOptions, out *copyResult) error {
	if st.backFor != nil {
		return nil // a "คืนแล้ว" tick: the bill's own copies carry it
	}
	var id *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM personal_debts WHERE user_id = $1 AND kind = 'repayment'
		AND source_project_transaction_id = $2 LIMIT 1`, userID, st.pt.ID).Scan(&id); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("payback repayment: %w", err)
	}
	if id != nil {
		a := st.pt.Amount
		out.debts = append(out.debts, *id)
		return s.debts.SyncBillRowTx(ctx, tx, userID, *id, &a, st.pt.Date, st.pt.Description, st.pt.Note)
	}
	other, dir := st.to, personal_debts.DirectionPaid
	if out.role == rolePaybackTo {
		other, dir = st.from, personal_debts.DirectionReceived
	}
	return s.billEntryTx(ctx, tx, st, userID, other, personal_debts.KindRepayment, dir, st.pt.Amount, st.pt.ID, nil, nil, opts, out)
}

// disconnectCopyTx — I disconnect my copy of the bill (rule a / f): my copy transaction and my debts /
// repayments of it become plain records (each remembers the bill, unlinked_project_transaction_id),
// and the bill no longer reaches me (shared_bill_opt_outs) until I connect again. Idempotent.
func (s *Service) disconnectCopyTx(ctx context.Context, tx pgx.Tx, ptID, userID uuid.UUID) (*copyResult, error) {
	out := &copyResult{}
	if _, err := tx.Exec(ctx, `INSERT INTO shared_bill_opt_outs (project_transaction_id, user_id) VALUES ($1, $2)
		ON CONFLICT DO NOTHING`, ptID, userID); err != nil {
		return nil, fmt.Errorf("disconnect: %w", err)
	}
	if err := unlinkMineTx(ctx, tx, userID, ptID, BookTx); err != nil {
		return nil, err
	}
	parts, err := myBillPartsTx(ctx, tx, userID, ptID)
	if err != nil {
		return nil, err
	}
	for _, p := range parts {
		if _, err := s.debts.UnlinkBillPartTx(ctx, tx, userID, p.ID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE personal_debts SET unlinked_project_transaction_id = $2, source_kind = NULL,
			source_project_transaction_id = NULL, project_id = NULL, updated_by_user_id = user_id
			WHERE id = $1 AND NOT EXISTS (SELECT 1 FROM personal_debt_bills WHERE debt_id = $1)`, p.DebtID, ptID); err != nil {
			return nil, fmt.Errorf("plain debt: %w", err)
		}
		out.debts = append(out.debts, p.DebtID)
	}
	ids, err := collectIDs(ctx, tx, `UPDATE personal_debts SET unlinked_project_transaction_id = source_project_transaction_id,
			source_kind = NULL, source_project_transaction_id = NULL, project_id = NULL, updated_by_user_id = user_id
		WHERE user_id = $1 AND source_project_transaction_id IN (
			SELECT id FROM project_transactions WHERE id = $2 OR paid_back_for_id = $2)
		RETURNING id`, userID, ptID)
	if err != nil {
		return nil, fmt.Errorf("plain repayments: %w", err)
	}
	out.debts = append(out.debts, ids...)
	return out, nil
}

// CopyDeletingTx — transactions.EventLinks (rule f): my copy is about to be deleted = I disconnect
// it (the bill and the others are unchanged; my debts of it stay, plain).
func (s *Service) CopyDeletingTx(ctx context.Context, tx pgx.Tx, userID, txID uuid.UUID) error {
	row := transactions.Transaction{ID: txID}
	if err := tx.QueryRow(ctx, `SELECT source_project_transaction_id FROM transactions WHERE id = $1 AND user_id = $2`,
		txID, userID).Scan(&row.SourceProjectTransactionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("copy: %w", err)
	}
	bill, err := billOfTx(ctx, tx, &row)
	if err != nil || bill == nil {
		return err
	}
	_, err = s.disconnectCopyTx(ctx, tx, bill.ID, userID)
	return err
}

// reconnectCopyTx — I connect my copy again (my own choice): the opt-out goes, my old copy
// transaction takes the link back, and my old debts / repayments of the bill are dropped (applyCopyTx
// writes them fresh from the bill). No-op when I never disconnected.
func (s *Service) reconnectCopyTx(ctx context.Context, tx pgx.Tx, ptID, userID uuid.UUID) error {
	tag, err := tx.Exec(ctx, `DELETE FROM shared_bill_opt_outs WHERE project_transaction_id = $1 AND user_id = $2`, ptID, userID)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	var projectID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT project_id FROM project_transactions WHERE id = $1`, ptID).Scan(&projectID); err != nil {
		return fmt.Errorf("bill event: %w", err)
	}
	if _, err := relinkMineTx(ctx, tx, userID, projectID, ptID, BookTx); err != nil {
		return err
	}
	ids, err := collectIDs(ctx, tx, `SELECT id FROM personal_debts WHERE user_id = $1 AND unlinked_project_transaction_id IN (
			SELECT id FROM project_transactions WHERE id = $2 OR paid_back_for_id = $2)
		  AND NOT EXISTS (SELECT 1 FROM personal_debt_bills b WHERE b.debt_id = personal_debts.id)`, userID, ptID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.debts.DeleteEntryTx(ctx, tx, userID, id); err != nil {
			return err
		}
	}
	return nil
}

func myBillPartsTx(ctx context.Context, tx pgx.Tx, userID, ptID uuid.UUID) ([]personal_debts.BillPart, error) {
	all, err := personal_debts.BillPartsOfBillTx(ctx, tx, ptID)
	if err != nil {
		return nil, err
	}
	out := make([]personal_debts.BillPart, 0, len(all))
	for _, p := range all {
		var owner uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT user_id FROM personal_debt_bills WHERE id = $1`, p.ID).Scan(&owner); err != nil {
			return nil, fmt.Errorf("part owner: %w", err)
		}
		if owner == userID {
			out = append(out, p)
		}
	}
	return out, nil
}

func sameText(a, b *string) bool {
	a, b = shared.CleanText(a), shared.CleanText(b)
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// fanOutTx — a saved bill reaches every OTHER member who is an app user (active, not disconnected
// from it) by their own switch (events: auto_sync_events; split bills: auto_sync_debts): on → their
// copy is written now and the notice is info; off → the notice asks. created = a new bill
// (shared_bill_created) vs an edit (project_tx_changed).
func (s *Service) fanOutTx(ctx context.Context, tx pgx.Tx, editor, ptID uuid.UUID, created bool, diff map[string][2]interface{}) error {
	if s.notifs == nil || s.debts == nil || s.txs == nil {
		return nil
	}
	st, err := s.loadBillTx(ctx, tx, ptID)
	if err != nil {
		return err
	}
	if st.status == StatusCompleted || (st.pt.Type == PTTypePayback && st.backFor != nil) {
		return nil
	}
	users, err := collectIDs(ctx, tx, `SELECT DISTINCT pm.user_id FROM project_members pm
		WHERE pm.project_id = $1 AND pm.user_id IS NOT NULL AND pm.user_id <> $2 AND pm.status = 'active'
		  AND NOT EXISTS (SELECT 1 FROM shared_bill_opt_outs o WHERE o.project_transaction_id = $3 AND o.user_id = pm.user_id)`,
		st.pt.ProjectID, editor, st.pt.ID)
	if err != nil {
		return err
	}
	editorName := userDisplayOrEmpty(ctx, tx, editor)
	var projectName *string
	if st.kind == KindEvent {
		projectName = &st.name
	}
	for _, u := range users {
		m, _, err := activeMemberTx(ctx, tx, st.pt.ProjectID, u)
		if err != nil {
			return err
		}
		role, amount := st.role(m)
		copyID, err := myLinkedTxTx(ctx, tx, u, st.pt.ID)
		if err != nil {
			return err
		}
		if role == roleNone && copyID == nil {
			continue
		}
		// An edit reaches by auto-sync only a member who holds the bill (accepted it); one who never
		// accepted keeps being asked to add it, the notice refreshed with the bill as it is now.
		asNew := created
		if !created && copyID == nil {
			var parts int
			if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM personal_debt_bills WHERE user_id = $1 AND pt_id = $2`,
				u, st.pt.ID).Scan(&parts); err != nil {
				return fmt.Errorf("member holds bill: %w", err)
			}
			if parts == 0 {
				if role == roleNone {
					continue
				}
				asNew = true
			}
		}
		typ := notifications.TypeProjectTxChanged
		if asNew {
			typ = notifications.TypeSharedBillCreated
		}
		d, err := s.notifs.DeliveryFor(ctx, tx, u, &editor, typ)
		if err != nil {
			return err
		}
		// owner 2026-10-28: a NEW bill becomes a copy at once only with auto-accept on; later edits
		// follow auto-sync.
		sync := d.SyncEvents
		switch {
		case created && st.kind == KindSplit:
			sync = d.AcceptDebts
		case created:
			sync = d.AcceptEvents
		case asNew: // an edit of a bill the member never accepted: keep asking
			sync = false
		case st.kind == KindSplit:
			sync = d.SyncDebts
		}
		if sync {
			res, err := s.applyCopyTx(ctx, tx, st, u, nil)
			if err != nil {
				return err
			}
			copyID = res.txID
		}
		if err := s.notifs.SupersedeBillNoticesTx(ctx, tx, u, st.pt.ID); err != nil {
			return err
		}
		if asNew {
			if role == roleNone {
				continue
			}
			payer := st.payer
			if st.pt.Type == PTTypePayback {
				payer = st.from
			}
			_, payerName, err := personOfTx(ctx, tx, u, payer)
			if err != nil {
				return err
			}
			var applied *uuid.UUID
			if sync {
				applied = copyID
			}
			if err := s.notifs.DispatchSharedBillCreated(ctx, tx, u, editor, notifications.SharedBillCreatedPayload{
				ProjectTransactionID: st.pt.ID, ProjectID: st.pt.ProjectID, ProjectKind: st.kind, ProjectName: projectName,
				Description: st.pt.Description, Amount: st.pt.Amount, MyRole: role, MyAmount: round2(amount),
				PayerName: payerName, Applied: sync, PersonalTransactionID: applied,
			}); err != nil {
				return err
			}
			continue
		}
		if err := s.notifs.DispatchProjectTxChanged(ctx, tx, u, editor, notifications.ProjectTxChangedPayload{
			ProjectTransactionID: st.pt.ID, ProjectID: st.pt.ProjectID, EditorUserID: editor,
			EditorDisplayName: editorName, ChangeKind: "edited", Diff: diff, PersonalTransactionID: copyID,
			ProjectKind: st.kind, Applied: sync,
		}, sync); err != nil {
			return err
		}
	}
	return nil
}
