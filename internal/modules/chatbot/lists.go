package chatbot

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/recurring"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// RuleSource is the recurring service as the chatbot needs it: the rules a payment can be tied to, and the slot
// one would take now.
type RuleSource interface {
	ChatRules(ctx context.Context, userID uuid.UUID) ([]recurring.ChatRule, error)
	PickSlot(ctx context.Context, userID, ruleID uuid.UUID, today time.Time) (time.Time, bool, error)
}

// DBLists reads the lists straight from the database (read only).
type DBLists struct {
	db    *pgxpool.Pool
	rules RuleSource
}

func NewDBLists(db *pgxpool.Pool) *DBLists { return &DBLists{db: db} }

// WithRules adds the recurring rules to the lists.
func (d *DBLists) WithRules(r RuleSource) *DBLists { d.rules = r; return d }

// Lang is the user's app language setting ("" when unset or unreadable).
func (d *DBLists) Lang(ctx context.Context, userID uuid.UUID) string {
	var lang string
	_ = d.db.QueryRow(ctx, `SELECT COALESCE(preferences->>'language', '') FROM user_preferences WHERE user_id = $1`,
		userID).Scan(&lang)
	return lang
}

// ChatLang is the app language setting of the user a Telegram chat is linked to (telegram.LangSource).
func (d *DBLists) ChatLang(ctx context.Context, chatID int64) string {
	var lang string
	_ = d.db.QueryRow(ctx, `
		SELECT COALESCE(p.preferences->>'language', '')
		  FROM user_connections c JOIN user_preferences p ON p.user_id = c.user_id
		 WHERE c.provider = 'telegram' AND c.external_id = $1`, strconv.FormatInt(chatID, 10)).Scan(&lang)
	return lang
}

// UserByChat is the user a Telegram chat is linked to (the eval runs as that user).
func (d *DBLists) UserByChat(ctx context.Context, chatID int64) (uuid.UUID, error) {
	var id uuid.UUID
	err := d.db.QueryRow(ctx, `SELECT user_id FROM user_connections WHERE provider = 'telegram' AND external_id = $1`,
		strconv.FormatInt(chatID, 10)).Scan(&id)
	return id, err
}

func (d *DBLists) Lists(ctx context.Context, userID uuid.UUID) (Lists, error) {
	var l Lists
	rows, err := d.db.Query(ctx, `
		SELECT a.id, a.name, a.id = s.default_account_id
		  FROM accounts a
		  LEFT JOIN user_notification_settings s ON s.user_id = $1
		 WHERE a.status = 'active' AND `+shared.ActiveMembershipPredicate("a.id", "$1")+`
		 ORDER BY (SELECT am.sort_order FROM account_members am
		            WHERE am.account_id = a.id AND am.user_id = $1 LIMIT 1), a.created_at`, userID)
	if err != nil {
		return l, fmt.Errorf("wallets: %w", err)
	}
	for rows.Next() {
		var w Wallet
		var main *bool
		if err := rows.Scan(&w.ID, &w.Name, &main); err != nil {
			rows.Close()
			return l, err
		}
		w.Main = main != nil && *main
		l.Wallets = append(l.Wallets, w)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return l, err
	}
	rows, err = d.db.Query(ctx, `
		SELECT c.id, c.name, c.type, COALESCE(p.name, ''),
		       COALESCE(NULLIF(c.icon_code->>'icon', ''), p.icon_code->>'icon', '')
		  FROM categories c LEFT JOIN categories p ON p.id = c.parent_id
		 WHERE c.user_id = $1 AND c.is_system = FALSE
		 ORDER BY c.type, c.sort_order, c.created_at`, userID)
	if err != nil {
		return l, fmt.Errorf("categories: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.Parent, &c.Icon); err != nil {
			return l, err
		}
		l.Categories = append(l.Categories, c)
	}
	if err := rows.Err(); err != nil {
		return l, err
	}
	rows.Close()
	return l, d.moreLists(ctx, userID, &l)
}

// moreLists reads the rest of what the chatbot can point at: savings goals, contacts, events with their members
// and the recurring rules (each with the slot a payment would take now).
func (d *DBLists) moreLists(ctx context.Context, userID uuid.UUID, l *Lists) error {
	rows, err := d.db.Query(ctx, `SELECT id, name FROM saving_goals WHERE user_id = $1 AND status = 'active' ORDER BY created_at, id`, userID)
	if err != nil {
		return fmt.Errorf("goals: %w", err)
	}
	for rows.Next() {
		var g Goal
		if err := rows.Scan(&g.ID, &g.Name); err != nil {
			rows.Close()
			return err
		}
		l.Goals = append(l.Goals, g)
	}
	rows.Close()
	rows, err = d.db.Query(ctx, `SELECT id, display_name, linked_user_id IS NOT NULL FROM contacts
		WHERE user_id = $1 AND status = 'active' ORDER BY last_used_at DESC NULLS LAST, display_name LIMIT 100`, userID)
	if err != nil {
		return fmt.Errorf("contacts: %w", err)
	}
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.ID, &c.Name, &c.Linked); err != nil {
			rows.Close()
			return err
		}
		l.Contacts = append(l.Contacts, c)
	}
	rows.Close()
	rows, err = d.db.Query(ctx, `SELECT p.id, p.name, p.status = 'completed' FROM projects p
		JOIN project_members m ON m.project_id = p.id AND m.user_id = $1 AND m.status = 'active'
		WHERE p.status IN ('active', 'completed') ORDER BY (p.status = 'active') DESC, p.created_at DESC LIMIT 20`, userID)
	if err != nil {
		return fmt.Errorf("events: %w", err)
	}
	var ids []uuid.UUID
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Name, &e.Completed); err != nil {
			rows.Close()
			return err
		}
		l.Events = append(l.Events, e)
		ids = append(ids, e.ID)
	}
	rows.Close()
	if len(ids) > 0 {
		rows, err = d.db.Query(ctx, `SELECT project_id, id, display_name, user_id = $2 FROM project_members
			WHERE project_id = ANY($1) AND status = 'active' ORDER BY created_at, id`, ids, userID)
		if err != nil {
			return fmt.Errorf("event members: %w", err)
		}
		byEvent := map[uuid.UUID][]Member{}
		for rows.Next() {
			var pid uuid.UUID
			var m Member
			var me *bool
			if err := rows.Scan(&pid, &m.ID, &m.Name, &me); err != nil {
				rows.Close()
				return err
			}
			m.Me = me != nil && *me
			byEvent[pid] = append(byEvent[pid], m)
		}
		rows.Close()
		for i := range l.Events {
			l.Events[i].Members = byEvent[l.Events[i].ID]
		}
	}
	if d.rules == nil {
		return nil
	}
	rules, err := d.rules.ChatRules(ctx, userID)
	if err != nil {
		return fmt.Errorf("rules: %w", err)
	}
	n := time.Now().In(bangkok)
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	for _, r := range rules {
		slot := ""
		if s, ok, err := d.rules.PickSlot(ctx, userID, r.ID, today); err == nil && ok {
			slot = s.Format(time.DateOnly)
		}
		l.Rules = append(l.Rules, Rule{ID: r.ID, Name: r.Name, Type: r.Type, Amount: r.Amount, AccountID: r.AccountID,
			CategoryID: r.CategoryID, Description: r.Description, Slot: slot})
	}
	return nil
}
