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

// TransactionContext allows repositories to work within a transaction
type TransactionContext struct {
	DB *gorm.DB
}

// RepositoryWithTransaction extends base repository with transaction support
type RepositoryWithTransaction interface {
	WithTx(tx *gorm.DB) interface{}
}

type UserRepository interface {
	Create(user *models.User) error
	GetByID(id uint) (*models.User, error)
	GetByEmail(email string) (*models.User, error)
	// GetByEmailUnscoped includes soft-deleted rows. Required for duplicate-email
	// pre-checks, since the unique index on users.email is not scoped to deleted_at.
	GetByEmailUnscoped(email string) (*models.User, error)
	Update(user *models.User) error
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
	GetByOwnerIDWithPreloads(ownerID uint, offset, limit int, preloads ...string) ([]models.Lead, error)
	GetByClassification(classification models.LeadClassification, offset, limit int) ([]models.Lead, error)
	Update(lead *models.Lead) error
	Delete(id uint) error
	List(offset, limit int) ([]models.Lead, error)
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
	CountByStatus() (map[string]int64, error)
	ListRecent(ownerID *uint, limit int) ([]models.Lead, error)
	ListRecentlyConverted(limit int) ([]models.Lead, error)
	ConversionTimestampsSince(since time.Time) ([]time.Time, error)
	WithTx(tx *gorm.DB) LeadRepository
}

