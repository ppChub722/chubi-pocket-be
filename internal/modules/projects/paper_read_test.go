package projects

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/database"
)

// A bill I booked: the event page, the bill and my transaction each read it once. GET
// /transactions/:id carries the paper and the paper carries my transaction, so the transaction
// inside the paper must not carry the paper again (it looped until the 10 s timeout, prod
// 2026-11-17).
func TestBookedBillReadsDontLoop(t *testing.T) {
	f := newFx(t)
	A := f.as(f.a)
	ev, mem := f.eventWithBee(A)
	bill := str(obj(A.do("POST", "/projects/"+ev+"/bills", map[string]any{"paper": map[string]any{
		"type": "expense", "amount": 300, "date": "2026-11-01", "split_mode": "equal", "people": []any{
			map[string]any{"me": true, "paid": 300},
			map[string]any{"member_id": mem["Bee"]},
		}}, "my_books": map[string]any{"account_id": f.walletA}}, "", 201), "paper"), "id")
	var tid string
	if err := f.pool.QueryRow(context.Background(), `SELECT id::text FROM transactions
		WHERE user_id = $1 AND split_transaction_id = $2`, f.a, bill).Scan(&tid); err != nil {
		t.Fatalf("A's book of the bill: %v", err)
	}

	// counted — one read with a deadline (a loop ends there instead of hanging the test).
	counted := func(name string, budget int64, read func(ctx context.Context) error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ctx, stats := database.WithQueryStats(ctx)
		if err := read(ctx); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if ctx.Err() != nil {
			t.Errorf("%s: hit the deadline after %d queries", name, stats.Count())
			return
		}
		t.Logf("%s: %d queries", name, stats.Count())
		if stats.Count() > budget {
			t.Errorf("%s ran %d queries, budget %d", name, stats.Count(), budget)
		}
	}
	get := func(path string) func(ctx context.Context) error {
		return func(ctx context.Context) error {
			req := httptest.NewRequest("GET", "/api/v1"+path, nil).WithContext(ctx)
			w := httptest.NewRecorder()
			f.engine(f.a).ServeHTTP(w, req)
			if w.Code != 200 {
				t.Errorf("GET %s: status %d; %s", path, w.Code, w.Body.String())
			}
			return nil
		}
	}

	counted("event detail", 40, get("/projects/"+ev+"/detail"))
	counted("event bill", 40, get("/projects/"+ev+"/bills/"+bill))
	counted("my transaction", 40, func(ctx context.Context) error {
		d, err := f.txs.Get(ctx, f.a, mustUUID(t, tid))
		if err == nil && d.Paper == nil {
			t.Errorf("my transaction came without its paper")
		}
		return err
	})
}
