package projects

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

func idp(id uuid.UUID) *uuid.UUID { return &id }

// TestBillPeopleIPaid — quick create tab อีเวนต์, I paid: my tx is the bill on my wallet; Bee (a
// linked contact) and a typed name are added to the event; their shares are the bill's splits.
func TestBillPeopleIPaid(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()
	resp, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{
		Name: "Trip",
		NewTransaction: &transactions.CreateRequest{
			Type: transactions.TypeExpense, AccountID: &f.acctPersonal, Amount: 300, Date: "2026-09-10", CategoryID: &f.cat,
		},
		BillPeople: BillPeople{Members: []BillMember{
			{BillPerson: BillPerson{Me: true}, Share: 100},
			{BillPerson: BillPerson{ContactID: &f.contactB}, Share: 120},
			{BillPerson: BillPerson{PersonName: "Dan"}, Share: 80},
		}},
	})
	if err != nil {
		t.Fatalf("quick create: %v", err)
	}
	if resp.NewTransactionID == nil || f.accountBalance(t, f.acctPersonal) != 700 {
		t.Fatalf("new tx %v, wallet %v (want 700)", resp.NewTransactionID, f.accountBalance(t, f.acctPersonal))
	}
	d, err := f.txs.Get(ctx, f.owner, *resp.NewTransactionID)
	if err != nil {
		t.Fatal(err)
	}
	if d.LinkMode != transactions.LinkEvent || d.EventBill == nil || !d.EventBill.Linked || d.MyShare == nil || *d.MyShare != 100 {
		t.Fatalf("my tx: mode %s event_bill %+v share %v", d.LinkMode, d.EventBill, d.MyShare)
	}
	eb := d.EventBill
	if eb.Payer == nil || !eb.Payer.IsMe || eb.BillAmount != 300 || len(eb.Members) != 3 ||
		!eb.Members[0].Member.IsMe || eb.Members[0].Share != 100 {
		t.Fatalf("event_bill people = payer %+v amount %v members %+v", eb.Payer, eb.BillAmount, eb.Members)
	}
	shares := map[string]float64{}
	for _, m := range eb.Members {
		shares[m.Member.Name] = m.Share
	}
	if shares["Bee"] != 120 || shares["Dan"] != 80 {
		t.Errorf("shares = %v", shares)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM project_members WHERE project_id = $1 AND status <> 'left'`, resp.Detail.Event.ID); n != 3 {
		t.Errorf("members = %d, want 3 (me, Bee linked, Dan typed)", n)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM personal_debts WHERE source_transaction_id = $1`, *resp.NewTransactionID); n != 0 {
		t.Errorf("the board keeps the debts, not personal_debts: %d", n)
	}
}

