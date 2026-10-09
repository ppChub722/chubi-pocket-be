package imports

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/accounts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/imports/ocr"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/imports/slip"
)

func TestMatchDigits(t *testing.T) {
	cases := []struct {
		slip, stored string
		want         bool
	}{
		{"xxxxx2780x", "1234527806", true},  // full number stored
		{"xxxxx2780x", "1234527816", false}, // a visible digit differs
		{"xxxxx2780x", "xxxxx2780x", true},  // saved from a slip
		{"xxxxxx4464", "0812344464", true},  // PromptPay phone
		{"xxxxx2780x", "2780", true},        // only the visible part typed
		{"xxxxx2780x", "780", false},        // too few digits
		{"xxxxx2780x", "xxxxxxxxx0", false}, // nothing to compare
		{"4000123412341234", "4000123412341234", true},
		{"xxxxxxxxx1234", "1103700012341", false},
	}
	for _, tc := range cases {
		if got := matchDigits(tc.slip, tc.stored); got != tc.want {
			t.Errorf("matchDigits(%q, %q) = %v", tc.slip, tc.stored, got)
		}
	}
}

func parseFixture(t *testing.T, name string) *slip.Slip {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("slip", "testdata", "kbank", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var r ocr.Result
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	s, err := slip.Parse("004", &r)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The fixtures' accounts: sender 1234 (masked xxxxx1234x), own second
// account 5678, a card 4000123412341234, a friend's PromptPay 9999.
func TestBuildPendingDirection(t *testing.T) {
	main := uuid.New()
	savings := uuid.New()
	card := uuid.New()
	feeCat := uuid.New()
	wallets := []Wallet{
		{ID: main, Identifiers: []accounts.Identifier{{Kind: "bank_account", Value: "1112212345", BankCode: "004"}}},
		{ID: savings, Identifiers: []accounts.Identifier{{Kind: "other", Value: "xxxxx5678x"}}},
		{ID: card, Identifiers: []accounts.Identifier{{Kind: "card", Value: "4000123412341234"}}},
	}
	// 1112212345 → visible "1234" at positions 5–8, like the slip's xxxxx1234x.
	ctx := DraftContext{Wallets: wallets, FeeCategoryID: &feeCat}

	type want struct {
		typ      string
		from, to *uuid.UUID
		guessed  string
	}
	cases := map[string]want{
		"transfer_own_account":     {typeTransfer, &main, &savings, "identifiers"},
		"transfer_memo_hotel":      {typeTransfer, &main, &savings, "identifiers"},
		"bill_card":                {typeTransfer, &main, &card, "identifiers"},
		"transfer_promptpay_phone": {typeExpense, &main, nil, "identifiers"},
		"payment_merchant_wrap":    {typeExpense, &main, nil, "identifiers"},
	}
	for name, w := range cases {
		items := buildPending(parseFixture(t, name), "REF", "004", ctx)
		d := items[0].Draft
		if *d.Type != w.typ || !sameID(d.AccountID, w.from) || !sameID(d.TransferToAccountID, w.to) {
			t.Errorf("%s: type %s from %v to %v", name, *d.Type, d.AccountID, d.TransferToAccountID)
		}
		if items[0].SourceRef.Guessed.Type != w.guessed {
			t.Errorf("%s: guessed %+v", name, items[0].SourceRef.Guessed)
		}
	}

	// No identifiers at all → expense, no wallet.
	items := buildPending(parseFixture(t, "transfer_own_account"), "REF", "004", DraftContext{})
	if d := items[0].Draft; *d.Type != typeExpense || d.AccountID != nil {
		t.Errorf("default: %+v", d)
	}

	// Only the receiver is ours → income into it.
	onlySavings := DraftContext{Wallets: wallets[1:2]}
	items = buildPending(parseFixture(t, "transfer_own_account"), "REF", "004", onlySavings)
	if d := items[0].Draft; *d.Type != typeIncome || !sameID(d.AccountID, &savings) {
		t.Errorf("income: %+v", d)
	}

	// The QR's bank must agree with a bank-coded identifier.
	items = buildPending(parseFixture(t, "transfer_promptpay_phone"), "REF", "014", ctx)
	if d := items[0].Draft; d.AccountID != nil {
		t.Errorf("bank mismatch still matched: %+v", d)
	}

	// Two wallets with the same number → unsure → no wallet.
	twice := DraftContext{Wallets: []Wallet{wallets[0], {ID: uuid.New(), Identifiers: wallets[0].Identifiers}}}
	items = buildPending(parseFixture(t, "transfer_promptpay_phone"), "REF", "004", twice)
	if d := items[0].Draft; d.AccountID != nil {
		t.Errorf("ambiguous still matched: %+v", d)
	}
}

func TestBuildPendingFee(t *testing.T) {
	main := uuid.New()
	feeCat := uuid.New()
	s := parseFixture(t, "transfer_promptpay_phone")
	fee := 10.0
	s.Fee = &fee
	ctx := DraftContext{
		Wallets:       []Wallet{{ID: main, Identifiers: []accounts.Identifier{{Kind: "other", Value: "xxxxx1234x"}}}},
		FeeCategoryID: &feeCat,
	}
	items := buildPending(s, "REF", "004", ctx)
	if len(items) != 2 {
		t.Fatalf("items = %d", len(items))
	}
	f := items[1]
	if f.SourceRef.Part != partFee || *f.Draft.Amount != 10 || !sameID(f.Draft.AccountID, &main) ||
		!sameID(f.Draft.CategoryID, &feeCat) || f.SourceRef.Guessed.Category != "fee_setting" {
		t.Errorf("fee draft: %+v %+v", f.Draft, f.SourceRef)
	}

	// Income: the sender paid the fee — no fee draft.
	ctx.Wallets = []Wallet{{ID: main, Identifiers: []accounts.Identifier{{Kind: "promptpay", Value: "0812349999"}}}}
	if items := buildPending(s, "REF", "004", ctx); len(items) != 1 || *items[0].Draft.Type != typeIncome {
		t.Errorf("income with fee: %d items, %+v", len(items), items[0].Draft)
	}
}

func sameID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
