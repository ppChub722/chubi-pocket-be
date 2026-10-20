package recurring

import (
	"testing"
	"time"
)

func TestOccurrenceName(t *testing.T) {
	d := func(y int, m time.Month, day int) time.Time { return time.Date(y, m, day, 0, 0, 0, 0, time.UTC) }
	cases := []struct {
		name string
		in   OccNameInput
		want string
	}{
		{"daily", OccNameInput{RuleName: "ค่าข้าวกลางวัน", Freq: "daily", Interval: 1, Slot: d(2026, 10, 20)},
			"ค่าข้าวกลางวัน ประจำวันที่ 20 ต.ค. 2026"},
		{"every 3 days", OccNameInput{RuleName: "ค่ารถ", Freq: "daily", Interval: 3, Slot: d(2026, 10, 20)},
			"ค่ารถ ประจำ 20–22 ต.ค. 2026"},
		{"weekly", OccNameInput{RuleName: "ค่าเรียนว่ายน้ำ", Freq: "weekly", Interval: 1, Weekdays: []int{2}, Slot: d(2026, 10, 20)},
			"ค่าเรียนว่ายน้ำ ประจำสัปดาห์ 20–26 ต.ค. 2026"},
		{"weekly across months", OccNameInput{RuleName: "ค่าเรียน", Freq: "weekly", Interval: 1, Slot: d(2026, 10, 28)},
			"ค่าเรียน ประจำสัปดาห์ 28 ต.ค. – 3 พ.ย. 2026"},
		{"weekly across years", OccNameInput{RuleName: "ค่าเรียน", Freq: "weekly", Interval: 1, Slot: d(2026, 12, 29)},
			"ค่าเรียน ประจำสัปดาห์ 29 ธ.ค. 2026 – 4 ม.ค. 2027"},
		{"every 2 weeks", OccNameInput{RuleName: "ค่าแม่บ้าน", Freq: "weekly", Interval: 2, Slot: d(2026, 10, 20)},
			"ค่าแม่บ้าน ประจำ 20 ต.ค. – 2 พ.ย. 2026"},
		{"weekly, several weekdays = by the day", OccNameInput{RuleName: "ฟิตเนส", Freq: "weekly", Interval: 1, Weekdays: []int{1, 3, 5}, Slot: d(2026, 10, 21)},
			"ฟิตเนส ประจำวันที่ 21 ต.ค. 2026"},
		{"monthly", OccNameInput{RuleName: "Netflix", Freq: "monthly", Interval: 1, Slot: d(2026, 10, 3)},
			"Netflix ประจำเดือน ต.ค. 2026"},
		{"monthly on the 31st", OccNameInput{RuleName: "ค่าห้อง", Freq: "monthly", Interval: 1, Slot: d(2026, 1, 31)},
			"ค่าห้อง ประจำเดือน ม.ค. 2026"},
		{"every 3 months", OccNameInput{RuleName: "ค่าน้ำ", Freq: "monthly", Interval: 3, Slot: d(2026, 10, 5)},
			"ค่าน้ำ ประจำ ต.ค. – ธ.ค. 2026"},
		{"every 3 months across years", OccNameInput{RuleName: "ประกันสุขภาพ", Freq: "monthly", Interval: 3, Slot: d(2026, 11, 30)},
			"ประกันสุขภาพ ประจำ พ.ย. 2026 – ม.ค. 2027"},
		{"yearly", OccNameInput{RuleName: "ภาษีรถ", Freq: "yearly", Interval: 1, Slot: d(2026, 3, 1)},
			"ภาษีรถ ประจำปี 2026"},
		{"every 2 years", OccNameInput{RuleName: "ต่อพาสปอร์ต", Freq: "yearly", Interval: 2, Slot: d(2026, 3, 1)},
			"ต่อพาสปอร์ต ประจำปี 2026–2027"},
		{"dates = by the day", OccNameInput{RuleName: "ค่าเทอม", Freq: "dates", Slot: d(2026, 8, 15)},
			"ค่าเทอม ประจำวันที่ 15 ส.ค. 2026"},
		{"installment", OccNameInput{RuleName: "ผ่อน iPhone 17", Freq: "monthly", Interval: 1, Slot: d(2026, 10, 25), Seq: 5, Total: 12},
			"ผ่อน iPhone 17 งวด 5/12 ประจำเดือน ต.ค. 2026"},
		{"last installment reads like the others", OccNameInput{RuleName: "ผ่อน iPhone 17", Freq: "monthly", Interval: 1, Slot: d(2027, 9, 25), Seq: 12, Total: 12},
			"ผ่อน iPhone 17 งวด 12/12 ประจำเดือน ก.ย. 2027"},
		{"no interval = 1", OccNameInput{RuleName: "Netflix", Freq: "monthly", Slot: d(2026, 10, 3)},
			"Netflix ประจำเดือน ต.ค. 2026"},
		{"no name", OccNameInput{Freq: "monthly", Interval: 1, Slot: d(2026, 10, 3)},
			"ประจำเดือน ต.ค. 2026"},
		{"unknown lang = th", OccNameInput{RuleName: "Netflix", Freq: "monthly", Interval: 1, Slot: d(2026, 10, 3), Lang: "fr"},
			"Netflix ประจำเดือน ต.ค. 2026"},

		{"en daily", OccNameInput{RuleName: "Lunch", Freq: "daily", Interval: 1, Slot: d(2026, 10, 20), Lang: "en"},
			"Lunch – 20 Oct 2026"},
		{"en weekly", OccNameInput{RuleName: "Swimming", Freq: "weekly", Interval: 1, Slot: d(2026, 10, 20), Lang: "en"},
			"Swimming – week of 20–26 Oct 2026"},
		{"en every 2 weeks", OccNameInput{RuleName: "Cleaner", Freq: "weekly", Interval: 2, Slot: d(2026, 10, 20), Lang: "en"},
			"Cleaner – 20 Oct – 2 Nov 2026"},
		{"en monthly", OccNameInput{RuleName: "Netflix", Freq: "monthly", Interval: 1, Slot: d(2026, 10, 3), Lang: "en"},
			"Netflix – Oct 2026"},
		{"en every 3 months across years", OccNameInput{RuleName: "Insurance", Freq: "monthly", Interval: 3, Slot: d(2026, 11, 30), Lang: "en"},
			"Insurance – Nov 2026 – Jan 2027"},
		{"en yearly", OccNameInput{RuleName: "Car tax", Freq: "yearly", Interval: 1, Slot: d(2026, 3, 1), Lang: "en"},
			"Car tax – 2026"},
		{"en installment", OccNameInput{RuleName: "iPhone 17", Freq: "monthly", Interval: 1, Slot: d(2026, 10, 25), Seq: 5, Total: 12, Lang: "en"},
			"iPhone 17 5/12 – Oct 2026"},
		{"en no name", OccNameInput{Freq: "monthly", Interval: 1, Slot: d(2026, 10, 3), Lang: "en"},
			"Oct 2026"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := OccurrenceName(c.in); got != c.want {
				t.Errorf("OccurrenceName = %q, want %q", got, c.want)
			}
		})
	}
}
