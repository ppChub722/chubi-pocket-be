package projects

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

// Two contracts over one core: /split-bills (papers with no event) and /projects/:id/bills (an
// event's papers). Each route checks its kind; a paper of the other kind answers 404.

var splitOnly = &uuid.Nil

func (h *Handler) user(c *gin.Context) (uuid.UUID, bool) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
	}
	return userID, ok
}

// paperRoute — the caller, the paper id, and the event the route names (splitOnly for /split-bills).
func (h *Handler) paperRoute(c *gin.Context, event bool) (uuid.UUID, uuid.UUID, *uuid.UUID, bool) {
	userID, ok := h.user(c)
	if !ok {
		return uuid.Nil, uuid.Nil, nil, false
	}
	if !event {
		id, ok := pathID(c, "id", "bill id")
		return userID, id, splitOnly, ok
	}
	pid, ok := pathID(c, "id", "event id")
	if !ok {
		return uuid.Nil, uuid.Nil, nil, false
	}
	bid, ok := pathID(c, "bid", "bill id")
	return userID, bid, &pid, ok
}

func paperFilter(c *gin.Context) (PaperFilter, bool) {
	f := PaperFilter{Type: c.Query("type"), Mine: c.Query("mine") == "true", Q: c.Query("q"), Sort: c.Query("sort"),
		Status: c.Query("status"), Page: atoiOr(c.Query("page"), 1), PerPage: atoiOr(c.Query("per_page"), 20)}
	switch f.Type {
	case "", "expense", "income", "payback":
	default:
		response.Validation(c, "Invalid type", apierr.Field("type", apierr.FieldInvalid))
		return f, false
	}
	switch f.Sort {
	case "", "date_desc", "date_asc", "amount_desc", "amount_asc":
	default:
		response.Validation(c, "Invalid sort", apierr.Field("sort", apierr.FieldInvalid))
		return f, false
	}
	if v := c.Query("tag_id"); v != "" {
		tid, err := uuid.Parse(v)
		if err != nil {
			response.Validation(c, "Invalid tag_id", apierr.Field("tag_id", apierr.FieldInvalid))
			return f, false
		}
		f.TagID = &tid
	}
	return f, true
}

// --- split bills ---

func (h *Handler) ListSplitBills(c *gin.Context) {
	userID, ok := h.user(c)
	if !ok {
		return
	}
	f, ok := paperFilter(c)
	if !ok {
		return
	}
	out, err := h.service.paperList(c.Request.Context(), userID, nil, f)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) CreateSplitBill(c *gin.Context) {
	userID, ok := h.user(c)
	if !ok {
		return
	}
	var req CreatePaperRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.createPaper(c.Request.Context(), userID, nil, req)
	reply(c, http.StatusCreated, out, err)
}

func (h *Handler) JoinSplitBill(c *gin.Context)    { h.answer(c, true) }
func (h *Handler) DeclineSplitBill(c *gin.Context) { h.answer(c, false) }

func (h *Handler) answer(c *gin.Context, join bool) {
	userID, id, _, ok := h.paperRoute(c, false)
	if !ok {
		return
	}
	out, err := h.service.answerInvite(c.Request.Context(), userID, id, join)
	reply(c, http.StatusOK, out, err)
}

// MoveSplitBill — POST /split-bills/:id/move-to-event {project_id}.
func (h *Handler) MoveSplitBill(c *gin.Context) {
	userID, id, kind, ok := h.paperRoute(c, false)
	if !ok {
		return
	}
	var req MovePaperRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.ProjectID == nil {
		response.Validation(c, "project_id is required", apierr.Field("project_id", apierr.FieldRequired))
		return
	}
	out, err := h.service.movePaper(c.Request.Context(), userID, id, kind, req.ProjectID)
	reply(c, http.StatusOK, out, err)
}

// --- event bills ---

func (h *Handler) ListEventBills(c *gin.Context) {
	userID, pid, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	f, ok := paperFilter(c)
	if !ok {
		return
	}
	if _, err := h.service.store.GetByIDForCaller(c.Request.Context(), userID, pid); err != nil {
		mapServiceError(c, err)
		return
	}
	out, err := h.service.paperList(c.Request.Context(), userID, &pid, f)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) CreateEventBill(c *gin.Context) {
	userID, pid, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	var req CreatePaperRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.createPaper(c.Request.Context(), userID, &pid, req)
	reply(c, http.StatusCreated, out, err)
}

