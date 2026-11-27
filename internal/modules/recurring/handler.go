package recurring

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/institutions"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/saving_goals"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/apierr"
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
		response.Fail(c, http.StatusUnauthorized, apierr.Unauthorized, "Unauthorized", nil)
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
		response.Validation(c, "Invalid id", apierr.Field("id", apierr.FieldInvalid))
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
		response.BindError(c, err)
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
		response.Fail(c, http.StatusConflict, apierr.ItemAlreadyLinked, "Some items can't take this action", gin.H{"ids": conflict.IDs})
	case errors.As(err, &link):
		response.Fail(c, http.StatusConflict, apierr.ItemAlreadyLinked, err.Error(), gin.H{"transaction": link.Linked, "other": link.Other})
	case errors.Is(err, ErrAmountRequired):
		response.BadRequest(c, apierr.AmountRequired, err.Error(), nil)
	case errors.Is(err, ErrNothingOwed):
		response.Fail(c, http.StatusConflict, apierr.NothingOwed, err.Error(), nil)
	case errors.Is(err, ErrAlreadyRecorded):
		response.Fail(c, http.StatusConflict, apierr.AlreadyRecorded, err.Error(), nil)
	case errors.Is(err, ErrNotFound):
		response.NotFound(c, apierr.NotFound, "Recurring item not found")
	case errors.Is(err, ErrLineNotFound):
		response.NotFound(c, apierr.RecurringLineNotFound, err.Error())
	case errors.Is(err, ErrLastLine):
		response.Fail(c, http.StatusConflict, apierr.RecurringLastLine, err.Error(), nil)
	case errors.Is(err, ErrSourceInvalid):
		response.BadRequest(c, apierr.RecurringSourceInvalid, err.Error(), nil)
	case errors.Is(err, ErrPlanInvalid):
		response.Fail(c, http.StatusBadRequest, apierr.PlanInvalid, "Invalid plan", apierr.FieldsOf(err))
	case errors.Is(err, ErrPlanNotFound):
		response.NotFound(c, apierr.NotFound, err.Error())
	case errors.Is(err, ErrInstallmentNotFound):
		response.NotFound(c, apierr.InstallmentNotFound, err.Error())
	case errors.Is(err, institutions.ErrNotFound), errors.Is(err, institutions.ErrInvalidState):
		response.BadRequest(c, apierr.InstitutionNotFound, "Institution not found", nil)
	case errors.Is(err, ErrTxNotFound):
		response.NotFound(c, apierr.NotFound, "Transaction not found")
	case errors.Is(err, ErrItemNotFound):
		response.NotFound(c, apierr.NotFound, "Recurring item not found")
	case errors.Is(err, ErrValidation):
		response.ValidationErr(c, err)
	case errors.Is(err, ErrInvalidRule):
		response.BadRequest(c, apierr.InvalidRule, err.Error(), nil)
	case errors.Is(err, ErrEndTooFar):
		response.BadRequest(c, apierr.EndTooFar, err.Error(), nil)
	case errors.Is(err, transactions.ErrAccountArchived):
		response.Fail(c, http.StatusConflict, apierr.AccountArchived, err.Error(), nil)
	case errors.Is(err, ErrAccountNotFound):
		response.BadRequest(c, apierr.AccountNotFound, err.Error(), nil)
	case errors.Is(err, ErrInvalidCategory):
		response.BadRequest(c, apierr.InvalidCategory, err.Error(), nil)
	case errors.Is(err, ErrTagNotFound):
		response.BadRequest(c, apierr.TagNotFound, err.Error(), nil)
	case errors.Is(err, ErrContactNotFound):
		response.BadRequest(c, apierr.ContactNotFound, err.Error(), nil)
	case errors.Is(err, ErrContactArchived):
		response.BadRequest(c, apierr.ContactArchived, err.Error(), nil)
	case errors.Is(err, ErrInvalidTransition):
		response.Fail(c, http.StatusConflict, apierr.InvalidTransition, err.Error(), nil)
	case errors.Is(err, ErrSavingNeedsWallet):
		response.BadRequest(c, apierr.SavingNeedsWallet, err.Error(), nil)
	case errors.Is(err, saving_goals.ErrAccountNotFound):
		response.BadRequest(c, apierr.AccountNotFound, err.Error(), nil)
	case errors.Is(err, saving_goals.ErrValidation):
		response.ValidationErr(c, err)
	case errors.Is(err, saving_goals.ErrInsufficientSaving):
		response.Fail(c, http.StatusConflict, apierr.InsufficientSaving, err.Error(), nil)
	case errors.Is(err, saving_goals.ErrWalletNotTracked):
		response.BadRequest(c, apierr.WalletNotTracked, err.Error(), nil)
	case errors.Is(err, saving_goals.ErrAutoSaveWallet):
		response.Fail(c, http.StatusConflict, apierr.InvalidTransition, err.Error(), nil)
	case errors.Is(err, saving_goals.ErrGoalNotFound):
		response.NotFound(c, apierr.NotFound, "Saving goal not found")
	case errors.Is(err, saving_goals.ErrGoalNotActive):
		response.Fail(c, http.StatusConflict, apierr.GoalNotActive, err.Error(), nil)
	case errors.Is(err, saving_goals.ErrGoalReached):
		response.Fail(c, http.StatusConflict, apierr.GoalReached, err.Error(), nil)
	case errors.Is(err, ErrAlreadyLinked):
		response.Fail(c, http.StatusConflict, apierr.ItemAlreadyLinked, err.Error(), nil)
	default:
		transactions.MapError(c, err)
	}
}

