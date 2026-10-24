package budgets

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type countRow struct {
	Kind        string
	ID          uuid.UUID
	Source      string
	CountsAs    string
	Date        *time.Time
	Amount      float64
	Description *string
	CategoryID  *uuid.UUID
	AccountID   *uuid.UUID
	Tags        []string
	EventID     *uuid.UUID
	ItemID      *uuid.UUID
	// RuleID / Slot name a computed recurring occurrence (ItemID is nil while it
	// is only computed); Link is its recurring link.
	RuleID *uuid.UUID
	Slot   *time.Time
	Link   *ItemRecurring
}

type window struct {
	From, To       time.Time
	Expected       bool
	ExpFrom, ExpTo time.Time
}

// storedItemsSQL is the รอจ่าย branch read from the stored recurring items; it is
// used until an Expecter is set.
const storedItemsSQL = `
	SELECT 'recurring_item', i.id, 'upcoming', 'expected', i.date, (i.draft->>'amount')::float8,
		COALESCE(i.draft->>'description', r.name), (i.draft->>'category_id')::uuid, (i.draft->>'account_id')::uuid,
		ARRAY(SELECT g.name FROM tags g WHERE g.user_id = $1 AND g.id::text IN
			(SELECT jsonb_array_elements_text(COALESCE(i.draft->'tag_ids', '[]'::jsonb))) ORDER BY g.name)::text[],
		NULL::uuid, i.id, NULL::uuid, NULL::date
	FROM recurring_items i LEFT JOIN recurring_rules r ON r.id = i.rule_id
	WHERE $4 AND i.user_id = $1 AND i.produced_at IS NULL AND i.pending_id IS NULL AND i.transaction_id IS NULL
	  AND NOT i.archived AND r.status = 'active'
	  AND i.draft->>'type' = 'expense' AND i.draft ? 'amount' AND i.date BETWEEN $5 AND $6`

// expectedItemsSQL is the same branch read from the occurrences an Expecter
// computed, passed as the JSON parameter $8, so the scope filter below still
// applies to them.
const expectedItemsSQL = `
	SELECT 'recurring_item', x.id, 'upcoming', 'expected', x.date, x.amount,
		x.description, x.category_id, x.account_id,
		ARRAY(SELECT g.name FROM tags g WHERE g.user_id = $1 AND g.id::text IN
			(SELECT jsonb_array_elements_text(COALESCE(x.tag_ids, '[]'::jsonb))) ORDER BY g.name)::text[],
		NULL::uuid, x.item_id, x.rule_id, x.slot
	FROM jsonb_to_recordset($8::jsonb) AS x(id uuid, rule_id uuid, slot date, item_id uuid, date date,
		amount float8, description text, category_id uuid, account_id uuid, tag_ids jsonb)
	WHERE $4 AND x.date BETWEEN $5 AND $6`

func countSQL(expected bool) string {
	items := storedItemsSQL
	if expected {
		items = expectedItemsSQL
	}
	return countSQLHead + items + countSQLTail
}

