package repository

import (
	"context"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

// TransactionManager provides transaction management capabilities
type TransactionManager interface {
	WithTransaction(ctx context.Context, fn func(ctx context.Context) error) error
	WithTransactionAndRetry(ctx context.Context, fn func(ctx context.Context) error, maxRetries int) error
}

type UserRepository interface {
	Create(user *models.User) error
	GetByID(id uint) (*models.User, error)
	GetByEmail(email string) (*models.User, error)
	// GetByEmailUnscoped includes soft-deleted rows. Required for duplicate-email
	// pre-checks, since the unique index on users.email is not scoped to deleted_at.
	GetByEmailUnscoped(email string) (*models.User, error)
	// UpdateFields writes only the named columns of a live (not soft-deleted)
	// user with a conditional UPDATE. It never inserts and reports
	// gorm.ErrRecordNotFound when no live row matched. There is deliberately
	// no full-struct update: GORM's Save upserts when its UPDATE matches
	// nothing, which resurrected erased accounts.
	UpdateFields(id uint, fields map[string]interface{}) error
	Delete(id uint) error
	List(offset, limit int) ([]models.User, error)
	ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.User, error)
	Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.User, error)
	CountSearch(query string) (int64, error)
	Count() (int64, error)
	UpdateLastLogin(id uint) error
	WithTx(tx *gorm.DB) UserRepository
}

type LeadRepository interface {
	Create(lead *models.Lead) error
	GetByID(id uint) (*models.Lead, error)
	GetByIDWithPreloads(id uint, preloads ...string) (*models.Lead, error)
	GetByExternalID(externalID string) (*models.Lead, error)
	GetByOwnerID(ownerID uint, offset, limit int) ([]models.Lead, error)
	GetByClassification(classification models.LeadClassification, offset, limit int) ([]models.Lead, error)
	Update(lead *models.Lead) error
	Delete(id uint) error
	ListWithPreloads(offset, limit int, preloads ...string) ([]models.Lead, error)
	ListSortedWithPreloads(offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Lead, error)
	Search(query string, offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Lead, error)
	CountSearch(query string) (int64, error)
	Count() (int64, error)
	CountByClassification(classification models.LeadClassification) (int64, error)
	CountByOwnerID(ownerID uint) (int64, error)
	ConvertToCustomer(leadID uint, customerID uint) error
	// Dashboard analytics. CountByStatus groups on a plain column, which both
	// MySQL and SQLite handle identically; every time-based aggregation is a
	// plain range scan whose results are bucketed in Go, because no date
	// function is portable across the two drivers.
	// A nil ownerID on the recent listings means every owner; a non-nil one
	// narrows the query to that owner before the limit is applied.
	CountByStatus() (map[string]int64, error)
	ListRecent(ownerID *uint, limit int) ([]models.Lead, error)
	ListRecentlyConverted(ownerID *uint, limit int) ([]models.Lead, error)
	ConversionTimestampsSince(since time.Time) ([]time.Time, error)
	// GetLatestByEmail returns the most recently created live lead with this
	// address, or gorm.ErrRecordNotFound when there is none. It backs the form
	// module's lead deduplication: a second submission from the same person
	// must append to the lead that already exists instead of forking a new one.
	GetLatestByEmail(email string) (*models.Lead, error)
	WithTx(tx *gorm.DB) LeadRepository
}

type CustomerRepository interface {
	Create(customer *models.Customer) error
	GetByID(id uint) (*models.Customer, error)
	GetByEmail(email string) (*models.Customer, error)
	// GetByEmailUnscoped includes soft-deleted rows. Required for duplicate-email
	// pre-checks, since the unique index on customers.email is not scoped to deleted_at.
	GetByEmailUnscoped(email string) (*models.Customer, error)
	GetByUserID(userID uint) (*models.Customer, error)
	Update(customer *models.Customer) error
	Delete(id uint) error
	List(offset, limit int) ([]models.Customer, error)
	ListSortedWithPreloads(offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Customer, error)
	Search(query string, offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Customer, error)
	// ListAllForExport returns every matching customer with no pagination, read
	// in batches. Backs the CSV export, where a page boundary would silently
	// truncate the file.
	ListAllForExport(search, sortBy, sortOrder string) ([]models.Customer, error)
	CountSearch(query string) (int64, error)
	Count() (int64, error)
	WithTx(tx *gorm.DB) CustomerRepository
}

// TicketAssigneeScope narrows a ticket collection to the tickets assigned to
// one user, optionally intersected with one customer and a free-text search.
// It is how support callers are scoped: the predicate is part of the query, so
// it applies before offset and limit, and the matching count uses the same
// predicate. An AssignedToID of zero matches no ticket.
type TicketAssigneeScope struct {
	AssignedToID uint
	CustomerID   *uint
	Search       string
}

