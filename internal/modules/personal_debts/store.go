package personal_debts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

func (s *Store) Pool() *pgxpool.Pool { return s.db }

var (
	ErrEntryNotFound = errors.New("debt entry not found")
	ErrPersonNotFound = errors.New("no entries with this person")
	ErrContactNotFound = errors.New("contact not found")
	ErrTagsNeedWallet = errors.New("tags go on the wallet transaction: pick a wallet first")
	ErrLedgerOnlyNoWallet = errors.New("a ledger-only entry has no wallet or tags: turn create_transaction on to use them")
	ErrMarkPaidNotFound = errors.New("an entry to mark paid is not one of this person's")
	ErrManagedBySource = errors.New("this entry comes from a split: only its description, note and paid label can change here")
)

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// assertContactOwned returns ErrContactNotFound unless contactID is one of userID's own contacts.
func assertContactOwned(ctx context.Context, q querier, userID uuid.UUID, contactID *uuid.UUID) error {
	if contactID == nil {
		return nil
	}
	var ok bool
	if err := q.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM contacts WHERE id = $1 AND user_id = $2)`,
		*contactID, userID).Scan(&ok); err != nil {
		return fmt.Errorf("check contact owner: %w", err)
	}
	if !ok {
		return ErrContactNotFound
	}
	return nil
}

const entryColumns = `id, user_id, direction,
	counterparty_contact_id, counterparty_person_name,
	source_transaction_id, source_project_transaction_id, project_id,
	amount, currency, to_char(date, 'YYYY-MM-DD'), paid, source_kind, description, note,
	created_at, updated_at, counterpart_debt_id`

func scanEntry(row pgx.Row) (*Entry, error) {
	var e Entry
	err := row.Scan(
		&e.ID, &e.UserID, &e.Direction,
		&e.ContactID, &e.PersonName,
		&e.SourceTransactionID, &e.SourceProjectTransactionID, &e.ProjectID,
		&e.Amount, &e.Currency, &e.Date, &e.Paid, &e.SourceKind, &e.Description, &e.Note,
		&e.CreatedAt, &e.UpdatedAt, &e.CounterpartDebtID,
	)
	return &e, err
}

func getEntry(ctx context.Context, q querier, userID, id uuid.UUID, forUpdate bool) (*Entry, error) {
	sql := `SELECT ` + entryColumns + ` FROM personal_debts WHERE id = $1 AND user_id = $2`
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	e, err := scanEntry(q.QueryRow(ctx, sql, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrEntryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("entry: %w", err)
	}
	return e, nil
}

func getAnyEntryTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*Entry, error) {
	e, err := scanEntry(tx.QueryRow(ctx,
		`SELECT `+entryColumns+` FROM personal_debts WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrEntryNotFound
	}
	return e, err
}

func insertEntryTx(ctx context.Context, tx pgx.Tx, e *Entry) (*Entry, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("uuid: %w", err)
	}
	if e.Currency == "" {
		if err := tx.QueryRow(ctx, `SELECT currency FROM users WHERE id = $1`, e.UserID).Scan(&e.Currency); err != nil {
			return nil, fmt.Errorf("user currency: %w", err)
		}
	}
	row, err := scanEntry(tx.QueryRow(ctx, `INSERT INTO personal_debts
		(id, user_id, direction, counterparty_contact_id, counterparty_person_name,
		 source_transaction_id, source_project_transaction_id, project_id,
		 amount, currency, date, paid, source_kind, description, note,
		 created_by_user_id, updated_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::date, $12, $13, $14, $15, $2, $2)
		RETURNING `+entryColumns,
		id, e.UserID, e.Direction, e.ContactID, strings.TrimSpace(e.PersonName),
		e.SourceTransactionID, e.SourceProjectTransactionID, e.ProjectID,
		e.Amount, strings.ToUpper(e.Currency), e.Date, e.Paid, e.SourceKind,
		shared.CleanText(e.Description), shared.CleanText(e.Note)))
	if err != nil {
		return nil, fmt.Errorf("insert entry: %w", err)
	}
	if e.ContactID != nil {
		_, _ = tx.Exec(ctx, `UPDATE contacts SET last_used_at = NOW() WHERE id = $1 AND user_id = $2`,
			*e.ContactID, e.UserID)
	}
	return row, nil
}

