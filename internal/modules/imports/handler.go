package imports

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/imports/ocr"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/imports/slip"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/providers"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const maxSlipBytes = 10 << 20

const scanDeadline = 90 * time.Second

type Handler struct {
	log  *slog.Logger
	ocr  ocr.Reader
	dump *ocrDumper // nil unless OCR_DUMP=1 — see ocr_dump.go
	// draftContext loads the user's wallets + fee category for the drafts; nil → drafts without
	// them.
	draftContext func(ctx context.Context, userID uuid.UUID) (DraftContext, error)
	// Import logs (migration 50): what the import met and can't handle yet.
	writeLog      func(ctx context.Context, l ImportLog) error
	providerKnown func(ctx context.Context, scheme, code string) (bool, error)
	resolve func(ctx context.Context, userID uuid.UUID, drafts []pending.Draft) ([]pending.DraftResolved, error)

	mu   sync.Mutex
	seen map[uuid.UUID]map[string]bool // user → scanned file keys
}

func NewHandler(log *slog.Logger, reader ocr.Reader) *Handler {
	return &Handler{log: log, ocr: reader, seen: map[uuid.UUID]map[string]bool{}}
}

type CheckRequest struct {
	Files []string `json:"files" binding:"required,min=1,max=500,dive,min=1,max=300"`
}

type CheckResponse struct {
	// Keys never scanned before — the ones the app should upload.
	NewFiles []string `json:"new_files"`
	SeenFiles []string `json:"seen_files"`
}

type ParseTextRequest struct {
	Text string `json:"text" binding:"required,min=1,max=500"`
}

func userID(c *gin.Context) (uuid.UUID, bool) {
	id, ok := auth.UserIDFromContext(c)
	if !ok {
		response.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "Unauthorized", nil)
	}
	return id, ok
}

func (h *Handler) CheckSlips(c *gin.Context) {
	uid, ok := userID(c)
	if !ok {
		return
	}
	var req CheckRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	out := CheckResponse{NewFiles: []string{}, SeenFiles: []string{}}
	h.mu.Lock()
	for _, f := range req.Files {
		if h.seen[uid][f] {
			out.SeenFiles = append(out.SeenFiles, f)
		} else {
			out.NewFiles = append(out.NewFiles, f)
		}
	}
	h.mu.Unlock()
	h.log.Info("imports: slip check OK (stub)",
		"user_id", uid, "sent", req.Files,
		"new", out.NewFiles, "seen", out.SeenFiles)
	c.JSON(http.StatusOK, out)
}

func (h *Handler) ResetSlips(c *gin.Context) {
	uid, ok := userID(c)
	if !ok {
		return
	}
	h.mu.Lock()
	n := len(h.seen[uid])
	delete(h.seen, uid)
	h.mu.Unlock()
	h.log.Info("imports: slip history cleared (stub)", "user_id", uid, "forgot", n)
	c.JSON(http.StatusOK, gin.H{"forgot": n})
}

func (h *Handler) ScanSlip(c *gin.Context) {
	uid, ok := userID(c)
	if !ok {
		return
	}
	rc := http.NewResponseController(c.Writer)
	_ = rc.SetReadDeadline(time.Now().Add(scanDeadline))
	_ = rc.SetWriteDeadline(time.Now().Add(scanDeadline))

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxSlipBytes+1<<20)
	file, err := c.FormFile("image")
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "image is required", nil)
		return
	}
	if file.Size > maxSlipBytes {
		response.BadRequest(c, "VALIDATION_ERROR", "image is larger than 10 MB", nil)
		return
	}
	f, err := file.Open()
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "image could not be read", nil)
		return
	}
	data, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", "image could not be read", nil)
		return
	}
	contentType := http.DetectContentType(data)
	if contentType != "image/png" && contentType != "image/jpeg" {
		response.BadRequest(c, "UNSUPPORTED_IMAGE", "image must be PNG or JPEG",
			gin.H{"detected": contentType})
		return
	}

	opt := ocr.Options{PSM: ocr.DefaultPSM, Preprocess: c.PostForm("preprocess") == "1"}
	if s := c.PostForm("psm"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || !ocr.AllowedPSM[n] {
			response.BadRequest(c, "VALIDATION_ERROR", "psm must be 3, 4, 6 or 11", nil)
			return
		}
		opt.PSM = n
	}

	key := c.PostForm("file_key")
	transRef := c.PostForm("trans_ref")
	bankCode := c.PostForm("bank_code") // from the slip's QR; picks the rule set
	res, err := h.ocr.Read(c.Request.Context(), data, opt)
	if err != nil {
		h.log.Warn("imports: scan slip OCR failed",
			"user_id", uid, "file_key", key, "size", file.Size, "err", err)
		switch {
		case errors.Is(err, ocr.ErrBadImage):
			response.BadRequest(c, "UNSUPPORTED_IMAGE", "image could not be decoded", nil)
		case errors.Is(err, ocr.ErrUnavailable):
			response.Fail(c, http.StatusServiceUnavailable, "OCR_UNAVAILABLE",
				"OCR is not installed on this server", nil)
		case errors.Is(err, ocr.ErrTimeout):
			response.Fail(c, http.StatusGatewayTimeout, "OCR_TIMEOUT",
				"Reading the slip took too long", nil)
		default:
			response.InternalError(c, "Reading the slip failed", nil)
		}
		return
	}

	if key != "" {
		h.mu.Lock()
		if h.seen[uid] == nil {
			h.seen[uid] = map[string]bool{}
		}
		h.seen[uid][key] = true
		h.mu.Unlock()
	}
	h.queueOCRDump(data, file.Filename, key, transRef, contentType, opt, res)

	out := ScanResult{
		V:        ScanResultVersion,
		FileKey:  key,
		TransRef: transRef,
		BankCode: bankCode,
		Pending:  []PendingItem{},
	}
	if c.PostForm("debug") == "1" {
		out.OCR = res
	}
	parsed, err := slip.Parse(bankCode, res)
	switch {
	case errors.Is(err, slip.ErrUnsupportedBank):
		out.Status = StatusUnsupportedBank
	case err != nil:
		response.InternalError(c, "Reading the slip failed", nil)
		return
	default:
		out.Slip = parsed
		out.Status = StatusOK
		if len(parsed.Missing) > 0 {
			out.Status = StatusIncomplete
		}
		var dc DraftContext
		if h.draftContext != nil {
			if dc, err = h.draftContext(c.Request.Context(), uid); err != nil {
				// Drafts still work without wallets / fee category.
				h.log.Warn("imports: draft context failed", "user_id", uid, "err", err)
				dc = DraftContext{}
			}
		}
		out.Pending = buildPending(parsed, transRef, bankCode, dc)
		if err := h.resolveItems(c.Request.Context(), uid, out.Pending); err != nil {
			response.InternalError(c, "Reading the slip failed", nil)
			return
		}
	}

	h.noteIssues(c.Request.Context(), uid, bankCode, transRef, out)

	// Never the text itself — slips carry names and account numbers.
	h.log.Info("imports: scan slip OK",
		"user_id", uid, "file_key", key, "trans_ref", transRef, "bank_code", bankCode,
		"status", out.Status, "drafts", len(out.Pending),
		"size", file.Size, "content_type", contentType,
		"psm", res.PSM, "ocr_ms", res.OCRMs,
		"conf", res.Conf, "lines", len(res.Lines), "chars", len([]rune(res.Text)))
	c.JSON(http.StatusOK, out)
}

