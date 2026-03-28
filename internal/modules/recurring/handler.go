package recurring

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
	default:
		response.BadRequest(c, "ERROR", err.Error(), nil)
	}
}

func (h *Handler) CreateRecurring(c *gin.Context) {
	var req CreateRecurringRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	r, err := h.service.CreateRecurring(c.Request.Context(), getUserID(c), req)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.Created(c, "Recurring entry created", r)
}

func (h *Handler) GetRecurrings(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "20"))

	var entryType, status, txType, billingCycle *string
	if v := c.Query("entry_type"); v != "" {
		entryType = &v
	}
	if v := c.Query("status"); v != "" {
		status = &v
	}
	if v := c.Query("type"); v != "" {
		txType = &v
	}
	if v := c.Query("billing_cycle"); v != "" {
		billingCycle = &v
	}

	items, total, err := h.service.GetRecurrings(c.Request.Context(), getUserID(c), entryType, status, txType, billingCycle, page, perPage)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Recurring entries fetched", gin.H{"data": items, "pagination": gin.H{"page": page, "total": total}})
}

func (h *Handler) GetRecurring(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	r, err := h.service.GetRecurringByID(c.Request.Context(), getUserID(c), id)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Recurring entry fetched", r)
}

func (h *Handler) UpdateRecurring(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	var req UpdateRecurringRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	if err := h.service.UpdateRecurring(c.Request.Context(), getUserID(c), id, req); err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Recurring entry updated", nil)
}

func (h *Handler) DeleteRecurring(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteRecurring(c.Request.Context(), getUserID(c), id); err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Recurring entry deleted", nil)
}

func (h *Handler) Pause(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	if err := h.service.Pause(c.Request.Context(), getUserID(c), id); err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Entry paused", nil)
}

func (h *Handler) Resume(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	if err := h.service.Resume(c.Request.Context(), getUserID(c), id); err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Entry resumed", nil)
}

func (h *Handler) Cancel(c *gin.Context) {
	id, ok := getID(c, "id")
	if !ok {
		return
	}
	if err := h.service.Cancel(c.Request.Context(), getUserID(c), id); err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Entry cancelled", nil)
}

func (h *Handler) GetUpcoming(c *gin.Context) {
	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))
	items, totalDue, err := h.service.GetUpcoming(c.Request.Context(), getUserID(c), days)
	if err != nil {
		handleErr(c, err)
		return
	}
	response.OK(c, "Upcoming entries fetched", gin.H{"data": items, "total_amount_due": totalDue})
}
