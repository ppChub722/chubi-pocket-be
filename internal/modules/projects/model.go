package projects

import (
	"encoding/json"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

const (
	StatusActive    = "active"
	StatusCompleted = "completed"
	StatusCancelled = "cancelled"
	StatusArchived  = "archived"
)

const (
	RoleOwner       = "owner"
	RoleContributor = "contributor"
	RoleViewer      = "viewer"
)

const (
	MemberStatusPending = "pending"
	MemberStatusActive  = "active"
	MemberStatusLeft    = "left"
)

const (
	PTTypeExpense = "expense"
	PTTypeIncome  = "income"
)

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
	PlannedAmount *float64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

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
	TagNames []string
}

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

	myStatus string
}

type EventList struct {
	Data       []Event          `json:"data"`
	Summary    EventListSummary `json:"summary"`
	Pagination shared.Page      `json:"pagination"`
}

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

type EventMemberList struct {
	Data []EventMember `json:"data"`
}

type EventTagRef struct {
	ID       uuid.UUID        `json:"id"`
	Name     string           `json:"name"`
	IconCode *shared.IconCode `json:"icon_code"`
	Mine     bool             `json:"mine"`
}

type EventTag struct {
	EventTagRef
	Count int `json:"count"`
}

type EventTagList struct {
	Data []EventTag `json:"data"`
}

type EventPosition struct {
	Member shared.MemberRef `json:"member"`
	Paid   float64          `json:"paid"`
	Share  float64          `json:"share"`
	Net    float64          `json:"net"`
	Receivable float64 `json:"receivable"`
	Payable    float64 `json:"payable"`
}

type EventTagTotal struct {
	Tag   *EventTagRef `json:"tag"`
	Total float64      `json:"total"`
	Share float64      `json:"share"`
	Count int          `json:"count"`
}

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

type BoardSourceTx struct {
	ID          uuid.UUID          `json:"id"`
	Description *string            `json:"description"`
	Account     *shared.AccountRef `json:"account"`
}

type BoardSplit struct {
	ID                uuid.UUID        `json:"id"`
	TransactionMember shared.MemberRef `json:"transaction_member"`
	Amount            float64          `json:"amount"`
	Marks             []uuid.UUID      `json:"marks"`
	MyMark            bool             `json:"my_mark"`
}

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

type BoardRowList struct {
	Data       []BoardRow  `json:"data"`
	Pagination shared.Page `json:"pagination"`
}

type EventDetail struct {
	Event   Event         `json:"event"`
	Summary EventSummary  `json:"summary"`
	Members []EventMember `json:"members"`
	Tags    []EventTag    `json:"tags"`
	Board   BoardRowList  `json:"board"`
	DeletePreview *DeletePreview `json:"delete_preview"`
}

type EventPatch struct {
	Event           Event                  `json:"event"`
	Summary         EventSummary           `json:"summary"`
	Members         []EventMember          `json:"members"`
	Tags            []EventTag             `json:"tags"`
	Row             *BoardRow              `json:"row"`
	RemovedRowID    *uuid.UUID             `json:"removed_row_id"`
	AccountBalances []shared.WalletBalance `json:"account_balances"`
}

type AddBillsResult struct {
	EventPatch
	NewTransactionID *uuid.UUID `json:"new_transaction_id"`
	Rows             []BoardRow `json:"rows"`
}

type QuickCreateEventResult struct {
	Detail           EventDetail            `json:"detail"`
	NewTransactionID *uuid.UUID             `json:"new_transaction_id"`
	AccountBalances  []shared.WalletBalance `json:"account_balances"`
}

type EventCopyResult struct {
	EventPatch
	Transaction *transactions.TransactionDetail `json:"transaction"`
}

type EventTagWriteResult struct {
	EventPatch
	Tag EventTag `json:"tag"`
}

type EventTagImportResult struct {
	Created []shared.TagRef `json:"created"`
	Tags    []EventTag      `json:"tags"`
}

type EventDeleteResult struct {
	ID                      uuid.UUID              `json:"id"`
	DeletedTransactionCount int                    `json:"deleted_transaction_count"`
	AccountBalances         []shared.WalletBalance `json:"account_balances"`
}

type DeletedResult struct {
	ID uuid.UUID `json:"id"`
}

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

func (r *UpdateProjectRequest) onlyMyTags() bool {
	return r.has("tag_ids") && len(r.present) == 1
}

func (r *UpdateProjectRequest) DescriptionChange() (*string, bool) {
	return shared.TextChange(r.Description, r.has("description"))
}

func (r *UpdateProjectRequest) NoteChange() (*string, bool) {
	return shared.TextChange(r.Note, r.has("note"))
}

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

	TagNames            []string   `json:"-"`
	LinkedTransactionID *uuid.UUID `json:"-"`
}

type UpdateProjectTransactionRequest struct {
	Amount      *float64             `json:"amount"      binding:"omitempty,gt=0"`
	Date        *string              `json:"date"        binding:"omitempty,datetime=2006-01-02"`
	Note        *string              `json:"note"        binding:"omitempty,max=500"`
	Description *string              `json:"description" binding:"omitempty,max=200"`
	TagIDs      *[]uuid.UUID         `json:"tag_ids"`
	Splits      *[]ProjectSplitInput `json:"splits"      binding:"omitempty,dive"`
	AlsoUpdateLink bool `json:"also_update_link"`

	descriptionPresent bool
	notePresent        bool
}

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

type MarkRequest struct {
	Marked bool `json:"marked"`
}

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

type EventTagImportRequest struct {
	TagIDs []uuid.UUID `json:"tag_ids"`
}

type QuickCreateRequest struct {
	Name           string                      `json:"name"            binding:"required,min=1,max=100"`
	NewTransaction *transactions.CreateRequest `json:"new_transaction" binding:"omitempty"`
	TransactionIDs []uuid.UUID      `json:"transaction_ids" binding:"omitempty"`
	IconCode       *shared.IconCode `json:"icon_code"       binding:"omitempty"`
	Move bool `json:"move"`
}

type AddBillsRequest struct {
	NewTransaction *transactions.CreateRequest `json:"new_transaction" binding:"omitempty"`
	TransactionIDs []uuid.UUID                 `json:"transaction_ids" binding:"omitempty"`
	Move           bool                        `json:"move"`
}

type ListFilter struct {
	Status  string
	Q       string
	TagIDs  []uuid.UUID
	Sort    string
	Page    int
	PerPage int
}

type BoardFilter struct {
	Type    string
	Mine    bool
	Q       string
	TagID   *uuid.UUID
	PayerMemberID *uuid.UUID
	Sort    string
	Page    int
	PerPage int
}

var _ json.Unmarshaler = (*UpdateProjectRequest)(nil)

type InviteAcceptResult struct {
	*EventDetail
	InboxSummary notifications.InboxSummary `json:"inbox_summary"`
}

type DeletePreview struct {
	Bills         int `json:"bills"`           // bill rows of the board
	Members       int `json:"members"`         // active members
	LinkedTxCount int `json:"linked_tx_count"` // my own book records linked to it
}
