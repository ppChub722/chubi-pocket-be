package personal_debts

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
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

type Handler struct {
	service *Service
}

func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

func caller(c *gin.Context) (uuid.UUID, bool) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
	}
	return userID, ok
}

// GET /v1/personal-debts/people — one page of people + the header totals.
func (h *Handler) People(c *gin.Context) {
	userID, ok := caller(c)
	if !ok {
		return
	}
	f := PeopleFilter{
		Q: c.Query("q"), All: c.Query("all") == "true", Side: c.Query("side"),
		Page: atoiOr(c.Query("page"), 1), PerPage: atoiOr(c.Query("per_page"), 30),
	}
	if f.Side != "" && f.Side != "owed_to_me" && f.Side != "i_owe" {
		response.BadRequest(c, "VALIDATION_ERROR", "side must be owed_to_me or i_owe", nil)
		return
	}
	out, err := h.service.People(c.Request.Context(), userID, f)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// GET /v1/personal-debts/people/:key — the person page.
func (h *Handler) Person(c *gin.Context) {
	userID, ok := caller(c)
	if !ok {
		return
	}
	out, err := h.service.Person(c.Request.Context(), userID, c.Param("key"),
		atoiOr(c.Query("page"), 1), atoiOr(c.Query("per_page"), 30))
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// GET /v1/personal-debts/people/:key/entries
func (h *Handler) Entries(c *gin.Context) {
	userID, ok := caller(c)
	if !ok {
		return
	}
	f := EntryFilter{
		Q: c.Query("q"), Sort: c.Query("sort"), Direction: c.Query("direction"),
		Money: c.Query("money") == "true",
		Page:  atoiOr(c.Query("page"), 1), PerPage: atoiOr(c.Query("per_page"), 30),
	}
	switch f.Sort {
	case "", "date_desc", "date_asc", "amount_desc", "amount_asc":
	default:
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid sort", nil)
		return
	}
	if f.Direction != "" && f.Direction != DirectionPaid && f.Direction != DirectionReceived {
		response.BadRequest(c, "VALIDATION_ERROR", "direction must be paid or received", nil)
		return
	}
	if v := c.Query("paid"); v != "" {
		b := v == "true"
		f.Paid = &b
	}
	if v := c.Query("account_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			response.BadRequest(c, "VALIDATION_ERROR", "Invalid account_id", nil)
			return
		}
		f.AccountID = &id
	}
	for _, p := range []struct {
		key string
		dst **string
	}{{"from", &f.From}, {"to", &f.To}} {
		if v := c.Query(p.key); v != "" {
			if !validDate(v) {
				response.BadRequest(c, "VALIDATION_ERROR", "Invalid "+p.key, nil)
				return
			}
			vv := v
			*p.dst = &vv
		}
	}
	out, err := h.service.Entries(c.Request.Context(), userID, c.Param("key"), f)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// POST /v1/personal-debts/people/:key/entries — the one debt write.
func (h *Handler) CreateEntry(c *gin.Context) {
	userID, ok := caller(c)
	if !ok {
		return
	}
	var req CreateEntryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out, err := h.service.CreateEntry(c.Request.Context(), userID, c.Param("key"), req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

// GET /v1/personal-debts/:id — the entry, page-ready (tx detail in debt mode).
func (h *Handler) Get(c *gin.Context) {
	userID, ok := caller(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	out, err := h.service.GetEntry(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// PUT /v1/personal-debts/:id
func (h *Handler) Update(c *gin.Context) {
	userID, ok := caller(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req UpdateEntryRequest
	if err := c.ShouldBindBodyWith(&req, binding.JSON); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out, err := h.service.UpdateEntry(c.Request.Context(), userID, id, req)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// DELETE /v1/personal-debts/:id
func (h *Handler) Delete(c *gin.Context) {
	userID, ok := caller(c)
	if !ok {
		return
	}
	id, ok := parseIDParam(c)
	if !ok {
		return
	}
	out, err := h.service.DeleteEntry(c.Request.Context(), userID, id)
	if err != nil {
		mapServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func parseIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid entry id", nil)
		return uuid.Nil, false
	}
	return id, true
}

func validDate(s string) bool {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	_, err1 := strconv.Atoi(s[:4])
	_, err2 := strconv.Atoi(s[5:7])
	_, err3 := strconv.Atoi(s[8:])
	return err1 == nil && err2 == nil && err3 == nil
}

func mapServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrEntryNotFound):
		response.NotFound(c, "NOT_FOUND", "Debt entry not found")
	case errors.Is(err, ErrPersonNotFound):
		response.NotFound(c, "NOT_FOUND", err.Error())
	case errors.Is(err, shared.ErrInvalidPersonKey):
		response.BadRequest(c, "INVALID_PERSON_KEY", err.Error(), nil)
	case errors.Is(err, ErrContactArchived):
		response.Fail(c, http.StatusConflict, "CONTACT_ARCHIVED", err.Error(), nil)
	case errors.Is(err, ErrContactNotFound):
		response.BadRequest(c, "CONTACT_NOT_FOUND", "Contact not found", nil)
	case errors.Is(err, ErrTagsNeedWallet):
		response.BadRequest(c, "TAGS_NEED_WALLET", err.Error(), nil)
	case errors.Is(err, ErrMarkPaidNotFound):
		response.BadRequest(c, "ENTRY_NOT_FOUND", err.Error(), nil)
	case errors.Is(err, ErrManagedBySource):
		response.Fail(c, http.StatusConflict, "ENTRY_MANAGED_BY_SOURCE", err.Error(), nil)
	case errors.Is(err, transactions.ErrAccountArchived):
		response.Fail(c, http.StatusConflict, "ACCOUNT_ARCHIVED", err.Error(), nil)
	case errors.Is(err, transactions.ErrAccountForbidden):
		response.BadRequest(c, "ACCOUNT_NOT_FOUND", "Account not found", nil)
	case errors.Is(err, transactions.ErrTagNotFound):
		response.BadRequest(c, "TAG_NOT_FOUND", "Tag not found", nil)
	default:
		response.InternalError(c, "Debt operation failed", err.Error())
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

// POST /v1/personal-debts/split-requests/:notification_id/accept —
// "add to my debts" on a split_created notification (contract §5).
func (h *Handler) AcceptSplitRequest(c *gin.Context) {
	userID, ok := caller(c)
	if !ok {
		return
	}
	nid, err := uuid.Parse(c.Param("notification_id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid notification id", nil)
		return
	}
	out, err := h.service.AcceptSplitRequest(c.Request.Context(), userID, nid)
	switch {
	case errors.Is(err, ErrSplitChangeStale):
		response.Fail(c, http.StatusConflict, "SPLIT_CHANGE_STALE", err.Error(), nil)
	case errors.Is(err, ErrNotSplitRequest):
		response.BadRequest(c, "NOT_SPLIT_REQUEST", err.Error(), nil)
	case errors.Is(err, notifications.ErrNotificationNotFound):
		response.NotFound(c, "NOT_FOUND", "Notification not found")
	case errors.Is(err, notifications.ErrNotForYou):
		response.Fail(c, http.StatusForbidden, "NOT_FOR_YOU", "Notification is not addressed to caller", nil)
	case errors.Is(err, notifications.ErrStateConflict):
		response.Fail(c, http.StatusConflict, "NOTIFICATION_STATE_CONFLICT", err.Error(), nil)
	case err != nil:
		mapServiceError(c, err)
	default:
		c.JSON(http.StatusCreated, out)
	}
}

// POST /v1/personal-debts/split-changes/:notification_id/apply —
// "อัปเดตตาม" on a split_changed notification (one shot).
func (h *Handler) ApplySplitChange(c *gin.Context) {
	userID, ok := caller(c)
	if !ok {
		return
	}
	nid, err := uuid.Parse(c.Param("notification_id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid notification id", nil)
		return
	}
	out, err := h.service.ApplySplitChange(c.Request.Context(), userID, nid)
	switch {
	case errors.Is(err, ErrNotSplitChange):
		response.BadRequest(c, "NOT_SPLIT_CHANGE", err.Error(), nil)
	case errors.Is(err, ErrNotificationActioned):
		response.Fail(c, http.StatusConflict, "NOTIFICATION_ACTIONED", err.Error(), nil)
	case errors.Is(err, ErrSplitChangeStale):
		response.Fail(c, http.StatusConflict, "SPLIT_CHANGE_STALE", err.Error(), nil)
	case errors.Is(err, notifications.ErrNotificationNotFound):
		response.NotFound(c, "NOT_FOUND", "Notification not found")
	case errors.Is(err, notifications.ErrNotForYou):
		response.Fail(c, http.StatusForbidden, "NOT_FOR_YOU", "Notification is not addressed to caller", nil)
	case errors.Is(err, notifications.ErrStateConflict):
		response.Fail(c, http.StatusConflict, "NOTIFICATION_STATE_CONFLICT", err.Error(), nil)
	case err != nil:
		mapServiceError(c, err)
	default:
		c.JSON(http.StatusOK, out)
	}
}
