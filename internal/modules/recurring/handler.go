package recurring

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
	"github.com/ppChub722/chubi-pocket-be/internal/modules/saving_goals"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
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
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid id", nil)
		return uuid.Nil, uuid.Nil, false
	}
	return uid, id, true
}

func presence(raw []byte) (map[string]json.RawMessage, error) {
	m := map[string]json.RawMessage{}
	if len(raw) == 0 {
		return m, nil
	}
	return m, json.Unmarshal(raw, &m)
}

func markFields(f *Fields, keys map[string]json.RawMessage) {
	f.present = map[string]bool{}
	for k := range keys {
		f.present[k] = true
	}
}

func bind(c *gin.Context, v any) (map[string]json.RawMessage, bool) {
	raw, err := c.GetRawData()
	if err == nil {
		if len(raw) == 0 {
			raw = []byte("{}")
		}
		err = json.Unmarshal(raw, v)
	}
	var keys map[string]json.RawMessage
	if err == nil {
		keys, err = presence(raw)
	}
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return nil, false
	}
	return keys, true
}

func reply(c *gin.Context, status int, out any, err error) {
	if err != nil {
		mapError(c, err)
		return
	}
	c.JSON(status, out)
}

func mapError(c *gin.Context, err error) {
	var conflict *ConflictError
	var link *LinkConflict
	switch {
	case errors.As(err, &conflict):
		response.Fail(c, http.StatusConflict, "ITEM_ALREADY_LINKED", "Some items can't take this action", gin.H{"ids": conflict.IDs})
	case errors.As(err, &link):
		response.Fail(c, http.StatusConflict, "ITEM_ALREADY_LINKED", err.Error(), gin.H{"transaction": link.Linked, "other": link.Other})
	case errors.Is(err, ErrAutoCreateNeedsAmount):
		response.BadRequest(c, "AUTO_CREATE_NEEDS_AMOUNT", err.Error(), nil)
	case errors.Is(err, ErrAutoCreateNeedsWallet):
		response.BadRequest(c, "AUTO_CREATE_NEEDS_WALLET", err.Error(), nil)
	case errors.Is(err, ErrAmountRequired):
		response.BadRequest(c, "AMOUNT_REQUIRED", err.Error(), nil)
	case errors.Is(err, ErrAlreadyRecorded):
		response.Fail(c, http.StatusConflict, "ALREADY_RECORDED", err.Error(), nil)
	case errors.Is(err, ErrNotFound):
		response.NotFound(c, "NOT_FOUND", "Recurring setting not found")
	case errors.Is(err, ErrTxNotFound):
		response.NotFound(c, "NOT_FOUND", "Transaction not found")
	case errors.Is(err, ErrItemNotFound):
		response.NotFound(c, "NOT_FOUND", "Recurring item not found")
	case errors.Is(err, ErrValidation):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	case errors.Is(err, ErrInvalidRule):
		response.BadRequest(c, "INVALID_RULE", err.Error(), nil)
	case errors.Is(err, ErrEndTooFar):
		response.BadRequest(c, "END_TOO_FAR", err.Error(), nil)
	case errors.Is(err, transactions.ErrAccountArchived):
		response.Fail(c, http.StatusConflict, "ACCOUNT_ARCHIVED", err.Error(), nil)
	case errors.Is(err, ErrAccountNotFound):
		response.BadRequest(c, "ACCOUNT_NOT_FOUND", err.Error(), nil)
	case errors.Is(err, ErrInvalidCategory):
		response.BadRequest(c, "INVALID_CATEGORY", err.Error(), nil)
	case errors.Is(err, ErrTagNotFound):
		response.BadRequest(c, "TAG_NOT_FOUND", err.Error(), nil)
	case errors.Is(err, ErrInvalidTransition):
		response.Fail(c, http.StatusConflict, "INVALID_TRANSITION", err.Error(), nil)
	case errors.Is(err, ErrSavingNeedsWallet):
		response.BadRequest(c, "SAVING_NEEDS_WALLET", err.Error(), nil)
	case errors.Is(err, saving_goals.ErrAccountNotFound):
		response.BadRequest(c, "ACCOUNT_NOT_FOUND", err.Error(), nil)
	case errors.Is(err, saving_goals.ErrValidation):
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
	case errors.Is(err, saving_goals.ErrInsufficientSaving):
		response.Fail(c, http.StatusConflict, "INSUFFICIENT_SAVING", err.Error(), nil)
	case errors.Is(err, saving_goals.ErrWalletNotTracked):
		response.BadRequest(c, "WALLET_NOT_TRACKED", err.Error(), nil)
	case errors.Is(err, saving_goals.ErrGoalNotFound):
		response.NotFound(c, "NOT_FOUND", "Saving goal not found")
	case errors.Is(err, saving_goals.ErrGoalNotActive):
		response.Fail(c, http.StatusConflict, "GOAL_NOT_ACTIVE", err.Error(), nil)
	case errors.Is(err, saving_goals.ErrGoalReached):
		response.Fail(c, http.StatusConflict, "GOAL_REACHED", err.Error(), nil)
	case errors.Is(err, ErrAlreadyLinked):
		response.Fail(c, http.StatusConflict, "ITEM_ALREADY_LINKED", err.Error(), nil)
	default:
		transactions.MapError(c, err)
	}
}

