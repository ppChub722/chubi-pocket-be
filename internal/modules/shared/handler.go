package shared

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
	case ErrAlreadyLinked:
		response.BadRequest(c, "ALREADY_LINKED", err.Error(), nil)
	case ErrHasSettlements:
		response.BadRequest(c, "HAS_SETTLEMENTS", err.Error(), nil)
	case ErrOverpayment:
		response.BadRequest(c, "OVERPAYMENT", err.Error(), nil)
	default:
		msg := err.Error()
		if msg == "forbidden" {
			response.Fail(c, http.StatusForbidden, "FORBIDDEN", "Access denied", nil)
			return
		}
		response.InternalError(c, msg, nil)
	}
}

// =============================================================
// SHARED EXPENSES
// =============================================================

func (h *Handler) CreateSharedExpense(c *gin.Context) {
	var req CreateSharedExpenseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	result, err := h.service.CreateSharedExpense(c.Request.Context(), getUserID(c), req)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.Created(c, "Shared expense created", result)
}

func (h *Handler) GetSharedExpenses(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "20"))

	var from, to *string
	if v := c.Query("from"); v != "" {
		from = &v
	}
	if v := c.Query("to"); v != "" {
		to = &v
	}

	items, total, err := h.service.GetSharedExpenses(c.Request.Context(), getUserID(c), nil, from, to, page, perPage)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Shared expenses fetched", gin.H{"data": items, "pagination": gin.H{"page": page, "total": total}})
}

func (h *Handler) GetSharedExpense(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	result, err := h.service.GetSharedExpenseByID(c.Request.Context(), getUserID(c), id)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Shared expense fetched", result)
}

func (h *Handler) UpdateSharedExpense(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	var req UpdateSharedExpenseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	if err := h.service.UpdateSharedExpense(c.Request.Context(), getUserID(c), id, req); err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Shared expense updated", nil)
}

func (h *Handler) DeleteSharedExpense(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteSharedExpense(c.Request.Context(), getUserID(c), id); err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Shared expense deleted", nil)
}

func (h *Handler) GetSummary(c *gin.Context) {
	summary, err := h.service.GetSummary(c.Request.Context(), getUserID(c))
	if err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Summary fetched", summary)
}

// =============================================================
// SPLITS
// =============================================================

func (h *Handler) GetSplits(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	splits, err := h.service.GetSplits(c.Request.Context(), getUserID(c), id)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Splits fetched", gin.H{"data": splits})
}

func (h *Handler) AddSplit(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	var req CreateSplitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	split, err := h.service.AddSplit(c.Request.Context(), getUserID(c), id, req)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.Created(c, "Split added", split)
}

func (h *Handler) UpdateSplit(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	var req UpdateSplitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	split, err := h.service.UpdateSplit(c.Request.Context(), getUserID(c), id, req)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Split updated", split)
}

func (h *Handler) DeleteSplit(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteSplit(c.Request.Context(), getUserID(c), id); err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Split deleted", nil)
}

// =============================================================
// SETTLEMENTS
// =============================================================

func (h *Handler) GetSettlements(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	settlements, err := h.service.GetSettlements(c.Request.Context(), getUserID(c), id)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Settlements fetched", gin.H{"data": settlements})
}

func (h *Handler) CreateSettlement(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	var req CreateSettlementRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	settlement, split, err := h.service.CreateSettlement(c.Request.Context(), getUserID(c), id, req)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.Created(c, "Settlement recorded", gin.H{
		"id":       settlement.ID,
		"split_id": settlement.SplitID,
		"amount":   settlement.Amount,
		"split_status": gin.H{
			"paid_amount": split.PaidAmount,
			"is_settled":  split.IsSettled,
			"outstanding": split.Outstanding,
		},
	})
}

func (h *Handler) DeleteSettlement(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteSettlement(c.Request.Context(), getUserID(c), id); err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Settlement deleted", nil)
}
