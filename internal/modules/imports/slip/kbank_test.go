package slip

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/imports/ocr"
)

// Fixtures: real K PLUS slips read with psm 11 (layout, junk lines and
// misreads kept), with names, account digits and refs replaced.
func loadFixture(t *testing.T, name string) *ocr.Result {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "kbank", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var r ocr.Result
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

func f(v float64) *float64 { return &v }

func TestKBankFixtures(t *testing.T) {
	cases := []struct {
		file     string
		kind     Kind
		at       string // "2006-01-02 15:04", Bangkok
		amount   *float64
		fee      *float64
		sender   Party
		receiver Party
		memo     string
	}{
		{
			file: "transfer_promptpay_phone", kind: KindTransfer, at: "2026-10-09 13:19",
			amount: f(97), fee: f(0),
			sender:   Party{Name: "นาย สมชาย ใ", Bank: "ธ.กสิกรไทย", Masked: "xxxxx1234x"},
			receiver: Party{Kind: ReceiverPromptPay, Name: "นางสาว สมหญิง รักเรียน", Masked: "xxxxxx9999"},
		},
		{
			file: "transfer_promptpay_id", kind: KindTransfer, at: "2026-10-07 09:11",
			amount: f(55), fee: f(0),
			sender:   Party{Name: "นาย สมชาย ใ", Bank: "ธ.กสิกรไทย", Masked: "xxxxx1234x"},
			receiver: Party{Kind: ReceiverPromptPay, Name: "นายสมปอง ดีใจ", Masked: "xxxxxxxxx1234"},
		},
		{
			file: "transfer_bank_other", kind: KindTransfer, at: "2026-10-07 12:51",
			amount: f(40), fee: f(0),
			sender:   Party{Name: "นาย สมชาย ใ", Bank: "ธ.กสิกรไทย", Masked: "xxxxx1234x"},
			receiver: Party{Kind: ReceiverBankAccount, Name: "นาย สมศักดิ์ มั่นคง", Bank: "ธ.กสิกรไทย", Masked: "xxxxx2468x"},
		},
		{
			file: "transfer_own_account", kind: KindTransfer, at: "2026-10-08 16:34",
			amount: f(30000), fee: f(0),
			sender:   Party{Name: "นาย สมชาย ใ", Bank: "ธ.กสิกรไทย", Masked: "xxxxx1234x"},
			receiver: Party{Kind: ReceiverBankAccount, Name: "นาย สมชาย ใจดีมาก", Bank: "ธ.กสิกรไทย", Masked: "xxxxx5678x"},
		},
		{
			file: "transfer_memo_icecream", kind: KindTransfer, at: "2026-10-09 15:30",
			amount: f(451.76), fee: f(0), memo: "ซื้อไอติม",
			sender:   Party{Name: "นาย สมชาย ใ", Bank: "ธ.กสิกรไทย", Masked: "xxxxx1234x"},
			receiver: Party{Kind: ReceiverBankAccount, Name: "นาย สมชาย ใจดีมาก", Bank: "ธ.กสิกรไทย", Masked: "xxxxx5678x"},
		},
		{
			file: "transfer_memo_hotel", kind: KindTransfer, at: "2026-10-09 15:31",
			amount: f(10), fee: f(0), memo: "จองโรงแรม",
			sender:   Party{Name: "นาย สมชาย ใ", Bank: "ธ.กสิกรไทย", Masked: "xxxxx1234x"},
			receiver: Party{Kind: ReceiverBankAccount, Name: "นาย สมชาย ใจดีมาก", Bank: "ธ.กสิกรไทย", Masked: "xxxxx5678x"},
		},
		{
			// Shop name wraps onto a second line; ต.ค. read as "ต.ุค.".
			file: "payment_merchant_wrap", kind: KindPayment, at: "2026-10-07 15:35",
			amount: f(70), fee: f(0),
			sender:   Party{Name: "นาย สมชาย ใ", Bank: "ธ.กสิกรไทย", Masked: "xxxxx1234x"},
			receiver: Party{Kind: ReceiverMerchant, Name: "คาเฟอเมซอน พหลโยธิน เพลส พลาซา", Company: "บจก. ตัวอย่าง", Ref1: "202610071969785"},
		},
		{
			// The shop's logo reads as a junk line left of the column.
			file: "payment_merchant_logo", kind: KindPayment, at: "2026-10-08 16:34",
			amount: f(40), fee: f(0),
			sender:   Party{Name: "นาย สมชาย ใ", Bank: "ธ.กสิกรไทย", Masked: "xxxxx1234x"},
			receiver: Party{Kind: ReceiverMerchant, Name: "ร้านเวนดิ้งบายบุญเติม", Company: "บมจ. ฟอร์ท สมาร์ท เซอร์วิส", Ref1: "202610082188915"},
		},
		{
			// Card bill: Ref 1 is the card number. The fee's value didn't read.
			file: "bill_card", kind: KindBill, at: "2026-10-08 15:30",
			amount: f(19241.95), fee: nil,
			sender:   Party{Name: "นาย สมชาย ใ", Bank: "ธ.กสิกรไทย", Masked: "xxxxx1234x"},
			receiver: Party{Kind: ReceiverBiller, Name: "AYUDHYA CAPIT", Ref1: "4000123412341234", Masked: "4000123412341234"},
		},
		{
			// เลขที่รายการ comes before จำนวน on this layout.
			file: "bill_repayment", kind: KindBill, at: "2026-10-03 16:00",
			amount: f(11336.20), fee: f(0),
			sender:   Party{Name: "นาย สมชาย ใ", Bank: "ธ.กสิกรไทย", Masked: "xxxxx1234x"},
			receiver: Party{Kind: ReceiverBiller, Name: "Credit Repayment", Ref1: "ABCD1234EFGH", Ref2: "012345"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			s, err := Parse("004", loadFixture(t, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if s.Kind != tc.kind {
				t.Errorf("kind = %q, want %q", s.Kind, tc.kind)
			}
			if s.OccurredAt == nil || s.OccurredAt.Format("2006-01-02 15:04") != tc.at {
				t.Errorf("occurred_at = %v, want %s", s.OccurredAt, tc.at)
			} else if _, off := s.OccurredAt.Zone(); off != 7*3600 {
				t.Errorf("zone offset = %d", off)
			}
			checkAmount(t, "amount", s.Amount, tc.amount)
			checkAmount(t, "fee", s.Fee, tc.fee)
			if s.Sender == nil || *s.Sender != tc.sender {
				t.Errorf("sender = %+v, want %+v", s.Sender, tc.sender)
			}
			if s.Receiver == nil || *s.Receiver != tc.receiver {
				t.Errorf("receiver = %+v, want %+v", s.Receiver, tc.receiver)
			}
			if s.Memo != tc.memo {
				t.Errorf("memo = %q, want %q", s.Memo, tc.memo)
			}
			if len(s.Missing) != 0 {
				t.Errorf("missing = %v", s.Missing)
			}
		})
	}
}

func checkAmount(t *testing.T, name string, got, want *float64) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("%s = %v, want none", name, *got)
	case want != nil && got == nil:
		t.Errorf("%s = none, want %v", name, *want)
	case want != nil && *got != *want:
		t.Errorf("%s = %v, want %v", name, *got, *want)
	}
}

func TestParseUnsupportedBank(t *testing.T) {
	if _, err := Parse("014", &ocr.Result{}); !errors.Is(err, ErrUnsupportedBank) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseReportsMissing(t *testing.T) {
	s, err := Parse("004", &ocr.Result{Width: 1000, Lines: []ocr.Line{{Text: "โอนเงินสำเร็จ"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Missing) != 2 || s.Missing[0] != FieldAmount || s.Missing[1] != FieldOccurredAt {
		t.Fatalf("missing = %v", s.Missing)
	}
}

func TestParseThaiDateTime(t *testing.T) {
	cases := map[string]string{
		"9 ต.ค. 69 13:19 น.":    "2026-10-09 13:19",
		"7 ต.ุค. 69 09:11 น.":   "2026-10-07 09:11",
		"9 ต .ค . 69 13:19 น .": "2026-10-09 13:19",
		"1 มี.ค. 2569 08:05":    "2026-03-01 08:05",
		"15 เม.ย. 70 23:59 น.":  "2027-04-15 23:59",
		"31 มิ.ย. 69 10:00":     "", // no 31 June
		"016282131941BPP08743":  "",
	}
	for in, want := range cases {
		got, ok := parseThaiDateTime(in)
		switch {
		case want == "" && ok:
			t.Errorf("%q → %v, want no date", in, got)
		case want != "" && (!ok || got.Format("2006-01-02 15:04") != want):
			t.Errorf("%q → %v %v, want %s", in, got, ok, want)
		case ok:
			if got.Location() != Bangkok {
				t.Errorf("%q not in Bangkok", in)
			}
		}
	}
	_ = time.UTC
}

func TestMasked(t *testing.T) {
	cases := map[string]string{
		"XXX-X-X2780-x":      "xxxxx2780x",
		"XXX-XXX-4464":       "xxxxxx4464",
		"X=-XXXX-XXXX7-45-5": "xxxxxxxxx7455",
		"XXX-X-X2121-%":      "xxxxx2121x",
		"202610071969785":    "", // a ref, not masked
		"0.00 บาท":           "",
	}
	for in, want := range cases {
		got, ok := masked(in)
		if (want == "") == ok || got != want {
			t.Errorf("%q → %q %v, want %q", in, got, ok, want)
		}
	}
}

// OCR splits sara am off its consonant ("จํ า") — labels must still match.
func TestMemoWithSplitSaraAm(t *testing.T) {
	res := &ocr.Result{Width: 1000, Lines: []ocr.Line{
		{Text: "จํ านวน:", Box: ocr.Box{Y: 10}},
		{Text: "12.00 บาท", Box: ocr.Box{Y: 20}},
		{Text: "บันทึกช่วยจํ า: ค่าน้ำ", Box: ocr.Box{Y: 30}},
	}}
	s, _ := Parse("004", res)
	if s.Amount == nil || *s.Amount != 12 || s.Memo != "ค่าน้ำ" {
		t.Fatalf("amount %v memo %q", s.Amount, s.Memo)
	}
}
