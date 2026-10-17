package pending

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
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
	KindSaving     = "saving"      // a saving entry (goals v2): never a transaction
)

// SourceManual — typed in by the user. Other sources (
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
	// ProjectID — the event the bill goes into on confirm (kind create only).
	// Not checked at save: the event is validated when the draft is submitted.
	ProjectID *uuid.UUID `json:"project_id,omitempty"`
	// GoalID — kind saving: the goal the money goes into (absent = spare).
	GoalID *uuid.UUID `json:"goal_id,omitempty"`
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
	Resolved            DraftResolved   `json:"resolved"`
	TargetDebtID        *uuid.UUID      `json:"target_debt_id"`
	TargetTransactionID *uuid.UUID      `json:"target_transaction_id"`
	SourceRef           json.RawMessage `json:"source_ref"`
	LastError           *SubmitError    `json:"last_error"`
	// NeedsInput — what the draft is still missing (amount, type, date, wallet,
	// wallet_to, category; empty = ready). CanConfirm — false while a hard one
	// (amount, type, date, a transfer's wallets) is missing: confirm is rejected.
	NeedsInput []string  `json:"needs_input"`
	CanConfirm bool      `json:"can_confirm"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type ListResponse struct {
	Data    []PendingTransaction `json:"data"`
	Count   int                  `json:"count"`
	Summary ListSummary          `json:"summary"`
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

// IdsRequest — POST /pending-transactions/submit and /discard.
type IdsRequest struct {
	IDs []uuid.UUID `json:"ids" binding:"required,min=1,max=100"`
}

// SubmitRequest — POST /pending-transactions/submit.
type SubmitRequest = IdsRequest

// DiscardRequest — POST /pending-transactions/discard.
type DiscardRequest = IdsRequest

// DiscardResult — what a discard removed.
type DiscardResult struct {
	Discarded      []uuid.UUID `json:"discarded"`
	Skipped        []uuid.UUID `json:"skipped"`
	RemainingCount int         `json:"remaining_count"`
}

// ResolvedSplit — one draft split, its person resolved by the one name rule.
type ResolvedSplit struct {
	Person     shared.PersonRef `json:"person"`
	OwedAmount float64          `json:"owed_amount"`
}

// DraftResolved — read-only (spec schema DraftResolved): the draft's ids
// resolved so the page renders without other calls. A dangling id is null
// here and its draft key is listed in Missing.
type DraftResolved struct {
	Account           *shared.AccountRef    `json:"account"`
	TransferToAccount *shared.AccountRef    `json:"transfer_to_account"`
	Category          *shared.CategoryRef   `json:"category"`
	Tags              []shared.TagRef       `json:"tags"`
	Project           *shared.EventRef      `json:"project"`
	Goal              *shared.SavingGoalRef `json:"goal"`
	Splits            []ResolvedSplit       `json:"splits"`
	Missing           []string              `json:"missing"`
}

type Submitted struct {
	ID uuid.UUID `json:"id"`
	// Rows — the created row(s) in their final state (a transfer: OUT first).
	Rows []transactions.TransactionDetail `json:"rows"`
	// TransactionID — the row the draft became (Go side only).
	TransactionID *uuid.UUID `json:"-"`
	// SavingEntryID — a saving draft: the saving entry it became.
	SavingEntryID *uuid.UUID `json:"saving_entry_id"`
}

type Failed struct {
	ID    uuid.UUID   `json:"id"`
	Error SubmitError `json:"error"`
	// Pending — the draft with last_error set; nil when it no longer exists.
	Pending *PendingTransaction `json:"pending"`
}

// SubmitResponse — each draft independently: the ones that went through
// are gone from pending; the ones that failed stay with their reason.
type SubmitResponse struct {
	Submitted       []Submitted            `json:"submitted"`
	Failed          []Failed               `json:"failed"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
	Badges          shared.Badges          `json:"badges"`
}

// SourceCounts — the pending page's source chips.
type SourceCounts struct {
	Recurring   int `json:"recurring"`    // recurring / upcoming items and goal auto-saving
	Manual      int `json:"manual"`       // typed in (and chat)
	OtherPeople int `json:"other_people"` // from another person (event bills, splits)
	Slip        int `json:"slip"`         // scanned slips
	Other       int `json:"other"`
}

// ListSummary — ListResponse.summary: over all the user's drafts.
type ListSummary struct {
	Count           int          `json:"count"`
	ExpenseTotal    float64      `json:"expense_total"`
	IncomeTotal     float64      `json:"income_total"`
	NeedsInputCount int          `json:"needs_input_count"`
	ReadyCount      int          `json:"ready_count"`
	BySource        SourceCounts `json:"by_source"`
}
