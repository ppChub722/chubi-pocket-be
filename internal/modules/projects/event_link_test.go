package projects

import (
	"context"
	"errors"
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

func modeP(m string) *string { return &m }

// TestLinkModeEventSwitches — 0.4.0-3 B3: a transaction switches into an event (I paid → the bill;
// Bee paid → my copy of my share, no wallet) and out again; my tx → the bill (apply_to_event); the
// bill → my tx (apply_to_my_tx, or apply-event later), live in_sync / differences.
func TestLinkModeEventSwitches(t *testing.T) {
	f := newQCFixture(t)
	ctx := context.Background()
	ev, err := f.svc.Create(ctx, f.owner, CreateProjectRequest{Name: "Beach"})
	if err != nil {
		t.Fatal(err)
	}
	pid := ev.Event.ID
	w, err := f.txs.CreateWrite(ctx, f.owner, transactions.CreateRequest{
		Type: transactions.TypeExpense, AccountID: &f.acctPersonal, Amount: 300, Date: "2026-09-20", CategoryID: &f.cat,
	})
	if err != nil {
		t.Fatal(err)
	}
	id := w.Rows[0].ID
	update := func(req transactions.UpdateRequest) *transactions.WriteResult {
		t.Helper()
		out, err := f.txs.UpdateWrite(ctx, f.owner, id, req)
		if err != nil {
			t.Fatalf("update %+v: %v", req, err)
		}
		return out
	}

	// personal → event, I paid: the transaction is the bill; Bee's share is a board split.
	u := update(transactions.UpdateRequest{LinkMode: modeP("event"), Event: &transactions.EventTarget{
		ProjectID: pid, BillPeople: shared.BillPeople{Members: []shared.BillMember{
			{BillPerson: shared.BillPerson{Me: true}, Share: 100},
			{BillPerson: shared.BillPerson{ContactID: &f.contactB}, Share: 200},
		}},
	}})
	eb := u.Rows[0].EventBill
	if u.Rows[0].LinkMode != transactions.LinkEvent || eb == nil || eb.Kind != "origin" || eb.InSync == nil || !*eb.InSync ||
		u.Rows[0].MyShare == nil || *u.Rows[0].MyShare != 100 || f.accountBalance(t, f.acctPersonal) != 700 {
		t.Fatalf("into the event: mode %s bill %+v share %v wallet %v", u.Rows[0].LinkMode, eb, u.Rows[0].MyShare, f.accountBalance(t, f.acctPersonal))
	}

	// event → personal: the bill made from it goes; the transaction is loose again.
	u = update(transactions.UpdateRequest{LinkMode: modeP("personal")})
	if u.Rows[0].LinkMode != transactions.LinkPersonal || u.Rows[0].EventBill != nil {
		t.Fatalf("out of the event: %s %+v", u.Rows[0].LinkMode, u.Rows[0].EventBill)
	}
	if n := f.countRows(t, `SELECT COUNT(*) FROM project_transactions WHERE project_id = $1`, pid); n != 0 {
		t.Errorf("board rows left = %d", n)
	}

	// personal → event, Bee paid: my transaction becomes my share, no wallet.
	u = update(transactions.UpdateRequest{LinkMode: modeP("event"), Event: &transactions.EventTarget{
		ProjectID: pid, BillPeople: shared.BillPeople{
			Payer:   &shared.BillPerson{ContactID: &f.contactB},
			Members: []shared.BillMember{{BillPerson: shared.BillPerson{Me: true}, Share: 120}},
		},
	}})
	row := u.Rows[0]
	if row.Amount != 120 || row.Account != nil || row.EventBill == nil || row.EventBill.Kind != "copy" ||
		row.EventBill.BillAmount != 300 || f.accountBalance(t, f.acctPersonal) != 1000 {
		t.Fatalf("Bee paid: amount %v account %v bill %+v wallet %v", row.Amount, row.Account, row.EventBill, f.accountBalance(t, f.acctPersonal))
	}
	billID := row.EventBill.BillID

	// My copy → the bill: my share is now 150 (Bee's rest 150).
	u = update(transactions.UpdateRequest{Amount: f64p(150), ApplyToEvent: true})
	eb = u.Rows[0].EventBill
	mine := 0.0
	for _, m := range eb.Members {
		if m.Member.IsMe {
			mine = m.Share
		}
	}
	if eb.InSync == nil || !*eb.InSync || mine != 150 || eb.BillAmount != 300 {
		t.Errorf("apply_to_event: in_sync %v my share %v bill %v", eb.InSync, mine, eb.BillAmount)
	}

	// Someone edits the bill (here: me, without applying): my copy is out of sync, the patch says so.
	desc := "Seafood"
	req := UpdateProjectTransactionRequest{}
	req.SetText(&desc, nil)
	patch, err := f.svc.UpdateProjectTransaction(ctx, f.owner, pid, billID, req)
	if err != nil {
		t.Fatal(err)
	}
	mt := patch.MyTransaction
	if mt == nil || mt.EventBill == nil || mt.EventBill.InSync == nil || *mt.EventBill.InSync ||
		len(mt.EventBill.Differences) != 1 || mt.EventBill.Differences[0].Field != "description" {
		t.Fatalf("my_transaction after a bill edit = %+v", mt)
	}
	// Later: "อัปเดตตามอีเวนต์".
	res, err := f.svc.ApplyEventToTx(ctx, f.owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if r := res.Rows[0]; shared.TextOr(r.Description) != "Seafood" || !*r.EventBill.InSync {
		t.Errorf("apply-event: %+v / %+v", r.Description, r.EventBill)
	}
	// A bill edit that applies right away (apply_to_my_tx).
	date := "2026-09-21"
	patch, err = f.svc.UpdateProjectTransaction(ctx, f.owner, pid, billID, UpdateProjectTransactionRequest{Date: &date, ApplyToMyTx: true})
	if err != nil {
		t.Fatal(err)
	}
	if patch.MyTransaction == nil || patch.MyTransaction.Date != "2026-09-21" || !*patch.MyTransaction.EventBill.InSync {
		t.Errorf("apply_to_my_tx: %+v", patch.MyTransaction)
	}

	// The resolve sheet's "as debt" keeps its bill (source event, editable money).
	pd := personal_debts.NewService(personal_debts.NewStore(f.pool), f.txs)
	debt, err := pd.CreateEntry(ctx, f.owner, "n:Dan", personal_debts.CreateEntryRequest{Direction: "received", Amount: 50, BillID: &billID})
	if err != nil {
		t.Fatal(err)
	}
	if s := debt.Entry.Source; s == nil || s.Kind != "event" || s.ID == nil || *s.ID != pid {
		t.Errorf("as-debt source = %+v, want event %v", s, pid)
	}
	amount := 60.0
	if _, err := pd.UpdateEntry(ctx, f.owner, debt.Entry.ID, personal_debts.UpdateEntryRequest{Amount: &amount}); err != nil {
		t.Errorf("an event debt's amount stays editable: %v", err)
	}
	if _, err := pd.CreateEntry(ctx, f.owner, "n:Dan", personal_debts.CreateEntryRequest{Direction: "received", Amount: 5,
		BillID: &f.cat}); !errors.Is(err, personal_debts.ErrBillNotFound) {
		t.Errorf("unknown bill: err = %v", err)
	}

	// Members carry my contact for them (Bee = my contact, linked).
	members, err := f.svc.ListMembers(ctx, f.owner, pid)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range members.Data {
		if m.UserID != nil && *m.UserID == f.friend {
			found = m.ContactID != nil && *m.ContactID == f.contactB
		}
	}
	if !found {
		t.Errorf("Bee's member row should carry contact_id %v: %+v", f.contactB, members.Data)
	}
}

func f64p(v float64) *float64 { return &v }
