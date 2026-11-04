package chatbot

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type CardState string

const (
	CardOpen      CardState = "open"
	CardSaved     CardState = "saved"
	CardCancelled CardState = "cancelled"
)

// Card is one bot message with buttons and the items it stands for.
type Card struct {
	ID        string
	UserID    uuid.UUID
	ChatID    int64
	MessageID int64
	Items     []Item
	State     CardState
	Label     string // "[keyword] " in the fake's all mode
}

// memCards keeps cards in memory (the fake mode: nothing is written to the database). A restart
// forgets them; a tap on a forgotten card reads as already handled.
type memCards struct {
	mu    sync.Mutex
	byID  map[string]*Card
	byMsg map[[2]int64]string
	order []string
	max   int
}

func newMemCards(max int) *memCards {
	return &memCards{byID: map[string]*Card{}, byMsg: map[[2]int64]string{}, max: max}
}

func newCardID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *memCards) put(c *Card) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[c.ID]; !ok {
		if len(s.order) >= s.max {
			old := s.byID[s.order[0]]
			delete(s.byMsg, [2]int64{old.ChatID, old.MessageID})
			delete(s.byID, s.order[0])
			s.order = s.order[1:]
		}
		s.order = append(s.order, c.ID)
	}
	cp := *c
	s.byID[c.ID] = &cp
	s.byMsg[[2]int64{c.ChatID, c.MessageID}] = c.ID
}

func (s *memCards) get(id string) (Card, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.byID[id]
	if !ok {
		return Card{}, false
	}
	return *c, true
}

func (s *memCards) byMessage(chatID, messageID int64) (Card, bool) {
	s.mu.Lock()
	id, ok := s.byMsg[[2]int64{chatID, messageID}]
	s.mu.Unlock()
	if !ok {
		return Card{}, false
	}
	return s.get(id)
}

// close moves an open card to state; false = it was not open (a double tap).
func (s *memCards) close(id string, state CardState) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.byID[id]
	if !ok || c.State != CardOpen {
		return false
	}
	c.State = state
	return true
}

// cardText renders the card (brief "Card, texts and errors"): a head line, then each item with
// its category and wallet. Shared wallet / date go into the head line.
func cardText(c Card, lists Lists, today time.Time) string {
	multi := len(c.Items) > 1
	head := []string{fmt.Sprintf("%s%s %d รายการ", c.Label, emojiCard, len(c.Items))}
	wallet, oneWallet := sharedWallet(c.Items)
	oneWallet = oneWallet && multi
	if oneWallet {
		head = append(head, emojiWallet+" "+walletName(lists, wallet))
	}
	day, oneDay := sharedDay(c.Items)
	if oneDay {
		head = append(head, dayLabel(day, today))
	}
	var b strings.Builder
	b.WriteString(strings.Join(head, sep))
	for i, it := range c.Items {
		num, indent := "", ""
		if multi {
			num, indent = fmt.Sprintf("%d. ", i+1), "   "
		}
		title := itemName(it) + " " + signedMoney(it)
		if !oneDay {
			title += sep + dayLabel(it.Date, today)
		}
		b.WriteString("\n" + num + title)
		if it.Type == "transfer" {
			b.WriteString("\n" + indent + emojiWallet + " " + walletName(lists, it.AccountID) + " → " +
				walletName(lists, it.TransferToID))
			continue
		}
		b.WriteString("\n" + indent + categoryLine(lists, it.CategoryID))
		if !oneWallet {
			b.WriteString("\n" + indent + emojiWallet + " " + walletName(lists, it.AccountID))
		}
	}
	return b.String()
}

// sharedWallet: the wallet every item goes to; false when items differ or one is a transfer.
func sharedWallet(items []Item) (*uuid.UUID, bool) {
	var w *uuid.UUID
	for i, it := range items {
		if it.Type == "transfer" {
			return nil, false
		}
		if i == 0 {
			w = it.AccountID
			continue
		}
		if (w == nil) != (it.AccountID == nil) || (w != nil && *w != *it.AccountID) {
			return nil, false
		}
	}
	return w, len(items) > 0
}

func sharedDay(items []Item) (string, bool) {
	for _, it := range items[1:] {
		if it.Date != items[0].Date {
			return "", false
		}
	}
	return items[0].Date, true
}

func itemName(it Item) string {
	if it.Description != nil {
		return *it.Description
	}
	return typeLabel(it.Type)
}

// signedMoney: expense -฿60, income +฿500, transfer ฿5,000 (Telegram has no text colour).
func signedMoney(it Item) string {
	switch it.Type {
	case "expense":
		return "-฿" + money(it.Amount)
	case "income":
		return "+฿" + money(it.Amount)
	}
	return "฿" + money(it.Amount)
}

func typeLabel(t string) string {
	switch t {
	case "income":
		return labelIncome
	case "transfer":
		return labelTransfer
	}
	return labelExpense
}

func dayLabel(date string, today time.Time) string {
	d, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return date
	}
	switch d.Format(time.DateOnly) {
	case today.Format(time.DateOnly):
		return labelToday
	case today.AddDate(0, 0, -1).Format(time.DateOnly):
		return labelYday
	}
	return d.Format("2/1/2006")
}

func walletName(l Lists, id *uuid.UUID) string {
	if id != nil {
		for _, w := range l.Wallets {
			if w.ID == *id {
				return w.Name
			}
		}
	}
	return labelNone
}

// categoryLine: "🍽️ Food & Drinks › Dining / Take-away", or "🏷️ ไม่ระบุ".
func categoryLine(l Lists, id *uuid.UUID) string {
	if id != nil {
		for _, c := range l.Categories {
			if c.ID == *id {
				name := c.Name
				if c.Parent != "" {
					name = c.Parent + " › " + c.Name
				}
				return categoryEmoji(c.Icon) + " " + name
			}
		}
	}
	return emojiCategory + " " + labelNone
}

// money: 1234.5 → "1,234.50"; whole numbers without decimals.
func money(f float64) string {
	whole := int64(f)
	cents := int64((f-float64(whole))*100 + 0.5)
	if cents == 100 {
		whole, cents = whole+1, 0
	}
	s := fmt.Sprintf("%d", whole)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if cents > 0 {
		fmt.Fprintf(&b, ".%02d", cents)
	}
	return b.String()
}
