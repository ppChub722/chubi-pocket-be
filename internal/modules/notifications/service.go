package notifications

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
)

type Service struct {
	store *Store
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

func (s *Service) SeedSettings(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	return s.store.SeedSettingsTx(ctx, tx, userID)
}

func (s *Service) LookupUserByEmail(ctx context.Context, email string) (*uuid.UUID, error) {
	return s.store.LookupUserByEmail(ctx, email)
}

func (s *Service) LookupUserByUsername(ctx context.Context, username string) (*uuid.UUID, error) {
	return s.store.LookupUserByUsername(ctx, username)
}

func (s *Service) HasPendingContactLinkRequest(
	ctx context.Context, recipientUserID, actorUserID, contactID uuid.UUID,
) (bool, error) {
	return s.store.HasPendingByPayloadKey(
		ctx, TypeContactLinkRequest,
		recipientUserID, actorUserID,
		"contact_id", contactID.String(),
	)
}

func (s *Service) GetSettings(ctx context.Context, userID uuid.UUID) (*Settings, error) {
	return s.store.GetSettings(ctx, userID)
}

// Notification-page groups (0.4.1-2 P7).
const (
	GroupSharedBill = "shared_bill"
	GroupEvent      = "event"
	GroupRecurring  = "recurring"
	GroupRequest    = "request"
)

// pageTypes — every type the notification settings page lists, in page order; every one can be
// muted (requests too since 0.4.2-3 F2, each with its own auto-accept switch).
var pageTypes = []struct{ Type, Group string }{
	{TypeSharedBillCreated, GroupSharedBill},
	{TypeSharedBillDeleted, GroupSharedBill},
	{TypeSharedBillDisconnected, GroupSharedBill},
	{TypeDebtDisconnected, GroupSharedBill},
	{TypeProjectTxChanged, GroupEvent},
	{TypeEventCompleted, GroupEvent},
	{TypeEventMemberRemoved, GroupEvent},
	{TypeRecurringDue, GroupRecurring},
	{TypeRecurringEnding, GroupRecurring},
	{TypeRecurringAutoCreated, GroupRecurring},
	{TypeProjectInvite, GroupRequest},
	{TypeContactLinkRequest, GroupRequest},
	{TypeAccountInvite, GroupRequest},
}

const (
	syncEvents = "events"
	syncDebts  = "debts"
)

// syncSwitch — the auto-sync switch a notice's "apply" follows (S4). Shared-bill types are not
// here: their switch depends on the bill kind (callers read Delivery.SyncEvents / SyncDebts).
var syncSwitch = map[string]string{
	TypeProjectTxChanged: syncEvents, // update my copy to match (shared-bill callers pick by kind)
}

var (
	mutableTypes = func() map[string]bool {
		m := map[string]bool{}
		for _, t := range pageTypes {
			m[t.Type] = true
		}
		return m
	}()
	// retiredTypes — no longer sent (project_added: F2, an invite replaces it); an older app may
	// still send its mute, which is dropped quietly.
	retiredTypes = map[string]bool{TypeProjectAdded: true}
)

var (
	ErrUnknownType       = errors.New("unknown or unsupported notification type")
	ErrTypeNotMutable    = errors.New("invites and link requests can't be muted")
	ErrDefaultAccountBad = apierr.Invalid("default_account_id must be an active wallet you're a member of", "default_account_id", apierr.FieldInvalid)
)

func cleanTypes(in []string, allowed map[string]bool) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, t := range in {
		switch {
		case retiredTypes[t]:
		case !allowed[t]:
			return nil, ErrUnknownType
		case !seen[t]:
			seen[t] = true
			out = append(out, t)
		}
	}
	return out, nil
}

func (s *Service) UpdateSettings(ctx context.Context, userID uuid.UUID, req UpdateSettingsRequest) (*Settings, error) {
	if req.MutedTypes != nil {
		clean, err := cleanTypes(*req.MutedTypes, mutableTypes)
		if err != nil {
			return nil, err
		}
		req.MutedTypes = &clean
	}
	if req.DefaultAccountID != nil && !req.ClearDefaultAccount {
		ok, err := s.store.IsActiveWalletMember(ctx, userID, *req.DefaultAccountID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrDefaultAccountBad
		}
	}
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

func (s *Service) GetByIDForCallerTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID) (*Notification, error) {
	return s.store.GetByIDForCallerTx(ctx, tx, userID, id)
}

func (s *Service) MarkActionedTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID) (*Notification, error) {
	return s.store.MarkActionedTx(ctx, tx, userID, id)
}

func (s *Service) MarkDismissedTx(ctx context.Context, tx pgx.Tx, userID, id uuid.UUID) (*Notification, error) {
	return s.store.MarkDismissedTx(ctx, tx, userID, id)
}

// SettingsCounts — the settings main page line: mutable types not muted, of all mutable types.
func SettingsCounts(muted []string) (on, total int) {
	m := map[string]bool{}
	for _, t := range muted {
		m[t] = true
	}
	for t := range mutableTypes {
		if !m[t] {
			on++
		}
	}
	return on, len(mutableTypes)
}