type TicketRepository interface {
	Create(ticket *models.Ticket) error
	GetByID(id uint) (*models.Ticket, error)
	GetByCustomerID(customerID uint, offset, limit int) ([]models.Ticket, error)
	GetByAssignedToID(assignedToID uint, offset, limit int) ([]models.Ticket, error)
	Update(ticket *models.Ticket) error
	Delete(id uint) error
	List(offset, limit int) ([]models.Ticket, error)
	ListSortedWithPreloads(offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Ticket, error)
	Search(query string, offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Ticket, error)
	CountSearch(query string) (int64, error)
	Count() (int64, error)
	CountByCustomerID(customerID uint) (int64, error)
	CountByAssignedToID(assignedToID uint) (int64, error)
	CountOpen() (int64, error)
	// Assignee-scoped collections. Rows are ordered by the validated sort
	// column, or by id when none is given, with id as the tie-break.
	ListByAssigneeScope(scope TicketAssigneeScope, offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Ticket, error)
	CountByAssigneeScope(scope TicketAssigneeScope) (int64, error)
	// Dashboard analytics. A nil assignedToID means every ticket; a non-nil one
	// narrows the query to that assignee before the limit is applied.
	CountByPriority() (map[string]int64, error)
	ListRecent(assignedToID *uint, limit int) ([]models.Ticket, error)
	ListRecentlyResolved(assignedToID *uint, limit int) ([]models.Ticket, error)
	CreateComment(comment *models.TicketComment) error
	ListComments(ticketID uint, includeInternal bool) ([]models.TicketComment, error)
	WithTx(tx *gorm.DB) TicketRepository
}

type TaskRepository interface {
	Create(task *models.Task) error
	GetByID(id uint) (*models.Task, error)
	GetByAssignedToID(assignedToID uint, offset, limit int) ([]models.Task, error)
	Update(task *models.Task) error
	Delete(id uint) error
	List(offset, limit int) ([]models.Task, error)
	ListSortedWithPreloads(offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Task, error)
	Search(query string, offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Task, error)
	CountSearch(query string) (int64, error)
	Count() (int64, error)
	CountByAssignedToID(assignedToID uint) (int64, error)
	CountPending() (int64, error)
	// Dashboard analytics. A nil assignedToID means "every assignee"; a non-nil
	// one narrows the result to that user, which is how the non-admin scoping is
	// pushed down to SQL instead of being filtered after the fact.
	CountByStatus() (map[string]int64, error)
	ListUpcoming(assignedToID *uint, limit int) ([]models.Task, error)
	ListDueBetween(assignedToID *uint, from, to time.Time, limit int) ([]models.Task, error)
	ListRecentlyCompleted(assignedToID *uint, limit int) ([]models.Task, error)
	// CreateWithLabels and UpdateWithLabels write the task and its label set in
	// one transaction. Update replaces the whole set — passing an empty slice
	// clears it — so a caller that must leave the labels alone uses Update.
	CreateWithLabels(task *models.Task, labels []models.Label) error
	UpdateWithLabels(task *models.Task, labels []models.Label) error
	// ListByLabel and CountByLabel narrow to the tasks carrying one label. A nil
	// assignedToID means every assignee, matching ListUpcoming's convention, so
	// the non-admin scoping is pushed down to SQL.
	ListByLabel(labelID uint, assignedToID *uint, offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Task, error)
	CountByLabel(labelID uint, assignedToID *uint) (int64, error)
	WithTx(tx *gorm.DB) TaskRepository
}

type LabelRepository interface {
	Create(label *models.Label) error
	// GetByID returns the label with TaskCount populated.
	GetByID(id uint) (*models.Label, error)
	Update(label *models.Label) error
	// Delete removes the label permanently (labels do not soft-delete) and
	// clears its task_labels rows in the same transaction.
	Delete(id uint) error
	// List returns every label ordered by name, each with TaskCount populated.
	List() ([]models.Label, error)
	FindByIDs(ids []uint) ([]models.Label, error)
	// ExistsByNameInsensitive backs the service's duplicate-name pre-check.
	// excludeID is the row an update is allowed to collide with; 0 for a create.
	ExistsByNameInsensitive(name string, excludeID uint) (bool, error)
	WithTx(tx *gorm.DB) LabelRepository
}

