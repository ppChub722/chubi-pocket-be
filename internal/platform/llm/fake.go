package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// KeywordFake answers fixed JSON for exact keywords, to try every card and button in Telegram
// without a model (LLM_PROVIDER=fake). Aliases w1 / w2 / c1 are the user's first wallets and
// category; c999 is never on a list. Any other text answers not_money.
type KeywordFake struct {
	SlowFor time.Duration
}

func NewKeywordFake() *KeywordFake { return &KeywordFake{SlowFor: 25 * time.Second} }

// ErrFakeDown is the "provider down" answer of the error keyword.
var ErrFakeDown = errors.New("fake provider: down")

type fakeItem struct {
	Type        string   `json:"type"`
	Amount      *float64 `json:"amount"`
	Description *string  `json:"description"`
	Category    *string  `json:"category"`
	Account     *string  `json:"account"`
	TransferTo  *string  `json:"transfer_to"`
	Date        *string  `json:"date"`
	Note        *string  `json:"note"`
	Unsure      []string `json:"unsure"`
}

type fakeAnswer struct {
	Items     []fakeItem `json:"items"`
	ReplyHint *string    `json:"reply_hint"`
}

func num(f float64) *float64 { return &f }
func str(s string) *string   { return &s }

func spend(amount float64, desc, cat string) fakeItem {
	return fakeItem{Type: "expense", Amount: num(amount), Description: str(desc), Category: str(cat), Unsure: []string{}}
}

// keywordAnswers: the descriptions are the keyword's own words, so the verbatim check keeps them
// (except fake desc, whose point is to be dropped).
var keywordAnswers = map[string]fakeAnswer{
	"normal tx": {Items: []fakeItem{spend(60, "normal", "c1")}},
	"multi tx": {Items: []fakeItem{
		spend(60, "multi", "c1"),
		spend(45, "tx", "c1"),
		{Type: "income", Amount: num(500), Description: str("multi"), Unsure: []string{}},
	}},
	"income":   {Items: []fakeItem{{Type: "income", Amount: num(25000), Description: str("income"), Unsure: []string{}}}},
	"transfer": {Items: []fakeItem{{Type: "transfer", Amount: num(5000), Account: str("w1"), TransferTo: str("w2"), Description: str("transfer"), Unsure: []string{}}}},
	"unsure": {Items: []fakeItem{{Type: "expense", Amount: num(120), Description: str("unsure"), Category: str("c1"),
		Unsure: []string{"category"}}}},
	"bad alias": {Items: []fakeItem{spend(80, "bad", "c999")}},
	"partial": {Items: []fakeItem{
		spend(30, "partial", "c1"),
		{Type: "expense", Amount: num(0), Description: str("partial"), Unsure: []string{}},
		{Type: "transfer", Amount: num(100), Account: str("w1"), Unsure: []string{}},
	}},
	"fake desc":        {Items: []fakeItem{spend(99, "อาหารกลางวันมื้อใหญ่", "c1")}},
	"no amount":        {Items: []fakeItem{}, ReplyHint: str("no_amount")},
	"not money":        {Items: []fakeItem{}, ReplyHint: str("not_money")},
	"transfer missing": {Items: []fakeItem{}, ReplyHint: str("transfer_wallet_missing")},
}

// FakeKeywords is the order "all" runs them in: every non-reply case, slow last.
var FakeKeywords = []string{
	"normal tx", "multi tx", "income", "transfer", "unsure", "bad alias", "partial", "fake desc",
	"no amount", "not money", "transfer missing", "refuse", "error", "slow",
}

func (f *KeywordFake) Complete(ctx context.Context, r Request) (Result, error) {
	key := strings.ToLower(strings.TrimSpace(r.Input))
	done := func(a fakeAnswer) (Result, error) {
		raw, _ := json.Marshal(a)
		return Result{Text: string(raw), Stop: StopDone, Model: "keywords", Usage: Usage{In: 1, Out: 1}}, nil
	}
	if r.Current != nil {
		switch key {
		case "edit":
			var cur fakeAnswer
			if err := json.Unmarshal(r.Current, &cur); err != nil || len(cur.Items) == 0 {
				return done(fakeAnswer{Items: []fakeItem{}, ReplyHint: str("no_amount")})
			}
			cur.Items[0].Amount = num(50)
			return done(cur)
		case "edit empty":
			return done(fakeAnswer{Items: []fakeItem{}, ReplyHint: str("no_amount")})
		}
	}
	switch key {
	case "refuse":
		return Result{Stop: StopRefused, Model: "keywords"}, nil
	case "error":
		return Result{}, ErrFakeDown
	case "slow":
		select {
		case <-time.After(f.SlowFor):
			return done(keywordAnswers["normal tx"])
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	if a, ok := keywordAnswers[key]; ok {
		return done(a)
	}
	return done(fakeAnswer{Items: []fakeItem{}, ReplyHint: str("not_money")})
}
