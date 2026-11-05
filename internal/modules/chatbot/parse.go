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
	maxName     = 100
	maxAmount   = 999_999_999_999.99
	maxOutToken = 4096 // each item now carries the whole draft; 10 items of JSON fit with room
)

// Wallet / Category / Goal / Contact / Event / Rule — the user's lists, sent to the model as short aliases
// (w1, c1, g1, p1, e1 + m1 for an event's members, r1).
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

type Goal struct {
	ID   uuid.UUID
	Name string
}

type Contact struct {
	ID     uuid.UUID
	Name   string
	Linked bool // the contact is a connected user: a bill is announced to them
}

type Member struct {
	ID   uuid.UUID
	Name string
	Me   bool
}

type Event struct {
	ID        uuid.UUID
	Name      string
	Completed bool
	Members   []Member
}

// Rule is a recurring rule a payment can be tied to. Slot is the slot a payment would take now (ISO date),
// "" when there is none; the model never sees it.
type Rule struct {
	ID          uuid.UUID
	Name        string
	Type        string // expense | income
	Amount      float64
	AccountID   *uuid.UUID
	CategoryID  *uuid.UUID
	Description *string
	Slot        string
}

// Lists — what the parse needs to know about the user.
type Lists struct {
	Wallets    []Wallet
	Categories []Category
	Goals      []Goal
	Contacts   []Contact
	Events     []Event
	Rules      []Rule
}

// Item kinds: the pending row each one becomes.
const (
	KindCreate = "create" // a transaction (expense, income, transfer; maybe a bill or tied to a recurring rule)
	KindSaving = "saving" // a saving entry
	KindDebt   = "debt"   // a person-book entry or a repayment
)

// Saving actions (the pending Draft's saving_action).
const (
	ActSave     = "save"
	ActAllocate = "allocate"
	ActMove     = "move"
	ActWithdraw = "withdraw"
)

// Debt sides (the pending Draft's debt.side).
const (
	SideTheyOweMe = "they_owe_me"
	SideIOweThem  = "i_owe_them"
	SideTheyRepay = "they_repay"
	SideIRepay    = "i_repay"
)

// PersonRef is someone on a bill: an event member, a contact, or only a typed name.
type PersonRef struct {
	MemberID  *uuid.UUID
	ContactID *uuid.UUID
	Name      string
}

// PaperItem is a shared bill: an event's bill (EventID, or NewEvent to make it) or a split bill. People are the
// others on it (I am always in); PaidBy nil = I paid.
type PaperItem struct {
	EventID   *uuid.UUID
	NewEvent  string
	SplitMode string
	People    []PersonRef
	PaidBy    *PersonRef
}

// DebtItem is a debt row; Side is one of the Side* constants.
type DebtItem struct {
	ContactID *uuid.UUID
	Name      string
	Side      string
	WithMoney bool
}

// Item is one checked row, ready for a pending draft (the model's answer after the checks).
type Item struct {
	Kind         string
	Type         string // expense | income | transfer | saving ("" for a debt)
	Amount       float64
	Description  *string
	Note         *string
	CategoryID   *uuid.UUID
	AccountID    *uuid.UUID
	TransferToID *uuid.UUID
	Date         string
	Unsure       []string // fields the model guessed (shown as "[value ?]"); a bad category alias too

	SavingAction string
	GoalID       *uuid.UUID // where a saving goes (nil = ยังไม่จัดสรร)
	FromGoalID   *uuid.UUID // where a move / withdrawal comes from (nil = ยังไม่จัดสรร)
	ToSpare      bool       // a move that ends in ยังไม่จัดสรร

	RecurringID   *uuid.UUID
	RecurringName string
	Slot          string

	Paper *PaperItem
	Debt  *DebtItem
}

// Outcome of one parse. Items empty → Hint says why.
type Outcome struct {
	Items []Item
	Hint  string
	Fail  string   // an error code when the model gave no usable answer (refused, cut off, bad JSON)
	Picks []string // the aliases the model gave per item, as given (for the log)
	Sent  string   // the per-user request (lists + date + message), for LogBodies
	Raw   string   // the model's answer as it came, for LogBodies
	Usage llm.Usage
	Model string
}

// answer is the model's JSON (the contract in now/briefs/0.4.2-chatbot.md): every item is a pending row, kind + draft.
type answer struct {
	Items     []answerItem `json:"items"`
	ReplyHint *string      `json:"reply_hint"`
}

type answerItem struct {
	Kind   string      `json:"kind"`
	Draft  answerDraft `json:"draft"`
	Unsure []string    `json:"unsure"`
}

