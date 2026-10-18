package budgets

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

type Handler struct {
	service *Service
}

func NewHandler(s *Service) *Handler { return &Handler{service: s} }

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func tz(c *gin.Context) string { return c.GetHeader("X-Timezone") }

func user(c *gin.Context) (uuid.UUID, bool) {
	id, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
	}
	return id, ok
}

func userAndID(c *gin.Context) (uuid.UUID, uuid.UUID, bool) {
	uid, ok := user(c)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid budget id", nil)
		return uuid.Nil, uuid.Nil, false
	}
	return uid, id, true
}

func optionalID(c *gin.Context, name string) (*uuid.UUID, bool) {
	v := c.Query(name)
	if v == "" {
		return nil, true
	}
	id, err := uuid.Parse(v)
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid "+name, nil)
		return nil, false
	}
	return &id, true
}

func bind(c *gin.Context, v any) (map[string]bool, bool) {
	raw, err := c.GetRawData()
	if err == nil && len(raw) == 0 {
		raw = []byte("{}")
	}
	keys := map[string]json.RawMessage{}
	if err == nil {
		err = json.Unmarshal(raw, v)
	}
	if err == nil {
		err = json.Unmarshal(raw, &keys)
	}
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return nil, false
	}
	present := map[string]bool{}
	for k := range keys {
		present[k] = true
	}
	return present, true
}

func reply(c *gin.Context, status int, out any, err error) {
	if err != nil {
		mapError(c, err)
		return
	}
	c.JSON(status, out)
}

func mapError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrBudgetNotFound):
		response.NotFound(c, "NOT_FOUND", "Budget not found")
	case errors.Is(err, ErrPeriodNotFound):
		response.NotFound(c, "NOT_FOUND", "Budget period not found")
	case errors.Is(err, ErrValidation):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	case errors.Is(err, ErrInvalidRule):
		response.BadRequest(c, "INVALID_RULE", err.Error(), nil)
	case errors.Is(err, ErrInvalidCategory):
		response.BadRequest(c, "INVALID_CATEGORY", err.Error(), nil)
	case errors.Is(err, ErrAccountNotFound):
		response.BadRequest(c, "ACCOUNT_NOT_FOUND", err.Error(), nil)
	case errors.Is(err, ErrInvalidTransition):
		response.Fail(c, http.StatusConflict, "INVALID_TRANSITION", err.Error(), nil)
	default:
		response.InternalError(c, "Budget operation failed", err.Error())
	}
}

func (h *Handler) List(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	status := c.DefaultQuery("status", "active")
	if status != "active" && status != "ended" && status != "archived" {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid status", nil)
		return
	}
	health := c.Query("health")
	if health != "" && health != "normal" && health != "near" && health != "over" {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid health", nil)
		return
	}
	sortBy := c.DefaultQuery("sort", "risk")
	if sortBy != "risk" && sortBy != "name" && sortBy != "newest" {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid sort", nil)
		return
	}
	out, err := h.service.List(c.Request.Context(), uid, status, sortBy, health, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Create(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	var req Request
	present, ok := bind(c, &req)
	if !ok {
		return
	}
	req.present = present
	out, err := h.service.Create(c.Request.Context(), uid, &req, tz(c))
	reply(c, http.StatusCreated, out, err)
}

func (h *Handler) Get(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	periodID, ok := optionalID(c, "period_id")
	if !ok {
		return
	}
	out, err := h.service.Get(c.Request.Context(), uid, id, periodID, tz(c), strings.Contains(","+c.Query("include")+",", ",delete_preview,"))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Update(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	var req Request
	present, ok := bind(c, &req)
	if !ok {
		return
	}
	req.present = present
	out, err := h.service.Update(c.Request.Context(), uid, id, &req, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Delete(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	err := h.service.Delete(c.Request.Context(), uid, id)
	reply(c, http.StatusOK, gin.H{"id": id}, err)
}

func (h *Handler) Periods(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.Periods(c.Request.Context(), uid, id,
		atoiOr(c.Query("page"), 1), atoiOr(c.Query("per_page"), 20), tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) UpdatePeriod(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	periodID, err := uuid.Parse(c.Param("period_id"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid period id", nil)
		return
	}
	var req PeriodRequest
	present, ok := bind(c, &req)
	if !ok {
		return
	}
	req.present = present
	out, err := h.service.UpdatePeriod(c.Request.Context(), uid, id, periodID, &req, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Items(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	periodID, ok := optionalID(c, "period_id")
	if !ok {
		return
	}
	source := c.Query("source")
	switch source {
	case "", SourceNormal, SourceRecurring, SourceEvent, SourceUpcoming, SourcePending:
	default:
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid source", nil)
		return
	}
	out, err := h.service.Items(c.Request.Context(), uid, id, periodID, source,
		atoiOr(c.Query("page"), 1), atoiOr(c.Query("per_page"), itemsPerPage), tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Close(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.Close(c.Request.Context(), uid, id, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Archive(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.Archive(c.Request.Context(), uid, id, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Restore(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.Restore(c.Request.Context(), uid, id, tz(c))
	reply(c, http.StatusOK, out, err)
}
