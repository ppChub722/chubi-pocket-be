package recurring

import (
	"fmt"
	"strings"
	"time"
)

// OccNameInput is what an occurrence's name is made of. Slot is the
// occurrence's original date, so moving its date never renames it.
type OccNameInput struct {
	RuleName string
	Freq     string // daily | weekly | monthly | yearly | dates
	Interval int
	Weekdays []int
	Slot     time.Time
	Seq      int
	Total    int    // 0 = not an installment
	Lang     string // "en"; anything else is Thai
}

var (
	occMonthsTH = [12]string{"ม.ค.", "ก.พ.", "มี.ค.", "เม.ย.", "พ.ค.", "มิ.ย.", "ก.ค.", "ส.ค.", "ก.ย.", "ต.ค.", "พ.ย.", "ธ.ค."}
	occMonthsEN = [12]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
)

// OccurrenceName names one occurrence by the period it covers: from the slot
// to the day before the next one, sized from the rule's step (never from the
// next row, so the last occurrence reads like the others). A `dates` rule and
// a weekly one on several weekdays are named by the day.
//
//	Netflix ประจำเดือน ต.ค. 2026 | ผ่อน iPhone งวด 5/12 ประจำเดือน ต.ค. 2026
//	Netflix – Oct 2026 | iPhone 5/12 – Oct 2026
func OccurrenceName(in OccNameInput) string {
	en := in.Lang == "en"
	months := occMonthsTH
	if en {
		months = occMonthsEN
	}
	step := max(in.Interval, 1)
	s := in.Slot
	day := func(t time.Time) string { return fmt.Sprintf("%d %s %d", t.Day(), months[t.Month()-1], t.Year()) }
	days := func(n int) string { return occDayRange(s, s.AddDate(0, 0, n-1), months) }

	var unit, period string // unit: the Thai word after ประจำ for a one-step period
	freq := in.Freq
	if freq == "weekly" && len(in.Weekdays) > 1 {
		freq = "dates"
	}
	switch freq {
	case "daily":
		if step == 1 {
			unit, period = "วันที่", day(s)
		} else {
			period = days(step)
		}
	case "weekly":
		period = days(7 * step)
		if step == 1 {
			unit = "สัปดาห์"
			if en {
				period = "week of " + period
			}
		}
	case "monthly":
		if step == 1 {
			unit, period = "เดือน", fmt.Sprintf("%s %d", months[s.Month()-1], s.Year())
		} else {
			last := time.Date(s.Year(), s.Month()+time.Month(step-1), 1, 0, 0, 0, 0, s.Location())
			period = occMonthRange(s, last, months)
		}
	case "yearly":
		unit, period = "ปี", fmt.Sprint(s.Year())
		if step > 1 {
			period = fmt.Sprintf("%d–%d", s.Year(), s.Year()+step-1)
		}
	default: // dates, and anything unknown
		unit, period = "วันที่", day(s)
	}

	name := strings.TrimSpace(in.RuleName)
	installment := in.Seq > 0 && in.Total > 0
	if en {
		head := name
		if installment {
			head = strings.TrimSpace(fmt.Sprintf("%s %d/%d", name, in.Seq, in.Total))
		}
		if head == "" {
			return period
		}
		return head + " – " + period
	}
	parts := make([]string, 0, 3)
	if name != "" {
		parts = append(parts, name)
	}
	if installment {
		parts = append(parts, fmt.Sprintf("งวด %d/%d", in.Seq, in.Total))
	}
	if unit != "" {
		parts = append(parts, "ประจำ"+unit+" "+period)
	} else {
		parts = append(parts, "ประจำ "+period)
	}
	return strings.Join(parts, " ")
}

// occDayRange: "20–26 ต.ค. 2026", "20 ต.ค. – 2 พ.ย. 2026",
// "29 ธ.ค. 2026 – 4 ม.ค. 2027".
func occDayRange(a, b time.Time, months [12]string) string {
	ma, mb := months[a.Month()-1], months[b.Month()-1]
	switch {
	case a.Year() == b.Year() && a.Month() == b.Month():
		return fmt.Sprintf("%d–%d %s %d", a.Day(), b.Day(), ma, a.Year())
	case a.Year() == b.Year():
		return fmt.Sprintf("%d %s – %d %s %d", a.Day(), ma, b.Day(), mb, a.Year())
	default:
		return fmt.Sprintf("%d %s %d – %d %s %d", a.Day(), ma, a.Year(), b.Day(), mb, b.Year())
	}
}

// occMonthRange: "ต.ค. – ธ.ค. 2026", "พ.ย. 2026 – ม.ค. 2027".
func occMonthRange(a, b time.Time, months [12]string) string {
	ma, mb := months[a.Month()-1], months[b.Month()-1]
	if a.Year() == b.Year() {
		return fmt.Sprintf("%s – %s %d", ma, mb, a.Year())
	}
	return fmt.Sprintf("%s %d – %s %d", ma, a.Year(), mb, b.Year())
}