// CompanyRepository backs the companies module. Companies hold no personal
// data, so Delete is a plain soft delete; the links from leads and customers
// are cleared by the service in the same transaction through UnlinkLeads and
// UnlinkCustomers.
type CompanyRepository interface {
	Create(company *models.Company) error
	// GetByID returns the company with Owner preloaded and CustomerCount,
	// LeadCount and DealCount populated.
	GetByID(id uint) (*models.Company, error)
	Update(company *models.Company) error
	// Delete soft-deletes the company and reports gorm.ErrRecordNotFound when
	// no live row matched.
	Delete(id uint) error
	// List returns one page plus the total matching the same search. sortBy is
	// checked against utils.AllowedSortColumns["companies"] (id, name, domain,
	// industry, created_at, updated_at); an unknown column is an error, an
	// empty one means created_at descending.
	List(offset, limit int, search, sortBy, sortOrder string) ([]models.Company, int64, error)
	// ExistsByDomain backs the service's duplicate-domain pre-check over LIVE
	// rows, compared with LOWER(). excludeID is the row an update may collide
	// with; 0 for a create.
	ExistsByDomain(domain string, excludeID uint) (bool, error)
	// UnlinkLeads, UnlinkCustomers and UnlinkDeals set company_id to NULL on
	// every row, soft-deleted ones included, that points at the company.
	UnlinkLeads(companyID uint) error
	UnlinkCustomers(companyID uint) error
	UnlinkDeals(companyID uint) error
	// ListCustomers and ListLeads page through the live rows linked to the
	// company, newest first. A nil ownerID means every owner.
	ListCustomers(companyID uint, offset, limit int) ([]models.Customer, int64, error)
	ListLeads(companyID uint, ownerID *uint, offset, limit int) ([]models.Lead, int64, error)
	WithTx(tx *gorm.DB) CompanyRepository
}

// DealListFilter narrows DealRepository.List. Every pointer that is nil and
// every empty string means "no filter"; Open selects the stages that are
// neither won nor lost. SortBy is checked against
// utils.AllowedSortColumns["deals"] and an empty one means newest first.
type DealListFilter struct {
	Search     string
	Stage      models.DealStage
	CompanyID  *uint
	CustomerID *uint
	OwnerID    *uint
	Open       bool
	SortBy     string
	SortOrder  string
}

// DealPipelineFilter narrows DealRepository.Pipeline. A nil pointer means
// "no filter".
type DealPipelineFilter struct {
	OwnerID   *uint
	CompanyID *uint
}

// DealRepository backs the deals module. Deals hold no personal data, so
// nothing here takes part in the erasure; the stage history is append-only.
type DealRepository interface {
	Create(deal *models.Deal) error
	// GetByID returns the deal with Owner, Company, Customer and Lead
	// preloaded.
	GetByID(id uint) (*models.Deal, error)
	// Update saves every column of the deal, nil pointers included, so a
	// cleared closed_at or lost_reason reaches the database. Associations are
	// not written.
	Update(deal *models.Deal) error
	// Delete soft-deletes the deal and reports gorm.ErrRecordNotFound when no
	// live row matched. History rows stay.
	Delete(id uint) error
	// List returns one page plus the total matching the same filter.
	List(offset, limit int, filter DealListFilter) ([]models.Deal, int64, error)
	// CreateStageChange appends one history row.
	CreateStageChange(change *models.DealStageChange) error
	// ListStageChanges returns the deal's history oldest first with ChangedBy
	// preloaded.
	ListStageChanges(dealID uint) ([]models.DealStageChange, error)
	// Pipeline aggregates the live deals matching the filter per (stage,
	// currency): the count, the sum of amount_cents and the sum of
	// amount_cents × probability, all integer arithmetic in SQL. Only groups
	// that hold a deal are returned, in no particular order.
	Pipeline(filter DealPipelineFilter) ([]models.DealPipelineRow, error)
	// WonBetween aggregates the live won deals whose closed_at lies in
	// [from, to) per currency: the count and the sum of amount_cents. A nil
	// ownerID means every owner. The bounds are bound parameters, converted
	// to UTC, the zone closed_at is written in.
	WonBetween(ownerID *uint, from, to time.Time) ([]models.DealWonRow, error)
	WithTx(tx *gorm.DB) DealRepository
}

// ProductListFilter narrows ProductRepository.List. A nil IsActive and an
// empty string mean "no filter"; Search matches name and SKU. SortBy is
// checked against utils.AllowedSortColumns["products"] and an empty one means
// newest first.
type ProductListFilter struct {
	Search    string
	IsActive  *bool
	Currency  string
	SortBy    string
	SortOrder string
}

// ProductRepository backs the catalogue. Products hold no personal data, so
// nothing here takes part in the erasure; Delete is a plain soft delete and
// quote lines keep referencing the row.
type ProductRepository interface {
	Create(product *models.Product) error
	// GetByID returns the live product; a soft-deleted one is a not-found.
	GetByID(id uint) (*models.Product, error)
	// Update saves every column of the product, zero values included.
	Update(product *models.Product) error
	// Delete soft-deletes the product and reports gorm.ErrRecordNotFound when
	// no live row matched.
	Delete(id uint) error
	// List returns one page plus the total matching the same filter.
	List(offset, limit int, filter ProductListFilter) ([]models.Product, int64, error)
	WithTx(tx *gorm.DB) ProductRepository
}

