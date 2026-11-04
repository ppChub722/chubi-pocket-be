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

	"github.com/ppChub722/chubi-pocket-be/internal/modules/telegram"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/llm"
)

var (
	walletA   = Wallet{ID: uuid.New(), Name: "กสิกร", Main: true}
	walletB   = Wallet{ID: uuid.New(), Name: "ออมทรัพย์"}
	catFood   = Category{ID: uuid.New(), Name: "อาหาร", Type: "expense"}
	testLists = Lists{Wallets: []Wallet{walletA, walletB}, Categories: []Category{catFood}}
	testDay   = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
)

func TestParseKeywords(t *testing.T) {
	fake := llm.NewKeywordFake()
	cases := []struct {
		text  string
		items int
		hint  string
		check func(t *testing.T, its []Item)
	}{
		{"normal tx", 1, "", func(t *testing.T, its []Item) {
			if *its[0].CategoryID != catFood.ID || *its[0].AccountID != walletA.ID || its[0].Date != "2026-11-01" {
				t.Errorf("normal = %+v", its[0])
			}
		}},
		{"multi tx", 3, "", nil},
		{"income", 1, "", func(t *testing.T, its []Item) {
			if its[0].Type != "income" || its[0].CategoryID != nil {
				t.Errorf("income = %+v", its[0])
			}
		}},
		{"transfer", 1, "", func(t *testing.T, its []Item) {
			if *its[0].AccountID != walletA.ID || *its[0].TransferToID != walletB.ID {
				t.Errorf("transfer = %+v", its[0])
			}
		}},
		{"unsure", 1, "", func(t *testing.T, its []Item) {
			if !contains(its[0].Unsure, "category") {
				t.Errorf("unsure = %+v", its[0])
			}
		}},
		{"bad alias", 1, "", func(t *testing.T, its []Item) {
			if its[0].CategoryID != nil || !contains(its[0].Unsure, "category") {
				t.Errorf("bad alias = %+v", its[0])
			}
		}},
		{"partial", 1, "", nil},
		{"fake desc", 1, "", func(t *testing.T, its []Item) {
			if its[0].Description != nil {
				t.Errorf("fake desc kept %q", *its[0].Description)
			}
		}},
		{"no amount", 0, HintNoAmount, nil},
		{"not money", 0, HintNotMoney, nil},
		{"transfer missing", 0, HintTransferWallet, nil},
		{"สวัสดี", 0, HintNotMoney, nil},
	}
	for _, c := range cases {
		out, err := Parse(context.Background(), fake, testDay, testLists, c.text, nil)
		if err != nil {
			t.Fatalf("%s: %v", c.text, err)
		}
		if len(out.Items) != c.items || (c.items == 0 && out.Hint != c.hint) {
			t.Errorf("%s: %d items hint %q, want %d %q", c.text, len(out.Items), out.Hint, c.items, c.hint)
			continue
		}
		if c.check != nil {
			c.check(t, out.Items)
		}
	}
	if out, _ := Parse(context.Background(), fake, testDay, testLists, "refuse", nil); out.Fail != CodeRefused {
		t.Errorf("refuse fail = %q", out.Fail)
	}
	if _, err := Parse(context.Background(), fake, testDay, testLists, "error", nil); !errors.As(err, new(ErrProvider)) {
		t.Errorf("error keyword = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := Parse(ctx, fake, testDay, testLists, "slow", nil); err == nil {
		t.Error("slow did not time out")
	}
}

type sent struct {
	kind    string // msg | card | edit | answer
	chatID  int64
	msgID   int64
	text    string
	buttons []telegram.Button
}

type fakeSender struct {
	mu        sync.Mutex
	next      int64
	log       []sent
	reactions []string // "msgID:emoji"
	drafts    []string
}

func (f *fakeSender) add(s sent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, s)
}

func (f *fakeSender) SendMessage(_ context.Context, chatID int64, text string, _ int64) error {
	f.add(sent{kind: "msg", chatID: chatID, text: text})
	return nil
}

func (f *fakeSender) SendCard(_ context.Context, chatID int64, text string, _ int64, b []telegram.Button) (int64, error) {
	f.mu.Lock()
	f.next++
	id := 1000 + f.next
	f.mu.Unlock()
	f.add(sent{kind: "card", chatID: chatID, msgID: id, text: text, buttons: b})
	return id, nil
}

func (f *fakeSender) EditCard(_ context.Context, chatID, msgID int64, text string, b []telegram.Button) error {
	f.add(sent{kind: "edit", chatID: chatID, msgID: msgID, text: text, buttons: b})
	return nil
}

func (f *fakeSender) AnswerCallback(_ context.Context, _, text string) error {
	f.add(sent{kind: "answer", text: text})
	return nil
}

// Reactions and drafts are kept apart from log, so the message counts stay as they were.
func (f *fakeSender) SetReaction(_ context.Context, _, msgID int64, emoji string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reactions = append(f.reactions, fmt.Sprintf("%d:%s", msgID, emoji))
	return nil
}

func (f *fakeSender) SendDraft(_ context.Context, _, _ int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drafts = append(f.drafts, text)
	return nil
}

func (f *fakeSender) last() sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.log[len(f.log)-1]
}

type staticLists struct{}

func (staticLists) Lists(context.Context, uuid.UUID) (Lists, error) { return testLists, nil }
func (staticLists) Lang(context.Context, uuid.UUID) string          { return "th" }

