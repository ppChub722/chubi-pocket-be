package slip

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// thaiMonths — abbreviations with dots and spaces removed.
var thaiMonths = map[string]time.Month{
	"มค": time.January, "กพ": time.February, "มีค": time.March,
	"เมย": time.April, "พค": time.May, "มิย": time.June,
	"กค": time.July, "สค": time.August, "กย": time.September,
	"ตค": time.October, "พย": time.November, "ธค": time.December,
}

// strayMarks — below-vowels and tone marks OCR sprinkles on a month
// ("ต.ุค."). None of them belongs to a month abbreviation.
var strayMarks = strings.NewReplacer(
	"ุ", "", "ู", "", // ุ ู
	"่", "", "้", "", "๊", "", "๋", "", // tone marks
	"็", "", "์", "", "ั", "", // ็ ์ ั
)

// "9 ต.ค. 69 13:19 น." — day, month, 2- or 4-digit Buddhist year, time.
var thaiDateTime = regexp.MustCompile(
	`(\d{1,2})\s*([^\d\s:][^\d:]{0,10}?)\s*(\d{4}|\d{2})\s+(\d{1,2})[:.](\d{2})`)

// parseThaiDateTime reads a slip's date line, in Bangkok time.
func parseThaiDateTime(s string) (time.Time, bool) {
	m := thaiDateTime.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false
	}
	tok := strings.NewReplacer(".", "", " ", "").Replace(m[2])
	month, ok := thaiMonths[tok]
	if !ok {
		month, ok = thaiMonths[strayMarks.Replace(tok)]
	}
	if !ok {
		return time.Time{}, false
	}
	day, _ := strconv.Atoi(m[1])
	year, _ := strconv.Atoi(m[3])
	if year < 100 {
		year += 2500 // "69" → 2569 BE
	}
	if year > 2400 {
		year -= 543 // BE → CE
	}
	hour, _ := strconv.Atoi(m[4])
	minute, _ := strconv.Atoi(m[5])
	if day < 1 || day > 31 || hour > 23 || minute > 59 {
		return time.Time{}, false
	}
	t := time.Date(year, month, day, hour, minute, 0, 0, Bangkok)
	if t.Day() != day { // 31 ก.พ. etc.
		return time.Time{}, false
	}
	return t, true
}

// "1,250.00 บาท" / "30,000.00 um" — the number only.
var amountRe = regexp.MustCompile(`(?:^|[^\d,.])(\d{1,3}(?:,\d{3})+|\d+)\.(\d{2})(?:[^\d]|$)`)

func parseAmount(s string) (float64, bool) {
	m := amountRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "")+"."+m[2], 64)
	return v, err == nil
}

// maskedRe — a masked account / PromptPay number as OCR reads it:
// "XXX-X-X2780-x", "XXX-XXX-4464", "X=-XXXX-XXXX7-45-5", "XXX-X-X2121-%".
var maskedRe = regexp.MustCompile(`^[Xx×%=\d\-]+$`)

// masked turns such a line into digits with `x` for hidden ones
// ("xxxxx2780x"). False when the line isn't one.
func masked(s string) (string, bool) {
	t := skeleton(s)
	if !maskedRe.MatchString(t) || !strings.Contains(t, "-") {
		return "", false
	}
	var b strings.Builder
	digits, hidden := 0, 0
	for _, r := range t {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			digits++
		case r == 'X' || r == 'x' || r == '×' || r == '%':
			b.WriteByte('x')
			hidden++
		}
	}
	if digits < 3 || hidden < 2 {
		return "", false
	}
	return b.String(), true
}
