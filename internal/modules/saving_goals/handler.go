package saving_goals

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
	"github.com/ppChub722/chubi-pocket-be/internal/shared/recurrence"
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
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid id", nil)
		return uuid.Nil, uuid.Nil, false
	}
	return uid, id, true
}

// monthParam reads ?month=YYYY-MM ("" = this month).
func monthParam(c *gin.Context) (string, bool) {
	m := c.Query("month")
	if m == "" {
		return "", true
	}
	if _, err := time.Parse("2006-01", m); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid month", nil)
		return "", false
	}
	return m, true
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
	var insufficient *InsufficientError
	switch {
	case errors.As(err, &insufficient):
		response.Fail(c, http.StatusConflict, "INSUFFICIENT_SAVING", err.Error(), gin.H{"available": insufficient.Available})
	case errors.Is(err, ErrGoalNotFound):
		response.NotFound(c, "NOT_FOUND", "Saving goal not found")
	case errors.Is(err, ErrEntryNotFound):
		response.NotFound(c, "NOT_FOUND", "Saving entry not found")
	case errors.Is(err, ErrValidation):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	case errors.Is(err, recurrence.ErrInvalidRule):
		response.BadRequest(c, "INVALID_RULE", err.Error(), nil)
	case errors.Is(err, recurrence.ErrEndTooFar):
		response.BadRequest(c, "END_TOO_FAR", err.Error(), nil)
	case errors.Is(err, ErrSplitMismatch):
		response.BadRequest(c, "SPLIT_MISMATCH", err.Error(), nil)
	case errors.Is(err, ErrAccountNotFound):
		response.BadRequest(c, "ACCOUNT_NOT_FOUND", err.Error(), nil)
	case errors.Is(err, ErrWalletNotTracked):
		response.BadRequest(c, "WALLET_NOT_TRACKED", err.Error(), nil)
	case errors.Is(err, ErrGoalNotActive):
		response.Fail(c, http.StatusConflict, "GOAL_NOT_ACTIVE", err.Error(), nil)
	case errors.Is(err, ErrGoalHasSavings):
		response.Fail(c, http.StatusConflict, "GOAL_HAS_SAVINGS", err.Error(), nil)
	case errors.Is(err, ErrInvalidTransition):
		response.Fail(c, http.StatusConflict, "INVALID_TRANSITION", err.Error(), nil)
	default:
		response.InternalError(c, "Saving operation failed", err.Error())
	}
}

func (h *Handler) List(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	status := c.DefaultQuery("status", StateActive)
	if status != StateActive && status != StateCompleted && status != StateArchived {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid status", nil)
		return
	}
	sortBy := c.Query("sort")
	if !goalSorts[sortBy] {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid sort", nil)
		return
	}
	month, ok := monthParam(c)
	if !ok {
		return
	}
	out, err := h.service.List(c.Request.Context(), uid, status, sortBy, month)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Create(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	var req GoalRequest
	present, ok := bind(c, &req)
	if !ok {
		return
	}
	req.present = present
	out, err := h.service.Create(c.Request.Context(), uid, &req)
	reply(c, http.StatusCreated, out, err)
}

func (h *Handler) Spare(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	out, err := h.service.Spare(c.Request.Context(), uid, atoiOr(c.Query("per_page"), entriesPerPage))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Get(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	month, ok := monthParam(c)
	if !ok {
		return
	}
	out, err := h.service.GetMonth(c.Request.Context(), uid, id, atoiOr(c.Query("per_page"), entriesPerPage), strings.Contains(","+c.Query("include")+",", ",delete_preview,"), month)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Update(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	var req GoalRequest
	present, ok := bind(c, &req)
	if !ok {
		return
	}
	req.present = present
	out, err := h.service.Update(c.Request.Context(), uid, id, &req)
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

func (h *Handler) Close(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	var req CloseRequest
	if _, ok := bind(c, &req); !ok {
		return
	}
	out, err := h.service.Close(c.Request.Context(), uid, id, req)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Archive(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.Archive(c.Request.Context(), uid, id)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Restore(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.Restore(c.Request.Context(), uid, id)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Entries(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	goalID, ok := optionalID(c, "goal_id")
	if !ok {
		return
	}
	accountID, ok := optionalID(c, "account_id")
	if !ok {
		return
	}
	bucket := c.Query("bucket")
	if bucket != "" && bucket != BucketSpare {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid bucket", nil)
		return
	}
	out, err := h.service.Entries(c.Request.Context(), uid, goalID, bucket == BucketSpare, accountID,
		atoiOr(c.Query("page"), 1), atoiOr(c.Query("per_page"), entriesPerPage))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) CreateEntry(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	var req EntryRequest
	if _, ok := bind(c, &req); !ok {
		return
	}
	out, err := h.service.CreateEntry(c.Request.Context(), uid, req)
	reply(c, http.StatusCreated, out, err)
}

func (h *Handler) UpdateEntry(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	var req EntryUpdateRequest
	present, ok := bind(c, &req)
	if !ok {
		return
	}
	req.present = present
	out, err := h.service.UpdateEntry(c.Request.Context(), uid, id, req)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) DeleteEntry(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.DeleteEntry(c.Request.Context(), uid, id)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) PutAutoSaves(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	var req AutoSavesRequest
	present, ok := bind(c, &req)
	if !ok {
		return
	}
	if !present["items"] || req.Items == nil {
		response.BadRequest(c, "VALIDATION_ERROR", "items is required", nil)
		return
	}
	out, err := h.service.PutAutoSavesGoal(c.Request.Context(), uid, id, req.Items)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) PutAutoSavesSpare(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	var req AutoSavesRequest
	present, ok := bind(c, &req)
	if !ok {
		return
	}
	if !present["items"] || req.Items == nil {
		response.BadRequest(c, "VALIDATION_ERROR", "items is required", nil)
		return
	}
	out, err := h.service.PutAutoSavesSpare(c.Request.Context(), uid, req.Items)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) PutAutoSave(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	var req AutoSaveRequest
	if _, ok := bind(c, &req); !ok {
		return
	}
	out, err := h.service.PutAutoSaveGoal(c.Request.Context(), uid, id, req)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) DeleteAutoSave(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.DeleteAutoSaveGoal(c.Request.Context(), uid, id)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) PutAutoSaveSpare(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	var req AutoSaveRequest
	if _, ok := bind(c, &req); !ok {
		return
	}
	out, err := h.service.PutAutoSaveSpare(c.Request.Context(), uid, req)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) DeleteAutoSaveSpare(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	out, err := h.service.DeleteAutoSaveSpare(c.Request.Context(), uid)
	reply(c, http.StatusOK, out, err)
}
