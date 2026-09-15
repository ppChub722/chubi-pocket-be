package accounts

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

// GET /v1/accounts/:id/members — full membership history (active +
// pending + left) for the members screen. Active members only.
func (h *Handler) ListMembers(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
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

// POST /v1/accounts/:id/members — invite by email (notification pattern).
func (h *Handler) InviteMember(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req InviteMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	m, err := h.service.InviteMember(c.Request.Context(), userID, id, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, m)
}

// DELETE /v1/accounts/:id/members/:member_id — leave / remove (sets left_at).
func (h *Handler) RemoveMember(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	memberID, err := uuid.Parse(c.Param("member_id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid member id", nil)
		return
	}
	m, err := h.service.RemoveMember(c.Request.Context(), userID, id, memberID)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, m)
}

// POST /v1/accounts/:id/transfer-ownership
func (h *Handler) TransferOwnership(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req TransferOwnershipRequest
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

// PUT /v1/accounts/:id/report-scope — caller's own membership row.
func (h *Handler) UpdateReportScope(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req ReportScopeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out, err := h.service.UpdateReportScope(c.Request.Context(), userID, id, req.ReportScope)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/accounts/invites/:notification_id/accept
func (h *Handler) AcceptInvite(c *gin.Context) {
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
	m, err := h.service.AcceptInvite(c.Request.Context(), userID, notifID)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, m)
}

// POST /v1/accounts/invites/:notification_id/reject
func (h *Handler) RejectInvite(c *gin.Context) {
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
	if err := h.service.RejectInvite(c.Request.Context(), userID, notifID); err != nil {
		mapServiceError(c, err)
		return
	}
	response.OK(c, "Wallet invite rejected", nil)
}
