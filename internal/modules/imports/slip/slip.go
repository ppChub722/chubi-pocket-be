package slip

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/imports/ocr"
)

var ErrUnsupportedBank = errors.New("no slip rules for this bank")

type Kind string

const (
	KindTransfer Kind = "transfer" // โอนเงินสำเร็จ
	KindPayment  Kind = "payment"  // ชำระเงินสำเร็จ (merchant / QR)
	KindBill     Kind = "bill"     // จ่ายบิลสำเร็จ
)

const (
	ReceiverBankAccount = "bank_account"
	ReceiverPromptPay   = "promptpay"
	ReceiverMerchant    = "merchant"
	ReceiverBiller      = "biller"
)

const (
	FieldAmount     = "amount"
	FieldOccurredAt = "occurred_at"
)

type Slip struct {
	Kind       Kind       `json:"kind,omitempty"`
	OccurredAt *time.Time `json:"occurred_at,omitempty"` // Asia/Bangkok
	Amount     *float64   `json:"amount,omitempty"`
	Fee        *float64   `json:"fee,omitempty"`
	Sender     *Party     `json:"sender,omitempty"`
	Receiver   *Party     `json:"receiver,omitempty"`
	Memo       string     `json:"memo,omitempty"`
	Missing []string `json:"missing"`
}

type Party struct {
	Kind    string `json:"kind,omitempty"` // receiver only, see Receiver*
	Name    string `json:"name,omitempty"`
	Bank    string `json:"bank,omitempty"`
	Masked  string `json:"masked,omitempty"`  // digits, `x` for hidden ones: "xxxxx2780x"
	Company string `json:"company,omitempty"` // merchant
	Ref1    string `json:"ref1,omitempty"`    // merchant ref / bill ref 1
	Ref2    string `json:"ref2,omitempty"`    // bill ref 2
}

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

var Bangkok = time.FixedZone("ICT", 7*60*60)

func sortedLines(res *ocr.Result) []ocr.Line {
	lines := slices.Clone(res.Lines)
	slices.SortStableFunc(lines, func(a, b ocr.Line) int { return a.Box.Y - b.Box.Y })
	return lines
}

func skeleton(s string) string {
	return strings.ReplaceAll(strings.Join(strings.Fields(s), ""), "ํา", "ำ")
}
