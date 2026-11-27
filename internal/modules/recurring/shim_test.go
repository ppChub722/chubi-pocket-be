package recurring

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// The engine tests were written against the 0.4.2 per-setting API. A setting is now a rule of a
// recurring; these helpers turn their calls into the recurring API (a one-rule recurring whose rule
// shares its id) and read the rule's engine view back (Detail) for their asserts.

func str(v any) string {
	s, _ := v.(string)
	return s
}

// legacyItemType — the rule type of a 0.4.2 setting body.
func legacyItemType(kind, typ string) string {
	switch kind {
	case KindSubscription, KindInstallment:
		return TypeExpense
	case KindIncome:
		return TypeIncome
	case KindReminder:
		if typ == "transfer" {
			return TypeTransfer
		}
		return TypeExpense // asks its amount (legacyCreate)
	case KindSaving:
		return TypeSaving
	case KindDebt:
		return TypeDebt
	}
	return typ
}

func toMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m
}

// legacyDirection — a 0.4.2 setting's direction was its rows' (a repayment setting: the way back);
// a rule input's is its debt's.
func legacyDirection(dir, kind any) any {
	if str(kind) != DebtKindRepayment {
		return dir
	}
	switch str(dir) {
	case "paid":
		return "received"
	case "received":
		return "paid"
	}
	return dir
}

// legacyCreate — a 0.4.2 create body as a RecurringInput body.
func legacyCreate(body map[string]any) map[string]any {
	end, _ := body["end"].(map[string]any)
	in := map[string]any{}
	for _, k := range []string{"name", "description", "note", "icon_code"} {
		if v, ok := body[k]; ok {
			in[k] = v
		}
	}
	line := map[string]any{"source": map[string]any{"account_id": body["account_id"]}}
	for _, k := range []string{"category_id", "tag_ids", "goal_id", "amount", "to_account_id", "debt_kind"} {
		if v, ok := body[k]; ok {
			line[k] = v
		}
	}
	if v, ok := body["auto_create"]; ok {
		line["auto_accept"] = v
	}
	if t := legacyItemType(str(body["kind"]), str(body["type"])); t != "" {
		line["type"] = t
	} else if k, ok := body["kind"]; ok {
		line["type"] = k // an unknown kind: let the API refuse it
	}
	if _, ok := body["contact_id"]; ok || body["person_name"] != nil {
		line["counterparty"] = map[string]any{"contact_id": body["contact_id"], "person_name": body["person_name"]}
	}
	if v, ok := body["direction"]; ok {
		line["debt_direction"] = legacyDirection(v, body["debt_kind"])
	}
	if str(body["kind"]) == KindReminder && str(body["type"]) != "transfer" {
		line["amount_mode"] = AmountAsk // a 0.4.2 bill reminder: its amount is typed each round
	}
	if rule, ok := body["rule"].(map[string]any); ok {
		sc := map[string]any{}
		for k, v := range rule {
			sc[k] = v
		}
		sc["start_date"] = body["start_date"]
		if end != nil {
			sc["end"] = end
		}
		line["schedule"] = sc
	}
	in["rules"] = []any{line}
	return in
}