func (h *Handler) Items(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	month := c.Query("month") // calendar: the items of one month (YYYY-MM), every status, oldest first
	if month != "" {
		if _, err := time.Parse("2006-01", month); err != nil {
			response.Validation(c, "month must be YYYY-MM", apierr.Field("month", apierr.FieldInvalid, "format", "month"))
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
		response.Validation(c, "Invalid status", apierr.Field("status", apierr.FieldInvalid))
		return
	}
	kind := c.DefaultQuery("kind", "all")
	if kind != "all" && kind != "recurring" {
		response.Validation(c, "Invalid kind", apierr.Field("kind", apierr.FieldInvalid))
		return
	}
	ruleID, ok := optionalID(c, "recurring_rule_id") // one line
	if !ok {
		return
	}
	itemID, ok := optionalID(c, "recurring_id") // every line of an item
	if !ok {
		return
	}
	out, err := h.service.Items(c.Request.Context(), uid, status, kind, month, ruleID, itemID,
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

// optionalID — an optional uuid query parameter.
func optionalID(c *gin.Context, name string) (*uuid.UUID, bool) {
	v := c.Query(name)
	if v == "" {
		return nil, true
	}
	id, err := uuid.Parse(v)
	if err != nil {
		response.Validation(c, "Invalid "+name, apierr.Field(name, apierr.FieldInvalid))
		return nil, false
	}
	return &id, true
}

// ruleAndSlot reads /recurring/:id/rules/:rule_id/occurrences/:slot (0.4.3) and answers the LINE id,
// checked to be a line of the item.
func (h *Handler) ruleAndSlot(c *gin.Context) (uuid.UUID, uuid.UUID, time.Time, bool) {
	uid, id, line, ok := itemAndLine(c)
	if !ok {
		return uuid.Nil, uuid.Nil, time.Time{}, false
	}
	slot, err := time.Parse(dateLayout, c.Param("slot"))
	if err != nil {
		response.Validation(c, "slot must be YYYY-MM-DD", apierr.Field("slot", apierr.FieldInvalid, "format", "date"))
		return uuid.Nil, uuid.Nil, time.Time{}, false
	}
	if err := lineOfItem(c.Request.Context(), h.service.db, uid, id, line); err != nil {
		mapError(c, err)
		return uuid.Nil, uuid.Nil, time.Time{}, false
	}
	return uid, line, slot, true
}

func (h *Handler) EditOcc(c *gin.Context) {
	uid, id, slot, ok := h.ruleAndSlot(c)
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
	uid, id, slot, ok := h.ruleAndSlot(c)
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
	uid, id, slot, ok := h.ruleAndSlot(c)
	if !ok {
		return
	}
	out, err := h.service.SkipOcc(c.Request.Context(), uid, id, slot, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) RestoreOcc(c *gin.Context) {
	uid, id, slot, ok := h.ruleAndSlot(c)
	if !ok {
		return
	}
	out, err := h.service.RestoreOcc(c.Request.Context(), uid, id, slot, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) MarkPaidOcc(c *gin.Context) {
	uid, id, slot, ok := h.ruleAndSlot(c)
	if !ok {
		return
	}
	var req MarkPaidRequest
	if _, ok := bind(c, &req); !ok {
		return
	}
	out, err := h.service.MarkPaid(c.Request.Context(), uid, id, slot, req.Amount, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) UnmarkPaidOcc(c *gin.Context) {
	uid, id, slot, ok := h.ruleAndSlot(c)
	if !ok {
		return
	}
	out, err := h.service.UnmarkPaid(c.Request.Context(), uid, id, slot, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) LinkOcc(c *gin.Context) {
	uid, id, slot, ok := h.ruleAndSlot(c)
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
	uid, id, slot, ok := h.ruleAndSlot(c)
	if !ok {
		return
	}
	out, err := h.service.UnlinkOcc(c.Request.Context(), uid, id, slot, tz(c))
	reply(c, http.StatusOK, out, err)
}

func (h *Handler) SettleOccurrences(c *gin.Context) {
	uid, ok := user(c)
	if !ok {
		return
	}
	var req OccBulkRequest
	if _, ok := bind(c, &req); !ok {
		return
	}
	out, err := h.service.SettleOccurrences(c.Request.Context(), uid, req, tz(c))
	reply(c, http.StatusOK, out, err)
}
