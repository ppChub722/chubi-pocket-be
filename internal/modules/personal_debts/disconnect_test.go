package personal_debts

import (
	"encoding/json"
	"errors"
	"testing"
)

// 0.4.1-1 D9: the partner disconnects their copy of my split. Both entries stay (same amount) as
// plain debts; my bill edits no longer reach them (no split_changed); I get debt_disconnected; my own
// split stays a split of my bill. A second disconnect → DEBT_NOT_LINKED.
func TestDisconnectPartnerCopy(t *testing.T) {
	f := newSplitEditFixture(t)
	s := f.splits(t)
	m := f.mirror(t)
	if m == nil || m.CounterpartDebtID == nil {
		t.Fatalf("partner mirror = %+v, want a linked copy", m)
	}

	res, err := f.debts.Disconnect(f.ctx, f.partner, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry.PartnerLinked || res.Entry.DisconnectedAt == nil || res.Entry.Amount != 100 || res.PersonBalance.Net != -100 {
		t.Fatalf("partner's entry after disconnect = %+v / %+v", res.Entry, res.PersonBalance)
	}
	if ds, _ := json.Marshal(res.Effects.Debts); len(res.Effects.Debts) != 1 {
		t.Errorf("effects.debts = %s", ds)
	}

	mine, err := f.debts.GetEntry(f.ctx, f.me, s["Partner"].DebtID)
	if err != nil || mine.Entry.PartnerLinked || mine.Entry.DisconnectedAt == nil || mine.Entry.Source == nil ||
		mine.Entry.Source.Kind != SourceTransaction || mine.Entry.Amount != 100 {
		t.Fatalf("my split after the partner's disconnect = %+v (%v)", mine, err)
	}
	var notes int
	var payload []byte
	if err := f.pool.QueryRow(f.ctx, `SELECT COUNT(*), MAX(payload::text)::bytea FROM notifications
		WHERE recipient_user_id = $1 AND type = 'debt_disconnected'`, f.me).Scan(&notes, &payload); err != nil || notes != 1 {
		t.Fatalf("debt_disconnected notices = %d (%v), want 1", notes, err)
	}
	var p struct {
		DebtID string  `json:"debt_id"`
		Amount float64 `json:"amount"`
	}
	if err := json.Unmarshal(payload, &p); err != nil || p.DebtID != s["Partner"].DebtID.String() || p.Amount != 100 {
		t.Errorf("payload = %s (%v)", payload, err)
	}

	before := len(f.changes(t))
	if err := f.edit(t, keep(s["Partner"], 150), keep(s["Beam"], 100)); err != nil {
		t.Fatal(err)
	}
	if after := len(f.changes(t)); after != before {
		t.Errorf("split_changed after a disconnect: %d → %d, want none new", before, after)
	}
	if m2 := f.mirror(t); m2.Amount != 100 {
		t.Errorf("partner copy followed my edit: %v", m2.Amount)
	}

	if _, err := f.debts.Disconnect(f.ctx, f.me, s["Partner"].DebtID); !errors.Is(err, ErrDebtNotLinked) {
		t.Errorf("second disconnect: err = %v, want DEBT_NOT_LINKED", err)
	}
}
