package notifications

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type ListView struct {
	Data        []shared.NotificationView `json:"data"`
	UnreadCount int                       `json:"unread_count"`
	Summary     InboxSummary              `json:"summary"`
	Pagination  shared.Page               `json:"pagination"`
}

type ActionResult struct {
	Notification shared.NotificationView `json:"notification"`
	Badges       shared.Badges           `json:"badges"`
	Summary      InboxSummary            `json:"summary"`
}

type ReadAllResult struct {
	MarkedRead int           `json:"marked_read"`
	Summary    InboxSummary  `json:"summary"`
	Badges     shared.Badges `json:"badges"`
}

type DeleteResult struct {
	ID      uuid.UUID     `json:"id"`
	Summary InboxSummary  `json:"summary"`
	Badges  shared.Badges `json:"badges"`
}

type SettingsView struct {
	MutedTypes              []string           `json:"muted_types"`
	Types                   []TypeSetting      `json:"types"`
	AutoSyncEvents          bool               `json:"auto_sync_events"`
	AutoSyncDebts           bool               `json:"auto_sync_debts"`
	AutoAcceptEvents        bool               `json:"auto_accept_events"`
	AutoAcceptDebts         bool               `json:"auto_accept_debts"`
	AutoAcceptEventInvites  bool               `json:"auto_accept_event_invites"`
	AutoAcceptContactLinks  bool               `json:"auto_accept_contact_links"`
	AutoAcceptWalletInvites bool               `json:"auto_accept_wallet_invites"`
	DefaultAccount          *shared.AccountRef `json:"default_account"`
	UpdatedAt               time.Time          `json:"updated_at"`
}

// TypeSetting — one row of the notification settings page (0.4.1-2 P7).
type TypeSetting struct {
	Type    string `json:"type"`
	Group   string `json:"group"`
	On      bool   `json:"on"`
	Mutable bool   `json:"mutable"`
}

func (s *Service) Badges(ctx context.Context, userID uuid.UUID) (shared.Badges, error) {
	return shared.LoadBadges(ctx, s.store.db, userID)
}

func (s *Service) ListViews(ctx context.Context, userID uuid.UUID, f ListFilter) (*ListView, error) {
	if f.AwaitsAnswer {
		ids, err := s.awaitingIDs(ctx, userID)
		if err != nil {
			return nil, err
		}
		f.IDs = ids
	}
	r, err := s.List(ctx, userID, f)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(r.Data))
	for i, n := range r.Data {
		ids[i] = n.ID
	}
	views, err := shared.LoadNotificationViews(ctx, s.store.db, userID, ids)
	if err != nil {
		return nil, err
	}
	sum, err := s.inboxSummary(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &ListView{
		Data:        views,
		UnreadCount: r.UnreadCount,
		Pagination:  shared.Page(r.Pagination),
		Summary:     sum,
	}, nil
}

func (s *Service) actionResult(ctx context.Context, userID uuid.UUID, n *Notification, err error) (*ActionResult, error) {
	if err != nil {
		return nil, err
	}
	v, err := shared.LoadNotificationView(ctx, s.store.db, userID, n.ID)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, ErrNotificationNotFound
	}
	b, err := s.Badges(ctx, userID)
	if err != nil {
		return nil, err
	}
	sum, err := s.inboxSummary(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &ActionResult{Notification: *v, Badges: b, Summary: sum}, nil
}

func (s *Service) MarkReadResult(ctx context.Context, userID, id uuid.UUID) (*ActionResult, error) {
	n, err := s.store.MarkRead(ctx, userID, id)
	return s.actionResult(ctx, userID, n, err)
}

func (s *Service) MarkActionedResult(ctx context.Context, userID, id uuid.UUID) (*ActionResult, error) {
	n, err := s.store.MarkActioned(ctx, userID, id)
	return s.actionResult(ctx, userID, n, err)
}

func (s *Service) MarkDismissedResult(ctx context.Context, userID, id uuid.UUID) (*ActionResult, error) {
	n, err := s.store.MarkDismissed(ctx, userID, id)
	return s.actionResult(ctx, userID, n, err)
}

