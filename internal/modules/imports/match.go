package imports

import (
	"strings"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/accounts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/imports/slip"
)

// Wallet — one of the user's active wallets and the numbers it's known by.
type Wallet struct {
	ID          uuid.UUID
	Identifiers []accounts.Identifier
}

// Which identifier kinds a slip side may match (spec 15 §5.2).
var (
	senderKinds = kindSet(accounts.IdentifierBankAccount, accounts.IdentifierOther)
	// by slip.Party.Kind of the receiver; a merchant's ref is never ours.
	receiverKinds = map[string]map[string]bool{
		slip.ReceiverBankAccount: kindSet(accounts.IdentifierBankAccount, accounts.IdentifierOther),
		slip.ReceiverPromptPay:   kindSet(accounts.IdentifierPromptPay, accounts.IdentifierOther),
		slip.ReceiverBiller: kindSet(accounts.IdentifierCard, accounts.IdentifierBankAccount,
			accounts.IdentifierOther),
	}
)

func kindSet(kinds ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range kinds {
		m[k] = true
	}
	return m
}

// matchWallet — the one wallet with an identifier matching masked, or nil
// when none or more than one does (spec: unsure → empty). bankCode, when
// both it and the identifier's are set, must agree.
func matchWallet(wallets []Wallet, masked string, kinds map[string]bool, bankCode string) *uuid.UUID {
	if masked == "" || kinds == nil {
		return nil
	}
	var found *uuid.UUID
	for _, w := range wallets {
		for _, id := range w.Identifiers {
			if !kinds[id.Kind] {
				continue
			}
			if bankCode != "" && id.BankCode != "" && bankCode != id.BankCode {
				continue
			}
			if !matchDigits(masked, id.Value) {
				continue
			}
			if found != nil && *found != w.ID {
				return nil // two wallets claim it
			}
			wid := w.ID
			found = &wid
			break
		}
	}
	return found
}

// minMatchDigits — digits that must agree before a number counts as ours.
const minMatchDigits = 4

// matchDigits — does a slip's masked number ("xxxxx2780x") fit a stored
// one? Same length: aligned digit by digit, every position where both show
// a digit agrees, at least 4 of them. Different length (the user typed
// part of the number): a run of 4+ visible digits on the slip appears in
// the stored value.
func matchDigits(slipMasked, stored string) bool {
	if len(slipMasked) == len(stored) {
		compared := 0
		for i := 0; i < len(stored); i++ {
			a, b := slipMasked[i], stored[i]
			if a == 'x' || b == 'x' {
				continue
			}
			if a != b {
				return false
			}
			compared++
		}
		return compared >= minMatchDigits
	}
	for _, run := range digitRuns(slipMasked) {
		if len(run) >= minMatchDigits && strings.Contains(stored, run) {
			return true
		}
	}
	return false
}

func digitRuns(s string) []string {
	var runs []string
	start := -1
	for i := 0; i <= len(s); i++ {
		digit := i < len(s) && s[i] >= '0' && s[i] <= '9'
		switch {
		case digit && start < 0:
			start = i
		case !digit && start >= 0:
			runs = append(runs, s[start:i])
			start = -1
		}
	}
	return runs
}
