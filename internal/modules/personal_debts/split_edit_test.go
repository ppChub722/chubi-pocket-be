package personal_debts

import (
	"context"
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
	f.txs.WithDebtLinks(f.debts)

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
	if _, err := seed.Exec(ctx, `UPDATE users SET display_name = 'Partner' WHERE id = $1`, f.partner); err != nil {
		t.Fatalf("partner name: %v", err)
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

// partnerBook — what the partner's own book holds (0.4.1-2 S16: a split with a linked contact makes
// no copy there): their debt rows and split notices.
func (f *splitEditFixture) partnerBook(t *testing.T, user uuid.UUID) (debts, notices int) {
	t.Helper()
	if err := f.pool.QueryRow(f.ctx, `SELECT
			(SELECT COUNT(*) FROM personal_debts WHERE user_id = $1),
			(SELECT COUNT(*) FROM notifications WHERE recipient_user_id = $1 AND type IN ('split_created', 'split_changed'))`,
		user).Scan(&debts, &notices); err != nil {
		t.Fatalf("partner book: %v", err)
	}
	return debts, notices
}

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

func TestEditSplits(t *testing.T) {
	f := newSplitEditFixture(t)
	s := f.splits(t)
	partnerID := s["Partner"].DebtID

	if err := f.edit(t, keep(s["Partner"], 120),
		transactions.SplitEdit{PersonName: "Cee", OwedAmount: 50}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	s = f.splits(t)
	if len(s) != 2 || s["Partner"].Amount != 120 || s["Cee"].Amount != 50 || s["Partner"].DebtID != partnerID {
		t.Fatalf("after edit: %+v", s)
	}

	if err := f.edit(t, keep(s["Partner"], 130), keep(s["Cee"], 50)); err != nil {
		t.Fatalf("edit 2: %v", err)
	}
	if p := f.splits(t)["Partner"]; p.Amount != 130 || p.DebtID != partnerID {
		t.Errorf("Partner after edit 2 = %+v, want 130 on the same debt", p)
	}
	if debts, notices := f.partnerBook(t, f.partner); debts != 0 || notices != 0 {
		t.Errorf("partner's book = %d debts / %d split notices, want none (no mirror)", debts, notices)
	}

	if err := f.edit(t, keep(s["Partner"], 200), keep(s["Cee"], 150)); !errors.Is(err, transactions.ErrSplitsExceedAmount) {
		t.Errorf("over the bill: err = %v", err)
	}
	// The paid label doesn't constrain edits (it never changes numbers).
	if _, err := f.pool.Exec(f.ctx, `UPDATE personal_debts SET paid = TRUE WHERE id = $1`, s["Cee"].DebtID); err != nil {
		t.Fatal(err)
	}
	if err := f.edit(t, keep(s["Partner"], 130), keep(s["Cee"], 10)); err != nil {
		t.Fatalf("edit a paid split: %v", err)
	}
	if c := f.splits(t)["Cee"]; c.Amount != 10 || !c.Paid || c.Direction != DirectionPaid || c.Key != "n:cee" {
		t.Errorf("Cee = %+v, want 10, still labelled paid", c)
	}
	other := uuid.New()
	if err := f.edit(t, transactions.SplitEdit{DebtID: &other, OwedAmount: 10}); !errors.Is(err, transactions.ErrSplitUnknownDebt) {
		t.Errorf("foreign debt id: err = %v", err)
	}
	// Removing the linked split deletes my debt row only.
	if err := f.edit(t, keep(s["Cee"], 50)); err != nil {
		t.Fatalf("remove partner: %v", err)
	}
	if _, ok := f.splits(t)["Partner"]; ok {
		t.Errorf("Partner's split still on the bill")
	}
	var left int
	if err := f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM personal_debts WHERE id = $1`, partnerID).Scan(&left); err != nil || left != 0 {
		t.Errorf("removed split's debt left = %d (%v)", left, err)
	}
	if debts, notices := f.partnerBook(t, f.partner); debts != 0 || notices != 0 {
		t.Errorf("partner's book after the removal = %d debts / %d split notices, want none", debts, notices)
	}
}

func TestEditSplitsIdentity(t *testing.T) {
	f := newSplitEditFixture(t)
	s := f.splits(t)
	beamID := s["Beam"].DebtID
	if _, err := f.pool.Exec(f.ctx, `UPDATE personal_debts SET paid = TRUE WHERE id = $1`, beamID); err != nil {
		t.Fatal(err)
	}
	ident := func(r transactions.SplitRef, name string, contact *uuid.UUID) transactions.SplitEdit {
		e := keep(r, r.Amount)
		e.PersonName, e.ContactID = name, contact
		return e
	}

	if err := f.edit(t, ident(s["Beam"], "Bam", nil), keep(s["Partner"], 100)); err != nil {
		t.Fatalf("rename: %v", err)
	}
	s = f.splits(t)
	if r, ok := s["Bam"]; !ok || r.DebtID != beamID || !r.Paid || r.ContactID != nil {
		t.Fatalf("renamed split = %+v, want same debt, still paid, no contact", s["Bam"])
	}

	bee := testdb.User(t, f.pool)
	seed, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(f.ctx, `UPDATE users SET display_name = 'Bee' WHERE id = $1`, bee); err != nil {
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
	if !ok || r.DebtID != beamID || !r.Paid || r.ContactID == nil || *r.ContactID != beeContact {
		t.Fatalf("linked split = %+v, want same debt, still paid, Bee's contact", r)
	}
	if debts, notices := f.partnerBook(t, bee); debts != 0 || notices != 0 {
		t.Errorf("Bee's book after the link = %d debts / %d split notices, want none (no mirror)", debts, notices)
	}

	if err := f.edit(t, ident(s["Bee"], "Beatrice", nil), keep(s["Partner"], 100)); err != nil {
		t.Fatalf("rename a contact split: %v", err)
	}
	s = f.splits(t)
	if _, ok := s["Beatrice"]; ok {
		t.Fatalf("a contact split must not be renamed in place: %+v", s)
	}
	if r, ok := s["Bee"]; !ok || r.DebtID != beamID || r.ContactID == nil || *r.ContactID != beeContact {
		t.Fatalf("contact split after a name-only edit = %+v, want unchanged (Bee, same debt and contact)", s["Bee"])
	}
	if err := f.edit(t, ident(s["Bee"], "Bee", &beeContact), keep(s["Partner"], 100)); err != nil {
		t.Errorf("unchanged identity sent back: %v", err)
	}

	// 0.4.1-2 S16: a linked split's contact change relinks in place (same debt id, no remove + add).
	partnerID := s["Partner"].DebtID
	partnerRow := keep(s["Partner"], 100)
	partnerRow.PersonName, partnerRow.ContactIDSet = "Cousin", true
	if err := f.edit(t, partnerRow, keep(s["Bee"], 100)); err != nil {
		t.Fatalf("change a linked split to a free name: %v", err)
	}
	s = f.splits(t)
	if _, ok := s["Partner"]; ok {
		t.Errorf("the split should no longer show Partner: %+v", s)
	}
	cousin, ok := s["Cousin"]
	if !ok || cousin.Amount != 100 || cousin.ContactID != nil || cousin.DebtID != partnerID {
		t.Fatalf("Cousin split = %+v, want the same debt (%s) as a free-name row of 100", cousin, partnerID)
	}

	beeRow := keep(s["Bee"], 100)
	beeRow.ContactID, beeRow.ContactIDSet = &f.partnerContact, true
	if err := f.edit(t, beeRow, keep(s["Cousin"], 100)); err != nil {
		t.Fatalf("change a linked split to another linked contact: %v", err)
	}
	s = f.splits(t)
	if _, ok := s["Bee"]; ok {
		t.Errorf("the split should no longer show Bee: %+v", s)
	}
	np, ok := s["Partner"]
	if !ok || np.DebtID != beamID || np.Amount != 100 || !np.Paid || np.ContactID == nil || *np.ContactID != f.partnerContact {
		t.Fatalf("Partner split = %+v, want the same debt (%s), 100, still labelled paid", np, beamID)
	}
	for _, u := range []uuid.UUID{f.partner, bee} {
		if debts, notices := f.partnerBook(t, u); debts != 0 || notices != 0 {
			t.Errorf("%s's book = %d debts / %d split notices, want none", u, debts, notices)
		}
	}
}

// TestEditSplitsOnDebtTx — a debt entry's wallet transaction never carries splits, even when its
// system category is gone.
func TestEditSplitsOnDebtTx(t *testing.T) {
	f := newSplitEditFixture(t)
	wallet := testdb.Account(t, f.pool, f.me)
	res, err := f.debts.CreateEntry(f.ctx, f.me, "n:beam", CreateEntryRequest{
		Direction: DirectionReceived, Amount: 20, AccountID: &wallet,
	})
	if err != nil {
		t.Fatalf("receive from Beam: %v", err)
	}
	repay := res.Transaction.ID
	if _, err := f.pool.Exec(f.ctx, `UPDATE transactions SET category_id = NULL WHERE id = $1`, repay); err != nil {
		t.Fatal(err)
	}
	_, err = f.txs.EditSplits(f.ctx, f.me, repay, []transactions.SplitEdit{{PersonName: "X", OwedAmount: 5}})
	if !errors.Is(err, transactions.ErrSystemTransactionImmutable) {
		t.Errorf("edit splits on a repayment: err = %v", err)
	}
}

func TestEditSplitsIdentityUnlinkedContact(t *testing.T) {
	f := newSplitEditFixture(t)
	s := f.splits(t)
	beamID := s["Beam"].DebtID
	if _, err := f.pool.Exec(f.ctx, `UPDATE personal_debts SET paid = TRUE WHERE id = $1`, beamID); err != nil {
		t.Fatal(err)
	}
	newContact := func(name string, linked *uuid.UUID) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if _, err := f.pool.Exec(f.ctx, `INSERT INTO contacts (id, user_id, display_name, linked_user_id, status)
			VALUES ($1, $2, $3, $4, 'active')`, id, f.me, name, linked); err != nil {
			t.Fatal(err)
		}
		return id
	}
	edit := func(e transactions.SplitEdit) transactions.SplitRef {
		t.Helper()
		if err := f.edit(t, e, keep(f.splits(t)["Partner"], 100)); err != nil {
			t.Fatalf("edit %+v: %v", e, err)
		}
		for _, r := range f.splits(t) {
			if r.DebtID == beamID {
				return r
			}
		}
		t.Fatal("Beam's split is gone")
		return transactions.SplitRef{}
	}
	kept := func(contact *uuid.UUID, set bool, name string) transactions.SplitEdit {
		id := beamID
		return transactions.SplitEdit{DebtID: &id, OwedAmount: 100, PersonName: name, ContactID: contact, ContactIDSet: set}
	}
	check := func(step string, r transactions.SplitRef, name string, contact *uuid.UUID) {
		t.Helper()
		if r.PersonName != name || !sameUUID(r.ContactID, contact) || !r.Paid {
			t.Errorf("%s: %+v, want %s / %v / labelled paid", step, r, name, contact)
		}
	}

	x, y := newContact("Xan", nil), newContact("Yod", nil)
	check("link to Xan (no app account)", edit(kept(&x, true, "")), "Xan", &x)
	check("switch to Yod", edit(kept(&y, true, "")), "Yod", &y)
	check("name-only edit keeping Yod (name ignored)", edit(kept(nil, false, "Yodsawee")), "Yod", &y)
	check("back to a free name", edit(kept(nil, true, "Beam")), "Beam", nil)

	bee := testdb.User(t, f.pool)
	if _, err := f.pool.Exec(f.ctx, `UPDATE users SET display_name = 'Bee' WHERE id = $1`, bee); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(f.ctx, `DELETE FROM notifications WHERE recipient_user_id = $1`, bee)
		_, _ = f.pool.Exec(f.ctx, `DELETE FROM personal_debts WHERE user_id = $1`, bee)
		_, _ = f.pool.Exec(f.ctx, `UPDATE personal_debts SET counterparty_contact_id = NULL
			WHERE counterparty_contact_id IN (SELECT id FROM contacts WHERE linked_user_id = $1)`, bee)
		_, _ = f.pool.Exec(f.ctx, `DELETE FROM contacts WHERE linked_user_id = $1`, bee)
	})
	beeContact := newContact("Bee", &bee)
	check("link to Bee (app user)", edit(kept(&beeContact, true, "")), "Bee", &beeContact)
	check("drop a linked contact (in place)", edit(kept(nil, true, "Beam")), "Beam", nil)
}
