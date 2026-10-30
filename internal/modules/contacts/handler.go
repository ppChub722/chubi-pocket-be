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

	var req CreateContactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}

	res, err := h.service.Create(c.Request.Context(), userID, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, res)
}

func (h *Handler) List(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}

	f := ListFilter{
		Status: c.Query("status"),
		Search: c.Query("search"),
	}
	if v := c.Query("linked"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			response.Validation(c, "linked must be true or false", apierr.Field("linked", apierr.FieldInvalid))
			return
		}
		f.Linked = &b
	}

	out, err := h.service.List(c.Request.Context(), userID, f)
	if err != nil {
		response.InternalError(c, "Failed to list contacts", err)
		return
	}
	c.JSON(http.StatusOK, ListResponse{Data: out})
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
	out, err := h.service.GetDetail(c.Request.Context(), userID, id, includes(c, "unlinked_names"), includes(c, "delete_preview"))
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
	var req UpdateContactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}
	out, err := h.service.Update(c.Request.Context(), userID, id, req)
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
	out, err := h.service.Archive(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
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
	out, err := h.service.Restore(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
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
	splitsRestored, err := h.service.Delete(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, DeleteContactResult{ID: id, SplitsRestored: splitsRestored})
}

func (h *Handler) Absorb(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req AbsorbRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BindError(c, err)
		return
	}
	out, err := h.service.Absorb(c.Request.Context(), userID, id, req.Names)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) RequestLink(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
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

func (h *Handler) AcceptLinkRequest(c *gin.Context) {
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
	out, err := h.service.AcceptLinkRequest(c.Request.Context(), userID, notifID)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) RejectLinkRequest(c *gin.Context) {
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
	out, err := h.service.RejectLinkRequest(c.Request.Context(), userID, notifID)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) SenderProfile(c *gin.Context) {
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
	out, err := h.service.GetSenderProfile(c.Request.Context(), userID, notifID)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) CreateLinkedContact(c *gin.Context) {
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
	var req CreateLinkedContactRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		response.BindError(c, err)
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

func (h *Handler) LinkExistingContact(c *gin.Context) {
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
	contactID, err := uuid.Parse(c.Param("contact_id"))
	if err != nil {
		response.Validation(c, "Invalid contact id", apierr.Field("contact_id", apierr.FieldInvalid))
		return
	}
	var req LinkExistingContactRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		response.BindError(c, err)
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

func (h *Handler) Unlink(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
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

func (h *Handler) UnlinkedNames(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	out, err := h.service.UnlinkedNames(c.Request.Context(), userID)
	if err != nil {
		response.InternalError(c, "Failed to list unlinked names", err)
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
		response.Validation(c, "Invalid contact id", apierr.Field("id", apierr.FieldInvalid))
		return uuid.Nil, false
	}
	return id, true
}

func mapServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrContactNotFound):
		response.NotFound(c, apierr.NotFound, "Contact not found")
	case errors.Is(err, ErrAlreadyArchived):
		response.BadRequest(c, apierr.AlreadyArchived, "Contact is already archived", nil)
	case errors.Is(err, ErrNotArchived):
		response.BadRequest(c, apierr.NotArchived, "Contact is not archived", nil)
	case errors.Is(err, ErrAlreadyLinked):
		response.Fail(c, http.StatusConflict, apierr.AlreadyLinked, "Contact is already linked", nil)
	case errors.Is(err, ErrNotLinked):
		response.BadRequest(c, apierr.NotLinked, "Contact is not linked", nil)
	case errors.Is(err, ErrCannotLinkSelf):
		response.BadRequest(c, apierr.CannotLinkSelf, "Cannot link a contact to yourself", nil)
	case errors.Is(err, notifications.ErrNotificationNotFound), errors.Is(err, notifications.ErrNotForYou), errors.Is(err, ErrNotLinkRequest):
		response.NotFound(c, apierr.NotFound, "Link request not found")
	case errors.Is(err, ErrRequestRejected):
		response.Fail(c, http.StatusConflict, apierr.RequestRejected, "The link request was rejected", nil)
	case errors.Is(err, ErrNotAccepted):
		response.Fail(c, http.StatusConflict, apierr.NotAccepted, "Accept the link request first", nil)
	case errors.Is(err, notifications.ErrStateConflict):
		response.Fail(c, http.StatusConflict, apierr.StateConflict, "The link request was already answered", nil)
	default:
		response.InternalError(c, "Contact operation failed", err)
	}
}

func includes(c *gin.Context, name string) bool {
	for _, p := range strings.Split(c.Query("include"), ",") {
		if strings.TrimSpace(p) == name {
			return true
		}
	}
	return false
}
