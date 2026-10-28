package projects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type Service struct {
	store  *Store
	notifs *notifications.Service
	txs    *transactions.Service
	debts  *personal_debts.Service // E8: the bill sheet writes my debts and repayments
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

func (s *Service) WithNotificationService(n *notifications.Service) { s.notifs = n }

func (s *Service) WithTransactionsService(t *transactions.Service) { s.txs = t }

func (s *Service) Create(ctx context.Context, ownerUserID uuid.UUID, req CreateProjectRequest) (*EventDetail, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	p, err := s.createTx(ctx, tx, ownerUserID, req)
	if err != nil {
		return nil, err
	}
	if err := setMyTagsTx(ctx, tx, p.ID, ownerUserID, req.TagIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.detail(ctx, ownerUserID, p.ID, 1, 20)
}

func (s *Service) createTx(ctx context.Context, tx pgx.Tx, ownerUserID uuid.UUID, req CreateProjectRequest) (*Project, error) {
	p, err := s.store.CreateInTx(ctx, tx, ownerUserID, req)
	if err != nil {
		return nil, err
	}
	ownerName := userDisplayOrEmpty(ctx, tx, ownerUserID)
	if ownerName == "" {
		ownerName = "Owner"
	}
	if _, err := s.store.InsertMemberTx(ctx, tx,
		p.ID, &ownerUserID, ownerName, RoleOwner, MemberStatusActive, ownerUserID, nil,
	); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, f ListFilter) (*EventList, error) {
	return s.list(ctx, userID, f, false)
}

// ListAll — every event in one page (the sync dataset); no 100-row cap.
func (s *Service) ListAll(ctx context.Context, userID uuid.UUID, f ListFilter) (*EventList, error) {
	return s.list(ctx, userID, f, true)
}

func (s *Service) list(ctx context.Context, userID uuid.UUID, f ListFilter, unlimited bool) (*EventList, error) {
	if unlimited {
		f.Page, f.PerPage = 1, unlimitedPerPage
	} else {
		f.Page, f.PerPage = clampPage(f.Page, f.PerPage)
	}
	if derived := derivedStatus[f.Status]; derived != nil {
		return s.listDerived(ctx, userID, f, derived)
	}
	ids, total, err := s.store.ListIDs(ctx, userID, f)
	if err != nil {
		return nil, err
	}
	events, err := loadEvents(ctx, s.store.db, userID, ids)
	if err != nil {
		return nil, err
	}
	out := &EventList{Data: make([]Event, 0, len(ids)), Pagination: shared.NewPage(f.Page, f.PerPage, total)}
	if out.Summary, err = s.listSummary(ctx, userID); err != nil {
		return nil, err
	}
	for _, id := range ids {
		out.Data = append(out.Data, events[id])
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (*Event, error) {
	if _, err := s.store.GetByIDForCaller(ctx, userID, id); err != nil {
		return nil, err
	}
	e, err := loadEvent(ctx, s.store.db, userID, id)
	return &e, err
}

func (s *Service) Detail(ctx context.Context, userID, id uuid.UUID, page, perPage int, withDeletePreview bool) (*EventDetail, error) {
	d, err := s.detail(ctx, userID, id, page, perPage)
	if err != nil || !withDeletePreview {
		return d, err
	}
	p := &DeletePreview{Members: d.Event.MembersCount, LinkedTxCount: d.Event.LinkedTxCount}
	if err := s.store.db.QueryRow(ctx, `SELECT COUNT(*) FROM project_transactions WHERE project_id = $1 AND parent_project_transaction_id IS NULL`, id).Scan(&p.Bills); err != nil {
		return nil, err
	}
	d.DeletePreview = p
	return d, nil
}

func (s *Service) Summary(ctx context.Context, userID, id uuid.UUID) (*EventSummary, error) {
	if _, err := s.store.GetByIDForCaller(ctx, userID, id); err != nil {
		return nil, err
	}
	e, err := loadEvent(ctx, s.store.db, userID, id)
	if err != nil {
		return nil, err
	}
	sum, err := loadSummary(ctx, s.store.db, userID, id, e)
	return &sum, err
}

func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, req UpdateProjectRequest) (*EventPatch, error) {
	p, err := s.store.GetByIDForCaller(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if p.OwnerUserID != userID && !req.onlyMyTags() {
		return nil, ErrNotOwner
	}
	if err := completedUpdate(p, &req); err != nil {
		return nil, err
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if !req.onlyMyTags() {
		if err := s.store.UpdateTx(ctx, tx, userID, id, p, req); err != nil {
			return nil, err
		}
	}
	if req.has("tag_ids") {
		if err := setMyTagsTx(ctx, tx, id, userID, req.TagIDs); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.patch(ctx, userID, id, nil, nil, nil)
}

// completedUpdate — V2 rules for PUT /projects/{id}: `completed` only through /complete; a completed
// event takes only my tags and archive, and restoring it from the archive returns it to completed.
func completedUpdate(p *Project, req *UpdateProjectRequest) error {
	if req.Status != nil && *req.Status == StatusCompleted && p.Status != StatusCompleted {
		return ErrUseCompleteEndpoint
	}
	if p.CompletedAt == nil {
		return nil
	}
	for k := range req.present {
		switch {
		case k == "tag_ids":
		case k == "status" && req.Status != nil && (*req.Status == StatusArchived || *req.Status == StatusCompleted):
		case k == "status" && req.Status != nil && *req.Status == StatusActive && p.Status == StatusArchived:
			done := StatusCompleted // restore
			req.Status = &done
		default:
			return ErrEventCompleted
		}
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID, deleteLinked bool) (*EventDeleteResult, error) {
	p, err := s.store.GetByIDForCaller(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if p.OwnerUserID != userID {
		return nil, ErrNotOwner
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	// owner 2026-10-28: the bills stay — each becomes a standalone split bill (BE-1's billToSplitTx),
	// every copy / debt still connected; then the event (members, settle bookings) goes. A completed
	// event is unlocked first so its bills can move (the event row goes anyway).
	if _, err := tx.Exec(ctx, `UPDATE projects SET completed_at = NULL WHERE id = $1`, id); err != nil {
		return nil, fmt.Errorf("unlock: %w", err)
	}
	others, err := collectIDs(ctx, tx, `SELECT user_id FROM project_members
		WHERE project_id = $1 AND user_id IS NOT NULL AND user_id <> $2 AND status = 'active'`, id, userID)
	if err != nil {
		return nil, fmt.Errorf("members: %w", err)
	}
	out := &EventDeleteResult{ID: id, Effects: shared.NewEffects()}
	var splits []uuid.UUID
	for guard := 0; ; guard++ {
		var ptID uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM project_transactions WHERE project_id = $1 AND parent_project_transaction_id IS NULL
			ORDER BY date, created_at, id LIMIT 1`, id).Scan(&ptID)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("next bill: %w", err)
		}
		if guard > 10000 {
			return nil, errors.New("delete event: bills keep coming back")
		}
		split, err := s.billToSplitTx(ctx, tx, userID, ptID)
		if err != nil {
			return nil, err
		}
		splits = append(splits, split)
		out.SplitBillCount++
	}
	if err := s.store.DeleteTx(ctx, tx, id); err != nil {
		return nil, err
	}
	if s.notifs != nil {
		for _, u := range others {
			if err := s.notifs.DispatchEventDeleted(ctx, tx, u, userID, notifications.EventDeletedPayload{
				ProjectID: id, ProjectName: p.Name, SplitBillCount: out.SplitBillCount,
			}); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	// effects: my records, now pointing at the split bills
	myTxs, err := collectPoolIDs(ctx, s.store.db, `SELECT t.id FROM transactions t WHERE t.user_id = $2 AND (t.project_id = ANY($1)
		OR t.source_project_transaction_id IN (SELECT x.id FROM project_transactions x WHERE x.project_id = ANY($1)))`, splits, userID)
	if err != nil {
		return nil, err
	}
	for _, tid := range myTxs {
		if d, err := s.txs.Get(ctx, userID, tid); err == nil {
			out.Effects.Transactions = append(out.Effects.Transactions, *d)
		}
	}
	if s.debts != nil {
		myDebts, err := collectPoolIDs(ctx, s.store.db, `SELECT d.id FROM personal_debts d WHERE d.user_id = $2 AND (d.project_id = ANY($1)
			OR d.id IN (SELECT b.debt_id FROM personal_debt_bills b JOIN project_transactions x ON x.id = b.pt_id
				WHERE b.user_id = $2 AND x.project_id = ANY($1)))`, splits, userID)
		if err != nil {
			return nil, err
		}
		if len(myDebts) > 0 {
			if out.Effects.Debts, _, err = s.debts.DebtEffects(ctx, userID, myDebts, nil); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// collectPoolIDs — ids from a read outside a DB transaction.
func collectPoolIDs(ctx context.Context, q shared.Querier, sql string, args ...any) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("ids: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Service) Leave(ctx context.Context, userID, projectID uuid.UUID) (*DeletedResult, error) {
	p, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if p.OwnerUserID == userID {
		return nil, ErrOwnerCannotLeave
	}
	var memberID uuid.UUID
	if err := s.store.db.QueryRow(ctx,
		`SELECT id FROM project_members WHERE project_id = $1 AND user_id = $2 AND status = 'active'`,
		projectID, userID).Scan(&memberID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotMember
		}
		return nil, fmt.Errorf("find member: %w", err)
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, _, _, err := s.leaveTx(ctx, tx, projectID, memberID, userID, false); err != nil { // V15
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return &DeletedResult{ID: projectID}, nil
}

func (s *Service) TransferOwnership(ctx context.Context, callerUserID, projectID, newOwnerUserID uuid.UUID) (*EventPatch, error) {
	p, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if p.OwnerUserID != callerUserID {
		return nil, ErrNotOwner
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := s.store.TransferOwnershipTx(ctx, tx, projectID, callerUserID, newOwnerUserID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.patch(ctx, callerUserID, projectID, nil, nil, nil)
}

func (s *Service) ListMembers(ctx context.Context, userID, projectID uuid.UUID) (*EventMemberList, error) {
	if _, err := s.store.GetByIDForCaller(ctx, userID, projectID); err != nil {
		return nil, err
	}
	m, err := loadMembers(ctx, s.store.db, userID, projectID)
	if err != nil {
		return nil, err
	}
	return &EventMemberList{Data: m}, nil
}

func (s *Service) AddMember(ctx context.Context, callerUserID, projectID uuid.UUID, req AddMemberRequest) (*EventPatch, error) {
	p, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	// A typed person (ad_hoc) may be added by any member who may add bills (0.4.0-3: the same power
	// as auto-adding from a bill); an account invite (email) stays the owner's.
	if req.AdHoc && req.Email == nil {
		if _, err := s.store.AssertCanWrite(ctx, projectID, callerUserID); err != nil {
			return nil, err
		}
	} else if p.OwnerUserID != callerUserID {
		return nil, ErrNotOwner
	}
	if err := s.store.AssertWritable(ctx, projectID, "member_crud"); err != nil {
		return nil, err
	}
	role := req.Role
	if role == "" {
		role = RoleContributor
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	switch {
	case req.Email != nil && req.DisplayName != nil:
		_, err = s.addMemberByEmailTx(ctx, tx, projectID, callerUserID, *req.DisplayName, *req.Email, role, req.IconCode)
	case req.AdHoc && req.DisplayName != nil:
		_, err = s.store.InsertMemberTx(ctx, tx,
			projectID, nil, *req.DisplayName, role, MemberStatusActive, callerUserID, req.IconCode,
		)
	default:
		return nil, ErrBadMemberRequest
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.patch(ctx, callerUserID, projectID, nil, nil, nil)
}

func (s *Service) addMemberByEmailTx(
	ctx context.Context, tx pgx.Tx, projectID, callerUserID uuid.UUID,
	displayName, email, role string, iconCode *shared.IconCode,
) (*ProjectMember, error) {
	var matchedUser *uuid.UUID
	if s.notifs != nil {
		var err error
		matchedUser, err = s.notifs.LookupUserByEmail(ctx, email)
		if err != nil {
			return nil, err
		}
	}
	member, err := s.store.InsertMemberTx(ctx, tx,
		projectID, nil, displayName, role, MemberStatusPending, callerUserID, iconCode,
	)
	if err != nil {
		return nil, err
	}
	if matchedUser != nil && *matchedUser != callerUserID && s.notifs != nil {
		_ = s.notifs.DispatchProjectInvite(ctx, tx, *matchedUser, callerUserID,
			notifications.ProjectInvitePayload{
				ProjectMemberID:    member.ID,
				ProjectID:          projectID,
				ProjectName:        projectNameOrEmpty(ctx, tx, projectID),
				InviterUserID:      callerUserID,
				InviterDisplayName: userDisplayOrEmpty(ctx, tx, callerUserID),
				Role:               role,
			})
	}
	return member, nil
}

func (s *Service) UpdateMember(
	ctx context.Context, callerUserID, projectID, memberID uuid.UUID, req UpdateMemberRequest,
) (*EventPatch, error) {
	p, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if p.OwnerUserID != callerUserID {
		return nil, ErrNotOwner
	}
	if err := s.store.AssertWritable(ctx, projectID, "member_crud"); err != nil {
		return nil, err
	}
	target, err := s.store.GetMember(ctx, memberID)
	if err != nil {
		return nil, err
	}
	if target.ProjectID != projectID {
		return nil, ErrMemberNotInProject
	}
	if req.Role != nil {
		if target.Role == RoleOwner {
			return nil, ErrCannotChangeOwner
		}
		if _, err := s.store.UpdateMemberRole(ctx, projectID, memberID, *req.Role, callerUserID); err != nil {
			return nil, err
		}
	}
	if req.IconCode != nil {
		if _, err := s.store.UpdateMemberIconCode(ctx, projectID, memberID, req.IconCode, callerUserID); err != nil {
			return nil, err
		}
	}
	return s.patch(ctx, callerUserID, projectID, nil, nil, nil)
}

func (s *Service) RemoveMember(ctx context.Context, callerUserID, projectID, memberID uuid.UUID) (*EventPatch, error) {
	p, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if p.OwnerUserID != callerUserID {
		return nil, ErrNotOwner
	}
	if err := s.store.AssertWritable(ctx, projectID, "member_crud"); err != nil {
		return nil, err
	}
	target, err := s.store.GetMember(ctx, memberID)
	if err != nil {
		return nil, err
	}
	if target.ProjectID != projectID {
		return nil, ErrMemberNotInProject
	}
	if target.Role == RoleOwner {
		return nil, ErrCannotRemoveOwner
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	m, txs, debts, err := s.leaveTx(ctx, tx, projectID, memberID, callerUserID, true) // V15
	if err != nil {
		return nil, err
	}
	if m.UserID != nil && s.notifs != nil && m.Status != MemberStatusPending {
		if err := s.notifs.DispatchEventMemberRemoved(ctx, tx, *m.UserID, callerUserID, notifications.EventMemberRemovedPayload{
			ProjectID: projectID, ProjectName: p.Name, TransactionCount: txs, DebtCount: debts,
		}); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.patch(ctx, callerUserID, projectID, nil, nil, nil)
}

func (s *Service) RequestLink(ctx context.Context, callerUserID, projectID, memberID uuid.UUID) error {
	p, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return err
	}
	if p.OwnerUserID != callerUserID {
		return ErrNotOwner
	}
	target, err := s.store.GetMember(ctx, memberID)
	if err != nil {
		return err
	}
	if target.ProjectID != projectID {
		return ErrMemberNotInProject
	}
	if target.UserID != nil || target.Status != MemberStatusPending {
		return ErrNotPending
	}
	return nil
}

var ErrNotPending = errors.New("this member isn't waiting for an invite")

func (s *Service) AcceptLinkRequest(ctx context.Context, userID, notificationID uuid.UUID) (*EventDetail, error) {
	if s.notifs == nil {
		return nil, errors.New("notifications module not wired")
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	n, err := s.notifs.GetByIDForCallerTx(ctx, tx, userID, notificationID)
	if err != nil {
		return nil, err
	}
	if n.Type != notifications.TypeProjectInvite {
		return nil, ErrLinkRequestNotForYou
	}
	var p notifications.ProjectInvitePayload
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	member, err := s.store.GetMemberTx(ctx, tx, p.ProjectMemberID)
	if err != nil {
		return nil, err
	}
	switch {
	case member.UserID != nil && *member.UserID != userID:
		return nil, ErrMemberAlreadyExists
	case member.UserID == nil:
		if _, err := s.store.LinkMemberToUserTx(ctx, tx, p.ProjectMemberID, userID); err != nil {
			return nil, err
		}
	}
	if _, err := s.notifs.MarkActionedTx(ctx, tx, userID, notificationID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.detail(ctx, userID, member.ProjectID, 1, 20)
}

func (s *Service) RejectLinkRequest(ctx context.Context, userID, notificationID uuid.UUID) error {
	if s.notifs == nil {
		return errors.New("notifications module not wired")
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	n, err := s.notifs.GetByIDForCallerTx(ctx, tx, userID, notificationID)
	if err != nil {
		return err
	}
	if n.Type != notifications.TypeProjectInvite {
		return ErrLinkRequestNotForYou
	}
	var p notifications.ProjectInvitePayload
	if err := json.Unmarshal(n.Payload, &p); err == nil && p.ProjectMemberID != uuid.Nil {
		if _, err := tx.Exec(ctx, `
			UPDATE project_members SET status = 'left', left_at = NOW(), updated_by_user_id = $1
			WHERE id = $2 AND user_id IS NULL AND status = 'pending'`,
			userID, p.ProjectMemberID); err != nil {
			return fmt.Errorf("drop pending member: %w", err)
		}
	}
	if _, err := s.notifs.MarkDismissedTx(ctx, tx, userID, notificationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) GetMember(ctx context.Context, memberID uuid.UUID) (*ProjectMember, error) {
	return s.store.GetMember(ctx, memberID)
}

func (s *Service) IsActiveMember(ctx context.Context, projectID, userID uuid.UUID) (bool, error) {
	return s.store.IsActiveMember(ctx, projectID, userID)
}

func projectNameOrEmpty(ctx context.Context, tx pgx.Tx, projectID uuid.UUID) string {
	var name string
	_ = tx.QueryRow(ctx, `SELECT name FROM projects WHERE id = $1`, projectID).Scan(&name)
	return name
}

func userDisplayOrEmpty(ctx context.Context, tx pgx.Tx, userID uuid.UUID) string {
	var name string
	_ = tx.QueryRow(ctx, `SELECT display_name FROM users WHERE id = $1`, userID).Scan(&name)
	return name
}

// listDerived drops archived events, so "current" = every event but archived.
var derivedStatus = map[string]func(e *Event) bool{
	"current":    func(e *Event) bool { return true },
	"unsettled":  func(e *Event) bool { return e.MyReceivable > 0 || e.MyPayable > 0 },
	"receivable": func(e *Event) bool { return e.MyReceivable > 0 },
	"payable":    func(e *Event) bool { return e.MyPayable > 0 },
	"ended":      func(e *Event) bool { return e.Status == StatusCompleted || e.Status == StatusCancelled },
}

func (s *Service) listDerived(ctx context.Context, userID uuid.UUID, f ListFilter, match func(e *Event) bool) (*EventList, error) {
	page, perPage := f.Page, f.PerPage
	f.Status, f.Page, f.PerPage = "all", 1, 100000
	ids, _, err := s.store.ListIDs(ctx, userID, f)
	if err != nil {
		return nil, err
	}
	events, err := loadEvents(ctx, s.store.db, userID, ids)
	if err != nil {
		return nil, err
	}
	var hit []uuid.UUID
	for _, id := range ids {
		if e := events[id]; e.Status != StatusArchived && match(&e) {
			hit = append(hit, id)
		}
	}
	out := &EventList{Data: []Event{}, Pagination: shared.NewPage(page, perPage, len(hit))}
	if out.Summary, err = s.listSummary(ctx, userID); err != nil {
		return nil, err
	}
	lo, hi := (page-1)*perPage, page*perPage
	if lo > len(hit) {
		lo = len(hit)
	}
	if hi > len(hit) {
		hi = len(hit)
	}
	for _, id := range hit[lo:hi] {
		out.Data = append(out.Data, events[id])
	}
	return out, nil
}
