package accounts

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

type Handler struct {
	service *Service
}

func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

func (h *Handler) Create(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}

	var req CreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}

	a, err := h.service.Create(c.Request.Context(), userID, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, a)
}

func (h *Handler) List(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}

	status := c.DefaultQuery("status", StatusActive)
	if status != StatusActive && status != StatusArchived && status != "all" {
		response.Validation(c, "Invalid status", apierr.Field("status", apierr.FieldInvalid))
		return
	}
	accType := c.Query("type")

	out, err := h.service.ListPage(c.Request.Context(), userID, status, accType, c.GetHeader("X-Timezone"))
	if err != nil {
		response.InternalError(c, "Failed to list accounts", err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) Get(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	var include []string
	for _, v := range c.QueryArray("include") {
		include = append(include, strings.Split(v, ",")...)
	}
	a, err := h.service.GetDetail(c.Request.Context(), userID, id, include, c.Query("from"), c.Query("to"), c.GetHeader("X-Timezone"))
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}

// Summary — GET /accounts/:id/summary?from=&to=: income and expense of the wallet over a period.
func (h *Handler) Summary(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	out, err := h.service.Summary(c.Request.Context(), userID, id, c.Query("from"), c.Query("to"))
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) Update(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	var peek map[string]any
	if err := c.ShouldBindBodyWith(&peek, binding.JSON); err != nil {
		response.BindError(c, err)
		return
	}
	if _, hasBalance := peek["balance"]; hasBalance {
		response.BadRequest(c, apierr.BalanceNotEditable, ErrBalanceNotEditable.Error(), nil)
		return
	}
	var req UpdateRequest
	if err := c.ShouldBindBodyWith(&req, binding.JSON); err != nil {
		response.BindError(c, err)
		return
	}

	a, err := h.service.Update(c.Request.Context(), userID, id, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}

func (h *Handler) AddIdentifier(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req Identifier
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}
	a, err := h.service.AddIdentifier(c.Request.Context(), userID, id, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}

func (h *Handler) AdjustBalance(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	var req AdjustBalanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}
	if req.Date == nil {
		t := time.Now().Format("2006-01-02")
		req.Date = &t
	}

	res, err := h.service.AdjustBalance(c.Request.Context(), userID, id, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

func (h *Handler) Restore(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	a, err := h.service.Restore(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}

func parseIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.Validation(c, "Invalid account id", apierr.Field("id", apierr.FieldInvalid))
		return uuid.Nil, false
	}
	return id, true
}

func mapServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrAccountNotFound):
		response.NotFound(c, apierr.NotFound, "Account not found")
	case errors.Is(err, ErrMemberNotFound):
		response.NotFound(c, apierr.NotFound, "Member not found")
	case errors.Is(err, ErrUserNotFound):
		response.NotFound(c, apierr.UserNotFound, "No user matches that email")
	case errors.Is(err, ErrInviteTarget):
		response.ValidationErr(c, err)
	case errors.Is(err, ErrContactNotFound):
		response.NotFound(c, apierr.ContactNotFound, "Contact not found")
	case errors.Is(err, ErrContactArchived):
		response.Fail(c, http.StatusConflict, apierr.ContactArchived, err.Error(), nil)
	case errors.Is(err, ErrContactNotLinked):
		response.Fail(c, http.StatusUnprocessableEntity, apierr.ContactNotLinked, err.Error(), nil)
	case errors.Is(err, ErrAlreadyMember):
		response.Fail(c, http.StatusConflict, apierr.UserAlreadyMember, "User is already a member of this account", nil)
	case errors.Is(err, ErrNotMember):
		response.Fail(c, http.StatusForbidden, apierr.NotMember, "Caller is not an active member of this account", nil)
	case errors.Is(err, ErrNotOwner):
		response.Fail(c, http.StatusForbidden, apierr.NotOwner, "Only the account owner can perform this action", nil)
	case errors.Is(err, ErrOwnerMustTransfer):
		response.BadRequest(c, apierr.OwnerMustTransfer, err.Error(), nil)
	case errors.Is(err, ErrAccountHasMembers):
		response.Fail(c, http.StatusConflict, apierr.AccountHasMembers, err.Error(), nil)
	case errors.Is(err, ErrScopeNotAllowed):
		response.Fail(c, http.StatusForbidden, apierr.ScopeNotAllowed, err.Error(), nil)
	case errors.Is(err, ErrInvalidInclude), errors.Is(err, ErrInvalidDate):
		response.ValidationErr(c, err)
	case errors.Is(err, ErrNotInvite), errors.Is(err, notifications.ErrNotificationNotFound), errors.Is(err, notifications.ErrNotForYou):
		response.NotFound(c, apierr.NotFound, "Invite not found")
	case errors.Is(err, ErrInvalidIdentifier), errors.Is(err, ErrTooManyIdentifiers):
		response.BadRequest(c, apierr.InvalidIdentifier, err.Error(), nil)
	case errors.Is(err, ErrCreditFieldMismatch):
		response.ValidationErr(c, err)
	case errors.Is(err, ErrTypeChangeNeedsLimit):
		response.ValidationErr(c, err)
	case errors.Is(err, ErrBalanceNotEditable):
		response.BadRequest(c, apierr.BalanceNotEditable, err.Error(), nil)
	case errors.Is(err, ErrAdjustNoOp):
		response.BadRequest(c, apierr.NoOp, err.Error(), nil)
	case errors.Is(err, ErrBalanceNotTracked):
		response.Fail(c, http.StatusConflict, apierr.BalanceNotTracked, err.Error(), nil)
	case errors.Is(err, ErrCurrentBalanceAlone):
		response.ValidationErr(c, err)
	default:
		response.InternalError(c, "Account operation failed", err)
	}
}

func (h *Handler) Delete(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	out, err := h.service.Delete(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) Archive(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	a, err := h.service.Archive(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}
