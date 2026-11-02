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
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
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
		response.BindError(c, err)
		return false
	}
	return true
}

func pathID(c *gin.Context, name, label string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		response.Validation(c, "Invalid "+label, apierr.Field(name, apierr.FieldInvalid))
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) Create(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
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
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return
	}
	f := ListFilter{
		Status: c.Query("status"), Q: c.Query("q"), Sort: c.Query("sort"),
		Page: atoiOr(c.Query("page"), 1), PerPage: atoiOr(c.Query("per_page"), 20),
	}
	switch f.Status {
	case "", StatusActive, StatusCompleted, StatusCancelled, StatusArchived, "all", "current", "unsettled", "receivable", "payable", "ended":
	default:
		response.Validation(c, "Invalid status", apierr.Field("status", apierr.FieldInvalid))
		return
	}
	switch f.Sort {
	case "", "updated_desc", "start_date_desc", "name_asc":
	default:
		response.Validation(c, "Invalid sort", apierr.Field("sort", apierr.FieldInvalid))
		return
	}
	for _, v := range c.QueryArray("tag_id") {
		id, err := uuid.Parse(v)
		if err != nil {
			response.Validation(c, "Invalid tag_id", apierr.Field("tag_id", apierr.FieldInvalid))
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

// BindMyContact — PUT /projects/:id/members/:member_id/my-contact (0.4.2-3 F3).
func (h *Handler) BindMyContact(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	memberID, ok := pathID(c, "member_id", "member id")
	if !ok {
		return
	}
	var req BindMyContactRequest
	if !bindJSON(c, &req) {
		return
	}
	out, err := h.service.BindMyContact(c.Request.Context(), userID, id, memberID, &req.ContactID)
	reply(c, http.StatusOK, out, err)
}

// UnbindMyContact — DELETE /projects/:id/members/:member_id/my-contact (0.4.2-3 F3).
func (h *Handler) UnbindMyContact(c *gin.Context) {
	userID, id, ok := h.bindProjectID(c)
	if !ok {
		return
	}
	memberID, ok := pathID(c, "member_id", "member id")
	if !ok {
		return
	}
	out, err := h.service.BindMyContact(c.Request.Context(), userID, id, memberID, nil)
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

func (h *Handler) AcceptLinkRequest(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
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
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
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
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
		return uuid.Nil, uuid.Nil, false
	}
	id, ok := pathID(c, "id", "project id")
	return userID, id, ok
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
