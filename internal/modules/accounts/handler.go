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
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

type Handler struct {
	service *Service
}

func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

// POST /v1/accounts
func (h *Handler) Create(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	var req CreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	a, err := h.service.Create(c.Request.Context(), userID, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, a)
}

// GET /v1/accounts
func (h *Handler) List(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	status := c.DefaultQuery("status", StatusActive)
	if status != StatusActive && status != StatusArchived && status != "all" {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid status", nil)
		return
	}
	accType := c.Query("type")

	out, err := h.service.ListPage(c.Request.Context(), userID, status, accType)
	if err != nil {
		response.InternalError(c, "Failed to list accounts", err.Error())
		return
	}
	c.JSON(http.StatusOK, out)
}

// GET /v1/accounts/:id
func (h *Handler) Get(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
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
	a, err := h.service.GetDetail(c.Request.Context(), userID, id, include, c.Query("from"), c.Query("to"))
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}

// PUT /v1/accounts/:id
func (h *Handler) Update(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	// Reject `balance` in body explicitly per spec §3.4. ShouldBindBodyWith
	// caches the body so we can probe for the field name and then bind to
	// the typed struct without re-reading the request.
	var peek map[string]any
	if err := c.ShouldBindBodyWith(&peek, binding.JSON); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	if _, hasBalance := peek["balance"]; hasBalance {
		response.BadRequest(c, "BALANCE_NOT_EDITABLE", ErrBalanceNotEditable.Error(), nil)
		return
	}
	var req UpdateRequest
	if err := c.ShouldBindBodyWith(&req, binding.JSON); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	a, err := h.service.Update(c.Request.Context(), userID, id, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}

// POST /v1/accounts/:id/identifiers — append one identifier
// ({kind, value, bank_code?}); answers the whole account. Owner only.
func (h *Handler) AddIdentifier(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req Identifier
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	a, err := h.service.AddIdentifier(c.Request.Context(), userID, id, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, a)
}

// POST /v1/accounts/:id/adjust-balance
func (h *Handler) AdjustBalance(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}

	var req AdjustBalanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
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

// DELETE /v1/accounts/:id (always soft → archived)
func (h *Handler) Delete(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
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

// POST /v1/accounts/:id/restore (owner only)
func (h *Handler) Restore(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
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

// --- Helpers ---

func parseIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid account id", nil)
		return uuid.Nil, false
	}
	return id, true
}

func mapServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrAccountNotFound):
		response.NotFound(c, "NOT_FOUND", "Account not found")
	case errors.Is(err, ErrMemberNotFound):
		response.NotFound(c, "NOT_FOUND", "Member not found")
	case errors.Is(err, ErrUserNotFound):
		response.NotFound(c, "USER_NOT_FOUND", "No user matches that email")
	case errors.Is(err, ErrInviteTarget):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	case errors.Is(err, ErrContactNotFound):
		response.NotFound(c, "CONTACT_NOT_FOUND", "Contact not found")
	case errors.Is(err, ErrContactArchived):
		response.Fail(c, http.StatusConflict, "CONTACT_ARCHIVED", err.Error(), nil)
	case errors.Is(err, ErrContactNotLinked):
		response.Fail(c, http.StatusUnprocessableEntity, "CONTACT_NOT_LINKED", err.Error(), nil)
	case errors.Is(err, ErrAlreadyMember):
		response.Fail(c, http.StatusConflict, "USER_ALREADY_MEMBER", "User is already a member of this account", nil)
	case errors.Is(err, ErrNotMember):
		response.Fail(c, http.StatusForbidden, "NOT_MEMBER", "Caller is not an active member of this account", nil)
	case errors.Is(err, ErrNotOwner):
		response.Fail(c, http.StatusForbidden, "NOT_OWNER", "Only the account owner can perform this action", nil)
	case errors.Is(err, ErrOwnerMustTransfer):
		response.BadRequest(c, "OWNER_MUST_TRANSFER", err.Error(), nil)
	case errors.Is(err, ErrAccountHasMembers):
		response.Fail(c, http.StatusConflict, "ACCOUNT_HAS_MEMBERS", err.Error(), nil)
	case errors.Is(err, ErrScopeNotAllowed):
		response.Fail(c, http.StatusForbidden, "SCOPE_NOT_ALLOWED", err.Error(), nil)
	case errors.Is(err, ErrInvalidInclude), errors.Is(err, ErrInvalidDate):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	case errors.Is(err, ErrNotInvite), errors.Is(err, notifications.ErrNotificationNotFound), errors.Is(err, notifications.ErrNotForYou):
		response.NotFound(c, "NOT_FOUND", "Invite not found")
	case errors.Is(err, ErrInvalidIdentifier), errors.Is(err, ErrTooManyIdentifiers):
		response.BadRequest(c, "INVALID_IDENTIFIER", err.Error(), nil)
	case errors.Is(err, ErrCreditFieldMismatch):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	case errors.Is(err, ErrTypeChangeNeedsLimit):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	case errors.Is(err, ErrBalanceNotEditable):
		response.BadRequest(c, "BALANCE_NOT_EDITABLE", err.Error(), nil)
	case errors.Is(err, ErrAdjustNoOp):
		response.BadRequest(c, "NO_OP", err.Error(), nil)
	case errors.Is(err, ErrBalanceNotTracked):
		response.Fail(c, http.StatusConflict, "BALANCE_NOT_TRACKED", err.Error(), nil)
	case errors.Is(err, ErrCurrentBalanceAlone):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	default:
		response.InternalError(c, "Account operation failed", err.Error())
	}
}

// POST /v1/accounts/:id/archive (owner only) — hides the wallet, takes it
// out of every calculation and makes it read-only until restored.
func (h *Handler) Archive(c *gin.Context) {
	h.Delete(c)
}
