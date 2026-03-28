package accounting

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
	id, exists := c.Get("userID")
	if !exists {
		return 0
	}
	return id.(int64)
}

func getIDParam(c *gin.Context, name string) (int64, bool) {
	idStr := c.Param(name)
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		response.BadRequest(c, "INVALID_ID", "Invalid ID parameter", nil)
		return 0, false
	}
	return id, true
}

func handleServiceError(c *gin.Context, err error) {
	switch err {
	case ErrNotFound:
		response.NotFound(c, "NOT_FOUND", "Resource not found")
	case ErrForbidden:
		response.Fail(c, http.StatusForbidden, "FORBIDDEN", "Access denied", nil)
	case ErrDuplicateTag:
		response.Fail(c, http.StatusConflict, "TAG_EXISTS", "Tag name already exists", nil)
	default:
		response.InternalError(c, err.Error(), nil)
	}
}

// =============================================================
// ACCOUNTS
// =============================================================

func (h *Handler) CreateAccount(c *gin.Context) {
	var req CreateAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}

	account, err := h.service.CreateAccount(c.Request.Context(), getUserID(c), req)
	if err != nil {
		handleServiceError(c, err)
		return
	}
	response.Created(c, "Account created successfully", account)
}

func (h *Handler) GetAccounts(c *gin.Context) {
	var isActive *bool
	if v := c.Query("is_active"); v != "" {
		b := v == "true"
		isActive = &b
	}

	accounts, err := h.service.GetAccounts(c.Request.Context(), getUserID(c), isActive)
	if err != nil {
		handleServiceError(c, err)
		return
	}
	response.OK(c, "Accounts fetched successfully", gin.H{"data": accounts, "total": len(accounts)})
}

func (h *Handler) GetAccount(c *gin.Context) {
	id, ok := getIDParam(c, "id")
	if !ok {
		return
	}
	account, err := h.service.GetAccountByID(c.Request.Context(), getUserID(c), id)
	if err != nil {
		handleServiceError(c, err)
		return
	}
	response.OK(c, "Account fetched successfully", account)
}

func (h *Handler) UpdateAccount(c *gin.Context) {
	id, ok := getIDParam(c, "id")
	if !ok {
		return
	}
	var req UpdateAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	account, err := h.service.UpdateAccount(c.Request.Context(), getUserID(c), id, req)
	if err != nil {
		handleServiceError(c, err)
		return
	}
	response.OK(c, "Account updated successfully", account)
}

func (h *Handler) DeleteAccount(c *gin.Context) {
	id, ok := getIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteAccount(c.Request.Context(), getUserID(c), id); err != nil {
		handleServiceError(c, err)
		return
	}
	response.OK(c, "Account deactivated", nil)
}

func (h *Handler) GetAccountSummary(c *gin.Context) {
	id, ok := getIDParam(c, "id")
	if !ok {
		return
	}
	var from, to *string
	if v := c.Query("from"); v != "" {
		from = &v
	}
	if v := c.Query("to"); v != "" {
		to = &v
	}
	summary, err := h.service.GetAccountSummary(c.Request.Context(), getUserID(c), id, from, to)
	if err != nil {
		handleServiceError(c, err)
		return
	}
	response.OK(c, "Account summary fetched", summary)
}

// =============================================================
// CATEGORIES
// =============================================================

func (h *Handler) CreateCategory(c *gin.Context) {
	var req CreateCategoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	category, err := h.service.CreateCategory(c.Request.Context(), getUserID(c), req)
	if err != nil {
		handleServiceError(c, err)
		return
	}
	response.Created(c, "Category created successfully", category)
}

func (h *Handler) GetCategories(c *gin.Context) {
	catType := c.Query("type")
	categories, err := h.service.GetCategories(c.Request.Context(), getUserID(c), catType)
	if err != nil {
		handleServiceError(c, err)
		return
	}
	response.OK(c, "Categories fetched successfully", gin.H{"data": categories})
}

func (h *Handler) UpdateCategory(c *gin.Context) {
	id, ok := getIDParam(c, "id")
	if !ok {
		return
	}
	var req UpdateCategoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	category, err := h.service.UpdateCategory(c.Request.Context(), getUserID(c), id, req)
	if err != nil {
		handleServiceError(c, err)
		return
	}
	response.OK(c, "Category updated successfully", category)
}

func (h *Handler) DeleteCategory(c *gin.Context) {
	id, ok := getIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteCategory(c.Request.Context(), getUserID(c), id); err != nil {
		if err.Error() == "category has child categories, delete or reparent them first" {
			response.BadRequest(c, "CATEGORY_HAS_CHILDREN", err.Error(), nil)
			return
		}
		handleServiceError(c, err)
		return
	}
	response.OK(c, "Category deactivated", nil)
}

// =============================================================
// TAGS
// =============================================================

func (h *Handler) CreateTag(c *gin.Context) {
	var req CreateTagRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	tag, err := h.service.CreateTag(c.Request.Context(), getUserID(c), req)
	if err != nil {
		handleServiceError(c, err)
		return
	}
	response.Created(c, "Tag created successfully", tag)
}

func (h *Handler) GetTags(c *gin.Context) {
	tags, err := h.service.GetTags(c.Request.Context(), getUserID(c))
	if err != nil {
		handleServiceError(c, err)
		return
	}
	response.OK(c, "Tags fetched successfully", gin.H{"data": tags})
}

