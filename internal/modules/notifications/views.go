package notifications

// HTTP shapes and display-ready service methods (API spec v2).

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// ListView is the spec schema NotificationList.
type ListView struct {
	Data        []shared.NotificationView `json:"data"`
	UnreadCount int                       `json:"unread_count"`
	Pagination  shared.Page               `json:"pagination"`
}

// ActionResult is the spec schema NotificationActionResult.
type ActionResult struct {
	Notification shared.NotificationView `json:"notification"`
	Badges       shared.Badges           `json:"badges"`
}

// ReadAllResult is the spec schema ReadAllResult.
type ReadAllResult struct {
	MarkedRead int           `json:"marked_read"`
	Badges     shared.Badges `json:"badges"`
}

// DeleteResult is the spec schema DeleteNotificationResult.
type DeleteResult struct {
	ID     uuid.UUID     `json:"id"`
	Badges shared.Badges `json:"badges"`
}

// SettingsView is the spec schema NotificationSettings.
type SettingsView struct {
	MutedTypes               []string           `json:"muted_types"`
	AutoTypes                []string           `json:"auto_types"`
	DefaultAccount           *shared.AccountRef `json:"default_account"`
	AutoResolveOwnInProjects bool               `json:"auto_resolve_own_in_projects"`
	UpdatedAt                time.Time          `json:"updated_at"`
}

func (s *Service) Badges(ctx context.Context, userID uuid.UUID) (shared.Badges, error) {
	return shared.LoadBadges(ctx, s.store.db, userID)
}

// ListViews is the inbox: items built by shared.LoadNotificationViews in list order.
func (s *Service) ListViews(ctx context.Context, userID uuid.UUID, f ListFilter) (*ListView, error) {
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
	return &ListView{
		Data:        views,
		UnreadCount: r.UnreadCount,
		Pagination:  shared.Page(r.Pagination),
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
	return &ActionResult{Notification: *v, Badges: b}, nil
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
		MutedTypes:               st.MutedTypes,
		AutoTypes:                st.AutoTypes,
		AutoResolveOwnInProjects: st.AutoResolveOwnInProjects,
		UpdatedAt:                st.UpdatedAt,
	}
	if v.MutedTypes == nil {
		v.MutedTypes = []string{}
	}
	if v.AutoTypes == nil {
		v.AutoTypes = []string{}
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

// RestoreResult un-hides a dismissed notification.
func (s *Service) RestoreResult(ctx context.Context, userID, id uuid.UUID) (*ActionResult, error) {
	n, err := s.store.Restore(ctx, userID, id)
	return s.actionResult(ctx, userID, n, err)
}
