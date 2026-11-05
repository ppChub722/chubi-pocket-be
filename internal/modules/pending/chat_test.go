package pending

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func (e *apiEnv) oneSided(t *testing.T) {
	t.Helper()
	for _, c := range [][2]string{{"WITHDRAW", "expense"}, {"DEPOSIT", "income"}} {
		e.exec(t, `INSERT INTO categories (id, user_id, name, type, is_system, system_kind) VALUES ($1, $2, $3, $4, TRUE, $3)`, uuid.New(), e.uid, c[0], c[1])
	}
}

func (e *apiEnv) pendingCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM pending_transactions WHERE user_id = $1`, e.uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// What the chatbot needs from pending: the readiness check, rows queued for review (✓ without auto)
// and rows booked at once (✓ with auto).
func TestChatCheck(t *testing.T) {
	s := &Service{}
	day := ptr("2026-11-05")
	acct := uuid.New()
	got := s.Check([]Draft{
		{Type: ptr("expense"), Amount: ptr(60.0), Date: day},                             // no wallet: valid (soft)
		{Type: ptr("transfer"), Amount: ptr(100.0), Date: day},                           // a transfer needs a wallet on one side
		{Type: ptr("transfer"), Amount: ptr(100.0), Date: day, AccountID: &acct},         // a withdrawal is fine
		{Type: ptr("expense"), Date: day},                                                // no amount
		{Type: ptr("expense"), Amount: ptr(60.0), Date: day, Paper: &DraftPaper{}},       // a shared bill with nobody on it
	})
	want := [][]string{{}, {NeedWallet}, {}, {NeedAmount}, {NeedPeople}}
	for i := range want {
		if len(got[i]) != len(want[i]) || (len(want[i]) > 0 && got[i][0] != want[i][0]) {
			t.Errorf("draft %d: missing %v, want %v", i, got[i], want[i])
		}
	}
}

func TestChatQueueAndBook(t *testing.T) {
	e := newAPIEnv(t)
	e.oneSided(t)
	ctx := context.Background()
	ref := json.RawMessage(`{"channel":"telegram","chat_id":7,"card_id":"abc","message_id":1}`)
	day := ptr("2026-11-05")
	exp := Draft{Type: ptr("expense"), Amount: ptr(60.0), AccountID: &e.acct, CategoryID: &e.cat, Date: day}
	wd := Draft{Type: ptr("transfer"), Amount: ptr(100.0), AccountID: &e.acct, Date: day}

	// ✓ without auto: rows wait on the pending page, source telegram
	ids, err := e.svc.CreateFrom(ctx, e.uid, SourceTelegram, ref, []Draft{exp, wd})
	if err != nil || len(ids) != 2 {
		t.Fatalf("CreateFrom = %v, %v", ids, err)
	}
	p, err := e.svc.store.Get(ctx, e.uid, ids[0])
	if err != nil || p.Source != SourceTelegram || string(p.SourceRef) == "" {
		t.Fatalf("row = %+v, %v", p, err)
	}
	if n := e.pendingCount(t); n != 2 {
		t.Fatalf("pending rows = %d, want 2", n)
	}
	// a bad draft changes nothing
	if _, err := e.svc.CreateFrom(ctx, e.uid, SourceTelegram, ref, []Draft{exp, {Type: ptr("gift")}}); !errors.Is(err, ErrInvalidDraft) {
		t.Fatalf("bad draft: %v", err)
	}
	if n := e.pendingCount(t); n != 2 {
		t.Fatalf("pending rows after a refused create = %d, want 2", n)
	}

	// ✓ with auto: the expense and a withdrawal (one wallet) go straight to the ledger, nothing stays pending
	before := e.pendingCount(t)
	booked, err := e.svc.Book(ctx, e.uid, SourceTelegram, ref, []Draft{exp, wd})
	if err != nil || booked.Booked != 2 {
		t.Fatalf("Book = %+v, %v", booked, err)
	}
	if n := e.pendingCount(t); n != before {
		t.Fatalf("pending rows after Book = %d, want %d (nothing left)", n, before)
	}
	var txs int
	if err := e.pool.QueryRow(ctx, `SELECT COUNT(*) FROM transactions WHERE user_id = $1`, e.uid).Scan(&txs); err != nil || txs != 2 {
		t.Fatalf("transactions = %d, %v", txs, err)
	}

	// one row cannot book (a wallet that is not the user's): the other still books, the failed row is deleted
	bad := Draft{Type: ptr("expense"), Amount: ptr(10.0), AccountID: ptr(uuid.New()), Date: day}
	booked, err = e.svc.Book(ctx, e.uid, SourceTelegram, ref, []Draft{exp, bad})
	if !errors.Is(err, ErrBookFailed) || booked.Booked != 1 {
		t.Fatalf("Book with a bad row = %+v, %v", booked, err)
	}
	if n := e.pendingCount(t); n != before {
		t.Fatalf("pending rows after a failed Book = %d, want %d", n, before)
	}
}
