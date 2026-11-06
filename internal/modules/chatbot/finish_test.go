package chatbot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/telegram"
)

// The HTML card: bold head and titles, the user's words escaped; the plain card is the same words.
func TestCardHTML(t *testing.T) {
	desc := "a <b> & c"
	c := Card{Lang: "th", Items: []Item{{Type: "expense", Amount: 60, Date: "2026-11-01", Description: &desc}}}
	got := cardHTML(c, testLists, testDay)
	if !strings.HasPrefix(got, "<b>🧾 1 รายการ") || !strings.Contains(got, "<b>") || strings.Contains(got, "a <b> & c") ||
		!strings.Contains(got, "a &lt;b&gt; &amp; c") {
		t.Errorf("card html = %s", got)
	}
	if plain(got) != cardText(c, testLists, testDay) {
		t.Errorf("html card read back = %q, plain = %q", plain(got), cardText(c, testLists, testDay))
	}
}

// The amounts follow the user's currency.
func TestCardCurrency(t *testing.T) {
	l := testLists
	l.Currency = "USD"
	c := Card{Lang: "en", Items: []Item{{Type: "expense", Amount: 60, Date: "2026-11-01"}}}
	if got := cardText(c, l, testDay); !strings.Contains(got, "-$60") {
		t.Errorf("USD card = %s", got)
	}
	l.Currency = "CHF"
	if got := cardText(c, l, testDay); !strings.Contains(got, "-CHF 60") {
		t.Errorf("CHF card = %s", got)
	}
	if got := cardText(c, testLists, testDay); !strings.Contains(got, "-฿60") {
		t.Errorf("default card = %s", got)
	}
}

// An example in a fixed line goes in monospace; nothing else is marked up.
func TestHTMLFixed(t *testing.T) {
	if got := htmlFixed(TextNoAmount); !strings.HasSuffix(got, "เช่น <code>ข้าว 60</code>") {
		t.Errorf("htmlFixed = %s", got)
	}
	if got := htmlFixed(textsEN.TransferWallet); !strings.HasSuffix(got, "e.g. <code>withdraw 500 from Kbank</code>.") {
		t.Errorf("htmlFixed en = %s", got)
	}
	if got := htmlFixed("a < b"); got != "a &lt; b" {
		t.Errorf("htmlFixed escape = %s", got)
	}
}

// ✏️ opens a force reply; what the user sends there edits the card.
func TestBotEditButton(t *testing.T) {
	b, s := newTestBot(0)
	user := uuid.New()
	ctx := context.Background()
	b.Text(ctx, user, text(1, "normal tx", 0))
	card := s.last()
	if len(card.buttons) != 3 || card.buttons[0].Style != "success" || card.buttons[1].Style != "danger" {
		t.Fatalf("buttons = %+v", card.buttons)
	}
	b.Tap(ctx, tap(card.buttons[2].Data, card.msgID, chat))
	prompt := s.last()
	if prompt.kind != "force" || !strings.HasPrefix(prompt.text, textsTH.EditPrompt) {
		t.Fatalf("prompt = %+v", prompt)
	}
	b.Text(ctx, user, text(2, "edit", prompt.msgID))
	if n := s.log[len(s.log)-2]; n.kind != "card" || !strings.Contains(n.text, "-฿50") {
		t.Fatalf("edited card = %+v", n)
	}
	// the old card is finished: a tap on its ✏️ says so
	b.Tap(ctx, tap(card.buttons[2].Data, card.msgID, chat))
	if a := s.last(); a.kind != "answer" || a.text != TextHandled {
		t.Fatalf("✏️ on a finished card = %+v", a)
	}
	// the grey button of a finished card only answers
	b.Tap(ctx, tap(cbDone, card.msgID, chat))
	if a := s.last(); a.kind != "answer" || a.text != textsEN.Handled { // the tap has no Telegram language
		t.Fatalf("grey button = %+v", a)
	}
}

// A press of the stop button ends the model call; the bot says it stopped and nothing is booked.
func TestBotStop(t *testing.T) {
	b, s := newTestBot(0)
	done := make(chan struct{})
	go func() {
		b.Text(context.Background(), uuid.New(), text(9, "hang", 0))
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		b.calls.mu.Lock()
		n := len(b.calls.m)
		b.calls.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the call never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	b.Stop(chat, 9)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the call did not stop")
	}
	if m := s.last(); m.kind != "msg" || m.text != textsTH.Stopped {
		t.Fatalf("after stop = %+v", m)
	}
	for _, e := range s.log {
		if e.kind == "card" {
			t.Fatalf("a card was sent after stop: %+v", e)
		}
	}
}

// A Reply on a card the bot no longer holds is not a new message: it is told the card is finished.
func TestBotReplyOnForgottenCard(t *testing.T) {
	b, s := newTestBot(0)
	user := uuid.New()
	ctx := context.Background()
	b.Text(ctx, user, text(1, "normal tx", 0))
	card := s.last()
	b.Text(ctx, user, text(2, "forget cards", 0))
	n := len(s.log)
	m := text(3, "edit", 0)
	m.ReplyToMessage = &telegram.Message{MessageID: card.msgID, Text: card.text, From: &telegram.User{IsBot: true}}
	b.Text(ctx, user, m)
	if len(s.log) != n+1 || s.last().text != textsTH.ReplyClosed {
		t.Fatalf("reply on a forgotten card = %+v", s.log[n:])
	}
	// a Reply on some other message of the bot stays a new message
	m = text(4, "normal tx", 0)
	m.ReplyToMessage = &telegram.Message{MessageID: 77, Text: "hello", From: &telegram.User{IsBot: true}}
	b.Text(ctx, user, m)
	if s.last().kind != "card" {
		t.Fatalf("reply on a plain bot message = %+v", s.last())
	}
}
