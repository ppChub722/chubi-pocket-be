package pending

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/projects"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const (
	KindCreate     = "create"      // a new transaction
	KindSettleDebt = "settle_debt" // a new transaction that settles TargetDebtID
	KindUpdateTx   = "update_tx"   // edits TargetTransactionID
	KindSaving     = "saving"      // a saving entry (goals v2): never a transaction
)

const SourceManual = "manual"

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
	// PaidBy — someone paid (part of) it for me (0.4.0-3); exclusive with splits and project_id.
	PaidBy    *transactions.PaidByInput `json:"paid_by,omitempty"`
	ProjectID *uuid.UUID                `json:"project_id,omitempty"`
	// Payer / Members — with project_id: who paid the bill and who is in it (0.4.0-3, as on
	// POST /projects/{id}/bills).
	Payer   *projects.BillPerson  `json:"payer,omitempty"`
	Members []projects.BillMember `json:"members,omitempty"`
	GoalID  *uuid.UUID            `json:"goal_id,omitempty"`
}

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
	NeedsInput          []string        `json:"needs_input"`
	CanConfirm          bool            `json:"can_confirm"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

type ListResponse struct {
	Data    []PendingTransaction `json:"data"`
	Count   int                  `json:"count"`
	Summary ListSummary          `json:"summary"`
}

type CreateRequest struct {
	Items []CreateItem `json:"items" binding:"required,min=1,max=100,dive"`
}

type CreateItem struct {
	Draft Draft `json:"draft"`
}

type UpdateRequest struct {
	Draft Draft `json:"draft"`
}

type IdsRequest struct {
	IDs []uuid.UUID `json:"ids" binding:"required,min=1,max=100"`
}

type SubmitRequest = IdsRequest

type DiscardRequest = IdsRequest

type DiscardResult struct {
	Discarded      []uuid.UUID `json:"discarded"`
	Skipped        []uuid.UUID `json:"skipped"`
	RemainingCount int         `json:"remaining_count"`
	Summary        ListSummary `json:"summary"`
}

type ResolvedSplit struct {
	Person     shared.PersonRef `json:"person"`
	OwedAmount float64          `json:"owed_amount"`
}

// DraftResolved — read-only (spec schema DraftResolved): the draft's ids resolved so the page
// renders without other calls.
type DraftResolved struct {
	Account           *shared.AccountRef    `json:"account"`
	TransferToAccount *shared.AccountRef    `json:"transfer_to_account"`
	Category          *shared.CategoryRef   `json:"category"`
	Tags              []shared.TagRef       `json:"tags"`
	Project           *shared.EventRef      `json:"project"`
	Goal              *shared.SavingGoalRef `json:"goal"`
	Splits            []ResolvedSplit       `json:"splits"`
	PaidBy            *ResolvedPaidBy       `json:"paid_by"`
	BillPeople        *ResolvedBillPeople   `json:"bill_people"` // event drafts with payer / members
	// LinkMode — personal | split | paid | event: the quick-create view the draft was saved with.
	LinkMode string   `json:"link_mode"`
	Missing  []string `json:"missing"`
}

// ResolvedBillPeople — an event draft's payer and members with names (me = the caller; name is
// empty for a plain `me`, the FE shows ฉัน).
type ResolvedBillPeople struct {
	Payer   ResolvedBillPerson   `json:"payer"`
	Members []ResolvedBillPerson `json:"members"`
}

type ResolvedBillPerson struct {
	Me        bool       `json:"me"`
	MemberID  *uuid.UUID `json:"member_id"`
	ContactID *uuid.UUID `json:"contact_id"`
	Name      string     `json:"name"`
	Share     *float64   `json:"share,omitempty"` // members only
}

// ResolvedPaidBy — the draft's paid_by with its person resolved; my_paid = amount − paid_by.amount.
type ResolvedPaidBy struct {
	Person shared.PersonRef `json:"person"`
	Amount float64          `json:"amount"`
	MyPaid *float64         `json:"my_paid"`
}

type Submitted struct {
	ID            uuid.UUID                        `json:"id"`
	Rows          []transactions.TransactionDetail `json:"rows"`
	TransactionID *uuid.UUID                       `json:"-"`
	SavingEntryID *uuid.UUID                       `json:"saving_entry_id"`
}

type Failed struct {
	ID      uuid.UUID           `json:"id"`
	Error   SubmitError         `json:"error"`
	Pending *PendingTransaction `json:"pending"`
}

type SubmitResponse struct {
	Submitted       []Submitted            `json:"submitted"`
	Failed          []Failed               `json:"failed"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
	Badges          shared.Badges          `json:"badges"`
	Summary         ListSummary            `json:"summary"`
}

type SourceCounts struct {
	Recurring   int `json:"recurring"`    // recurring / upcoming items and goal auto-saving
	Manual      int `json:"manual"`       // typed in (and chat)
	OtherPeople int `json:"other_people"` // from another person (event bills, splits)
	Slip        int `json:"slip"`         // scanned slips
	Other       int `json:"other"`
}

type ListSummary struct {
	Count           int          `json:"count"`
	ExpenseTotal    float64      `json:"expense_total"`
	IncomeTotal     float64      `json:"income_total"`
	NeedsInputCount int          `json:"needs_input_count"`
	ReadyCount      int          `json:"ready_count"`
	BySource        SourceCounts `json:"by_source"`
}

type UpdateResponse struct {
	*PendingTransaction
	Summary ListSummary `json:"summary"`
}