// QuoteListFilter narrows QuoteRepository.List. Every pointer that is nil and
// every empty string means "no filter". SortBy is checked against
// utils.AllowedSortColumns["quotes"] and an empty one means newest first.
type QuoteListFilter struct {
	Status     models.QuoteStatus
	DealID     *uint
	CustomerID *uint
	OwnerID    *uint
	SortBy     string
	SortOrder  string
}

// QuoteStatusStamps are the timestamps a status claim writes next to the new
// status: sent_at and closed_at, both written as given (nil clears).
type QuoteStatusStamps struct {
	SentAt   *time.Time
	ClosedAt *time.Time
}

// QuoteRepository backs the quotes module. Quotes copy no personal data, so
// nothing here takes part in the erasure; the status history is append-only.
//
// The conditional updates (UpdateDraft, UpdateTotals, ClaimStatus,
// StampEmailed) report how many rows matched their WHERE clause. On MySQL the
// driver counts changed rows rather than matched rows, so each of them
// re-checks its own conditions when the driver reports zero and answers 1
// when the row already held exactly the written values.
type QuoteRepository interface {
	Create(quote *models.Quote) error
	// GetByID returns the live quote with Owner, Customer, Deal and Lines
	// (ordered by position) preloaded.
	GetByID(id uint) (*models.Quote, error)
	// GetByIDForUpdate is the locking read of a write transaction: the bare
	// row, `SELECT ... FOR UPDATE` on MySQL and a plain read on SQLite, where
	// the single connection serialises writers anyway.
	GetByIDForUpdate(id uint) (*models.Quote, error)
	// UpdateDraft writes every scalar column of the quote WHERE the stored
	// status is still draft and returns the matched row count (0 or 1).
	UpdateDraft(quote *models.Quote) (int64, error)
	// UpdateTotals writes the three totals WHERE the status is still draft
	// and returns the matched row count.
	UpdateTotals(id uint, subtotal, discount, total int64) (int64, error)
	// ClaimStatus moves the quote from one status to another with one
	// conditional update (WHERE id = ? AND status = from), writing the stamps
	// with it, and returns the matched row count: exactly one row is a
	// successful claim, zero means another transition got there first.
	ClaimStatus(id uint, from, to models.QuoteStatus, stamps QuoteStatusStamps) (int64, error)
	// StampEmailed sets last_emailed_at WHERE the status is sent or accepted
	// and no history row moved the quote to draft after afterHistoryID, and
	// returns the matched row count.
	StampEmailed(id uint, afterHistoryID uint, at time.Time) (int64, error)
	// Delete soft-deletes the quote and reports gorm.ErrRecordNotFound when no
	// live row matched. Lines and history rows stay.
	Delete(id uint) error
	// List returns one page plus the total matching the same filter, without
	// lines but with Owner, Customer and Deal.
	List(offset, limit int, filter QuoteListFilter) ([]models.Quote, int64, error)

	CreateLine(line *models.QuoteLine) error
	// GetLine returns the line only when it belongs to the quote; a line of
	// another quote is gorm.ErrRecordNotFound.
	GetLine(quoteID, lineID uint) (*models.QuoteLine, error)
	// UpdateLine saves every column of the line, zero values included.
	UpdateLine(line *models.QuoteLine) error
	// DeleteLine removes the line when it belongs to the quote and reports
	// gorm.ErrRecordNotFound otherwise. Lines are not soft-deleted.
	DeleteLine(quoteID, lineID uint) error
	// ListLines returns the quote's lines ordered by position, then id.
	ListLines(quoteID uint) ([]models.QuoteLine, error)
	// CountLines counts the quote's lines with a plain read.
	CountLines(quoteID uint) (int64, error)
	// CountLinesForUpdate counts the quote's lines with a locking read (FOR
	// UPDATE on MySQL, plain on SQLite), so inside a transaction it sees every
	// committed line and no uncommitted one.
	CountLinesForUpdate(quoteID uint) (int64, error)
	// NextLinePosition is max(position) + 1 over the quote's lines, 1 for the
	// first line.
	NextLinePosition(quoteID uint) (int64, error)

	// CreateStatusChange appends one history row.
	CreateStatusChange(change *models.QuoteStatusChange) error
	// ListStatusChanges returns the quote's history oldest first with
	// ChangedBy preloaded.
	ListStatusChanges(quoteID uint) ([]models.QuoteStatusChange, error)
	// LatestStatusChangeID is the highest history id of the quote, 0 when it
	// has none; the marker StampEmailed compares against.
	LatestStatusChangeID(quoteID uint) (uint, error)
	WithTx(tx *gorm.DB) QuoteRepository
}

