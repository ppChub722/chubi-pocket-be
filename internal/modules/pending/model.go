package pending

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

// Pending transactions — drafts waiting to be confirmed (owner design
// 2026-10-08). A draft is not a transaction: it never moves a balance or
// shows in reports. Any field may be empty while it waits; the full
// transaction rules apply only on submit.

// Kinds — what submitting a draft does.
const (
	KindCreate     = "create"      // a new transaction
	KindSettleDebt = "settle_debt" // a new transaction that settles TargetDebtID
	KindUpdateTx   = "update_tx"   // edits TargetTransactionID
)

// SourceManual — typed in by the user. Other sources (split_paid,
// project_copy, project_update, ocr, chat) arrive with their features.
const SourceManual = "manual"

// Draft — the POST /transactions body plus tag ids; every key optional.
type Draft struct {
	Type                *string                   `json:"type,omitempty"`
	Amount              *float64                  `json:"amount,omitempty"`
	AccountID           *uuid.UUID                `json:"account_id,omitempty"`
	CategoryID          *uuid.UUID                `json:"category_id,omitempty"`
	Date                *string                   `json:"date,omitempty"`
	Description         *string                   `json:"description,omitempty" binding:"omitempty,max=200"`
	Note                *string                   `json:"note,omitempty"        binding:"omitempty,max=500"`
	TransferToAccountID *uuid.UUID                `json:"transfer_to_account_id,omitempty"`
	TagIDs              []uuid.UUID               `json:"tag_ids,omitempty"`
	Splits              []transactions.SplitInput `json:"splits,omitempty"`
}

// SubmitError — why a draft didn't go through (kept on the row).
type SubmitError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type PendingTransaction struct {
	ID                  uuid.UUID       `json:"id"`
	Source              string          `json:"source"`
	Kind                string          `json:"kind"`
	Draft               Draft           `json:"draft"`
	TargetDebtID        *uuid.UUID      `json:"target_debt_id"`
	TargetTransactionID *uuid.UUID      `json:"target_transaction_id"`
	SourceRef           json.RawMessage `json:"source_ref"`
	LastError           *SubmitError    `json:"last_error"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

type ListResponse struct {
	Data  []PendingTransaction `json:"data"`
	Count int                  `json:"count"`
}

// CreateRequest — POST /pending-transactions: one or many manual drafts.
type CreateRequest struct {
	Items []CreateItem `json:"items" binding:"required,min=1,max=100,dive"`
}

type CreateItem struct {
	Draft Draft `json:"draft"`
}

// UpdateRequest — PUT /pending-transactions/:id replaces the draft.
type UpdateRequest struct {
	Draft Draft `json:"draft"`
}

// SubmitRequest — POST /pending-transactions/submit.
type SubmitRequest struct {
	IDs []uuid.UUID `json:"ids" binding:"required,min=1,max=100"`
}

type Submitted struct {
	ID            uuid.UUID  `json:"id"`
	TransactionID *uuid.UUID `json:"transaction_id"`
}

type Failed struct {
	ID    uuid.UUID   `json:"id"`
	Error SubmitError `json:"error"`
}

// SubmitResponse — each draft independently: the ones that went through
// are gone from pending; the ones that failed stay with their reason.
type SubmitResponse struct {
	Submitted []Submitted `json:"submitted"`
	Failed    []Failed    `json:"failed"`
}