func peopleSQL(keyFilter string) string {
	return `
		WITH e AS (
			SELECT ` + shared.DebtKeySQL("pd") + ` AS key, pd.id,
			       pd.counterparty_contact_id AS cid, pd.counterparty_person_name AS snap,
			       ` + shared.DebtSignedSQL("pd") + ` AS signed, pd.paid, pd.date, pd.created_at
			FROM personal_debts pd
			WHERE pd.user_id = $1 ` + keyFilter + `
		), e2 AS (
			-- the running balance in book order; a cycle ends where it hits 0
			SELECT e.*, ROW_NUMBER() OVER w AS rn, SUM(signed) OVER w AS run
			FROM e WINDOW w AS (PARTITION BY key ORDER BY date, created_at, id)
		), e3 AS (
			SELECT e2.*, MAX(CASE WHEN ABS(run) < 0.005 THEN rn END) OVER (PARTITION BY key) AS cleared_rn
			FROM e2
		), g AS (
			SELECT key, (ARRAY_AGG(cid))[1] AS cid,
			       (ARRAY_AGG(snap ORDER BY created_at DESC))[1] AS snap,
			       SUM(signed) AS net,
			       COUNT(*) FILTER (WHERE NOT paid) AS open_count,
			       COUNT(*) AS entry_count, MAX(date) AS last,
			       MAX(date) FILTER (WHERE rn = cleared_rn) AS cleared,
			       MIN(date) FILTER (WHERE NOT paid AND (cleared_rn IS NULL OR rn > cleared_rn)) AS oldest_open
			FROM e3 GROUP BY key
		)
		SELECT g.key, g.cid, COALESCE(` + shared.PersonNameSQL("c", "lu") + `, g.snap) AS name,
		       CASE WHEN c.id IS NOT NULL THEN ` + shared.PersonIconSQL("c", "lu") + ` END AS icon,
		       lu.id AS linked, g.net, g.open_count, g.entry_count, g.last,
		       to_char(g.cleared, 'YYYY-MM-DD') AS cleared_at,
		       (` + todayInUserTZ + ` - g.oldest_open) AS days_out
		FROM g
		LEFT JOIN contacts c ON c.id = g.cid AND c.user_id = $1
		LEFT JOIN users lu ON lu.id = c.linked_user_id`
}

const todayInUserTZ = `(NOW() AT TIME ZONE COALESCE(
	(SELECT z.name FROM pg_timezone_names z JOIN user_preferences up ON up.preferences->>'timezone' = z.name
	 WHERE up.user_id = $1 LIMIT 1), 'Asia/Bangkok'))::date`

const TodayInUserTZSQL = todayInUserTZ

const openSQL = `(ABS(p.net) > 0.005 OR p.open_count > 0)`

func scanPerson(rows pgx.Rows) (DebtPerson, error) {
	var (
		p      DebtPerson
		cid    *uuid.UUID
		name   string
		icon   []byte
		linked *uuid.UUID
		last   *string
	)
	if err := rows.Scan(&p.Key, &cid, &name, &icon, &linked, &p.Net, &p.OpenCount, &p.EntryCount, &last, &p.ClearedAt, &p.DaysOutstanding); err != nil {
		return p, err
	}
	p.Net = round2(p.Net)
	p.Person = shared.PersonRef{Name: name, ContactID: cid, Linked: linked != nil, LinkedUserID: linked}
	if cid != nil {
		p.Person.IconCode = shared.IconFromJSON(icon)
	}
	p.LastActivityAt = last
	return p, nil
}

