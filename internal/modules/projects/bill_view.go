package projects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// The ONE bill form (0.4.1-2 SA2 / S7 / S13): GET /event-bills/{pt_id} (the bill, its event, the
// rights and MY copy), POST …/apply ("อัปเดตตาม" / "เพิ่มในสมุด"), POST …/disconnect, DELETE.

type EventBillRights struct {
	CanEdit        bool `json:"can_edit"`
	CanDelete      bool `json:"can_delete"`
	CanChangePayer bool `json:"can_change_payer"`
	CanMove        bool `json:"can_move"`
}

// MyBillCopy — my copy of a shared bill (openapi MyBillCopy).
type MyBillCopy struct {
	Connected   bool                            `json:"connected"`
	Role        string                          `json:"role"`
	Amount      float64                         `json:"amount"`
	Transaction *transactions.TransactionDetail `json:"transaction"`
	PaidBy      *shared.MemberRef               `json:"paid_by"`
	PaidBackMe  bool                            `json:"paid_back_me"`
	// MyPaid / PaidByParts (multi-payer): what I paid of the bill; who paid my share and how much each.
	MyPaid        float64                 `json:"my_paid"`
	PaidByParts   []BillPayer             `json:"paid_by_parts"`
	Debts         []any                   `json:"debts"`
	InSync        *bool                   `json:"in_sync"`
	Differences   []shared.BillDifference `json:"differences"`
	PendingUpdate bool                    `json:"pending_update"`
	PrivateNote   *string                 `json:"private_note"`
}