type APIKeyRepository interface {
	Create(apiKey *models.APIKey) error
	GetByID(id uint) (*models.APIKey, error)
	GetByKeyHash(keyHash string) (*models.APIKey, error)
	GetByUserID(userID uint) ([]models.APIKey, error)
	Update(apiKey *models.APIKey) error
	UpdateLastUsed(id uint) error
	WithTx(tx *gorm.DB) APIKeyRepository
}

// Note: ConfigurationRepository is defined in configuration_repository.go

type RefreshTokenRepository interface {
	Create(token *models.RefreshToken) error
	// GetByTokenHash returns the token only while it is still live: not
	// revoked and unexpired. Anything else is a not-found.
	GetByTokenHash(tokenHash string) (*models.RefreshToken, error)
	// ClaimForRotation revokes the token only while it is still not revoked
	// and strictly unexpired, and succeeds only when exactly one row was
	// affected; otherwise it reports gorm.ErrRecordNotFound. It is the
	// single-use claim of the rotation and runs inside the transaction that
	// inserts the successor.
	ClaimForRotation(id uint) error
	// RevokeByTokenHash revokes unconditionally and idempotently: the logout
	// and invalidation primitive. It is not a claim and never fails because
	// the token was already revoked, expired or unknown.
	RevokeByTokenHash(tokenHash string) error
	RevokeAllForUser(userID uint) error
	DeleteExpired() error
	WithTx(tx *gorm.DB) RefreshTokenRepository
}

type PasswordResetTokenRepository interface {
	Create(token *models.PasswordResetToken) error
	// GetByTokenHash returns the token only while it is still spendable:
	// unused (used_at IS NULL) and unexpired. Anything else is a not-found.
	GetByTokenHash(tokenHash string) (*models.PasswordResetToken, error)
	MarkUsed(id uint) error
	// InvalidateAllForUser marks every outstanding (unused, unexpired) token of
	// the user as used, so only the most recently issued reset link works.
	InvalidateAllForUser(userID uint) error
	DeleteExpired() error
	WithTx(tx *gorm.DB) PasswordResetTokenRepository
}

// BulkOperationRepository records the history row of each bulk status update:
// one bulk_operations row created before the batch runs, its status set to the
// outcome afterwards.
type BulkOperationRepository interface {
	Create(operation *models.BulkOperation) error
	UpdateStatus(id uint, status models.BulkOperationStatus) error
}

// BulkStatusRepository holds the read and write halves of a bulk status change.
// Reading the records and writing the new status are separate operations on
// purpose: the caller has to see every row — does it exist, who owns it, what
// status is it in now — before any row is written, because a bulk status update
// is all-or-nothing.
type BulkStatusRepository interface {
	GetLeadsByIDs(ids []uint) ([]models.Lead, error)
	GetTicketsByIDs(ids []uint) ([]models.Ticket, error)
	GetTasksByIDs(ids []uint) ([]models.Task, error)
	SetLeadStatus(ids []uint, status models.LeadStatus) error
	SetTicketStatus(ids []uint, status models.TicketStatus) error
	SetTaskStatus(ids []uint, status models.TaskStatus) error

	WithTx(tx *gorm.DB) BulkStatusRepository
}