func TestLang(t *testing.T) {
	cases := []struct{ app, tg, want string }{
		{"th", "en", "th"}, {"en", "th", "en"}, {"", "th-TH", "th"}, {"ja", "th", "th"}, {"", "", "en"}, {"", "fr", "en"},
	}
	for _, c := range cases {
		if got := telegram.Lang(c.app, c.tg); got != c.want {
			t.Errorf("Lang(%q, %q) = %q, want %q", c.app, c.tg, got, c.want)
		}
	}
	if !strings.Contains(cardText(Card{Lang: "en", Items: []Item{{Type: "expense", Amount: 60, Date: "2026-11-01"}}},
		testLists, testDay), "Category: 🏷️ none") {
		t.Error("an English card is not in English")
	}
}

func newTestBot(perMinute int) (*Bot, *fakeSender) {
	s := &fakeSender{}
	fake := llm.NewKeywordFake()
	fake.SlowFor = 300 * time.Millisecond // longer than the bot's timeout: slow = provider down
	b := New(s, staticLists{}, llm.StaticRouter(llm.ProviderFake, fake), perMinute, 100*time.Millisecond,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.now = func() time.Time { return testDay.Add(5 * time.Hour) }
	return b, s
}

const chat = int64(42)

func text(id int64, s string, replyTo int64) *telegram.Message {
	m := &telegram.Message{MessageID: id, Chat: telegram.Chat{ID: chat, Type: "private"}, Text: s}
	if replyTo > 0 {
		m.ReplyToMessage = &telegram.Message{MessageID: replyTo}
	}
	return m
}

func tap(data string, msgID, from int64) *telegram.CallbackQuery {
	return &telegram.CallbackQuery{ID: "q", From: telegram.User{ID: from}, Data: data,
		Message: &telegram.Message{MessageID: msgID, Chat: telegram.Chat{ID: chat, Type: "private"}}}
}

func TestBotCardFlow(t *testing.T) {
	b, s := newTestBot(0)
	user := uuid.New()
	ctx := context.Background()

	b.Text(ctx, user, text(1, "normal tx", 0))
	card := s.last()
	if card.kind != "card" || card.text != "🧾 1 รายการ | วันนี้\nnormal -฿60\nหมวด: 🏷️ อาหาร\nกระเป๋า: 👛 กสิกร" || len(card.buttons) != 2 {
		t.Fatalf("card = %+v", card)
	}

	// Reply "edit": the first amount becomes 50, same card, buttons kept
	b.Text(ctx, user, text(2, "edit", card.msgID))
	if e := s.last(); e.kind != "edit" || e.msgID != card.msgID || !strings.Contains(e.text, "-฿50") || len(e.buttons) != 2 {
		t.Fatalf("edit = %+v", e)
	}
	// Reply "edit empty": a fixed line, the card stays
	b.Text(ctx, user, text(3, "edit empty", card.msgID))
	if m := s.last(); m.kind != "msg" || m.text != TextNoAmount {
		t.Fatalf("edit empty = %+v", m)
	}

	// someone else's tap does nothing
	if !b.Tap(ctx, tap(card.buttons[0].Data, card.msgID, 7)) || s.last().text != TextHandled {
		t.Fatalf("other tap = %+v", s.last())
	}
	// ✓ closes the card: buttons gone, "(fake)" mark
	b.Tap(ctx, tap(card.buttons[0].Data, card.msgID, chat))
	if e := s.last(); e.kind != "edit" || len(e.buttons) != 0 || !strings.HasSuffix(e.text, TextSaved+TextFakeMark) {
		t.Fatalf("saved = %+v", e)
	}
	// a second tap (✗) on the closed card
	b.Tap(ctx, tap(card.buttons[1].Data, card.msgID, chat))
	if a := s.last(); a.kind != "answer" || a.text != TextHandled {
		t.Fatalf("double tap = %+v", a)
	}
	// a Reply on the closed card
	b.Text(ctx, user, text(4, "edit", card.msgID))
	if m := s.last(); m.text != TextHandled {
		t.Fatalf("reply on closed = %+v", m)
	}
	// ✗ on a new card
	b.Text(ctx, user, text(5, "income", 0))
	c2 := s.last()
	b.Tap(ctx, tap(c2.buttons[1].Data, c2.msgID, chat))
	if e := s.last(); !strings.HasSuffix(e.text, TextCancelled+TextFakeMark) {
		t.Fatalf("cancelled = %+v", e)
	}
	// not a card button
	if b.Tap(ctx, tap("li:abc", 1, chat)) {
		t.Error("login button taken by the chatbot")
	}
}

func TestBotAllAndLimits(t *testing.T) {
	b, s := newTestBot(2)
	user := uuid.New()
	ctx := context.Background()

	b.Text(ctx, user, text(1, "all", 0))
	if len(s.log) != len(llm.FakeKeywords) {
		t.Fatalf("all sent %d messages, want %d", len(s.log), len(llm.FakeKeywords))
	}
	for i, kw := range llm.FakeKeywords {
		if !strings.HasPrefix(s.log[i].text, "["+kw+"] ") {
			t.Errorf("message %d = %q, want the [%s] label", i, s.log[i].text, kw)
		}
	}
	if last := s.log[len(s.log)-1].text; !strings.Contains(last, TextFailAI) || !strings.Contains(last, llm.CodeTimeout+" #") {
		t.Errorf("slow = %q, want the AI-side line with E-TIMEOUT", last)
	}

	b.Text(ctx, user, text(2, "normal tx", 0)) // 2nd of 2 this minute
	b.Text(ctx, user, text(3, "normal tx", 0))
	if m := s.last(); m.text != TextTooFast {
		t.Errorf("3rd in a minute = %+v", m)
	}
	b2, s2 := newTestBot(0)
	b2.Text(ctx, user, text(1, strings.Repeat("ก", MaxInput+1), 0))
	if s2.last().text != TextTooLong {
		t.Errorf("long = %+v", s2.last())
	}
}