type answerDraft struct {
	Type                *string      `json:"type"`
	Amount              *float64     `json:"amount"`
	Description         *string      `json:"description"`
	Note                *string      `json:"note"`
	Date                *string      `json:"date"`
	CategoryID          *string      `json:"category_id"`
	AccountID           *string      `json:"account_id"`
	TransferToAccountID *string      `json:"transfer_to_account_id"`
	GoalID              *string      `json:"goal_id"`
	FromGoalID          *string      `json:"from_goal_id"`
	SavingAction        *string      `json:"saving_action"`
	RecurringID         *string      `json:"recurring_id"`
	Paper               *answerPaper `json:"paper"`
	Debt                *answerDebt  `json:"debt"`
}

type answerPaper struct {
	ProjectID *string         `json:"project_id"`
	NewEvent  *answerNewEvent `json:"new_event"`
	SplitMode *string         `json:"split_mode"`
	People    []answerPerson  `json:"people"`
	PaidBy    *answerPerson   `json:"paid_by"`
	Exclude   []string        `json:"exclude"`
}

type answerNewEvent struct {
	Name string `json:"name"`
}

type answerPerson struct {
	MemberID   *string `json:"member_id"`
	ContactID  *string `json:"contact_id"`
	PersonName *string `json:"person_name"`
}

type answerDebt struct {
	ContactID  *string `json:"contact_id"`
	PersonName *string `json:"person_name"`
	Side       *string `json:"side"`
	WithMoney  *bool   `json:"with_money"`
}

// ErrProvider wraps a failed model call (down, timeout, no key).
type ErrProvider struct{ Err error }

func (e ErrProvider) Error() string { return "chatbot: llm: " + e.Err.Error() }
func (e ErrProvider) Unwrap() error { return e.Err }

// Parse turns text (and, for a Reply edit, the card's current items) into checked items.
// Nothing the model says is trusted: aliases must be on the lists, description / note / names must be the
// user's own words, bad items are dropped.
func Parse(ctx context.Context, c llm.Client, today time.Time, lists Lists, text string, current []Item) (Outcome, error) {
	al := newAliases(lists)
	var cur []byte
	if current != nil {
		cur, _ = json.Marshal(answer{Items: al.toAnswer(current)})
	}
	req := llm.Request{
		System:    systemPrompt,
		Context:   contextPart(al),
		User:      userPart(today, text, cur),
		Schema:    answerSchema,
		MaxTokens: maxOutToken,
		Input:     text,
		Current:   cur,
	}
	sent := req.Context + "\n" + req.User
	res, err := c.Complete(ctx, req)
	if err != nil {
		return Outcome{Sent: sent}, ErrProvider{err}
	}
	out := Outcome{Sent: sent, Raw: res.Text, Usage: res.Usage, Model: res.Model}
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
		if it.Debt != nil && it.Debt.Name != "" {
			words = append(words, it.Debt.Name)
		}
		if p := it.Paper; p != nil {
			words = append(words, p.NewEvent)
			for _, x := range p.People {
				words = append(words, x.Name)
			}
		}
	}
	dropHint := ""
	for _, ai := range a.Items {
		if len(out.Items) == maxItems {
			break
		}
		out.Picks = append(out.Picks, picksOf(ai))
		if it, why, ok := al.check(ai, today, words); ok {
			out.Items = append(out.Items, it)
		} else if why != "" && dropHint == "" {
			dropHint = why
		}
	}
	if len(out.Items) == 0 {
		if a.ReplyHint != nil && *a.ReplyHint != HintUnknown {
			out.Hint = *a.ReplyHint
		}
		// an item left out for a reason the BE knows (a transfer without a wallet, a goal not named, a
		// finished event) says so, whatever hint the model gave
		if dropHint != "" {
			out.Hint = dropHint
		}
	}
	return out, nil
}

// picksOf: what the model chose for one item (aliases), for the log; never the user's text.
func picksOf(ai answerItem) string {
	d := ai.Draft
	pick := "kind=" + ai.Kind
	if d.SavingAction != nil {
		pick += " action=" + *d.SavingAction
	}
	for _, kv := range [][2]any{{"category", d.CategoryID}, {"account", d.AccountID}, {"goal", d.GoalID}, {"from", d.FromGoalID}, {"recurring", d.RecurringID}} {
		if v := kv[1].(*string); v != nil {
			pick += " " + kv[0].(string) + "=" + *v
		}
	}
	if p := d.Paper; p != nil && p.ProjectID != nil {
		pick += " event=" + *p.ProjectID
	}
	return pick
}

