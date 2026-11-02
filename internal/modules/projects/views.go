package projects

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
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

// EventListSummary — MyShareTotal / PaidTotal: active events. ReceivableTotal / PayableTotal and
// every count: all events but archived.
type EventListSummary struct {
	MyShareTotal    float64     `json:"my_share_total"`
	PaidTotal       float64     `json:"paid_total"`
	ReceivableTotal float64     `json:"receivable_total"`
	PayableTotal    float64     `json:"payable_total"`
	Counts          EventCounts `json:"counts"`
}

type EventCounts struct {
	All        int `json:"all"`
	Active     int `json:"active"`
	Unsettled  int `json:"unsettled"`
	Receivable int `json:"receivable"`
	Payable    int `json:"payable"`
	Ended      int `json:"ended"`
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
		       p.status, p.icon_code, p.planned_amount, p.created_at, p.updated_at, p.completed_at,
		       (SELECT COUNT(*) FROM project_members m WHERE m.project_id = p.id AND m.status = 'active'),
		       COALESCE((SELECT SUM(amount) FROM project_transactions x WHERE x.project_id = p.id AND x.type = 'expense'), 0)::float8,
		       COALESCE((SELECT SUM(amount) FROM project_transactions x WHERE x.project_id = p.id AND x.type = 'income'), 0)::float8,
		       me.id, me.role, me.status,
		       COALESCE((SELECT SUM(CASE WHEN v.creditor = me.id THEN v.amount ELSE -v.amount END)
		                 FROM bill_pairs v WHERE v.project_id = p.id AND me.id IN (v.debtor, v.creditor)), 0)::float8,
		       COALESCE((SELECT SUM(sp.paid) FROM project_transactions x JOIN split_transaction_people sp
		                 ON sp.split_transaction_id = x.id AND sp.project_member_id = me.id
		                 WHERE x.project_id = p.id AND x.type = 'expense'), 0)::float8,
		       COALESCE((SELECT SUM(sp.share) FROM project_transactions x JOIN split_transaction_people sp
		                 ON sp.split_transaction_id = x.id AND sp.project_member_id = me.id
		                 WHERE x.project_id = p.id AND x.type = 'expense'), 0)::float8,
		       (SELECT COUNT(DISTINCT sp.split_transaction_id) FROM project_transactions x
		         JOIN split_transaction_people sp ON sp.split_transaction_id = x.id
		         JOIN project_members rm ON rm.id = sp.project_member_id
		         WHERE x.project_id = p.id AND rm.removed_at IS NOT NULL),
		       (SELECT COUNT(*) FROM project_transactions x JOIN split_transaction_people sp
		         ON sp.split_transaction_id = x.id AND sp.project_member_id = me.id
		         WHERE x.project_id = p.id AND (sp.share > 0 OR sp.paid > 0)
		           AND NOT EXISTS (SELECT 1 FROM transactions t WHERE t.split_transaction_id = x.id AND t.user_id = $2)
		           AND NOT EXISTS (SELECT 1 FROM personal_debt_bills b WHERE b.split_transaction_id = x.id AND b.user_id = $2)),
		       d.category_id, d.account_id
		FROM projects p
		LEFT JOIN me ON me.project_id = p.id
		LEFT JOIN project_my_defaults d ON d.project_id = p.id AND d.user_id = $2
		WHERE p.id = ANY($1)`, ids, callerID)
	if err != nil {
		return nil, fmt.Errorf("events: %w", err)
	}
	for rows.Next() {
		var (
			e         Event
			icon      []byte
			mid       *uuid.UUID
			role      *string
			status    *string
			defCat    *uuid.UUID
			defWallet *uuid.UUID
		)
		if err := rows.Scan(&e.ID, &e.OwnerUserID, &e.Name, &e.Description, &e.Note,
			&e.StartDate, &e.EndDate, &e.Status, &icon, &e.PlannedAmount, &e.CreatedAt, &e.UpdatedAt, &e.CompletedAt,
			&e.MembersCount, &e.TotalExpense, &e.TotalIncome, &mid, &role, &status, &e.MyNet, &e.MyPaid, &e.MyShare,
			&e.EmptyPersonBillCount, &e.UnclaimedCount, &defCat, &defWallet); err != nil {
			rows.Close()
			return nil, fmt.Errorf("events scan: %w", err)
		}
		e.IconCode = shared.IconFromJSON(icon)
		e.MyNet, e.MyPaid, e.MyShare = round2(e.MyNet), round2(e.MyPaid), round2(e.MyShare)
		switch {
		case e.MyNet > 0.005:
			e.MyReceivable = e.MyNet
		case e.MyNet < -0.005:
			e.MyPayable = -e.MyNet
		}
		if e.PlannedAmount != nil {
			r := round2(*e.PlannedAmount - (e.TotalExpense - e.TotalIncome))
			e.Remaining = &r
		}
		e.Tags = []shared.TagRef{}
		e.IsOwner = e.OwnerUserID == callerID
		e.IsLocked = e.Status != StatusActive
		var me *myMember
		if mid != nil {
			me = &myMember{id: *mid, role: *role, status: *status}
			e.MyMemberID, e.MyRole, e.myStatus = mid, role, *status
			e.MyDefaults = &MyDefaults{CategoryID: defCat, AccountID: defWallet}
		}
		e.CanAddBill = e.Status == StatusActive && me.canWrite()
		e.CanEdit = e.IsOwner && e.CompletedAt == nil
		e.CanManageMembers = e.IsOwner && e.Status != StatusArchived && e.CompletedAt == nil
		e.CanComplete = e.IsOwner && e.Status == StatusActive
		out[e.ID] = e
	}
	rows.Close()
	if err := rows.Err(); err != nil {
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
			`+shared.AvatarSQL("u.id", "pm.icon_code")+`, pm.role, pm.status, pm.invited_at, pm.joined_at, pm.left_at,
			mc.id, pm.removed_at
		FROM project_members pm LEFT JOIN users u ON u.id = pm.user_id
		`+myContactJoin("$2")+` ON pm.user_id IS DISTINCT FROM $2
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
			&m.ContactID, &m.RemovedAt); err != nil {
			return nil, err
		}
		m.Avatar = shared.AvatarFromSrc(m.Name, icon)
		m.Linked = m.UserID != nil
		m.IsMe = m.UserID != nil && *m.UserID == callerID
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	refs := make([]shared.MemberRef, len(out))
	for i, m := range out {
		refs[i] = shared.MemberRef{ID: m.ID, UserID: m.UserID, Name: m.Name, Avatar: m.Avatar, IsMe: m.IsMe,
			Removed: m.RemovedAt != nil}
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

// loadSummary — each member's position over every paper of the event (bill_pairs: income flipped,
// paybacks netted), plus the totals by event tag.
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
	rows, err := q.Query(ctx, `
		WITH pp AS (
			SELECT sp.project_member_id AS m, b.type, sp.paid, sp.share
			FROM project_transactions b JOIN split_transaction_people sp ON sp.split_transaction_id = b.id
			WHERE b.project_id = $1 AND sp.project_member_id IS NOT NULL
		), net AS (
			SELECT m, SUM(v) AS v FROM (
				SELECT creditor AS m, amount AS v FROM bill_pairs WHERE project_id = $1
				UNION ALL SELECT debtor, -amount FROM bill_pairs WHERE project_id = $1) x GROUP BY m
		)
		SELECT pm.id,
		       COALESCE((SELECT SUM(paid) FROM pp WHERE pp.m = pm.id AND type = 'expense'), 0)::float8,
		       COALESCE((SELECT SUM(share) FROM pp WHERE pp.m = pm.id AND type = 'expense'), 0)::float8,
		       COALESCE((SELECT SUM(paid) FROM pp WHERE pp.m = pm.id AND type = 'payback'), 0)::float8,
		       COALESCE((SELECT SUM(share) FROM pp WHERE pp.m = pm.id AND type = 'payback'), 0)::float8,
		       COALESCE(net.v, 0)::float8
		FROM project_members pm LEFT JOIN net ON net.m = pm.id
		WHERE pm.project_id = $1 AND (pm.status = 'active' OR EXISTS (SELECT 1 FROM pp WHERE pp.m = pm.id))
		ORDER BY 2 DESC, pm.created_at`, projectID)
	if err != nil {
		return s, fmt.Errorf("positions: %w", err)
	}
	type pos struct {
		id                               uuid.UUID
		paid, share, sent, received, net float64
	}
	var (
		ps        []pos
		memberIDs []uuid.UUID
	)
	for rows.Next() {
		var p pos
		if err := rows.Scan(&p.id, &p.paid, &p.share, &p.sent, &p.received, &p.net); err != nil {
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
	billIDs, err := collectIDs(ctx, q, `SELECT id FROM project_transactions WHERE project_id = $1 AND type <> 'payback'`, projectID)
	if err != nil {
		return s, fmt.Errorf("bill count: %w", err)
	}
	s.TransactionCount = len(billIDs)
	refs, err := LoadMemberRefs(ctx, q, callerID, memberIDs)
	if err != nil {
		return s, err
	}
	for _, p := range ps {
		ep := EventPosition{Member: refs[p.id], Paid: round2(p.paid), Share: round2(p.share),
			Sent: round2(p.sent), Received: round2(p.received), Net: round2(p.net)}
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
		SELECT ptt.event_tag_id, SUM(b.amount)::float8,
		       SUM(COALESCE((SELECT x.share FROM split_transaction_people x WHERE x.split_transaction_id = b.id
		                     AND x.project_member_id = $2), 0))::float8, COUNT(*)
		FROM project_transactions b
		LEFT JOIN project_transaction_tags ptt ON ptt.project_transaction_id = b.id
		WHERE b.project_id = $1 AND b.type = 'expense'
		GROUP BY ptt.event_tag_id
		ORDER BY SUM(b.amount) DESC, ptt.event_tag_id NULLS LAST
		LIMIT 5`, projectID, me)
	if err != nil {
		return s, fmt.Errorf("by tag: %w", err)
	}
	type bt struct {
		tag          *uuid.UUID
		total, share float64
		count        int
	}
	var (
		bts    []bt
		tagIDs []uuid.UUID
	)
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