var countSQLHead = `
WITH ev AS (
	SELECT pt.id, pt.project_id, pt.date, pt.description,
		CASE WHEN pt.transaction_member_id = pm.id
			THEN pt.amount - COALESCE((SELECT SUM(ch.amount) FROM project_transactions ch
				WHERE ch.parent_project_transaction_id = pt.id), 0)
			ELSE COALESCE((SELECT SUM(ch.amount) FROM project_transactions ch
				WHERE ch.parent_project_transaction_id = pt.id AND ch.transaction_member_id = pm.id), 0)
		END AS share,
		(SELECT t2.id FROM transactions t2 WHERE t2.user_id = $1
			AND (t2.id = pt.linked_transaction_id OR t2.source_project_transaction_id = pt.id)
			ORDER BY (t2.id = pt.linked_transaction_id) DESC LIMIT 1) AS my_tx
	FROM project_transactions pt
	JOIN project_members pm ON pm.project_id = pt.project_id AND pm.user_id = $1 AND pm.status = 'active'
	JOIN projects p ON p.id = pt.project_id AND p.status <> 'cancelled'
	WHERE pt.parent_project_transaction_id IS NULL AND pt.type = 'expense' AND pt.date BETWEEN $2 AND $3
),
all_rows AS (
	SELECT 'transaction' AS kind, t.id, CASE WHEN ri.id IS NULL THEN 'normal' ELSE 'recurring' END AS source,
		'used' AS counts_as, t.date, (` + shared.ShareAmountExpr("t") + `)::float8 AS amount, t.description,
		t.category_id, t.account_id,
		ARRAY(SELECT g.name FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id
			WHERE tt.transaction_id = t.id AND g.user_id = $1 ORDER BY g.name)::text[] AS tags,
		NULL::uuid AS event_id, ri.id AS item_id, NULL::uuid AS rule_id, NULL::date AS slot
	FROM transactions t
	LEFT JOIN categories c ON c.id = t.category_id
	LEFT JOIN recurring_items ri ON ri.transaction_id = t.id AND ri.user_id = $1
	WHERE t.user_id = $1 AND ` + shared.ReportScopePredicate("t", "$1") + `
	  AND t.type = 'expense' AND t.date BETWEEN $2 AND $3 AND t.date <= ` + shared.TodaySQL("$1") + `
	  AND t.source_personal_debt_id IS NULL AND t.source_project_transaction_id IS NULL
	  AND (c.id IS NULL OR (NOT c.is_system AND ` + shared.ReportableCategoryPredicate("c") + `))
	  AND NOT EXISTS (SELECT 1 FROM project_transactions lp WHERE lp.linked_transaction_id = t.id)

	UNION ALL
	SELECT 'transaction', t.id, 'future_tx', 'expected', t.date, (` + shared.ShareAmountExpr("t") + `)::float8, t.description,
		t.category_id, t.account_id,
		ARRAY(SELECT g.name FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id
			WHERE tt.transaction_id = t.id AND g.user_id = $1 ORDER BY g.name)::text[],
		NULL::uuid, NULL::uuid, NULL::uuid, NULL::date
	FROM transactions t
	LEFT JOIN categories c ON c.id = t.category_id
	WHERE $4 AND t.user_id = $1 AND ` + shared.ReportScopePredicate("t", "$1") + `
	  AND t.type = 'expense' AND t.date > ` + shared.TodaySQL("$1") + ` AND t.date BETWEEN $5 AND $6
	  AND t.source_personal_debt_id IS NULL AND t.source_project_transaction_id IS NULL
	  AND (c.id IS NULL OR (NOT c.is_system AND ` + shared.ReportableCategoryPredicate("c") + `))
	  AND NOT EXISTS (SELECT 1 FROM project_transactions lp WHERE lp.linked_transaction_id = t.id)

	UNION ALL
	SELECT 'event_bill', ev.id, 'event', CASE WHEN ev.date <= ` + shared.TodaySQL("$1") + ` THEN 'used' ELSE 'expected' END,
		ev.date, ev.share::float8, COALESCE(ev.description, mt.description),
		mt.category_id, mt.account_id,
		ARRAY(SELECT n FROM (
			SELECT et.name AS n FROM project_transaction_tags ptt JOIN event_tags et ON et.id = ptt.event_tag_id
				WHERE ptt.project_transaction_id = ev.id
			UNION SELECT g.name FROM project_my_tags pmt JOIN tags g ON g.id = pmt.tag_id
				WHERE pmt.project_id = ev.project_id AND pmt.user_id = $1
			UNION SELECT g.name FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id
				WHERE tt.transaction_id = ev.my_tx AND g.user_id = $1) z ORDER BY n)::text[],
		ev.project_id, NULL::uuid, NULL::uuid, NULL::date
	FROM ev LEFT JOIN transactions mt ON mt.id = ev.my_tx
	WHERE ev.share > 0
	  AND (ev.date <= ` + shared.TodaySQL("$1") + ` OR ($4 AND ev.date BETWEEN $5 AND $6))

	UNION ALL`