type aliases struct {
	lists    Lists
	wallet   map[string]Wallet
	category map[string]Category
	goal     map[string]Goal
	contact  map[string]Contact
	event    map[string]Event
	member   map[string]Member
	rule     map[string]Rule
	main     *Wallet
}

func newAliases(l Lists) *aliases {
	a := &aliases{lists: l, wallet: map[string]Wallet{}, category: map[string]Category{}, goal: map[string]Goal{},
		contact: map[string]Contact{}, event: map[string]Event{}, member: map[string]Member{}, rule: map[string]Rule{}}
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
	for i, g := range l.Goals {
		a.goal[fmt.Sprintf("g%d", i+1)] = g
	}
	for i, c := range l.Contacts {
		a.contact[fmt.Sprintf("p%d", i+1)] = c
	}
	n := 0
	for i, e := range l.Events {
		a.event[fmt.Sprintf("e%d", i+1)] = e
		for _, m := range e.Members {
			if m.Me {
				continue
			}
			n++
			a.member[fmt.Sprintf("m%d", n)] = m
		}
	}
	for i, r := range l.Rules {
		a.rule[fmt.Sprintf("r%d", i+1)] = r
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

func (a *aliases) goalAlias(id *uuid.UUID, spare bool) *string {
	if spare {
		s := spareAlias
		return &s
	}
	if id == nil {
		return nil
	}
	for i, g := range a.lists.Goals {
		if g.ID == *id {
			s := fmt.Sprintf("g%d", i+1)
			return &s
		}
	}
	return nil
}

func (a *aliases) contactAlias(id uuid.UUID) *string {
	for i, c := range a.lists.Contacts {
		if c.ID == id {
			s := fmt.Sprintf("p%d", i+1)
			return &s
		}
	}
	return nil
}

func (a *aliases) eventAlias(id uuid.UUID) *string {
	for i, e := range a.lists.Events {
		if e.ID == id {
			s := fmt.Sprintf("e%d", i+1)
			return &s
		}
	}
	return nil
}

func (a *aliases) memberAlias(id uuid.UUID) *string {
	for k, m := range a.member {
		if m.ID == id {
			s := k
			return &s
		}
	}
	return nil
}

func (a *aliases) ruleAlias(id uuid.UUID) *string {
	for i, r := range a.lists.Rules {
		if r.ID == id {
			s := fmt.Sprintf("r%d", i+1)
			return &s
		}
	}
	return nil
}

func (a *aliases) personAnswer(p *PersonRef) *answerPerson {
	if p == nil {
		return nil
	}
	ap := &answerPerson{}
	switch {
	case p.MemberID != nil:
		ap.MemberID = a.memberAlias(*p.MemberID)
	case p.ContactID != nil:
		ap.ContactID = a.contactAlias(*p.ContactID)
	}
	if ap.MemberID == nil && ap.ContactID == nil && p.Name != "" {
		n := p.Name
		ap.PersonName = &n
	}
	return ap
}

// toAnswer is the card's items in the model's own shape (the "current card" of a Reply edit).
func (a *aliases) toAnswer(items []Item) []answerItem {
	out := make([]answerItem, 0, len(items))
	for _, it := range items {
		amt, date := it.Amount, it.Date
		d := answerDraft{Amount: &amt, Description: it.Description, Note: it.Note, Date: &date}
		if it.Type != "" {
			t := it.Type
			d.Type = &t
		}
		if it.CategoryID != nil {
			d.CategoryID = a.categoryAlias(*it.CategoryID)
		}
		if it.AccountID != nil {
			d.AccountID = a.walletAlias(*it.AccountID)
		}
		if it.TransferToID != nil {
			d.TransferToAccountID = a.walletAlias(*it.TransferToID)
		}
		switch it.Kind {
		case KindSaving:
			act := it.SavingAction
			d.SavingAction = &act
			d.GoalID = a.goalAlias(it.GoalID, it.ToSpare)
			d.FromGoalID = a.goalAlias(it.FromGoalID, false)
		case KindDebt:
			if db := it.Debt; db != nil {
				side, money := db.Side, db.WithMoney
				ad := &answerDebt{Side: &side, WithMoney: &money}
				if db.ContactID != nil {
					ad.ContactID = a.contactAlias(*db.ContactID)
				}
				if ad.ContactID == nil && db.Name != "" {
					n := db.Name
					ad.PersonName = &n
				}
				d.Debt = ad
			}
		}
		if it.RecurringID != nil {
			d.RecurringID = a.ruleAlias(*it.RecurringID)
		}
		if p := it.Paper; p != nil {
			ap := &answerPaper{PaidBy: a.personAnswer(p.PaidBy), People: []answerPerson{}, Exclude: []string{}}
			if p.EventID != nil {
				ap.ProjectID = a.eventAlias(*p.EventID)
			}
			if p.NewEvent != "" {
				ap.NewEvent = &answerNewEvent{Name: p.NewEvent}
			}
			if p.SplitMode != "" {
				m := p.SplitMode
				ap.SplitMode = &m
			}
			for i := range p.People {
				ap.People = append(ap.People, *a.personAnswer(&p.People[i]))
			}
			d.Paper = ap
		}
		out = append(out, answerItem{Kind: it.Kind, Draft: d, Unsure: append([]string{}, it.Unsure...)})
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func validAmount(f *float64) bool {
	return f != nil && !math.IsNaN(*f) && *f > 0 && *f <= maxAmount
}

// check validates one item. ok false = left out; why is a hint for the user when the BE knows the reason.
func (a *aliases) check(ai answerItem, today time.Time, words []string) (Item, string, bool) {
	d := ai.Draft
	it := Item{Unsure: []string{}}
	for _, f := range ai.Unsure {
		if contains(unsureFields, f) && !contains(it.Unsure, f) {
			it.Unsure = append(it.Unsure, f)
		}
	}
	it.Description = verbatim(d.Description, maxDesc, words)
	it.Note = verbatim(d.Note, maxNote, words)
	it.Date = today.Format(time.DateOnly)
	if d.Date != nil {
		if t, err := time.Parse(time.DateOnly, *d.Date); err == nil {
			it.Date = t.Format(time.DateOnly)
		}
	}
	switch ai.Kind {
	case KindSaving:
		return a.checkSaving(it, d)
	case KindDebt, "settle_debt":
		return a.checkDebt(it, d, words)
	case KindCreate, "":
		return a.checkCreate(it, d, words)
	}
	return Item{}, "", false
}

func (a *aliases) checkCreate(it Item, d answerDraft, words []string) (Item, string, bool) {
	typ := deref(d.Type)
	it.Kind, it.Type = KindCreate, typ
	var rule *Rule
	if d.RecurringID != nil && (typ == "expense" || typ == "income") {
		if r, ok := a.ruleOf(*d.RecurringID); ok && r.Slot != "" {
			rule = &r
		}
	}
	switch {
	case validAmount(d.Amount):
		it.Amount = math.Round(*d.Amount*100) / 100
	case rule != nil && rule.Amount > 0:
		it.Amount = rule.Amount // "จ่ายค่าไฟ": the rule's amount
	default:
		return Item{}, "", false
	}
	switch typ {
	case "expense", "income":
		if d.CategoryID != nil {
			if c, ok := a.categoryOf(*d.CategoryID, typ); ok {
				it.CategoryID = &c.ID
			} else if !contains(it.Unsure, "category") {
				it.Unsure = append(it.Unsure, "category")
			}
		}
		if w := a.walletOf(d.AccountID); w != nil {
			it.AccountID = w
		} else if rule != nil && rule.AccountID != nil {
			it.AccountID = rule.AccountID
		} else if a.main != nil {
			it.AccountID = &a.main.ID
		}
		if rule != nil {
			it.RecurringID, it.RecurringName, it.Slot = &rule.ID, rule.Name, rule.Slot
			if rule.Type == "expense" || rule.Type == "income" {
				it.Type = rule.Type
			}
			if it.CategoryID == nil && rule.CategoryID != nil {
				it.CategoryID = rule.CategoryID
			}
			if it.Description == nil {
				name := rule.Name
				it.Description = &name
			}
		}
		if p, why := a.checkPaper(d.Paper, typ, words); why != "" {
			return Item{}, why, false
		} else if p != nil {
			it.Paper = p
		}
	case "transfer":
		from, to := a.walletOf(d.AccountID), a.walletOf(d.TransferToAccountID)
		if (from == nil && to == nil) || (from != nil && to != nil && *from == *to) {
			return Item{}, HintTransferWallet, false
		}
		it.AccountID, it.TransferToID = from, to
	default:
		return Item{}, "", false
	}
	return it, "", true
}

// goalRef is how an alias (or a name) points at a goal: the goal, ยังไม่จัดสรร, or nothing known.
type goalRef struct {
	id    *uuid.UUID
	spare bool
	named bool // the model named something (even if it is not on the list)
}

func (a *aliases) goalOf(v *string) goalRef {
	if v == nil || strings.TrimSpace(*v) == "" {
		return goalRef{}
	}
	s := strings.TrimSpace(*v)
	if strings.EqualFold(s, spareAlias) || sameName(s, "ยังไม่จัดสรร") || sameName(s, "unallocated") {
		return goalRef{spare: true, named: true}
	}
	if g, ok := a.goal[s]; ok {
		return goalRef{id: &g.ID, named: true}
	}
	for _, g := range a.lists.Goals {
		if sameName(g.Name, s) {
			id := g.ID
			return goalRef{id: &id, named: true}
		}
	}
	return goalRef{named: true}
}

func (a *aliases) checkSaving(it Item, d answerDraft) (Item, string, bool) {
	if !validAmount(d.Amount) {
		return Item{}, "", false
	}
	it.Kind, it.Type = KindSaving, "saving"
	it.Amount = math.Round(*d.Amount*100) / 100
	act := deref(d.SavingAction)
	goal, from := a.goalOf(d.GoalID), a.goalOf(d.FromGoalID)
	wallet := a.walletOf(d.AccountID)
	if wallet == nil && len(a.lists.Wallets) == 1 { // one wallet: no need to ask which
		wallet = &a.lists.Wallets[0].ID
	}
	it.SavingAction = act
	switch act {
	case ActSave:
		it.AccountID, it.GoalID = wallet, goal.id // no goal = ยังไม่จัดสรร; the model's own "unsure goal" stays (goal_id null does not say whether one was named)
		if goal.named && goal.id == nil && !goal.spare && !contains(it.Unsure, "goal") {
			it.Unsure = append(it.Unsure, "goal")
		}
	case ActAllocate:
		if goal.id == nil {
			return Item{}, HintGoalMissing, false
		}
		it.GoalID = goal.id
	case ActMove:
		if from.id == nil || (goal.id == nil && !goal.spare) || (goal.id != nil && *goal.id == *from.id) {
			return Item{}, HintGoalMissing, false
		}
		it.FromGoalID, it.GoalID, it.ToSpare = from.id, goal.id, goal.spare
	case ActWithdraw:
		it.AccountID, it.FromGoalID = wallet, from.id // no goal = from ยังไม่จัดสรร
	default:
		return Item{}, "", false
	}
	return it, "", true
}

func (a *aliases) checkDebt(it Item, d answerDraft, words []string) (Item, string, bool) {
	if !validAmount(d.Amount) || d.Debt == nil {
		return Item{}, "", false
	}
	side := deref(d.Debt.Side)
	if side != SideTheyOweMe && side != SideIOweThem && side != SideTheyRepay && side != SideIRepay {
		return Item{}, "", false
	}
	it.Kind, it.Type = KindDebt, ""
	it.Description = nil // the row says what it is; the model tends to copy the whole message here
	it.Amount = math.Round(*d.Amount*100) / 100
	db := &DebtItem{Side: side, WithMoney: true}
	if d.Debt.WithMoney != nil {
		db.WithMoney = *d.Debt.WithMoney
	}
	if d.Debt.ContactID != nil {
		if c, ok := a.contactOf(*d.Debt.ContactID); ok {
			id := c.ID
			db.ContactID, db.Name = &id, c.Name
		}
	}
	if db.ContactID == nil {
		if n := verbatim(d.Debt.PersonName, maxName, words); n != nil {
			db.Name = *n
			if c, ok := a.contactOf(*n); ok { // a known contact the model gave by name
				id := c.ID
				db.ContactID, db.Name = &id, c.Name
			}
		}
	}
	if db.WithMoney {
		if w := a.walletOf(d.AccountID); w != nil {
			it.AccountID = w
		} else if a.main != nil {
			it.AccountID = &a.main.ID
		}
	}
	it.Debt = db
	return it, "", true
}

// newEventName: only a name that says it is an event makes a new one ("ทริปภูเก็ต", "อีเวนต์งานแต่ง", "trip …");
// any other unmatched name is just a word on a plain expense.
func newEventName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, p := range []string{"ทริป", "อีเวนต์", "อีเว้นท์", "event", "trip"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// checkPaper turns the model's paper into a shared bill; nil = a plain transaction. why is set when the user
// must hear about it (a finished event).
func (a *aliases) checkPaper(p *answerPaper, typ string, words []string) (*PaperItem, string) {
	if p == nil {
		return nil, ""
	}
	if p.ProjectID == nil && p.NewEvent == nil && len(p.People) == 0 && p.PaidBy == nil && p.SplitMode == nil {
		return nil, ""
	}
	out := &PaperItem{SplitMode: "equal"}
	var event *Event
	switch {
	case p.ProjectID != nil:
		if e, ok := a.eventOf(*p.ProjectID); ok {
			if e.Completed {
				return nil, HintEventDone
			}
			event = &e
			out.EventID = &e.ID
		}
	case p.NewEvent != nil:
		if name := verbatim(&p.NewEvent.Name, maxName, words); name != nil && newEventName(*name) {
			out.NewEvent = *name
		}
	}
	if p.ProjectID != nil && event == nil && p.NewEvent == nil { // an event that is not on the list: not an event bill
		return nil, ""
	}
	if p.ProjectID == nil && p.NewEvent != nil && out.NewEvent == "" { // a new event by a name that is not one
		return nil, ""
	}
	excluded := map[uuid.UUID]bool{}
	for _, m := range p.Exclude {
		if mem, ok := a.member[strings.TrimSpace(m)]; ok {
			excluded[mem.ID] = true
		}
	}
	for i := range p.People {
		if ref, ok := a.personOf(&p.People[i], event, words); ok && !hasPerson(out.People, ref) && !excluded[derefID(ref.MemberID)] {
			out.People = append(out.People, ref)
		}
	}
	if event != nil && len(p.People) == 0 { // an event bill is shared by every member unless some are left out
		for _, m := range event.Members {
			if m.Me || excluded[m.ID] {
				continue
			}
			id := m.ID
			out.People = append(out.People, PersonRef{MemberID: &id, Name: m.Name})
		}
	}
	if p.PaidBy != nil {
		if ref, ok := a.personOf(p.PaidBy, event, words); ok {
			out.PaidBy = &ref
			if !hasPerson(out.People, ref) {
				out.People = append(out.People, ref)
			}
		}
	}
	return out, ""
}

func derefID(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}

func hasPerson(xs []PersonRef, p PersonRef) bool {
	for _, x := range xs {
		switch {
		case p.MemberID != nil && x.MemberID != nil && *p.MemberID == *x.MemberID:
			return true
		case p.ContactID != nil && x.ContactID != nil && *p.ContactID == *x.ContactID:
			return true
		case p.MemberID == nil && p.ContactID == nil && x.MemberID == nil && x.ContactID == nil && sameName(p.Name, x.Name):
			return true
		}
	}
	return false
}

// personOf resolves one person of the answer: an event member (of this event), a contact, or a typed name.
func (a *aliases) personOf(p *answerPerson, event *Event, words []string) (PersonRef, bool) {
	if p.MemberID != nil && event != nil {
		if m, ok := a.member[strings.TrimSpace(*p.MemberID)]; ok {
			for _, em := range event.Members {
				if em.ID == m.ID {
					id := m.ID
					return PersonRef{MemberID: &id, Name: m.Name}, true
				}
			}
		}
	}
	if p.ContactID != nil {
		if c, ok := a.contactOf(*p.ContactID); ok {
			id := c.ID
			return PersonRef{ContactID: &id, Name: c.Name}, true
		}
	}
	if n := verbatim(p.PersonName, maxName, words); n != nil {
		if c, ok := a.contactOf(*n); ok {
			id := c.ID
			return PersonRef{ContactID: &id, Name: c.Name}, true
		}
		return PersonRef{Name: *n}, true
	}
	return PersonRef{}, false
}

func (a *aliases) contactOf(v string) (Contact, bool) {
	v = strings.TrimSpace(v)
	if c, ok := a.contact[v]; ok {
		return c, true
	}
	for _, c := range a.lists.Contacts {
		if sameName(c.Name, v) {
			return c, true
		}
	}
	return Contact{}, false
}

func (a *aliases) eventOf(v string) (Event, bool) {
	v = strings.TrimSpace(v)
	if e, ok := a.event[v]; ok {
		return e, true
	}
	for _, e := range a.lists.Events {
		if sameName(e.Name, v) {
			return e, true
		}
	}
	return Event{}, false
}

func (a *aliases) ruleOf(v string) (Rule, bool) {
	v = strings.TrimSpace(v)
	if r, ok := a.rule[v]; ok {
		return r, true
	}
	for _, r := range a.lists.Rules {
		if sameName(r.Name, v) {
			return r, true
		}
	}
	return Rule{}, false
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
