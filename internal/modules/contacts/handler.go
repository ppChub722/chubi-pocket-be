package contacts

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
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

// POST /v1/contacts
func (h *Handler) Create(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	var req CreateContactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	res, err := h.service.Create(c.Request.Context(), userID, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, res)
}

// GET /v1/contacts?status=&linked=&search=
func (h *Handler) List(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}

	f := ListFilter{
		Status: c.Query("status"),
		Search: c.Query("search"),
	}
	if v := c.Query("linked"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			response.BadRequest(c, "VALIDATION_ERROR", "linked must be true or false", nil)
			return
		}
		f.Linked = &b
	}

	out, err := h.service.List(c.Request.Context(), userID, f)
	if err != nil {
		response.InternalError(c, "Failed to list contacts", err.Error())
		return
	}
	c.JSON(http.StatusOK, ListResponse{Data: out})
}

// GET /v1/contacts/:id
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
	out, err := h.service.GetDetail(c.Request.Context(), userID, id, includes(c, "unlinked_names"))
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// PUT /v1/contacts/:id
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
	var req UpdateContactRequest
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

// POST /v1/contacts/:id/archive
func (h *Handler) Archive(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	out, err := h.service.Archive(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/contacts/:id/restore
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
	out, err := h.service.Restore(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// DELETE /v1/contacts/:id
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
	splitsRestored, err := h.service.Delete(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, DeleteContactResult{ID: id, SplitsRestored: splitsRestored})
}

// POST /v1/contacts/:id/absorb
func (h *Handler) Absorb(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req AbsorbRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out, err := h.service.Absorb(c.Request.Context(), userID, id, req.Names)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/contacts/:id/request-link
func (h *Handler) RequestLink(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	out, err := h.service.RequestLink(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/contacts/link-requests/:notification_id/accept
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

// POST /v1/contacts/link-requests/:notification_id/reject
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
	out, err := h.service.RejectLinkRequest(c.Request.Context(), userID, notifID)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// GET /v1/contacts/link-requests/:notification_id/sender-profile
//
// Returns the sender's public profile (display_name, email, icon_code)
// for pre-filling the create-contact form during the inbox tap flow.
// Gated to "caller is the recipient and the request is still pending".
func (h *Handler) SenderProfile(c *gin.Context) {
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
	out, err := h.service.GetSenderProfile(c.Request.Context(), userID, notifID)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/contacts/link-requests/:notification_id/create-linked-contact
//
// Post-accept "no existing contact, create one" path. The notification
// must already be actioned (Accept was tapped). BE pulls display_name +
// email from the sender so the contact's snapshot stays correct as a
// fallback if it's later unlinked.
func (h *Handler) CreateLinkedContact(c *gin.Context) {
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
	var req CreateLinkedContactRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out, err := h.service.CreateLinkedContactFromLinkRequest(
		c.Request.Context(), userID, notifID, req,
	)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/contacts/link-requests/:notification_id/link-existing-contact/:contact_id
//
// Post-accept "I already have a contact for them, just wire the link"
// path. The notification must be actioned; the contact must be caller-
// owned. display_name + email are NOT touched on the contact (those
// project from the linked user once linked); phone / description / note / icon are
// optional B-side updates.
func (h *Handler) LinkExistingContact(c *gin.Context) {
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
	contactID, err := uuid.Parse(c.Param("contact_id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid contact id", nil)
		return
	}
	var req LinkExistingContactRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out, err := h.service.LinkExistingContactFromLinkRequest(
		c.Request.Context(), userID, notifID, contactID, req,
	)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/contacts/:id/unlink
func (h *Handler) Unlink(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	out, err := h.service.Unlink(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// GET /v1/contacts/unlinked-names
func (h *Handler) UnlinkedNames(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
		return
	}
	out, err := h.service.UnlinkedNames(c.Request.Context(), userID)
	if err != nil {
		response.InternalError(c, "Failed to list unlinked names", err.Error())
		return
	}
	if out == nil {
		out = []UnlinkedName{}
	}
	c.JSON(http.StatusOK, UnlinkedNameList{Data: out})
}

func parseIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid contact id", nil)
		return uuid.Nil, false
	}
	return id, true
}

func mapServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrContactNotFound):
		response.NotFound(c, "NOT_FOUND", "Contact not found")
	case errors.Is(err, ErrAlreadyArchived):
		response.BadRequest(c, "ALREADY_ARCHIVED", "Contact is already archived", nil)
	case errors.Is(err, ErrNotArchived):
		response.BadRequest(c, "NOT_ARCHIVED", "Contact is not archived", nil)
	case errors.Is(err, ErrAlreadyLinked):
		response.Fail(c, http.StatusConflict, "ALREADY_LINKED", "Contact is already linked", nil)
	case errors.Is(err, ErrNotLinked):
		response.BadRequest(c, "NOT_LINKED", "Contact is not linked", nil)
	case errors.Is(err, ErrCannotLinkSelf):
		response.BadRequest(c, "CANNOT_LINK_SELF", "Cannot link a contact to yourself", nil)
	case errors.Is(err, notifications.ErrNotificationNotFound), errors.Is(err, notifications.ErrNotForYou), errors.Is(err, ErrNotLinkRequest):
		response.NotFound(c, "NOT_FOUND", "Link request not found")
	case errors.Is(err, ErrRequestRejected):
		response.Fail(c, http.StatusConflict, "REQUEST_REJECTED", "The link request was rejected", nil)
	case errors.Is(err, ErrNotAccepted):
		response.Fail(c, http.StatusConflict, "NOT_ACCEPTED", "Accept the link request first", nil)
	case errors.Is(err, notifications.ErrStateConflict):
		response.Fail(c, http.StatusConflict, "STATE_CONFLICT", "The link request was already answered", nil)
	default:
		response.InternalError(c, "Contact operation failed", err.Error())
	}
}

// includes reports whether the comma-separated ?include= list has name.
func includes(c *gin.Context, name string) bool {
	for _, p := range strings.Split(c.Query("include"), ",") {
		if strings.TrimSpace(p) == name {
			return true
		}
	}
	return false
}
