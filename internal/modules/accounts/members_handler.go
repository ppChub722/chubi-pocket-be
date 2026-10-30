package accounts

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

func (h *Handler) InviteMember(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req InviteMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}
	if _, err := h.service.InviteMember(c.Request.Context(), userID, id, req); err != nil {
		mapServiceError(c, err)
		return
	}
	h.membersResult(c, http.StatusCreated, userID, id)
}

func (h *Handler) RemoveMember(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	memberID, err := uuid.Parse(c.Param("member_id"))
	if err != nil {
		response.Validation(c, "Invalid member id", apierr.Field("member_id", apierr.FieldInvalid))
		return
	}
	if _, err := h.service.RemoveMember(c.Request.Context(), userID, id, memberID); err != nil {
		mapServiceError(c, err)
		return
	}
	h.membersResult(c, http.StatusOK, userID, id)
}

func (h *Handler) TransferOwnership(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req TransferOwnershipRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}
	if err := h.service.TransferOwnership(c.Request.Context(), userID, id, req.NewOwnerUserID); err != nil {
		mapServiceError(c, err)
		return
	}
	h.membersResult(c, http.StatusOK, userID, id)
}

func (h *Handler) UpdateReportScope(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req ReportScopeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}
	out, err := h.service.UpdateReportScope(c.Request.Context(), userID, id, req.ReportScope)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) AcceptInvite(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	notifID, err := uuid.Parse(c.Param("notification_id"))
	if err != nil {
		response.Validation(c, "Invalid notification id", apierr.Field("notification_id", apierr.FieldInvalid))
		return
	}
	m, err := h.service.AcceptInvite(c.Request.Context(), userID, notifID)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, m)
}

func (h *Handler) RejectInvite(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	notifID, err := uuid.Parse(c.Param("notification_id"))
	if err != nil {
		response.Validation(c, "Invalid notification id", apierr.Field("notification_id", apierr.FieldInvalid))
		return
	}
	out, err := h.service.RejectInvite(c.Request.Context(), userID, notifID)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) membersResult(c *gin.Context, status int, userID, accountID uuid.UUID) {
	out, err := h.service.MembersResult(c.Request.Context(), userID, accountID)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(status, out)
}
