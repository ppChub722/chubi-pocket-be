package pending

import (
	"context"
	"encoding/json"
	"testing"
)

// The chatbot's way in (K4): rows with a source, a Reply edit replaces them, a row settled in the
// app is no longer alive, and a one-wallet transfer (a withdrawal) books.
func TestChatRowsLifecycle(t *testing.T) {
	e := newAPIEnv(t)
	ctx := context.Background()
	ref := json.RawMessage(`{"channel":"telegram","chat_id":7,"card_id":"abc","message_id":1,"text":"x"}`)
	day := ptr("2026-11-05")
	exp := Draft{Type: ptr("expense"), Amount: ptr(60.0), AccountID: &e.acct, CategoryID: &e.cat, Date: day}
	wd := Draft{Type: ptr("transfer"), Amount: ptr(100.0), AccountID: &e.acct, Date: day}

	ids, err := e.svc.CreateFrom(ctx, e.uid, SourceTelegram, ref, []Draft{exp, wd})
	if err != nil || len(ids) != 2 {
		t.Fatalf("CreateFrom = %v, %v", ids, err)
	}
	p, err := e.svc.store.Get(ctx, e.uid, ids[0])
	if err != nil || p.Source != SourceTelegram || string(p.SourceRef) == "" {
		t.Fatalf("row = %+v, %v", p, err)
	}
	if n, err := e.svc.Alive(ctx, e.uid, ids); err != nil || n != 2 {
		t.Fatalf("Alive = %d, %v", n, err)
	}

	// a Reply edit: the old rows go, the new one comes, in one go
	next, err := e.svc.Replace(ctx, e.uid, ids, SourceTelegram, ref, []Draft{exp})
	if err != nil || len(next) != 1 {
		t.Fatalf("Replace = %v, %v", next, err)
	}
	if n, _ := e.svc.Alive(ctx, e.uid, ids); n != 0 {
		t.Fatalf("old rows alive = %d", n)
	}

	// a bad draft changes nothing
	if _, err := e.svc.Replace(ctx, e.uid, next, SourceTelegram, ref, []Draft{{Type: ptr("gift")}}); err == nil {
		t.Fatal("bad draft accepted")
	}
	if n, _ := e.svc.Alive(ctx, e.uid, next); n != 1 {
		t.Fatalf("rows after a refused replace = %d", n)
	}

	// ✓ = Submit: the expense and a withdrawal (one wallet) both book
	both, err := e.svc.CreateFrom(ctx, e.uid, SourceTelegram, ref, []Draft{exp, wd})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Submit(ctx, e.uid, SubmitRequest{IDs: both})
	if err != nil || len(res.Submitted) != 2 || len(res.Failed) != 0 {
		t.Fatalf("Submit = %+v, %v", res, err)
	}
	if n, _ := e.svc.Alive(ctx, e.uid, both); n != 0 {
		t.Fatalf("rows alive after submit = %d", n)
	}
}
