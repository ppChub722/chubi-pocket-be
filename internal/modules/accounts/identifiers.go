package accounts

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type Identifier struct {
	Kind string `json:"kind"`
	Value string `json:"value"`
	BankCode string `json:"bank_code,omitempty"`
}

const (
	IdentifierBankAccount = "bank_account"
	IdentifierPromptPay   = "promptpay" // phone or national id
	IdentifierCard        = "card"
	IdentifierOther       = "other"
)

var identifierKinds = map[string]bool{
	IdentifierBankAccount: true, IdentifierPromptPay: true,
	IdentifierCard: true, IdentifierOther: true,
}

const MaxIdentifiers = 10

var (
	ErrInvalidIdentifier  = errors.New("invalid identifier")
	ErrTooManyIdentifiers = errors.New("too many identifiers")
)

var (
	identifierValueRe = regexp.MustCompile(`^[0-9xX×*•\-\s.]+$`)
	bankCodeRe        = regexp.MustCompile(`^\d{3}$`)
)

func NormalizeIdentifierValue(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == 'x' || r == 'X' || r == '×' || r == '*' || r == '•':
			b.WriteByte('x')
		}
	}
	return b.String()
}

func NormalizeIdentifiers(in []Identifier) ([]Identifier, error) {
	out := make([]Identifier, 0, len(in))
	seen := map[string]bool{}
	for i, id := range in {
		kind := strings.TrimSpace(id.Kind)
		if !identifierKinds[kind] {
			return nil, fmt.Errorf("%w: identifiers[%d].kind %q", ErrInvalidIdentifier, i, id.Kind)
		}
		if !identifierValueRe.MatchString(id.Value) {
			return nil, fmt.Errorf("%w: identifiers[%d].value", ErrInvalidIdentifier, i)
		}
		value := NormalizeIdentifierValue(id.Value)
		if strings.Count(value, "x") > len(value)-4 || len(value) > 32 {
			return nil, fmt.Errorf("%w: identifiers[%d].value needs at least 4 digits", ErrInvalidIdentifier, i)
		}
		bank := strings.TrimSpace(id.BankCode)
		if bank != "" && !bankCodeRe.MatchString(bank) {
			return nil, fmt.Errorf("%w: identifiers[%d].bank_code must be 3 digits", ErrInvalidIdentifier, i)
		}
		key := kind + "|" + value + "|" + bank
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Identifier{Kind: kind, Value: value, BankCode: bank})
	}
	if len(out) > MaxIdentifiers {
		return nil, fmt.Errorf("%w: at most %d per wallet", ErrTooManyIdentifiers, MaxIdentifiers)
	}
	return out, nil
}
