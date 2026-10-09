package accounts

import (
	"errors"
	"testing"
)

func TestNormalizeIdentifiers(t *testing.T) {
	got, err := NormalizeIdentifiers([]Identifier{
		{Kind: "bank_account", Value: "123-4-52780-6", BankCode: "004"},
		{Kind: "other", Value: "xxx-x-x2780-x"},
		{Kind: "promptpay", Value: "081 234 4464"},
		{Kind: "bank_account", Value: "1234527806", BankCode: "004"}, // same as the first
		{Kind: "card", Value: "4000 •••• •••• 1234"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Identifier{
		{Kind: "bank_account", Value: "1234527806", BankCode: "004"},
		{Kind: "other", Value: "xxxxx2780x"},
		{Kind: "promptpay", Value: "0812344464"},
		{Kind: "card", Value: "4000xxxxxxxx1234"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestNormalizeIdentifiersRejects(t *testing.T) {
	bad := [][]Identifier{
		{{Kind: "iban", Value: "12345"}},
		{{Kind: "card", Value: "12a45"}},
		{{Kind: "card", Value: "xxx-123"}},                           // 3 digits
		{{Kind: "card", Value: "1234", BankCode: "4"}},               // bank code
		{{Kind: "card", Value: "123456789012345678901234567890123"}}, // 33
	}
	for _, in := range bad {
		if _, err := NormalizeIdentifiers(in); !errors.Is(err, ErrInvalidIdentifier) {
			t.Errorf("%+v → %v", in, err)
		}
	}
	many := make([]Identifier, MaxIdentifiers+1)
	for i := range many {
		many[i] = Identifier{Kind: "other", Value: "1234" + string(rune('0'+i%10)) + string(rune('0'+i/10))}
	}
	if _, err := NormalizeIdentifiers(many); !errors.Is(err, ErrTooManyIdentifiers) {
		t.Errorf("11 identifiers → %v", err)
	}
}
