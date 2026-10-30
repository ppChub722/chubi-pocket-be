package shared

import (
	"context"
	"fmt"
	"math"
	"sync"

	"github.com/google/uuid"
)

type memberRefsKey struct{}

type memberRefCache struct {
	mu   sync.Mutex
	refs map[[2]uuid.UUID]MemberRef // (caller, member id)
}

// WithMemberRefCache lets the member refs one request reads once be reused by every
// LoadMemberRefs after it (an event page asks for the same members in three sections).
func WithMemberRefCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, memberRefsKey{}, &memberRefCache{refs: map[[2]uuid.UUID]MemberRef{}})
}

// RememberMemberRefs hands the cache refs a caller already holds.
func RememberMemberRefs(ctx context.Context, callerUserID uuid.UUID, refs []MemberRef) {
	c, _ := ctx.Value(memberRefsKey{}).(*memberRefCache)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range refs {
		c.refs[[2]uuid.UUID{callerUserID, r.ID}] = r
	}
}

// LoadMemberRefs — event members by id, page-ready (a linked user's live name / icon).
func LoadMemberRefs(ctx context.Context, q Querier, callerUserID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]MemberRef, error) {
	out := map[uuid.UUID]MemberRef{}
	if len(ids) == 0 {
		return out, nil
	}
	cache, _ := ctx.Value(memberRefsKey{}).(*memberRefCache)
	missing := uniqueIDs(ids)
	if cache != nil {
		cache.mu.Lock()
		missing = missing[:0:0]
		for _, id := range uniqueIDs(ids) {
			if r, ok := cache.refs[[2]uuid.UUID{callerUserID, id}]; ok {
				out[id] = r
			} else {
				missing = append(missing, id)
			}
		}
		cache.mu.Unlock()
	}
	if len(missing) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT pm.id, pm.user_id,
			COALESCE(u.display_name, pm.display_name), `+AvatarSQL("u.id", "pm.icon_code")+`, pm.removed_at IS NOT NULL
		FROM project_members pm LEFT JOIN users u ON u.id = pm.user_id
		WHERE pm.id = ANY($1::uuid[])`, missing)
	if err != nil {
		return nil, fmt.Errorf("member refs: %w", err)
	}
	defer rows.Close()
	var loaded []MemberRef
	for rows.Next() {
		var (
			r    MemberRef
			icon []byte
		)
		if err := rows.Scan(&r.ID, &r.UserID, &r.Name, &icon, &r.Removed); err != nil {
			return nil, err
		}
		r.Avatar = AvatarFromSrc(r.Name, icon)
		r.IsMe = r.UserID != nil && *r.UserID == callerUserID
		out[r.ID] = r
		loaded = append(loaded, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	RememberMemberRefs(ctx, callerUserID, loaded)
	return out, nil
}

// BillShare — one person's part of an event bill.
type BillShare struct {
	Member MemberRef `json:"member"`
	Share  float64   `json:"share"`
}

// EventBillPeople — who paid an event bill and who is in it (payer first, with the rest of the bill
// as their share), for the tx detail's event link.
type EventBillPeople struct {
	Payer   MemberRef   `json:"payer"`
	Amount  float64     `json:"amount"` // the bill
	Members []BillShare `json:"members"`

	ProjectID   uuid.UUID  `json:"-"`
	Date        string     `json:"-"`
	Description *string    `json:"-"`
	Note        *string    `json:"-"`
	LinkedTxID  *uuid.UUID `json:"-"` // the transaction the bill was made from (its origin)
}

// BillDifference — one field where my transaction and its event bill differ (mine vs the event's).
type BillDifference struct {
	Field string `json:"field"` // amount | date | description | note
	Mine  any    `json:"mine"`
	Event any    `json:"event"`
}

// TargetAmount — what my transaction's amount is on the bill: the whole bill when it was made from
// my transaction (origin), else my share (a copy).
func (b EventBillPeople) TargetAmount(origin bool) float64 {
	if origin {
		return b.Amount
	}
	return b.MyShare()
}

// Differences — my transaction against its bill, live (tags are copied on apply but never compared).
func (b EventBillPeople) Differences(origin bool, amount float64, date string, description, note *string) []BillDifference {
	out := []BillDifference{}
	if want := b.TargetAmount(origin); math.Abs(want-amount) > 0.005 {
		out = append(out, BillDifference{Field: "amount", Mine: amount, Event: want})
	}
	if b.Date != date {
		out = append(out, BillDifference{Field: "date", Mine: date, Event: b.Date})
	}
	if TextOr(CleanText(description)) != TextOr(CleanText(b.Description)) {
		out = append(out, BillDifference{Field: "description", Mine: CleanText(description), Event: CleanText(b.Description)})
	}
	if TextOr(CleanText(note)) != TextOr(CleanText(b.Note)) {
		out = append(out, BillDifference{Field: "note", Mine: CleanText(note), Event: CleanText(b.Note)})
	}
	return out
}

// TextOr — the text of a nullable string ("" for nil).
func TextOr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// MyShare — the caller's part of the bill (0 when not in it).
func (b EventBillPeople) MyShare() float64 {
	for _, m := range b.Members {
		if m.Member.IsMe {
			return m.Share
		}
	}
	return 0
}

// LoadEventBillPeople — the payer / members / shares of event bills (board parent ids).
func LoadEventBillPeople(ctx context.Context, q Querier, callerUserID uuid.UUID, billIDs []uuid.UUID) (map[uuid.UUID]EventBillPeople, error) {
	out := map[uuid.UUID]EventBillPeople{}
	if len(billIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
		SELECT p.id, p.transaction_member_id, p.amount::float8, NULL::uuid, 0::float8,
		       p.scope_id, to_char(p.date, 'YYYY-MM-DD'), p.description, p.note, p.linked_transaction_id
		FROM project_transactions p WHERE p.id = ANY($1::uuid[])
		UNION ALL
		SELECT sp.split_transaction_id, sp.event_member_id, 0, sp.event_member_id, sp.share::float8,
		       NULL, NULL, NULL, NULL, NULL
		FROM split_transaction_people sp JOIN project_transactions b ON b.id = sp.split_transaction_id
		WHERE sp.split_transaction_id = ANY($1::uuid[]) AND sp.share > 0 AND sp.event_member_id IS NOT NULL
		  AND sp.event_member_id IS DISTINCT FROM b.transaction_member_id
		ORDER BY 1, 4 NULLS FIRST, 2`, uniqueIDs(billIDs))
	if err != nil {
		return nil, fmt.Errorf("bill people: %w", err)
	}
	type line struct {
		bill, member uuid.UUID
		amount       float64
		child        bool
		share        float64
		project      *uuid.UUID
		date         *string
		desc, note   *string
		linked       *uuid.UUID
	}
	var (
		lines     []line
		memberIDs []uuid.UUID
	)
	for rows.Next() {
		var (
			l     line
			child *uuid.UUID
		)
		if err := rows.Scan(&l.bill, &l.member, &l.amount, &child, &l.share,
			&l.project, &l.date, &l.desc, &l.note, &l.linked); err != nil {
			rows.Close()
			return nil, err
		}
		l.child = child != nil
		lines = append(lines, l)
		memberIDs = append(memberIDs, l.member)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	refs, err := LoadMemberRefs(ctx, q, callerUserID, memberIDs)
	if err != nil {
		return nil, err
	}
	payers := map[uuid.UUID]uuid.UUID{}
	for _, l := range lines {
		if !l.child {
			payers[l.bill] = l.member
			b := EventBillPeople{Payer: refs[l.member], Amount: l.amount, Members: []BillShare{},
				Description: l.desc, Note: l.note, LinkedTxID: l.linked}
			if l.project != nil {
				b.ProjectID = *l.project
			}
			if l.date != nil {
				b.Date = *l.date
			}
			out[l.bill] = b
		}
	}
	for _, l := range lines {
		if l.child {
			b := out[l.bill]
			b.Members = append(b.Members, BillShare{Member: refs[l.member], Share: l.share})
			out[l.bill] = b
		}
	}
	for id, b := range out {
		rest := b.Amount
		for _, m := range b.Members {
			rest -= m.Share
		}
		if rest > 0.005 {
			b.Members = append([]BillShare{{Member: refs[payers[id]], Share: round2(rest)}}, b.Members...)
		}
		out[id] = b
	}
	return out, nil
}
