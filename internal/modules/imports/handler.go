// Package imports — turning outside input into pending drafts: bank-slip
// images scanned from the phone's gallery, and free text typed in the
// Pending page's chat box (0.3.0).
//
// STUB (owner 2026-10-09): every endpoint validates the request, logs it
// and echoes the payload back. Nothing is parsed or turned into a draft
// yet. "Seen" slip files live in memory only (lost on restart) — just
// enough to walk the check → scan → check-again flow from the dev lab; the
// real version keeps them in a slip_imports table.
package imports

import (
	"log/slog"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/auth"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/response"
)

// maxSlipBytes — a phone screenshot of a slip is well under this.
const maxSlipBytes = 10 << 20

type Handler struct {
	log *slog.Logger

	mu   sync.Mutex
	seen map[uuid.UUID]map[string]bool // user → scanned file keys
}

func NewHandler(log *slog.Logger) *Handler {
	return &Handler{log: log, seen: map[uuid.UUID]map[string]bool{}}
}

// CheckRequest — POST /v1/slip-imports/check. One key per image found in
// the chosen albums ("name|size|modified").
type CheckRequest struct {
	Files []string `json:"files" binding:"required,min=1,max=500,dive,min=1,max=300"`
}

type CheckResponse struct {
	// Keys never scanned before — the ones the app should upload.
	NewFiles []string `json:"new_files"`
	// Keys already scanned — skipped.
	SeenFiles []string `json:"seen_files"`
}

// ParseTextRequest — POST /v1/pending-transactions/parse-text.
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

// POST /v1/slip-imports/check
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
	response.OK(c, "Slip check received (stub)", out)
}

// DELETE /v1/slip-imports — forget every scanned file ("ล้างประวัติการสแกน").
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
	response.OK(c, "Slip history cleared (stub)", gin.H{"forgot": n})
}

// POST /v1/pending-transactions/scan-slip — multipart: `image` (the slip),
// `file_key` (same key as in /check), `trans_ref` (from the slip's QR, when
// the phone could read it). Marks file_key as scanned.
func (h *Handler) ScanSlip(c *gin.Context) {
	uid, ok := userID(c)
	if !ok {
		return
	}
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
	key := c.PostForm("file_key")
	payload := gin.H{
		"file_key":     key,
		"trans_ref":    c.PostForm("trans_ref"),
		"filename":     file.Filename,
		"size":         file.Size,
		"content_type": file.Header.Get("Content-Type"),
	}
	if key != "" {
		h.mu.Lock()
		if h.seen[uid] == nil {
			h.seen[uid] = map[string]bool{}
		}
		h.seen[uid][key] = true
		h.mu.Unlock()
	}
	h.log.Info("imports: scan slip OK (stub)", "user_id", uid, "payload", payload)
	response.OK(c, "Slip received (stub)", payload)
}

// POST /v1/pending-transactions/parse-text
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
	response.OK(c, "Text received (stub)", req)
}
