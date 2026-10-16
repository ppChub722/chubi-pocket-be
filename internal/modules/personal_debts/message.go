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

// SummaryMessage — the generated text for a person (spec DebtSummaryMessage).
type SummaryMessage struct {
	Text    string  `json:"text"`
	Entries int     `json:"entries"`
	Net     float64 `json:"net"`
}

var thaiMonthsShort = [...]string{"ม.ค.", "ก.พ.", "มี.ค.", "เม.ย.", "พ.ค.", "มิ.ย.", "ก.ค.", "ส.ค.", "ก.ย.", "ต.ค.", "พ.ย.", "ธ.ค."}

// money — 1,234 / 1,234.50 (no trailing .00).
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

// SummaryMessage — GET /personal-debts/people/:key/summary-message. The
// entries are the CURRENT cycle, oldest first, each signed (+ they owe me /
// − I owe them); the total is the person's net. Thai, both directions.
func (s *Service) SummaryMessage(ctx context.Context, userID uuid.UUID, rawKey string) (*SummaryMessage, error) {
	k, err := shared.ParseDebtKey(rawKey)
	if err != nil {
		return nil, err
	}
	bal, found, err := s.store.personBalance(ctx, s.store.db, userID, k)
	if err != nil {
		return nil, err
	}
	if !found && (k.ContactID == nil || bal.Person.ContactID == nil) {
		return nil, ErrPersonNotFound
	}
	items, _, err := s.store.listEntries(ctx, userID, k.Key(), EntryFilter{Cycle: "current", Sort: "date_asc", Page: 1, PerPage: 200})
	if err != nil {
		return nil, err
	}
	var cur string
	if err := s.store.db.QueryRow(ctx, `SELECT currency FROM users WHERE id = $1`, userID).Scan(&cur); err != nil {
		return nil, fmt.Errorf("currency: %w", err)
	}
	unit := cur
	if cur == "THB" {
		unit = "บาท"
	}
	name := bal.Person.Name
	var b strings.Builder
	fmt.Fprintf(&b, "สรุปยอดกับ %s\n", name)
	for _, e := range items {
		sign, what := "+", "ฉันจ่ายให้"
		if e.Direction == DirectionReceived {
			sign, what = "−", "ได้รับจาก"
		}
		line := fmt.Sprintf("%s %s %s%s %s", thaiDate(e.Date), what, sign, money(e.Amount), unit)
		if e.Description != nil && *e.Description != "" {
			line += " (" + *e.Description + ")"
		}
		b.WriteString("• " + line + "\n")
	}
	switch {
	case bal.Net > 0.005:
		fmt.Fprintf(&b, "รวม %s ค้างจ่ายฉัน %s %s", name, money(bal.Net), unit)
	case bal.Net < -0.005:
		fmt.Fprintf(&b, "รวม ฉันค้างจ่าย %s %s %s", name, money(bal.Net), unit)
	default:
		b.WriteString("ยอดตอนนี้เคลียร์กันหมดแล้ว")
	}
	return &SummaryMessage{Text: b.String(), Entries: len(items), Net: bal.Net}, nil
}
