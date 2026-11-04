package chatbot

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/llm"
)

const (
	MaxInput    = 500
	maxItems    = 10
	maxDesc     = 50
	maxNote     = 200
	maxAmount   = 999_999_999_999.99
	maxOutToken = 2048 // 10 items of JSON fit with room; 1024 was cut off on 4 items while thinking was on
)

// Wallet / Category — the user's lists, sent to the model as short aliases (w1, c1 …).
type Wallet struct {
	ID   uuid.UUID
	Name string
	Main bool
}

type Category struct {
	ID     uuid.UUID
	Name   string
	Type   string // expense | income
	Parent string
	Icon   string // the app's icon id (its own, else the parent's)
}

// Lists — what the parse needs to know about the user.
type Lists struct {
	Wallets    []Wallet
	Categories []Category
}

// Item is one checked transaction, ready for a pending draft.
type Item struct {
	Type         string
	Amount       float64
	Description  *string
	Note         *string
	CategoryID   *uuid.UUID
	AccountID    *uuid.UUID
	TransferToID *uuid.UUID
	Date         string
	Unsure       []string
}

// Outcome of one parse. Items empty → Hint says why.
type Outcome struct {
	Items []Item
	Hint  string
	Fail  string // an error code when the model gave no usable answer (refused, cut off, bad JSON)
	Picks []string // the category / account values the model gave per item, as given (for the log)
	Usage llm.Usage
	Model string
}

// answer is the model's JSON (the contract in now/briefs/0.4.2-chatbot.md).
type answer struct {
	Items     []answerItem `json:"items"`
	ReplyHint *string      `json:"reply_hint"`
}

type answerItem struct {
	Type        string   `json:"type"`
	Amount      *float64 `json:"amount"`
	Description *string  `json:"description"`
	Category    *string  `json:"category"`
	Account     *string  `json:"account"`
	TransferTo  *string  `json:"transfer_to"`
	Date        *string  `json:"date"`
	Note        *string  `json:"note"`
	Unsure      []string `json:"unsure"`
}

var unsureFields = map[string]bool{"type": true, "amount": true, "category": true, "account": true,
	"transfer_to": true, "date": true, "description": true}

// ErrProvider wraps a failed model call (down, timeout, no key).
type ErrProvider struct{ Err error }

func (e ErrProvider) Error() string { return "chatbot: llm: " + e.Err.Error() }
func (e ErrProvider) Unwrap() error { return e.Err }

// Parse turns text (and, for a Reply edit, the card's current items) into checked items.
// Nothing the model says is trusted: aliases must be on the lists, description / note must be the
// user's own words, bad items are dropped.
func Parse(ctx context.Context, c llm.Client, today time.Time, lists Lists, text string, current []Item) (Outcome, error) {
	al := newAliases(lists)
	var cur []byte
	if current != nil {
		cur, _ = json.Marshal(answer{Items: al.toAnswer(current)})
	}
	res, err := c.Complete(ctx, llm.Request{
		System:    systemPrompt,
		Context:   contextPart(al),
		User:      userPart(today, text, cur),
		Schema:    answerSchema,
		MaxTokens: maxOutToken,
		Input:     text,
		Current:   cur,
	})
	if err != nil {
		return Outcome{}, ErrProvider{err}
	}
	out := Outcome{Usage: res.Usage, Model: res.Model, Hint: HintNotMoney}
	switch res.Stop {
	case llm.StopRefused:
		out.Fail = CodeRefused
		return out, nil
	case llm.StopTruncated:
		out.Fail = CodeTruncated
		return out, nil
	}
	var a answer
	if err := json.Unmarshal([]byte(res.Text), &a); err != nil {
		out.Fail = CodeBadJSON
		return out, nil
	}
	words := []string{text}
	for _, it := range current {
		if it.Description != nil {
			words = append(words, *it.Description)
		}
		if it.Note != nil {
			words = append(words, *it.Note)
		}
	}
	for _, ai := range a.Items {
		if len(out.Items) == maxItems {
			break
		}
		pick := "category=null"
		if ai.Category != nil {
			pick = "category=" + *ai.Category
		}
		if ai.Account != nil {
			pick += " account=" + *ai.Account
		} else {
			pick += " account=null"
		}
		out.Picks = append(out.Picks, pick)
		if it, ok := al.check(ai, today, words); ok {
			out.Items = append(out.Items, it)
		}
	}
	if len(out.Items) == 0 && a.ReplyHint != nil && *a.ReplyHint != HintUnknown {
		out.Hint = *a.ReplyHint
	}
	return out, nil
}

type aliases struct {
	lists    Lists
	wallet   map[string]Wallet
	category map[string]Category
	main     *Wallet
}

