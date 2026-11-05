package chatbot

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// cardText renders the card (brief "Card, texts and errors") in the card's language: a head line,
// then each item with its lines, a line between parts. Shared wallet / date go into the head line.
// A value the model guessed (unsure) shows as "[value ?]" (owner 2026-11-04).
func cardText(c Card, lists Lists, today time.Time) string {
	t := textsFor(c.Lang)
	multi := len(c.Items) > 1
	head := []string{c.Label + emojiCard + " " + fmt.Sprintf(t.Items, len(c.Items))}
	wallet, oneWallet := sharedWallet(c.Items)
	oneWallet = oneWallet && multi
	if oneWallet {
		head = append(head, emojiWallet+" "+unsureMark(walletName(t, lists, wallet), c.Items[0], "account"))
	}
	oneDay := sharedDay(c.Items)
	if oneDay {
		head = append(head, unsureMark(dayLabel(t, c.Items[0].Date, today), c.Items[0], "date"))
	}
	var b strings.Builder
	b.WriteString(strings.Join(head, sep))
	for i, it := range c.Items {
		num, indent := "", ""
		if multi {
			num, indent = fmt.Sprintf("%d. ", i+1), "   "
		}
		title := itemTitle(t, it)
		if !oneDay {
			title += sep + unsureMark(dayLabel(t, it.Date, today), it, "date")
		}
		b.WriteString("\n" + separator + "\n" + num + title)
		for _, line := range itemLines(t, lists, it, oneWallet) {
			b.WriteString("\n" + indent + line)
		}
	}
	return b.String()
}

// itemTitle: the name and the signed amount of one item.
func itemTitle(t Texts, it Item) string {
	switch it.Kind {
	case KindSaving:
		name := t.Card.Save
		switch it.SavingAction {
		case ActAllocate:
			name = t.Card.Allocate
		case ActMove:
			name = t.Card.Move
		case ActWithdraw:
			name = t.Card.Withdraw
		}
		return name + " ฿" + money(it.Amount)
	case KindDebt:
		return debtTitle(t, it)
	}
	money := signedMoney(it)
	if contains(it.Unsure, "amount") || contains(it.Unsure, "type") {
		money = "[" + money + " ?]"
	}
	return itemName(t, it) + " " + money
}

func debtName(t Texts, db *DebtItem) string {
	if db == nil || db.Name == "" {
		return t.Card.NobodyYet
	}
	return db.Name
}

// debtTitle: "ให้บอยยืม -฿500" (money moves) or "บอยค้างฉัน ฿500" (only noted).
func debtTitle(t Texts, it Item) string {
	db := it.Debt
	if db == nil {
		return t.Card.Debt
	}
	n, amt := debtName(t, db), money(it.Amount)
	var title, sign string
	switch db.Side {
	case SideTheyOweMe:
		title, sign = fmt.Sprintf(t.Card.Lend, n), "-"
		if !db.WithMoney {
			title = fmt.Sprintf(t.Card.OwesMeTitle, n)
		}
	case SideIOweThem:
		title, sign = fmt.Sprintf(t.Card.Borrow, n), "+"
		if !db.WithMoney {
			title = fmt.Sprintf(t.Card.IOweTitle, n)
		}
	case SideTheyRepay:
		title, sign = fmt.Sprintf(t.Card.RepayIn, n), "+"
	default:
		title, sign = fmt.Sprintf(t.Card.RepayOut, n), "-"
	}
	if !db.WithMoney {
		sign = ""
	}
	return title + " " + sign + "฿" + amt
}

func goalName(t Texts, l Lists, id *uuid.UUID) string {
	if id != nil {
		for _, g := range l.Goals {
			if g.ID == *id {
				return g.Name
			}
		}
	}
	return t.Card.Spare
}