// AEORepository backs the answer-engine-optimization module: the brand profile,
// the tracked prompts, the runs that query the providers, and the answers and
// citations those runs produce.
//
// Every metrics method obeys the same range convention — `from` inclusive, `to`
// exclusive — and is written to mean the same thing on MySQL 8 and SQLite. See
// the DUAL-DATABASE CONSTRAINT comment in aeo_repository.go for what that rules
// out (no date functions, no JSON functions, no aggregate over a time column).
type AEORepository interface {
	// GetProfile returns the single brand-profile row, or gorm.ErrRecordNotFound
	// when the profile has not been configured yet.
	GetProfile() (*models.AEOProfile, error)
	// UpsertProfile inserts or updates the profile's editable fields, preserves
	// created_at, and reloads the stored row into profile. The service pins ID to 1.
	UpsertProfile(profile *models.AEOProfile) error

	CreatePrompt(prompt *models.AEOPrompt) error
	GetPromptByID(id uint) (*models.AEOPrompt, error)
	UpdatePrompt(prompt *models.AEOPrompt) error
	// DeletePrompt soft-deletes the prompt and reports gorm.ErrRecordNotFound
	// when no row matched.
	DeletePrompt(id uint) error
	ListPrompts(activeOnly bool, offset, limit int, sortBy, sortOrder string) ([]models.AEOPrompt, error)
	CountPrompts(activeOnly bool) (int64, error)
	// ListActivePrompts returns every active prompt, unpaginated: it is the run
	// engine's input and the service caps the active set.
	ListActivePrompts() ([]models.AEOPrompt, error)
	// ExistsByTextInsensitive backs the service's duplicate-text pre-check over
	// live rows. excludeID is the row an update may collide with; 0 for create.
	ExistsByTextInsensitive(text string, excludeID uint) (bool, error)

	CreateRun(run *models.AEORun) error
	GetRunByID(id uint) (*models.AEORun, error)
	UpdateRun(run *models.AEORun) error
	ListRuns(offset, limit int, sortBy, sortOrder string) ([]models.AEORun, error)
	CountRuns() (int64, error)
	// GetLatestRun returns the most recently started run, or (nil, nil) when
	// none exists — a fresh install is not an error condition.
	GetLatestRun() (*models.AEORun, error)
	// CountRunsByStatus backs the overlap guard on "running".
	CountRunsByStatus(status string) (int64, error)
	// MarkStaleRunsFailed fails every run left in "running" that started
	// before cutoff, stamping completed_at, and returns the row count. It is
	// how a run stranded by a crash or a restart stops blocking the overlap
	// guard: the executor is in-process, so a running row older than a whole
	// run's worst-case duration belongs to a process that is already gone.
	MarkStaleRunsFailed(cutoff time.Time) (int64, error)

	// CreateAnswerWithCitations writes the answer and its citations in one
	// transaction, filling each citation's AnswerID from the inserted answer.
	CreateAnswerWithCitations(answer *models.AEOAnswer, citations []models.AEOCitation) error
	// ListAnswersByPrompt returns the prompt's answers newest first with their
	// citations preloaded; a nil runID means every run.
	ListAnswersByPrompt(promptID uint, runID *uint, offset, limit int) ([]models.AEOAnswer, error)
	CountAnswersByPrompt(promptID uint, runID *uint) (int64, error)

	// ListAnswerFacts returns one projected fact per answer in the range,
	// oldest first, INCLUDING failed answers (Errored marks them). Per-day
	// bucketing and competitor-mention aggregation happen in Go from this.
	ListAnswerFacts(from, to time.Time) ([]models.AEOAnswerFact, error)
	// PromptVisibility aggregates the given prompts over the range. Answers and
	// Mentions exclude failed answers so Mentions/Answers is the ratio directly;
	// LastRunAt does not, because a failed run still ran. An empty promptIDs
	// returns an empty map.
	PromptVisibility(from, to time.Time, promptIDs []uint) (map[uint]models.AEOPromptVisibility, error)
	// CitationDomainStats groups the citations of non-error answers in the range
	// by domain, most-cited first.
	CitationDomainStats(from, to time.Time) ([]models.AEOCitationAggRow, error)
	// CountAnswersInRange returns the answers in the range and how many of them
	// mentioned the brand. BOTH counts exclude failed answers, so total is a
	// usable rate denominator.
	CountAnswersInRange(from, to time.Time) (total int64, withBrandMention int64, err error)
	// CountAnswersWithCitations counts the non-error answers in the range that
	// carry at least one citation.
	CountAnswersWithCitations(from, to time.Time) (int64, error)

	WithTx(tx *gorm.DB) AEORepository
	// WithContext returns a repository whose statements run under ctx, so
	// cancelling ctx aborts a blocked read or write instead of letting it
	// outlive the run that issued it. The run engine binds its writes to the
	// run context and its finalization to a short, independent cleanup
	// context; a nil ctx returns the receiver unchanged.
	WithContext(ctx context.Context) AEORepository
}

// AutomationRuleListFilter narrows AutomationRepository.ListRules. SortBy is
// checked against utils.AllowedSortColumns["automation_rules"]; an empty one
// means newest first.
type AutomationRuleListFilter struct {
	EntityType string
	Enabled    *bool
	SortBy     string
	SortOrder  string
}

// AutomationRunListFilter narrows AutomationRepository.ListRuns. SortBy is
// checked against utils.AllowedSortColumns["automation_runs"] (id,
// created_at, updated_at, status, resume_at); an empty one means newest first.
type AutomationRunListFilter struct {
	Status     string
	RuleID     *uint
	EntityType string
	EntityID   *uint
	SortBy     string
	SortOrder  string
}

// AutomationRunScope is the role scope of a run listing. A nil OwnerID is the
// admin scope (every run); a non-nil one is the sales scope: only runs whose
// lead or deal the user currently owns, applied inside the SQL before offset
// and limit, so the total counts the same filtered set.
type AutomationRunScope struct {
	OwnerID *uint
}

// AutomationRunAdvance is what a step's conditional advance writes onto the
// run: the status it moves to, the position it continues at, the resume time
// of a wait, and the stamps of a terminal status.
type AutomationRunAdvance struct {
	Status       string
	NextPosition int
	ResumeAt     *time.Time
	FinishedAt   *time.Time
	CancelReason string
	ErrorCode    string
}

