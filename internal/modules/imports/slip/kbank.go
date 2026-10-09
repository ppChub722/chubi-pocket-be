package slip

import (
	"regexp"
	"strings"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/imports/ocr"
)

// KBank / K PLUS (bank code 004) — spec §3.2, built on 15 real slips read
// with psm 11. Layout, top to bottom:
//
//	header · date · sender (name, ธ.bank, masked acct) · receiver block ·
//	จำนวน: amount · ค่าธรรมเนียม: fee · เลขที่รายการ: ref · [บันทึกช่วยจำ: memo]
//
// Values are found by their label, not by position (a bill slip puts
// เลขที่รายการ before จำนวน). The sender / receiver blocks are a text
// column at ~15–50 % of the width; logos and the arrow left of it and the
// cartoon right of it come out as junk lines, dropped by their x.

// Column of the party blocks, as a fraction of the slip's width.
const kbankColMin, kbankColMax = 0.12, 0.55

var kbankHeaders = []struct {
	text string
	kind Kind
}{
	{"โอนเงินสำเร็จ", KindTransfer},
	{"ชำระเงินสำเร็จ", KindPayment},
	{"จ่ายบิลสำเร็จ", KindBill},
}

// Label skeletons (no spaces). OCR sometimes drops the ่ of ที่.
var (
	kbankAmountLabel = []string{"จำนวน"}
	kbankFeeLabel    = []string{"ค่าธรรมเนียม"}
	kbankRefLabel    = []string{"ขที่รายการ", "ขทีรายการ"}
	kbankMemoLabel   = []string{"ช่วยจำ"}
)

// Company prefixes on a merchant's second line.
var companyRe = regexp.MustCompile(`^(บจก|บมจ|หจก|บริษัท|ห้างหุ้นส่วน)`)

// A bill / merchant reference: letters and digits, no spaces.
var refRe = regexp.MustCompile(`^[A-Z0-9]{5,}$`)

// isRef — a reference has digits; "AYUDHYA CAPIT" does not.
func isRef(s string) bool { return refRe.MatchString(s) && strings.ContainsAny(s, "0123456789") }

func parseKBank(res *ocr.Result) *Slip {
	lines := sortedLines(res)
	s := &Slip{}

	// Header + date: the first lines that look like them.
	dateBottom := -1
	for _, l := range lines {
		sk := skeleton(l.Text)
		if s.Kind == "" {
			for _, h := range kbankHeaders {
				if strings.Contains(sk, h.text) {
					s.Kind = h.kind
				}
			}
		}
		if s.OccurredAt == nil {
			if t, ok := parseThaiDateTime(l.Text); ok {
				s.OccurredAt = &t
				dateBottom = l.Box.Y + l.Box.H
			}
		}
	}

	// Labels.
	amountAt := findLabel(lines, kbankAmountLabel)
	feeAt := findLabel(lines, kbankFeeLabel)
	refAt := findLabel(lines, kbankRefLabel)
	memoAt := findLabel(lines, kbankMemoLabel)
	labels := []int{amountAt, feeAt, refAt, memoAt}

	if v, ok := valueAfter(lines, amountAt, labels); ok {
		s.Amount = &v
	}
	if v, ok := valueAfter(lines, feeAt, labels); ok {
		s.Fee = &v
	}
	if memoAt >= 0 {
		s.Memo = memoText(lines[memoAt].Text)
	}

	// Party blocks: between the date and the first label, in the column.
	firstLabel := len(lines)
	for _, i := range labels {
		if i >= 0 && i < firstLabel {
			firstLabel = i
		}
	}
	var zone []ocr.Line
	for _, l := range lines[:firstLabel] {
		if dateBottom >= 0 && l.Box.Y < dateBottom {
			continue
		}
		if res.Width > 0 {
			x := float64(l.Box.X) / float64(res.Width)
			if x < kbankColMin || x > kbankColMax {
				continue
			}
		}
		if strings.TrimSpace(l.Text) != "" {
			zone = append(zone, l)
		}
	}
	s.Sender, s.Receiver = kbankParties(zone, s.Kind)
	return s
}