// paperViews — papers as userID sees them, in ids' order (unknown ids dropped).
func (s *Service) paperViews(ctx context.Context, q shared.Querier, userID uuid.UUID, ids []uuid.UUID) ([]PaperView, error) {
	papers, err := shared.LoadPapers(ctx, q, userID, ids)
	if err != nil {
		return nil, err
	}
	var eventIDs []uuid.UUID
	for _, p := range papers {
		if p.ProjectID != nil {
			eventIDs = append(eventIDs, *p.ProjectID)
		}
	}
	events, err := shared.LoadEventRefs(ctx, q, eventIDs)
	if err != nil {
		return nil, err
	}
	roles := map[uuid.UUID]string{}
	if len(eventIDs) > 0 {
		rows, err := q.Query(ctx, `SELECT project_id, role FROM project_members
			WHERE project_id = ANY($1) AND user_id = $2 AND status = 'active'`, uniqueIDs(eventIDs), userID)
		if err != nil {
			return nil, fmt.Errorf("my roles: %w", err)
		}
		for rows.Next() {
			var (
				pid  uuid.UUID
				role string
			)
			if err := rows.Scan(&pid, &role); err != nil {
				rows.Close()
				return nil, err
			}
			roles[pid] = role
		}
		rows.Close()
	}
	tags := map[uuid.UUID][]EventTagRef{}
	trows, err := q.Query(ctx, `SELECT ptt.project_transaction_id, et.id, et.name, et.icon_code,
			EXISTS (SELECT 1 FROM tags t WHERE t.user_id = $2 AND LOWER(t.name) = LOWER(et.name))
		FROM project_transaction_tags ptt JOIN event_tags et ON et.id = ptt.event_tag_id
		WHERE ptt.project_transaction_id = ANY($1) ORDER BY LOWER(et.name)`, ids, userID)
	if err != nil {
		return nil, fmt.Errorf("bill tags: %w", err)
	}
	for trows.Next() {
		var (
			pid  uuid.UUID
			r    EventTagRef
			icon []byte
		)
		if err := trows.Scan(&pid, &r.ID, &r.Name, &icon, &r.Mine); err != nil {
			trows.Close()
			return nil, err
		}
		r.IconCode = shared.IconFromJSON(icon)
		tags[pid] = append(tags[pid], r)
	}
	trows.Close()
	myTx := map[uuid.UUID]uuid.UUID{}
	xrows, err := q.Query(ctx, `SELECT split_transaction_id, id FROM transactions
		WHERE split_transaction_id = ANY($1) AND user_id = $2`, ids, userID)
	if err != nil {
		return nil, fmt.Errorf("my paper books: %w", err)
	}
	for xrows.Next() {
		var pid, tid uuid.UUID
		if err := xrows.Scan(&pid, &tid); err != nil {
			xrows.Close()
			return nil, err
		}
		myTx[pid] = tid
	}
	xrows.Close()
	parts, err := personal_debts.MyPaperPartsTx(ctx, q, userID, ids)
	if err != nil {
		return nil, err
	}
	partsOf := map[uuid.UUID][]personal_debts.PaperPart{}
	for _, p := range parts {
		partsOf[p.PaperID] = append(partsOf[p.PaperID], p)
	}
	out := make([]PaperView, 0, len(ids))
	for _, id := range ids {
		p, ok := papers[id]
		if !ok {
			continue
		}
		v := PaperView{Paper: p, Tags: tags[id]}
		if v.Tags == nil {
			v.Tags = []EventTagRef{}
		}
		completed := false
		if p.ProjectID != nil {
			if e, ok := events[*p.ProjectID]; ok {
				v.Event = &e
				completed = e.Status == StatusCompleted
			}
		}
		v.Me = p.PersonOfUser(userID)
		v.MyBooks = MyPaperBooks{DebtIDs: []uuid.UUID{}, Missing: []string{}, InSync: true}
		if tid, ok := myTx[id]; ok && s.txs != nil {
			if d, err := s.txs.Get(ctx, userID, tid); err == nil {
				v.MyBooks.Transaction = d
				v.MyBooks.InSync = p.TxInSync(userID, d.Amount, d.MyShare, d.Date, d.Description)
			}
		}
		for _, part := range partsOf[id] {
			v.MyBooks.DebtIDs = append(v.MyBooks.DebtIDs, part.DebtID)
		}
		v.MyBooks.DebtIDs = uniqueIDs(v.MyBooks.DebtIDs)
		if v.Me != nil {
			v.MyBooks.Missing = missingBooks(p, v.Me.ID, myTx[id] != uuid.Nil, partsOf[id])
			if v.MyBooks.InSync {
				net := p.NetOf(v.Me.ID)
				for _, part := range partsOf[id] {
					if math.Abs(math.Abs(net[part.PersonID])-part.Amount) >= 0.005 {
						v.MyBooks.InSync = false
					}
				}
			}
		}
		if completed {
			v.MyBooks.InSync = true
		}
		v.MyBooks.Claimed = v.Me != nil && len(v.MyBooks.Missing) == 0
		role, member := roles[valueOr(p.ProjectID)]
		joined := v.Me != nil && v.Me.Status == shared.PersonJoined
		writer := (p.ProjectID == nil && joined) || (member && role != RoleViewer)
		open := !completed
		v.Rights = PaperRights{
			Edit:         writer && open,
			RemovePeople: p.IsOwner && open,
			Move:         p.IsOwner && open,
			Claim:        joined && len(v.MyBooks.Missing) > 0,
			Leave:        v.Me != nil && v.Me.Status != shared.PersonLeft,
			Join:         v.Me != nil && v.Me.Status == shared.PersonInvited,
		}
		out = append(out, v)
	}
	return out, nil
}

