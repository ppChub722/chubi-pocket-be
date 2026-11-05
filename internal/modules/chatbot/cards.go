package chatbot

import (
	"crypto/rand"
	"encoding/hex"
	"sync"

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
	SourceID  int64  // the user's message the card answers (its reaction follows the card)
	Lang      string // the language the card is written in (telegram.Lang)
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

// repoint stores c, now shown as a new message: a Reply on the old message no longer finds it.
func (s *memCards) repoint(c *Card, oldMessageID int64) {
	s.put(c)
	s.mu.Lock()
	defer s.mu.Unlock()
	if oldMessageID != c.MessageID {
		delete(s.byMsg, [2]int64{c.ChatID, oldMessageID})
	}
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

// reopen puts a closed card back to open (a ✓ whose booking failed, so the user can try again).
func (s *memCards) reopen(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.byID[id]; ok {
		c.State = CardOpen
	}
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
