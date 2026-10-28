package projects

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

type Handler struct {
	service *Service
}

func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

func reply(c *gin.Context, status int, out any, err error) {
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(status, out)
}

func bindJSON(c *gin.Context, v any) bool {
	if err := c.ShouldBindBodyWith(v, binding.JSON); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return false
	}
	return true
}

func pathID(c *gin.Context, name, label string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid "+label, nil)
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) Create(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	var req CreateProjectRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.Create(c.Request.Context(), userID, req)
	reply(c, http.StatusCreated, out, err)
}

func (h *Handler) List(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	f := ListFilter{
		Status: c.Query("status"), Q: c.Query("q"), Sort: c.Query("sort"),
		Page: atoiOr(c.Query("page"), 1), PerPage: atoiOr(c.Query("per_page"), 20),
	}
	switch f.Status {
	case "", StatusActive, StatusCompleted, StatusCancelled, StatusArchived, "all", "current", "unsettled", "receivable", "payable", "ended":
	default:
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid status", nil)
		return
	}
	switch f.Sort {
	case "", "updated_desc", "start_date_desc", "name_asc":
	default:
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid sort", nil)
		return
	}
	for _, v := range c.QueryArray("tag_id") {
		id, err := uuid.Parse(v)
		if err != nil {
			response.BadRequest(c, "VALIDATION_ERROR", "Invalid tag_id", nil)
			return
		}
		f.TagIDs = append(f.TagIDs, id)
	}
	out, err := h.service.List(c.Request.Context(), userID, f)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Get(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.Get(c.Request.Context(), userID, id)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Detail(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.Detail(c.Request.Context(), userID, id,
		atoiOr(c.Query("page"), 1), atoiOr(c.Query("per_page"), 20),
		strings.Contains(","+c.Query("include")+",", ",delete_preview,"))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Update(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	var req UpdateProjectRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.Update(c.Request.Context(), userID, id, req)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Delete(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.Delete(c.Request.Context(), userID, id, false) // delete_linked_txs retired (0.4.1-2)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Leave(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.Leave(c.Request.Context(), userID, id)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) TransferOwnership(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	var req struct {
		NewOwnerUserID uuid.UUID `json:"new_owner_user_id" binding:"required"`
	}
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.TransferOwnership(c.Request.Context(), userID, id, req.NewOwnerUserID)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Summary(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.Summary(c.Request.Context(), userID, id)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) ResolvePeople(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	var req ResolvePeopleRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.ResolvePeople(c.Request.Context(), userID, id, req)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Settle(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.Settle(c.Request.Context(), userID, id)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) ListMembers(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.ListMembers(c.Request.Context(), userID, id)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) AddMember(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	var req AddMemberRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.AddMember(c.Request.Context(), userID, id, req)
	reply(c, http.StatusCreated, out, err)
}

func (h *Handler) UpdateMember(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	memberID, ok := pathID(c, "member_id", "member id")
	if !ok {
		return
	}
	var req UpdateMemberRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.UpdateMember(c.Request.Context(), userID, id, memberID, req)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) RemoveMember(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	memberID, ok := pathID(c, "member_id", "member id")
	if !ok {
		return
	}
	out, err := h.service.RemoveMember(c.Request.Context(), userID, id, memberID)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) RequestLink(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	memberID, ok := pathID(c, "member_id", "member id")
	if !ok {
		return
	}
	if err := h.service.RequestLink(c.Request.Context(), userID, id, memberID); err != nil {
		mapServiceError(c, err)
		return
	}
	response.OK(c, "Invite sent", nil)
}

func (h *Handler) AcceptLinkRequest(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	notifID, ok := pathID(c, "notification_id", "notification id")
	if !ok {
		return
	}
	out, err := h.service.AcceptLinkRequest(c.Request.Context(), userID, notifID)
	if err != nil {
		reply(c, http.StatusOK, out, err)
		return
	}
	sum, err := notifications.LoadInboxSummary(c.Request.Context(), h.service.store.Pool(), userID)
	if err != nil {
		reply(c, http.StatusOK, out, err)
		return
	}
	reply(c, http.StatusOK, &InviteAcceptResult{EventDetail: out, InboxSummary: sum}, nil)
}

func (h *Handler) RejectLinkRequest(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	notifID, ok := pathID(c, "notification_id", "notification id")
	if !ok {
		return
	}
	if err := h.service.RejectLinkRequest(c.Request.Context(), userID, notifID); err != nil {
		mapServiceError(c, err)
		return
	}
	sum, err := notifications.LoadInboxSummary(c.Request.Context(), h.service.store.Pool(), userID)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Project link request rejected", "inbox_summary": sum})
}

func (h *Handler) CreatePT(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	var req CreateProjectTransactionRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.CreateProjectTransaction(c.Request.Context(), userID, id, req)
	reply(c, http.StatusCreated, out, err)
}

func (h *Handler) UpdatePT(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	ptID, ok := pathID(c, "pt_id", "project transaction id")
	if !ok {
		return
	}
	var req UpdateProjectTransactionRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.UpdateProjectTransaction(c.Request.Context(), userID, id, ptID, req)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) DeletePT(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	ptID, ok := pathID(c, "pt_id", "project transaction id")
	if !ok {
		return
	}
	out, err := h.service.DeleteProjectTransaction(c.Request.Context(), userID, id, ptID)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) ListPT(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	f := BoardFilter{
		Type: c.Query("type"), Mine: c.Query("mine") == "true", Q: c.Query("q"), Sort: c.Query("sort"),
		Page: atoiOr(c.Query("page"), 1), PerPage: atoiOr(c.Query("per_page"), 20),
	}
	switch f.Type {
	case "", PTTypeExpense, PTTypeIncome:
	default:
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid type", nil)
		return
	}
	switch f.Sort {
	case "", "date_desc", "date_asc", "amount_desc", "amount_asc":
	default:
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid sort", nil)
		return
	}
	if v := c.Query("tag_id"); v != "" {
		tid, err := uuid.Parse(v)
		if err != nil {
			response.BadRequest(c, "VALIDATION_ERROR", "Invalid tag_id", nil)
			return
		}
		f.TagID = &tid
	}
	if v := c.Query("payer_member_id"); v != "" {
		pid, err := uuid.Parse(v)
		if err != nil {
			response.BadRequest(c, "VALIDATION_ERROR", "Invalid payer_member_id", nil)
			return
		}
		f.PayerMemberID = &pid
	}
	out, err := h.service.ListPT(c.Request.Context(), userID, id, f)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) ToggleMark(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	ptID, ok := pathID(c, "pt_id", "project transaction id")
	if !ok {
		return
	}
	var req MarkRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.ToggleMark(c.Request.Context(), userID, id, ptID, req.Marked)
	reply(c, http.StatusOK, out, err)
}

// ApplyEventToTx — POST /transactions/:id/apply-event: my transaction takes its event bill as it is
// now (0.4.0-3 B3).
func (h *Handler) ApplyEventToTx(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c) // :id is the transaction here
	if !ok {
		return
	}
	out, err := h.service.ApplyEventToTx(c.Request.Context(), userID, id)
	if err != nil {
		mapQuickCreateError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) ListTags(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.ListTags(c.Request.Context(), userID, id)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) CreateTag(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	var req EventTagRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.CreateTag(c.Request.Context(), userID, id, req)
	reply(c, http.StatusCreated, out, err)
}

func (h *Handler) UpdateTag(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	tagID, ok := pathID(c, "tag_id", "tag id")
	if !ok {
		return
	}
	var req EventTagRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.UpdateTag(c.Request.Context(), userID, id, tagID, req)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) DeleteTag(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	tagID, ok := pathID(c, "tag_id", "tag id")
	if !ok {
		return
	}
	out, err := h.service.DeleteTag(c.Request.Context(), userID, id, tagID)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) ImportTags(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	var req EventTagImportRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.ImportTags(c.Request.Context(), userID, id, req)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) bindProjectID(c *gin.Context) (uuid.UUID, uuid.UUID, bool) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return uuid.Nil, uuid.Nil, false
	}
	id, ok := pathID(c, "id", "project id")
	return userID, id, ok
}

func mapServiceError(c *gin.Context, err error) {
	var (
		empty    *EmptyPersonError
		claimErr *ClaimError
	)
	switch {
	case errors.As(err, &empty):
		response.Fail(c, http.StatusConflict, "EVENT_HAS_EMPTY_PERSON", err.Error(), gin.H{"bill_ids": empty.BillIDs})
	case errors.Is(err, ErrEventCompleted), isEventCompletedPg(err):
		response.Fail(c, http.StatusConflict, "EVENT_COMPLETED", ErrEventCompleted.Error(), nil)
	case errors.Is(err, ErrUseCompleteEndpoint):
		response.BadRequest(c, "USE_COMPLETE_ENDPOINT", err.Error(), nil)
	case errors.Is(err, ErrCompleteNotActive):
		response.Fail(c, http.StatusConflict, "PROJECT_NOT_ACTIVE", err.Error(), nil)
	case errors.Is(err, ErrMemberNotRemoved):
		response.Fail(c, http.StatusConflict, "MEMBER_NOT_REMOVED", err.Error(), nil)
	case errors.As(err, &claimErr) && errors.Is(err, ErrNotInBill):
		response.BadRequest(c, "NOT_IN_BILL", err.Error(), gin.H{"bill_ids": claimErr.BillIDs})
	case errors.As(err, &claimErr):
		response.Fail(c, http.StatusConflict, "BILL_ALREADY_BOOKED", err.Error(), gin.H{"bill_ids": claimErr.BillIDs})
	case errors.Is(err, personal_debts.ErrBillAlreadyBooked):
		response.Fail(c, http.StatusConflict, "BILL_ALREADY_BOOKED", err.Error(), nil)
	case errors.Is(err, ErrSettleNotCompleted):
		response.Fail(c, http.StatusConflict, "EVENT_NOT_COMPLETED", err.Error(), nil)
	case errors.Is(err, ErrSettleBooked):
		response.Fail(c, http.StatusConflict, "SETTLE_BOOKED", err.Error(), nil)
	case errors.Is(err, ErrSettleLines):
		response.BadRequest(c, "SETTLE_LINES", err.Error(), nil)
	case errors.Is(err, ErrClaimMixedDirection):
		response.BadRequest(c, "CLAIM_MIXED_DIRECTION", err.Error(), nil)
	case errors.Is(err, ErrClaimLinkOne), errors.Is(err, ErrClaimTxNotLinkable):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	case errors.Is(err, ErrProjectNotFound):
		response.NotFound(c, "NOT_FOUND", "Project not found")
	case errors.Is(err, ErrPTNotFound):
		response.NotFound(c, "NOT_FOUND", "Project transaction not found")
	case errors.Is(err, ErrEventTagNotFound):
		response.NotFound(c, "NOT_FOUND", "Event tag not found")
	case errors.Is(err, ErrUnknownTag), errors.Is(err, transactions.ErrTagNotFound):
		response.BadRequest(c, "TAG_NOT_FOUND", "Tag not found", nil)
	case errors.Is(err, ErrTagNameTaken):
		response.Fail(c, http.StatusConflict, "TAG_NAME_TAKEN", err.Error(), nil)
	case errors.Is(err, ErrTagNameRequired), errors.Is(err, ErrBadMemberRequest):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	case errors.Is(err, ErrInvalidDateRange):
		response.BadRequest(c, "INVALID_DATE_RANGE", err.Error(), nil)

	case errors.Is(err, ErrNoShareToCopy):
		response.Fail(c, http.StatusUnprocessableEntity, "NO_SHARE_TO_COPY", err.Error(), nil)
	case errors.Is(err, ErrPaybackNotCopyable):
		response.Fail(c, http.StatusUnprocessableEntity, "PAYBACK_NOT_COPYABLE", err.Error(), nil)
	case errors.Is(err, ErrBadPayback):
		response.BadRequest(c, "BAD_PAYBACK", err.Error(), nil)
	case errors.Is(err, ErrPayerFixed):
		response.BadRequest(c, "PAYER_FIXED", err.Error(), nil)
	case errors.Is(err, ErrPTIsChild):
		response.Fail(c, http.StatusConflict, "PT_IS_CHILD", "Cannot operate on a split-child row directly", nil)
	case errors.Is(err, ErrSelfSplit):
		response.BadRequest(c, "SELF_SPLIT", "A split's member must differ from the payer", nil)
	case errors.Is(err, ErrSplitsExceedParent):
		response.BadRequest(c, "SPLITS_EXCEED_AMOUNT", "Splits add up to more than the bill", nil)
	case errors.Is(err, ErrMemberNotFound):
		response.NotFound(c, "NOT_FOUND", "Member not found")
	case errors.Is(err, ErrNotOwner):
		response.Fail(c, http.StatusForbidden, "FORBIDDEN_ROLE", "Only the event owner can do this", nil)
	case errors.Is(err, ErrProjectLocked):
		response.Fail(c, http.StatusConflict, "PROJECT_LOCKED", "The event is cancelled or archived; writes are blocked", nil)
	case errors.Is(err, ErrProjectNotActive):
		response.Fail(c, http.StatusConflict, "PROJECT_LOCKED", "The event is not active", nil)
	case errors.Is(err, ErrMemberAlreadyExists):
		response.Fail(c, http.StatusConflict, "ALREADY_MEMBER", "Already a member", nil)
	case errors.Is(err, ErrCannotRemoveOwner):
		response.BadRequest(c, "CANNOT_REMOVE_OWNER", "Cannot remove the owner", nil)
	case errors.Is(err, ErrOwnerCannotLeave):
		response.Fail(c, http.StatusConflict, "OWNER_CANNOT_LEAVE", "Owner cannot leave; transfer ownership first", nil)
	case errors.Is(err, ErrNotPending):
		response.Fail(c, http.StatusConflict, "NOT_PENDING", err.Error(), nil)
	case errors.Is(err, ErrNotMember):
		response.Fail(c, http.StatusForbidden, "NOT_MEMBER", "Caller is not a member of this event", nil)
	case errors.Is(err, ErrCannotChangeOwner):
		response.Fail(c, http.StatusConflict, "CANNOT_CHANGE_OWNER", err.Error(), nil)
	case errors.Is(err, ErrViewerReadOnly):
		response.Fail(c, http.StatusForbidden, "FORBIDDEN_ROLE", err.Error(), nil)
	case errors.Is(err, ErrMemberNotInProject):
		response.BadRequest(c, "MEMBER_NOT_IN_PROJECT", "Member does not belong to this event", nil)
	case errors.Is(err, ErrLinkRequestNotForYou):
		response.BadRequest(c, "NOT_PROJECT_INVITE", err.Error(), nil)
	case errors.Is(err, notifications.ErrNotificationNotFound):
		response.NotFound(c, "NOT_FOUND", "Notification not found")
	case errors.Is(err, notifications.ErrNotForYou):
		response.Fail(c, http.StatusForbidden, "NOT_FOR_YOU", "Notification is not addressed to caller", nil)
	case errors.Is(err, notifications.ErrStateConflict):
		response.Fail(c, http.StatusConflict, "NOTIFICATION_STATE_CONFLICT", err.Error(), nil)
	default:
		response.InternalError(c, "Project operation failed", err.Error())
	}
}

func mapQuickCreateError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrQuickTxNotFound):
		response.NotFound(c, "TX_NOT_FOUND", "Transaction not found or not owned")
	case errors.Is(err, ErrQuickTxAlreadyInProject):
		response.Fail(c, http.StatusConflict, "TX_ALREADY_IN_PROJECT", "Transaction is already in a project", nil)
	case errors.Is(err, ErrQuickTxNotBillable), errors.Is(err, ErrQuickNothingToAdd):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	case errors.Is(err, ErrQuickTxSplitPaid):
		response.Fail(c, http.StatusUnprocessableEntity, "TX_SPLIT_PAID", err.Error(), nil)
	case errors.Is(err, transactions.ErrSplitContactNotFound):
		response.BadRequest(c, "CONTACT_NOT_FOUND", "Split contact not found", nil)
	case errors.Is(err, ErrQuickTxIsRepayment), errors.Is(err, ErrQuickTxSystemRow):
		response.Fail(c, http.StatusUnprocessableEntity, "TX_NOT_BILLABLE", err.Error(), nil)
	case errors.Is(err, ErrBillNotInProject):
		response.NotFound(c, "TX_NOT_IN_PROJECT", "Transaction is not in this project")
	case errors.Is(err, transactions.ErrTxNotFound):
		response.NotFound(c, "NOT_FOUND", "Transaction not found")
	case errors.Is(err, transactions.ErrLinkModeConflict):
		response.BadRequest(c, "LINK_MODE_CONFLICT", err.Error(), nil)
	case errors.Is(err, ErrBadBillPeople), errors.Is(err, ErrPayerIncome):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	case errors.Is(err, ErrSplitsExceedParent):
		response.BadRequest(c, "SPLITS_EXCEED_AMOUNT", "Shares add up to more than the bill", nil)
	case errors.Is(err, ErrMemberNotInProject):
		response.BadRequest(c, "MEMBER_NOT_IN_PROJECT", "Member does not belong to this event", nil)
	case errors.Is(err, transactions.ErrAccountArchived):
		response.Fail(c, http.StatusConflict, "ACCOUNT_ARCHIVED", err.Error(), nil)
	case errors.Is(err, transactions.ErrAccountForbidden):
		response.NotFound(c, "ACCOUNT_NOT_FOUND", "Account not found")
	case errors.Is(err, transactions.ErrCategoryForbidden):
		response.Fail(c, http.StatusForbidden, "FORBIDDEN", "Category not accessible", nil)
	case errors.Is(err, transactions.ErrCategoryTypeMismatch),
		errors.Is(err, transactions.ErrSystemCategoryNotAllowed),
		errors.Is(err, transactions.ErrTransferFieldsOnNonTransfer),
		errors.Is(err, transactions.ErrProjectIDNotAllowed),
		errors.Is(err, transactions.ErrSplitsNotSupportedYet),
		errors.Is(err, transactions.ErrAmountInvalid):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	default:
		mapServiceError(c, err)
	}
}

