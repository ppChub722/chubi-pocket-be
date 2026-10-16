package projects

// Page-ready event shapes (spec schemas Event*, Board*). Every read and
// every write response is built here, one query per kind (never per row):
// the write handlers return EventPatch so the app patches instead of
// re-fetching.

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// myMember — the caller's member row facts in one event.
type myMember struct {
	id     uuid.UUID
	role   string
	status string
}

func (m *myMember) canWrite() bool {
	return m != nil && m.status == MemberStatusActive && m.role != RoleViewer
}

// loadEvents builds the Event rows of ids for callerID (map by id).
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
		                 WHERE x.project_id = p.id AND x.type = 'expense' AND x.transaction_member_id = me.id
		                   AND c.transaction_member_id <> me.id), 0)
		       - COALESCE((SELECT SUM(c.amount) FROM project_transactions c
		                   JOIN project_transactions x ON x.id = c.parent_project_transaction_id
		                   WHERE x.project_id = p.id AND x.type = 'expense' AND c.transaction_member_id = me.id
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
	// MY personal tags on each event.
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

// loadMembers — the event's members (active / pending first, then left).
func loadMembers(ctx context.Context, q shared.Querier, callerID, projectID uuid.UUID) ([]EventMember, error) {
	rows, err := q.Query(ctx, `SELECT pm.id, pm.user_id, COALESCE(u.display_name, pm.display_name),
			COALESCE(u.icon_code, pm.icon_code), pm.role, pm.status, pm.invited_at, pm.joined_at, pm.left_at
		FROM project_members pm LEFT JOIN users u ON u.id = pm.user_id
		WHERE pm.project_id = $1
		ORDER BY pm.status = 'left', pm.role <> 'owner', pm.created_at, pm.id`, projectID)
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
		if err := rows.Scan(&m.ID, &m.UserID, &m.Name, &icon, &m.Role, &m.Status, &m.InvitedAt, &m.JoinedAt, &m.LeftAt); err != nil {
			return nil, err
		}
		m.IconCode = shared.IconFromJSON(icon)
		m.Linked = m.UserID != nil
		m.IsMe = m.UserID != nil && *m.UserID == callerID
		out = append(out, m)
	}
	return out, rows.Err()
}

// loadEventTags — the event's tags, most used first; mine = I have a
// personal tag with the same name.
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

// loadSummary — totals, plan, positions and the top-5 tags.
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
		WHERE project_id = $1 AND parent_project_transaction_id IS NULL`, projectID)
	if err != nil {
		return s, fmt.Errorf("bill count: %w", err)
	}
	if rows.Next() {
		_ = rows.Scan(&s.TransactionCount)
	}
	rows.Close()

	// Positions (contract §6a): expense bills only; the payer's share of a
	// bill is what its splits leave (all of it without splits), so the nets
	// add up to 0.
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
		)
		SELECT pm.id, COALESCE(paid.v, 0), COALESCE(share.v, 0)
		FROM project_members pm
		LEFT JOIN paid ON paid.m = pm.id
		LEFT JOIN share ON share.m = pm.id
		WHERE pm.project_id = $1 AND (pm.status = 'active' OR paid.v IS NOT NULL OR share.v IS NOT NULL)
		ORDER BY COALESCE(paid.v, 0) DESC, pm.created_at`, projectID)
	if err != nil {
		return s, fmt.Errorf("positions: %w", err)
	}
	type pos struct {
		id          uuid.UUID
		paid, share float64
	}
	var ps []pos
	var memberIDs []uuid.UUID
	for rows.Next() {
		var p pos
		if err := rows.Scan(&p.id, &p.paid, &p.share); err != nil {
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
		ep := EventPosition{Member: refs[p.id], Paid: round2(p.paid), Share: round2(p.share), Net: round2(p.paid - p.share)}
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

	// By tag (top 5): total of the tagged expense bills and my share of them.
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

func loadTagRefs(ctx context.Context, q shared.Querier, callerID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]EventTagRef, error) {
	out := map[uuid.UUID]EventTagRef{}
	if len(ids) == 0 {
		return out, nil
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

// --- board ---

// loadBoard — one page of bills (parents) of projectID for callerID,
// splits nested, everything resolved. ids (when set) replaces the filter:
// exactly those bills, in board order.
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
		if f.TagID != nil {
			add(`EXISTS (SELECT 1 FROM project_transaction_tags ptt
				WHERE ptt.project_transaction_id = pt.id AND ptt.event_tag_id = $%d)`, *f.TagID)
		}
	}
	w := strings.Join(where, " AND ")
	total := 0
	if ids == nil {
		rows, err := q.Query(ctx, `SELECT COUNT(*) FROM project_transactions pt WHERE `+w, args...)
		if err != nil {
			return nil, 0, fmt.Errorf("board count: %w", err)
		}
		if rows.Next() {
			_ = rows.Scan(&total)
		}
		rows.Close()
	}
	order := "pt.date DESC, pt.created_at DESC"
	switch f.Sort {
	case "date_asc":
		order = "pt.date ASC, pt.created_at ASC"
	case "amount_desc":
		order = "pt.amount DESC, pt.created_at DESC"
	case "amount_asc":
		order = "pt.amount ASC, pt.created_at DESC"
	}
	sql := `SELECT ` + strings.ReplaceAll(ptColumnsPrefixed, "@", "pt.") + ` FROM project_transactions pt WHERE ` + w + ` ORDER BY ` + order + `, pt.id`
	if ids == nil {
		args = append(args, f.PerPage, (f.Page-1)*f.PerPage)
		sql += fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(args)-1, len(args))
	}
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("board: %w", err)
	}
	var parents []ProjectTransaction
	for rows.Next() {
		pt, err := scanPT(rows)
		if err != nil {
			rows.Close()
			return nil, 0, err
		}
		parents = append(parents, *pt)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	out, err := buildRows(ctx, q, callerID, e, parents)
	return out, total, err
}