// TestBillPeopleSomeoneElsePaid — Bee paid 300 for me, Bee and Dan: the board bill is Bee's; my tx is
// my share (100), an expense with no wallet, a copy of the bill; any member may add people.
func TestBillPeopleSomeoneElsePaid(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()
	ev, err := f.svc.QuickCreate(ctx, f.owner, QuickCreateRequest{Name: "Dinner", TransactionIDs: []uuid.UUID{f.bill1}})
	if err != nil {
		t.Fatal(err)
	}
	pid := ev.Detail.Event.ID
	beeMember := uuid.Nil
	if err := f.pool.QueryRow(ctx, `SELECT id FROM project_members WHERE project_id = $1 AND user_id = $2`, pid, f.friend).Scan(&beeMember); err != nil {
		t.Fatalf("Bee should be a member (bill1 split): %v", err)
	}
	add, err := f.svc.AddBills(ctx, f.owner, pid, AddBillsRequest{
		NewTransaction: &transactions.CreateRequest{
			Type: transactions.TypeExpense, AccountID: &f.acctPersonal, Amount: 300, Date: "2026-09-11",
			CategoryID: &f.cat, Description: strp("Hotpot"),
		},
		BillPeople: BillPeople{
			Payer: &BillPerson{MemberID: idp(beeMember)},
			Members: []BillMember{
				{BillPerson: BillPerson{Me: true}, Share: 100},
				{BillPerson: BillPerson{MemberID: idp(beeMember)}, Share: 999}, // the payer's share is the rest
				{BillPerson: BillPerson{PersonName: "Dan"}, Share: 120},
			},
		},
	})
	if err != nil {
		t.Fatalf("add bill paid by Bee: %v", err)
	}
	if add.NewTransactionID == nil || f.accountBalance(t, f.acctPersonal) != 1000 {
		t.Fatalf("my tx %v, wallet %v (want untouched 1000)", add.NewTransactionID, f.accountBalance(t, f.acctPersonal))
	}
	mine, err := f.txs.Get(ctx, f.owner, *add.NewTransactionID)
	if err != nil {
		t.Fatal(err)
	}
	if mine.Amount != 100 || mine.Account != nil || mine.Type != transactions.TypeExpense || mine.LinkMode != transactions.LinkEvent ||
		mine.Category == nil || mine.Category.ID != f.cat || mine.EventBill == nil || mine.EventBill.Linked ||
		mine.EventBill.Payer == nil || mine.EventBill.Payer.Name != "Bee" || mine.EventBill.BillAmount != 300 {
		t.Fatalf("my share tx = %+v / event_bill %+v", mine.Transaction, mine.EventBill)
	}
	shares := map[string]float64{}
	for _, m := range mine.EventBill.Members {
		shares[m.Member.Name] = m.Share
	}
	if shares["Bee"] != 80 || shares["Dan"] != 120 || len(mine.EventBill.Members) != 3 {
		t.Errorf("shares = %v, want Bee 80 (the rest), me 100, Dan 120", shares)
	}
	var payer uuid.UUID
	var linked *uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT transaction_member_id, linked_transaction_id FROM project_transactions WHERE id = $1`,
		mine.EventBill.BillID).Scan(&payer, &linked); err != nil || payer != beeMember || linked != nil {
		t.Errorf("board bill payer %v linked %v (%v), want Bee, no linked tx", payer, linked, err)
	}
	// A linked payer hears about it.
	if n := f.countRows(t, `SELECT COUNT(*) FROM notifications WHERE recipient_user_id = $1 AND type = 'project_tx_recorded_for_you'`, f.friend); n != 1 {
		t.Errorf("recorded-for-you notices to Bee = %d, want 1", n)
	}

	// Rules.
	bad := []struct {
		name string
		req  AddBillsRequest
		want error
	}{
		{"income paid by another", AddBillsRequest{
			NewTransaction: &transactions.CreateRequest{Type: transactions.TypeIncome, Amount: 50, Date: "2026-09-11"},
			BillPeople:     BillPeople{Payer: &BillPerson{MemberID: idp(beeMember)}},
		}, ErrPayerIncome},
		{"over the bill", AddBillsRequest{
			NewTransaction: &transactions.CreateRequest{Type: transactions.TypeExpense, Amount: 50, Date: "2026-09-11"},
			BillPeople:     BillPeople{Members: []BillMember{{BillPerson: BillPerson{PersonName: "Dan"}, Share: 60}}},
		}, ErrSplitsExceedParent},
		{"listed twice", AddBillsRequest{
			NewTransaction: &transactions.CreateRequest{Type: transactions.TypeExpense, Amount: 50, Date: "2026-09-11"},
			BillPeople: BillPeople{Members: []BillMember{
				{BillPerson: BillPerson{PersonName: "dan"}, Share: 10}, {BillPerson: BillPerson{PersonName: "Dan "}, Share: 10}}},
		}, ErrBadBillPeople},
		{"with splits", AddBillsRequest{
			NewTransaction: &transactions.CreateRequest{Type: transactions.TypeExpense, Amount: 50, Date: "2026-09-11",
				Splits: []transactions.SplitInput{{PersonName: "Dan", OwedAmount: 10}}},
			BillPeople: BillPeople{Payer: &BillPerson{Me: true}},
		}, transactions.ErrLinkModeConflict},
		{"no new bill", AddBillsRequest{TransactionIDs: []uuid.UUID{f.bill2}, BillPeople: BillPeople{Payer: &BillPerson{Me: true}}}, ErrBadBillPeople},
		{"foreign member", AddBillsRequest{
			NewTransaction: &transactions.CreateRequest{Type: transactions.TypeExpense, Amount: 50, Date: "2026-09-11"},
			BillPeople:     BillPeople{Payer: &BillPerson{MemberID: idp(uuid.New())}},
		}, ErrMemberNotInProject},
	}
	for _, c := range bad {
		if _, err := f.svc.AddBills(ctx, f.owner, pid, c.req); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}

	// Someone else paid and I'm not in it: no transaction of mine.
	none, err := f.svc.AddBills(ctx, f.owner, pid, AddBillsRequest{
		NewTransaction: &transactions.CreateRequest{Type: transactions.TypeExpense, Amount: 40, Date: "2026-09-12"},
		BillPeople: BillPeople{Payer: &BillPerson{MemberID: idp(beeMember)},
			Members: []BillMember{{BillPerson: BillPerson{PersonName: "Dan"}, Share: 40}}},
	})
	if err != nil || none.NewTransactionID != nil || len(none.Rows) != 1 {
		t.Errorf("bill without me: %v / tx %v / rows %d", err, none.NewTransactionID, len(none.Rows))
	}

	// Any writable member adds a typed person; an email invite stays the owner's.
	name := "Eve"
	if _, err := f.svc.AddMember(ctx, f.friend, pid, AddMemberRequest{AdHoc: true, DisplayName: &name}); err != nil {
		t.Errorf("member adds a typed person: %v", err)
	}
	email := "eve@test.local"
	if _, err := f.svc.AddMember(ctx, f.friend, pid, AddMemberRequest{Email: &email, DisplayName: &name}); !errors.Is(err, ErrNotOwner) {
		t.Errorf("member invites by email: err = %v, want ErrNotOwner", err)
	}
}

func strp(s string) *string { return &s }