// The recurring-items branch goes between the head and the tail.
const countSQLTail = `

	UNION ALL
	SELECT 'pending', p.id, 'pending', 'expected', (p.draft->>'date')::date, (p.draft->>'amount')::float8,
		p.draft->>'description', (p.draft->>'category_id')::uuid, (p.draft->>'account_id')::uuid,
		ARRAY(SELECT g.name FROM tags g WHERE g.user_id = $1 AND g.id::text IN
			(SELECT jsonb_array_elements_text(COALESCE(p.draft->'tag_ids', '[]'::jsonb))) ORDER BY g.name)::text[],
		(p.draft->>'project_id')::uuid, (SELECT ri.id FROM recurring_items ri WHERE ri.pending_id = p.id),
		NULL::uuid, NULL::date
	FROM pending_transactions p
	WHERE $4 AND p.user_id = $1 AND p.kind = 'create' AND p.draft->>'type' = 'expense'
	  AND p.draft ? 'amount' AND p.draft ? 'date' AND (p.draft->>'date')::date BETWEEN $5 AND $6
)
SELECT s.idx, a.kind, a.id, a.source, a.counts_as, a.date, a.amount, a.description, a.category_id, a.account_id,
	a.tags, a.event_id, a.item_id, a.rule_id, a.slot
FROM all_rows a
JOIN jsonb_to_recordset($7::jsonb) AS s(idx int, from_date date, to_date date, expected bool, exp_from date, exp_to date,
	scope_all bool, cats jsonb, wallets jsonb, tags jsonb)
  ON ((a.counts_as = 'used' AND a.date BETWEEN s.from_date AND s.to_date)
   OR (a.counts_as = 'expected' AND s.expected AND a.date BETWEEN s.exp_from AND s.exp_to
       AND (a.kind <> 'event_bill' OR a.date BETWEEN s.from_date AND s.to_date)))
WHERE s.scope_all OR (
	-- A picked scope narrows: a row must match every list that is set
	-- (categories AND wallets AND tags); within one list any entry will do.
	jsonb_array_length(s.cats) + jsonb_array_length(s.tags) + jsonb_array_length(s.wallets) > 0
	AND (jsonb_array_length(s.cats) = 0 OR (a.category_id IS NOT NULL AND s.cats ? a.category_id::text))
	AND (jsonb_array_length(s.wallets) = 0 OR (a.account_id IS NOT NULL AND s.wallets ? a.account_id::text))
	AND (jsonb_array_length(s.tags) = 0 OR EXISTS (SELECT 1 FROM unnest(a.tags) n WHERE s.tags ? lower(n))))
ORDER BY s.idx`

// expectedJSON is one computed occurrence as the CTE reads it.
type expectedJSON struct {
	ID          uuid.UUID   `json:"id"`
	RuleID      uuid.UUID   `json:"rule_id"`
	Slot        string      `json:"slot"`
	ItemID      *uuid.UUID  `json:"item_id"`
	Date        string      `json:"date"`
	Amount      float64     `json:"amount"`
	Description string      `json:"description"`
	CategoryID  *uuid.UUID  `json:"category_id"`
	AccountID   *uuid.UUID  `json:"account_id"`
	TagIDs      []uuid.UUID `json:"tag_ids"`
}

// occurrenceID is the row id of an occurrence: its stored item, else an id
// derived from the rule and the slot, so the same occurrence keeps one id.
func occurrenceID(o shared.ExpectedOccurrence) uuid.UUID {
	if o.ItemID != nil {
		return *o.ItemID
	}
	return uuid.NewSHA1(o.RuleID, []byte(o.Slot.Format(dateLayout)))
}

// expectedOccurrences asks the Expecter ONCE for what is still to be recorded between
// from and to (the widest window of every budget in the pass) and returns it as the
// JSON the CTE reads, with each row's recurring link by row id. Only expenses with a
// known amount count, as a stored item without an amount always did.
func (s *Service) expectedOccurrences(ctx context.Context, userID uuid.UUID, from, to time.Time) (string, map[uuid.UUID]*ItemRecurring, error) {
	occs, err := s.expected.ExpectedBetween(ctx, userID, from, to)
	if err != nil {
		return "", nil, fmt.Errorf("budget expected: %w", err)
	}
	rows := make([]expectedJSON, 0, len(occs))
	links := make(map[uuid.UUID]*ItemRecurring, len(occs))
	for _, o := range occs {
		if o.Type != "expense" || o.Amount <= 0 {
			continue
		}
		id := occurrenceID(o)
		desc := o.Name
		if desc == "" {
			desc = o.RuleName
		}
		rows = append(rows, expectedJSON{
			ID: id, RuleID: o.RuleID, Slot: o.Slot.Format(dateLayout), ItemID: o.ItemID,
			Date: o.Date.Format(dateLayout), Amount: o.Amount, Description: desc,
			CategoryID: o.CategoryID, AccountID: o.AccountID, TagIDs: o.TagIDs,
		})
		links[id] = &ItemRecurring{
			ItemID: o.ItemID, Seq: o.Seq, Total: o.Total,
			Recurring: &shared.RecurringRef{ID: o.RuleID, Name: o.RuleName, IconCode: o.RuleIcon},
		}
	}
	js, err := json.Marshal(rows)
	if err != nil {
		return "", nil, err
	}
	return string(js), links, nil
}

