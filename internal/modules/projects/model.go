package projects

import (
	"encoding/json"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// Project status enum values.
const (
	StatusActive    = "active"
	StatusCompleted = "completed"
	StatusCancelled = "cancelled"
	StatusArchived  = "archived"
)

// Member role enum.
const (
	RoleOwner       = "owner"
	RoleContributor = "contributor"
	RoleViewer      = "viewer"
)

// Member status enum.
const (
	MemberStatusPending = "pending"
	MemberStatusActive  = "active"
	MemberStatusLeft    = "left"
)

// Project transaction type enum.
const (
	PTTypeExpense = "expense"
	PTTypeIncome  = "income"
)

// Project is the projects row (the API shape is Event, views.go).
type Project struct {
	ID          uuid.UUID
	OwnerUserID uuid.UUID
	Name        string
	Description *string
	Note        *string
	StartDate   *string
	EndDate     *string
	Status      string
	IconCode    *shared.IconCode
	// Plan-vs-actual single total (spec §10/4.23). NULL = plan UI off.
	PlannedAmount *float64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ProjectMember has 2 kinds post-migration 27:
//
//	linked  (user_id IS NOT NULL)
//	ad-hoc  (user_id IS NULL)
type ProjectMember struct {
	ID          uuid.UUID
	ProjectID   uuid.UUID
	UserID      *uuid.UUID
	DisplayName string
	Role        string
	Status      string
	IconCode    *shared.IconCode
	InvitedAt   *time.Time
	JoinedAt    *time.Time
	LeftAt      *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ProjectTransaction — a board row. Splits are child rows linked via
// ParentProjectTransactionID. Children inherit the parent's type / currency /
// date / note (copied at create). Marks = the member ids who ticked it.
// The API shape is BoardRow (views.go).
type ProjectTransaction struct {
	ID                         uuid.UUID
	ProjectID                  uuid.UUID
	ParentProjectTransactionID *uuid.UUID
	TransactionMemberID        uuid.UUID
	RecordUserID               uuid.UUID
	Type                       string
	Amount                     float64
	Currency                   string
	Date                       string
	Note                       *string
	Description                *string
	Marks                      []uuid.UUID
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
	// The event tags on the row (parents only; names for the copy rules).
	TagNames []string
}

// --- API shapes (spec schemas Event*, Board*) ---

// Event — spec schema Event.
type Event struct {
	ID            uuid.UUID        `json:"id"`
	OwnerUserID   uuid.UUID        `json:"owner_user_id"`
	Name          string           `json:"name"`
	IconCode      *shared.IconCode `json:"icon_code"`
	Description   *string          `json:"description"`
	Note          *string          `json:"note"`
	Status        string           `json:"status"`
	StartDate     *string          `json:"start_date"`
	EndDate       *string          `json:"end_date"`
	PlannedAmount *float64         `json:"planned_amount"`
	MembersCount  int              `json:"members_count"`
	TotalExpense  float64          `json:"total_expense"`
	TotalIncome   float64          `json:"total_income"`
	MyNet         float64          `json:"my_net"`
	// 0.4.0 (spec 10): my_paid = what I paid on the expense bills; my_share = my
	// part of them; my_receivable / my_payable = my_net split by sign (both ≥ 0);
	// remaining = planned_amount − (expense − income), null without a plan.
	MyPaid           float64         `json:"my_paid"`
	MyShare          float64         `json:"my_share"`
	MyReceivable     float64         `json:"my_receivable"`
	MyPayable        float64         `json:"my_payable"`
	Remaining        *float64        `json:"remaining"`
	Tags             []shared.TagRef `json:"tags"`
	MyMemberID       *uuid.UUID      `json:"my_member_id"`
	MyRole           *string         `json:"my_role"`
	IsOwner          bool            `json:"is_owner"`
	IsLocked         bool            `json:"is_locked"`
	CanAddBill       bool            `json:"can_add_bill"`
	CanEdit          bool            `json:"can_edit"`
	CanManageMembers bool            `json:"can_manage_members"`
	LinkedTxCount    int             `json:"linked_tx_count"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`

	// myStatus — the caller's member status ("" = not a member).
	myStatus string
}

// EventList — spec schema EventList.
type EventList struct {
	Data       []Event          `json:"data"`
	Summary    EventListSummary `json:"summary"`
	Pagination shared.Page      `json:"pagination"`
}

// EventMember — spec schema EventMember.
type EventMember struct {
	ID        uuid.UUID        `json:"id"`
	UserID    *uuid.UUID       `json:"user_id"`
	Name      string           `json:"name"`
	IconCode  *shared.IconCode `json:"icon_code"`
	IsMe      bool             `json:"is_me"`
	Linked    bool             `json:"linked"`
	Role      string           `json:"role"`
	Status    string           `json:"status"`
	InvitedAt *time.Time       `json:"invited_at"`
	JoinedAt  *time.Time       `json:"joined_at"`
	LeftAt    *time.Time       `json:"left_at"`
}

// EventMemberList — spec schema EventMemberList.
type EventMemberList struct {
	Data []EventMember `json:"data"`
}

// EventTagRef — spec schema EventTagRef.
type EventTagRef struct {
	ID       uuid.UUID        `json:"id"`
	Name     string           `json:"name"`
	IconCode *shared.IconCode `json:"icon_code"`
	Mine     bool             `json:"mine"`
}

// EventTag — spec schema EventTag.
type EventTag struct {
	EventTagRef
	Count int `json:"count"`
}

// EventTagList — spec schema EventTagList.
type EventTagList struct {
	Data []EventTag `json:"data"`
}

// EventPosition — spec schema EventPosition.
type EventPosition struct {
	Member shared.MemberRef `json:"member"`
	Paid   float64          `json:"paid"`
	Share  float64          `json:"share"`
	Net    float64          `json:"net"`
	// Receivable / Payable — net split by sign (both ≥ 0).
	Receivable float64 `json:"receivable"`
	Payable    float64 `json:"payable"`
}

// EventTagTotal — spec schema EventTagTotal.
type EventTagTotal struct {
	Tag   *EventTagRef `json:"tag"`
	Total float64      `json:"total"`
	Share float64      `json:"share"`
	Count int          `json:"count"`
}

// EventSummary — spec schema EventSummary.
type EventSummary struct {
	TotalExpense     float64         `json:"total_expense"`
	TotalIncome      float64         `json:"total_income"`
	TransactionCount int             `json:"transaction_count"`
	MemberCount      int             `json:"member_count"`
	PlannedAmount    *float64        `json:"planned_amount"`
	SpentNet         *float64        `json:"spent_net"`
	Remaining        *float64        `json:"remaining"`
	MyPosition       *EventPosition  `json:"my_position"`
	Members          []EventPosition `json:"members"`
	ByTag            []EventTagTotal `json:"by_tag"`
}

// BoardSourceTx — spec schema BoardSourceTx.
type BoardSourceTx struct {
	ID          uuid.UUID          `json:"id"`
	Description *string            `json:"description"`
	Account     *shared.AccountRef `json:"account"`
}

// BoardSplit — spec schema BoardSplit.
type BoardSplit struct {
	ID                uuid.UUID        `json:"id"`
	TransactionMember shared.MemberRef `json:"transaction_member"`
	Amount            float64          `json:"amount"`
	Marks             []uuid.UUID      `json:"marks"`
	MyMark            bool             `json:"my_mark"`
}

// BoardRow — spec schema BoardRow.
type BoardRow struct {
	ID                uuid.UUID        `json:"id"`
	ProjectID         uuid.UUID        `json:"project_id"`
	Type              string           `json:"type"`
	Amount            float64          `json:"amount"`
	Date              string           `json:"date"`
	Description       *string          `json:"description"`
	Note              *string          `json:"note"`
	TransactionMember shared.MemberRef `json:"transaction_member"`
	RecordUserID      uuid.UUID        `json:"record_user_id"`
	Tags              []EventTagRef    `json:"tags"`
	Marks             []uuid.UUID      `json:"marks"`
	MyMark            bool             `json:"my_mark"`
	MyShare           float64          `json:"my_share"`
	Locked            bool             `json:"locked"`
	Linked            bool             `json:"linked"`
	SourceTx          *BoardSourceTx   `json:"source_tx"`
	Children          []BoardSplit     `json:"children"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
}

// BoardRowList — spec schema BoardRowList.
type BoardRowList struct {
	Data       []BoardRow  `json:"data"`
	Pagination shared.Page `json:"pagination"`
}

// EventDetail — spec schema EventDetail.
type EventDetail struct {
	Event   Event         `json:"event"`
	Summary EventSummary  `json:"summary"`
	Members []EventMember `json:"members"`
	Tags    []EventTag    `json:"tags"`
	Board   BoardRowList  `json:"board"`
	// DeletePreview — only with ?include=delete_preview, else null.
	DeletePreview *DeletePreview `json:"delete_preview"`
}

// EventPatch — spec schema EventPatch: every event write's final state.
type EventPatch struct {
	Event           Event                  `json:"event"`
	Summary         EventSummary           `json:"summary"`
	Members         []EventMember          `json:"members"`
	Tags            []EventTag             `json:"tags"`
	Row             *BoardRow              `json:"row"`
	RemovedRowID    *uuid.UUID             `json:"removed_row_id"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
}

// AddBillsResult — spec schema AddBillsResult.
type AddBillsResult struct {
	EventPatch
	NewTransactionID *uuid.UUID `json:"new_transaction_id"`
	Rows             []BoardRow `json:"rows"`
}

// QuickCreateEventResult — spec schema QuickCreateEventResult.
type QuickCreateEventResult struct {
	Detail           EventDetail            `json:"detail"`
	NewTransactionID *uuid.UUID             `json:"new_transaction_id"`
	AccountBalances  []shared.WalletBalance `json:"account_balances"`
}

// EventCopyResult — spec schema EventCopyResult.
type EventCopyResult struct {
	EventPatch
	Transaction *transactions.TransactionDetail `json:"transaction"`
}

// EventTagWriteResult — spec schema EventTagWriteResult.
type EventTagWriteResult struct {
	EventPatch
	Tag EventTag `json:"tag"`
}

// EventTagImportResult — spec schema EventTagImportResult.
type EventTagImportResult struct {
	Created []shared.TagRef `json:"created"`
	Tags    []EventTag      `json:"tags"`
}

// EventDeleteResult — spec schema EventDeleteResult.
type EventDeleteResult struct {
	ID                      uuid.UUID              `json:"id"`
	DeletedTransactionCount int                    `json:"deleted_transaction_count"`
	AccountBalances         []shared.WalletBalance `json:"account_balances"`
}

// DeletedResult — spec schema DeletedResult.
type DeletedResult struct {
	ID uuid.UUID `json:"id"`
}

// --- Request bodies ---

type CreateProjectRequest struct {
	Name          string           `json:"name"           binding:"required,min=1,max=100"`
	Description   *string          `json:"description"    binding:"omitempty,max=200"`
	Note          *string          `json:"note"           binding:"omitempty,max=500"`
	StartDate     *string          `json:"start_date"     binding:"omitempty,datetime=2006-01-02"`
	EndDate       *string          `json:"end_date"       binding:"omitempty,datetime=2006-01-02"`
	IconCode      *shared.IconCode `json:"icon_code"      binding:"omitempty"`
	PlannedAmount *float64         `json:"planned_amount" binding:"omitempty,gt=0"`
	TagIDs        []uuid.UUID      `json:"tag_ids"`
}

// UpdateProjectRequest — partial; presence-tracked: an absent key leaves the
// field, null clears description / note / dates / planned_amount.
type UpdateProjectRequest struct {
	Name          *string          `json:"name"           binding:"omitempty,min=1,max=100"`
	Description   *string          `json:"description"    binding:"omitempty,max=200"`
	Note          *string          `json:"note"           binding:"omitempty,max=500"`
	StartDate     *string          `json:"start_date"     binding:"omitempty,datetime=2006-01-02"`
	EndDate       *string          `json:"end_date"       binding:"omitempty,datetime=2006-01-02"`
	Status        *string          `json:"status"         binding:"omitempty,oneof=active completed cancelled archived"`
	IconCode      *shared.IconCode `json:"icon_code"      binding:"omitempty"`
	PlannedAmount *float64         `json:"planned_amount" binding:"omitempty,gt=0"`
	TagIDs        []uuid.UUID      `json:"tag_ids"`

	present map[string]bool
}

func (r *UpdateProjectRequest) UnmarshalJSON(data []byte) error {
	type alias UpdateProjectRequest
	present, err := shared.DecodeTracked(data, (*alias)(r))
	r.present = present
	return err
}

func (r *UpdateProjectRequest) has(k string) bool { return r.present[k] }

// onlyMyTags — the body sets nothing but MY tags (any member may).
func (r *UpdateProjectRequest) onlyMyTags() bool {
	return r.has("tag_ids") && len(r.present) == 1
}

// DescriptionChange / NoteChange: absent → unchanged; null or "" clears.
func (r *UpdateProjectRequest) DescriptionChange() (*string, bool) {
	return shared.TextChange(r.Description, r.has("description"))
}

func (r *UpdateProjectRequest) NoteChange() (*string, bool) {
	return shared.TextChange(r.Note, r.has("note"))
}

// AddMemberRequest — one of: (a) ad_hoc + display_name; (b) email +
// display_name (invite).
type AddMemberRequest struct {
	Email       *string          `json:"email"        binding:"omitempty,email"`
	DisplayName *string          `json:"display_name" binding:"omitempty,min=1,max=100"`
	Role        string           `json:"role"         binding:"omitempty,oneof=contributor viewer"`
	AdHoc       bool             `json:"ad_hoc"`
	IconCode    *shared.IconCode `json:"icon_code"    binding:"omitempty"`
}

type UpdateMemberRequest struct {
	Role     *string          `json:"role"      binding:"omitempty,oneof=contributor viewer"`
	IconCode *shared.IconCode `json:"icon_code" binding:"omitempty"`
}

// ProjectSplitInput — one share line on a bill. MemberID is a member of the
// same event, not the payer; Σ amounts ≤ the bill (the rest is the payer's).
type ProjectSplitInput struct {
	MemberID uuid.UUID `json:"member_id" binding:"required"`
	Amount   float64   `json:"amount"    binding:"required,gt=0"`
}

type CreateProjectTransactionRequest struct {
	TransactionMemberID uuid.UUID           `json:"transaction_member_id" binding:"required"`
	Type                string              `json:"type"                  binding:"required,oneof=expense income"`
	Amount              float64             `json:"amount"                binding:"required,gt=0"`
	Date                string              `json:"date"                  binding:"required,datetime=2006-01-02"`
	Note                *string             `json:"note"                  binding:"omitempty,max=500"`
	Description         *string             `json:"description"           binding:"omitempty,max=200"`
	TagIDs              []uuid.UUID         `json:"tag_ids"`
	Splits              []ProjectSplitInput `json:"splits"                binding:"omitempty,dive"`

	// Set in Go: event tags by name (a pulled bill's copied tags) and the
	// transaction the bill is made from. The currency is always the
	// recorder's users.currency (InsertPTTx).
	TagNames            []string   `json:"-"`
	LinkedTransactionID *uuid.UUID `json:"-"`
}

// UpdateProjectTransactionRequest — partial; splits / tag_ids present =
// the whole new set.
type UpdateProjectTransactionRequest struct {
	Amount      *float64             `json:"amount"      binding:"omitempty,gt=0"`
	Date        *string              `json:"date"        binding:"omitempty,datetime=2006-01-02"`
	Note        *string              `json:"note"        binding:"omitempty,max=500"`
	Description *string              `json:"description" binding:"omitempty,max=200"`
	TagIDs      *[]uuid.UUID         `json:"tag_ids"`
	Splits      *[]ProjectSplitInput `json:"splits"      binding:"omitempty,dive"`
	// AlsoUpdateLink — my own linked bill: also change my transaction.
	AlsoUpdateLink bool `json:"also_update_link"`

	descriptionPresent bool
	notePresent        bool
}

// UnmarshalJSON records which keys the body carried: an absent description /
// note stays as is; null or "" clears it.
func (r *UpdateProjectTransactionRequest) UnmarshalJSON(data []byte) error {
	type alias UpdateProjectTransactionRequest
	present, err := shared.DecodeTracked(data, (*alias)(r))
	r.descriptionPresent, r.notePresent = present["description"], present["note"]
	return err
}

func (r *UpdateProjectTransactionRequest) DescriptionChange() (*string, bool) {
	return shared.TextChange(r.Description, r.descriptionPresent)
}

func (r *UpdateProjectTransactionRequest) NoteChange() (*string, bool) {
	return shared.TextChange(r.Note, r.notePresent)
}

// MarkRequest — body for PUT /projects/:id/project-transactions/:pt_id/mark.
type MarkRequest struct {
	Marked bool `json:"marked"`
}

// EventTagRequest — create / update an event tag.
type EventTagRequest struct {
	Name     *string          `json:"name"      binding:"omitempty,min=1,max=50"`
	IconCode *shared.IconCode `json:"icon_code"`

	iconPresent bool
}

func (r *EventTagRequest) UnmarshalJSON(data []byte) error {
	type alias EventTagRequest
	present, err := shared.DecodeTracked(data, (*alias)(r))
	r.iconPresent = present["icon_code"]
	return err
}

// EventTagImportRequest — import event tags into my personal tags.
type EventTagImportRequest struct {
	TagIDs []uuid.UUID `json:"tag_ids"`
}

// QuickCreateRequest — POST /v1/projects/quick: create the event, a new bill
// through the normal transaction path, and pull loose bills in, atomically.
type QuickCreateRequest struct {
	Name           string                      `json:"name"            binding:"required,min=1,max=100"`
	NewTransaction *transactions.CreateRequest `json:"new_transaction" binding:"omitempty"`
	// My own expense / income transactions, not in an event (or Move).
	TransactionIDs []uuid.UUID      `json:"transaction_ids" binding:"omitempty"`
	IconCode       *shared.IconCode `json:"icon_code"       binding:"omitempty"`
	// Move: a listed bill already in another event leaves it first.
	Move bool `json:"move"`
}

// AddBillsRequest — POST /v1/projects/:id/bills: same rules as quick create.
type AddBillsRequest struct {
	NewTransaction *transactions.CreateRequest `json:"new_transaction" binding:"omitempty"`
	TransactionIDs []uuid.UUID                 `json:"transaction_ids" binding:"omitempty"`
	Move           bool                        `json:"move"`
}

// --- List filters ---

type ListFilter struct {
	Status  string
	Q       string
	TagIDs  []uuid.UUID
	Sort    string
	Page    int
	PerPage int
}

// BoardFilter — GET /projects/:id/transactions.
type BoardFilter struct {
	Type    string
	Mine    bool
	Q       string
	TagID   *uuid.UUID
	// PayerMemberID — only bills this member paid.
	PayerMemberID *uuid.UUID
	Sort    string
	Page    int
	PerPage int
}

var _ json.Unmarshaler = (*UpdateProjectRequest)(nil)

// InviteAcceptResult — the event detail (flattened) + the inbox summary after the
// accept (named inbox_summary: the event detail already has its own `summary`).
type InviteAcceptResult struct {
	*EventDetail
	InboxSummary notifications.InboxSummary `json:"inbox_summary"`
}

// DeletePreview — what deleting the event removes (owner delete; the members' own
// book copies stay as ordinary records, detached).
type DeletePreview struct {
	Bills         int `json:"bills"`           // bill rows of the board
	Members       int `json:"members"`         // active members
	LinkedTxCount int `json:"linked_tx_count"` // my own book records linked to it
}
