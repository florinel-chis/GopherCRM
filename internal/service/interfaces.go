package service

import (
	"context"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
)

type AuthTokens struct {
	AccessToken  string
	RefreshToken string
	// User is the authenticated account the tokens belong to, so handlers can
	// mirror the login response shape without a second lookup. RefreshToken is
	// the raw opaque value — it is returned to the client once and only its
	// hash is persisted; it must never be logged.
	User *models.User
}

type AuthService interface {
	Login(email, password string) (string, error)
	LoginWithTokens(email, password string) (*AuthTokens, error)
	ValidateToken(token string) (*models.User, error)
	ValidateAPIKey(key string) (*models.User, error)
	GenerateJWT(user *models.User) (string, error)
	GenerateTokens(user *models.User) (*AuthTokens, error)
	RefreshAccessToken(refreshToken string) (*AuthTokens, error)
	InvalidateRefreshToken(refreshToken string) error
	// Logout revokes the given refresh token if it belongs to the user, or all
	// of the user's refresh tokens when refreshToken is empty. Idempotent.
	Logout(userID uint, refreshToken string) error
	ChangePassword(userID uint, currentPassword, newPassword string) error
	// RequestPasswordReset never discloses whether the account exists; mail
	// and lookup failures are swallowed by design (anti-enumeration).
	RequestPasswordReset(email string) error
	ConfirmPasswordReset(token, newPassword string) error
	GenerateCSRFToken() (string, error)
	ValidateCSRFToken(token string) bool
}

type UserService interface {
	Register(user *models.User, password string) error
	GetByID(id uint) (*models.User, error)
	GetByEmail(email string) (*models.User, error)
	Update(id uint, updates map[string]interface{}) (*models.User, error)
	Delete(id uint) error
	List(offset, limit int) ([]models.User, int64, error)
	ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.User, int64, error)
	Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.User, int64, error)
}

type LeadService interface {
	Create(lead *models.Lead) error
	GetByID(id uint) (*models.Lead, error)
	GetByExternalID(externalID string) (*models.Lead, error)
	GetByOwner(ownerID uint, offset, limit int) ([]models.Lead, int64, error)
	GetByClassification(classification models.LeadClassification, offset, limit int) ([]models.Lead, int64, error)
	Update(id uint, updates map[string]interface{}) (*models.Lead, error)
	Delete(id uint) error
	List(offset, limit int) ([]models.Lead, int64, error)
	ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.Lead, int64, error)
	Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.Lead, int64, error)
	ConvertToCustomer(leadID uint, customerData *models.Customer) (*models.Customer, error)
	GetCount() (int64, error)
	GetCountByClassification(classification models.LeadClassification) (int64, error)
	// Dashboard analytics.
	GetStatusCounts() (map[string]int64, error)
	GetRecent(limit int) ([]models.Lead, error)
	GetRecentByOwner(ownerID uint, limit int) ([]models.Lead, error)
	GetRecentlyConverted(limit int) ([]models.Lead, error)
	// GetConversionTimestamps returns the conversion time of every lead
	// converted at or after `since`. Callers bucket them in Go: no date function
	// is spelled the same on MySQL 8 and SQLite.
	GetConversionTimestamps(since time.Time) ([]time.Time, error)
}

type CustomerService interface {
	Create(customer *models.Customer) error
	GetByID(id uint) (*models.Customer, error)
	GetByUserID(userID uint) (*models.Customer, error)
	Update(customer *models.Customer) error
	Delete(id uint) error
	List(offset, limit int) ([]models.Customer, int64, error)
	ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.Customer, int64, error)
	Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.Customer, int64, error)
	// ExportAll returns every matching customer, unpaginated, for the admin-only
	// CSV export.
	ExportAll(search, sortBy, sortOrder string) ([]models.Customer, error)
	// Assign sets the staff account owning the customer relationship. The
	// assignee must exist, be active, and hold the admin or sales role.
	Assign(customerID, userID uint) (*models.Customer, error)
	GetCount() (int64, error)
}

