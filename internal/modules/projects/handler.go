package projects

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

type Handler struct {
	service *Service
}

func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

// --- Project CRUD ---

// POST /v1/projects
func (h *Handler) Create(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	var req CreateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out, err := h.service.Create(c.Request.Context(), userID, req)
	if err != nil {
		mapServiceError(c, err)
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
		Status:  c.Query("status"),
		Page:    atoiOr(c.Query("page"), 1),
		PerPage: atoiOr(c.Query("per_page"), 20),
	}
	if v := c.Query("type"); v != "" {
		f.Type = &v
	}
	out, err := h.service.List(c.Request.Context(), userID, f)
	if err != nil {
		response.InternalError(c, "Failed to list projects", err.Error())
		return
	}
	c.JSON(http.StatusOK, out)
}

// GET /v1/projects/:id
func (h *Handler) Get(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.Get(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// PUT /v1/projects/:id
func (h *Handler) Update(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	var req UpdateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out, err := h.service.Update(c.Request.Context(), userID, id, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// DELETE /v1/projects/:id
func (h *Handler) Delete(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	if err := h.service.Delete(c.Request.Context(), userID, id); err != nil {
		mapServiceError(c, err)
		return
	}
	response.OK(c, "Project deleted", nil)
}

// POST /v1/projects/:id/leave
func (h *Handler) Leave(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	if err := h.service.Leave(c.Request.Context(), userID, id); err != nil {
		mapServiceError(c, err)
		return
	}
	response.OK(c, "Left project", nil)
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
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	if err := h.service.TransferOwnership(c.Request.Context(), userID, id, req.NewOwnerUserID); err != nil {
		mapServiceError(c, err)
		return
	}
	response.OK(c, "Ownership transferred", nil)
}

// --- Members ---

// GET /v1/projects/:id/members
func (h *Handler) ListMembers(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.ListMembers(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, ListMembersResponse{Data: out})
}

// POST /v1/projects/:id/members
func (h *Handler) AddMember(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	var req AddMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out, err := h.service.AddMember(c.Request.Context(), userID, id, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

// PUT /v1/projects/:id/members/:member_id
func (h *Handler) UpdateMember(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	memberID, err := uuid.Parse(c.Param("member_id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid member id", nil)
		return
	}
	var req UpdateMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out, err := h.service.UpdateMember(c.Request.Context(), userID, id, memberID, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// DELETE /v1/projects/:id/members/:member_id
func (h *Handler) RemoveMember(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	memberID, err := uuid.Parse(c.Param("member_id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid member id", nil)
		return
	}
	if err := h.service.RemoveMember(c.Request.Context(), userID, id, memberID); err != nil {
		mapServiceError(c, err)
		return
	}
	response.OK(c, "Member removed", nil)
}

// POST /v1/projects/:id/members/:member_id/request-link
func (h *Handler) RequestLink(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	memberID, err := uuid.Parse(c.Param("member_id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid member id", nil)
		return
	}
	if err := h.service.RequestLink(c.Request.Context(), userID, id, memberID); err != nil {
		mapServiceError(c, err)
		return
	}
	response.OK(c, "Link request sent", nil)
}

// POST /v1/projects/link-requests/:notification_id/accept
func (h *Handler) AcceptLinkRequest(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	notifID, err := uuid.Parse(c.Param("notification_id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid notification id", nil)
		return
	}
	out, err := h.service.AcceptLinkRequest(c.Request.Context(), userID, notifID)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/projects/link-requests/:notification_id/reject
func (h *Handler) RejectLinkRequest(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	notifID, err := uuid.Parse(c.Param("notification_id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid notification id", nil)
		return
	}
	if err := h.service.RejectLinkRequest(c.Request.Context(), userID, notifID); err != nil {
		mapServiceError(c, err)
		return
	}
	response.OK(c, "Project link request rejected", nil)
}

// --- Project transactions ---

// POST /v1/projects/:id/project-transactions
func (h *Handler) CreatePT(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	var req CreateProjectTransactionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out, err := h.service.CreateProjectTransaction(c.Request.Context(), userID, id, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

// PUT /v1/projects/:id/project-transactions/:pt_id
func (h *Handler) UpdatePT(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	ptID, err := uuid.Parse(c.Param("pt_id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid project transaction id", nil)
		return
	}
	var req UpdateProjectTransactionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out, err := h.service.UpdateProjectTransaction(c.Request.Context(), userID, id, ptID, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// DELETE /v1/projects/:id/project-transactions/:pt_id
func (h *Handler) DeletePT(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	ptID, err := uuid.Parse(c.Param("pt_id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid project transaction id", nil)
		return
	}
	if err := h.service.DeleteProjectTransaction(c.Request.Context(), userID, id, ptID); err != nil {
		mapServiceError(c, err)
		return
	}
	response.OK(c, "Project transaction deleted", nil)
}

// GET /v1/projects/:id/transactions  (project ledger view)
func (h *Handler) ListPT(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.ListPT(c.Request.Context(), userID, id,
		atoiOr(c.Query("page"), 1), atoiOr(c.Query("per_page"), 20))
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// PUT /v1/projects/:id/project-transactions/:pt_id/mark
func (h *Handler) ToggleMark(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	ptID, err := uuid.Parse(c.Param("pt_id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid project transaction id", nil)
		return
	}
	var req MarkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out, err := h.service.ToggleMark(c.Request.Context(), userID, id, ptID, req.Marked)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// GET /v1/projects/:id/summary
func (h *Handler) Summary(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	out, err := h.service.Summary(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// --- Helpers ---

func (h *Handler) bindProjectID(c *gin.Context) (uuid.UUID, uuid.UUID, bool) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return uuid.Nil, uuid.Nil, false
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid project id", nil)
		return uuid.Nil, uuid.Nil, false
	}
	return userID, id, true
}

func mapServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrProjectNotFound):
		response.NotFound(c, "NOT_FOUND", "Project not found")
	case errors.Is(err, ErrPTNotFound):
		response.NotFound(c, "NOT_FOUND", "Project transaction not found")
	case errors.Is(err, ErrPTIsChild):
		response.BadRequest(c, "PT_IS_CHILD", "Cannot operate on a split-child row directly", nil)
	case errors.Is(err, ErrSelfSplit):
		response.BadRequest(c, "SELF_SPLIT", "A split's member must differ from the parent's actor", nil)
	case errors.Is(err, ErrSplitsExceedParent):
		response.BadRequest(c, "SPLITS_EXCEED_PARENT", "Sum of splits exceeds parent amount", nil)
	case errors.Is(err, ErrMemberNotFound):
		response.NotFound(c, "NOT_FOUND", "Member not found")
	case errors.Is(err, ErrNotOwner):
		response.Fail(c, http.StatusForbidden, "NOT_OWNER", "Only the project owner can perform this action", nil)
	case errors.Is(err, ErrProjectLocked):
		response.BadRequest(c, "PROJECT_LOCKED", "Project is cancelled or archived; writes are blocked", nil)
	case errors.Is(err, ErrProjectNotActive):
		response.BadRequest(c, "PROJECT_NOT_ACTIVE", "Project is not active", nil)
	case errors.Is(err, ErrProjectHasTxs):
		response.Fail(c, http.StatusConflict, "PROJECT_HAS_TRANSACTIONS", "Project has transactions; cannot delete", nil)
	case errors.Is(err, ErrMemberAlreadyExists):
		response.Fail(c, http.StatusConflict, "USER_ALREADY_MEMBER", "User is already a member", nil)
	case errors.Is(err, ErrCannotRemoveOwner):
		response.BadRequest(c, "CANNOT_REMOVE_OWNER", "Cannot remove the owner", nil)
	case errors.Is(err, ErrOwnerCannotLeave):
		response.BadRequest(c, "OWNER_CANNOT_LEAVE", "Owner cannot leave; transfer ownership first", nil)
	case errors.Is(err, ErrNotMember):
		response.Fail(c, http.StatusForbidden, "NOT_MEMBER", "Caller is not a member of this project", nil)
	case errors.Is(err, ErrMemberNotInProject):
		response.BadRequest(c, "MEMBER_NOT_IN_PROJECT", "Member does not belong to this project", nil)
	default:
		response.InternalError(c, "Project operation failed", err.Error())
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