func valueOr(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}

// missingBooks — what a claim would add now: a transaction I don't have, and (split bills) a pair
// with nothing booked (a pair I owe counts only when my books took the debt branch or hold nothing).
func missingBooks(p shared.Paper, me uuid.UUID, hasTx bool, parts []personal_debts.PaperPart) []string {
	out := []string{}
	planTx, ok := p.PlanFor(me, true)
	if !ok {
		return out
	}
	if planTx.Tx && !hasTx {
		out = append(out, "transaction")
	}
	booked := map[uuid.UUID]bool{}
	for _, part := range parts {
		booked[part.PersonID] = true
	}
	for _, d := range append(planTx.Debts, planTx.Repayments...) {
		if !booked[d.PersonID] && (!hasTx || !d.IOwe || len(parts) > 0) {
			out = append(out, "debt")
			break
		}
	}
	return out
}

// listPaperIDs — papers matching f: an event's board (projectID), or my split bills.
func (s *Service) listPaperIDs(ctx context.Context, userID uuid.UUID, projectID *uuid.UUID, f PaperFilter) ([]uuid.UUID, int, error) {
	where := []string{}
	args := []any{userID}
	if projectID != nil {
		args = append(args, *projectID)
		where = append(where, fmt.Sprintf("b.project_id = $%d", len(args)))
		if f.Mine {
			where = append(where, `EXISTS (SELECT 1 FROM split_transaction_people mp WHERE mp.split_transaction_id = b.id
				AND mp.user_id = $1 AND (mp.share > 0 OR mp.paid > 0))`)
		}
	} else {
		st := []string{"'invited'", "'joined'"}
		switch f.Status {
		case "invited":
			st = []string{"'invited'"}
		case "joined":
			st = []string{"'joined'"}
		}
		where = append(where, `b.project_id IS NULL`, `EXISTS (SELECT 1 FROM split_transaction_people mp
			WHERE mp.split_transaction_id = b.id AND mp.user_id = $1 AND mp.status IN (`+strings.Join(st, ", ")+`))`)
	}
	if f.Type != "" {
		args = append(args, f.Type)
		where = append(where, fmt.Sprintf("b.type = $%d", len(args)))
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		args = append(args, "%"+shared.EscapeLike(q)+"%")
		where = append(where, fmt.Sprintf(`(b.description ILIKE $%[1]d OR b.note ILIKE $%[1]d OR EXISTS (
			SELECT 1 FROM split_transaction_people qp WHERE qp.split_transaction_id = b.id AND qp.name ILIKE $%[1]d))`, len(args)))
	}
	if f.TagID != nil {
		args = append(args, *f.TagID)
		where = append(where, fmt.Sprintf(`EXISTS (SELECT 1 FROM project_transaction_tags t
			WHERE t.project_transaction_id = b.id AND t.event_tag_id = $%d)`, len(args)))
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := s.store.db.QueryRow(ctx, `SELECT COUNT(*) FROM project_transactions b WHERE `+w, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("papers count: %w", err)
	}
	order := "b.date DESC, b.created_at DESC"
	switch f.Sort {
	case "date_asc":
		order = "b.date, b.created_at"
	case "amount_desc":
		order = "b.amount DESC, b.date DESC"
	case "amount_asc":
		order = "b.amount, b.date DESC"
	}
	args = append(args, f.PerPage, (f.Page-1)*f.PerPage)
	ids, err := collectIDs(ctx, s.store.db, fmt.Sprintf(`SELECT b.id FROM project_transactions b WHERE %s
		ORDER BY %s, b.id LIMIT $%d OFFSET $%d`, w, order, len(args)-1, len(args)), args...)
	return ids, total, err
}

func (s *Service) paperList(ctx context.Context, userID uuid.UUID, projectID *uuid.UUID, f PaperFilter) (*PaperList, error) {
	f.Page, f.PerPage = clampPage(f.Page, f.PerPage)
	ids, total, err := s.listPaperIDs(ctx, userID, projectID, f)
	if err != nil {
		return nil, err
	}
	views, err := s.paperViews(ctx, s.store.db, userID, ids)
	if err != nil {
		return nil, err
	}
	return &PaperList{Data: views, Pagination: shared.NewPage(f.Page, f.PerPage, total)}, nil
}

func (s *Service) detail(ctx context.Context, callerID, projectID uuid.UUID, page, perPage int) (*EventDetail, error) {
	if _, err := s.store.GetByIDForCaller(ctx, callerID, projectID); err != nil {
		return nil, err
	}
	patch, err := s.patch(ctx, callerID, projectID)
	if err != nil {
		return nil, err
	}
	board, err := s.paperList(ctx, callerID, &projectID, PaperFilter{Page: page, PerPage: perPage})
	if err != nil {
		return nil, err
	}
	d := &EventDetail{Event: patch.Event, Summary: patch.Summary, Members: patch.Members, Tags: patch.Tags, Board: *board}
	if s.notifs != nil {
		if st, err := s.notifs.GetSettings(ctx, callerID); err == nil {
			d.AutoSyncEvents, d.AutoAcceptEvents = st.AutoSyncEvents, st.AutoAcceptEvents
		}
	}
	return d, nil
}

// patch — the event's header parts (event, summary, members, tags) after a write.
func (s *Service) patch(ctx context.Context, callerID, projectID uuid.UUID) (*EventPatch, error) {
	e, err := loadEvent(ctx, s.store.db, callerID, projectID)
	if err != nil {
		return nil, err
	}
	members, err := loadMembers(ctx, s.store.db, callerID, projectID)
	if err != nil {
		return nil, err
	}
	tags, err := loadEventTags(ctx, s.store.db, callerID, projectID)
	if err != nil {
		return nil, err
	}
	sum, err := loadSummary(ctx, s.store.db, callerID, projectID, e)
	if err != nil {
		return nil, err
	}
	return &EventPatch{Event: e, Summary: sum, Members: members, Tags: tags}, nil
}

func (s *Service) listSummary(ctx context.Context, userID uuid.UUID) (EventListSummary, error) {
	var out EventListSummary
	ids, err := collectIDs(ctx, s.store.db, `SELECT p.id FROM projects p
		JOIN project_members m ON m.project_id = p.id AND m.user_id = $1 AND m.status = 'active'
		WHERE p.status <> 'archived'`, userID)
	if err != nil {
		return out, fmt.Errorf("event summary ids: %w", err)
	}
	events, err := loadEvents(ctx, s.store.db, userID, ids)
	if err != nil {
		return out, err
	}
	for _, e := range events {
		out.Counts.All++
		switch e.Status {
		case StatusActive:
			out.Counts.Active++
			out.MyShareTotal += e.MyShare
			out.PaidTotal += e.MyPaid
		case StatusCompleted, StatusCancelled:
			out.Counts.Ended++
		}
		if e.MyReceivable > 0 {
			out.Counts.Receivable++
			out.ReceivableTotal += e.MyReceivable
		}
		if e.MyPayable > 0 {
			out.Counts.Payable++
			out.PayableTotal += e.MyPayable
		}
		if e.MyReceivable > 0 || e.MyPayable > 0 {
			out.Counts.Unsettled++
		}
	}
	out.MyShareTotal, out.PaidTotal = round2(out.MyShareTotal), round2(out.PaidTotal)
	out.ReceivableTotal, out.PayableTotal = round2(out.ReceivableTotal), round2(out.PayableTotal)
	return out, nil
}

const (
	maxPerPage       = 100
	unlimitedPerPage = 100000 // the sync dataset reads every event in one page
)

func clampPage(page, perPage int) (int, int) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}
	if perPage > maxPerPage {
		perPage = maxPerPage
	}
	return page, perPage
}