// MoveEventBill — POST /projects/:id/bills/:bid/move {project_id | null}.
func (h *Handler) MoveEventBill(c *gin.Context) {
	userID, id, kind, ok := h.paperRoute(c, true)
	if !ok {
		return
	}
	var req MovePaperRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.movePaper(c.Request.Context(), userID, id, kind, req.ProjectID)
	reply(c, http.StatusOK, out, err)
}

// --- both kinds ---

func (h *Handler) GetPaper(event bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, id, kind, ok := h.paperRoute(c, event)
		if !ok {
			return
		}
		out, err := h.service.getPaper(c.Request.Context(), userID, id, kind)
		reply(c, http.StatusOK, out, err)
	}
}

func (h *Handler) UpdatePaper(event bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, id, kind, ok := h.paperRoute(c, event)
		if !ok {
			return
		}
		var req UpdatePaperRequest
		if !bindJSON(c, &req) {
			return
		}
		out, err := h.service.updatePaper(c.Request.Context(), userID, id, kind, req)
		reply(c, http.StatusOK, out, err)
	}
}

func (h *Handler) ClaimPaper(event bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, id, kind, ok := h.paperRoute(c, event)
		if !ok {
			return
		}
		var req ClaimInput
		if c.Request.ContentLength > 0 && !bindJSON(c, &req) {
			return
		}
		out, err := h.service.claim(c.Request.Context(), userID, id, kind, req)
		reply(c, http.StatusOK, out, err)
	}
}

func (h *Handler) LeavePaper(event bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, id, kind, ok := h.paperRoute(c, event)
		if !ok {
			return
		}
		var req LeaveRequest
		if c.Request.ContentLength > 0 && !bindJSON(c, &req) {
			return
		}
		out, err := h.service.leave(c.Request.Context(), userID, id, kind, req)
		reply(c, http.StatusOK, out, err)
	}
}

func (h *Handler) RemovePaperPerson(event bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, id, kind, ok := h.paperRoute(c, event)
		if !ok {
			return
		}
		pid, ok := pathID(c, "pid", "person id")
		if !ok {
			return
		}
		out, err := h.service.removePerson(c.Request.Context(), userID, id, pid, kind)
		reply(c, http.StatusOK, out, err)
	}
}

func (h *Handler) MarkPaperPerson(event bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, id, kind, ok := h.paperRoute(c, event)
		if !ok {
			return
		}
		pid, ok := pathID(c, "pid", "person id")
		if !ok {
			return
		}
		var req MarkPaidBackRequest
		if !bindJSON(c, &req) {
			return
		}
		out, err := h.service.markPaidBack(c.Request.Context(), userID, id, pid, kind, req.PaidBack)
		reply(c, http.StatusOK, out, err)
	}
}

// --- my books ---

// SyncTransaction — POST /transactions/:id/paper-sync ("อัปเดต").
func (h *Handler) SyncTransaction(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.SyncMyTransaction(c.Request.Context(), userID, id)
	reply(c, http.StatusOK, out, err)
}

// UnlinkTransaction — POST /transactions/:id/unlink-paper.
func (h *Handler) UnlinkTransaction(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.UnlinkMyTransaction(c.Request.Context(), userID, id)
	reply(c, http.StatusOK, out, err)
}

// SyncDebt — POST /personal-debts/:id/paper-sync.
func (h *Handler) SyncDebt(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.SyncMyDebt(c.Request.Context(), userID, id)
	reply(c, http.StatusOK, out, err)
}

// UnlinkDebt — POST /personal-debts/:id/unlink-paper.
func (h *Handler) UnlinkDebt(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	if err := h.service.UnlinkMyDebt(c.Request.Context(), userID, id); err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id})
}

// --- event members ---

// MergeMember — POST /projects/:id/members/:member_id/merge {into}.
func (h *Handler) MergeMember(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	memberID, ok := pathID(c, "member_id", "member id")
	if !ok {
		return
	}
	var req MergeMemberRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.MergeMember(c.Request.Context(), userID, id, memberID, req)
	reply(c, http.StatusOK, out, err)
}

