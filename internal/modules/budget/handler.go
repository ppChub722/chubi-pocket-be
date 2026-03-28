package budget

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/ppChub722/finna-bbear-be/internal/platform/response"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func getUserID(c *gin.Context) int64 {
	id, _ := c.Get("userID")
	return id.(int64)
}

func getID(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil {
		response.BadRequest(c, "INVALID_ID", "Invalid ID", nil)
		return 0, false
	}
	return id, true
}

func handleErr(c *gin.Context, err error) {
	switch err {
	case ErrNotFound:
		response.NotFound(c, "NOT_FOUND", "Resource not found")
	case ErrForbidden:
		response.Fail(c, http.StatusForbidden, "FORBIDDEN", "Access denied", nil)
	case ErrDuplicateBudget:
		response.BadRequest(c, "DUPLICATE_BUDGET", err.Error(), nil)
	default:
		response.InternalError(c, err.Error(), nil)
	}
}

// =============================================================
// BUDGETS
// =============================================================

func (h *Handler) CreateBudget(c *gin.Context) {
	var req CreateBudgetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	b, err := h.service.CreateBudget(c.Request.Context(), getUserID(c), req)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.Created(c, "Budget created", b)
}

func (h *Handler) GetBudgets(c *gin.Context) {
	var isActive *bool
	if v := c.Query("is_active"); v != "" {
		b := v == "true"
		isActive = &b
	}
	var period *string
	if v := c.Query("period"); v != "" {
		period = &v
	}
	budgets, err := h.service.GetBudgets(c.Request.Context(), getUserID(c), isActive, period)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Budgets fetched", gin.H{"data": budgets})
}

func (h *Handler) GetBudget(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	b, err := h.service.GetBudgetByID(c.Request.Context(), getUserID(c), id)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Budget fetched", b)
}

func (h *Handler) UpdateBudget(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	var req UpdateBudgetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	if err := h.service.UpdateBudget(c.Request.Context(), getUserID(c), id, req); err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Budget updated", nil)
}

func (h *Handler) DeleteBudget(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteBudget(c.Request.Context(), getUserID(c), id); err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Budget deleted", nil)
}

func (h *Handler) GetBudgetOverview(c *gin.Context) {
	period := c.DefaultQuery("period", "monthly")
	overview, err := h.service.GetBudgetOverview(c.Request.Context(), getUserID(c), period)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Budget overview fetched", overview)
}

// =============================================================
// SAVING GOALS
// =============================================================

func (h *Handler) CreateSavingGoal(c *gin.Context) {
	var req CreateSavingGoalRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	sg, err := h.service.CreateSavingGoal(c.Request.Context(), getUserID(c), req)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.Created(c, "Saving goal created", sg)
}

func (h *Handler) GetSavingGoals(c *gin.Context) {
	var isCompleted *bool
	if v := c.Query("is_completed"); v != "" {
		b := v == "true"
		isCompleted = &b
	}
	goals, err := h.service.GetSavingGoals(c.Request.Context(), getUserID(c), isCompleted)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Saving goals fetched", gin.H{"data": goals})
}

func (h *Handler) GetSavingGoal(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	sg, err := h.service.GetSavingGoalByID(c.Request.Context(), getUserID(c), id)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Saving goal fetched", sg)
}

func (h *Handler) UpdateSavingGoal(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	var req UpdateSavingGoalRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	if err := h.service.UpdateSavingGoal(c.Request.Context(), getUserID(c), id, req); err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Saving goal updated", nil)
}

func (h *Handler) DeleteSavingGoal(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteSavingGoal(c.Request.Context(), getUserID(c), id); err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Saving goal deleted", nil)
}
