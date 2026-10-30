// Package apierr is the ONE registry of every error code the API can return: the `error.code` of
// the response envelope, a pending draft's `last_error.code` and a sync dataset's `errors[name].code`.
// Each code has its HTTP status(es) and a short English description. The app maps codes (never
// messages) to its own text. Handlers pass these constants; registry_test.go scans the code so no
// call site uses a code that is not here, and apidoc checks the OpenAPI enums against it.
//
// Rules: codes are stable (never renamed; a new case gets a new code). Add a code here first, then
// to openapi.yaml (the test lists what differs).
package apierr

// Code is a stable UPPER_SNAKE error identifier. Clients key on it.
type Code string

// Use says where a code can appear.
type Use uint8

const (
	// InEnvelope: the `error.code` of a failed HTTP response ({success:false, error:{code, message, details}}).
	InEnvelope Use = 1 << iota
	// InPendingLastError: a pending draft's `last_error.code` (GET /pending-transactions, the submit results).
	InPendingLastError
	// InSyncDatasetError: POST /sync `errors[dataset].code`.
	InSyncDatasetError
)

// Def is one registry row. Status lists the HTTP statuses the code is sent with (the first is the
// usual one); it is empty for codes that never form an HTTP response (pending / sync only).
type Def struct {
	Code   Code
	Status []int
	Use    Use
	Desc   string
}