// SetMyDefaults — PUT /projects/:id/my-defaults {category_id, account_id}.
func (h *Handler) SetMyDefaults(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	var req MyDefaults
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.SetMyDefaults(c.Request.Context(), userID, id, req)
	reply(c, http.StatusOK, out, err)
}

// getPaper — one paper as I see it.
func (s *Service) getPaper(ctx context.Context, userID, paperID uuid.UUID, kind *uuid.UUID) (*PaperView, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := s.accessTx(ctx, tx, userID, paperID, kind); err != nil {
		var person *uuid.UUID
		// an invite still waiting: the invitee may read it to decide
		if errors.Is(err, ErrPaperNotFound) {
			_ = tx.QueryRow(ctx, `SELECT id FROM split_transaction_people WHERE split_transaction_id = $1
				AND user_id = $2 AND status = 'invited'`, paperID, userID).Scan(&person)
		}
		if person == nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	views, err := s.paperViews(ctx, s.store.db, userID, []uuid.UUID{paperID})
	if err != nil {
		return nil, err
	}
	if len(views) == 0 {
		return nil, ErrPaperNotFound
	}
	return &views[0], nil
}

func mapServiceError(c *gin.Context, err error) {
	var empty *EmptyPersonError
	switch {
	case errors.As(err, &empty):
		response.Fail(c, http.StatusConflict, apierr.EventHasEmptyPerson, err.Error(), gin.H{"bill_ids": empty.BillIDs})
	case errors.Is(err, ErrEventCompleted), isEventCompletedPg(err):
		response.Fail(c, http.StatusConflict, apierr.EventCompleted, ErrEventCompleted.Error(), nil)
	case errors.Is(err, ErrUseCompleteEndpoint):
		response.BadRequest(c, apierr.UseCompleteEndpoint, err.Error(), nil)
	case errors.Is(err, ErrCompleteNotActive):
		response.Fail(c, http.StatusConflict, apierr.ProjectNotActive, err.Error(), nil)
	case errors.Is(err, ErrPaperNotFound):
		response.NotFound(c, apierr.BillNotFound, err.Error())
	case errors.Is(err, ErrPaperChanged):
		response.Fail(c, http.StatusConflict, apierr.BillChanged, err.Error(), nil)
	case errors.Is(err, ErrNotPaperOwner):
		response.Fail(c, http.StatusForbidden, apierr.NotBillOwner, err.Error(), nil)
	case errors.Is(err, ErrNotOnPaper):
		response.BadRequest(c, apierr.NotInBill, err.Error(), nil)
	case errors.Is(err, ErrNothingToClaim):
		response.Fail(c, http.StatusUnprocessableEntity, apierr.NothingToClaim, err.Error(), nil)
	case errors.Is(err, ErrAlreadyClaimed):
		response.Fail(c, http.StatusConflict, apierr.AlreadyClaimed, err.Error(), nil)
	case errors.Is(err, ErrMergeTwoUsers):
		response.Fail(c, http.StatusConflict, apierr.MergeTwoUsers, err.Error(), nil)
	case errors.Is(err, ErrMoveNotAMember):
		response.Fail(c, http.StatusForbidden, apierr.NotMember, err.Error(), nil)
	case errors.Is(err, ErrSettleNothing):
		response.Fail(c, http.StatusUnprocessableEntity, apierr.SettleNothing, err.Error(), nil)
	case errors.Is(err, ErrSettleSimplifiedOff):
		response.Fail(c, http.StatusConflict, apierr.EventNotCompleted, err.Error(), nil)
	case errors.Is(err, ErrSettleBookedOnce), errors.Is(err, ErrSettleSimplifiedSet):
		response.Fail(c, http.StatusConflict, apierr.SettleBooked, err.Error(), nil)
	case errors.Is(err, transactions.ErrNotPaperBook):
		response.BadRequest(c, apierr.NotPaperBook, err.Error(), nil)
	case errors.Is(err, personal_debts.ErrBillAlreadyBooked):
		response.Fail(c, http.StatusConflict, apierr.BillAlreadyBooked, err.Error(), nil)
	case errors.Is(err, personal_debts.ErrEntryNotFound):
		response.NotFound(c, apierr.EntryNotFound, err.Error())
	case errors.Is(err, transactions.ErrTxNotFound):
		response.NotFound(c, apierr.NotFound, "Transaction not found")
	case errors.Is(err, transactions.ErrAccountArchived):
		response.Fail(c, http.StatusConflict, apierr.AccountArchived, err.Error(), nil)
	case errors.Is(err, transactions.ErrAccountForbidden):
		response.NotFound(c, apierr.AccountNotFound, "Account not found")
	case errors.Is(err, transactions.ErrCategoryForbidden):
		response.Fail(c, http.StatusForbidden, apierr.Forbidden, "Category not accessible", nil)
	case errors.Is(err, transactions.ErrCategoryTypeMismatch):
		response.BadRequest(c, apierr.CategoryTypeMismatch, err.Error(), nil)
	case errors.Is(err, transactions.ErrSystemCategoryNotAllowed):
		response.BadRequest(c, apierr.SystemCategoryNotAllowed, err.Error(), nil)
	case errors.Is(err, ErrContactNotFound):
		response.BadRequest(c, apierr.ContactNotFound, "Contact not found", nil)
	case errors.Is(err, ErrProjectNotFound):
		response.NotFound(c, apierr.NotFound, "Event not found")
	case errors.Is(err, ErrEventTagNotFound):
		response.NotFound(c, apierr.NotFound, "Event tag not found")
	case errors.Is(err, ErrUnknownTag), errors.Is(err, transactions.ErrTagNotFound):
		response.BadRequest(c, apierr.TagNotFound, "Tag not found", nil)
	case errors.Is(err, ErrTagNameTaken):
		response.Fail(c, http.StatusConflict, apierr.TagNameTaken, err.Error(), nil)
	case errors.Is(err, ErrInvalidDateRange):
		response.BadRequest(c, apierr.InvalidDateRange, err.Error(), nil)
	case errors.Is(err, ErrMemberNotFound):
		response.NotFound(c, apierr.NotFound, "Member not found")
	case errors.Is(err, ErrNotOwner):
		response.Fail(c, http.StatusForbidden, apierr.ForbiddenRole, "Only the event owner can do this", nil)
	case errors.Is(err, ErrProjectLocked):
		response.Fail(c, http.StatusConflict, apierr.ProjectLocked, "The event is cancelled or archived; writes are blocked", nil)
	case errors.Is(err, ErrProjectNotActive):
		response.Fail(c, http.StatusConflict, apierr.ProjectLocked, "The event is not active", nil)
	case errors.Is(err, ErrMemberAlreadyExists):
		response.Fail(c, http.StatusConflict, apierr.AlreadyMember, "Already a member", nil)
	case errors.Is(err, ErrCannotRemoveOwner):
		response.BadRequest(c, apierr.CannotRemoveOwner, "Cannot remove the owner", nil)
	case errors.Is(err, ErrOwnerCannotLeave):
		response.Fail(c, http.StatusConflict, apierr.OwnerCannotLeave, "Owner cannot leave; transfer ownership first", nil)
	case errors.Is(err, ErrNotMember):
		response.Fail(c, http.StatusForbidden, apierr.NotMember, "Caller is not a member of this event", nil)
	case errors.Is(err, ErrCannotChangeOwner):
		response.Fail(c, http.StatusConflict, apierr.CannotChangeOwner, err.Error(), nil)
	case errors.Is(err, ErrViewerReadOnly):
		response.Fail(c, http.StatusForbidden, apierr.ForbiddenRole, err.Error(), nil)
	case errors.Is(err, ErrMemberNotInProject):
		response.BadRequest(c, apierr.MemberNotInProject, "Member does not belong to this event", nil)
	case errors.Is(err, ErrLinkRequestNotForYou):
		response.BadRequest(c, apierr.NotProjectInvite, err.Error(), nil)
	case errors.Is(err, notifications.ErrNotificationNotFound):
		response.NotFound(c, apierr.NotFound, "Notification not found")
	case errors.Is(err, notifications.ErrNotForYou):
		response.Fail(c, http.StatusForbidden, apierr.NotForYou, "Notification is not addressed to caller", nil)
	case errors.Is(err, notifications.ErrStateConflict):
		response.Fail(c, http.StatusConflict, apierr.NotificationStateConflict, err.Error(), nil)
	case isValidation(err):
		response.ValidationErr(c, err)
	default:
		response.InternalError(c, "Event operation failed", err)
	}
}

func isValidation(err error) bool { return apierr.FieldsOf(err) != nil }
