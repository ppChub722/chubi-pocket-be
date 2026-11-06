package chatbot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/llm"
)

// memSaver records what ✓ asked for; nothing is stored until a card is confirmed.
type memSaver struct {
	mu      sync.Mutex
	auto    bool
	missing map[int][]string // items index → readiness codes Check answers
	queued  [][]Item
	booked  [][]Item
	refs    []SaveRef
	bookN   int   // rows Book reports booked
	unlinked int  // rows Book reports as not tied to their recurring slot
	bookErr error // Book's error
	failQ   error // Queue's error
}

func (m *memSaver) Check(items []Item) [][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]string, len(items))
	for i := range items {
		out[i] = m.missing[i]
	}
	return out
}

func (m *memSaver) Auto(context.Context, uuid.UUID, string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.auto
}

func (m *memSaver) Queue(_ context.Context, _ uuid.UUID, ref SaveRef, items []Item) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failQ != nil {
		return m.failQ
	}
	m.queued, m.refs = append(m.queued, items), append(m.refs, ref)
	return nil
}

func (m *memSaver) Book(_ context.Context, _ uuid.UUID, ref SaveRef, items []Item) (BookOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refs = append(m.refs, ref)
	if m.bookErr == nil {
		m.booked = append(m.booked, items)
		return BookOutcome{Booked: len(items), Unlinked: m.unlinked}, nil
	}
	return BookOutcome{Booked: m.bookN}, m.bookErr
}

func (m *memSaver) set(f func(*memSaver)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f(m)
}