// ptColumnsPrefixed — ptColumns with an @ where the table alias goes.
const ptColumnsPrefixed = `@id, @project_id, @parent_project_transaction_id,
	@transaction_member_id, @record_user_id,
	@type, @amount, @currency,
	to_char(@date, 'YYYY-MM-DD'),
	@note, @description,
	@marks, @created_at, @updated_at`

// buildRows resolves bills (parents) into BoardRows: children, members,
// event tags, the linked source transaction.
func buildRows(ctx context.Context, q shared.Querier, callerID uuid.UUID, e Event, parents []ProjectTransaction) ([]BoardRow, error) {
	out := make([]BoardRow, 0, len(parents))
	if len(parents) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(parents))
	memberIDs := []uuid.UUID{}
	for i, p := range parents {
		ids[i] = p.ID
		memberIDs = append(memberIDs, p.TransactionMemberID)
	}
	// Children.
	rows, err := q.Query(ctx, `SELECT `+ptColumns+` FROM project_transactions
		WHERE parent_project_transaction_id = ANY($1) ORDER BY created_at, id`, ids)
	if err != nil {
		return nil, fmt.Errorf("board children: %w", err)
	}
	children := map[uuid.UUID][]ProjectTransaction{}
	for rows.Next() {
		c, err := scanPT(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		children[*c.ParentProjectTransactionID] = append(children[*c.ParentProjectTransactionID], *c)
		memberIDs = append(memberIDs, c.TransactionMemberID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	members, err := LoadMemberRefs(ctx, q, callerID, memberIDs)
	if err != nil {
		return nil, err
	}
	// Event tags per bill.
	rows, err = q.Query(ctx, `SELECT ptt.project_transaction_id, et.id, et.name, et.icon_code,
			EXISTS (SELECT 1 FROM tags t WHERE t.user_id = $2 AND LOWER(t.name) = LOWER(et.name))
		FROM project_transaction_tags ptt JOIN event_tags et ON et.id = ptt.event_tag_id
		WHERE ptt.project_transaction_id = ANY($1)
		ORDER BY LOWER(et.name), et.id`, ids, callerID)
	if err != nil {
		return nil, fmt.Errorf("board tags: %w", err)
	}
	tags := map[uuid.UUID][]EventTagRef{}
	for rows.Next() {
		var (
			pid  uuid.UUID
			r    EventTagRef
			icon []byte
		)
		if err := rows.Scan(&pid, &r.ID, &r.Name, &icon, &r.Mine); err != nil {
			rows.Close()
			return nil, err
		}
		r.IconCode = shared.IconFromJSON(icon)
		tags[pid] = append(tags[pid], r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The linked personal transaction (the bill was made from it).
	rows, err = q.Query(ctx, `SELECT pt.id, t.id, t.user_id, t.description, t.account_id
		FROM project_transactions pt JOIN transactions t ON t.id = pt.linked_transaction_id
		WHERE pt.id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("board sources: %w", err)
	}
	type src struct {
		txID    uuid.UUID
		owner   uuid.UUID
		desc    *string
		account *uuid.UUID
	}
	sources := map[uuid.UUID]src{}
	var accIDs []uuid.UUID
	for rows.Next() {
		var (
			pid uuid.UUID
			s   src
		)
		if err := rows.Scan(&pid, &s.txID, &s.owner, &s.desc, &s.account); err != nil {
			rows.Close()
			return nil, err
		}
		sources[pid] = s
		if s.account != nil && s.owner == callerID {
			accIDs = append(accIDs, *s.account)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	accounts, err := shared.LoadAccountRefs(ctx, q, accIDs)
	if err != nil {
		return nil, err
	}

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
			MyMark: containsID(p.Marks, me), Locked: !canWrite,
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
				Marks: marks, MyMark: containsID(marks, me),
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
				st := &BoardSourceTx{ID: s.txID, Description: s.desc}
				if s.account != nil {
					if a, ok := accounts[*s.account]; ok {
						st.Account = &a
					}
				}
				r.SourceTx = st
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

// --- composed responses ---

// detail — GET /projects/:id/detail (and create / quick / accept).
func (s *Service) detail(ctx context.Context, callerID, projectID uuid.UUID, page, perPage int) (*EventDetail, error) {
	if _, err := s.store.GetByIDForCaller(ctx, callerID, projectID); err != nil {
		return nil, err
	}
	q := s.store.db
	e, err := loadEvent(ctx, q, callerID, projectID)
	if err != nil {
		return nil, err
	}
	out := &EventDetail{Event: e}
	if out.Summary, err = loadSummary(ctx, q, callerID, projectID, e); err != nil {
		return nil, err
	}
	if out.Members, err = loadMembers(ctx, q, callerID, projectID); err != nil {
		return nil, err
	}
	if out.Tags, err = loadEventTags(ctx, q, callerID, projectID); err != nil {
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

// patch — what every event write returns. rowID = the bill the write
// touched (a split / tick → its parent); removed = a bill that left.
func (s *Service) patch(ctx context.Context, callerID, projectID uuid.UUID, rowID, removed *uuid.UUID, touched []uuid.UUID) (*EventPatch, error) {
	q := s.store.db
	e, err := loadEvent(ctx, q, callerID, projectID)
	if err != nil {
		return nil, err
	}
	out := &EventPatch{Event: e, RemovedRowID: removed}
	if out.Summary, err = loadSummary(ctx, q, callerID, projectID, e); err != nil {
		return nil, err
	}
	if out.Members, err = loadMembers(ctx, q, callerID, projectID); err != nil {
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

// boardRows — bills ids as BoardRows (board order).
func (s *Service) boardRows(ctx context.Context, callerID, projectID uuid.UUID, ids []uuid.UUID) ([]BoardRow, error) {
	e, err := loadEvent(ctx, s.store.db, callerID, projectID)
	if err != nil {
		return nil, err
	}
	rows, _, err := loadBoard(ctx, s.store.db, callerID, projectID, e, BoardFilter{}, ids)
	return rows, err
}

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