type TicketService interface {
	Create(ticket *models.Ticket) error
	GetByID(id uint) (*models.Ticket, error)
	GetByCustomer(customerID uint, offset, limit int) ([]models.Ticket, int64, error)
	GetByAssignee(assigneeID uint, offset, limit int) ([]models.Ticket, int64, error)
	Update(ticket *models.Ticket) error
	Delete(id uint) error
	List(offset, limit int) ([]models.Ticket, int64, error)
	ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.Ticket, int64, error)
	Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.Ticket, int64, error)
	GetOpenCount() (int64, error)
	// Dashboard analytics.
	GetPriorityCounts() (map[string]int64, error)
	GetRecent(limit int) ([]models.Ticket, error)
	GetRecentlyResolved(limit int) ([]models.Ticket, error)
}

type TaskService interface {
	Create(task *models.Task) error
	GetByID(id uint) (*models.Task, error)
	GetByAssignee(assigneeID uint, offset, limit int) ([]models.Task, int64, error)
	Update(task *models.Task) error
	Delete(id uint) error
	List(offset, limit int) ([]models.Task, int64, error)
	ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.Task, int64, error)
	Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.Task, int64, error)
	GetPendingCount() (int64, error)
	// Dashboard analytics. The ByAssignee variants exist so the handlers can
	// narrow non-admin callers to their own assignments without the scoping
	// decision leaking into the repository.
	GetStatusCounts() (map[string]int64, error)
	GetUpcoming(limit int) ([]models.Task, error)
	GetUpcomingByAssignee(assigneeID uint, limit int) ([]models.Task, error)
	GetDueWithin(from, to time.Time, limit int) ([]models.Task, error)
	GetDueWithinByAssignee(assigneeID uint, from, to time.Time, limit int) ([]models.Task, error)
	GetRecentlyCompleted(limit int) ([]models.Task, error)
	// Labels. CreateWithLabels attaches the referenced labels to the new task;
	// an empty or nil slice creates it without any. UpdateWithLabels takes a
	// POINTER so the caller can tell "no label_ids field was sent, leave the set
	// alone" (nil) apart from "label_ids was sent empty, clear the set"
	// (non-nil, empty). An id that matches no label yields
	// apperrors.ErrLabelNotFound, which the handler answers with 400
	// INVALID_REFERENCE.
	CreateWithLabels(task *models.Task, labelIDs []uint) error
	UpdateWithLabels(task *models.Task, labelIDs *[]uint) error
	// ListByLabel and the ByAssignee variant back the ?label_id= filter, with
	// the same admin/non-admin split as the rest of the task listings.
	ListByLabel(labelID uint, offset, limit int, sortBy, sortOrder string) ([]models.Task, int64, error)
	ListByLabelForAssignee(assigneeID, labelID uint, offset, limit int, sortBy, sortOrder string) ([]models.Task, int64, error)
}

type LabelService interface {
	// Create trims the name, validates the colour against ^#[0-9a-fA-F]{6}$ and
	// rejects a name that already exists case-insensitively with
	// apperrors.ErrDuplicateLabelName.
	Create(label *models.Label) error
	GetByID(id uint) (*models.Label, error)
	Update(label *models.Label) error
	// Delete removes the label permanently and detaches it from every task.
	Delete(id uint) error
	// List returns every label ordered by name, each carrying its task count.
	List() ([]models.Label, error)
}

type APIKeyService interface {
	// Generate mints a key for userID. A non-nil expiresAt is stored on the key
	// and enforced at authentication time by AuthService.ValidateAPIKey.
	Generate(userID uint, name string, expiresAt *time.Time) (string, *models.APIKey, error)
	GetByUser(userID uint) ([]models.APIKey, error)
	// GetByID is owner-scoped: a key belonging to another user yields
	// apperrors.ErrForbidden, a missing one apperrors.ErrNotFound.
	GetByID(id uint, userID uint) (*models.APIKey, error)
	// Update applies only the non-nil fields. Owner-scoped like GetByID.
	Update(id uint, userID uint, name *string, isActive *bool) (*models.APIKey, error)
	Revoke(id uint, userID uint) error
	List(userID uint) ([]models.APIKey, error)
}