// legacyUpdate — a 0.4.2 partial edit of setting id as a full RecurringInput body.
func (f *recFix) legacyUpdate(id uuid.UUID, body map[string]any) map[string]any {
	f.t.Helper()
	p, err := f.svc.GetRecurring(f.ctx, f.me, id, "")
	if err != nil {
		return legacyCreate(body)
	}
	in := map[string]any{"name": p.Name, "description": p.Description, "note": p.Note, "icon_code": p.IconCode}
	ln := p.Lines[0]
	line := map[string]any{"id": ln.ID, "type": ln.Type, "category_id": ln.CategoryID, "tag_ids": ln.TagIDs, "goal_id": ln.GoalID,
		"debt_direction": ln.DebtDirection, "amount": ln.Amount, "source": toMap(ln.Source), "to_account_id": ln.ToAccountID,
		"debt_kind": ln.DebtKind, "auto_accept": ln.AutoAccept}
	if ln.AmountMode == AmountAsk {
		line["amount_mode"] = AmountAsk
	}
	if c := ln.Counterparty; c != nil && ln.DebtPlanID == nil {
		cp := map[string]any{}
		switch {
		case c.Institution != nil:
			cp["institution_id"] = c.Institution.ID
		case c.Person != nil && c.Person.ContactID != nil:
			cp["contact_id"] = c.Person.ContactID
		default:
			cp["person_name"] = c.Name
		}
		line["counterparty"] = cp
	}
	for _, k := range []string{"name", "description", "note", "icon_code"} {
		if v, ok := body[k]; ok {
			in[k] = v
		}
	}
	for _, k := range []string{"category_id", "tag_ids", "goal_id", "amount", "to_account_id", "debt_kind"} {
		if v, ok := body[k]; ok {
			line[k] = v
		}
	}
	if k, ok := body["kind"]; ok {
		line["type"] = legacyItemType(str(k), ln.Type)
	}
	if v, ok := body["direction"]; ok {
		kind := any(ln.DebtKind)
		if k, ok := body["debt_kind"]; ok {
			kind = k
		} else if ln.DebtKind != nil {
			kind = *ln.DebtKind
		}
		line["debt_direction"] = legacyDirection(v, kind)
	}
	if _, ok := body["contact_id"]; ok {
		line["counterparty"] = map[string]any{"contact_id": body["contact_id"]}
	}
	if _, ok := body["person_name"]; ok {
		line["counterparty"] = map[string]any{"person_name": body["person_name"]}
	}
	if v, ok := body["auto_create"]; ok {
		line["auto_accept"] = v
	}
	if v, ok := body["account_id"]; ok {
		line["source"] = map[string]any{"account_id": v}
	}
	_, r1 := body["rule"]
	_, r2 := body["start_date"]
	_, r3 := body["end"]
	if r1 || r2 || r3 {
		sc := toMap(ln.Schedule)
		if rule, ok := body["rule"].(map[string]any); ok {
			for _, k := range []string{"freq", "interval", "weekdays", "monthly", "month_days", "months", "dates"} {
				delete(sc, k)
			}
			for k, v := range rule {
				sc[k] = v
			}
		}
		if v, ok := body["start_date"]; ok {
			sc["start_date"] = v
		}
		if v, ok := body["end"]; ok {
			sc["end"] = v
		}
		line["schedule"] = sc
	}
	in["rules"] = []any{line}
	return in
}

var (
	reOcc    = regexp.MustCompile(`^/recurring/([0-9a-f-]{36})/occurrences/(\d{4}-\d{2}-\d{2})(.*)$`)
	reExtend = regexp.MustCompile(`^/recurring/([0-9a-f-]{36})/extend$`)
	reItem   = regexp.MustCompile(`^/recurring/([0-9a-f-]{36})`)
)

// shim — a 0.4.2 call as its route, operation and body now; the setting (rule) it is about.
func (f *recFix) shim(method, path, op string, body any) (string, string, any) {
	if m := reOcc.FindStringSubmatch(path); m != nil {
		path = "/recurring/" + m[1] + "/rules/" + m[1] + "/occurrences/" + m[2] + m[3]
	}
	if m := reExtend.FindStringSubmatch(path); m != nil {
		path, op = "/recurring/"+m[1]+"/rules/"+m[1]+"/extend", strings.Replace(op, "extendRecurring", "extendRecurringRule", 1)
	}
	path = strings.Replace(path, "rule_id=", "recurring_rule_id=", 1)
	if b, ok := body.(map[string]any); ok {
		switch {
		case method == "POST" && path == "/recurring":
			body = legacyCreate(b)
		case method == "PUT" && reItem.MatchString(path) && !strings.Contains(path, "/rules/"):
			id, _ := uuid.Parse(reItem.FindStringSubmatch(path)[1])
			body = f.legacyUpdate(id, b)
		}
	}
	return path, op, body
}

// fillDetail — the engine view (0.4.2 Detail) of the rule of a recurring response.
func (f *recFix) fillDetail(path string, raw []byte, out *Detail) {
	f.t.Helper()
	var id uuid.UUID
	var p struct {
		ID uuid.UUID `json:"id"`
	}
	if json.Unmarshal(raw, &p) == nil && p.ID != uuid.Nil {
		id = p.ID
	} else if m := reItem.FindStringSubmatch(path); m != nil {
		id, _ = uuid.Parse(m[1])
	}
	today, err := f.svc.today(f.ctx, f.me, "")
	if err != nil {
		f.t.Fatal(err)
	}
	lines, err := linesOf(f.ctx, f.pool, f.me, []uuid.UUID{id}, false)
	if err != nil || len(lines) == 0 {
		f.t.Fatalf("no rule for %s: %v", id, err)
	}
	d, err := f.svc.detail(f.ctx, f.me, lines[0].ID, today, 1, 20, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	*out = *d
}