func atoiOr(s string, dflt int) int {
	if s == "" {
		return dflt
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return dflt
	}
	return n
}

func (h *Handler) setStatus(c *gin.Context, status string) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	req := UpdateProjectRequest{Status: &status, present: map[string]bool{"status": true}}
	out, err := h.service.Update(c.Request.Context(), userID, id, req)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Archive(c *gin.Context) { h.setStatus(c, StatusArchived) }

func (h *Handler) Restore(c *gin.Context) { h.setStatus(c, StatusActive) }

// Complete — POST /projects/:id/complete (0.4.1-2 V2).
func (h *Handler) Complete(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.Complete(c.Request.Context(), userID, id)
	reply(c, http.StatusOK, out, err)
}

// AssignRemoved — POST /projects/:id/members/:member_id/assign (0.4.1-2 V15).
func (h *Handler) AssignRemoved(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	memberID, err := uuid.Parse(c.Param("member_id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid member_id", nil)
		return
	}
	var req AssignRemovedRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.AssignRemoved(c.Request.Context(), userID, id, memberID, req)
	reply(c, http.StatusOK, out, err)
}

// ListClaimable — GET /projects/:id/claimable[?member_id=] (0.4.1-2).
func (h *Handler) ListClaimable(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	who := ClaimablePerson{PersonName: c.Query("person_name")}
	for key, dst := range map[string]**uuid.UUID{"member_id": &who.MemberID, "contact_id": &who.ContactID} {
		if v := c.Query(key); v != "" {
			m, err := uuid.Parse(v)
			if err != nil {
				response.BadRequest(c, "VALIDATION_ERROR", "Invalid "+key, nil)
				return
			}
			*dst = &m
		}
	}
	out, err := h.service.ListClaimable(c.Request.Context(), userID, id, who)
	reply(c, http.StatusOK, out, err)
}

// Claim — POST /projects/:id/claim (0.4.1-2 V6 / V9 / V11).
func (h *Handler) Claim(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	var req ClaimBillsRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.Claim(c.Request.Context(), userID, id, req)
	reply(c, http.StatusOK, out, err)
}

// BookSettle — POST /projects/:id/settle/book (0.4.1-2 V12).
func (h *Handler) BookSettle(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	var req SettleBookRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.BookSettle(c.Request.Context(), userID, id, req)
	reply(c, http.StatusOK, out, err)
}

// isEventCompletedPg — the 101 trigger refused a bill write racing the complete call.
func isEventCompletedPg(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Message == "EVENT_COMPLETED"
}
