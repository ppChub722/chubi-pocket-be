package imports

import (
	"time"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/imports/ocr"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/imports/slip"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/pending"
)

const ScanResultVersion = 1

type Status string

const (
	StatusOK              Status = "ok"
	StatusIncomplete      Status = "incomplete"
	StatusUnsupportedBank Status = "unsupported_bank"
)

type ScanResult struct {
	V        int           `json:"v"`
	Status   Status        `json:"status"`
	FileKey  string        `json:"file_key"`
	TransRef string        `json:"trans_ref"`
	BankCode string        `json:"bank_code"`
	Slip     *slip.Slip    `json:"slip"`
	Pending  []PendingItem `json:"pending"`
	OCR *ocr.Result `json:"ocr,omitempty"`
}

type PendingItem struct {
	Source    string                `json:"source"` // "ocr"
	Kind      string                `json:"kind"`   // "create"
	Draft     pending.Draft         `json:"draft"`
	Resolved  pending.DraftResolved `json:"resolved"`
	SourceRef SlipRef               `json:"source_ref"`
}

// SlipRef — the row's source_ref: what the draft can't hold (the time, the slip's ref, payee,
// masked numbers) and how each guess was made.
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

type ParseTextResult struct {
	Pending []PendingItem `json:"pending"`
}