// A bot on the real (non-fake) provider slot with the keyword client: ✓ goes through the saver.
func newSavingBot() (*Bot, *fakeSender, *memSaver) {
	s := &fakeSender{}
	b := New(s, staticLists{}, llm.StaticRouter(llm.ProviderAnthropic, llm.NewKeywordFake()), 0, time.Second,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.now = func() time.Time { return testDay.Add(5 * time.Hour) }
	m := &memSaver{}
	b.WithSaver(m)
	return b, s, m
}

// The flow the owner set: the card is in memory until ✓; ✓ without auto queues pending rows, ✓ with
// auto books at once; ✗ and a Reply edit write nothing.
func TestBotSettlesOnTick(t *testing.T) {
	b, s, m := newSavingBot()
	user, ctx := uuid.New(), context.Background()

	// a card writes nothing
	b.Text(ctx, user, text(1, "multi tx", 0))
	card := s.last()
	if card.kind != "card" || len(m.queued)+len(m.booked) != 0 {
		t.Fatalf("card = %+v queued %d booked %d", card, len(m.queued), len(m.booked))
	}
	// a Reply edit writes nothing either
	b.Text(ctx, user, text(2, "edit", card.msgID))
	card = s.log[len(s.log)-2]
	if len(m.queued)+len(m.booked) != 0 {
		t.Fatal("a Reply edit wrote rows")
	}
	// ✓ without auto: the rows are queued for the pending page, the card says so, a second tap is "handled"
	b.Tap(ctx, tap(card.buttons[0].Data, card.msgID, chat))
	if e := s.last(); e.kind != "edit" || !strings.HasSuffix(e.text, TextQueued) || len(e.buttons) != 1 {
		t.Fatalf("queued = %+v", e)
	}
	if len(m.queued) != 1 || len(m.queued[0]) != 3 || len(m.booked) != 0 || m.refs[0].ChatID != chat || m.refs[0].MessageID != 1 {
		t.Fatalf("queued %+v booked %d refs %+v", m.queued, len(m.booked), m.refs)
	}
	b.Tap(ctx, tap(card.buttons[0].Data, card.msgID, chat))
	if a := s.last(); a.kind != "answer" || a.text != TextHandled || len(m.queued) != 1 {
		t.Fatalf("double tap = %+v", a)
	}

	// ✗ writes nothing
	b.Text(ctx, user, text(3, "income", 0))
	c2 := s.last()
	b.Tap(ctx, tap(c2.buttons[1].Data, c2.msgID, chat))
	if e := s.last(); !strings.HasSuffix(e.text, TextCancelled) || len(m.queued) != 1 || len(m.booked) != 0 {
		t.Fatalf("cancelled = %+v", e)
	}

	// auto on: ✓ books at once, nothing is queued
	m.set(func(m *memSaver) { m.auto = true })
	b.Text(ctx, user, text(4, "multi tx", 0))
	c3 := s.last()
	b.Tap(ctx, tap(c3.buttons[0].Data, c3.msgID, chat))
	if e := s.last(); !strings.HasSuffix(e.text, TextSaved) || len(m.booked) != 1 || len(m.queued) != 1 {
		t.Fatalf("auto = %+v booked %d queued %d", e, len(m.booked), len(m.queued))
	}
}

// Booking fails with auto on: an error line, no fallback to pending, the card stays open for another ✓.
func TestBotAutoBookingFails(t *testing.T) {
	b, s, m := newSavingBot()
	user, ctx := uuid.New(), context.Background()
	m.set(func(m *memSaver) { m.auto, m.bookErr = true, errors.New("db down") })

	b.Text(ctx, user, text(1, "multi tx", 0))
	card := s.last()
	b.Tap(ctx, tap(card.buttons[0].Data, card.msgID, chat))
	if l := s.last(); l.kind != "msg" || !strings.Contains(l.text, CodeDB) || len(m.queued) != 0 || len(m.booked) != 0 {
		t.Fatalf("failed ✓ = %+v queued %d", l, len(m.queued))
	}
	// the card is still open: fixing the problem and pressing ✓ again books
	m.set(func(m *memSaver) { m.bookErr = nil })
	b.Tap(ctx, tap(card.buttons[0].Data, card.msgID, chat))
	if e := s.last(); e.kind != "edit" || !strings.HasSuffix(e.text, TextSaved) || len(m.booked) != 1 {
		t.Fatalf("retry = %+v booked %d", e, len(m.booked))
	}

	// only part booked: the card closes and says how many
	m.set(func(m *memSaver) { m.bookErr, m.bookN = errors.New("one failed"), 2 })
	b.Text(ctx, user, text(2, "multi tx", 0))
	c2 := s.last()
	b.Tap(ctx, tap(c2.buttons[0].Data, c2.msgID, chat))
	if e := s.last(); e.kind != "edit" || !strings.HasSuffix(e.text, fmt.Sprintf(textsFor("th").PartialBooked, 2, 3)) {
		t.Fatalf("partial = %+v", e)
	}
	b.Tap(ctx, tap(c2.buttons[0].Data, c2.msgID, chat))
	if a := s.last(); a.kind != "answer" || a.text != TextHandled {
		t.Fatalf("tap after a partial booking = %+v", a)
	}
}

// The check runs before the card goes out: a missing field shows as [… ?], ✓ answers what is missing
// and the card stays open until a Reply fills it.
func TestBotNotReadyCard(t *testing.T) {
	b, s, m := newSavingBot()
	user, ctx := uuid.New(), context.Background()
	m.set(func(m *memSaver) { m.missing = map[int][]string{0: {"wallet"}} })

	b.Text(ctx, user, text(1, "normal tx", 0))
	card := s.last()
	if card.kind != "card" || !strings.Contains(card.text, "[กสิกร ?]") {
		t.Fatalf("card = %+v", card)
	}
	b.Tap(ctx, tap(card.buttons[0].Data, card.msgID, chat))
	if a := s.last(); a.kind != "answer" || a.text != textsFor("th").missing([]string{"wallet"}) || len(m.queued) != 0 {
		t.Fatalf("✓ on a card that is not ready = %+v", a)
	}
	// a Reply fills the gap: the new card is ready and ✓ goes through
	m.set(func(m *memSaver) { m.missing = nil })
	b.Text(ctx, user, text(2, "edit", card.msgID))
	card = s.log[len(s.log)-2]
	b.Tap(ctx, tap(card.buttons[0].Data, card.msgID, chat))
	if e := s.last(); !strings.HasSuffix(e.text, TextQueued) || len(m.queued) != 1 {
		t.Fatalf("after the fill = %+v queued %d", e, len(m.queued))
	}
}

func TestMissingText(t *testing.T) {
	if got := textsFor("th").missing([]string{"wallet", "amount"}); got != "ยังขาด: กระเป๋า, ยอด · Reply บนการ์ดเพื่อเติม" {
		t.Errorf("th = %q", got)
	}
	if got := textsFor("en").missing([]string{"date"}); got != "Still missing: date · Reply on the card to fill it" {
		t.Errorf("en = %q", got)
	}
}

// A booking refused because the saving it comes from holds less: its own line, the card stays open.
func TestBotAutoBookingNotEnoughSaving(t *testing.T) {
	b, s, m := newSavingBot()
	user, ctx := uuid.New(), context.Background()
	m.set(func(m *memSaver) { m.auto, m.bookErr = true, &pending.BookError{Code: "INSUFFICIENT_SAVING"} })
	b.Text(ctx, user, text(1, "withdraw saving", 0))
	card := s.last()
	b.Tap(ctx, tap(card.buttons[0].Data, card.msgID, chat))
	if l := s.last(); l.kind != "msg" || l.text != TextNotEnoughSaving {
		t.Fatalf("refused ✓ = %+v", l)
	}
	m.set(func(m *memSaver) { m.bookErr = nil })
	b.Tap(ctx, tap(card.buttons[0].Data, card.msgID, chat))
	if e := s.last(); e.kind != "edit" || !strings.HasSuffix(e.text, TextSaved) {
		t.Fatalf("retry = %+v", e)
	}
}

// A payment tied to a recurring item whose slot link failed: booked, and the card says so.
func TestBotAutoBookedButNotLinked(t *testing.T) {
	b, s, m := newSavingBot()
	user, ctx := uuid.New(), context.Background()
	m.set(func(m *memSaver) { m.auto, m.unlinked = true, 1 })
	b.Text(ctx, user, text(1, "normal tx", 0))
	card := s.last()
	b.Tap(ctx, tap(card.buttons[0].Data, card.msgID, chat))
	if e := s.last(); e.kind != "edit" || !strings.HasSuffix(e.text, TextSaved+"\n"+TextUnlinked) || len(m.booked) != 1 {
		t.Fatalf("booked but not linked = %+v", e)
	}
}

// CHATBOT_SAVE off (the default): the real model slot, cards and buttons, nothing written; ✓ says it is a trial.
func TestBotTrialModeWritesNothing(t *testing.T) {
	s := &fakeSender{}
	b := New(s, staticLists{}, llm.StaticRouter(llm.ProviderAnthropic, llm.NewKeywordFake()), 0, time.Second,
		slog.New(slog.NewTextHandler(io.Discard, nil))) // no WithSaver
	b.now = func() time.Time { return testDay.Add(5 * time.Hour) }
	user, ctx := uuid.New(), context.Background()
	for _, kw := range []string{"normal tx", "save goal", "debt lend", "split ลี"} {
		b.Text(ctx, user, text(1, kw, 0))
		card := s.last()
		if card.kind != "card" {
			t.Fatalf("%s: no card: %+v", kw, card)
		}
		b.Tap(ctx, tap(card.buttons[0].Data, card.msgID, chat))
		if e := s.last(); e.kind != "edit" || !strings.HasSuffix(e.text, TextSaved+TextTrialMark) {
			t.Fatalf("%s: ✓ = %+v", kw, e)
		}
	}
}
