package personal_debts

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// SummaryMessage — the share sheet's text in its two variants (0.4.0-3): the whole current cycle
// (text, "รอบนี้") or the balance only (text_balance_only, "ยอดคงเหลืออย่างเดียว").
type SummaryMessage struct {
	Text            string  `json:"text"`
	TextBalanceOnly string  `json:"text_balance_only"`
	Since           *string `json:"since"` // the current cycle's first line date
	Entries         int     `json:"entries"`
	Net             float64 `json:"net"`
}

var thaiMonthsShort = [...]string{"ม.ค.", "ก.พ.", "มี.ค.", "เม.ย.", "พ.ค.", "มิ.ย.", "ก.ค.", "ส.ค.", "ก.ย.", "ต.ค.", "พ.ย.", "ธ.ค."}

func money(v float64) string {
	v = math.Abs(round2(v))
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimSuffix(s, ".00")
	ip, fp := s, ""
	if i := strings.Index(s, "."); i >= 0 {
		ip, fp = s[:i], s[i:]
	}
	var b strings.Builder
	for i, ch := range ip {
		if i > 0 && (len(ip)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	return b.String() + fp
}

func thaiDate(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return fmt.Sprintf("%d %s", t.Day(), thaiMonthsShort[t.Month()-1])
}

// summaryLines is how many lines of the current cycle the summary message lists (the oldest).
const summaryLines = 200

// summaryMessage words the current cycle's lines (oldest first, at most summaryLines).
func (s *Service) summaryMessage(ctx context.Context, userID uuid.UUID, bal DebtPerson, items []StatementItem) (*SummaryMessage, error) {
	prefs, err := shared.UserPrefs(ctx, s.store.db, userID)
	if err != nil {
		return nil, fmt.Errorf("currency: %w", err)
	}
	cur := prefs.Currency
	unit := cur
	if cur == "THB" {
		unit = "บาท"
	}
	name := bal.Person.Name
	var since *string
	var b strings.Builder
	if len(items) > 0 {
		since = &items[0].Date
		fmt.Fprintf(&b, "สรุปยอดกับ %s (ตั้งแต่ %s)\n", name, thaiDate(items[0].Date))
	} else {
		fmt.Fprintf(&b, "สรุปยอดกับ %s\n", name)
	}
	for _, e := range items {
		sign, what := "+", "ฉันจ่ายให้"
		switch {
		case e.Kind == ItemRepayment && e.Direction == DirectionPaid:
			what = "ฉันจ่ายคืน"
		case e.Kind == ItemRepayment:
			sign, what = "−", "ได้รับคืนจาก"
		case e.Direction == DirectionReceived:
			sign, what = "−", "ได้รับจาก"
		}
		line := fmt.Sprintf("%s %s %s%s %s", thaiDate(e.Date), what, sign, money(e.Amount), unit)
		if e.Description != nil && *e.Description != "" {
			line += " (" + *e.Description + ")"
		}
		b.WriteString("• " + line + "\n")
	}
	// Wording (owner 2026-10-23): "{name} เป็นลูกหนี้ / เจ้าหนี้" like the app, never "ติด".
	var total string
	switch {
	case bal.Net > 0.005:
		total = fmt.Sprintf("%s เป็นลูกหนี้ %s %s", name, money(bal.Net), unit)
	case bal.Net < -0.005:
		total = fmt.Sprintf("%s เป็นเจ้าหนี้ %s %s", name, money(bal.Net), unit)
	default:
		total = "ยอดตอนนี้เคลียร์กันหมดแล้ว"
	}
	if math.Abs(bal.Net) > 0.005 {
		b.WriteString("รวม " + total)
	} else {
		b.WriteString(total)
	}
	only := fmt.Sprintf("ยอดกับ %s ตอนนี้: %s", name, total)
	if math.Abs(bal.Net) <= 0.005 {
		only = fmt.Sprintf("ยอดกับ %s: %s", name, total)
	}
	return &SummaryMessage{Text: b.String(), TextBalanceOnly: only, Since: since, Entries: len(items), Net: bal.Net}, nil
}
