package pending

import (
	"strings"
)

const (
	NeedAmount   = "amount"    // hard: confirm is rejected (MISSING_AMOUNT)
	NeedType     = "type"      // hard
	NeedDate     = "date"      // hard
	NeedWallet   = "wallet"    // a bill without a wallet is valid, so soft; hard for a transfer
	NeedWalletTo = "wallet_to" // a transfer's destination: hard
	NeedCategory = "category"  // soft: an uncategorized bill is valid
	NeedPeople   = "people"    // a shared-bill draft with nobody on it: hard
	NeedPerson   = "person"    // a debt draft with nobody named: hard
)

func readiness(p *PendingTransaction) (missing []string, canConfirm bool) {
	missing, hard := readinessDetail(p)
	return missing, len(hard) == 0
}

// readinessDetail is readiness with the hard ones listed: what a draft still needs before it can be confirmed.
func readinessDetail(p *PendingTransaction) (missing, hard []string) {
	missing = []string{}
	hard = []string{}
	add := func(k string, isHard bool) {
		missing = append(missing, k)
		if isHard {
			hard = append(hard, k)
		}
	}
	d := p.Draft
	switch p.Kind {
	case KindUpdateTx:
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
			case d.Debt != nil:
				if d.Debt.ContactID == nil && (d.Debt.PersonName == nil || strings.TrimSpace(*d.Debt.PersonName) == "") {
					add(NeedPerson, true)
				}
				if d.Debt.WithMoney && d.AccountID == nil {
					add(NeedWallet, true)
				}
			case d.Type == nil:
				add(NeedType, true)
			case *d.Type == "transfer":
				// One side is enough (ถอนเงิน / เพิ่มเงิน).
				if d.AccountID == nil && d.TransferToAccountID == nil {
					add(NeedWallet, true)
				}
			default:
				if d.AccountID == nil {
					add(NeedWallet, false)
				}
				if d.CategoryID == nil {
					add(NeedCategory, false)
				}
			}
			if d.Paper != nil && len(d.Paper.People) == 0 {
				add(NeedPeople, true)
			}
		}
	}
	return missing, hard
}

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
			amount := *p.Draft.Amount
			if p.Draft.Paper != nil {
				amount = p.Draft.Paper.MyShare(amount)
			}
			switch *p.Draft.Type {
			case "expense":
				s.ExpenseTotal += amount
			case "income":
				s.IncomeTotal += amount
			}
		}
		switch strings.ToLower(p.Source) {
		case "recurring", "upcoming", "auto_save":
			s.BySource.Recurring++
		case "manual", "chat", SourceTelegram:
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
