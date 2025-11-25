package accounting

import (
	"github.com/gin-gonic/gin"
	"github.com/ppChub722/finna-bbear-be/internal/platform/response"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// Helper to get UserID from context (set by Auth Middleware)
func getUserID(c *gin.Context) int64 {
	id, exists := c.Get("userID")
	if !exists {
		return 0
	}
	return id.(int64)
}

// ---------------------------------------------------------
// 1. ACCOUNTS (Wallets)
// ---------------------------------------------------------

// CreateAccount handles POST /api/v1/accounts
func (h *Handler) CreateAccount(c *gin.Context) {
	var req CreateAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "INVALID_INPUT", err.Error(), nil)
		return
	}

	userID := getUserID(c)
	account, err := h.service.CreateAccount(c.Request.Context(), userID, req)
	if err != nil {
		response.InternalError(c, "Failed to create account", err.Error())
		return
	}

	response.Created(c, "Account created successfully", account)
}

// GetAccounts handles GET /api/v1/accounts
func (h *Handler) GetAccounts(c *gin.Context) {
	userID := getUserID(c)
	accounts, err := h.service.GetMyAccounts(c.Request.Context(), userID)
	if err != nil {
		response.InternalError(c, "Failed to fetch accounts", err.Error())
		return
	}

	response.OK(c, "Accounts fetched successfully", accounts)
}

// ---------------------------------------------------------
// 2. CATEGORIES
// ---------------------------------------------------------

// CreateCategory handles POST /api/v1/categories
func (h *Handler) CreateCategory(c *gin.Context) {
	var req CreateCategoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "INVALID_INPUT", err.Error(), nil)
		return
	}

	userID := getUserID(c)
	category, err := h.service.CreateCategory(c.Request.Context(), userID, req)
	if err != nil {
		response.InternalError(c, "Failed to create category", err.Error())
		return
	}

	response.Created(c, "Category created successfully", category)
}

// GetCategories handles GET /api/v1/categories
func (h *Handler) GetCategories(c *gin.Context) {
	userID := getUserID(c)
	categories, err := h.service.GetMyCategories(c.Request.Context(), userID)
	if err != nil {
		response.InternalError(c, "Failed to fetch categories", err.Error())
		return
	}

	response.OK(c, "Categories fetched successfully", categories)
}

// ---------------------------------------------------------
// 3. TRANSACTIONS
// ---------------------------------------------------------

// CreateTransaction handles POST /api/v1/transactions
func (h *Handler) CreateTransaction(c *gin.Context) {
	var req CreateTransactionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "INVALID_INPUT", err.Error(), nil)
		return
	}

	userID := getUserID(c)
	tx, err := h.service.CreateTransaction(c.Request.Context(), userID, req)
	if err != nil {
		response.InternalError(c, "Failed to record transaction", err.Error())
		return
	}

	response.Created(c, "Transaction recorded successfully", tx)
}

// GetTransactions handles GET /api/v1/transactions
func (h *Handler) GetTransactions(c *gin.Context) {
	userID := getUserID(c)
	transactions, err := h.service.GetMyTransactions(c.Request.Context(), userID)
	if err != nil {
		response.InternalError(c, "Failed to fetch transactions", err.Error())
		return
	}

	response.OK(c, "Transactions fetched successfully", transactions)
}