// AutomationScheduleQuery selects one batch of a schedule pass: the live
// records of the entity type matching the status (leads) or stage (deals)
// filter, with updated_at before StaleBefore when set, with ids strictly
// above AfterID, in id order, at most Limit of them.
type AutomationScheduleQuery struct {
	EntityType  string
	Status      string
	Stage       string
	StaleBefore *time.Time
	AfterID     uint
	Limit       int
}

// AutomationRepository backs the follow-up automation module: the rules and
// their steps, the outbox events, the runs and their step log, plus the
// locking reads and owner writes the executor makes on leads and deals.
//
// Every conditional update reports the affected-row count and changes a
// column value whenever it matches, so the count is the matched count on
// MySQL too. The locking reads carry FOR UPDATE on MySQL and are plain reads
// on SQLite. Nothing here holds personal data; the record reads return the
// live row for the executor to evaluate and render from.
type AutomationRepository interface {
	// CreateRule inserts the rule and its steps numbered 1..n in order.
	CreateRule(rule *models.AutomationRule) error
	// GetRuleByID returns the live rule with its steps in position order.
	GetRuleByID(id uint) (*models.AutomationRule, error)
	// GetRuleByIDForUpdate is the locking read of a rule write: the bare row.
	GetRuleByIDForUpdate(id uint) (*models.AutomationRule, error)
	ListRules(offset, limit int, filter AutomationRuleListFilter) ([]models.AutomationRule, int64, error)
	// UpdateRule writes the editable columns and the version by primary key.
	UpdateRule(rule *models.AutomationRule) error
	// ReplaceSteps deletes the rule's steps and inserts the given ones 1..n.
	ReplaceSteps(ruleID uint, steps []models.AutomationStep) error
	SetRuleEnabled(id uint, enabled bool) error
	// DeleteRule soft-deletes the rule; gorm.ErrRecordNotFound when none matched.
	DeleteRule(id uint) error
	// ListMatchingRules returns the enabled live rules of the entity and
	// trigger type with their steps.
	ListMatchingRules(entityType, triggerType string) ([]models.AutomationRule, error)
	// ListScheduleRules returns the enabled live schedule rules with steps.
	ListScheduleRules() ([]models.AutomationRule, error)
	// AdvanceScheduleCursor is the conditional update of a schedule pass on
	// (schedule_pass_on, schedule_cursor); it returns the matched row count.
	AdvanceScheduleCursor(ruleID uint, expectedPassOn *string, expectedCursor uint, passOn string, cursor uint, completedOn *string, now time.Time) (int64, error)

	RecordEvent(event *models.AutomationEvent) error
	ListUnprocessedEvents(limit int) ([]models.AutomationEvent, error)
	// ClaimEvent stamps processed_at WHERE it is still NULL and returns the
	// affected row count (0 or 1).
	ClaimEvent(id uint, now time.Time) (int64, error)

	// CreateRunIgnoringDuplicate inserts with clause.OnConflict{DoNothing:
	// true} on the unique dedupe_key and reports whether a row was added.
	CreateRunIgnoringDuplicate(run *models.AutomationRun) (bool, error)
	// GetRunByID returns the run with its step log in position order.
	GetRunByID(id uint) (*models.AutomationRun, error)
	// GetRunByIDForUpdate is the locking read of the bare run row.
	GetRunByIDForUpdate(id uint) (*models.AutomationRun, error)
	// ListRuns returns one page plus the total, the scope applied in SQL.
	ListRuns(offset, limit int, filter AutomationRunListFilter, scope AutomationRunScope) ([]models.AutomationRun, int64, error)
	// RunInScope reports whether the run exists inside the scope.
	RunInScope(id uint, scope AutomationRunScope) (bool, error)
	// ListClaimableRuns returns pending runs and waiting runs whose resume_at
	// is at or before now, in id order.
	ListClaimableRuns(now time.Time, limit int) ([]models.AutomationRun, error)
	// ClaimRun moves the run from fromStatus to running conditionally (a
	// waiting run only once resume_at <= now) and returns the matched count.
	ClaimRun(id uint, fromStatus string, now time.Time) (int64, error)
	// AdvanceRun is UPDATE ... WHERE id = ? AND status = 'running' AND
	// next_position = ?; it returns the matched count.
	AdvanceRun(id uint, expectedPosition int, advance AutomationRunAdvance, now time.Time) (int64, error)
	// ReleaseRun hands a running run back to pending at the same position
	// after a failed attempt, incrementing attempts; the matched count.
	ReleaseRun(id uint, expectedPosition int, now time.Time) (int64, error)
	// CancelRun and the two bulk cancels set status cancelled with the reason
	// on pending, waiting and running runs and return the affected count.
	CancelRun(id uint, reason string, now time.Time) (int64, error)
	CancelRunsForRule(ruleID uint, reason string, now time.Time) (int64, error)
	CancelRunsForEntity(entityType string, entityID uint, reason string, now time.Time) (int64, error)
	ListRunsByStatus(status string) ([]models.AutomationRun, error)
	FailRun(id uint, errorCode string, now time.Time) error
	ResetRunToPending(id uint, now time.Time) error

	CreateRunStep(step *models.AutomationRunStep) error
	// GetRunStep returns the step row at the position or gorm.ErrRecordNotFound.
	GetRunStep(runID uint, position int) (*models.AutomationRunStep, error)
	// LastRunStep returns the highest-positioned step row, or (nil, nil).
	LastRunStep(runID uint) (*models.AutomationRunStep, error)
	UpdateRunStepOutcome(id uint, outcome, errorCode string, details map[string]interface{}) error
	ListRunSteps(runID uint) ([]models.AutomationRunStep, error)

	// GetLeadForUpdate and GetDealForUpdate are the locking reads of a step
	// transaction; the plain Get variants back the dry run and the API.
	GetLeadForUpdate(id uint) (*models.Lead, error)
	GetDealForUpdate(id uint) (*models.Deal, error)
	GetLead(id uint) (*models.Lead, error)
	GetDeal(id uint) (*models.Deal, error)
	SetLeadOwner(id uint, ownerID uint, now time.Time) error
	SetDealOwner(id uint, ownerID uint, now time.Time) error
	ListScheduleCandidateIDs(query AutomationScheduleQuery) ([]uint, error)

	WithTx(tx *gorm.DB) AutomationRepository
}

