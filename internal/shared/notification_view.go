package shared

// The display-ready notification (spec schema Notification), built in ONE
// place so the inbox, the Home "ต้องจัดการ" block and every action response
// (read / dismiss / link-request / invite …) return the same shape.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// NotificationView — spec schema Notification.
type NotificationView struct {
	ID           uuid.UUID       `json:"id"`
	Type         string          `json:"type"`
	Actor        *UserRef        `json:"actor"`
	Payload      json.RawMessage `json:"payload"`
	DeepLink     *string         `json:"deep_link"`
	State        string          `json:"state"` // unread | read | actioned | dismissed
	AwaitsAnswer bool            `json:"awaits_answer"`
	ReadAt       *time.Time      `json:"read_at"`
	ActionedAt   *time.Time      `json:"actioned_at"`
	DismissedAt  *time.Time      `json:"dismissed_at"`
	CreatedAt    time.Time       `json:"created_at"`
}

// NotificationState — actioned > dismissed > read > unread.
func NotificationState(readAt, actionedAt, dismissedAt *time.Time) string {
	switch {
	case actionedAt != nil:
		return "actioned"
	case dismissedAt != nil:
		return "dismissed"
	case readAt != nil:
		return "read"
	}
	return "unread"
}

// AwaitsAnswer — the row still waits for the user (owner / FE rule): not
// actioned, not dismissed, and its type asks something of the user:
// contact_link_request / account_invite / project_invite / recurring_ending (ต่ออีก / ปล่อยให้จบ), or a one-tap action type —
// split_created / split_changed (unless payload.superseded),
// project_tx_recorded_for_you, project_tx_changed (only when
// payload.personal_transaction_id AND payload.suggested are set).
func AwaitsAnswer(typ string, actionedAt, dismissedAt *time.Time, payload json.RawMessage) bool {
	if actionedAt != nil || dismissedAt != nil {
		return false
	}
	var p map[string]any
	_ = json.Unmarshal(payload, &p)
	has := func(k string) bool {
		v, ok := p[k]
		return ok && v != nil && v != false && v != ""
	}
	switch typ {
	case "contact_link_request", "account_invite", "project_invite", "recurring_ending":
		return true
	case "split_created", "split_changed":
		return !has("superseded")
	case "project_tx_recorded_for_you":
		return true
	case "project_tx_changed":
		return has("personal_transaction_id") && has("suggested")
	}
	return false
}

// LoadNotificationViews builds the views of the caller's notifications by id
// (any state), in the order of ids; unknown ids are skipped.
func LoadNotificationViews(ctx context.Context, q Querier, userID uuid.UUID, ids []uuid.UUID) ([]NotificationView, error) {
	ids = uniqueIDs(ids)
	out := make([]NotificationView, 0, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT n.id, n.type, n.payload, n.deep_link, n.read_at, n.actioned_at, n.dismissed_at, n.created_at,
			u.id, u.display_name, u.icon_code
		FROM notifications n LEFT JOIN users u ON u.id = n.actor_user_id
		WHERE n.id = ANY($1) AND n.recipient_user_id = $2`, ids, userID)
	if err != nil {
		return nil, fmt.Errorf("notification views: %w", err)
	}
	defer rows.Close()
	byID := map[uuid.UUID]NotificationView{}
	for rows.Next() {
		var (
			v         NotificationView
			actorID   *uuid.UUID
			actorName *string
			actorIcon []byte
		)
		if err := rows.Scan(&v.ID, &v.Type, &v.Payload, &v.DeepLink, &v.ReadAt, &v.ActionedAt, &v.DismissedAt, &v.CreatedAt,
			&actorID, &actorName, &actorIcon); err != nil {
			return nil, err
		}
		if len(v.Payload) == 0 {
			v.Payload = json.RawMessage(`{}`)
		}
		if actorID != nil && actorName != nil {
			v.Actor = &UserRef{UserID: *actorID, DisplayName: *actorName, IconCode: IconFromJSON(actorIcon)}
		}
		v.State = NotificationState(v.ReadAt, v.ActionedAt, v.DismissedAt)
		v.AwaitsAnswer = AwaitsAnswer(v.Type, v.ActionedAt, v.DismissedAt, v.Payload)
		byID[v.ID] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if v, ok := byID[id]; ok {
			out = append(out, v)
		}
	}
	return out, nil
}

// LoadNotificationView — one notification, or (nil, nil) when it isn't the
// caller's.
func LoadNotificationView(ctx context.Context, q Querier, userID, id uuid.UUID) (*NotificationView, error) {
	vs, err := LoadNotificationViews(ctx, q, userID, []uuid.UUID{id})
	if err != nil || len(vs) == 0 {
		return nil, err
	}
	return &vs[0], nil
}