// findLabel — index of the first line containing one of the label
// skeletons, or -1.
func findLabel(lines []ocr.Line, labels []string) int {
	for i, l := range lines {
		sk := skeleton(l.Text)
		for _, lb := range labels {
			if strings.Contains(sk, lb) {
				return i
			}
		}
	}
	return -1
}

// valueAfter — the first amount below the label at `at`, before the next
// label (a missing value must not borrow the next label's).
func valueAfter(lines []ocr.Line, at int, labels []int) (float64, bool) {
	if at < 0 {
		return 0, false
	}
	for i := at; i < len(lines); i++ {
		if i > at {
			for _, lb := range labels {
				if lb == i {
					return 0, false
				}
			}
		}
		text := lines[i].Text
		if i == at {
			// "จำนวน: 97.00 บาท" on one line — only what follows the label.
			_, after, ok := strings.Cut(text, ":")
			if !ok {
				continue
			}
			text = after
		}
		if v, ok := parseAmount(text); ok {
			return v, true
		}
	}
	return 0, false
}

func memoText(line string) string {
	if _, after, ok := strings.Cut(line, ":"); ok {
		return strings.TrimSpace(after)
	}
	// No colon read: drop the label word itself.
	if i := strings.Index(line, "ช่วยจำ"); i >= 0 {
		return strings.TrimSpace(line[i+len("ช่วยจำ"):])
	}
	return ""
}

// kbankParties splits the column into the sender (name · ธ.bank · masked
// account) and whatever follows as the receiver.
func kbankParties(zone []ocr.Line, kind Kind) (*Party, *Party) {
	senderEnd := -1
	sender := &Party{}
	for i, l := range zone {
		if m, ok := masked(l.Text); ok {
			sender.Masked = m
			senderEnd = i
			break
		}
	}
	if senderEnd < 0 {
		return nil, nil
	}
	for _, l := range zone[:senderEnd] {
		switch {
		case isBankLine(l.Text):
			sender.Bank = skeleton(l.Text)
		case sender.Name == "":
			sender.Name = strings.TrimSpace(l.Text)
		}
	}

	rest := zone[senderEnd+1:]
	if len(rest) == 0 {
		return sender, nil
	}
	recv := &Party{}
	switch {
	case kind == KindPayment:
		recv.Kind = ReceiverMerchant
		var name []string
		for _, l := range rest {
			t := strings.TrimSpace(l.Text)
			switch {
			case recv.Company == "" && companyRe.MatchString(skeleton(t)):
				recv.Company = t
			case isRef(skeleton(t)):
				if recv.Ref1 == "" {
					recv.Ref1 = skeleton(t)
				}
			case recv.Company == "":
				name = append(name, t) // the shop name may wrap
			}
		}
		recv.Name = strings.Join(name, " ")
	case kind == KindBill:
		recv.Kind = ReceiverBiller
		for _, l := range rest {
			t := strings.TrimSpace(l.Text)
			switch {
			case isRef(skeleton(t)):
				if recv.Ref1 == "" {
					recv.Ref1 = skeleton(t)
				} else if recv.Ref2 == "" {
					recv.Ref2 = skeleton(t)
				}
			case recv.Name == "":
				recv.Name = t
			}
		}
		// An all-digit Ref 1 is usually the card / account being paid —
		// kept whole so a wallet's number can match it.
		if isDigits(recv.Ref1) && len(recv.Ref1) >= 10 {
			recv.Masked = recv.Ref1
		}
	default: // transfer
		recv.Kind = ReceiverBankAccount
		for _, l := range rest {
			t := strings.TrimSpace(l.Text)
			if m, ok := masked(t); ok {
				if recv.Masked == "" {
					recv.Masked = m
				}
				continue
			}
			sk := skeleton(t)
			switch {
			case strings.Contains(sk, "พร้อมเพย์"):
				recv.Kind = ReceiverPromptPay
			case isBankLine(t):
				recv.Bank = sk
			case recv.Name == "":
				recv.Name = t
			}
		}
	}
	return sender, recv
}

// "ธ.กสิกรไทย" — the bank line under a name.
func isBankLine(s string) bool { return strings.HasPrefix(skeleton(s), "ธ.") }

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