type CustomerRepository interface {
	Create(customer *models.Customer) error
	GetByID(id uint) (*models.Customer, error)
	GetByIDWithPreloads(id uint, preloads ...string) (*models.Customer, error)
	GetByEmail(email string) (*models.Customer, error)
	// GetByEmailUnscoped includes soft-deleted rows. Required for duplicate-email
	// pre-checks, since the unique index on customers.email is not scoped to deleted_at.
	GetByEmailUnscoped(email string) (*models.Customer, error)
	GetByUserID(userID uint) (*models.Customer, error)
	Update(customer *models.Customer) error
	Delete(id uint) error
	List(offset, limit int) ([]models.Customer, error)
	ListWithPreloads(offset, limit int, preloads ...string) ([]models.Customer, error)
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

type TicketRepository interface {
	Create(ticket *models.Ticket) error
	GetByID(id uint) (*models.Ticket, error)
	GetByIDWithPreloads(id uint, preloads ...string) (*models.Ticket, error)
	GetByCustomerID(customerID uint, offset, limit int) ([]models.Ticket, error)
	GetByCustomerIDWithPreloads(customerID uint, offset, limit int, preloads ...string) ([]models.Ticket, error)
	GetByAssignedToID(assignedToID uint, offset, limit int) ([]models.Ticket, error)
	GetByAssignedToIDWithPreloads(assignedToID uint, offset, limit int, preloads ...string) ([]models.Ticket, error)
	Update(ticket *models.Ticket) error
	Delete(id uint) error
	List(offset, limit int) ([]models.Ticket, error)
	ListWithPreloads(offset, limit int, preloads ...string) ([]models.Ticket, error)
	ListSortedWithPreloads(offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Ticket, error)
	Search(query string, offset, limit int, sortBy, sortOrder string, preloads ...string) ([]models.Ticket, error)
	CountSearch(query string) (int64, error)
	Count() (int64, error)
	CountByCustomerID(customerID uint) (int64, error)
	CountByAssignedToID(assignedToID uint) (int64, error)
	CountOpen() (int64, error)
	// Dashboard analytics.
	CountByPriority() (map[string]int64, error)
	ListRecent(limit int) ([]models.Ticket, error)
	ListRecentlyResolved(limit int) ([]models.Ticket, error)
	WithTx(tx *gorm.DB) TicketRepository
}

type TaskRepository interface {
	Create(task *models.Task) error
	GetByID(id uint) (*models.Task, error)
	GetByIDWithPreloads(id uint, preloads ...string) (*models.Task, error)
	GetByAssignedToID(assignedToID uint, offset, limit int) ([]models.Task, error)
	GetByAssignedToIDWithPreloads(assignedToID uint, offset, limit int, preloads ...string) ([]models.Task, error)
	Update(task *models.Task) error
	Delete(id uint) error
	List(offset, limit int) ([]models.Task, error)
	ListWithPreloads(offset, limit int, preloads ...string) ([]models.Task, error)
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
	ListRecentlyCompleted(limit int) ([]models.Task, error)
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

type APIKeyRepository interface {
	Create(apiKey *models.APIKey) error
	GetByID(id uint) (*models.APIKey, error)
	GetByKeyHash(keyHash string) (*models.APIKey, error)
	GetByUserID(userID uint) ([]models.APIKey, error)
	Update(apiKey *models.APIKey) error
	Delete(id uint) error
	UpdateLastUsed(id uint) error
	WithTx(tx *gorm.DB) APIKeyRepository
}

// Note: ConfigurationRepository is defined in configuration_repository.go

type RefreshTokenRepository interface {
	Create(token *models.RefreshToken) error
	GetByTokenHash(tokenHash string) (*models.RefreshToken, error)
	GetByUserID(userID uint) ([]models.RefreshToken, error)
	RevokeByTokenHash(tokenHash string) error
	RevokeAllByUserID(userID uint) error
	RevokeAllForUser(userID uint) error
	DeleteExpired() error
	DeleteByTokenHash(tokenHash string) error
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

type BulkOperationRepository interface {
	Create(operation *models.BulkOperation) error
	GetByID(id uint) (*models.BulkOperation, error)
	GetByIDWithItems(id uint) (*models.BulkOperation, error)
	GetByUserID(userID uint, offset, limit int) ([]models.BulkOperation, error)
	Update(operation *models.BulkOperation) error
	UpdateStatus(id uint, status models.BulkOperationStatus) error
	Delete(id uint) error
	Count() (int64, error)
	CountByUserID(userID uint) (int64, error)
	List(offset, limit int) ([]models.BulkOperation, error)
	CreateItem(item *models.BulkOperationItem) error
	UpdateItem(item *models.BulkOperationItem) error
	GetItemsByOperationID(operationID uint) ([]models.BulkOperationItem, error)
	WithTx(tx *gorm.DB) BulkOperationRepository
}

type BulkRepository interface {
	// User bulk operations
	BulkCreateUsers(users []models.User) ([]models.User, []error)
	BulkUpdateUsers(updates []models.BulkUpdateItem) ([]models.User, []error)
	BulkDeleteUsers(ids []uint) []error

	// Lead bulk operations
	BulkCreateLeads(leads []models.Lead) ([]models.Lead, []error)
	BulkUpdateLeads(updates []models.BulkUpdateItem) ([]models.Lead, []error)
	BulkDeleteLeads(ids []uint) []error

	// Customer bulk operations
	BulkCreateCustomers(customers []models.Customer) ([]models.Customer, []error)
	BulkUpdateCustomers(updates []models.BulkUpdateItem) ([]models.Customer, []error)
	BulkDeleteCustomers(ids []uint) []error

	// Task bulk operations
	BulkCreateTasks(tasks []models.Task) ([]models.Task, []error)
	BulkUpdateTasks(updates []models.BulkUpdateItem) ([]models.Task, []error)
	BulkDeleteTasks(ids []uint) []error

	// Ticket bulk operations
	BulkCreateTickets(tickets []models.Ticket) ([]models.Ticket, []error)
	BulkUpdateTickets(updates []models.BulkUpdateItem) ([]models.Ticket, []error)
	BulkDeleteTickets(ids []uint) []error

	// Bulk status updates. Reading the records and writing the new status are
	// separate operations on purpose: the caller has to see every row — does it
	// exist, who owns it, what status is it in now — before any row is written,
	// because a bulk status update is all-or-nothing.
	GetLeadsByIDs(ids []uint) ([]models.Lead, error)
	GetTicketsByIDs(ids []uint) ([]models.Ticket, error)
	GetTasksByIDs(ids []uint) ([]models.Task, error)
	SetLeadStatus(ids []uint, status models.LeadStatus) error
	SetTicketStatus(ids []uint, status models.TicketStatus) error
	SetTaskStatus(ids []uint, status models.TaskStatus) error

	WithTx(tx *gorm.DB) BulkRepository
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
	// UpsertProfile inserts or replaces the profile row whole. The service pins
	// its ID to 1.
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
	// ListAnswersByRun returns one run's answers oldest first, citations
	// preloaded.
	ListAnswersByRun(runID uint) ([]models.AEOAnswer, error)

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
}