// itemLines: the lines under an item's title.
func itemLines(t Texts, l Lists, it Item, oneWallet bool) []string {
	wallet := func() string {
		return t.Wallet + emojiWallet + " " + unsureMark(walletName(t, l, it.AccountID), it, "account")
	}
	switch it.Kind {
	case KindSaving:
		goal := unsureMark(goalName(t, l, it.GoalID), it, "goal")
		switch it.SavingAction {
		case ActAllocate:
			return []string{"🐷 " + t.Card.Spare + " → " + goal}
		case ActMove:
			to := goal
			if it.ToSpare {
				to = t.Card.Spare
			}
			return []string{"🐷 " + goalName(t, l, it.FromGoalID) + " → " + to}
		case ActWithdraw:
			return []string{t.Card.Goal + goalName(t, l, it.FromGoalID),
				t.Card.BackTo + unsureMark(walletName(t, l, it.AccountID), it, "account")}
		}
		return []string{t.Card.Goal + goal, wallet()}
	case KindDebt:
		return debtLines(t, l, it)
	}
	if it.Type == "transfer" {
		return []string{t.Wallet + emojiWallet + " " + transferWallets(t, l, it)}
	}
	var lines []string
	if it.RecurringID != nil {
		lines = append(lines, t.Card.Recurring+it.RecurringName+" · "+slotLabel(t, it.Slot))
	}
	if it.Paper != nil {
		lines = append(lines, paperLines(t, l, it)...)
		return append(lines, t.Category+unsureMark(categoryLine(t, l, it.CategoryID), it, "category"))
	}
	lines = append(lines, t.Category+unsureMark(categoryLine(t, l, it.CategoryID), it, "category"))
	if !oneWallet {
		lines = append(lines, wallet())
	}
	return lines
}

func slotLabel(t Texts, iso string) string {
	d, err := time.Parse(time.DateOnly, iso)
	if err != nil {
		return iso
	}
	return fmt.Sprintf(t.Card.Slot, fmt.Sprintf("%d %s", d.Day(), t.Card.Months[d.Month()-1]))
}

// debtLines: the debt and, under it, whether money moves (named after the two pages of the app).
func debtLines(t Texts, l Lists, it Item) []string {
	db := it.Debt
	if db == nil {
		return nil
	}
	n, amt := debtName(t, db), "฿"+money(it.Amount)
	var debt string
	switch db.Side {
	case SideTheyOweMe:
		debt = fmt.Sprintf(t.Card.OwesMe, n, amt)
	case SideIOweThem:
		debt = fmt.Sprintf(t.Card.IOwe, n, amt)
	case SideTheyRepay:
		debt = fmt.Sprintf(t.Card.RepayIn, n) + " " + amt
	default:
		debt = fmt.Sprintf(t.Card.RepayOut, n) + " " + amt
	}
	out := db.Side == SideTheyOweMe || db.Side == SideIRepay // money leaves my wallet
	var moneyLine string
	wallet := unsureMark(walletName(t, l, it.AccountID), it, "account")
	switch {
	case db.WithMoney && out:
		moneyLine = fmt.Sprintf(t.Card.MoneyOut, wallet, amt)
	case db.WithMoney:
		moneyLine = fmt.Sprintf(t.Card.MoneyIn, wallet, amt)
	case out:
		moneyLine = t.Card.NoOut
	default:
		moneyLine = t.Card.NoIn
	}
	if contains(it.Unsure, "with_money") {
		moneyLine = "[" + moneyLine + " ?]"
	}
	return []string{t.Card.Debt + debt, moneyLine}
}

func personName(p PersonRef) string {
	if p.ContactID != nil {
		return "📇 " + p.Name
	}
	return p.Name
}

