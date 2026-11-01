package chatbot

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// DBLists reads the lists straight from the database (read only).
type DBLists struct{ db *pgxpool.Pool }

func NewDBLists(db *pgxpool.Pool) *DBLists { return &DBLists{db: db} }

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
		SELECT c.id, c.name, c.type, COALESCE(p.name, '')
		  FROM categories c LEFT JOIN categories p ON p.id = c.parent_id
		 WHERE c.user_id = $1 AND c.is_system = FALSE
		 ORDER BY c.type, c.sort_order, c.created_at`, userID)
	if err != nil {
		return l, fmt.Errorf("categories: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.Parent); err != nil {
			return l, err
		}
		l.Categories = append(l.Categories, c)
	}
	return l, rows.Err()
}