const (
	// general
	ValidationError     Code = "VALIDATION_ERROR"
	Unauthorized        Code = "UNAUTHORIZED"
	Forbidden           Code = "FORBIDDEN"
	NotFound            Code = "NOT_FOUND"
	InternalServerError Code = "INTERNAL_SERVER_ERROR"
	AppOutdated         Code = "APP_OUTDATED"
	InternalError       Code = "INTERNAL_ERROR"

	// auth / users
	InvalidCredentials      Code = "INVALID_CREDENTIALS"
	WrongPassword           Code = "WRONG_PASSWORD"
	EmailExists             Code = "EMAIL_EXISTS"
	UsernameExists          Code = "USERNAME_EXISTS"
	AccountInactive         Code = "ACCOUNT_INACTIVE"
	AvatarSourceUnavailable Code = "AVATAR_SOURCE_UNAVAILABLE"
	CurrencyLocked          Code = "CURRENCY_LOCKED"
	AlreadyActive           Code = "ALREADY_ACTIVE"

	// wallets (accounts)
	AccountNotFound    Code = "ACCOUNT_NOT_FOUND"
	AccountArchived    Code = "ACCOUNT_ARCHIVED"
	AccountHasMembers  Code = "ACCOUNT_HAS_MEMBERS"
	OwnerMustTransfer  Code = "OWNER_MUST_TRANSFER"
	UserAlreadyMember  Code = "USER_ALREADY_MEMBER"
	UserNotFound       Code = "USER_NOT_FOUND"
	NotMember          Code = "NOT_MEMBER"
	NotOwner           Code = "NOT_OWNER"
	ScopeNotAllowed    Code = "SCOPE_NOT_ALLOWED"
	InvalidIdentifier  Code = "INVALID_IDENTIFIER"
	BalanceNotEditable Code = "BALANCE_NOT_EDITABLE"
	BalanceNotTracked  Code = "BALANCE_NOT_TRACKED"
	NoOp               Code = "NO_OP"
	WalletNotTracked   Code = "WALLET_NOT_TRACKED"
	RowLocked          Code = "ROW_LOCKED"
	ContactNotLinked   Code = "CONTACT_NOT_LINKED"

	// categories / tags
	CategoryNameDuplicate    Code = "CATEGORY_NAME_DUPLICATE"
	InvalidParent            Code = "INVALID_PARENT"
	CycleDetected            Code = "CYCLE_DETECTED"
	MaxDepthExceeded         Code = "MAX_DEPTH_EXCEEDED"
	SystemCategoryImmutable  Code = "SYSTEM_CATEGORY_IMMUTABLE"
	SystemCategoryNotAllowed Code = "SYSTEM_CATEGORY_NOT_ALLOWED"
	CategoryTypeMismatch     Code = "CATEGORY_TYPE_MISMATCH"
	CategoryAuthorOnly       Code = "CATEGORY_AUTHOR_ONLY"
	InvalidCategory          Code = "INVALID_CATEGORY"
	CategoryNotFound         Code = "CATEGORY_NOT_FOUND"
	TagNotFound              Code = "TAG_NOT_FOUND"
	TagExists                Code = "TAG_EXISTS"
	TagNameTaken             Code = "TAG_NAME_TAKEN"

	// transactions
	TransactionNotFound        Code = "TRANSACTION_NOT_FOUND"
	TxNotFound                 Code = "TX_NOT_FOUND"
	TransferSameAccount        Code = "TRANSFER_SAME_ACCOUNT"
	TransferNeedsWallets       Code = "TRANSFER_NEEDS_WALLETS"
	SystemTransactionImmutable Code = "SYSTEM_TRANSACTION_IMMUTABLE"
	SplitsNotSupportedYet      Code = "SPLITS_NOT_SUPPORTED_YET"
	SplitsOnTransfer           Code = "SPLITS_ON_TRANSFER"
	SplitsAuthorOnly           Code = "SPLITS_AUTHOR_ONLY"
	SplitsExceedAmount         Code = "SPLITS_EXCEED_AMOUNT"
	SplitsExceedShare          Code = "SPLITS_EXCEED_SHARE"
	SplitMismatch              Code = "SPLIT_MISMATCH"
	InvalidSplit               Code = "INVALID_SPLIT"
	PaidByExpenseOnly          Code = "PAID_BY_EXPENSE_ONLY"
	PaidByExceedsAmount        Code = "PAID_BY_EXCEEDS_AMOUNT"
	InvalidPaidBy              Code = "INVALID_PAID_BY"
	LinkModeConflict           Code = "LINK_MODE_CONFLICT"
	LinkModeInput              Code = "LINK_MODE_INPUT"
	SharedBillField            Code = "SHARED_BILL_FIELD"
	PrivateNoteNotShared       Code = "PRIVATE_NOTE_NOT_SHARED"
	SourcePtNotFound           Code = "SOURCE_PT_NOT_FOUND"
	SourcePtNotForUser         Code = "SOURCE_PT_NOT_FOR_USER"
	InvalidPersonKey           Code = "INVALID_PERSON_KEY"

	// debts
	EntryNotFound        Code = "ENTRY_NOT_FOUND"
	EntryManagedBySource Code = "ENTRY_MANAGED_BY_SOURCE"
	EntryHasTransaction  Code = "ENTRY_HAS_TRANSACTION"
	DebtManagedByTx      Code = "DEBT_MANAGED_BY_TX"
	DebtNotLinked        Code = "DEBT_NOT_LINKED"
	DebtTxAlreadyLinked  Code = "DEBT_TX_ALREADY_LINKED"
	DebtTxAuthorOnly     Code = "DEBT_TX_AUTHOR_ONLY"
	DebtTxNeedsWallet    Code = "DEBT_TX_NEEDS_WALLET"
	TxNotLinkable        Code = "TX_NOT_LINKABLE"
	TxNotDisconnectable  Code = "TX_NOT_DISCONNECTABLE"
	TagsNeedWallet       Code = "TAGS_NEED_WALLET"
	LedgerOnlyNoWallet   Code = "LEDGER_ONLY_NO_WALLET"
	BillNotFound         Code = "BILL_NOT_FOUND"
	UseClaim             Code = "USE_CLAIM"
	DebtNotFound         Code = "DEBT_NOT_FOUND"

	// contacts
	ContactNotFound Code = "CONTACT_NOT_FOUND"
	ContactArchived Code = "CONTACT_ARCHIVED"
	AlreadyArchived Code = "ALREADY_ARCHIVED"
	NotArchived     Code = "NOT_ARCHIVED"
	AlreadyLinked   Code = "ALREADY_LINKED"
	NotLinked       Code = "NOT_LINKED"
	CannotLinkSelf  Code = "CANNOT_LINK_SELF"
	RequestRejected Code = "REQUEST_REJECTED"
	NotAccepted     Code = "NOT_ACCEPTED"
	StateConflict   Code = "STATE_CONFLICT"

	// notifications
	NotForYou                 Code = "NOT_FOR_YOU"
	NotificationStateConflict Code = "NOTIFICATION_STATE_CONFLICT"
	NotProjectInvite          Code = "NOT_PROJECT_INVITE"
	TypeNotMutable            Code = "TYPE_NOT_MUTABLE"

	// events (projects)
	ProjectNotFound     Code = "PROJECT_NOT_FOUND"
	ProjectNotActive    Code = "PROJECT_NOT_ACTIVE"
	ProjectLocked       Code = "PROJECT_LOCKED"
	ProjectNotMember    Code = "PROJECT_NOT_MEMBER"
	ProjectReadOnly     Code = "PROJECT_READ_ONLY"
	EventCompleted      Code = "EVENT_COMPLETED"
	EventNotCompleted   Code = "EVENT_NOT_COMPLETED"
	EventHasEmptyPerson Code = "EVENT_HAS_EMPTY_PERSON"
	UseCompleteEndpoint Code = "USE_COMPLETE_ENDPOINT"
	AlreadyMember       Code = "ALREADY_MEMBER"
	MemberNotInProject  Code = "MEMBER_NOT_IN_PROJECT"
	MemberNotRemoved    Code = "MEMBER_NOT_REMOVED"
	NotPending          Code = "NOT_PENDING"
	CannotChangeOwner   Code = "CANNOT_CHANGE_OWNER"
	CannotRemoveOwner   Code = "CANNOT_REMOVE_OWNER"
	OwnerCannotLeave    Code = "OWNER_CANNOT_LEAVE"
	ForbiddenRole       Code = "FORBIDDEN_ROLE"
	PtIsChild           Code = "PT_IS_CHILD"
	SelfSplit           Code = "SELF_SPLIT"
	NotInBill           Code = "NOT_IN_BILL"
	BillAlreadyBooked   Code = "BILL_ALREADY_BOOKED"
	BillChanged         Code = "BILL_CHANGED"
	BillDisconnected    Code = "BILL_DISCONNECTED"
	BillGone            Code = "BILL_GONE"
	MoveTarget          Code = "MOVE_TARGET"
	PayersSum           Code = "PAYERS_SUM"
	PayerFixed          Code = "PAYER_FIXED"
	BadPayback          Code = "BAD_PAYBACK"
	PaybackNotCopyable  Code = "PAYBACK_NOT_COPYABLE"
	NoShareToCopy       Code = "NO_SHARE_TO_COPY"
	NoShare             Code = "NO_SHARE"
	SettleBooked        Code = "SETTLE_BOOKED"
	SettleLines         Code = "SETTLE_LINES"
	ClaimMixedDirection Code = "CLAIM_MIXED_DIRECTION"
	TxAlreadyInProject  Code = "TX_ALREADY_IN_PROJECT"
	TxNotBillable       Code = "TX_NOT_BILLABLE"
	TxNotInProject      Code = "TX_NOT_IN_PROJECT"
	TxSplitPaid         Code = "TX_SPLIT_PAID"
	InvalidDateRange    Code = "INVALID_DATE_RANGE"
	InvalidBillPeople   Code = "INVALID_BILL_PEOPLE"

	// recurring / budgets / saving goals
	InvalidRule        Code = "INVALID_RULE"
	InvalidTransition  Code = "INVALID_TRANSITION"
	EndTooFar          Code = "END_TOO_FAR"
	AmountRequired     Code = "AMOUNT_REQUIRED"
	AlreadyRecorded    Code = "ALREADY_RECORDED"
	NothingOwed        Code = "NOTHING_OWED"
	ItemAlreadyLinked  Code = "ITEM_ALREADY_LINKED"
	SavingNeedsWallet  Code = "SAVING_NEEDS_WALLET"
	InsufficientSaving Code = "INSUFFICIENT_SAVING"
	GoalNotActive      Code = "GOAL_NOT_ACTIVE"
	GoalReached        Code = "GOAL_REACHED"
	GoalHasSavings     Code = "GOAL_HAS_SAVINGS"

	// imports
	UnsupportedImage Code = "UNSUPPORTED_IMAGE"
	OcrUnavailable   Code = "OCR_UNAVAILABLE"
	OcrTimeout       Code = "OCR_TIMEOUT"

	// pending last_error only
	MissingType        Code = "MISSING_TYPE"
	MissingAmount      Code = "MISSING_AMOUNT"
	MissingDate        Code = "MISSING_DATE"
	SubmitFailed       Code = "SUBMIT_FAILED"
	MissingAccount     Code = "MISSING_ACCOUNT"
	DebtAlreadySettled Code = "DEBT_ALREADY_SETTLED"
	Overpayment        Code = "OVERPAYMENT"

	// connections / Telegram login (0.4.2 1b-2)
	LoginCodeInvalid      Code = "LOGIN_CODE_INVALID"
	TelegramAlreadyLinked Code = "TELEGRAM_ALREADY_LINKED"
	RegistrationClosed    Code = "REGISTRATION_CLOSED"
	ConnectionUnavailable Code = "CONNECTION_UNAVAILABLE"
	RateLimited           Code = "RATE_LIMITED"
	PermissionUnavailable Code = "PERMISSION_UNAVAILABLE"
	NoPhoto               Code = "NO_PHOTO"
	LastLoginMethod       Code = "LAST_LOGIN_METHOD"
	ProviderError         Code = "PROVIDER_ERROR"

	// idempotent saves (0.4.2-3)
	IdempotencyKeyReused  Code = "IDEMPOTENCY_KEY_REUSED"
	IdempotencyInProgress Code = "IDEMPOTENCY_IN_PROGRESS"
)

