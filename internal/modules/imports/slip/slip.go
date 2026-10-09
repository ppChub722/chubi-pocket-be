// Package slip — OCR text → what a bank slip says (0.3.1). Facts only:
// nothing here knows the user, their wallets or categories (that's the
// imports draft builder). Spec: docs design/spec/15-slip-import.md §3–4.
//
// The QR comes first (read on the phone): its bank code picks the rule
// set. One rule set per bank layout; today only KBank / K PLUS.
package slip

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/imports/ocr"
)

// ErrUnsupportedBank — no rule set for the QR's bank code yet.
var ErrUnsupportedBank = errors.New("no slip rules for this bank")

// Kind of slip, from its header.
type Kind string

const (
	KindTransfer Kind = "transfer" // โอนเงินสำเร็จ
	KindPayment  Kind = "payment"  // ชำระเงินสำเร็จ (merchant / QR)
	KindBill     Kind = "bill"     // จ่ายบิลสำเร็จ
)

// Receiver kinds (Party.Kind).
const (
	ReceiverBankAccount = "bank_account"
	ReceiverPromptPay   = "promptpay"
	ReceiverMerchant    = "merchant"
	ReceiverBiller      = "biller"
)

// Required fields that can end up in Slip.Missing.
const (
	FieldAmount     = "amount"
	FieldOccurredAt = "occurred_at"
)

// Slip — what was read. Unknown → zero value, omitted from JSON.
type Slip struct {
	Kind       Kind       `json:"kind,omitempty"`
	OccurredAt *time.Time `json:"occurred_at,omitempty"` // Asia/Bangkok
	Amount     *float64   `json:"amount,omitempty"`
	Fee        *float64   `json:"fee,omitempty"`
	Sender     *Party     `json:"sender,omitempty"`
	Receiver   *Party     `json:"receiver,omitempty"`
	Memo       string     `json:"memo,omitempty"`
	// Required fields the rules couldn't read.
	Missing []string `json:"missing"`
}

// Party — one side of the slip.
type Party struct {
	Kind    string `json:"kind,omitempty"` // receiver only, see Receiver*
	Name    string `json:"name,omitempty"`
	Bank    string `json:"bank,omitempty"`
	Masked  string `json:"masked,omitempty"`  // digits, `x` for hidden ones: "xxxxx2780x"
	Company string `json:"company,omitempty"` // merchant
	Ref1    string `json:"ref1,omitempty"`    // merchant ref / bill ref 1
	Ref2    string `json:"ref2,omitempty"`    // bill ref 2
}

// Parse reads res with the rule set of bankCode (from the slip's QR).
func Parse(bankCode string, res *ocr.Result) (*Slip, error) {
	var s *Slip
	switch bankCode {
	case "004": // KBank — K PLUS
		s = parseKBank(res)
	default:
		return nil, ErrUnsupportedBank
	}
	s.Missing = []string{}
	if s.Amount == nil {
		s.Missing = append(s.Missing, FieldAmount)
	}
	if s.OccurredAt == nil {
		s.Missing = append(s.Missing, FieldOccurredAt)
	}
	return s, nil
}

// Bangkok — fixed +07:00 (no DST); the Alpine image has no tzdata.
var Bangkok = time.FixedZone("ICT", 7*60*60)

// sortedLines — the OCR lines top to bottom (psm 11 doesn't promise it).
func sortedLines(res *ocr.Result) []ocr.Line {
	lines := slices.Clone(res.Lines)
	slices.SortStableFunc(lines, func(a, b ocr.Line) int { return a.Box.Y - b.Box.Y })
	return lines
}

// skeleton drops every space (OCR spacing in Thai is unreliable) and
// rejoins sara am ("จํ า" → "จำ").
func skeleton(s string) string {
	return strings.ReplaceAll(strings.Join(strings.Fields(s), ""), "ํา", "ำ")
}
