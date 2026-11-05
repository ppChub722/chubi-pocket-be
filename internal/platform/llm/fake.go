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
// category; g1 / g2 goals, p1 a contact, e1 an open event (m1 a member), e2 a finished one, r1 a recurring item;
// c999 is never on a list. Any other text answers not_money. Every answer is the model's shape:
// {"items": [{"kind", "draft", "unsure"}], "reply_hint"}.
type KeywordFake struct {
	SlowFor time.Duration
}

func NewKeywordFake() *KeywordFake { return &KeywordFake{SlowFor: 25 * time.Second} }

// ErrFakeDown is the "provider down" answer of the error keyword.
var ErrFakeDown = errors.New("fake provider: down")

type obj = map[string]any

// d is a draft from key / value pairs (a key left out is null).
func d(kv ...any) obj {
	out := obj{}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

func row(kind string, draft obj, unsure ...string) obj {
	if unsure == nil {
		unsure = []string{}
	}
	return obj{"kind": kind, "draft": draft, "unsure": unsure}
}

func create(kv ...any) obj { return row("create", d(kv...)) }
func saving(kv ...any) obj { return row("saving", d(append([]any{"type", "saving"}, kv...)...)) }
func debt(side string, money bool, amount float64, who obj, unsure ...string) obj {
	dd := obj{"side": side, "with_money": money}
	for k, v := range who {
		dd[k] = v
	}
	return row("debt", d("amount", amount, "debt", dd), unsure...)
}

func answerOf(items ...obj) obj { return obj{"items": items, "reply_hint": "unknown"} }
func hintOf(h string) obj       { return obj{"items": []obj{}, "reply_hint": h} }

func spend(amount float64, desc, cat string) obj {
	return create("type", "expense", "amount", amount, "description", desc, "category_id", cat)
}

func paper(kv ...any) obj { return d(kv...) }

// keywordAnswers: the descriptions are the keyword's own words, so the verbatim check keeps them
// (except fake desc, whose point is to be dropped).
var keywordAnswers = map[string]obj{
	"normal tx": answerOf(spend(60, "normal", "c1")),
	"multi tx": answerOf(spend(60, "multi", "c1"), spend(45, "tx", "c1"),
		create("type", "income", "amount", 500.0, "description", "multi")),
	"income":   answerOf(create("type", "income", "amount", 25000.0, "description", "income")),
	"transfer": answerOf(create("type", "transfer", "amount", 5000.0, "account_id", "w1", "transfer_to_account_id", "w2", "description", "transfer")),
	"withdraw": answerOf(create("type", "transfer", "amount", 500.0, "account_id", "w1", "description", "withdraw")),
	"deposit":  answerOf(create("type", "transfer", "amount", 1000.0, "transfer_to_account_id", "w2", "description", "deposit")),
	"unsure": answerOf(row("create", d("type", "expense", "amount", 120.0, "description", "unsure", "category_id", "c1"), "category")),
	"bad alias": answerOf(spend(80, "bad", "c999")),
	"partial": answerOf(spend(30, "partial", "c1"),
		create("type", "expense", "amount", 0.0, "description", "partial"),
		create("type", "transfer", "amount", 100.0)),
	"fake desc":        answerOf(spend(99, "อาหารกลางวันมื้อใหญ่", "c1")),
	"no amount":        hintOf("no_amount"),
	"not money":        hintOf("not_money"),
	"transfer missing": hintOf("transfer_wallet_missing"),

	// savings
	"save goal":         answerOf(saving("saving_action", "save", "amount", 2000.0, "goal_id", "g1", "account_id", "w1")),
	"save spare":        answerOf(saving("saving_action", "save", "amount", 2000.0, "account_id", "w1")),
	"save nowallet":     answerOf(saving("saving_action", "save", "amount", 2000.0, "goal_id", "g1")),
	"save unknown goal": answerOf(row("saving", d("type", "saving", "saving_action", "save", "amount", 2000.0, "account_id", "w1"), "goal")),
	"allocate":          answerOf(saving("saving_action", "allocate", "amount", 2000.0, "goal_id", "g1")),
	"allocate nogoal":   answerOf(saving("saving_action", "allocate", "amount", 2000.0)),
	"move goals":        answerOf(saving("saving_action", "move", "amount", 500.0, "from_goal_id", "g1", "goal_id", "g2")),
	"move spare":        answerOf(saving("saving_action", "move", "amount", 500.0, "from_goal_id", "g1", "goal_id", "spare")),
	"move same":         answerOf(saving("saving_action", "move", "amount", 500.0, "from_goal_id", "g1", "goal_id", "g1")),
	"withdraw saving":   answerOf(saving("saving_action", "withdraw", "amount", 500.0, "from_goal_id", "g1", "account_id", "w1")),
	"withdraw nowallet": answerOf(saving("saving_action", "withdraw", "amount", 500.0)),

	// bills in an event and split bills
	"event bill":      answerOf(create("type", "expense", "amount", 3000.0, "category_id", "c1", "paper", paper("project_id", "e1"))),
	"event payer":     answerOf(create("type", "expense", "amount", 900.0, "category_id", "c1", "paper", paper("project_id", "e1", "paid_by", obj{"member_id": "m1"}))),
	"event exclude":   answerOf(create("type", "expense", "amount", 3000.0, "category_id", "c1", "paper", paper("project_id", "e1", "exclude", []string{"m1"}))),
	"event new":       answerOf(create("type", "expense", "amount", 1200.0, "paper", paper("new_event", obj{"name": "event new"}))),
	"event new plain": answerOf(create("type", "expense", "amount", 1200.0, "paper", paper("new_event", obj{"name": "plain"}))),
	"event done":      answerOf(create("type", "expense", "amount", 3000.0, "paper", paper("project_id", "e2"))),
	"event unknown":   answerOf(create("type", "expense", "amount", 3000.0, "paper", paper("project_id", "e9"))),
	"split ลี": answerOf(create("type", "expense", "amount", 600.0, "category_id", "c1",
		"paper", paper("split_mode", "equal", "people", []obj{{"contact_id": "p1"}, {"person_name": "ลี"}}))),
	"split nobody": answerOf(create("type", "expense", "amount", 600.0, "category_id", "c1", "paper", paper("split_mode", "equal", "people", []obj{}))),
	"split payer": answerOf(create("type", "expense", "amount", 600.0, "category_id", "c1",
		"paper", paper("split_mode", "equal", "paid_by", obj{"contact_id": "p1"}, "people", []obj{{"contact_id": "p1"}}))),
	"split plain": answerOf(create("type", "expense", "amount", 600.0, "paper", paper("split_mode", "equal", "people", []obj{{"person_name": "plain"}}))),

	// a payment of a recurring item
	"recurring":          answerOf(create("type", "expense", "amount", 500.0, "recurring_id", "r1")),
	"recurring noamount": answerOf(create("type", "expense", "amount", nil, "recurring_id", "r1")),
	"recurring unknown":  answerOf(create("type", "expense", "amount", 500.0, "recurring_id", "r9", "description", "recurring unknown")),
	"recurring unsure":   answerOf(row("create", d("type", "expense", "amount", 500.0, "recurring_id", "r1"), "recurring")),

	// debts
	"debt lend":   answerOf(debt("they_owe_me", true, 500, obj{"contact_id": "p1"})),
	"debt ledger": answerOf(debt("they_owe_me", false, 500, obj{"contact_id": "p1"})),
	"debt borrow": answerOf(debt("i_owe_them", true, 500, obj{"contact_id": "p1"})),
	"debt owe":    answerOf(debt("i_owe_them", false, 500, obj{"contact_id": "p1"})),
	"debt repay":  answerOf(row("settle_debt", d("amount", 200.0, "debt", obj{"contact_id": "p1", "side": "they_repay", "with_money": true}))),
	"debt unsure": answerOf(debt("they_owe_me", true, 500, obj{"contact_id": "p1"}, "with_money")),
	"debt name ลี": answerOf(debt("they_owe_me", false, 500, obj{"person_name": "ลี"})),
	"debt nobody": answerOf(debt("they_owe_me", false, 500, obj{})),
	"mixed":       answerOf(spend(60, "mixed", "c1"), debt("they_owe_me", false, 200, obj{"contact_id": "p1"})),
}

// FakeKeywords is the order "all" runs them in: every non-reply case, slow last.
var FakeKeywords = []string{
	"normal tx", "multi tx", "income", "transfer", "withdraw", "deposit", "unsure", "bad alias", "partial", "fake desc",
	"no amount", "not money", "transfer missing",
	"save goal", "save spare", "save nowallet", "allocate", "allocate nogoal", "move goals", "withdraw saving",
	"event bill", "event payer", "event new", "event done", "split ลี", "split nobody",
	"recurring", "recurring noamount", "debt lend", "debt ledger", "debt borrow", "debt repay", "debt unsure", "mixed",
	"refuse", "error", "slow",
}

func (f *KeywordFake) Complete(ctx context.Context, r Request) (Result, error) {
	key := strings.ToLower(strings.TrimSpace(r.Input))
	done := func(a obj) (Result, error) {
		raw, _ := json.Marshal(a)
		return Result{Text: string(raw), Stop: StopDone, Model: "keywords", Usage: Usage{In: 1, Out: 1}}, nil
	}
	if r.Current != nil {
		switch key {
		case "edit":
			var cur obj
			items, ok := []any(nil), false
			if err := json.Unmarshal(r.Current, &cur); err == nil {
				items, ok = cur["items"].([]any)
			}
			if !ok || len(items) == 0 {
				return done(hintOf("no_amount"))
			}
			if first, ok := items[0].(map[string]any); ok {
				if draft, ok := first["draft"].(map[string]any); ok {
					draft["amount"] = 50.0
				}
			}
			return done(cur)
		case "edit empty":
			return done(hintOf("no_amount"))
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
	return done(hintOf("not_money"))
}