func (s *Service) settingsView(ctx context.Context, st *Settings) (*SettingsView, error) {
	v := &SettingsView{
		MutedTypes:              st.MutedTypes,
		Types:                   make([]TypeSetting, 0, len(pageTypes)),
		AutoSyncEvents:          st.AutoSyncEvents,
		AutoSyncDebts:           st.AutoSyncDebts,
		AutoAcceptEvents:        st.AutoAcceptEvents,
		AutoAcceptDebts:         st.AutoAcceptDebts,
		AutoAcceptEventInvites:  st.AutoAcceptEventInvites,
		AutoAcceptContactLinks:  st.AutoAcceptContactLinks,
		AutoAcceptWalletInvites: st.AutoAcceptWalletInvites,
		UpdatedAt:               st.UpdatedAt,
	}
	if v.MutedTypes == nil {
		v.MutedTypes = []string{}
	}
	for _, t := range pageTypes {
		mutable := t.Group != GroupRequest
		v.Types = append(v.Types, TypeSetting{
			Type: t.Type, Group: t.Group, Mutable: mutable,
			On: !mutable || st.Deliver(t.Type),
		})
	}
	if st.DefaultAccountID != nil {
		refs, err := shared.LoadAccountRefs(ctx, s.store.db, []uuid.UUID{*st.DefaultAccountID})
		if err != nil {
			return nil, err
		}
		if r, ok := refs[*st.DefaultAccountID]; ok {
			v.DefaultAccount = &r
		}
	}
	return v, nil
}

func (s *Service) GetSettingsView(ctx context.Context, userID uuid.UUID) (*SettingsView, error) {
	st, err := s.GetSettings(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.settingsView(ctx, st)
}

func (s *Service) UpdateSettingsView(ctx context.Context, userID uuid.UUID, req UpdateSettingsRequest) (*SettingsView, error) {
	st, err := s.UpdateSettings(ctx, userID, req)
	if err != nil {
		return nil, err
	}
	return s.settingsView(ctx, st)
}

func (s *Service) RestoreResult(ctx context.Context, userID, id uuid.UUID) (*ActionResult, error) {
	n, err := s.store.Restore(ctx, userID, id)
	return s.actionResult(ctx, userID, n, err)
}

type InboxSummary struct {
	All         int `json:"all"` // everything not dismissed (the ทั้งหมด tile)
	NeedsAnswer int `json:"needs_answer"`
	Unread      int `json:"unread"`
	Dismissed   int `json:"dismissed"`
}

func (s *Service) inboxSummary(ctx context.Context, userID uuid.UUID) (InboxSummary, error) {
	return LoadInboxSummary(ctx, s.store.db, userID)
}

func LoadInboxSummary(ctx context.Context, db shared.Querier, userID uuid.UUID) (InboxSummary, error) {
	var out InboxSummary
	rows, err := db.Query(ctx, `SELECT type, payload, read_at, actioned_at, dismissed_at
		FROM notifications WHERE recipient_user_id = $1`, userID)
	if err != nil {
		return out, fmt.Errorf("inbox summary: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			typ                             string
			payload                         []byte
			readAt, actionedAt, dismissedAt *time.Time
		)
		if err := rows.Scan(&typ, &payload, &readAt, &actionedAt, &dismissedAt); err != nil {
			return out, err
		}
		switch {
		case dismissedAt != nil:
			out.Dismissed++
			continue
		case readAt == nil:
			out.Unread++
		}
		out.All++
		if shared.AwaitsAnswer(typ, actionedAt, dismissedAt, payload) {
			out.NeedsAnswer++
		}
	}
	return out, rows.Err()
}

// awaitingIDs — the ids of the rows that still wait for an answer (never nil).
func (s *Service) awaitingIDs(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.store.db.Query(ctx, `SELECT id, type, payload, actioned_at, dismissed_at FROM notifications
		WHERE recipient_user_id = $1 AND actioned_at IS NULL AND dismissed_at IS NULL`, userID)
	if err != nil {
		return nil, fmt.Errorf("awaiting: %w", err)
	}
	defer rows.Close()
	ids := []uuid.UUID{}
	for rows.Next() {
		var (
			id                      uuid.UUID
			typ                     string
			payload                 []byte
			actionedAt, dismissedAt *time.Time
		)
		if err := rows.Scan(&id, &typ, &payload, &actionedAt, &dismissedAt); err != nil {
			return nil, err
		}
		if shared.AwaitsAnswer(typ, actionedAt, dismissedAt, payload) {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}
