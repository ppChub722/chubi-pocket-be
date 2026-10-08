package projects

import (
	"context"
	"fmt"
	"math"

	"github.com/google/uuid"
)

// MemberPosition — one member's standing in the project (contract §6a).
//
//   - paid  = expense parents recorded with this member as the actor.
//   - share = this member's split rows; a parent with no split rows is
//     the actor's own spend, so it counts fully as the actor's share.
//   - net   = paid − share (> 0: the others owe them).
//
// Income parents are left out — they're refunds / pot top-ups, not spend.
type MemberPosition struct {
	MemberID    uuid.UUID  `json:"member_id"`
	UserID      *uuid.UUID `json:"user_id"`
	DisplayName string     `json:"display_name"`
	Paid        float64    `json:"paid"`
	Share       float64    `json:"share"`
	Net         float64    `json:"net"`
}

// CategoryTotal — expense parents grouped by their category name snapshot.
// Rows without a category come back as name "" (the client localises it).
type CategoryTotal struct {
	Name  string  `json:"name"`
	Total float64 `json:"total"`
	Count int     `json:"count"`
}

// fillBreakdowns adds members / my_position / by_category to resp.
// Members are the active ones plus anyone who left but still has money in
// the project.
func (s *Store) fillBreakdowns(ctx context.Context, resp *SummaryResponse, callerID uuid.UUID) error {
	rows, err := s.db.Query(ctx, `
		WITH parents AS (
			SELECT id, transaction_member_id AS m, amount
			FROM project_transactions
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
				SELECT p.m, p.amount FROM parents p
				WHERE NOT EXISTS (SELECT 1 FROM kids k WHERE k.pid = p.id)
			) x GROUP BY m
		)
		SELECT pm.id, pm.user_id, pm.display_name,
		       COALESCE(paid.v, 0), COALESCE(share.v, 0)
		FROM project_members pm
		LEFT JOIN paid  ON paid.m  = pm.id
		LEFT JOIN share ON share.m = pm.id
		WHERE pm.project_id = $1
		  AND (pm.status = 'active' OR paid.v IS NOT NULL OR share.v IS NOT NULL)
		ORDER BY COALESCE(paid.v, 0) DESC, pm.display_name`, resp.ProjectID)
	if err != nil {
		return fmt.Errorf("member positions: %w", err)
	}
	resp.Members = make([]MemberPosition, 0)
	for rows.Next() {
		var p MemberPosition
		if err := rows.Scan(&p.MemberID, &p.UserID, &p.DisplayName, &p.Paid, &p.Share); err != nil {
			rows.Close()
			return err
		}
		p.Net = round2(p.Paid - p.Share)
		resp.Members = append(resp.Members, p)
		if p.UserID != nil && *p.UserID == callerID {
			me := p
			resp.MyPosition = &me
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = s.db.Query(ctx, `
		SELECT COALESCE(category_name, ''), SUM(amount), COUNT(*)
		FROM project_transactions
		WHERE project_id = $1 AND parent_project_transaction_id IS NULL AND type = 'expense'
		GROUP BY COALESCE(category_name, '')
		ORDER BY SUM(amount) DESC`, resp.ProjectID)
	if err != nil {
		return fmt.Errorf("by category: %w", err)
	}
	defer rows.Close()
	resp.ByCategory = make([]CategoryTotal, 0)
	for rows.Next() {
		var c CategoryTotal
		if err := rows.Scan(&c.Name, &c.Total, &c.Count); err != nil {
			return err
		}
		resp.ByCategory = append(resp.ByCategory, c)
	}
	return rows.Err()
}

func round2(f float64) float64 {
	return math.Round(f*100) / 100
}