// NotificationRepository backs the in-app notifications a notify step
// creates. Every read and write is keyed by the recipient, so one user can
// never reach another's rows.
type NotificationRepository interface {
	Create(notification *models.Notification) error
	GetByID(id uint) (*models.Notification, error)
	// ListByUser returns one page of the user's notifications, newest first,
	// plus the total; unreadOnly narrows both to read_at IS NULL.
	ListByUser(userID uint, unreadOnly bool, offset, limit int) ([]models.Notification, int64, error)
	CountUnread(userID uint) (int64, error)
	// MarkRead stamps the user's own unread notification; the affected count.
	MarkRead(id, userID uint, now time.Time) (int64, error)
	MarkAllRead(userID uint, now time.Time) (int64, error)
	WithTx(tx *gorm.DB) NotificationRepository
}

// FormRepository backs the forms module: the form definitions, the submissions
// they collect and the single-use tokens of the double-opt-in flow.
//
// Everything here is deliberately status-blind. Whether a form may be served
// publicly, whether a submission counts as spam and whether a token has already
// been spent are service decisions; this layer only reports what is stored —
// with the one exception of GetConfirmationTokenByHash, whose filter is a
// disclosure guarantee rather than a business rule.
type FormRepository interface {
	Create(form *models.Form) error
	GetByID(id uint) (*models.Form, error)
	// GetByPublicID looks the form up by its public identifier regardless of
	// status, and reports gorm.ErrRecordNotFound when nothing matches.
	GetByPublicID(publicID string) (*models.Form, error)
	// List returns one page plus the total matching the same status filter. An
	// empty status means every status; sortBy is checked against an allowlist
	// and an unknown column is an error, never a silent fallback.
	List(offset, limit int, status string, sortBy, sortOrder string) ([]models.Form, int64, error)
	Update(form *models.Form) error
	// Delete soft-deletes the form and reports gorm.ErrRecordNotFound when no
	// row matched. Submissions are left in place.
	Delete(id uint) error
	// SubmissionCounts counts the submissions of every requested form, spam
	// included. Forms without submissions are absent from the map.
	SubmissionCounts(formIDs []uint) (map[uint]int64, error)

	CreateSubmission(sub *models.FormSubmission) error
	GetSubmissionByID(id uint) (*models.FormSubmission, error)
	// ListSubmissions returns one page of a form's submissions newest first,
	// plus the total matching the same status filter.
	ListSubmissions(formID uint, offset, limit int, status string) ([]models.FormSubmission, int64, error)
	UpdateSubmission(sub *models.FormSubmission) error

	CreateConfirmationToken(t *models.FormConfirmationToken) error
	// GetConfirmationTokenByHash returns the token only while it is still
	// spendable: unused and unexpired. Anything else is a not-found, so used,
	// expired and unknown tokens are indistinguishable to the caller.
	GetConfirmationTokenByHash(hash string) (*models.FormConfirmationToken, error)
	MarkConfirmationTokenUsed(id uint) error
	// InvalidatePendingTokens spends every outstanding token of every pending
	// submission of this form with this address, so only the newest
	// confirmation link keeps working.
	InvalidatePendingTokens(formID uint, email string) error

	WithTx(tx *gorm.DB) FormRepository
}
