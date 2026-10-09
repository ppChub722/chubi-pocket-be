package imports

import (
	"time"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/imports/ocr"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/imports/slip"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
)

// ScanResultVersion — bump on a breaking change to ScanResult.
const ScanResultVersion = 1

// Status of one scanned image (spec §4). `not_slip` is decided on the
// phone (no slip QR → never uploaded); `duplicate` comes with 0.3.3.
type Status string

const (
	StatusOK              Status = "ok"
	StatusIncomplete      Status = "incomplete"
	StatusUnsupportedBank Status = "unsupported_bank"
)

// ScanResult — what scan-slip answers for one image: what was read
// (Slip) and the pending drafts it becomes (Pending, saved in 0.3.3).
// Unknown → key absent.
type ScanResult struct {
	V        int           `json:"v"`
	Status   Status        `json:"status"`
	FileKey  string        `json:"file_key,omitempty"`
	TransRef string        `json:"trans_ref,omitempty"`
	BankCode string        `json:"bank_code,omitempty"`
	Slip     *slip.Slip    `json:"slip,omitempty"`
	Pending  []PendingItem `json:"pending"`
	// Only with debug=1 (the Imports lab).
	OCR *ocr.Result `json:"ocr,omitempty"`
}

// PendingItem — one pending_transactions row, as-is.
type PendingItem struct {
	Source    string        `json:"source"` // "ocr"
	Kind      string        `json:"kind"`   // "create"
	Draft     pending.Draft `json:"draft"`
	SourceRef SlipRef       `json:"source_ref"`
}

// SlipRef — the row's source_ref: what the draft can't hold (the time,
// the slip's ref, payee, masked numbers) and how each guess was made.
// Display only; submit never reads it.
type SlipRef struct {
	Type           string     `json:"type"` // "slip"
	Part           string     `json:"part"` // main | fee
	TransRef       string     `json:"trans_ref,omitempty"`
	BankCode       string     `json:"bank_code,omitempty"`
	SlipKind       slip.Kind  `json:"slip_kind,omitempty"`
	OccurredAt     *time.Time `json:"occurred_at,omitempty"`
	Payee          string     `json:"payee,omitempty"`
	Memo           string     `json:"memo,omitempty"`
	SenderMasked   string     `json:"sender_masked,omitempty"`
	ReceiverMasked string     `json:"receiver_masked,omitempty"`
	Guessed        Guessed    `json:"guessed"`
}

// Guessed — where the draft's type / wallet / category came from.
type Guessed struct {
	Type     string `json:"type"`     // identifiers | default
	Account  string `json:"account"`  // identifier | none
	Category string `json:"category"` // fee_setting | none
}

const (
	sourceOCR  = "ocr"
	kindCreate = "create"
	partMain   = "main"
	partFee    = "fee"
)