// paperLines: the event or the people of a shared bill, who paid, the debts that follow and who is told.
func paperLines(t Texts, l Lists, it Item) []string {
	p := it.Paper
	var lines []string
	switch {
	case p.EventID != nil:
		name := ""
		for _, e := range l.Events {
			if e.ID == *p.EventID {
				name = e.Name
			}
		}
		lines = append(lines, t.Card.Event+name)
	case p.NewEvent != "":
		lines = append(lines, t.Card.NewEvent+p.NewEvent)
	}
	if len(p.People) == 0 {
		return append(lines, t.Card.People+t.Card.NobodyYet)
	}
	n := len(p.People) + 1 // I am always in
	payer, paidBy := t.Card.PaidMe, ""
	if p.PaidBy != nil {
		paidBy = p.PaidBy.Name
		payer = fmt.Sprintf(t.Card.PaidBy, paidBy)
	}
	names := []string{t.Card.Me}
	for _, x := range p.People {
		names = append(names, personName(x))
	}
	lines = append(lines, payer+" · "+fmt.Sprintf(t.Card.Equal, n), t.Card.People+strings.Join(names, " · "))
	share := "฿" + money(it.Amount/float64(n))
	if p.PaidBy == nil {
		lines = append(lines, t.Card.Debt+fmt.Sprintf(t.Card.Everyone, share),
			fmt.Sprintf(t.Card.MoneyOut, walletName(t, l, it.AccountID), "฿"+money(it.Amount)),
			fmt.Sprintf(t.Card.MyShare, share))
	} else {
		lines = append(lines, t.Card.Debt+fmt.Sprintf(t.Card.IOwe, paidBy, share), t.Card.NotOutOfWallet,
			fmt.Sprintf(t.Card.MyShare, share))
	}
	var told []string
	for _, x := range p.People {
		if x.ContactID == nil {
			continue
		}
		for _, c := range l.Contacts {
			if c.ID == *x.ContactID && c.Linked {
				told = append(told, c.Name)
			}
		}
	}
	if len(told) > 0 {
		lines = append(lines, t.Card.Notify+strings.Join(told, ", "))
	}
	return lines
}

// unsureMark wraps s as "[s ?]" when the model guessed field.
func unsureMark(s string, it Item, field string) string {
	if contains(it.Unsure, field) {
		return "[" + s + " ?]"
	}
	return s
}

// sharedWallet: the wallet every item goes to (and equally sure of); false when items differ or one
// is not a plain expense / income.
func sharedWallet(items []Item) (*uuid.UUID, bool) {
	var w *uuid.UUID
	for i, it := range items {
		if it.Kind != KindCreate || it.Type == "transfer" || it.Paper != nil {
			return nil, false
		}
		if i == 0 {
			w = it.AccountID
			continue
		}
		if (w == nil) != (it.AccountID == nil) || (w != nil && *w != *it.AccountID) ||
			contains(it.Unsure, "account") != contains(items[0].Unsure, "account") {
			return nil, false
		}
	}
	return w, len(items) > 0
}

// sharedDay: every item has the same date, equally sure of.
func sharedDay(items []Item) bool {
	for _, it := range items[1:] {
		if it.Date != items[0].Date || contains(it.Unsure, "date") != contains(items[0].Unsure, "date") {
			return false
		}
	}
	return true
}

func itemName(t Texts, it Item) string {
	if it.Description != nil {
		return *it.Description
	}
	switch it.Type {
	case "income":
		return t.Income
	case "transfer":
		return t.Transfer
	}
	return t.Expense
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

func dayLabel(t Texts, date string, today time.Time) string {
	d, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return date
	}
	switch d.Format(time.DateOnly) {
	case today.Format(time.DateOnly):
		return t.Today
	case today.AddDate(0, 0, -1).Format(time.DateOnly):
		return t.Yesterday
	}
	return d.Format("2/1/2006")
}

func walletName(t Texts, l Lists, id *uuid.UUID) string {
	if id != nil {
		for _, w := range l.Wallets {
			if w.ID == *id {
				return w.Name
			}
		}
	}
	return t.None
}

// categoryLine: "🍽️ Food & Drinks › Dining / Take-away", or "🏷️ ไม่ระบุ".
func categoryLine(t Texts, l Lists, id *uuid.UUID) string {
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
	return emojiCategory + " " + t.None
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

// transferWallets: "from → to"; a withdrawal shows "from →", a deposit "→ to" (one side only).
func transferWallets(t Texts, l Lists, it Item) string {
	from := unsureMark(walletName(t, l, it.AccountID), it, "account")
	to := walletName(t, l, it.TransferToID)
	switch {
	case it.AccountID == nil:
		return "→ " + to
	case it.TransferToID == nil:
		return from + " →"
	}
	return from + " → " + to
}
