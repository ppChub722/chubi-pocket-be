package pending

// What a draft still needs before it can be confirmed (0.4.0): the page shows
// "ต้องใส่ข้อมูล" on it and a bulk confirm takes only the ready ones.

import (
	"strings"
)

// Missing keys of PendingTransaction.needs_input.
const (
	NeedAmount   = "amount"    // hard: confirm is rejected (MISSING_AMOUNT)
	NeedType     = "type"      // hard
	NeedDate     = "date"      // hard
	NeedWallet   = "wallet"    // a bill without a wallet is valid, so soft; hard for a transfer
	NeedWalletTo = "wallet_to" // a transfer's destination: hard
	NeedCategory = "category"  // soft: an uncategorized bill is valid
)

// readiness — the keys a draft is still missing, and whether it can be
// confirmed at all (no hard key missing). A draft with no soft / hard key is
// "ready" (the bulk confirm takes it).
func readiness(p *PendingTransaction) (missing []string, canConfirm bool) {
	missing = []string{}
	hard := false
	add := func(k string, isHard bool) {
		missing = append(missing, k)
		hard = hard || isHard
	}
	d := p.Draft
	switch p.Kind {
	case KindUpdateTx:
		// an edit of an existing transaction: nothing is required
	case KindSettleDebt:
		if d.Amount == nil || *d.Amount <= 0 {
			add(NeedAmount, true)
		}
	default: // create, saving
		if d.Amount == nil || *d.Amount <= 0 {
			add(NeedAmount, true)
		}
		if d.Date == nil || strings.TrimSpace(*d.Date) == "" {
			add(NeedDate, true)
		}
		if p.Kind == KindCreate {
			switch {
			case d.Type == nil:
				add(NeedType, true)
			case *d.Type == "transfer":
				if d.AccountID == nil {
					add(NeedWallet, true)
				}
				if d.TransferToAccountID == nil {
					add(NeedWalletTo, true)
				}
			default:
				if d.AccountID == nil {
					add(NeedWallet, false)
				}
				if d.CategoryID == nil {
					add(NeedCategory, false)
				}
			}
		}
	}
	return missing, !hard
}

// summarize — the list summary of resolved rows.
func summarize(rows []PendingTransaction) ListSummary {
	s := ListSummary{Count: len(rows)}
	for i := range rows {
		p := &rows[i]
		if len(p.NeedsInput) > 0 {
			s.NeedsInputCount++
		} else {
			s.ReadyCount++
		}
		if p.Kind == KindCreate && p.Draft.Amount != nil && p.Draft.Type != nil {
			switch *p.Draft.Type {
			case "expense":
				s.ExpenseTotal += *p.Draft.Amount
			case "income":
				s.IncomeTotal += *p.Draft.Amount
			}
		}
		switch strings.ToLower(p.Source) {
		case "recurring", "upcoming", "auto_save":
			s.BySource.Recurring++
		case "manual", "chat":
			s.BySource.Manual++
		case "project_update", "project_copy", "split", "partner":
			s.BySource.OtherPeople++
		case "ocr", "slip":
			s.BySource.Slip++
		default:
			s.BySource.Other++
		}
	}
	s.ExpenseTotal, s.IncomeTotal = round2(s.ExpenseTotal), round2(s.IncomeTotal)
	return s
}

func round2(v float64) float64 {
	if v < 0 {
		return -float64(int64(-v*100+0.5)) / 100
	}
	return float64(int64(v*100+0.5)) / 100
}
