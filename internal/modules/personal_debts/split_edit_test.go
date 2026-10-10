package personal_debts

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

type splitEditFixture struct {
	pool           *pgxpool.Pool
	txs            *transactions.Service
	debts          *Service
	notifs         *notifications.Service
	me, partner    uuid.UUID
	partnerContact uuid.UUID // my contact for the partner (linked)
	billID         uuid.UUID
	ctx            context.Context
}

func newSplitEditFixture(t *testing.T) *splitEditFixture {
	t.Helper()
	pool := testdb.Pool(t)
	ctx := context.Background()
	f := &splitEditFixture{pool: pool, ctx: ctx}
	f.me = testdb.User(t, pool)
	f.partner = testdb.User(t, pool)
	cats := categories.NewService(categories.NewStore(pool))
	f.txs = transactions.NewService(transactions.NewStore(pool), cats)
	f.debts = NewService(NewStore(pool), f.txs)
	f.notifs = notifications.NewService(notifications.NewStore(pool))
	f.debts.WithNotifications(f.notifs)
	f.txs.WithDebtsCreator(f.debts.CreateForTransactionTx)
	f.txs.WithSplitsEditor(f.debts.EditForTransactionTx)
	f.txs.WithDebtValidator(f.debts.ValidateOwnership)
	f.txs.WithDebtAutoBumper(f.debts.AutoBumpInTx)

	seed, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, u := range []uuid.UUID{f.me, f.partner} {
		if err := cats.SeedForUser(ctx, seed, u); err != nil {
			t.Fatalf("seed categories: %v", err)
		}
		if err := f.notifs.SeedSettings(ctx, seed, u); err != nil {
			t.Fatalf("seed settings: %v", err)
		}
	}
	f.partnerContact = uuid.New()
	if _, err := seed.Exec(ctx, `INSERT INTO contacts (id, user_id, display_name, linked_user_id, status)
		VALUES ($1, $2, 'Partner', $3, 'active'), ($4, $3, 'Me', $2, 'active')`,
		f.partnerContact, f.me, f.partner, uuid.New()); err != nil {
		t.Fatalf("contacts: %v", err)
	}
	if err := seed.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM notifications WHERE recipient_user_id = ANY($1) OR actor_user_id = ANY($1)`,
			[]uuid.UUID{f.me, f.partner})
		_, _ = pool.Exec(ctx, `DELETE FROM personal_debts WHERE user_id = ANY($1)`, []uuid.UUID{f.me, f.partner})
		_, _ = pool.Exec(ctx, `DELETE FROM contacts WHERE user_id = ANY($1)`, []uuid.UUID{f.me, f.partner})
	})

	wallet := testdb.Account(t, pool, f.me)
	created, err := f.txs.Create(ctx, f.me, transactions.CreateRequest{
		Type: transactions.TypeExpense, AccountID: &wallet, Amount: 300, Date: "2026-10-10",
		Description: testdb.Str("Dinner"),
		Splits: []transactions.SplitInput{
			{PersonName: "Beam", OwedAmount: 100},
			{PersonName: "Partner", ContactID: &f.partnerContact, OwedAmount: 100},
		},
	})
	if err != nil {
		t.Fatalf("create bill: %v", err)
	}
	f.billID = created.(*transactions.TransactionDetail).ID
	return f
}

// splits returns my split debts on the bill by person name.
func (f *splitEditFixture) splits(t *testing.T) map[string]transactions.SplitRef {
	t.Helper()
	d, err := f.txs.Get(f.ctx, f.me, f.billID)
	if err != nil {
		t.Fatalf("get bill: %v", err)
	}
	out := map[string]transactions.SplitRef{}
	for _, s := range *d.Splits {
		out[s.PersonName] = s
	}
	return out
}

func (f *splitEditFixture) edit(t *testing.T, edits ...transactions.SplitEdit) error {
	t.Helper()
	_, err := f.txs.EditSplits(f.ctx, f.me, f.billID, edits)
	return err
}

func keep(s transactions.SplitRef, amount float64) transactions.SplitEdit {
	id := s.DebtID
	return transactions.SplitEdit{DebtID: &id, OwedAmount: amount}
}

// changes returns the partner's split_changed notifications, oldest first.
func (f *splitEditFixture) changes(t *testing.T) []notifications.Notification {
	t.Helper()
	rows, err := f.pool.Query(f.ctx, `SELECT id, payload, actioned_at IS NOT NULL FROM notifications
		WHERE recipient_user_id = $1 AND type = 'split_changed' ORDER BY created_at, id`, f.partner)
	if err != nil {
		t.Fatalf("query notifications: %v", err)
	}
	defer rows.Close()
	var out []notifications.Notification
	for rows.Next() {
		var (
			n        notifications.Notification
			actioned bool
		)
		if err := rows.Scan(&n.ID, &n.Payload, &actioned); err != nil {
			t.Fatal(err)
		}
		if actioned {
			n.ActionedAt = &n.CreatedAt
		}
		out = append(out, n)
	}
	return out
}

func (f *splitEditFixture) mirror(t *testing.T) *PersonalDebt {
	t.Helper()
	d, err := scanDebt(f.pool.QueryRow(f.ctx, `SELECT `+debtColumns+` FROM personal_debts
		WHERE user_id = $1 ORDER BY created_at DESC LIMIT 1`, f.partner))
	if err != nil {
		return nil
	}
	return d
}

// TestEditSplits — add / re-amount / remove, the guard rails, and the
// partner's one-shot "อัปเดตตาม" (owner 2026-10-10).
func TestEditSplits(t *testing.T) {
	f := newSplitEditFixture(t)
	s := f.splits(t)
	if m := f.mirror(t); m == nil || m.Amount != 100 {
		t.Fatalf("partner mirror after create = %+v, want 100 (split_created auto)", m)
	}

	// Re-amount the partner, drop Beam, add Cee.
	if err := f.edit(t, keep(s["Partner"], 120),
		transactions.SplitEdit{PersonName: "Cee", OwedAmount: 50}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	s = f.splits(t)
	if len(s) != 2 || s["Partner"].Amount != 120 || s["Cee"].Amount != 50 {
		t.Fatalf("after edit: %+v", s)
	}
	ch := f.changes(t)
	if len(ch) != 1 {
		t.Fatalf("partner got %d split_changed, want 1 (Beam/Cee aren't linked)", len(ch))
	}
	if m := f.mirror(t); m.Amount != 100 {
		t.Errorf("mirror changed without the partner's tap: %v", m.Amount)
	}

	// A newer change supersedes the older notice.
	if err := f.edit(t, keep(s["Partner"], 130), keep(s["Cee"], 50)); err != nil {
		t.Fatalf("edit 2: %v", err)
	}
	ch = f.changes(t)
	if len(ch) != 2 {
		t.Fatalf("want 2 split_changed, got %d", len(ch))
	}
	if _, err := f.debts.ApplySplitChange(f.ctx, f.partner, ch[0].ID); !errors.Is(err, ErrSplitChangeStale) {
		t.Errorf("apply superseded: err = %v, want stale", err)
	}
	res, err := f.debts.ApplySplitChange(f.ctx, f.partner, ch[1].ID)
	if err != nil || res.Result != "updated" || res.Debt.Amount != 130 {
		t.Fatalf("apply newest: %+v, %v", res, err)
	}
	if _, err := f.debts.ApplySplitChange(f.ctx, f.partner, ch[1].ID); !errors.Is(err, ErrNotificationActioned) {
		t.Errorf("apply twice: err = %v, want actioned", err)
	}
	if _, err := f.debts.ApplySplitChange(f.ctx, f.me, ch[1].ID); !errors.Is(err, notifications.ErrNotForYou) {
		t.Errorf("apply someone else's: err = %v", err)
	}

	// Guard rails.
	if err := f.edit(t, keep(s["Partner"], 200), keep(s["Cee"], 150)); !errors.Is(err, transactions.ErrSplitsExceedAmount) {
		t.Errorf("over the bill: err = %v", err)
	}
	if _, err := f.debts.Settle(f.ctx, f.me, s["Cee"].DebtID, SettleRequest{Amount: testdb.Float(20)}); err != nil {
		t.Fatalf("settle Cee: %v", err)
	}
	// Repayments don't constrain edits: 10 < 20 repaid → overpaid by 10.
	if err := f.edit(t, keep(s["Partner"], 130), keep(s["Cee"], 10)); err != nil {
		t.Fatalf("below repaid: %v", err)
	}
	if c := f.splits(t)["Cee"]; c.Amount != 10 || c.SettledAmount != 20 || c.Status != StatusOpen {
		t.Errorf("overpaid Cee = %+v, want 10 / 20 open", c)
	}
	other := uuid.New()
	if err := f.edit(t, transactions.SplitEdit{DebtID: &other, OwedAmount: 10}); !errors.Is(err, transactions.ErrSplitUnknownDebt) {
		t.Errorf("foreign debt id: err = %v", err)
	}
	// A pending change whose split was deleted since → stale.
	if err := f.edit(t, keep(s["Partner"], 140), keep(s["Cee"], 50)); err != nil {
		t.Fatalf("edit 3: %v", err)
	}
	ch = f.changes(t)
	pending := ch[len(ch)-1]
	if err := f.debts.Delete(f.ctx, f.me, s["Partner"].DebtID); err != nil {
		t.Fatalf("delete split debt: %v", err)
	}
	if _, err := f.debts.ApplySplitChange(f.ctx, f.partner, pending.ID); !errors.Is(err, ErrSplitChangeStale) {
		t.Errorf("apply after the split was deleted: err = %v, want stale", err)
	}
}

// TestEditSplitsRemovePartner — removing a linked person sends "removed";
// applying deletes the partner's unpaid mirror.
func TestEditSplitsRemovePartner(t *testing.T) {
	f := newSplitEditFixture(t)
	s := f.splits(t)
	if err := f.edit(t, keep(s["Beam"], 100)); err != nil {
		t.Fatalf("remove partner: %v", err)
	}
	ch := f.changes(t)
	if len(ch) != 1 {
		t.Fatalf("want 1 split_changed, got %d", len(ch))
	}
	var p notifications.SplitChangedPayload
	_ = json.Unmarshal(ch[0].Payload, &p)
	if p.Change != notifications.SplitChangeRemoved || p.OldAmount != 100 || p.Description == nil || *p.Description != "Dinner" {
		t.Errorf("payload = %+v", p)
	}
	res, err := f.debts.ApplySplitChange(f.ctx, f.partner, ch[0].ID)
	if err != nil || res.Result != "deleted" {
		t.Fatalf("apply removed: %+v, %v", res, err)
	}
	if m := f.mirror(t); m != nil {
		t.Errorf("partner mirror still there: %+v", m)
	}
}

// TestEditSplitsBeforePartnerAdded — the partner hasn't added the split
// (auto off): their pending split_created follows the amount, and a
// removal supersedes it. No split_changed is sent.
func TestEditSplitsBeforePartnerAdded(t *testing.T) {
	f := newSplitEditFixture(t)
	// Partner turns "add to my debts" auto off, then I add them again.
	if _, err := f.pool.Exec(f.ctx, `UPDATE user_notification_settings SET auto_types = '{}' WHERE user_id = $1`, f.partner); err != nil {
		t.Fatal(err)
	}
	s := f.splits(t)
	if err := f.edit(t, keep(s["Beam"], 100),
		transactions.SplitEdit{PersonName: "Partner", ContactID: &f.partnerContact, OwedAmount: 60}); err != nil {
		t.Fatalf("re-add partner: %v", err)
	}
	s = f.splits(t)
	var nID uuid.UUID
	var payload []byte
	if err := f.pool.QueryRow(f.ctx, `SELECT id, payload FROM notifications
		WHERE recipient_user_id = $1 AND type = 'split_created' AND actioned_at IS NULL`, f.partner).Scan(&nID, &payload); err != nil {
		t.Fatalf("pending split_created: %v", err)
	}
	if err := f.edit(t, keep(s["Beam"], 100), keep(s["Partner"], 80)); err != nil {
		t.Fatalf("re-amount: %v", err)
	}
	var p notifications.SplitCreatedPayload
	_ = f.pool.QueryRow(f.ctx, `SELECT payload FROM notifications WHERE id = $1`, nID).Scan(&payload)
	_ = json.Unmarshal(payload, &p)
	if p.Amount != 80 {
		t.Errorf("pending split_created amount = %v, want 80", p.Amount)
	}
	if err := f.edit(t, keep(s["Beam"], 100)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := f.debts.AcceptSplitRequest(f.ctx, f.partner, nID); !errors.Is(err, ErrSplitChangeStale) {
		t.Errorf("accept after removal: err = %v, want stale", err)
	}
	// The first (auto-added) mirror's change notices exist from removing
	// the original partner split; no notice for the never-added one.
	for _, n := range f.changes(t) {
		var cp notifications.SplitChangedPayload
		_ = json.Unmarshal(n.Payload, &cp)
		if cp.SplitID == s["Partner"].DebtID {
			t.Errorf("split_changed sent for a split the partner never added")
		}
	}
}

// TestRepaymentLifecycle — removing a person who repaid keeps the row at
// 0 (overpaid, owed the other way); deleting a repayment transaction takes
// what it repaid off the debt. (Editing / unlinking repayments waits for
// the debt-repayment round: still read-only.)
func TestRepaymentLifecycle(t *testing.T) {
	f := newSplitEditFixture(t)
	f.txs.WithDebtSettledAdjuster(f.debts.AdjustSettledInTx)
	s := f.splits(t)
	beam := s["Beam"].DebtID
	res, err := f.debts.Settle(f.ctx, f.me, beam, SettleRequest{Amount: testdb.Float(50)})
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	repay := res.Transaction.(*transactions.TransactionDetail)
	debt := func() *PersonalDebt {
		d, err := f.debts.store.GetByID(f.ctx, f.me, beam)
		if err != nil {
			t.Fatalf("get debt: %v", err)
		}
		return d
	}

	// Remove Beam → kept at 0, overpaid 50 → People counts it as I owe Beam 50.
	if err := f.edit(t, keep(s["Partner"], 100)); err != nil {
		t.Fatalf("remove repaid person: %v", err)
	}
	if d := debt(); d.Amount != 0 || d.SettledAmount != 50 || d.Status != StatusOpen {
		t.Fatalf("removed Beam = %+v, want 0 / 50 open", d)
	}
	people, err := f.debts.People(f.ctx, f.me)
	if err != nil {
		t.Fatal(err)
	}
	if people.TotalIOwe != 50 {
		t.Errorf("total I owe = %v, want 50 (Beam overpaid)", people.TotalIOwe)
	}
	if _, err := f.debts.Settle(f.ctx, f.me, beam, SettleRequest{}); !errors.Is(err, ErrDebtOverpaid) {
		t.Errorf("settle an overpaid debt: err = %v", err)
	}

	// Repayment rows stay read-only for edits (for now).
	amt := 30.0
	if _, err := f.txs.Update(f.ctx, f.me, repay.ID, transactions.UpdateRequest{Amount: &amt}); !errors.Is(err, transactions.ErrSystemTransactionImmutable) {
		t.Errorf("edit a repayment: err = %v, want immutable", err)
	}

	// Deleting Beam's repayment → repaid 0 → 0 / 0 = settled (nothing owed).
	if err := f.txs.Delete(f.ctx, f.me, repay.ID); err != nil {
		t.Fatalf("delete Beam's repayment: %v", err)
	}
	if d := debt(); d.SettledAmount != 0 || d.Status != StatusSettled {
		t.Errorf("after deleting the repayment: %+v, want 0 repaid, settled", d)
	}

	// Delete a repayment on an open debt → repaid drops, back to open.
	partner := s["Partner"].DebtID
	r2, err := f.debts.Settle(f.ctx, f.me, partner, SettleRequest{Amount: testdb.Float(40)})
	if err != nil {
		t.Fatalf("settle partner: %v", err)
	}
	if err := f.txs.Delete(f.ctx, f.me, r2.Transaction.(*transactions.TransactionDetail).ID); err != nil {
		t.Fatalf("delete repayment: %v", err)
	}
	d, _ := f.debts.store.GetByID(f.ctx, f.me, partner)
	if d.SettledAmount != 0 || d.Status != StatusOpen {
		t.Errorf("after deleting the repayment: %+v, want 0 repaid, open", d)
	}
}

// TestEditSplitsIdentity — a saved split without a contact can be renamed
// or linked to a contact in place, keeping its repayments; a split with a
// contact can't change its person (owner 2026-10-10).
func TestEditSplitsIdentity(t *testing.T) {
	f := newSplitEditFixture(t)
	s := f.splits(t)
	beamID := s["Beam"].DebtID
	if _, err := f.debts.Settle(f.ctx, f.me, beamID, SettleRequest{Amount: testdb.Float(50)}); err != nil {
		t.Fatalf("settle: %v", err)
	}
	ident := func(r transactions.SplitRef, name string, contact *uuid.UUID) transactions.SplitEdit {
		e := keep(r, r.Amount)
		e.PersonName, e.ContactID = name, contact
		return e
	}
	countNotifs := func(user uuid.UUID, typ string) int {
		t.Helper()
		var n int
		if err := f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM notifications
			WHERE recipient_user_id = $1 AND type = $2`, user, typ).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	partnerNotifs := countNotifs(f.partner, "split_created") + countNotifs(f.partner, "split_changed")

	// Rename: same row, repayment kept, nobody told.
	if err := f.edit(t, ident(s["Beam"], "Bam", nil), keep(s["Partner"], 100)); err != nil {
		t.Fatalf("rename: %v", err)
	}
	s = f.splits(t)
	if r, ok := s["Bam"]; !ok || r.DebtID != beamID || r.SettledAmount != 50 || r.ContactID != nil {
		t.Fatalf("renamed split = %+v, want same debt, 50 repaid, no contact", s["Bam"])
	}
	if n := countNotifs(f.partner, "split_created") + countNotifs(f.partner, "split_changed"); n != partnerNotifs {
		t.Errorf("rename notified the partner: %d -> %d", partnerNotifs, n)
	}

	// Link to an archived contact → refused, nothing changes.
	archived := uuid.New()
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO contacts (id, user_id, display_name, status)
		VALUES ($1, $2, 'Old', 'archived')`, archived, f.me); err != nil {
		t.Fatal(err)
	}
	if err := f.edit(t, ident(s["Bam"], "", &archived), keep(s["Partner"], 100)); !errors.Is(err, transactions.ErrSplitContactArchived) {
		t.Errorf("link to archived contact: err = %v", err)
	}

	// Link to a contact with an app account: name from the contact, same
	// row, repayment kept, split_created to them (auto → mirror at 100).
	bee := testdb.User(t, f.pool)
	seed, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.notifs.SeedSettings(f.ctx, seed, bee); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	if err := seed.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(f.ctx, `DELETE FROM notifications WHERE recipient_user_id = $1`, bee)
		_, _ = f.pool.Exec(f.ctx, `DELETE FROM personal_debts WHERE user_id = $1`, bee)
		_, _ = f.pool.Exec(f.ctx, `UPDATE personal_debts SET counterparty_contact_id = NULL
			WHERE counterparty_contact_id IN (SELECT id FROM contacts WHERE linked_user_id = $1)`, bee)
		_, _ = f.pool.Exec(f.ctx, `DELETE FROM contacts WHERE linked_user_id = $1`, bee)
	})
	beeContact := uuid.New()
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO contacts (id, user_id, display_name, linked_user_id, status)
		VALUES ($1, $2, 'Bee', $3, 'active')`, beeContact, f.me, bee); err != nil {
		t.Fatal(err)
	}
	if err := f.edit(t, ident(s["Bam"], "", &beeContact), keep(s["Partner"], 100)); err != nil {
		t.Fatalf("link: %v", err)
	}
	s = f.splits(t)
	r, ok := s["Bee"]
	if !ok || r.DebtID != beamID || r.SettledAmount != 50 || r.ContactID == nil || *r.ContactID != beeContact {
		t.Fatalf("linked split = %+v, want same debt, 50 repaid, Bee's contact", r)
	}
	if n := countNotifs(bee, "split_created"); n != 1 {
		t.Errorf("split_created to Bee = %d, want 1", n)
	}
	var mirrorAmount float64
	if err := f.pool.QueryRow(f.ctx, `SELECT amount FROM personal_debts WHERE user_id = $1 AND direction = 'i_owe'`,
		bee).Scan(&mirrorAmount); err != nil || mirrorAmount != 100 {
		t.Errorf("Bee's mirror amount = %v (err %v), want 100", mirrorAmount, err)
	}

	// A split with a contact is fixed: rename or swap → 422; the same
	// name / contact sent back is no change.
	if err := f.edit(t, ident(s["Bee"], "Beatrice", nil), keep(s["Partner"], 100)); !errors.Is(err, transactions.ErrSplitIdentityLocked) {
		t.Errorf("rename a linked split: err = %v", err)
	}
	if err := f.edit(t, keep(s["Bee"], 100), ident(s["Partner"], "", &beeContact)); !errors.Is(err, transactions.ErrSplitIdentityLocked) {
		t.Errorf("swap a linked split's contact: err = %v", err)
	}
	if err := f.edit(t, ident(s["Bee"], "Bee", &beeContact), keep(s["Partner"], 100)); err != nil {
		t.Errorf("unchanged identity sent back: %v", err)
	}
}

// TestEditSplitsOnRepayment — a repayment's splits can't be edited, even
// when its system category is gone.
func TestEditSplitsOnRepayment(t *testing.T) {
	f := newSplitEditFixture(t)
	res, err := f.debts.Settle(f.ctx, f.me, f.splits(t)["Beam"].DebtID, SettleRequest{Amount: testdb.Float(20)})
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	repay := res.Transaction.(*transactions.TransactionDetail).ID
	if _, err := f.pool.Exec(f.ctx, `UPDATE transactions SET category_id = NULL WHERE id = $1`, repay); err != nil {
		t.Fatal(err)
	}
	_, err = f.txs.EditSplits(f.ctx, f.me, repay, []transactions.SplitEdit{{PersonName: "X", OwedAmount: 5}})
	if !errors.Is(err, transactions.ErrSystemTransactionImmutable) {
		t.Errorf("edit splits on a repayment: err = %v", err)
	}
}
