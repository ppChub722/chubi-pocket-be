package chatbot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type memMeter struct {
	tier  string
	used  int
	calls []Call
	since time.Time
}

func (m *memMeter) Tier(context.Context, uuid.UUID) (string, error) { return m.tier, nil }
func (m *memMeter) Used(_ context.Context, _ uuid.UUID, since time.Time) (int, error) {
	m.since = since
	return m.used, nil
}
func (m *memMeter) Record(_ context.Context, _ uuid.UUID, c Call) error {
	m.calls = append(m.calls, c)
	return nil
}

func TestBotTierCapAndLog(t *testing.T) {
	b, s, _ := newSavingBot()
	m := &memMeter{tier: "free"}
	b.WithMeter(m, 3)
	user, ctx := uuid.New(), context.Background()

	// free: the fixed line, no model call, nothing logged
	b.Text(ctx, user, text(1, "normal tx", 0))
	if l := s.last(); l.kind != "msg" || l.text != TextTierFree || len(m.calls) != 0 {
		t.Fatalf("free = %+v calls %d", l, len(m.calls))
	}

	// owner under the cap: a card, and the call is logged without the text
	m.tier = TierOwner
	b.Text(ctx, user, text(2, "normal tx", 0))
	if s.last().kind != "card" || len(m.calls) != 1 || m.calls[0].Status != "ok" || m.calls[0].Channel != "telegram" {
		t.Fatalf("owner = %+v calls %+v", s.last(), m.calls)
	}
	// the cap counts from the 1st (Asia/Bangkok)
	if want := time.Date(2026, 11, 1, 0, 0, 0, 0, bangkok); !m.since.Equal(want) {
		t.Errorf("since = %v, want %v", m.since, want)
	}

	// a failed call is logged with its code, and the owner sees the code line
	b.Text(ctx, user, text(3, "error", 0))
	if len(m.calls) != 2 || m.calls[1].Status == "ok" || !strings.Contains(s.last().text, m.calls[1].Status) {
		t.Fatalf("failed call = %+v last %+v", m.calls, s.last())
	}

	// at the cap: the fixed line, no further call
	m.used = 3
	b.Text(ctx, user, text(4, "normal tx", 0))
	if l := s.last(); l.text != TextCapReached || len(m.calls) != 2 {
		t.Fatalf("cap = %+v calls %d", l, len(m.calls))
	}
}

func TestMonthStart(t *testing.T) {
	// 31 Oct 18:00 UTC is already 1 Nov in Bangkok
	got := monthStart(time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 11, 1, 0, 0, 0, 0, bangkok); !got.Equal(want) {
		t.Errorf("monthStart = %v, want %v", got, want)
	}
}
