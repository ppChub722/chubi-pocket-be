package budgets

// What a budget counts, in ONE query per date range (owner 2026-10-13):
//   - normal / recurring — my expense transactions (my share), not a debt
//     entry's wallet side, not a system / non-report category, not a
//     transaction an event bill was made from or a copy of my share of a
//     bill (those count as the bill);
//   - event — my share of event bills (payer: amount − every split; else my
//     splits), events not cancelled; category / wallet / tags of my
//     transaction behind the bill when there is one;
//   - upcoming — recurring items not made yet (expected);
//   - pending — pending drafts (expected).
// Scope: everything, or a row matching ANY category / tag name / wallet.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// countRow — one counted thing.
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
}

// window — what to collect: used rows dated [From, To] (To nil = no end);
// expected rows dated [ExpFrom, ExpTo] when Expected.
type window struct {
	From, To       time.Time
	Expected       bool
	ExpFrom, ExpTo time.Time
}

var countSQL = `
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
		NULL::uuid AS event_id, ri.id AS item_id
	FROM transactions t
	LEFT JOIN categories c ON c.id = t.category_id
	LEFT JOIN recurring_items ri ON ri.transaction_id = t.id AND ri.user_id = $1
	WHERE t.user_id = $1 AND ` + shared.ReportScopePredicate("t", "$1") + `
	  AND t.type = 'expense' AND t.date BETWEEN $2 AND $3
	  AND t.source_personal_debt_id IS NULL AND t.source_project_transaction_id IS NULL
	  AND (c.id IS NULL OR (NOT c.is_system AND ` + shared.ReportableCategoryPredicate("c") + `))
	  AND NOT EXISTS (SELECT 1 FROM project_transactions lp WHERE lp.linked_transaction_id = t.id)

	UNION ALL
	SELECT 'event_bill', ev.id, 'event', 'used', ev.date, ev.share::float8, COALESCE(ev.description, mt.description),
		mt.category_id, mt.account_id,
		ARRAY(SELECT n FROM (
			SELECT et.name AS n FROM project_transaction_tags ptt JOIN event_tags et ON et.id = ptt.event_tag_id
				WHERE ptt.project_transaction_id = ev.id
			UNION SELECT g.name FROM project_my_tags pmt JOIN tags g ON g.id = pmt.tag_id
				WHERE pmt.project_id = ev.project_id AND pmt.user_id = $1
			UNION SELECT g.name FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id
				WHERE tt.transaction_id = ev.my_tx AND g.user_id = $1) z ORDER BY n)::text[],
		ev.project_id, NULL::uuid
	FROM ev LEFT JOIN transactions mt ON mt.id = ev.my_tx
	WHERE ev.share > 0

	UNION ALL
	SELECT 'recurring_item', i.id, 'upcoming', 'expected', i.date, (i.draft->>'amount')::float8,
		COALESCE(i.draft->>'description', r.name), (i.draft->>'category_id')::uuid, (i.draft->>'account_id')::uuid,
		ARRAY(SELECT g.name FROM tags g WHERE g.user_id = $1 AND g.id::text IN
			(SELECT jsonb_array_elements_text(COALESCE(i.draft->'tag_ids', '[]'::jsonb))) ORDER BY g.name)::text[],
		NULL::uuid, i.id
	FROM recurring_items i LEFT JOIN recurring_rules r ON r.id = i.rule_id
	WHERE $8 AND i.user_id = $1 AND i.produced_at IS NULL AND i.pending_id IS NULL AND i.transaction_id IS NULL
	  AND NOT i.archived AND (i.rule_id IS NULL OR r.status = 'active')
	  AND i.draft->>'type' = 'expense' AND i.draft ? 'amount' AND i.date BETWEEN $9 AND $10

	UNION ALL
	SELECT 'pending', p.id, 'pending', 'expected', (p.draft->>'date')::date, (p.draft->>'amount')::float8,
		p.draft->>'description', (p.draft->>'category_id')::uuid, (p.draft->>'account_id')::uuid,
		ARRAY(SELECT g.name FROM tags g WHERE g.user_id = $1 AND g.id::text IN
			(SELECT jsonb_array_elements_text(COALESCE(p.draft->'tag_ids', '[]'::jsonb))) ORDER BY g.name)::text[],
		(p.draft->>'project_id')::uuid, (SELECT ri.id FROM recurring_items ri WHERE ri.pending_id = p.id)
	FROM pending_transactions p
	WHERE $8 AND p.user_id = $1 AND p.kind = 'create' AND p.draft->>'type' = 'expense'
	  AND p.draft ? 'amount' AND p.draft ? 'date' AND (p.draft->>'date')::date BETWEEN $9 AND $10
)
SELECT kind, id, source, counts_as, date, amount, description, category_id, account_id, tags, event_id, item_id
FROM all_rows a
WHERE $4 OR a.category_id = ANY($5) OR a.account_id = ANY($7)
   OR EXISTS (SELECT 1 FROM unnest(a.tags) n WHERE lower(n) = ANY($6))`

// collect runs countSQL for budget b over w.
func (s *Service) collect(ctx context.Context, b *Budget, w window) ([]countRow, error) {
	cats, err := s.store.expandCategories(ctx, b.Categories)
	if err != nil {
		return nil, err
	}
	tags := make([]string, len(b.TagNames))
	for i, t := range b.TagNames {
		tags[i] = strings.ToLower(t)
	}
	wallets := b.Wallets
	if wallets == nil {
		wallets = []uuid.UUID{}
	}
	if cats == nil {
		cats = []uuid.UUID{}
	}
	to := w.To
	if to.IsZero() {
		to = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
	}
	rows, err := s.store.db.Query(ctx, countSQL, b.UserID, w.From, to, b.ScopeAll, cats, tags, wallets,
		w.Expected, w.ExpFrom, w.ExpTo)
	if err != nil {
		return nil, fmt.Errorf("budget count: %w", err)
	}
	defer rows.Close()
	var out []countRow
	for rows.Next() {
		var r countRow
		if err := rows.Scan(&r.Kind, &r.ID, &r.Source, &r.CountsAs, &r.Date, &r.Amount, &r.Description,
			&r.CategoryID, &r.AccountID, &r.Tags, &r.EventID, &r.ItemID); err != nil {
			return nil, err
		}
		if r.Tags == nil {
			r.Tags = []string{}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