func (s *Store) listPeople(ctx context.Context, userID uuid.UUID, f PeopleFilter) ([]DebtPerson, int, error) {
	where := []string{"TRUE"}
	args := []any{userID}
	if !f.All {
		where = append(where, openSQL)
	}
	switch f.Side {
	case "owed_to_me":
		where = append(where, "p.net > 0.005")
	case "i_owe":
		where = append(where, "p.net < -0.005")
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		args = append(args, "%"+shared.EscapeLike(q)+"%")
		where = append(where, fmt.Sprintf("p.name ILIKE $%d", len(args)))
	}
	base := `SELECT key, cid, name, icon, linked, net, open_count, entry_count, to_char(last, 'YYYY-MM-DD'), cleared_at, days_out
		FROM (` + peopleSQL("") + `) p WHERE ` + strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM (`+base+`) x`, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("people count: %w", err)
	}
	args = append(args, f.PerPage, (f.Page-1)*f.PerPage)
	rows, err := s.db.Query(ctx, base+fmt.Sprintf(` ORDER BY `+peopleOrder(f.Sort)+`, p.key
		LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("people: %w", err)
	}
	defer rows.Close()
	out := make([]DebtPerson, 0)
	for rows.Next() {
		p, err := scanPerson(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

func (s *Store) totals(ctx context.Context, q shared.Querier, userID uuid.UUID) (DebtTotals, error) {
	var t DebtTotals
	rows, err := q.Query(ctx, `
		SELECT COALESCE(SUM(p.net) FILTER (WHERE p.net > 0.005), 0),
		       COALESCE(SUM(-p.net) FILTER (WHERE p.net < -0.005), 0),
		       COUNT(*) FILTER (WHERE `+openSQL+`),
		       COUNT(*) FILTER (WHERE p.net > 0.005),
		       COUNT(*) FILTER (WHERE p.net < -0.005),
		       COUNT(*) FILTER (WHERE p.days_out > 30)
		FROM (`+peopleSQL("")+`) p`, userID)
	if err != nil {
		return t, fmt.Errorf("debt totals: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&t.OwedToMe, &t.IOwe, &t.OpenCount, &t.OwedToMePeople, &t.IOwePeople, &t.Over30DaysPeople); err != nil {
			return t, err
		}
	}
	t.OwedToMe, t.IOwe = round2(t.OwedToMe), round2(t.IOwe)
	t.Net = round2(t.OwedToMe - t.IOwe)
	return t, rows.Err()
}

func (s *Store) personBalance(ctx context.Context, q shared.Querier, userID uuid.UUID, k shared.DebtPersonKey) (DebtPerson, bool, error) {
	key := k.Key()
	rows, err := q.Query(ctx, `SELECT key, cid, name, icon, linked, net, open_count, entry_count, to_char(last, 'YYYY-MM-DD'), cleared_at, days_out
		FROM (`+peopleSQL("AND "+shared.DebtKeySQL("pd")+" = $2")+`) p`, userID, key)
	if err != nil {
		return DebtPerson{}, false, fmt.Errorf("person balance: %w", err)
	}
	var (
		p     DebtPerson
		found bool
	)
	if rows.Next() {
		if p, err = scanPerson(rows); err != nil {
			rows.Close()
			return DebtPerson{}, false, err
		}
		found = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return DebtPerson{}, false, err
	}
	if found {
		return p, true, nil
	}
	p = DebtPerson{Key: key, Person: shared.PersonRef{Name: k.Name}}
	if k.ContactID != nil {
		persons, err := shared.LoadPersons(ctx, q, userID, []uuid.UUID{*k.ContactID})
		if err != nil {
			return DebtPerson{}, false, err
		}
		if ref, ok := persons[*k.ContactID]; ok {
			p.Person = ref
		}
	}
	return p, false, nil
}

const entryViewSQL = `SELECT pd.id, ` + `pd.direction, pd.counterparty_contact_id, pd.counterparty_person_name,
		pd.amount, to_char(pd.date, 'YYYY-MM-DD'), pd.description, pd.note, pd.paid, pd.source_kind,
		CASE pd.source_kind WHEN 'transaction' THEN pd.source_transaction_id
		                    WHEN 'event' THEN pd.project_id END,
		CASE pd.source_kind WHEN 'transaction' THEN COALESCE(st.description, sc.name)
		                    WHEN 'event' THEN sp.name
		                    WHEN 'partner' THEN ou.display_name END,
		t.id, t.account_id, pd.created_at, pd.updated_at
	FROM personal_debts pd
	LEFT JOIN transactions t ON t.source_personal_debt_id = pd.id
	LEFT JOIN transactions st ON st.id = pd.source_transaction_id
	LEFT JOIN categories sc ON sc.id = st.category_id
	LEFT JOIN projects sp ON sp.id = pd.project_id
	LEFT JOIN personal_debts op ON op.id = pd.counterpart_debt_id
	LEFT JOIN users ou ON ou.id = op.user_id`

func loadViews(ctx context.Context, q shared.Querier, tail string, args ...any) ([]DebtEntry, error) {
	rows, err := q.Query(ctx, entryViewSQL+" "+tail, args...)
	if err != nil {
		return nil, fmt.Errorf("entries: %w", err)
	}
	type acc struct {
		i  int
		id uuid.UUID
	}
	var (
		out  = make([]DebtEntry, 0)
		accs []acc
		ids  []uuid.UUID
	)
	for rows.Next() {
		var (
			v        DebtEntry
			cid      *uuid.UUID
			name     string
			kind     *string
			sourceID *uuid.UUID
			title    *string
			txID     *uuid.UUID
			accID    *uuid.UUID
		)
		if err := rows.Scan(&v.ID, &v.Direction, &cid, &name, &v.Amount, &v.Date, &v.Description, &v.Note,
			&v.Paid, &kind, &sourceID, &title, &txID, &accID, &v.CreatedAt, &v.UpdatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("entries scan: %w", err)
		}
		v.Key = shared.DebtKey(cid, name)
		if kind != nil {
			v.Source = &DebtSource{Kind: *kind, ID: sourceID, Title: title}
		}
		if txID != nil {
			v.Transaction = &EntryTx{ID: *txID}
			if accID != nil {
				accs = append(accs, acc{len(out), *accID})
				ids = append(ids, *accID)
			}
		}
		out = append(out, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	refs, err := shared.LoadAccountRefs(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	for _, a := range accs {
		if r, ok := refs[a.id]; ok {
			out[a.i].Transaction.Account = &r
		}
	}
	return out, nil
}

func entryView(ctx context.Context, q shared.Querier, userID, id uuid.UUID) (*DebtEntry, error) {
	vs, err := loadViews(ctx, q, `WHERE pd.id = $1 AND pd.user_id = $2`, id, userID)
	if err != nil {
		return nil, err
	}
	if len(vs) == 0 {
		return nil, ErrEntryNotFound
	}
	return &vs[0], nil
}

func (s *Store) listEntries(ctx context.Context, userID uuid.UUID, key string, f EntryFilter) ([]DebtEntry, int, error) {
	where := []string{"pd.user_id = $1", shared.DebtKeySQL("pd") + " = $2"}
	args := []any{userID, key}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		add("(pd.description ILIKE $%[1]d OR pd.note ILIKE $%[1]d)", "%"+shared.EscapeLike(q)+"%")
	}
	if f.Direction != "" {
		add("pd.direction = $%d", f.Direction)
	}
	if f.Paid != nil {
		add("pd.paid = $%d", *f.Paid)
	}
	if f.Money {
		where = append(where, "t.id IS NOT NULL")
	}
	if f.AccountID != nil {
		add("t.account_id = $%d", *f.AccountID)
	}
	if f.From != nil {
		add("pd.date >= $%d::date", *f.From)
	}
	if f.To != nil {
		add("pd.date <= $%d::date", *f.To)
	}
	mark, err := s.cycleBoundary(ctx, s.db, userID, key)
	if err != nil {
		return nil, 0, err
	}
	if mark != nil && (f.Cycle == "current" || f.Cycle == "earlier") {
		op := ">"
		if f.Cycle == "earlier" {
			op = "<="
		}
		args = append(args, mark.Date, mark.Created, mark.ID)
		where = append(where, fmt.Sprintf("(pd.date, pd.created_at, pd.id) %s ($%d::date, $%d::timestamptz, $%d::uuid)",
			op, len(args)-2, len(args)-1, len(args)))
	} else if mark == nil && f.Cycle == "earlier" {
		where = append(where, "FALSE")
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM personal_debts pd
		LEFT JOIN transactions t ON t.source_personal_debt_id = pd.id WHERE `+w, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("entries count: %w", err)
	}
	order := "pd.date DESC, pd.created_at DESC"
	switch f.Sort {
	case "date_asc":
		order = "pd.date ASC, pd.created_at DESC"
	case "amount_desc":
		order = "pd.amount DESC, pd.created_at DESC"
	case "amount_asc":
		order = "pd.amount ASC, pd.created_at DESC"
	}
	args = append(args, f.PerPage, (f.Page-1)*f.PerPage)
	out, err := loadViews(ctx, s.db, fmt.Sprintf(`WHERE %s ORDER BY %s, pd.id LIMIT $%d OFFSET $%d`,
		w, order, len(args)-1, len(args)), args...)
	if err == nil {
		for i := range out {
			cur := mark.after(out[i].Date, out[i].CreatedAt, out[i].ID)
			out[i].InCurrentCycle = &cur
		}
	}
	return out, total, err
}

func (s *Store) entryCounts(ctx context.Context, userID uuid.UUID, key string) (EntryCounts, error) {
	var c EntryCounts
	err := s.db.QueryRow(ctx, `SELECT COUNT(*), COUNT(t.id) FROM personal_debts pd
		LEFT JOIN transactions t ON t.source_personal_debt_id = pd.id
		WHERE pd.user_id = $1 AND `+shared.DebtKeySQL("pd")+` = $2`, userID, key).Scan(&c.Entries, &c.Money)
	if err != nil {
		return c, err
	}
	mark, err := s.cycleBoundary(ctx, s.db, userID, key)
	if err != nil {
		return c, err
	}
	if mark == nil {
		c.Current = c.Entries
		return c, nil
	}
	err = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM personal_debts pd
		WHERE pd.user_id = $1 AND `+shared.DebtKeySQL("pd")+` = $2
		  AND (pd.date, pd.created_at, pd.id) > ($3::date, $4::timestamptz, $5::uuid)`,
		userID, key, mark.Date, mark.Created, mark.ID).Scan(&c.Current)
	c.Earlier = c.Entries - c.Current
	return c, err
}

func entryTxID(ctx context.Context, q querier, entryID uuid.UUID) (*uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM transactions WHERE source_personal_debt_id = $1`, entryID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("entry tx: %w", err)
	}
	return &id, nil
}

func (s *Store) UnlinkedNames(ctx context.Context, userID uuid.UUID) ([]struct {
	Name  string
	Count int
}, error) {
	rows, err := s.db.Query(ctx, `SELECT counterparty_person_name, COUNT(*)
		FROM personal_debts
		WHERE user_id = $1 AND counterparty_contact_id IS NULL AND counterparty_person_name <> ''
		GROUP BY counterparty_person_name
		ORDER BY COUNT(*) DESC, LOWER(counterparty_person_name)`, userID)
	if err != nil {
		return nil, fmt.Errorf("unlinked names: %w", err)
	}
	defer rows.Close()
	out := make([]struct {
		Name  string
		Count int
	}, 0)
	for rows.Next() {
		var name string
		var count int
		if err := rows.Scan(&name, &count); err != nil {
			return nil, err
		}
		out = append(out, struct {
			Name  string
			Count int
		}{name, count})
	}
	return out, rows.Err()
}

func (s *Store) AbsorbForContact(ctx context.Context, userID, contactID uuid.UUID, names []string) (int, error) {
	if len(names) == 0 {
		return 0, nil
	}
	lowered := make([]string, len(names))
	for i, n := range names {
		lowered[i] = strings.ToLower(strings.TrimSpace(n))
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	var displayName string
	if err := tx.QueryRow(ctx,
		`SELECT display_name FROM contacts WHERE id = $1 AND user_id = $2`,
		contactID, userID).Scan(&displayName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("contact not found or not owned: %s", contactID)
		}
		return 0, fmt.Errorf("contact lookup: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE personal_debts
		SET counterparty_contact_id = $1, counterparty_person_name = $2, updated_by_user_id = $3
		WHERE user_id = $3 AND counterparty_contact_id IS NULL
		  AND LOWER(TRIM(counterparty_person_name)) = ANY($4::text[])`,
		contactID, displayName, userID, lowered)
	if err != nil {
		return 0, fmt.Errorf("absorb: %w", err)
	}
	if tag.RowsAffected() > 0 {
		_, _ = tx.Exec(ctx, `UPDATE contacts SET last_used_at = NOW() WHERE id = $1 AND user_id = $2`,
			contactID, userID)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (s *Store) RestoreOnContactDeleteTx(ctx context.Context, tx pgx.Tx, userID, contactID uuid.UUID) (int, error) {
	var displayName string
	if err := tx.QueryRow(ctx,
		`SELECT `+shared.PersonNameSQL("c", "u")+` FROM contacts c
		 LEFT JOIN users u ON u.id = c.linked_user_id
		 WHERE c.id = $1 AND c.user_id = $2`,
		contactID, userID).Scan(&displayName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("contact lookup: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE personal_debts
		SET counterparty_person_name = $1, updated_by_user_id = $2
		WHERE user_id = $2 AND counterparty_contact_id = $3`,
		displayName, userID, contactID)
	if err != nil {
		return 0, fmt.Errorf("restore on delete: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func round2(f float64) float64 {
	if f < 0 {
		return -float64(int64(-f*100+0.5)) / 100
	}
	return float64(int64(f*100+0.5)) / 100
}

func peopleOrder(sort string) string {
	switch sort {
	case "amount":
		return `ABS(p.net) DESC, p.last DESC NULLS LAST`
	case "recent":
		return `p.last DESC NULLS LAST, ABS(p.net) DESC`
	}
	return `p.days_out DESC NULLS LAST, ABS(p.net) DESC, p.last DESC NULLS LAST`
}