func (h *Handler) List(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	status := c.DefaultQuery("status", "current")
	if status != "current" && status != "archived" && status != "all" {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid status", nil)
		return
	}
	forType := c.Query("for_type")
	if forType != "" && forType != "expense" && forType != "income" && forType != "transfer" {
		response.BadRequest(c, "VALIDATION_ERROR", "for_type must be expense, income or transfer", nil)
		return
	}
	var hint PickerHint
	if v := c.Query("category_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			response.BadRequest(c, "VALIDATION_ERROR", "category_id must be a uuid", nil)
			return
		}
		hint.CategoryID = &id
	}
	if v := c.Query("amount"); v != "" {
		a, err := strconv.ParseFloat(v, 64)
		if err != nil || a <= 0 {
			response.BadRequest(c, "VALIDATION_ERROR", "amount must be a positive number", nil)
			return
		}
		hint.Amount = &a
	}
	kind := c.Query("kind")
	if kind != "" && !validKind(kind) {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid kind", nil)
		return
	}
	out, err := h.service.List(c.Request.Context(), uid, status, c.Query("q"), forType, hint, kind,
		atoiOr(c.Query("page"), 1), atoiOr(c.Query("per_page"), 20), tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Create(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	var req CreateRequest
	keys, ok := bind(c, &req)
	if !ok {
		return
	}
	markFields(&req.Fields, keys)
	out, err := h.service.Create(c.Request.Context(), uid, req, tz(c))
	reply(c, http.StatusCreated, out, err)
}

func (h *Handler) Get(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.Get(c.Request.Context(), uid, id,
		atoiOr(c.Query("page"), 1), atoiOr(c.Query("per_page"), 20), tz(c), strings.Contains(","+c.Query("include")+",", ",delete_preview,"))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Update(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	var req UpdateRequest
	keys, ok := bind(c, &req)
	if !ok {
		return
	}
	markFields(&req.Fields, keys)
	out, err := h.service.Update(c.Request.Context(), uid, id, req, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Delete(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.Delete(c.Request.Context(), uid, id, c.Query("delete_linked_txs") == "true")
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) RuleItems(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	tab := c.DefaultQuery("tab", "produced")
	if tab != "produced" && tab != "upcoming" {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid tab", nil)
		return
	}
	var from *time.Time
	if v := c.Query("from"); v != "" {
		d, err := time.Parse(dateLayout, v)
		if err != nil {
			response.BadRequest(c, "VALIDATION_ERROR", "from must be YYYY-MM-DD", nil)
			return
		}
		from = &d
	}
	out, err := h.service.RuleItems(c.Request.Context(), uid, id, tab,
		atoiOr(c.Query("page"), 1), atoiOr(c.Query("per_page"), 20), from, atoiOr(c.Query("months"), 12), tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Pause(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.Pause(c.Request.Context(), uid, id, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Resume(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.Resume(c.Request.Context(), uid, id, tz(c))
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

func (h *Handler) Extend(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	var req ExtendRequest
	if _, ok := bind(c, &req); !ok {
		return
	}
	out, err := h.service.Extend(c.Request.Context(), uid, id, req, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) Items(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	month := c.Query("month") // calendar: the items of one month (YYYY-MM), every status, oldest first
	if month != "" {
		if _, err := time.Parse("2006-01", month); err != nil {
			response.BadRequest(c, "VALIDATION_ERROR", "month must be YYYY-MM", nil)
			return
		}
	}
	defStatus := StatusUpcoming
	if month != "" {
		defStatus = "all"
	}
	status := c.Query("status")
	if status == "" {
		status = c.DefaultQuery("state", defStatus)
	}
	switch status {
	case StatusUpcoming, StatusDue, StatusPending, StatusDone, StatusPaid, StatusSkipped, StatusNotCreated, StatusArchived, "all":
	default:
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid status", nil)
		return
	}
	kind := c.DefaultQuery("kind", "all")
	if kind != "all" && kind != "recurring" {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid kind", nil)
		return
	}
	ruleID, ok := optionalRuleID(c)
	if !ok {
		return
	}
	out, err := h.service.Items(c.Request.Context(), uid, status, kind, month, ruleID,
		atoiOr(c.Query("page"), 1), atoiOr(c.Query("per_page"), 20), tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) CreateItem(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	var req ItemCreateRequest
	keys, ok := bind(c, &req)
	if !ok {
		return
	}
	markFields(&req.Fields, keys)
	out, err := h.service.CreateItem(c.Request.Context(), uid, req, tz(c))
	reply(c, http.StatusCreated, out, err)
}

func (h *Handler) GetItem(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.GetItem(c.Request.Context(), uid, id)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) UpdateItem(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	var req ItemUpdateRequest
	keys, ok := bind(c, &req)
	if !ok {
		return
	}
	markFields(&req.Fields, keys)
	out, err := h.service.UpdateItem(c.Request.Context(), uid, id, req, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) DeleteItem(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.DeleteItem(c.Request.Context(), uid, id, c.Query("delete_linked_tx") == "true")
	reply(c, http.StatusOK, out, err)
}

func nestedFields(f *Fields, keys map[string]json.RawMessage) error {
	if f == nil {
		return nil
	}
	inner, err := presence(keys["fields"])
	if err != nil {
		return err
	}
	markFields(f, inner)
	return nil
}

func (h *Handler) ArchiveItem(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.ArchiveItem(c.Request.Context(), uid, id)
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) RestoreItem(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.RestoreItem(c.Request.Context(), uid, id)
	reply(c, http.StatusOK, out, err)
}

func optionalRuleID(c *gin.Context) (*uuid.UUID, bool) {
	v := c.Query("rule_id")
	if v == "" {
		return nil, true
	}
	id, err := uuid.Parse(v)
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "Invalid rule_id", nil)
		return nil, false
	}
	return &id, true
}

// ruleAndSlot reads /recurring/:id/occurrences/:slot.
func ruleAndSlot(c *gin.Context) (uuid.UUID, uuid.UUID, time.Time, bool) {
	uid, id, ok := userAndID(c)
	if !ok {
		return uuid.Nil, uuid.Nil, time.Time{}, false
	}
	slot, err := time.Parse(dateLayout, c.Param("slot"))
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "slot must be YYYY-MM-DD", nil)
		return uuid.Nil, uuid.Nil, time.Time{}, false
	}
	return uid, id, slot, true
}

func (h *Handler) Occurrences(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	var txDate *time.Time
	if v := c.Query("tx_date"); v != "" {
		d, err := time.Parse(dateLayout, v)
		if err != nil {
			response.BadRequest(c, "VALIDATION_ERROR", "tx_date must be YYYY-MM-DD", nil)
			return
		}
		txDate = &d
	}
	out, err := h.service.Occurrences(c.Request.Context(), uid, id, txDate, atoiOr(c.Query("past"), 3), atoiOr(c.Query("upcoming"), 3), tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) EditOcc(c *gin.Context) {
	uid, id, slot, ok := ruleAndSlot(c)
	if !ok {
		return
	}
	var req ItemUpdateRequest
	keys, ok := bind(c, &req)
	if !ok {
		return
	}
	markFields(&req.Fields, keys)
	out, err := h.service.EditOcc(c.Request.Context(), uid, id, slot, req, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) ConfirmOcc(c *gin.Context) {
	uid, id, slot, ok := ruleAndSlot(c)
	if !ok {
		return
	}
	var req ConfirmRequest
	keys, ok := bind(c, &req)
	if !ok {
		return
	}
	markFields(&req.Fields, keys)
	out, err := h.service.Confirm(c.Request.Context(), uid, id, slot, req, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) SkipOcc(c *gin.Context) {
	uid, id, slot, ok := ruleAndSlot(c)
	if !ok {
		return
	}
	var req SkipRequest
	if _, ok := bind(c, &req); !ok {
		return
	}
	out, err := h.service.SkipOcc(c.Request.Context(), uid, id, slot, req.DeleteTx, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) RestoreOcc(c *gin.Context) {
	uid, id, slot, ok := ruleAndSlot(c)
	if !ok {
		return
	}
	out, err := h.service.RestoreOcc(c.Request.Context(), uid, id, slot, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) MarkPaidOcc(c *gin.Context) {
	uid, id, slot, ok := ruleAndSlot(c)
	if !ok {
		return
	}
	out, err := h.service.MarkPaid(c.Request.Context(), uid, id, slot, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) UnmarkPaidOcc(c *gin.Context) {
	uid, id, slot, ok := ruleAndSlot(c)
	if !ok {
		return
	}
	out, err := h.service.UnmarkPaid(c.Request.Context(), uid, id, slot, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) LinkOcc(c *gin.Context) {
	uid, id, slot, ok := ruleAndSlot(c)
	if !ok {
		return
	}
	var req LinkSlotRequest
	if _, ok := bind(c, &req); !ok {
		return
	}
	out, err := h.service.LinkOcc(c.Request.Context(), uid, id, slot, req, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) UnlinkOcc(c *gin.Context) {
	uid, id, slot, ok := ruleAndSlot(c)
	if !ok {
		return
	}
	out, err := h.service.UnlinkOcc(c.Request.Context(), uid, id, slot, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) ConfirmReady(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.ConfirmReady(c.Request.Context(), uid, id, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) MarkPaidReady(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.MarkPaidReady(c.Request.Context(), uid, id, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) SkipReady(c *gin.Context) {
	uid, id, ok := userAndID(c)
	if !ok {
		return
	}
	out, err := h.service.SkipReady(c.Request.Context(), uid, id, tz(c))
	reply(c, http.StatusOK, out, err)
}
