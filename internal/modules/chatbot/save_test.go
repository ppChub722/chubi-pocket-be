package chatbot

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/llm"
)

// memSaver keeps rows in a map: what the pending table would hold.
type memSaver struct {
	mu       sync.Mutex
	rows     map[uuid.UUID]Item
	refs     []SaveRef
	failRows int // the next Confirm books only this many fewer rows
	booked   []uuid.UUID
}

func newMemSaver() *memSaver { return &memSaver{rows: map[uuid.UUID]Item{}} }

func (m *memSaver) add(items []Item) []uuid.UUID {
	ids := []uuid.UUID{}
	for _, it := range items {
		id := uuid.New()
		m.rows[id] = it
		ids = append(ids, id)
	}
	return ids
}

func (m *memSaver) Save(_ context.Context, _ uuid.UUID, ref SaveRef, items []Item) ([]uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refs = append(m.refs, ref)
	return m.add(items), nil
}

func (m *memSaver) Replace(_ context.Context, _ uuid.UUID, old []uuid.UUID, ref SaveRef, items []Item) ([]uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range old {
		delete(m.rows, id)
	}
	m.refs = append(m.refs, ref)
	return m.add(items), nil
}

func (m *memSaver) Alive(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, id := range ids {
		if _, ok := m.rows[id]; ok {
			n++
		}
	}
	return n, nil
}

func (m *memSaver) Confirm(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for i, id := range ids {
		if i >= len(ids)-m.failRows {
			break
		}
		delete(m.rows, id)
		m.booked = append(m.booked, id)
		n++
	}
	return n, nil
}

func (m *memSaver) Discard(_ context.Context, _ uuid.UUID, ids []uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		delete(m.rows, id)
	}
	return nil
}

func (m *memSaver) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rows)
}

// A bot on the real (non-fake) provider slot with the keyword client: cards are rows.
func newSavingBot() (*Bot, *fakeSender, *memSaver) {
	s := &fakeSender{}
	b := New(s, staticLists{}, llm.StaticRouter(llm.ProviderAnthropic, llm.NewKeywordFake()), 0, time.Second,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.now = func() time.Time { return testDay.Add(5 * time.Hour) }
	m := newMemSaver()
	b.WithSaver(m)
	return b, s, m
}

func TestBotSavesRows(t *testing.T) {
	b, s, m := newSavingBot()
	user, ctx := uuid.New(), context.Background()

	// a card = its rows, with where they came from
	b.Text(ctx, user, text(1, "multi tx", 0))
	card := s.last()
	if m.count() != 3 || len(m.refs) != 1 || m.refs[0].ChatID != chat || m.refs[0].MessageID != 1 || m.refs[0].Text != "multi tx" {
		t.Fatalf("rows = %d refs = %+v", m.count(), m.refs)
	}
	// Reply edit: the rows are replaced, not added
	b.Text(ctx, user, text(2, "edit", card.msgID))
	if m.count() != 3 {
		t.Fatalf("rows after edit = %d, want 3", m.count())
	}
	card = s.log[len(s.log)-2]
	// ✓ books the rows, no trial mark
	b.Tap(ctx, tap(card.buttons[0].Data, card.msgID, chat))
	if e := s.last(); e.kind != "edit" || !strings.HasSuffix(e.text, TextSaved) || m.count() != 0 || len(m.booked) != 3 {
		t.Fatalf("saved = %+v rows %d booked %d", e, m.count(), len(m.booked))
	}

	// ✗ drops the rows
	b.Text(ctx, user, text(3, "income", 0))
	c2 := s.last()
	b.Tap(ctx, tap(c2.buttons[1].Data, c2.msgID, chat))
	if e := s.last(); !strings.HasSuffix(e.text, TextCancelled) || m.count() != 0 {
		t.Fatalf("cancelled = %+v rows %d", e, m.count())
	}

	// a row settled in the app: the card is stale for the button and for a Reply
	b.Text(ctx, user, text(4, "multi tx", 0))
	c3 := s.last()
	for id := range m.rows {
		delete(m.rows, id)
		break
	}
	b.Tap(ctx, tap(c3.buttons[0].Data, c3.msgID, chat))
	if a := s.last(); a.kind != "answer" || a.text != TextHandled || len(m.booked) != 3 {
		t.Fatalf("stale tap = %+v", a)
	}
	b.Text(ctx, user, text(5, "edit", c3.msgID))
	if a := s.last(); a.text != TextHandled {
		t.Fatalf("stale reply = %+v", a)
	}

	// some rows fail: the card says how many went through, the rest stay in the app
	b.Text(ctx, user, text(6, "multi tx", 0))
	c4 := s.last()
	m.failRows = 1
	b.Tap(ctx, tap(c4.buttons[0].Data, c4.msgID, chat))
	if e := s.last(); !strings.HasSuffix(e.text, fmt.Sprintf(textsFor("th").Partial, 2, 3)) || m.count() != 3 { // 2 left by the stale card + 1 failed
		t.Fatalf("partial = %+v rows %d", e, m.count())
	}
}
