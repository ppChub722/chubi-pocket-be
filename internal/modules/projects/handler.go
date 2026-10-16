package projects

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

type Handler struct {
	service *Service
}

func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

// reply writes out (status) or maps err.
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

// --- Events ---

// POST /v1/projects
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

// POST /v1/projects/quick — quick create from bills (spec §10/4.24).
func (h *Handler) QuickCreate(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	var req QuickCreateRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.QuickCreate(c.Request.Context(), userID, req)
	if err != nil {
		mapQuickCreateError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

// GET /v1/projects
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
	case "", StatusActive, StatusCompleted, StatusCancelled, StatusArchived, "all":
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

// GET /v1/projects/:id
func (h *Handler) Get(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.Get(c.Request.Context(), userID, id)
	reply(c, http.StatusOK, out, err)
}

// GET /v1/projects/:id/detail — the detail page in one call.
func (h *Handler) Detail(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.Detail(c.Request.Context(), userID, id,
		atoiOr(c.Query("page"), 1), atoiOr(c.Query("per_page"), 20))
	reply(c, http.StatusOK, out, err)
}

// PUT /v1/projects/:id
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

// DELETE /v1/projects/:id
func (h *Handler) Delete(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.Delete(c.Request.Context(), userID, id, c.Query("delete_linked_txs") == "true")
	reply(c, http.StatusOK, out, err)
}

// POST /v1/projects/:id/leave
func (h *Handler) Leave(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.Leave(c.Request.Context(), userID, id)
	reply(c, http.StatusOK, out, err)
}

// POST /v1/projects/:id/transfer-ownership
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

// GET /v1/projects/:id/summary
func (h *Handler) Summary(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.Summary(c.Request.Context(), userID, id)
	reply(c, http.StatusOK, out, err)
}

// --- Members ---

// GET /v1/projects/:id/members
func (h *Handler) ListMembers(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.ListMembers(c.Request.Context(), userID, id)
	reply(c, http.StatusOK, out, err)
}

// POST /v1/projects/:id/members
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

// PUT /v1/projects/:id/members/:member_id
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

// DELETE /v1/projects/:id/members/:member_id
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

// POST /v1/projects/:id/members/:member_id/request-link
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

// POST /v1/projects/link-requests/:notification_id/accept
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
	reply(c, http.StatusOK, out, err)
}

// POST /v1/projects/link-requests/:notification_id/reject
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
	response.OK(c, "Project link request rejected", nil)
}

// --- Bills (board rows) ---

// POST /v1/projects/:id/project-transactions
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

// PUT /v1/projects/:id/project-transactions/:pt_id
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

// DELETE /v1/projects/:id/project-transactions/:pt_id
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

// GET /v1/projects/:id/transactions — the board.
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
	out, err := h.service.ListPT(c.Request.Context(), userID, id, f)
	reply(c, http.StatusOK, out, err)
}

// PUT /v1/projects/:id/project-transactions/:pt_id/mark
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

// POST /v1/projects/:id/project-transactions/:pt_id/copy — my share of a
// bill into my book (idempotent).
func (h *Handler) CopyToPersonal(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	ptID, ok := pathID(c, "pt_id", "project transaction id")
	if !ok {
		return
	}
	out, err := h.service.CopyToPersonal(c.Request.Context(), userID, id, ptID)
	reply(c, http.StatusCreated, out, err)
}

// POST /v1/projects/:id/bills — a new bill and / or my transactions pulled in.
func (h *Handler) AddBills(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	var req AddBillsRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.AddBills(c.Request.Context(), userID, id, req)
	if err != nil {
		mapQuickCreateError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// DELETE /v1/projects/:id/bills/:transaction_id — my transaction leaves.
func (h *Handler) RemoveBill(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	txID, ok := pathID(c, "transaction_id", "transaction id")
	if !ok {
		return
	}
	out, err := h.service.RemoveBill(c.Request.Context(), userID, id, txID)
	if err != nil {
		mapQuickCreateError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// --- Event tags ---

// GET /v1/projects/:id/tags
func (h *Handler) ListTags(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.ListTags(c.Request.Context(), userID, id)
	reply(c, http.StatusOK, out, err)
}

// POST /v1/projects/:id/tags
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

// PUT /v1/projects/:id/tags/:tag_id
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

// DELETE /v1/projects/:id/tags/:tag_id
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

// POST /v1/projects/:id/tags/import
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

// --- Helpers ---

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
	switch {
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

// mapQuickCreateError maps the bill-pull errors plus the transactions-module
// errors that can bubble out of the embedded personal-transaction create,
// then falls back to the shared project error mapping.
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
