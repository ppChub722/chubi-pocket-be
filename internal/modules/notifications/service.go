package notifications

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Service struct {
	store *Store
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

// SeedSettings is the auth.RegistrationHook entrypoint. Inserts a default
// notification-settings row for the new user inside the registration tx.
func (s *Service) SeedSettings(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	return s.store.SeedSettingsTx(ctx, tx, userID)
}

// LookupUserByEmail returns the user id for an active user with a matching
// email, or nil if none. Always-execute path — callers run it on hits and
// misses to keep response timing flat (privacy preservation).
func (s *Service) LookupUserByEmail(ctx context.Context, email string) (*uuid.UUID, error) {
	return s.store.LookupUserByEmail(ctx, email)
}

func (s *Service) LookupUserByUsername(ctx context.Context, username string) (*uuid.UUID, error) {
	return s.store.LookupUserByUsername(ctx, username)
}

// HasPendingContactLinkRequest reports whether [recipientUserID] still
// has an unresolved `contact_link_request` notification from
// [actorUserID] for the same [contactID]. Used by contacts.RequestLink
// to make a re-send a no-op while the previous request is still in the
// inbox awaiting Accept / Reject / Dismiss. Resolved (or hard-deleted)
// rows don't count, so a follow-up request after rejection works.
func (s *Service) HasPendingContactLinkRequest(
	ctx context.Context, recipientUserID, actorUserID, contactID uuid.UUID,
) (bool, error) {
	return s.store.HasPendingByPayloadKey(
		ctx, TypeContactLinkRequest,
		recipientUserID, actorUserID,
		"contact_id", contactID.String(),
	)
}

// GetSettings returns the caller's settings row.
func (s *Service) GetSettings(ctx context.Context, userID uuid.UUID) (*Settings, error) {
	return s.store.GetSettings(ctx, userID)
}

// UpdateSettings applies a partial patch.
func (s *Service) UpdateSettings(ctx context.Context, userID uuid.UUID, req UpdateSettingsRequest) (*Settings, error) {
	return s.store.UpdateSettings(ctx, userID, req)
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, f ListFilter) (*ListResponse, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PerPage <= 0 {
		f.PerPage = 20
	}
	if f.PerPage > 100 {
		f.PerPage = 100
	}
	rows, total, unread, err := s.store.List(ctx, userID, f)
	if err != nil {
		return nil, err
	}
	totalPages := (total + f.PerPage - 1) / f.PerPage
	if totalPages == 0 {
		totalPages = 1
	}
	return &ListResponse{
		Data:        rows,
		UnreadCount: unread,
		Pagination: Pagination{
			Page:       f.Page,
			PerPage:    f.PerPage,
			Total:      total,
			TotalPages: totalPages,
		},
	}, nil
}

func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (*Notification, error) {
	return s.store.GetByID(ctx, userID, id)
}

func (s *Service) MarkRead(ctx context.Context, userID, id uuid.UUID) (*Notification, error) {
	return s.store.MarkRead(ctx, userID, id)
}

func (s *Service) MarkReadAll(ctx context.Context, userID uuid.UUID) (int, error) {
	return s.store.MarkReadAll(ctx, userID)
}

func (s *Service) MarkActioned(ctx context.Context, userID, id uuid.UUID) (*Notification, error) {
	return s.store.MarkActioned(ctx, userID, id)
}

func (s *Service) MarkDismissed(ctx context.Context, userID, id uuid.UUID) (*Notification, error) {
	return s.store.MarkDismissed(ctx, userID, id)
}

func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	return s.store.Delete(ctx, userID, id)
}

// --- Tx-aware variants for cross-module callers ---
//
// Producer modules (contacts, projects) sometimes need to read or mark a
// notification inside their own action's tx — e.g., the link-request
// accept flow reads the payload then marks actioned in the same tx as
// setting contacts.linked_user_id.

func (s *Service) GetByIDForCallerTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID) (*Notification, error) {
	return s.store.GetByIDForCallerTx(ctx, tx, userID, id)
}

func (s *Service) MarkActionedTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID) (*Notification, error) {
	return s.store.MarkActionedTx(ctx, tx, userID, id)
}

func (s *Service) MarkDismissedTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID) (*Notification, error) {
	return s.store.MarkDismissedTx(ctx, tx, userID, id)
}