type EventBillView struct {
	Bill      BoardRow        `json:"bill"`
	Event     shared.EventRef `json:"event"`
	Members   []EventMember   `json:"members"`
	Tags      []EventTag      `json:"tags"`
	Rights    EventBillRights `json:"rights"`
	MyCopy    *MyBillCopy     `json:"my_copy"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type EventBillApplyResult struct {
	MyCopy *MyBillCopy `json:"my_copy"`
	// Bill — the whole bill form after the write (App-1: one call, no GET after); null when the
	// bill is gone or I can no longer open it.
	Bill    *EventBillView `json:"bill"`
	Effects shared.Effects `json:"effects"`
}

type EventBillDeleteResult struct {
	ID             uuid.UUID      `json:"id"`
	ProjectID      uuid.UUID      `json:"project_id"`
	RemovedProject bool           `json:"removed_project"`
	Event          *EventPatch    `json:"event"`
	Effects        shared.Effects `json:"effects"`
}

// BillChangedError — 409 BILL_CHANGED with the bill as it is now.
type BillChangedError struct{ Bill *EventBillView }

func (e *BillChangedError) Error() string { return ErrBillChanged.Error() }
func (e *BillChangedError) Unwrap() error { return ErrBillChanged }

// billOf — the bill (parent) of a row id.
func (s *Service) billOf(ctx context.Context, ptID uuid.UUID) (*ProjectTransaction, error) {
	pt, err := s.store.GetPT(ctx, ptID)
	if err != nil {
		return nil, err
	}
	if pt.ParentProjectTransactionID != nil {
		return s.store.GetPT(ctx, *pt.ParentProjectTransactionID)
	}
	return pt, nil
}

// GetBill — GET /event-bills/{pt_id}.
func (s *Service) GetBill(ctx context.Context, userID, ptID uuid.UUID) (*EventBillView, error) {
	pt, err := s.billOf(ctx, ptID)
	if err != nil {
		return nil, err
	}
	return s.billView(ctx, userID, pt)
}

func (s *Service) billView(ctx context.Context, userID uuid.UUID, pt *ProjectTransaction) (*EventBillView, error) {
	q := s.store.db
	var (
		mem     *myMember
		holder  bool
		project struct{ kind, status string }
	)
	m := &myMember{}
	err := q.QueryRow(ctx, `SELECT id, role, status FROM project_members WHERE project_id = $1 AND user_id = $2`,
		pt.ProjectID, userID).Scan(&m.id, &m.role, &m.status)
	switch {
	case err == nil:
		mem = m
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("bill member: %w", err)
	}
	if err := q.QueryRow(ctx, `SELECT p.kind, p.status, EXISTS (SELECT 1 FROM transactions t WHERE t.user_id = $2
			AND (t.source_project_transaction_id = $3 OR t.unlinked_project_transaction_id = $3))
		FROM projects p WHERE p.id = $1`, pt.ProjectID, userID, pt.ID).Scan(&project.kind, &project.status, &holder); err != nil {
		return nil, fmt.Errorf("bill event: %w", err)
	}
	if mem == nil && !holder {
		return nil, ErrPTNotFound
	}
	ctx = shared.WithMemberRefCache(ctx)
	out := &EventBillView{UpdatedAt: pt.UpdatedAt, Tags: []EventTag{}}
	rows, err := s.boardRows(ctx, userID, pt.ProjectID, []uuid.UUID{pt.ID})
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, ErrPTNotFound
	}
	out.Bill = rows[0]
	refs, err := shared.LoadEventRefs(ctx, q, []uuid.UUID{pt.ProjectID})
	if err != nil {
		return nil, err
	}
	out.Event = refs[pt.ProjectID]
	if project.kind == KindSplit {
		out.Event.Name = shared.TextOr(shared.CleanText(pt.Description))
	}
	if out.Members, err = loadMembers(ctx, q, userID, pt.ProjectID); err != nil {
		return nil, err
	}
	if project.kind == KindEvent {
		if out.Tags, err = loadEventTags(ctx, q, userID, pt.ProjectID); err != nil {
			return nil, err
		}
	}
	writable := project.status == StatusActive && mem != nil && mem.status == MemberStatusActive &&
		(project.kind == KindSplit || mem.role != RoleViewer)
	out.Rights = EventBillRights{CanEdit: writable, CanDelete: writable, CanChangePayer: writable && pt.Type != PTTypePayback,
		CanMove: writable}
	if out.MyCopy, err = s.myBillCopy(ctx, userID, pt, mem); err != nil {
		return nil, err
	}
	return out, nil
}

// myBillCopy — my copy of the bill (nil when I have no part in it and hold nothing of it).
func (s *Service) myBillCopy(ctx context.Context, userID uuid.UUID, pt *ProjectTransaction, mem *myMember) (*MyBillCopy, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	st, err := s.loadBillTx(ctx, tx, pt.ID)
	if err != nil {
		return nil, err
	}
	role, amount := roleNone, 0.0
	var m uuid.UUID
	if mem != nil {
		m = mem.id
		role, amount = st.role(m)
	}
	out := &MyBillCopy{Role: role, Amount: round2(amount), Debts: []any{}, Differences: []shared.BillDifference{},
		PaidByParts: []BillPayer{}}
	opted, err := optedOutTx(ctx, tx, pt.ID, userID)
	if err != nil {
		return nil, err
	}
	out.Connected = !opted
	copyID, err := myLinkedTxTx(ctx, tx, userID, pt.ID)
	if err != nil {
		return nil, err
	}
	if copyID == nil {
		var id uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM transactions WHERE user_id = $1 AND unlinked_project_transaction_id = $2
			ORDER BY created_at LIMIT 1`, userID, pt.ID).Scan(&id); err == nil {
			copyID = &id
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("my plain copy: %w", err)
		}
	}
	debtIDs, err := collectIDs(ctx, tx, `SELECT debt_id FROM personal_debt_bills WHERE user_id = $1 AND pt_id = $2
		UNION SELECT id FROM personal_debts WHERE user_id = $1 AND kind = 'repayment' AND source_project_transaction_id IN (
			SELECT id FROM project_transactions WHERE paid_back_for_id = $2 OR id = $2)`, userID, pt.ID)
	if err != nil {
		return nil, err
	}
	if role == roleNone && copyID == nil && len(debtIDs) == 0 {
		return nil, nil
	}
	out.MyPaid = round2(st.payers[m])
	if st.paidBack(m) {
		out.PaidBackMe = true
	} else if role != roleNone {
		// who paid my share (multi-payer: each payer's part of it); paid_by = the biggest of them
		var ids []uuid.UUID
		for _, p := range st.pairs {
			if p.Debtor == m && p.Creditor != m {
				ids = append(ids, p.Creditor)
			}
		}
		refs, err := LoadMemberRefs(ctx, tx, userID, ids)
		if err != nil {
			return nil, err
		}
		best := 0.0
		for _, p := range st.pairs {
			if r, ok := refs[p.Creditor]; ok && p.Debtor == m {
				out.PaidByParts = append(out.PaidByParts, BillPayer{Member: r, Amount: p.Amount})
				if p.Amount > best {
					rr := r
					out.PaidBy, best = &rr, p.Amount
				}
			}
		}
	}
	if copyID != nil {
		d, err := s.txs.Get(ctx, userID, *copyID)
		if err != nil && !errors.Is(err, transactions.ErrTxNotFound) {
			return nil, err
		}
		if d != nil {
			out.Transaction, out.PrivateNote = d, d.PrivateNote
			if d.EventBill != nil && d.EventBill.InSync != nil {
				out.InSync, out.Differences = d.EventBill.InSync, d.EventBill.Differences
			}
		}
	}
	if len(debtIDs) > 0 {
		eff, err := s.debts.RowEffects(ctx, tx, userID, debtIDs, nil, nil)
		if err != nil {
			return nil, err
		}
		out.Debts = eff.Debts
	}
	if out.Connected && role != roleNone && pt.Type != PTTypePayback {
		ok, err := debtsInSyncTx(ctx, tx, st, userID, m)
		if err != nil {
			return nil, err
		}
		inSync := ok && (out.InSync == nil || *out.InSync)
		if out.Transaction == nil {
			inSync = false
		}
		out.InSync = &inSync
	}
	if out.PendingUpdate, err = pendingBillTx(ctx, tx, userID, pt.ID); err != nil {
		return nil, err
	}
	return out, nil
}