func (h *Handler) UpdateTag(c *gin.Context) {
	id, ok := getIDParam(c, "id")
	if !ok {
		return
	}
	var req UpdateTagRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	tag, err := h.service.UpdateTag(c.Request.Context(), getUserID(c), id, req)
	if err != nil {
		handleServiceError(c, err)
		return
	}
	response.OK(c, "Tag updated successfully", tag)
}

func (h *Handler) DeleteTag(c *gin.Context) {
	id, ok := getIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteTag(c.Request.Context(), getUserID(c), id); err != nil {
		handleServiceError(c, err)
		return
	}
	response.OK(c, "Tag deleted", nil)
}

// =============================================================
// TRANSACTIONS
// =============================================================

func (h *Handler) CreateTransaction(c *gin.Context) {
	var req CreateTransactionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	tx, err := h.service.CreateTransaction(c.Request.Context(), getUserID(c), req)
	if err != nil {
		response.BadRequest(c, "TRANSACTION_ERROR", err.Error(), nil)
		return
	}
	response.Created(c, "Transaction recorded successfully", tx)
}

func (h *Handler) GetTransactions(c *gin.Context) {
	params := make(map[string]interface{})

	if v := c.Query("account_id"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			params["account_id"] = id
		}
	}
	if v := c.Query("category_id"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			params["category_id"] = id
		}
	}
	if v := c.Query("type"); v != "" {
		params["type"] = v
	}
	if v := c.Query("project_id"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			params["project_id"] = id
		}
	}
	if v := c.Query("from"); v != "" {
		params["from"] = v
	}
	if v := c.Query("to"); v != "" {
		params["to"] = v
	}
	if v := c.Query("sort"); v != "" {
		params["sort"] = v
	}
	if v := c.Query("page"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			params["page"] = p
		}
	}
	if v := c.Query("per_page"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			if p > 100 {
				p = 100
			}
			params["per_page"] = p
		}
	}

	transactions, total, err := h.service.GetTransactions(c.Request.Context(), getUserID(c), params)
	if err != nil {
		handleServiceError(c, err)
		return
	}

	page := 1
	perPage := 20
	if v, ok := params["page"]; ok {
		page = v.(int)
	}
	if v, ok := params["per_page"]; ok {
		perPage = v.(int)
	}
	totalPages := (total + perPage - 1) / perPage

	response.OK(c, "Transactions fetched successfully", gin.H{
		"data": transactions,
		"pagination": Pagination{
			Page:       page,
			PerPage:    perPage,
			Total:      total,
			TotalPages: totalPages,
		},
	})
}

func (h *Handler) GetTransaction(c *gin.Context) {
	id, ok := getIDParam(c, "id")
	if !ok {
		return
	}
	tx, err := h.service.GetTransactionByID(c.Request.Context(), getUserID(c), id)
	if err != nil {
		handleServiceError(c, err)
		return
	}
	response.OK(c, "Transaction fetched successfully", tx)
}

func (h *Handler) UpdateTransaction(c *gin.Context) {
	id, ok := getIDParam(c, "id")
	if !ok {
		return
	}
	var req UpdateTransactionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	tx, err := h.service.UpdateTransaction(c.Request.Context(), getUserID(c), id, req)
	if err != nil {
		handleServiceError(c, err)
		return
	}
	response.OK(c, "Transaction updated successfully", tx)
}

func (h *Handler) DeleteTransaction(c *gin.Context) {
	id, ok := getIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteTransaction(c.Request.Context(), getUserID(c), id); err != nil {
		if err.Error() == "cannot delete transaction linked to a shared expense" {
			response.BadRequest(c, "HAS_SHARED_EXPENSE", err.Error(), nil)
			return
		}
		handleServiceError(c, err)
		return
	}
	response.OK(c, "Transaction deleted", nil)
}

func (h *Handler) GetTransactionSummary(c *gin.Context) {
	from := c.Query("from")
	to := c.Query("to")
	if from == "" || to == "" {
		response.BadRequest(c, "VALIDATION_ERROR", "from and to date parameters are required", nil)
		return
	}
	groupBy := c.Query("group_by")
	var accountID *int64
	if v := c.Query("account_id"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			accountID = &id
		}
	}

	summary, err := h.service.GetTransactionSummary(c.Request.Context(), getUserID(c), from, to, accountID, groupBy)
	if err != nil {
		handleServiceError(c, err)
		return
	}
	response.OK(c, "Summary fetched successfully", summary)
}

func (h *Handler) AddTransactionTags(c *gin.Context) {
	id, ok := getIDParam(c, "id")
	if !ok {
		return
	}
	var req AddTagsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	tags, err := h.service.AddTransactionTags(c.Request.Context(), getUserID(c), id, req.TagIDs)
	if err != nil {
		handleServiceError(c, err)
		return
	}
	response.OK(c, "Tags added to transaction", gin.H{"transaction_id": id, "tags": tags})
}

func (h *Handler) RemoveTransactionTag(c *gin.Context) {
	txID, ok := getIDParam(c, "id")
	if !ok {
		return
	}
	tagID, err := strconv.ParseInt(c.Param("tag_id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "INVALID_ID", "Invalid tag ID", nil)
		return
	}
	if err := h.service.RemoveTransactionTag(c.Request.Context(), getUserID(c), txID, tagID); err != nil {
		handleServiceError(c, err)
		return
	}
	response.OK(c, "Tag removed from transaction", nil)
}