type BulkOperationService interface {
	// Bulk operation management
	CreateBulkOperation(userID uint, resourceType string, operationType models.BulkOperationType, totalItems int) (*models.BulkOperation, error)
	GetBulkOperation(id uint) (*models.BulkOperation, error)
	GetBulkOperationWithItems(id uint) (*models.BulkOperation, error)
	GetUserBulkOperations(userID uint, offset, limit int) ([]models.BulkOperation, int64, error)
	ListAllBulkOperations(offset, limit int) ([]models.BulkOperation, int64, error)
	UpdateBulkOperationStatus(id uint, status models.BulkOperationStatus) error
	
	// Generic bulk operations
	ProcessBulkCreate(userID uint, resourceType string, request *models.BulkCreateRequest) (*models.BulkResponse, error)
	ProcessBulkUpdate(userID uint, resourceType string, request *models.BulkUpdateRequest) (*models.BulkResponse, error)
	ProcessBulkDelete(userID uint, resourceType string, request *models.BulkDeleteRequest) (*models.BulkResponse, error)
	ProcessBulkAction(userID uint, resourceType string, request *models.BulkActionRequest) (*models.BulkResponse, error)
	
	// User bulk operations
	BulkCreateUsers(request *models.BulkCreateRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkUpdateUsers(request *models.BulkUpdateRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkDeleteUsers(request *models.BulkDeleteRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkActionUsers(request *models.BulkActionRequest, currentUserID uint) (*models.BulkResponse, error)
	
	// Lead bulk operations
	BulkCreateLeads(request *models.BulkCreateRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkUpdateLeads(request *models.BulkUpdateRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkDeleteLeads(request *models.BulkDeleteRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkActionLeads(request *models.BulkActionRequest, currentUserID uint) (*models.BulkResponse, error)
	
	// Customer bulk operations
	BulkCreateCustomers(request *models.BulkCreateRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkUpdateCustomers(request *models.BulkUpdateRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkDeleteCustomers(request *models.BulkDeleteRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkActionCustomers(request *models.BulkActionRequest, currentUserID uint) (*models.BulkResponse, error)
	
	// Task bulk operations
	BulkCreateTasks(request *models.BulkCreateRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkUpdateTasks(request *models.BulkUpdateRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkDeleteTasks(request *models.BulkDeleteRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkActionTasks(request *models.BulkActionRequest, currentUserID uint) (*models.BulkResponse, error)
	
	// Bulk status updates. All-or-nothing, authorized per record against the
	// same rules the single-record update applies.
	BulkSetLeadStatus(actorID uint, actorRole models.UserRole, ids []uint, status models.LeadStatus) (*models.BulkStatusUpdateResult, error)
	BulkSetTicketStatus(actorID uint, actorRole models.UserRole, ids []uint, status models.TicketStatus) (*models.BulkStatusUpdateResult, error)
	BulkSetTaskStatus(actorID uint, actorRole models.UserRole, ids []uint, status models.TaskStatus) (*models.BulkStatusUpdateResult, error)

	// Ticket bulk operations
	BulkCreateTickets(request *models.BulkCreateRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkUpdateTickets(request *models.BulkUpdateRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkDeleteTickets(request *models.BulkDeleteRequest, currentUserID uint) (*models.BulkResponse, error)
	BulkActionTickets(request *models.BulkActionRequest, currentUserID uint) (*models.BulkResponse, error)
}

// AEOService drives the Answer Engine Optimization module: the brand profile,
// the tracked prompts, the runs that query every configured LLM provider, and
// the visibility/citation metrics computed over the recorded answers.
//
// Percentages returned by this service are 0..100 rounded to one decimal, and a
// zero denominator always yields 0 rather than NaN. Metrics windows are
// half-open: `from` inclusive, `to` exclusive, clamped to the most recent 90
// days.
//
// Errors: ErrProfileNotConfigured, ErrDuplicatePrompt, ErrRunInProgress and
// ErrNoProvidersConfigured come from internal/errors; the validation-flavoured
// ErrAEOPromptLimit, ErrAEOInvalidPrompt, ErrAEOInvalidProfile,
// ErrAEOInvalidTrigger and ErrAEOGenerationUnavailable are declared in
// aeo_service.go. All of them are matched with errors.Is.
type AEOService interface {
	// GetProfile returns the single brand profile, or
	// apperrors.ErrProfileNotConfigured when setup has not happened yet.
	GetProfile() (*models.AEOProfile, error)
	// SaveProfile creates or replaces the profile. Aliases and domains are
	// trimmed and de-duplicated, domains are lowercased with any leading
	// "www." stripped, and the row's ID is pinned so the table stays a
	// singleton.
	SaveProfile(profile *models.AEOProfile) (*models.AEOProfile, error)

	// ListPrompts returns a page of prompts, each decorated with the answer
	// count, mention count, visibility percentage and last-run timestamp for
	// the [from, to) window.
	ListPrompts(from, to time.Time, activeOnly bool, offset, limit int, sortBy, sortOrder string) ([]models.AEOPrompt, int64, error)
	// CreatePrompts is all-or-nothing: one duplicate or one prompt over the
	// active-prompt cap saves none of them.
	CreatePrompts(texts []string, createdByID uint) ([]models.AEOPrompt, error)
	// UpdatePrompt applies only the non-nil fields. Re-activating a prompt
	// counts against the active-prompt cap.
	UpdatePrompt(id uint, text *string, isActive *bool) (*models.AEOPrompt, error)
	DeletePrompt(id uint) error
	// GeneratePrompts returns AI-suggested prompts. Not implemented yet — it
	// returns ErrAEOGenerationUnavailable until the AI-assist phase lands.
	GeneratePrompts(ctx context.Context, count int) ([]string, error)

	// StartRun records a run and hands it to the engine on a background
	// goroutine, returning as soon as the row exists. It refuses to start
	// while another run is still going.
	StartRun(ctx context.Context, trigger string, triggeredByID *uint) (*models.AEORun, error)
	// ReconcileRunningRuns fails runs left in "running" by a process that
	// died mid-run and returns how many it recovered. Call it once at
	// startup, before arming the scheduler: without it a single stranded row
	// makes the overlap guard reject every future run.
	ReconcileRunningRuns() (int64, error)
	ListRuns(offset, limit int, sortBy, sortOrder string) ([]models.AEORun, int64, error)
	GetRun(id uint) (*models.AEORun, error)
	// GetPromptAnswers returns the answer transcript for one prompt, newest
	// first, optionally narrowed to a single run.
	GetPromptAnswers(promptID uint, runID *uint, offset, limit int) ([]models.AEOAnswer, int64, error)

	Dashboard(from, to time.Time) (*models.AEODashboard, error)
	Citations(from, to time.Time) (*models.AEOCitationsReport, error)
	// Providers reports the engines this instance can actually query.
	Providers() []models.AEOProviderStatus
}

// FormService drives the forms module: the CRM-side definitions, the public
// rendering and submission pipeline, and the double-opt-in confirmation.
//
// The three public methods are the ones an unauthenticated visitor reaches, and
// they are deliberately tight-lipped. An unknown, unpublished or
// origin-restricted form is apperrors.ErrNotFound with no further explanation;
// a submission that fails a spam layer is stored and answered exactly like a
// genuine one; every confirmation-token rejection is
// ErrInvalidConfirmationToken. Only field-level validation talks back, as
// FieldErrors, which unwraps to apperrors.ErrValidation.
type FormService interface {
	// Create allocates the public identifier, records the author and validates
	// the definition and the lead owner.
	Create(form *models.Form, actorID uint) error
	GetByID(id uint) (*models.Form, error)
	// List returns one page of forms, the per-form submission counts of that
	// page (forms without submissions are absent from the map) and the total
	// matching the status filter.
	List(offset, limit int, status, sortBy, sortOrder string) ([]models.Form, map[uint]int64, int64, error)
	// Update replaces the definition and settings wholesale; the public
	// identifier and the author are immutable and carried over from the stored
	// row.
	Update(id uint, form *models.Form) error
	Delete(id uint) error
	ListSubmissions(formID uint, offset, limit int, status string) ([]models.FormSubmission, int64, error)
	GetSubmission(id uint) (*models.FormSubmission, error)

	// PublicDefinition returns what a visitor's browser needs to render a
	// published form, including a freshly minted time-trap challenge.
	PublicDefinition(publicID, origin string) (*PublicFormDefinition, error)
	// SubmitPublic validates, spam-checks and stores a submission, then creates
	// the lead and sends the mail the form configures — or, for a double-opt-in
	// form, defers all of that to ConfirmSubmission.
	SubmitPublic(publicID string, req *PublicSubmissionRequest, meta SubmissionMeta) (*SubmitOutcome, error)
	// ConfirmSubmission spends a confirmation token exactly once.
	ConfirmSubmission(rawToken string) error
}