// debtsInSyncTx — my bill debts hold what the bill says (one per other member with a part).
func debtsInSyncTx(ctx context.Context, tx pgx.Tx, st *billState, userID, m uuid.UUID) (bool, error) {
	want := st.wantDebts(m)
	parts, err := myBillPartsTx(ctx, tx, userID, st.pt.ID)
	if err != nil {
		return false, err
	}
	if len(parts) != len(want) {
		return false, nil
	}
	for _, p := range parts {
		if w, ok := want[p.MemberID]; !ok || math.Abs(w.amount-p.Amount) > 0.005 {
			return false, nil
		}
	}
	return true, nil
}

func pendingBillTx(ctx context.Context, tx pgx.Tx, userID, ptID uuid.UUID) (bool, error) {
	var out bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM notifications
		WHERE recipient_user_id = $1 AND actioned_at IS NULL AND dismissed_at IS NULL
		  AND type IN ('shared_bill_created', 'project_tx_changed')
		  AND COALESCE((payload->>'applied')::boolean, FALSE) = FALSE
		  AND payload->>'project_transaction_id' = $2)`, userID, ptID.String()).Scan(&out)
	return out, err
}

// copyEffects — the B2 answer of a copy write, read after the commit.
func (s *Service) copyEffects(ctx context.Context, userID uuid.UUID, res *copyResult) (shared.Effects, error) {
	keys := make([]string, 0, len(res.keys))
	for k := range res.keys {
		keys = append(keys, k)
	}
	eff, err := s.debts.RowEffects(ctx, s.store.Pool(), userID, uniqueIDs(res.debts), uniqueIDs(res.removedDebts), keys)
	if err != nil {
		return eff, err
	}
	if txs, err := s.txs.DetailsTx(ctx, s.store.Pool(), userID, uniqueIDs(res.txs)); err != nil {
		return eff, err
	} else {
		for _, t := range txs {
			eff.Transactions = append(eff.Transactions, t)
		}
	}
	eff.RemovedTransactionIDs = append(eff.RemovedTransactionIDs, uniqueIDs(res.removedTxs)...)
	if eff.AccountBalances, err = shared.LoadWalletBalances(ctx, s.store.Pool(), uniqueIDs(res.wallets)); err != nil {
		return eff, err
	}
	if eff.AccountBalances == nil {
		eff.AccountBalances = []shared.WalletBalance{}
	}
	return eff, nil
}

// ApplyBill — POST /event-bills/{pt_id}/apply: my copy takes the bill as it is now (S7).
func (s *Service) ApplyBill(ctx context.Context, userID, ptID uuid.UUID) (*EventBillApplyResult, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	st, err := s.loadBillTx(ctx, tx, ptID)
	if errors.Is(err, ErrPTNotFound) {
		return s.applyDeletedBillTx(ctx, tx, userID, ptID)
	}
	if err != nil {
		return nil, err
	}
	if st.status == StatusCompleted {
		return nil, ErrProjectLocked
	}
	opted, err := optedOutTx(ctx, tx, st.pt.ID, userID)
	if err != nil {
		return nil, err
	}
	if opted {
		return nil, ErrBillDisconnected
	}
	if _, ok, err := activeMemberTx(ctx, tx, st.pt.ProjectID, userID); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrPTNotFound
	}
	res, err := s.applyCopyTx(ctx, tx, st, userID, nil)
	if err != nil {
		return nil, err
	}
	if s.notifs != nil {
		if err := s.notifs.SupersedeBillNoticesTx(ctx, tx, userID, st.pt.ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.applyResult(ctx, userID, st.pt.ID, res)
}

func (s *Service) applyResult(ctx context.Context, userID, ptID uuid.UUID, res *copyResult) (*EventBillApplyResult, error) {
	out := &EventBillApplyResult{}
	var err error
	if out.Effects, err = s.copyEffects(ctx, userID, res); err != nil {
		return nil, err
	}
	pt, err := s.store.GetPT(ctx, ptID)
	if errors.Is(err, ErrPTNotFound) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var mem *myMember
	m := &myMember{}
	if err := s.store.db.QueryRow(ctx, `SELECT id, role, status FROM project_members WHERE project_id = $1 AND user_id = $2`,
		pt.ProjectID, userID).Scan(&m.id, &m.role, &m.status); err == nil {
		mem = m
	}
	if out.MyCopy, err = s.myBillCopy(ctx, userID, pt, mem); err != nil {
		return nil, err
	}
	bill := pt
	if pt.ParentProjectTransactionID != nil { // a share row: the form is its bill's
		if bill, err = s.billOf(ctx, ptID); err != nil {
			return nil, err
		}
	}
	switch out.Bill, err = s.billView(ctx, userID, bill); {
	case errors.Is(err, ErrPTNotFound):
		out.Bill = nil // e.g. disconnected and not a member: nothing to open
	case err != nil:
		return nil, err
	}
	return out, nil
}

// applyDeletedBillTx — "apply" on a deleted bill's notice that asked (V13, switch off): my records
// of it, kept plain, are removed now. Without such a notice: 410 BILL_GONE.
func (s *Service) applyDeletedBillTx(ctx context.Context, tx pgx.Tx, userID, ptID uuid.UUID) (*EventBillApplyResult, error) {
	var (
		nid     uuid.UUID
		payload []byte
	)
	err := tx.QueryRow(ctx, `SELECT id, payload FROM notifications WHERE recipient_user_id = $1 AND type = 'shared_bill_deleted'
		AND actioned_at IS NULL AND payload->>'project_transaction_id' = $2 ORDER BY created_at DESC LIMIT 1`,
		userID, ptID.String()).Scan(&nid, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBillGone
	}
	if err != nil {
		return nil, fmt.Errorf("deleted bill notice: %w", err)
	}
	var p notifications.SharedBillDeletedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	res := &copyResult{}
	noop := &copyOptions{beforeKey: func(string) error { return nil }, beforeWallet: func(uuid.UUID) error { return nil }}
	for _, id := range p.MyTransactionIDs {
		var wallet *uuid.UUID
		err := tx.QueryRow(ctx, `SELECT account_id FROM transactions WHERE id = $1 AND user_id = $2`, id, userID).Scan(&wallet)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("plain copy: %w", err)
		}
		if _, err := s.txs.DeleteInTx(ctx, tx, userID, id); err != nil {
			return nil, err
		}
		if wallet != nil {
			res.wallets = append(res.wallets, *wallet)
		}
		res.removedTxs = append(res.removedTxs, id)
	}
	for _, id := range p.MyDebtIDs {
		if err := s.dropEntryTx(ctx, tx, userID, id, noop, res); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE notifications SET actioned_at = NOW() WHERE id = $1`, nid); err != nil {
		return nil, fmt.Errorf("action notice: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.applyResult(ctx, userID, ptID, res)
}

// DisconnectBill — POST /event-bills/{pt_id}/disconnect: my copy + debts become plain; on a split
// bill the other members hear about it (info).
func (s *Service) DisconnectBill(ctx context.Context, userID, ptID uuid.UUID) (*EventBillApplyResult, error) {
	pt, err := s.billOf(ctx, ptID)
	if err != nil {
		return nil, err
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	opted, err := optedOutTx(ctx, tx, pt.ID, userID)
	if err != nil {
		return nil, err
	}
	if opted {
		return nil, ErrBillDisconnected
	}
	st, err := s.loadBillTx(ctx, tx, pt.ID)
	if err != nil {
		return nil, err
	}
	copyID, err := myLinkedTxTx(ctx, tx, userID, pt.ID)
	if err != nil {
		return nil, err
	}
	m, member, err := activeMemberTx(ctx, tx, pt.ProjectID, userID)
	if err != nil {
		return nil, err
	}
	if role, _ := st.role(m); copyID == nil && (!member || role == roleNone) {
		return nil, ErrPTNotFound
	}
	res, err := s.disconnectCopyTx(ctx, tx, pt.ID, userID)
	if err != nil {
		return nil, err
	}
	if copyID != nil {
		res.txs = append(res.txs, *copyID)
	}
	if st.kind == KindSplit && s.notifs != nil {
		users, err := collectIDs(ctx, tx, `SELECT DISTINCT user_id FROM project_members
			WHERE project_id = $1 AND user_id IS NOT NULL AND user_id <> $2 AND status = 'active'`, pt.ProjectID, userID)
		if err != nil {
			return nil, err
		}
		for _, u := range users {
			_, name, err := personOfTx(ctx, tx, u, m)
			if err != nil {
				return nil, err
			}
			if err := s.notifs.DispatchSharedBillDisconnected(ctx, tx, u, userID, notifications.SharedBillDisconnectedPayload{
				ProjectTransactionID: pt.ID, ProjectKind: st.kind, Description: pt.Description, Amount: pt.Amount, PersonName: name,
			}); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.applyResult(ctx, userID, pt.ID, res)
}

// DeleteBill — DELETE /event-bills/{pt_id}: a split bill is gone for everyone (owner Q-B5: my
// records go, unless keepMine; the others' stay plain, notice info); an event bill follows each
// member's switch (V13, BE-2's deleteEventBillTx).
func (s *Service) DeleteBill(ctx context.Context, userID, ptID uuid.UUID, keepMine bool) (*EventBillDeleteResult, error) {
	pt, err := s.billOf(ctx, ptID)
	if err != nil {
		return nil, err
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := getPT(ctx, tx, pt.ID, true); err != nil {
		return nil, err
	}
	st, err := s.loadBillTx(ctx, tx, pt.ID)
	if err != nil {
		return nil, err
	}
	var res *copyResult
	removedProject := false
	if st.kind == KindEvent {
		if err := s.store.AssertWritable(ctx, pt.ProjectID, "edit_pt"); err != nil {
			return nil, err
		}
		if _, err := s.store.AssertCanWrite(ctx, pt.ProjectID, userID); err != nil {
			return nil, err
		}
		if res, err = s.deleteEventBillTx(ctx, tx, userID, pt, keepMine); err != nil {
			return nil, err
		}
	} else {
		if _, ok, err := activeMemberTx(ctx, tx, pt.ProjectID, userID); err != nil {
			return nil, err
		} else if !ok {
			return nil, ErrNotMember
		}
		if res, err = s.deleteSplitBillTx(ctx, tx, userID, st, keepMine); err != nil {
			return nil, err
		}
		var left int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM project_transactions WHERE project_id = $1`, pt.ProjectID).Scan(&left); err != nil {
			return nil, fmt.Errorf("bills left: %w", err)
		}
		if left == 0 {
			if _, err := tx.Exec(ctx, `DELETE FROM projects WHERE id = $1 AND kind = 'split'`, pt.ProjectID); err != nil {
				return nil, fmt.Errorf("delete split: %w", err)
			}
			removedProject = true
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	out := &EventBillDeleteResult{ID: pt.ID, ProjectID: pt.ProjectID, RemovedProject: removedProject}
	if out.Effects, err = s.copyEffects(ctx, userID, res); err != nil {
		return nil, err
	}
	out.Effects.RemovedBillIDs = append(out.Effects.RemovedBillIDs, pt.ID)
	if st.kind == KindEvent {
		if out.Event, err = s.patch(ctx, userID, pt.ProjectID, nil, &pt.ID, uniqueIDs(res.wallets)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// deleteSplitBillTx — S13 / Q-B5: the split bill goes; my records go (keepMine: they stay plain);
// every other member's records stay plain and they hear shared_bill_deleted (info).
func (s *Service) deleteSplitBillTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, st *billState, keepMine bool) (*copyResult, error) {
	pt := st.pt
	users, err := collectIDs(ctx, tx, `SELECT DISTINCT user_id FROM project_members
		WHERE project_id = $1 AND user_id IS NOT NULL AND status = 'active'`, pt.ProjectID)
	if err != nil {
		return nil, err
	}
	mine := &copyResult{}
	noop := &copyOptions{beforeKey: func(string) error { return nil }, beforeWallet: func(uuid.UUID) error { return nil }}
	for _, u := range users {
		copyID, err := myLinkedTxTx(ctx, tx, u, pt.ID)
		if err != nil {
			return nil, err
		}
		if u == userID && !keepMine {
			if err := s.dropCopyTx(ctx, tx, st, u, copyID, noop, mine); err != nil {
				return nil, err
			}
			continue
		}
		txIDs, debtIDs, err := plainBillRecordsTx(ctx, tx, u, pt.ID, copyID)
		if err != nil {
			return nil, err
		}
		if u == userID {
			mine.txs, mine.debts = append(mine.txs, txIDs...), append(mine.debts, debtIDs...)
			continue
		}
		if s.notifs == nil || (len(txIDs) == 0 && len(debtIDs) == 0) {
			continue
		}
		if err := s.notifs.SupersedeBillNoticesTx(ctx, tx, u, pt.ID); err != nil {
			return nil, err
		}
		if err := s.notifs.DispatchSharedBillDeleted(ctx, tx, u, userID, notifications.SharedBillDeletedPayload{
			ProjectTransactionID: pt.ID, ProjectKind: KindSplit, Description: pt.Description, Amount: pt.Amount,
			MyTransactionIDs: nonNilIDs(txIDs), MyDebtIDs: nonNilIDs(debtIDs), Applied: true,
		}); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM project_transactions WHERE paid_back_for_id = $1`, pt.ID); err != nil {
		return nil, fmt.Errorf("delete paybacks of bill: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM project_transactions WHERE id = $1`, pt.ID); err != nil {
		return nil, fmt.Errorf("delete bill: %w", err)
	}
	return mine, nil
}