func (h *Handler) ParseText(c *gin.Context) {
	uid, ok := userID(c)
	if !ok {
		return
	}
	var req ParseTextRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "VALIDATION_ERROR", err.Error(), nil)
		return
	}
	h.log.Info("imports: parse text OK (stub)", "user_id", uid, "text", req.Text)
	c.JSON(http.StatusOK, ParseTextResult{Pending: []PendingItem{}})
}

func (h *Handler) WithDraftContext(fn func(ctx context.Context, userID uuid.UUID) (DraftContext, error)) {
	h.draftContext = fn
}

func (h *Handler) WithResolver(fn func(ctx context.Context, userID uuid.UUID, drafts []pending.Draft) ([]pending.DraftResolved, error)) {
	h.resolve = fn
}

func (h *Handler) resolveItems(ctx context.Context, uid uuid.UUID, items []PendingItem) error {
	drafts := make([]pending.Draft, len(items))
	for i := range items {
		drafts[i] = items[i].Draft
	}
	var res []pending.DraftResolved
	if h.resolve != nil {
		var err error
		if res, err = h.resolve(ctx, uid, drafts); err != nil {
			return err
		}
	}
	for i := range items {
		if i < len(res) {
			items[i].Resolved = res[i]
		} else {
			items[i].Resolved = pending.DraftResolved{
				Tags: []shared.TagRef{}, Splits: []pending.ResolvedSplit{}, Missing: []string{},
			}
		}
	}
	return nil
}

func (h *Handler) WithImportLogs(
	write func(ctx context.Context, l ImportLog) error,
	known func(ctx context.Context, scheme, code string) (bool, error),
) {
	h.writeLog, h.providerKnown = write, known
}

func (h *Handler) noteIssues(ctx context.Context, uid uuid.UUID, bankCode, transRef string, out ScanResult) {
	if h.writeLog == nil {
		return
	}
	entry := ImportLog{UserID: uid, Scheme: providers.SchemeBOT, Code: bankCode, TransRef: transRef}
	switch {
	case bankCode != "" && h.providerKnown != nil && !h.known(ctx, bankCode):
		entry.Kind = LogUnknownProvider
		entry.Details = map[string]any{"status": out.Status}
	case out.Status == StatusUnsupportedBank:
		entry.Kind = LogUnsupportedBank
	case out.Status == StatusIncomplete && out.Slip != nil:
		entry.Kind = LogIncomplete
		entry.Details = map[string]any{"missing": out.Slip.Missing}
	default:
		return
	}
	if err := h.writeLog(ctx, entry); err != nil {
		h.log.Warn("imports: import log not written", "kind", entry.Kind, "err", err)
		return
	}
	h.log.Info("imports: import log written", "kind", entry.Kind, "user_id", uid, "bank_code", bankCode)
}

// known — a lookup failure counts as known (don't log noise for it).
func (h *Handler) known(ctx context.Context, code string) bool {
	ok, err := h.providerKnown(ctx, providers.SchemeBOT, code)
	return err != nil || ok
}