var registry = []Def{
	// general
	{ValidationError, []int{400}, InEnvelope, "The request is invalid; details lists the fields (FieldError[])."},
	{Unauthorized, []int{401}, InEnvelope, "No valid session (missing / expired token); the app signs out."},
	{Forbidden, []int{403}, InEnvelope, "The caller may not use this record or action."},
	{NotFound, []int{404}, InEnvelope | InPendingLastError, "The record does not exist or is not the caller's."},
	{InternalServerError, []int{500}, InEnvelope, "Unexpected server failure; the cause is logged server-side, never sent."},
	{AppOutdated, []int{426}, InEnvelope, "The app build is older than MIN_APP_BUILD; the app must update."},
	{InternalError, nil, InSyncDatasetError, "Sync only: one dataset failed to load (in POST /sync errors, not an HTTP status)."},

	// auth / users
	{InvalidCredentials, []int{401}, InEnvelope, "Login: wrong identifier or password."},
	{WrongPassword, []int{401}, InEnvelope, "Change password: the current password is wrong."},
	{EmailExists, []int{409}, InEnvelope, "The email is already registered to another account."},
	{UsernameExists, []int{409}, InEnvelope, "The username is already registered."},
	{AccountInactive, []int{403}, InEnvelope, "The user deactivated their account; reactivate to continue."},
	{AvatarSourceUnavailable, []int{409}, InEnvelope, "Profile: the picked avatar source has no picture (or the app is no longer linked)."},
	{CurrencyLocked, []int{409}, InEnvelope, "Profile: the currency can't be changed after sign-up (sending the current one is a no-op)."},
	{AlreadyActive, []int{400}, InEnvelope, "Reactivate: the account is already active."},

	// wallets (accounts)
	{AccountNotFound, []int{404, 400}, InEnvelope | InPendingLastError, "The wallet does not exist or is not the caller's (400 when it is referenced in a body)."},
	{AccountArchived, []int{409}, InEnvelope | InPendingLastError, "The wallet is archived; restore it first."},
	{AccountHasMembers, []int{409}, InEnvelope, "The wallet still has other active members; remove them or transfer ownership first."},
	{OwnerMustTransfer, []int{400}, InEnvelope, "The owner must transfer ownership before leaving while other members exist."},
	{UserAlreadyMember, []int{409}, InEnvelope, "Invite: the user is already a member of this wallet."},
	{UserNotFound, []int{404}, InEnvelope, "Invite: no user matches that email."},
	{NotMember, []int{403}, InEnvelope, "The caller is not an active member of this wallet / event."},
	{NotOwner, []int{403}, InEnvelope, "Only the owner can do this."},
	{ScopeNotAllowed, []int{403}, InEnvelope, "Ex-members may only use report_scope none or own."},
	{InvalidIdentifier, []int{400}, InEnvelope, "A wallet identifier (account number, bank code) is invalid, or there are too many."},
	{BalanceNotEditable, []int{400}, InEnvelope, "The balance can't be set directly; use adjust-balance."},
	{BalanceNotTracked, []int{409}, InEnvelope, "The wallet doesn't track its balance; turn track_balance on first."},
	{NoOp, []int{400}, InEnvelope, "Adjust balance: the new balance equals the current one."},
	{WalletNotTracked, []int{400}, InEnvelope | InPendingLastError, "The wallet doesn't track its balance (savings need a tracked wallet)."},
	{RowLocked, []int{403}, InEnvelope, "Membership on the wallet ended; the row is read-only for the caller."},
	{ContactNotLinked, []int{422}, InEnvelope, "Invite: the contact isn't linked to an app account."},

	// categories / tags
	{CategoryNameDuplicate, []int{422}, InEnvelope, "A category with that name already exists at this level (details.field = name)."},
	{InvalidParent, []int{400}, InEnvelope, "The parent category is invalid (wrong owner / type / archived / system)."},
	{CycleDetected, []int{400}, InEnvelope, "The parent change would create a cycle."},
	{MaxDepthExceeded, []int{400}, InEnvelope, "The category tree would be deeper than 3 levels."},
	{SystemCategoryImmutable, []int{400}, InEnvelope, "A system category can't be changed this way."},
	{SystemCategoryNotAllowed, []int{400}, InEnvelope, "A system category can't be picked; use a user category."},
	{CategoryTypeMismatch, []int{400}, InEnvelope | InPendingLastError, "The category type does not match the transaction type."},
	{CategoryAuthorOnly, []int{403}, InEnvelope, "Only the row's author may change its category."},
	{InvalidCategory, []int{400}, InEnvelope, "The category can't be used here (budgets: own expense categories; recurring: matching type)."},
	{CategoryNotFound, nil, InPendingLastError, "Pending: the draft's category is missing or not usable."},
	{TagNotFound, []int{400}, InEnvelope | InPendingLastError, "A tag id in the body is not the caller's."},
	{TagExists, []int{409}, InEnvelope, "A tag with that name already exists (batch: details {op, index})."},
	{TagNameTaken, []int{409}, InEnvelope, "The event already has a tag with that name."},

	// transactions
	{TransactionNotFound, []int{404}, InEnvelope | InPendingLastError, "The transaction does not exist or is not the caller's."},
	{TxNotFound, []int{404}, InEnvelope, "Events quick-create: the transaction does not exist or is not the caller's."},
	{TransferSameAccount, []int{400}, InEnvelope | InPendingLastError, "A transfer's source and destination wallets must differ."},
	{TransferNeedsWallets, nil, InPendingLastError, "Pending: a transfer needs both wallets."},
	{SystemTransactionImmutable, []int{400}, InEnvelope, "A system transaction (opening balance, adjustment) can't be edited or deleted directly."},
	{SplitsNotSupportedYet, []int{400}, InEnvelope, "Splits are not supported on this operation."},
	{SplitsOnTransfer, []int{400}, InEnvelope | InPendingLastError, "A transfer can't have splits."},
	{SplitsAuthorOnly, []int{403}, InEnvelope, "Only the transaction's author can change its splits."},
	{SplitsExceedAmount, []int{400}, InEnvelope | InPendingLastError, "The splits add up to more than the amount."},
	{SplitsExceedShare, []int{400}, InEnvelope, "The splits add up to more than the caller's share of the event bill."},
	{SplitMismatch, []int{400}, InEnvelope, "Savings: the wallet splits must add up to the amount."},
	{InvalidSplit, nil, InPendingLastError, "Pending: a split needs a person name and an amount above 0."},
	{PaidByExpenseOnly, []int{400}, InEnvelope | InPendingLastError, "paid_by is only for an expense or an income."},
	{PaidByExceedsAmount, []int{400}, InEnvelope | InPendingLastError, "paid_by.amount is more than the bill."},
	{InvalidPaidBy, nil, InPendingLastError, "Pending: paid_by needs a person and an amount above 0."},
	{LinkModeConflict, []int{400}, InEnvelope | InPendingLastError, "A bill is shared one way at a time: splits, paid_by or an event."},
	{LinkModeInput, []int{400}, InEnvelope, "link_mode is missing its data (split -> splits, paid -> paid_by, event -> event.project_id)."},
	{SharedBillField, []int{409}, InEnvelope, "A copy of a shared bill: this field is edited in the bill form (details.fields)."},
	{PrivateNoteNotShared, []int{400}, InEnvelope, "private_note is only for a copy of a shared bill."},
	{SourcePtNotFound, []int{404}, InEnvelope, "Copy from event: the source event row was not found."},
	{SourcePtNotForUser, []int{403}, InEnvelope, "Copy from event: the caller is not a member of the source event."},
	{InvalidPersonKey, []int{400}, InEnvelope, "A person key must be c:<contact_id> or n:<name>."},

	// debts
	{EntryNotFound, []int{400}, InEnvelope, "A debt entry in the body is not one of this person's."},
	{EntryManagedBySource, []int{409}, InEnvelope, "The entry comes from a split; only description, note and the paid label can change here."},
	{EntryHasTransaction, []int{409}, InEnvelope, "The entry already has a transaction; delete or disconnect it first."},
	{DebtManagedByTx, []int{409}, InEnvelope, "The debt is the paid-by part of a transaction; edit the transaction instead."},
	{DebtNotLinked, []int{409}, InEnvelope, "The debt has no copy in another user's book."},
	{DebtTxAlreadyLinked, []int{409}, InEnvelope, "The transaction is already linked to another debt entry."},
	{DebtTxAuthorOnly, []int{403}, InEnvelope, "Only its author can change a debt entry's transaction."},
	{DebtTxNeedsWallet, []int{400}, InEnvelope, "A debt entry's transaction keeps its wallet; edit the entry to change it."},
	{TxNotLinkable, []int{400}, InEnvelope, "This transaction can't be linked to this entry."},
	{TxNotDisconnectable, []int{409}, InEnvelope, "Only a transaction connected later can be disconnected; delete it instead."},
	{TagsNeedWallet, []int{400}, InEnvelope, "Tags go on the wallet transaction; pick a wallet first."},
	{LedgerOnlyNoWallet, []int{400}, InEnvelope, "A ledger-only entry has no wallet or tags; turn create_transaction on."},
	{BillNotFound, []int{400}, InEnvelope, "The bill is not in an event the caller is a member of."},
	{UseClaim, []int{400}, InEnvelope, "bill_id / event_id are retired here; claim the bills on the event."},
	{DebtNotFound, nil, InPendingLastError, "Pending: the target debt entry was not found."},

	// contacts
	{ContactNotFound, []int{404, 400}, InEnvelope | InPendingLastError, "The contact does not exist or is not the caller's (400 when it is referenced in a body)."},
	{ContactArchived, []int{409, 400}, InEnvelope, "The contact is archived."},
	{AlreadyArchived, []int{400}, InEnvelope, "The contact is already archived."},
	{NotArchived, []int{400}, InEnvelope, "The contact is not archived."},
	{AlreadyLinked, []int{409}, InEnvelope, "The contact is already linked."},
	{NotLinked, []int{400, 409}, InEnvelope, "The contact is not linked (400), or the app is not linked (connections, 409)."},
	{CannotLinkSelf, []int{400}, InEnvelope, "A contact can't be linked to the caller."},
	{RequestRejected, []int{409}, InEnvelope, "The link request was rejected."},
	{NotAccepted, []int{409}, InEnvelope, "Accept the link request first."},
	{StateConflict, []int{409}, InEnvelope, "The link request was already answered."},

	// notifications
	{NotForYou, []int{403}, InEnvelope, "The notification is not addressed to the caller."},
	{NotificationStateConflict, []int{409}, InEnvelope, "The notification was already handled the other way."},
	{NotProjectInvite, []int{400}, InEnvelope, "The notification is not an event invite."},
	{TypeNotMutable, []int{400}, InEnvelope, "Invites and link requests can't be muted."},

	// events (projects)
	{ProjectNotFound, nil, InPendingLastError, "Pending: the event was not found."},
	{ProjectNotActive, []int{409}, InEnvelope | InPendingLastError, "The event is not active."},
	{ProjectLocked, []int{409}, InEnvelope | InPendingLastError, "The event is cancelled or archived; writes are blocked."},
	{ProjectNotMember, nil, InPendingLastError, "Pending: a member is not in the event."},
	{ProjectReadOnly, nil, InPendingLastError, "Pending: viewers can't change event rows."},
	{EventCompleted, []int{409}, InEnvelope, "The event is completed; it can't be changed any more."},
	{EventNotCompleted, []int{409}, InEnvelope, "Settle lines can be booked only after the event is completed."},
	{EventHasEmptyPerson, []int{409}, InEnvelope, "A bill still has an empty person to assign (details.bill_ids)."},
	{UseCompleteEndpoint, []int{400}, InEnvelope, "Complete an event with POST /projects/{id}/complete."},
	{AlreadyMember, []int{409}, InEnvelope, "The user is already a member of the event."},
	{MemberNotInProject, []int{400}, InEnvelope, "The member does not belong to this event."},
	{MemberNotRemoved, []int{409}, InEnvelope, "Only a removed member's spots can be assigned."},
	{NotPending, []int{409}, InEnvelope, "The member isn't waiting for an invite."},
	{CannotChangeOwner, []int{409}, InEnvelope, "The owner's role can't be changed here; use transfer-ownership."},
	{CannotRemoveOwner, []int{400}, InEnvelope, "The owner can't be removed; transfer ownership first."},
	{OwnerCannotLeave, []int{409}, InEnvelope, "The owner can't leave; transfer ownership first."},
	{ForbiddenRole, []int{403}, InEnvelope, "The caller's event role can't do this."},
	{PtIsChild, []int{409}, InEnvelope, "A split-child row can't be changed directly; edit its parent."},
	{SelfSplit, []int{400}, InEnvelope, "A split's member must differ from the payer."},
	{NotInBill, []int{400}, InEnvelope, "Claim: no pair with that member on the bill (details.bill_ids)."},
	{BillAlreadyBooked, []int{409}, InEnvelope, "The bill is already booked with that person (details.bill_ids on claim)."},
	{BillChanged, []int{409}, InEnvelope, "The bill changed since it was opened; reload (details.bill)."},
	{BillDisconnected, []int{409}, InEnvelope, "The caller disconnected their copy of this bill."},
	{BillGone, []int{410}, InEnvelope, "The bill was deleted."},
	{MoveTarget, []int{400}, InEnvelope, "Move: exactly one of project_id, new_event or split."},
	{PayersSum, []int{400}, InEnvelope, "The payers must add up to the bill."},
	{PayerFixed, []int{400}, InEnvelope, "A bill's payer can't change."},
	{BadPayback, []int{400}, InEnvelope, "A payback has exactly one split: the receiver, for the whole amount."},
	{PaybackNotCopyable, []int{422}, InEnvelope, "A payback can't be copied as an expense."},
	{NoShareToCopy, []int{422}, InEnvelope, "The caller has no part in this event row to copy."},
	{NoShare, nil, InPendingLastError, "Pending: the caller has no part in the event row to copy."},
	{SettleBooked, []int{409}, InEnvelope, "The caller already booked this event's settle."},
	{SettleLines, []int{400}, InEnvelope, "lines must list each of the caller's fewest-transfers lines once."},
	{ClaimMixedDirection, []int{400}, InEnvelope, "The bills of one claim must all go the same way."},
	{TxAlreadyInProject, []int{409}, InEnvelope, "The transaction is already in an event."},
	{TxNotBillable, []int{422}, InEnvelope | InPendingLastError, "Only expense and income transactions (not system rows or repayments) can be pulled into an event."},
	{TxNotInProject, []int{404}, InEnvelope, "The transaction is not in this event."},
	{TxSplitPaid, []int{422}, InEnvelope, "A split on this transaction is labelled paid."},
	{InvalidDateRange, []int{400}, InEnvelope, "end_date must not be before start_date."},
	{InvalidBillPeople, nil, InPendingLastError, "Pending: payer / members are invalid."},

	// recurring / budgets / saving goals
	{InvalidRule, []int{400}, InEnvelope, "The recurrence / budget period rule is invalid."},
	{InvalidTransition, []int{409}, InEnvelope, "This action doesn't apply in the record's current state."},
	{EndTooFar, []int{400}, InEnvelope, "The end must be at most 2 years after the start."},
	{AmountRequired, []int{400}, InEnvelope, "The occurrence has no amount yet; send one."},
	{AlreadyRecorded, []int{409}, InEnvelope, "The occurrence is already recorded."},
	{NothingOwed, []int{409}, InEnvelope, "Nothing is owed to or by this person now."},
	{ItemAlreadyLinked, []int{409}, InEnvelope, "The item already has a pending draft or a transaction (details: ids, or transaction + other)."},
	{SavingNeedsWallet, []int{400}, InEnvelope, "A saving needs a wallet."},
	{InsufficientSaving, []int{409}, InEnvelope, "Not enough saved there (details.available)."},
	{GoalNotActive, []int{409}, InEnvelope | InPendingLastError, "The saving goal isn't active."},
	{GoalReached, []int{409}, InEnvelope, "The saving goal already holds its target."},
	{GoalHasSavings, []int{409}, InEnvelope, "The saving goal still holds money; close it first."},

	// imports
	{UnsupportedImage, []int{400}, InEnvelope, "The image is not a PNG / JPEG or could not be decoded."},
	{OcrUnavailable, []int{503}, InEnvelope, "OCR is not installed on this server."},
	{OcrTimeout, []int{504}, InEnvelope, "Reading the slip took too long."},

	// pending last_error only
	{MissingType, nil, InPendingLastError, "Pending: the draft has no type."},
	{MissingAmount, nil, InPendingLastError, "Pending: the draft's amount must be above 0."},
	{MissingDate, nil, InPendingLastError, "Pending: the draft has no date."},
	{SubmitFailed, nil, InPendingLastError, "Pending: saving the draft failed for another reason (the cause is logged server-side)."},
	{MissingAccount, nil, InPendingLastError, "Legacy (no longer produced; may remain on stored pending rows): the draft had no wallet."},
	{DebtAlreadySettled, nil, InPendingLastError, "Legacy (no longer produced; may remain on stored pending rows): the debt was already settled."},
	{Overpayment, nil, InPendingLastError, "Legacy (no longer produced; may remain on stored pending rows): the payment was more than owed."},

	// connections / Telegram login (0.4.2 1b-2)
	{LoginCodeInvalid, []int{401}, InEnvelope, "Telegram login: the login link expired or was already used; start again."},
	{TelegramAlreadyLinked, []int{409}, InEnvelope, "Telegram sign-up: this Telegram already has an account; log in instead."},
	{RegistrationClosed, []int{403}, InEnvelope, "Sign-up is closed."},
	{ConnectionUnavailable, []int{409}, InEnvelope, "The app (provider) can't be linked or used for login yet."},
	{RateLimited, []int{429}, InEnvelope, "Too many tries; wait a few minutes."},
	{PermissionUnavailable, []int{409}, InEnvelope, "Connections: that permission can't be turned on for this app."},
	{NoPhoto, []int{404}, InEnvelope, "Connections: the linked app gave no picture."},
	{LastLoginMethod, []int{409}, InEnvelope, "Unlink: it is the account's only way in (no password); set a password first."},
	{ProviderError, []int{502}, InEnvelope, "The linked app did not answer; try again."},

	// idempotent saves (0.4.2-3)
	{IdempotencyKeyReused, []int{422}, InEnvelope, "The Idempotency-Key was already used for a different request."},
	{IdempotencyInProgress, []int{409}, InEnvelope, "The first request with this Idempotency-Key is still running; retry in a moment."},
}

var byCode = func() map[Code]Def {
	m := make(map[Code]Def, len(registry))
	for _, d := range registry {
		m[d.Code] = d
	}
	return m
}()

// All returns every registered code, in registry order.
func All() []Def {
	out := make([]Def, len(registry))
	copy(out, registry)
	return out
}

// Lookup returns the registry row of a code.
func Lookup(c Code) (Def, bool) {
	d, ok := byCode[c]
	return d, ok
}

// Codes returns the codes that can appear in the given place, in registry order.
func Codes(u Use) []Code {
	var out []Code
	for _, d := range registry {
		if d.Use&u != 0 {
			out = append(out, d.Code)
		}
	}
	return out
}

// AllowsStatus reports whether the code is registered with that HTTP status.
func (d Def) AllowsStatus(status int) bool {
	for _, s := range d.Status {
		if s == status {
			return true
		}
	}
	return false
}
