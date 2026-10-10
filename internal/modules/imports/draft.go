package imports

import (
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/imports/slip"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
)

// DraftContext — the user's side of turning a slip into drafts: their
// active wallets (with identifiers) and their fee category.
type DraftContext struct {
	Wallets       []Wallet
	FeeCategoryID *uuid.UUID
}

const (
	typeExpense  = "expense"
	typeIncome   = "income"
	typeTransfer = "transfer"
)

// buildPending — a read slip → its pending drafts (spec 15 §5.3, §7).
// Where our wallet shows up decides the sign:
//
//	sender only   → expense from it      (+ fee draft, we paid it)
//	receiver only → income into it       (no fee draft, the sender paid)
//	both          → transfer between     (+ fee draft from the sender)
//	neither       → expense, no wallet   (+ fee draft)
//
// Incomplete slips get the main draft only.
func buildPending(s *slip.Slip, transRef, bankCode string, ctx DraftContext) []PendingItem {
	ref := SlipRef{
		Type:       "slip",
		Part:       partMain,
		TransRef:   transRef,
		BankCode:   bankCode,
		SlipKind:   s.Kind,
		OccurredAt: s.OccurredAt,
		Memo:       s.Memo,
		Guessed:    Guessed{Type: "default", Account: "none", Category: "none"},
	}

	var from, to *uuid.UUID
	if s.Sender != nil {
		ref.SenderMasked = s.Sender.Masked
		// The QR's bank is the sender's.
		from = matchWallet(ctx.Wallets, s.Sender.Masked, senderKinds, bankCode)
	}
	if s.Receiver != nil {
		ref.Payee = s.Receiver.Name
		ref.ReceiverMasked = s.Receiver.Masked
		to = matchWallet(ctx.Wallets, s.Receiver.Masked, receiverKinds[s.Receiver.Kind], "")
	}
	if from != nil && to != nil && *from == *to {
		to = nil // one wallet can't be both ends
	}

	var date *string
	if s.OccurredAt != nil {
		d := s.OccurredAt.In(slip.Bangkok).Format("2006-01-02")
		date = &d
	}
	// What it was for = who got the money; the slip's memo is the note.
	main := pending.Draft{Amount: s.Amount, Date: date}
	if ref.Payee != "" {
		payee := ref.Payee
		main.Description = &payee
	}
	if s.Memo != "" {
		memo := s.Memo
		main.Note = &memo
	}
	txType := typeExpense
	wePaid := true // the fee is ours
	switch {
	case from != nil && to != nil:
		txType = typeTransfer
		main.AccountID, main.TransferToAccountID = from, to
	case from != nil:
		main.AccountID = from
	case to != nil:
		txType = typeIncome
		main.AccountID = to
		wePaid = false
	}
	main.Type = &txType
	if from != nil || to != nil {
		ref.Guessed.Type, ref.Guessed.Account = "identifiers", "identifier"
	}
	items := []PendingItem{{Source: sourceOCR, Kind: kindCreate, Draft: main, SourceRef: ref}}

	if wePaid && len(s.Missing) == 0 && s.Fee != nil && *s.Fee > 0 {
		feeRef := ref
		feeRef.Part = partFee
		feeDesc := "ค่าธรรมเนียม"
		if ref.Payee != "" {
			feeDesc += " · " + ref.Payee
		}
		expense := typeExpense
		fee := pending.Draft{Type: &expense, Amount: s.Fee, Date: date, Description: &feeDesc, AccountID: from}
		if ctx.FeeCategoryID != nil {
			fee.CategoryID = ctx.FeeCategoryID
			feeRef.Guessed.Category = "fee_setting"
		}
		items = append(items, PendingItem{Source: sourceOCR, Kind: kindCreate, Draft: fee, SourceRef: feeRef})
	}
	return items
}