func newAliases(l Lists) *aliases {
	a := &aliases{lists: l, wallet: map[string]Wallet{}, category: map[string]Category{}}
	for i, w := range l.Wallets {
		a.wallet[fmt.Sprintf("w%d", i+1)] = w
		if w.Main && a.main == nil {
			w := w
			a.main = &w
		}
	}
	for i, c := range l.Categories {
		a.category[fmt.Sprintf("c%d", i+1)] = c
	}
	return a
}

func (a *aliases) walletAlias(id uuid.UUID) *string {
	for i, w := range a.lists.Wallets {
		if w.ID == id {
			s := fmt.Sprintf("w%d", i+1)
			return &s
		}
	}
	return nil
}

func (a *aliases) categoryAlias(id uuid.UUID) *string {
	for i, c := range a.lists.Categories {
		if c.ID == id {
			s := fmt.Sprintf("c%d", i+1)
			return &s
		}
	}
	return nil
}

func (a *aliases) toAnswer(items []Item) []answerItem {
	out := make([]answerItem, 0, len(items))
	for _, it := range items {
		amt, date := it.Amount, it.Date
		ai := answerItem{Type: it.Type, Amount: &amt, Description: it.Description, Note: it.Note, Date: &date,
			Unsure: append([]string{}, it.Unsure...)}
		if it.CategoryID != nil {
			ai.Category = a.categoryAlias(*it.CategoryID)
		}
		if it.AccountID != nil {
			ai.Account = a.walletAlias(*it.AccountID)
		}
		if it.TransferToID != nil {
			ai.TransferTo = a.walletAlias(*it.TransferToID)
		}
		out = append(out, ai)
	}
	return out
}

func (a *aliases) check(ai answerItem, today time.Time, words []string) (Item, bool) {
	if ai.Amount == nil || math.IsNaN(*ai.Amount) || *ai.Amount <= 0 || *ai.Amount > maxAmount {
		return Item{}, false
	}
	it := Item{Type: ai.Type, Amount: math.Round(*ai.Amount*100) / 100, Unsure: []string{}}
	for _, f := range ai.Unsure {
		if unsureFields[f] && !contains(it.Unsure, f) {
			it.Unsure = append(it.Unsure, f)
		}
	}
	switch ai.Type {
	case "expense", "income":
		if ai.Category != nil {
			if c, ok := a.categoryOf(*ai.Category, ai.Type); ok {
				it.CategoryID = &c.ID
			} else if !contains(it.Unsure, "category") {
				it.Unsure = append(it.Unsure, "category")
			}
		}
		if w := a.walletOf(ai.Account); w != nil {
			it.AccountID = w
		} else if a.main != nil {
			it.AccountID = &a.main.ID
		}
	case "transfer":
		from, to := a.walletOf(ai.Account), a.walletOf(ai.TransferTo)
		if from == nil || to == nil || *from == *to {
			return Item{}, false
		}
		it.AccountID, it.TransferToID = from, to
	default:
		return Item{}, false
	}
	it.Description = verbatim(ai.Description, maxDesc, words)
	it.Note = verbatim(ai.Note, maxNote, words)
	it.Date = today.Format(time.DateOnly)
	if ai.Date != nil {
		if d, err := time.Parse(time.DateOnly, *ai.Date); err == nil {
			it.Date = d.Format(time.DateOnly)
		}
	}
	return it, true
}

func (a *aliases) walletOf(alias *string) *uuid.UUID {
	if alias == nil {
		return nil
	}
	if w, ok := a.wallet[*alias]; ok {
		return &w.ID
	}
	// The model sometimes answers the name instead of the alias; a name on the list is as safe.
	for _, w := range a.lists.Wallets {
		if sameName(w.Name, *alias) {
			return &w.ID
		}
	}
	return nil
}

// categoryOf finds a category of type t by alias, else by its exact name (or "parent › name").
func (a *aliases) categoryOf(v, t string) (Category, bool) {
	if c, ok := a.category[v]; ok && c.Type == t {
		return c, true
	}
	for _, c := range a.lists.Categories {
		if c.Type == t && (sameName(c.Name, v) || (c.Parent != "" && sameName(c.Parent+" › "+c.Name, v))) {
			return c, true
		}
	}
	return Category{}, false
}

func sameName(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// verbatim keeps s only when it is the user's own words (a substring of one of them) and short
// enough.
func verbatim(s *string, max int, words []string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	if t == "" || utf8.RuneCountInString(t) > max {
		return nil
	}
	for _, w := range words {
		if strings.Contains(w, t) {
			return &t
		}
	}
	return nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
