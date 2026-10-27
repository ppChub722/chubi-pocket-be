package projects

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type myMember struct {
	id     uuid.UUID
	role   string
	status string
}

func (m *myMember) canWrite() bool {
	return m != nil && m.status == MemberStatusActive && m.role != RoleViewer
}

func loadEvents(ctx context.Context, q shared.Querier, callerID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]Event, error) {
	out := map[uuid.UUID]Event{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
		WITH me AS (
			SELECT pm.project_id, pm.id, pm.role, pm.status FROM project_members pm
			WHERE pm.project_id = ANY($1) AND pm.user_id = $2
		)
		SELECT p.id, p.owner_user_id, p.name, p.description, p.note,
		       to_char(p.start_date, 'YYYY-MM-DD'), to_char(p.end_date, 'YYYY-MM-DD'),
		       p.status, p.icon_code, p.planned_amount, p.created_at, p.updated_at,
		       (SELECT COUNT(*) FROM project_members m WHERE m.project_id = p.id AND m.status = 'active'),
		       COALESCE((SELECT SUM(amount) FROM project_transactions x
		                 WHERE x.project_id = p.id AND x.parent_project_transaction_id IS NULL AND x.type = 'expense'), 0),
		       COALESCE((SELECT SUM(amount) FROM project_transactions x
		                 WHERE x.project_id = p.id AND x.parent_project_transaction_id IS NULL AND x.type = 'income'), 0),
		       me.id, me.role, me.status,
		       (SELECT COUNT(*) FROM transactions lt WHERE lt.project_id = p.id AND lt.user_id = $2),
		       COALESCE((SELECT SUM(c.amount) FROM project_transactions c
		                 JOIN project_transactions x ON x.id = c.parent_project_transaction_id
		                 WHERE x.project_id = p.id AND x.type IN ('expense', 'payback') AND x.transaction_member_id = me.id
		                   AND c.transaction_member_id <> me.id), 0)
		       - COALESCE((SELECT SUM(c.amount) FROM project_transactions c
		                   JOIN project_transactions x ON x.id = c.parent_project_transaction_id
		                   WHERE x.project_id = p.id AND x.type IN ('expense', 'payback') AND c.transaction_member_id = me.id
		                     AND x.transaction_member_id <> me.id), 0)
		FROM projects p
		LEFT JOIN me ON me.project_id = p.id
		WHERE p.id = ANY($1)`, ids, callerID)
	if err != nil {
		return nil, fmt.Errorf("events: %w", err)
	}
	for rows.Next() {
		var (
			e      Event
			icon   []byte
			mid    *uuid.UUID
			role   *string
			status *string
		)
		if err := rows.Scan(&e.ID, &e.OwnerUserID, &e.Name, &e.Description, &e.Note,
			&e.StartDate, &e.EndDate, &e.Status, &icon, &e.PlannedAmount, &e.CreatedAt, &e.UpdatedAt,
			&e.MembersCount, &e.TotalExpense, &e.TotalIncome, &mid, &role, &status, &e.LinkedTxCount, &e.MyNet); err != nil {
			rows.Close()
			return nil, fmt.Errorf("events scan: %w", err)
		}
		e.IconCode = shared.IconFromJSON(icon)
		e.MyNet = round2(e.MyNet)
		e.Tags = []shared.TagRef{}
		e.IsOwner = e.OwnerUserID == callerID
		e.IsLocked = e.Status != StatusActive
		var me *myMember
		if mid != nil {
			me = &myMember{id: *mid, role: *role, status: *status}
			e.MyMemberID, e.MyRole, e.myStatus = mid, role, *status
		}
		e.CanAddBill = e.Status == StatusActive && me.canWrite()
		e.CanEdit = e.IsOwner
		e.CanManageMembers = e.IsOwner && e.Status != StatusArchived
		out[e.ID] = e
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	evIDs := make([]uuid.UUID, 0, len(out))
	for id := range out {
		evIDs = append(evIDs, id)
	}
	if err := fillMyNumbers(ctx, q, callerID, out, evIDs); err != nil {
		return nil, err
	}
	trows, err := q.Query(ctx, `SELECT mt.project_id, t.id, t.name, t.icon_code
		FROM project_my_tags mt JOIN tags t ON t.id = mt.tag_id
		WHERE mt.project_id = ANY($1) AND mt.user_id = $2
		ORDER BY LOWER(t.name), t.id`, ids, callerID)
	if err != nil {
		return nil, fmt.Errorf("event my tags: %w", err)
	}
	defer trows.Close()
	for trows.Next() {
		var (
			pid  uuid.UUID
			t    shared.TagRef
			icon []byte
		)
		if err := trows.Scan(&pid, &t.ID, &t.Name, &icon); err != nil {
			return nil, err
		}
		t.IconCode = shared.IconFromJSON(icon)
		e := out[pid]
		e.Tags = append(e.Tags, t)
		out[pid] = e
	}
	return out, trows.Err()
}

func loadEvent(ctx context.Context, q shared.Querier, callerID, id uuid.UUID) (Event, error) {
	m, err := loadEvents(ctx, q, callerID, []uuid.UUID{id})
	if err != nil {
		return Event{}, err
	}
	e, ok := m[id]
	if !ok {
		return Event{}, ErrProjectNotFound
	}
	return e, nil
}

func loadMembers(ctx context.Context, q shared.Querier, callerID, projectID uuid.UUID) ([]EventMember, error) {
	rows, err := q.Query(ctx, `SELECT pm.id, pm.user_id, COALESCE(u.display_name, pm.display_name),
			COALESCE(u.icon_code, pm.icon_code), pm.role, pm.status, pm.invited_at, pm.joined_at, pm.left_at,
			mc.id
		FROM project_members pm LEFT JOIN users u ON u.id = pm.user_id
		LEFT JOIN LATERAL (
			SELECT c.id FROM contacts c
			WHERE c.user_id = $2 AND ((pm.user_id IS NOT NULL AND c.linked_user_id = pm.user_id)
			   OR (pm.user_id IS NULL AND LOWER(BTRIM(c.display_name)) = LOWER(BTRIM(pm.display_name))))
			ORDER BY (c.status = 'active') DESC, c.created_at, c.id LIMIT 1) mc ON pm.user_id IS DISTINCT FROM $2
		WHERE pm.project_id = $1
		ORDER BY pm.status = 'left', pm.role <> 'owner', pm.created_at, pm.id`, projectID, callerID)
	if err != nil {
		return nil, fmt.Errorf("members: %w", err)
	}
	defer rows.Close()
	out := make([]EventMember, 0)
	for rows.Next() {
		var (
			m    EventMember
			icon []byte
		)
		if err := rows.Scan(&m.ID, &m.UserID, &m.Name, &icon, &m.Role, &m.Status, &m.InvitedAt, &m.JoinedAt, &m.LeftAt,
			&m.ContactID); err != nil {
			return nil, err
		}
		m.IconCode = shared.IconFromJSON(icon)
		m.Linked = m.UserID != nil
		m.IsMe = m.UserID != nil && *m.UserID == callerID
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	refs := make([]shared.MemberRef, len(out))
	for i, m := range out {
		refs[i] = shared.MemberRef{ID: m.ID, UserID: m.UserID, Name: m.Name, Icon: m.IconCode, IsMe: m.IsMe}
	}
	shared.RememberMemberRefs(ctx, callerID, refs)
	return out, nil
}

func loadEventTags(ctx context.Context, q shared.Querier, callerID, projectID uuid.UUID) ([]EventTag, error) {
	rows, err := q.Query(ctx, `SELECT et.id, et.name, et.icon_code,
			EXISTS (SELECT 1 FROM tags t WHERE t.user_id = $2 AND LOWER(t.name) = LOWER(et.name)),
			(SELECT COUNT(*) FROM project_transaction_tags ptt WHERE ptt.event_tag_id = et.id)
		FROM event_tags et WHERE et.project_id = $1
		ORDER BY 5 DESC, LOWER(et.name), et.id`, projectID, callerID)
	if err != nil {
		return nil, fmt.Errorf("event tags: %w", err)
	}
	defer rows.Close()
	out := make([]EventTag, 0)
	for rows.Next() {
		var (
			t    EventTag
			icon []byte
		)
		if err := rows.Scan(&t.ID, &t.Name, &icon, &t.Mine, &t.Count); err != nil {
			return nil, err
		}
		t.IconCode = shared.IconFromJSON(icon)
		out = append(out, t)
	}
	return out, rows.Err()
}

func loadSummary(ctx context.Context, q shared.Querier, callerID, projectID uuid.UUID, e Event) (EventSummary, error) {
	s := EventSummary{
		TotalExpense: e.TotalExpense, TotalIncome: e.TotalIncome, MemberCount: e.MembersCount,
		PlannedAmount: e.PlannedAmount, Members: []EventPosition{}, ByTag: []EventTagTotal{},
	}
	if e.PlannedAmount != nil {
		spent := round2(e.TotalExpense - e.TotalIncome)
		remaining := round2(*e.PlannedAmount - spent)
		s.SpentNet, s.Remaining = &spent, &remaining
	}
	rows, err := q.Query(ctx, `SELECT COUNT(*) FROM project_transactions
		WHERE project_id = $1 AND parent_project_transaction_id IS NULL AND type <> 'payback'`, projectID)
	if err != nil {
		return s, fmt.Errorf("bill count: %w", err)
	}
	if rows.Next() {
		_ = rows.Scan(&s.TransactionCount)
	}
	rows.Close()

	rows, err = q.Query(ctx, `
		WITH parents AS (
			SELECT id, transaction_member_id AS m, amount FROM project_transactions
			WHERE project_id = $1 AND parent_project_transaction_id IS NULL AND type = 'expense'
		),
		kids AS (
			SELECT c.transaction_member_id AS m, c.amount, c.parent_project_transaction_id AS pid
			FROM project_transactions c JOIN parents p ON p.id = c.parent_project_transaction_id
		),
		paid AS (SELECT m, SUM(amount) AS v FROM parents GROUP BY m),
		share AS (
			SELECT m, SUM(amount) AS v FROM (
				SELECT m, amount FROM kids
				UNION ALL
				SELECT p.m, p.amount - COALESCE((SELECT SUM(k.amount) FROM kids k WHERE k.pid = p.id), 0)
				FROM parents p
			) x GROUP BY m
		),
		backs AS (
			SELECT id, transaction_member_id AS m, amount FROM project_transactions
			WHERE project_id = $1 AND parent_project_transaction_id IS NULL AND type = 'payback'
		),
		sent AS (SELECT m, SUM(amount) AS v FROM backs GROUP BY m),
		received AS (
			SELECT c.transaction_member_id AS m, SUM(c.amount) AS v
			FROM project_transactions c JOIN backs b ON b.id = c.parent_project_transaction_id
			GROUP BY c.transaction_member_id
		)
		SELECT pm.id, COALESCE(paid.v, 0), COALESCE(share.v, 0), COALESCE(sent.v, 0), COALESCE(received.v, 0)
		FROM project_members pm
		LEFT JOIN paid ON paid.m = pm.id
		LEFT JOIN share ON share.m = pm.id
		LEFT JOIN sent ON sent.m = pm.id
		LEFT JOIN received ON received.m = pm.id
		WHERE pm.project_id = $1 AND (pm.status = 'active' OR paid.v IS NOT NULL OR share.v IS NOT NULL
		      OR sent.v IS NOT NULL OR received.v IS NOT NULL)
		ORDER BY COALESCE(paid.v, 0) DESC, pm.created_at`, projectID)
	if err != nil {
		return s, fmt.Errorf("positions: %w", err)
	}
	type pos struct {
		id                          uuid.UUID
		paid, share, sent, received float64
	}
	var ps []pos
	var memberIDs []uuid.UUID
	for rows.Next() {
		var p pos
		if err := rows.Scan(&p.id, &p.paid, &p.share, &p.sent, &p.received); err != nil {
			rows.Close()
			return s, err
		}
		ps = append(ps, p)
		memberIDs = append(memberIDs, p.id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return s, err
	}
	refs, err := LoadMemberRefs(ctx, q, callerID, memberIDs)
	if err != nil {
		return s, err
	}
	for _, p := range ps {
		ep := EventPosition{Member: refs[p.id], Paid: round2(p.paid), Share: round2(p.share),
			Sent: round2(p.sent), Received: round2(p.received), Net: round2(p.paid - p.share + p.sent - p.received)}
		if ep.Net > 0.005 {
			ep.Receivable = ep.Net
		} else if ep.Net < -0.005 {
			ep.Payable = -ep.Net
		}
		s.Members = append(s.Members, ep)
		if ep.Member.IsMe {
			me := ep
			s.MyPosition = &me
		}
	}

	me := uuid.Nil
	if e.MyMemberID != nil {
		me = *e.MyMemberID
	}
	rows, err = q.Query(ctx, `
		WITH parents AS (
			SELECT id, transaction_member_id AS m, amount FROM project_transactions
			WHERE project_id = $1 AND parent_project_transaction_id IS NULL AND type = 'expense'
		),
		mine AS (
			SELECT p.id, CASE WHEN p.m = $2
				THEN p.amount - COALESCE((SELECT SUM(c.amount) FROM project_transactions c WHERE c.parent_project_transaction_id = p.id), 0)
				ELSE COALESCE((SELECT SUM(c.amount) FROM project_transactions c
				               WHERE c.parent_project_transaction_id = p.id AND c.transaction_member_id = $2), 0)
				END AS share
			FROM parents p
		)
		SELECT ptt.event_tag_id, SUM(p.amount), SUM(m.share), COUNT(*)
		FROM parents p JOIN mine m ON m.id = p.id
		LEFT JOIN project_transaction_tags ptt ON ptt.project_transaction_id = p.id
		GROUP BY ptt.event_tag_id
		ORDER BY SUM(p.amount) DESC, ptt.event_tag_id NULLS LAST
		LIMIT 5`, projectID, me)
	if err != nil {
		return s, fmt.Errorf("by tag: %w", err)
	}
	type bt struct {
		tag          *uuid.UUID
		total, share float64
		count        int
	}
	var bts []bt
	var tagIDs []uuid.UUID
	for rows.Next() {
		var b bt
		if err := rows.Scan(&b.tag, &b.total, &b.share, &b.count); err != nil {
			rows.Close()
			return s, err
		}
		bts = append(bts, b)
		if b.tag != nil {
			tagIDs = append(tagIDs, *b.tag)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return s, err
	}
	tagRefs, err := loadTagRefs(ctx, q, callerID, tagIDs)
	if err != nil {
		return s, err
	}
	for _, b := range bts {
		t := EventTagTotal{Total: round2(b.total), Share: round2(b.share), Count: b.count}
		if b.tag != nil {
			if r, ok := tagRefs[*b.tag]; ok {
				t.Tag = &r
			}
		}
		s.ByTag = append(s.ByTag, t)
	}
	return s, nil
}

type tagRefsKey struct{}

// withTagRefs hands loadTagRefs the event's tags a page already read.
func withTagRefs(ctx context.Context, tags []EventTag) context.Context {
	m := make(map[uuid.UUID]EventTagRef, len(tags))
	for _, t := range tags {
		m[t.ID] = EventTagRef{ID: t.ID, Name: t.Name, IconCode: t.IconCode, Mine: t.Mine}
	}
	return context.WithValue(ctx, tagRefsKey{}, m)
}

func loadTagRefs(ctx context.Context, q shared.Querier, callerID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]EventTagRef, error) {
	out := map[uuid.UUID]EventTagRef{}
	if len(ids) == 0 {
		return out, nil
	}
	if known, _ := ctx.Value(tagRefsKey{}).(map[uuid.UUID]EventTagRef); known != nil {
		all := true
		for _, id := range ids {
			r, ok := known[id]
			if !ok {
				all = false
				break
			}
			out[id] = r
		}
		if all {
			return out, nil
		}
		out = map[uuid.UUID]EventTagRef{}
	}
	rows, err := q.Query(ctx, `SELECT et.id, et.name, et.icon_code,
			EXISTS (SELECT 1 FROM tags t WHERE t.user_id = $2 AND LOWER(t.name) = LOWER(et.name))
		FROM event_tags et WHERE et.id = ANY($1)`, uniqueIDs(ids), callerID)
	if err != nil {
		return nil, fmt.Errorf("event tag refs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			r    EventTagRef
			icon []byte
		)
		if err := rows.Scan(&r.ID, &r.Name, &icon, &r.Mine); err != nil {
			return nil, err
		}
		r.IconCode = shared.IconFromJSON(icon)
		out[r.ID] = r
	}
	return out, rows.Err()
}

func loadBoard(ctx context.Context, q shared.Querier, callerID, projectID uuid.UUID, e Event, f BoardFilter, ids []uuid.UUID) ([]BoardRow, int, error) {
	me := uuid.Nil
	if e.MyMemberID != nil {
		me = *e.MyMemberID
	}
	where := []string{"pt.project_id = $1", "pt.parent_project_transaction_id IS NULL"}
	args := []any{projectID}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if ids != nil {
		add("pt.id = ANY($%d)", ids)
	} else {
		if f.Type != "" {
			add("pt.type = $%d", f.Type)
		}
		if f.Mine {
			add(`(pt.transaction_member_id = $%[1]d OR EXISTS (SELECT 1 FROM project_transactions c
				WHERE c.parent_project_transaction_id = pt.id AND c.transaction_member_id = $%[1]d))`, me)
		}
		if qs := strings.TrimSpace(f.Q); qs != "" {
			add("(pt.description ILIKE $%[1]d OR pt.note ILIKE $%[1]d)", "%"+shared.EscapeLike(qs)+"%")
		}
		if f.PayerMemberID != nil {
			add("pt.transaction_member_id = $%d", *f.PayerMemberID)
		}
		if f.TagID != nil {
			add(`EXISTS (SELECT 1 FROM project_transaction_tags ptt
				WHERE ptt.project_transaction_id = pt.id AND ptt.event_tag_id = $%d)`, *f.TagID)
		}
	}
	w := strings.Join(where, " AND ")
	order := "pt.date DESC, pt.created_at DESC"
	switch f.Sort {
	case "date_asc":
		order = "pt.date ASC, pt.created_at ASC"
	case "amount_desc":
		order = "pt.amount DESC, pt.created_at DESC"
	case "amount_asc":
		order = "pt.amount ASC, pt.created_at DESC"
	}
	// ONE statement: the page of bills (its position and how many match without the limit ride
	// along) and the splits of those bills.
	countArgs := args
	page := `SELECT pt.id, ROW_NUMBER() OVER (ORDER BY ` + order + `, pt.id) AS rn, COUNT(*) OVER () AS total
		FROM project_transactions pt WHERE ` + w + ` ORDER BY ` + order + `, pt.id`
	if ids == nil {
		args = append(args, f.PerPage, (f.Page-1)*f.PerPage)
		page += fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(args)-1, len(args))
	}
	args = append(args, callerID)
	caller := fmt.Sprintf("$%d", len(args))
	// The tags of a bill, the transaction it was made from (with its wallet) and what of my books
	// holds it ride along on the bill's own row.
	rows, err := q.Query(ctx, `WITH page AS (`+page+`)
		SELECT page.total, `+strings.ReplaceAll(ptColumnsPrefixed, "@", "x.")+`,
		CASE WHEN x.parent_project_transaction_id IS NULL THEN COALESCE((
			SELECT json_agg(json_build_object('id', et.id, 'name', et.name, 'icon_code', et.icon_code,
				'mine', EXISTS (SELECT 1 FROM tags t WHERE t.user_id = `+caller+` AND LOWER(t.name) = LOWER(et.name)))
				ORDER BY LOWER(et.name), et.id)
			FROM project_transaction_tags ptt JOIN event_tags et ON et.id = ptt.event_tag_id
			WHERE ptt.project_transaction_id = x.id), '[]'::json) END,
		CASE WHEN x.parent_project_transaction_id IS NULL AND x.linked_transaction_id IS NOT NULL THEN (
			SELECT json_build_object('tx_id', lt.id, 'owner', lt.user_id, 'description', lt.description,
				'account', CASE WHEN lt.user_id = `+caller+` AND a.id IS NOT NULL THEN json_build_object(
					'id', a.id, 'name', a.name, 'icon_code', a.icon_code, 'logo_url', a.logo_url,
					'type', a.type, 'currency', a.currency, 'status', a.status) END)
			FROM transactions lt LEFT JOIN accounts a ON a.id = lt.account_id
			WHERE lt.id = x.linked_transaction_id) END,
		CASE WHEN x.parent_project_transaction_id IS NULL THEN (
			SELECT json_build_object(
				'tx', COALESCE(bool_or(t.id IS NOT NULL), false),
				'repay', EXISTS (SELECT 1 FROM personal_debts d
					WHERE d.user_id = `+caller+` AND d.source_project_transaction_id = x.id AND d.kind = 'repayment'),
				'debt', EXISTS (SELECT 1 FROM personal_debts d
					WHERE d.user_id = `+caller+` AND d.source_project_transaction_id = x.id AND d.kind = 'debt'))
			FROM (SELECT 1) one
			LEFT JOIN transactions t ON t.user_id = `+caller+` AND (
				t.id = x.linked_transaction_id OR t.source_project_transaction_id = x.id
				OR t.source_project_transaction_id IN (SELECT c.id FROM project_transactions c
					WHERE c.parent_project_transaction_id = x.id))) END,
		CASE WHEN x.parent_project_transaction_id IS NOT NULL THEN EXISTS (SELECT 1 FROM project_transactions pb
			WHERE pb.paid_back_for_id = x.parent_project_transaction_id AND pb.transaction_member_id = x.transaction_member_id
			  AND pb.parent_project_transaction_id IS NULL) END
		FROM page JOIN project_transactions x ON x.id = page.id OR x.parent_project_transaction_id = page.id
		ORDER BY page.rn, (x.parent_project_transaction_id IS NOT NULL), x.created_at, x.id`, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("board: %w", err)
	}
	var (
		parents  []ProjectTransaction
		children = map[uuid.UUID][]ProjectTransaction{}
		extras   = boardExtras{
			tags: map[uuid.UUID][]EventTagRef{}, sources: map[uuid.UUID]boardSource{}, booked: map[uuid.UUID]MyBooks{},
			paidBack: map[uuid.UUID]map[uuid.UUID]bool{},
		}
		total int
	)
	for rows.Next() {
		var tagsJSON, srcJSON, bookedJSON []byte
		var paidBack *bool
		pt, err := scanPTAround(rows, []any{&total}, []any{&tagsJSON, &srcJSON, &bookedJSON, &paidBack})
		if err != nil {
			rows.Close()
			return nil, 0, err
		}
		if pt.ParentProjectTransactionID != nil {
			if paidBack != nil && *paidBack {
				if extras.paidBack[*pt.ParentProjectTransactionID] == nil {
					extras.paidBack[*pt.ParentProjectTransactionID] = map[uuid.UUID]bool{}
				}
				extras.paidBack[*pt.ParentProjectTransactionID][pt.TransactionMemberID] = true
			}
			children[*pt.ParentProjectTransactionID] = append(children[*pt.ParentProjectTransactionID], *pt)
			continue
		}
		parents = append(parents, *pt)
		if err := extras.add(pt.ID, tagsJSON, srcJSON, bookedJSON); err != nil {
			rows.Close()
			return nil, 0, err
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if ids != nil {
		total = 0
	} else if len(parents) == 0 && f.Page > 1 { // past the end: the window count has no row to ride on
		cr, err := q.Query(ctx, `SELECT COUNT(*) FROM project_transactions pt WHERE `+w, countArgs...)
		if err != nil {
			return nil, 0, fmt.Errorf("board count: %w", err)
		}
		if cr.Next() {
			_ = cr.Scan(&total)
		}
		cr.Close()
	}
	out, err := buildRows(ctx, q, callerID, e, parents, children, extras)
	return out, total, err
}

const ptColumnsPrefixed = `@id, @project_id, @parent_project_transaction_id,
	@transaction_member_id, @record_user_id,
	@type, @amount, @currency,
	to_char(@date, 'YYYY-MM-DD'),
	@note, @description,
	@marks, @created_at, @updated_at`

// boardSource is the transaction a bill was made from; account is set only for the caller's own.
type boardSource struct {
	txID    uuid.UUID
	owner   uuid.UUID
	desc    *string
	account *shared.AccountRef
}

// boardExtras are what loadBoard reads with each bill: its tags, its source transaction and
// what of the caller's books holds it.
type boardExtras struct {
	tags    map[uuid.UUID][]EventTagRef
	sources map[uuid.UUID]boardSource
	booked  map[uuid.UUID]MyBooks
	// paidBack — bill → the members a "คืนแล้ว" payback row was made for (0.4.1-1)
	paidBack map[uuid.UUID]map[uuid.UUID]bool
}

func (x *boardExtras) add(id uuid.UUID, tagsJSON, srcJSON, bookedJSON []byte) error {
	var tags []struct {
		ID       uuid.UUID       `json:"id"`
		Name     string          `json:"name"`
		IconCode json.RawMessage `json:"icon_code"`
		Mine     bool            `json:"mine"`
	}
	if len(tagsJSON) > 0 {
		if err := json.Unmarshal(tagsJSON, &tags); err != nil {
			return fmt.Errorf("board tags: %w", err)
		}
	}
	for _, t := range tags {
		x.tags[id] = append(x.tags[id], EventTagRef{ID: t.ID, Name: t.Name, IconCode: shared.IconFromJSON(t.IconCode), Mine: t.Mine})
	}
	if len(srcJSON) > 0 {
		var s struct {
			TxID        uuid.UUID `json:"tx_id"`
			Owner       uuid.UUID `json:"owner"`
			Description *string   `json:"description"`
			Account     *struct {
				ID       uuid.UUID       `json:"id"`
				Name     string          `json:"name"`
				IconCode json.RawMessage `json:"icon_code"`
				LogoURL  *string         `json:"logo_url"`
				Type     string          `json:"type"`
				Currency string          `json:"currency"`
				Status   string          `json:"status"`
			} `json:"account"`
		}
		if err := json.Unmarshal(srcJSON, &s); err != nil {
			return fmt.Errorf("board source: %w", err)
		}
		bs := boardSource{txID: s.TxID, owner: s.Owner, desc: s.Description}
		if a := s.Account; a != nil {
			bs.account = &shared.AccountRef{ID: a.ID, Name: a.Name, IconCode: shared.IconFromJSON(a.IconCode), LogoURL: a.LogoURL,
				Type: a.Type, Currency: a.Currency, Group: shared.AccountGroup(a.Type), Archived: a.Status != "active"}
		}
		x.sources[id] = bs
	}
	if len(bookedJSON) > 0 {
		var b MyBooks
		if err := json.Unmarshal(bookedJSON, &b); err != nil {
			return fmt.Errorf("board booked: %w", err)
		}
		x.booked[id] = b
	}
	return nil
}

func buildRows(ctx context.Context, q shared.Querier, callerID uuid.UUID, e Event, parents []ProjectTransaction, children map[uuid.UUID][]ProjectTransaction, extras boardExtras) ([]BoardRow, error) {
	out := make([]BoardRow, 0, len(parents))
	if len(parents) == 0 {
		return out, nil
	}
	memberIDs := []uuid.UUID{}
	for _, p := range parents {
		memberIDs = append(memberIDs, p.TransactionMemberID)
	}
	for _, cs := range children {
		for _, c := range cs {
			memberIDs = append(memberIDs, c.TransactionMemberID)
		}
	}
	members, err := LoadMemberRefs(ctx, q, callerID, memberIDs)
	if err != nil {
		return nil, err
	}
	tags, sources, booked := extras.tags, extras.sources, extras.booked
	paidBack := extras.paidBack

	me := uuid.Nil
	if e.MyMemberID != nil {
		me = *e.MyMemberID
	}
	canWrite := e.myStatus == MemberStatusActive && e.MyRole != nil && *e.MyRole != RoleViewer &&
		writable(e.Status, "edit_pt") == nil
	for _, p := range parents {
		r := BoardRow{
			ID: p.ID, ProjectID: p.ProjectID, Type: p.Type, Amount: p.Amount, Date: p.Date,
			Description: p.Description, Note: p.Note, TransactionMember: members[p.TransactionMemberID],
			RecordUserID: p.RecordUserID, Tags: tags[p.ID], Marks: p.Marks,
			MyMark: containsID(p.Marks, me), Locked: !canWrite, MyBooks: booked[p.ID],
			MyBooked: booked[p.ID].Tx || booked[p.ID].Debt || booked[p.ID].Repay,
			Children: []BoardSplit{}, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
		}
		if r.Tags == nil {
			r.Tags = []EventTagRef{}
		}
		if r.Marks == nil {
			r.Marks = []uuid.UUID{}
		}
		var others, mine float64
		for _, c := range children[p.ID] {
			marks := c.Marks
			if marks == nil {
				marks = []uuid.UUID{}
			}
			r.Children = append(r.Children, BoardSplit{
				ID: c.ID, TransactionMember: members[c.TransactionMemberID], Amount: c.Amount,
				Marks: marks, MyMark: containsID(marks, me), PaidBack: paidBack[p.ID][c.TransactionMemberID],
			})
			if c.TransactionMemberID == me {
				mine += c.Amount
			} else {
				others += c.Amount
			}
		}
		switch {
		case p.TransactionMemberID == me:
			r.MyShare = round2(p.Amount - others - mine)
		default:
			r.MyShare = round2(mine)
		}
		if s, ok := sources[p.ID]; ok {
			r.Linked = true
			if s.owner == callerID {
				r.SourceTx = &BoardSourceTx{ID: s.txID, Description: s.desc, Account: s.account}
			}
		}
		out = append(out, r)
	}
	return out, nil
}

func containsID(ids []uuid.UUID, id uuid.UUID) bool {
	if id == uuid.Nil {
		return false
	}
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

func (s *Service) detail(ctx context.Context, callerID, projectID uuid.UUID, page, perPage int) (*EventDetail, error) {
	q := s.store.db
	e, err := loadEvent(ctx, q, callerID, projectID)
	if err != nil {
		return nil, err
	}
	if !e.IsOwner && e.myStatus != MemberStatusActive { // the owner or an active member only
		return nil, ErrProjectNotFound
	}
	out := &EventDetail{Event: e}
	ctx = shared.WithMemberRefCache(ctx) // the members are read first, the summary and the board reuse them
	if out.Members, err = loadMembers(ctx, q, callerID, projectID); err != nil {
		return nil, err
	}
	if out.Tags, err = loadEventTags(ctx, q, callerID, projectID); err != nil {
		return nil, err
	}
	ctx = withTagRefs(ctx, out.Tags) // the summary's by-tag refs are these tags
	if out.Summary, err = loadSummary(ctx, q, callerID, projectID, e); err != nil {
		return nil, err
	}
	page, perPage = clampPage(page, perPage)
	rows, total, err := loadBoard(ctx, q, callerID, projectID, e, BoardFilter{Page: page, PerPage: perPage}, nil)
	if err != nil {
		return nil, err
	}
	out.Board = BoardRowList{Data: rows, Pagination: shared.NewPage(page, perPage, total)}
	return out, nil
}

func (s *Service) patch(ctx context.Context, callerID, projectID uuid.UUID, rowID, removed *uuid.UUID, touched []uuid.UUID) (*EventPatch, error) {
	q := s.store.db
	e, err := loadEvent(ctx, q, callerID, projectID)
	if err != nil {
		return nil, err
	}
	out := &EventPatch{Event: e, RemovedRowID: removed}
	ctx = shared.WithMemberRefCache(ctx)
	if out.Members, err = loadMembers(ctx, q, callerID, projectID); err != nil {
		return nil, err
	}
	if out.Summary, err = loadSummary(ctx, q, callerID, projectID, e); err != nil {
		return nil, err
	}
	if out.Tags, err = loadEventTags(ctx, q, callerID, projectID); err != nil {
		return nil, err
	}
	if rowID != nil {
		rows, _, err := loadBoard(ctx, q, callerID, projectID, e, BoardFilter{}, []uuid.UUID{*rowID})
		if err != nil {
			return nil, err
		}
		if len(rows) == 1 {
			out.Row = &rows[0]
		}
	}
	if out.AccountBalances, err = shared.LoadWalletBalances(ctx, q, touched); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) boardRows(ctx context.Context, callerID, projectID uuid.UUID, ids []uuid.UUID) ([]BoardRow, error) {
	e, err := loadEvent(ctx, s.store.db, callerID, projectID)
	if err != nil {
		return nil, err
	}
	rows, _, err := loadBoard(ctx, s.store.db, callerID, projectID, e, BoardFilter{}, ids)
	return rows, err
}

const unlimitedPerPage = 100000

func clampPage(page, perPage int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if perPage <= 0 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}
	return page, perPage
}