// collectSpec is one budget and the window whose rows it wants; cats is its scope's
// categories with the sub-categories already expanded.
type collectSpec struct {
	b    *Budget
	w    window
	cats []uuid.UUID
}

// specJSON is one collectSpec as the final join reads it.
type specJSON struct {
	Idx      int      `json:"idx"`
	From     string   `json:"from_date"`
	To       string   `json:"to_date"`
	Expected bool     `json:"expected"`
	ExpFrom  *string  `json:"exp_from"`
	ExpTo    *string  `json:"exp_to"`
	ScopeAll bool     `json:"scope_all"`
	Cats     []string `json:"cats"`
	Wallets  []string `json:"wallets"`
	Tags     []string `json:"tags"`
}

func uuidStrings(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// collectMany reads the rows of every spec in ONE statement (the user's counted rows
// over the widest window, each spec keeping the ones in its window and scope) and ONE
// expected-occurrences call.
func (s *Service) collectMany(ctx context.Context, userID uuid.UUID, specs []collectSpec) ([][]countRow, error) {
	out := make([][]countRow, len(specs))
	if len(specs) == 0 {
		return out, nil
	}
	forever := time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
	from, to := specs[0].w.From, time.Time{}
	var expFrom, expTo time.Time
	anyExpected := false
	js := make([]specJSON, len(specs))
	for i, sp := range specs {
		w := sp.w
		end := w.To
		if end.IsZero() {
			end = forever
		}
		if w.From.Before(from) {
			from = w.From
		}
		if end.After(to) {
			to = end
		}
		tags := make([]string, len(sp.b.TagNames))
		for j, t := range sp.b.TagNames {
			tags[j] = strings.ToLower(t)
		}
		js[i] = specJSON{
			Idx: i, From: w.From.Format(dateLayout), To: end.Format(dateLayout), Expected: w.Expected,
			ScopeAll: sp.b.ScopeAll, Cats: uuidStrings(sp.cats), Wallets: uuidStrings(sp.b.Wallets), Tags: tags,
		}
		if w.Expected {
			ef, et := w.ExpFrom.Format(dateLayout), w.ExpTo.Format(dateLayout)
			js[i].ExpFrom, js[i].ExpTo = &ef, &et
			if !anyExpected || w.ExpFrom.Before(expFrom) {
				expFrom = w.ExpFrom
			}
			if !anyExpected || w.ExpTo.After(expTo) {
				expTo = w.ExpTo
			}
			anyExpected = true
		}
	}
	specsJSON, err := json.Marshal(js)
	if err != nil {
		return nil, err
	}
	args := []any{userID, from, to, anyExpected, expFrom, expTo, string(specsJSON)}
	query := countSQL(false)
	var links map[uuid.UUID]*ItemRecurring
	if s.expected != nil {
		occJSON := "[]"
		if anyExpected {
			if occJSON, links, err = s.expectedOccurrences(ctx, userID, expFrom, expTo); err != nil {
				return nil, err
			}
		}
		args, query = append(args, occJSON), countSQL(true)
	}
	rows, err := s.store.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("budget count: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			idx int
			r   countRow
		)
		if err := rows.Scan(&idx, &r.Kind, &r.ID, &r.Source, &r.CountsAs, &r.Date, &r.Amount, &r.Description,
			&r.CategoryID, &r.AccountID, &r.Tags, &r.EventID, &r.ItemID, &r.RuleID, &r.Slot); err != nil {
			return nil, err
		}
		if r.Kind == "recurring_item" {
			r.Link = links[r.ID]
		}
		if r.Tags == nil {
			r.Tags = []string{}
		}
		out[idx] = append(out[idx], r)
	}
	return out, rows.Err()
}
