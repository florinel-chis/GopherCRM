package service

import (
	"context"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/repository"
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

// Authentication methods recorded by the auth middleware under
// AuthMethodContextKey.
const (
	AuthMethodContextKey = "auth_method"
	AuthMethodBearer     = "bearer"
	AuthMethodAPIKey     = "api_key"
	// AuthMethodOIDC is recorded on the role audit row of an OpenID Connect
	// sign-in (#435), whose mapped role is applied by the sign-in itself.
	AuthMethodOIDC = "oidc"
	// AuthMethodSCIM is recorded on the role audit row of a SCIM 2.0 user
	// create, replace or patch (#470, #471), with actor_user_id NULL: the
	// provisioning client is no user.
	AuthMethodSCIM = "scim"
	// AuthMethodMigration is recorded on the role audit row of a startup or
	// migration data step, with actor_user_id NULL.
	AuthMethodMigration = models.RoleAuditAuthMethodMigration
)

// SignedInAtContextKey holds the sign-in time (a time.Time) of a Bearer
// request whose access token carries auth_time, set by the auth middleware
// (#532). It is absent for an ApiKey request and for a token from a refresh.
const SignedInAtContextKey = "signed_in_at"

type AuthService interface {
	Login(email, password string) (string, error)
	LoginWithTokens(email, password string) (*AuthTokens, error)
	ValidateToken(token string) (*models.User, error)
	ValidateAPIKey(key string) (*models.User, error)
	GenerateJWT(user *models.User) (string, error)
	// SignedInAt returns the sign-in time (auth_time claim) of an access
	// token that verifies, and false when the token does not verify or has
	// no numeric claim: tokens from a refresh carry none (#532).
	SignedInAt(token string) (time.Time, bool)
	GenerateTokens(user *models.User) (*AuthTokens, error)
	RefreshAccessToken(refreshToken string) (*AuthTokens, error)
	InvalidateRefreshToken(refreshToken string) error
	// Logout revokes the given refresh token if it belongs to the user, or all
	// of the user's refresh tokens when refreshToken is empty. Idempotent.
	Logout(userID uint, refreshToken string) error
	ChangePassword(userID uint, currentPassword, newPassword, refreshToken string) (*AuthTokens, error)
	// RequestPasswordReset never discloses whether the account exists; mail
	// and lookup failures are swallowed by design (anti-enumeration).
	RequestPasswordReset(email string) error
	// IssuePasswordReset is RequestPasswordReset with its result (#550): the
	// account's id once a reset link was stored, otherwise a refusal that
	// carries the audit reason. The caller's answer must not depend on it.
	IssuePasswordReset(email string) (uint, error)
	ConfirmPasswordReset(token, newPassword string) error
	// CompletePasswordReset is ConfirmPasswordReset returning the owner's id
	// on success (#550).
	CompletePasswordReset(token, newPassword string) (uint, error)
}

type UserService interface {
	Register(user *models.User, password string) error
	GetByID(id uint) (*models.User, error)
	GetByEmail(email string) (*models.User, error)
	Update(id uint, updates map[string]interface{}) (*models.User, error)
	// CheckOwnEmailChange is the guard the user routes run before an account
	// writes its own profile (#394). It answers nil when email is empty or is
	// the stored one, and then never looks at currentPassword; otherwise
	// ErrCurrentPasswordRequired for an empty currentPassword and
	// ErrCurrentPasswordIncorrect for one that does not match the stored
	// hash. A missing account is ErrNotFound. It writes nothing and counts
	// nothing toward the login lockout.
	CheckOwnEmailChange(id uint, email, currentPassword string) error
	Delete(ctx context.Context, id uint) error
	List(offset, limit int) ([]models.User, int64, error)
	ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.User, int64, error)
	Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.User, int64, error)
	// ListFiltered lists one page of the users matching filter and the number
	// the same filter matches before pagination (GET /users?role=, #428).
	ListFiltered(filter repository.UserListFilter, offset, limit int, sortBy, sortOrder string) ([]models.User, int64, error)

	// The actor-taking writes of the user routes (#259 S4, #348). On a
	// service built with WithUserRoleGovernance each is one transaction that
	// locks the active admin set, the actor and the target (ascending id) and
	// their custom roles (ascending id), re-reads the actor's permissions and
	// then refuses, in this order: an actor that lost the route's key
	// (ErrForbidden), ErrRoleEscalation, ErrCustomerLoginRole, ErrLastAdmin.
	// A changed role or role_id commits with its user_role_changed audit row.
	// Without the option they behave as Register, Update and Delete do.

	// CreateAs is Register by an actor. user.Role custom needs user.RoleID,
	// any other role forbids it (ErrInvalidRoleAssignment); an unknown
	// RoleID is ErrRoleNotFound.
	CreateAs(ctx context.Context, actor Actor, user *models.User, password string) error
	// UpdateAs is Update by an actor. Besides the keys of Update the map may
	// carry "role_id" (*uint) next to "role".
	UpdateAs(ctx context.Context, actor Actor, id uint, updates map[string]interface{}) (*models.User, error)
	// SetActiveAs activates or deactivates an account.
	SetActiveAs(ctx context.Context, actor Actor, id uint, active bool) (*models.User, error)
	// DeleteAs is Delete (the irreversible erasure) by an actor.
	DeleteAs(ctx context.Context, actor Actor, id uint) error
}

// RoleService is the role API of #259 (S4, #348): the permission catalogue,
// custom roles and the audit listing. Every write takes the actor and runs
// as one transaction with its audit row; see role_service.go for the locks
// and the order of the refusals.
type RoleService interface {
	// Catalogue is the permission tree in display order.
	Catalogue() []permissions.Node
	List(offset, limit int, sortBy, sortOrder string) ([]RoleDetail, int64, error)
	// Get answers ErrRoleNotFound for an id no role has.
	Get(id uint) (*RoleDetail, error)
	Create(ctx context.Context, actor Actor, input RoleCreateInput) (*RoleDetail, error)
	Update(ctx context.Context, actor Actor, id uint, input RoleUpdateInput) (*RoleDetail, error)
	Delete(ctx context.Context, actor Actor, id uint) error
	Clone(ctx context.Context, actor Actor, sourceID uint, name, description string) (*RoleDetail, error)
	ListAudit(filter repository.RoleAuditFilter, offset, limit int, sortBy, sortOrder string) ([]models.RoleAuditEntry, int64, error)
}

type LeadService interface {
	Create(lead *models.Lead) error
	GetByID(id uint) (*models.Lead, error)
	// GetByIDContext is GetByID bound to the caller's request: a cancelled
	// request stops waiting for a connection and returns the context's error.
	GetByIDContext(ctx context.Context, id uint) (*models.Lead, error)
	GetByExternalID(externalID string) (*models.Lead, error)
	// ListByExternalID returns up to LeadExternalIDLookupLimit live leads
	// whose external_id equals externalID, lowest id first, owner preloaded.
	ListByExternalID(externalID string) ([]models.Lead, error)
	GetByOwner(ownerID uint, offset, limit int) ([]models.Lead, int64, error)
	GetByClassification(classification models.LeadClassification, offset, limit int) ([]models.Lead, int64, error)
	Update(id uint, updates map[string]interface{}) (*models.Lead, error)
	// Assign makes ownerID the lead's owner, for an actor CanViewLead admits on
	// the locked row: apperrors.ErrForbidden otherwise, a not-found error for a
	// lead that is gone, apperrors.ErrInvalidLeadOwner for an owner that is not
	// an active admin or sales user. The owner is always checked, even when it
	// is the lead's current one.
	Assign(ctx context.Context, leadID, ownerID uint, actor LeadViewer) (*models.Lead, error)
	// Delete erases the lead. Only an admin (actorRole) may erase a converted
	// lead, because that erases the customer it became; anyone else is refused
	// with an apperrors.ErrForbidden and nothing changes.
	Delete(ctx context.Context, id uint, actorRole models.UserRole) error
	List(offset, limit int) ([]models.Lead, int64, error)
	ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.Lead, int64, error)
	Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.Lead, int64, error)
	// ListFiltered lists the leads matching filter for GET /leads with a
	// status filter (#426): rows and total share one predicate, applied before
	// offset and limit. The handler sets filter.OwnerID for callers that see
	// only their own leads.
	ListFiltered(filter repository.LeadListFilter, offset, limit int, sortBy, sortOrder string) ([]models.Lead, int64, error)
	ConvertToCustomer(ctx context.Context, leadID uint, customerData *models.Customer) (*models.Customer, error)
	GetCount() (int64, error)
	GetCountByClassification(classification models.LeadClassification) (int64, error)
	// Dashboard analytics. The ByOwner variants narrow the query to one owner
	// before the limit, which is how sales callers are scoped.
	GetStatusCounts() (map[string]int64, error)
	GetRecent(limit int) ([]models.Lead, error)
	GetRecentByOwner(ownerID uint, limit int) ([]models.Lead, error)
	GetRecentlyConverted(limit int) ([]models.Lead, error)
	GetRecentlyConvertedByOwner(ownerID uint, limit int) ([]models.Lead, error)
	// GetConversionTimestamps returns the conversion time of every lead
	// converted at or after `since`. Callers bucket them in Go: no date function
	// is spelled the same on MySQL 8 and SQLite.
	GetConversionTimestamps(since time.Time) ([]time.Time, error)
	// ExportForActor returns every lead the actor may list, unpaginated, for
	// GET /leads/export: an admin under the list's parameter precedence, a
	// sales user only its own leads in id order with every parameter ignored,
	// and an error matching apperrors.ErrForbidden for any other role, without
	// a read.
	ExportForActor(actor LeadViewer, params LeadExportParams) ([]models.Lead, error)
}

type CustomerService interface {
	Create(customer *models.Customer) error
	GetByID(id uint) (*models.Customer, error)
	GetByUserID(userID uint) (*models.Customer, error)
	Update(customer *models.Customer) error
	Delete(ctx context.Context, id uint) error
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

// CustomerExternalIDService maps integrators' (source, scope, external_id)
// identities to customers (#212). Every method validates its input before any
// read or write (see ValidateCustomerExternalIdentity) and returns
// ErrCustomerExternalIDsUnavailable when the service was built without its
// repository or transaction manager.
type CustomerExternalIDService interface {
	// AddExternalID maps the triple to the live customer in one transaction
	// on ctx: the customer row is locked, then the mapping inserted.
	// apperrors.ErrNotFound for a missing or erased customer,
	// apperrors.ErrCustomerExternalIDTaken for a triple already mapped.
	AddExternalID(ctx context.Context, customerID uint, source, scope, externalID string) (*models.CustomerExternalID, error)
	// RemoveExternalID hard-deletes the customer's mapping;
	// apperrors.ErrNotFound when the customer has no mapping with that id.
	RemoveExternalID(ctx context.Context, customerID, mappingID uint) error
	// LookupCustomer returns the live customer the exact triple maps to, or
	// nil and no error when there is none.
	LookupCustomer(ctx context.Context, source, scope, externalID string) (*models.Customer, error)
}

// ProductExternalIDService manages the (source, scope, external_id) → product
// mappings (#239). Every method validates the triple it receives before any
// read or write (see ValidateProductExternalIdentity) and returns
// ErrProductExternalIDsUnavailable when the service was built without its
// repository or transaction manager.
type ProductExternalIDService interface {
	// AddExternalID maps the triple to the live product in one transaction on
	// ctx: the product row is locked, then the mapping inserted.
	// apperrors.ErrNotFound for a missing or deleted product,
	// apperrors.ErrProductExternalIDTaken for a triple already mapped.
	AddExternalID(ctx context.Context, productID uint, source, scope, externalID string) (*models.ProductExternalID, error)
	// RemoveExternalID hard-deletes the product's mapping;
	// apperrors.ErrNotFound when the product has no mapping with that id.
	RemoveExternalID(ctx context.Context, productID, mappingID uint) error
	// LookupProduct returns the live product (active or not) the exact triple
	// maps to, or nil and no error when there is none.
	LookupProduct(ctx context.Context, source, scope, externalID string) (*models.Product, error)
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
	// Assignee-scoped collections, which is how support callers are listed.
	// Rows and total share one predicate, applied before offset and limit; an
	// empty search lists every assigned ticket.
	ListAssigned(assigneeID uint, search string, offset, limit int, sortBy, sortOrder string) ([]models.Ticket, int64, error)
	GetByCustomerAndAssignee(customerID, assigneeID uint, offset, limit int) ([]models.Ticket, int64, error)
	// ListFiltered lists the tickets matching filter for GET /tickets with a
	// status or priority filter (#218): rows and total share one predicate,
	// applied before offset and limit. The handler sets filter.AssigneeID for
	// support callers.
	ListFiltered(filter repository.TicketListFilter, offset, limit int, sortBy, sortOrder string) ([]models.Ticket, int64, error)
	GetOpenCount() (int64, error)
	// Dashboard analytics. The ByAssignee variants narrow the query to one
	// assignee before the limit, which is how support callers are scoped.
	GetPriorityCounts() (map[string]int64, error)
	GetRecent(limit int) ([]models.Ticket, error)
	GetRecentByAssignee(assigneeID uint, limit int) ([]models.Ticket, error)
	GetRecentlyResolved(limit int) ([]models.Ticket, error)
	GetRecentlyResolvedByAssignee(assigneeID uint, limit int) ([]models.Ticket, error)
	CreateComment(comment *models.TicketComment) error
	// CreateCommentWithCustomerEmail (#219) inserts a staff comment that is
	// not an internal note, its first_responded_at stamp and a pending
	// ticket_mail_deliveries row in one transaction on ctx, then, after the
	// commit and with no transaction held, mails the comment to the ticket
	// customer's current address once and stamps the row. It returns nil
	// whatever the mail outcome; comment.Delivery carries it. It is a
	// transaction root (utils.ErrTransactionRootRequired inside one), refuses
	// with ErrTicketCommentMailUnavailable unless WithTicketCommentMail wired
	// it, and with ErrTicketCommentEmailNotAllowed for an internal note or an
	// author who is neither admin nor support.
	CreateCommentWithCustomerEmail(ctx context.Context, comment *models.TicketComment) error
	ListComments(ticketID uint, includeInternal bool) ([]models.TicketComment, error)
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
	// ListFiltered is GET /tasks with a status or priority filter (#427): one
	// page of the tasks matching filter, with taskListPreloads, and the number
	// of tasks the same filter matches before pagination.
	ListFiltered(filter repository.TaskListFilter, offset, limit int, sortBy, sortOrder string) ([]models.Task, int64, error)
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
	GetRecentlyCompletedByAssignee(assigneeID uint, limit int) ([]models.Task, error)
	// Labels. CreateWithLabels attaches the referenced labels to the new task;
	// an empty or nil slice creates it without any. UpdateWithLabels takes a
	// POINTER so the caller can tell "no label_ids field was sent, leave the set
	// alone" (nil) apart from "label_ids was sent empty, clear the set"
	// (non-nil, empty). An id that matches no label yields
	// apperrors.ErrLabelNotFound, which the handler answers with 400
	// INVALID_REFERENCE.
	//
	// actorID is the user making the change. A lead link the write sets (on
	// an update: one that differs from the stored link, or one the request
	// sent at all, which the caller reports as leadIDSent) must name a lead the
	// actor may see under CanViewLead; a lead that is hidden from the actor
	// yields apperrors.ErrLeadNotFound, exactly like a missing one. A sent
	// lead_id equal to a stored hidden link is refused like any other hidden
	// id, so the answer cannot reveal the link. Create and Update have no
	// actor and so cannot set a lead link at all. Reads return the stored link
	// as is: callers answering a user pass the result through RedactTaskLead /
	// RedactTaskLeads.
	//
	// A deal link follows CanViewDeal. A deal_id the write sets must name a live
	// deal the actor may see, closed deals included; a missing or hidden one
	// yields apperrors.ErrDealNotFound, and a lookup that fails for any other
	// reason returns that error. On an update dealIDSent says whether the
	// request named deal_id: absent keeps the stored link unchecked, a sent id
	// is checked even when it equals the stored one, and nil or 0 clears the
	// link when the actor may see the stored deal. A clear of a hidden link
	// returns no error and keeps it. Reads go through RedactTaskDeal /
	// RedactTaskDeals as well.
	CreateWithLabels(task *models.Task, labelIDs []uint, actorID uint) error
	UpdateWithLabels(task *models.Task, labelIDs *[]uint, leadIDSent, dealIDSent bool, actorID uint) error
	// ListByLabel and the ByAssignee variant back the ?label_id= filter, with
	// the same admin/non-admin split as the rest of the task listings.
	ListByLabel(labelID uint, offset, limit int, sortBy, sortOrder string) ([]models.Task, int64, error)
	ListByLabelForAssignee(assigneeID, labelID uint, offset, limit int, sortBy, sortOrder string) ([]models.Task, int64, error)
	// ListByDeal backs GET /deals/{id}/tasks: apperrors.ErrDealNotFound for a
	// missing or soft-deleted deal, apperrors.ErrForbidden for a deal the viewer
	// may not see, every live task of the deal for an admin and only the tasks
	// assigned to the caller for the sales owner.
	ListByDeal(viewer LeadViewer, dealID uint, offset, limit int, sortBy, sortOrder string) ([]models.Task, int64, error)
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

// CompanyService drives the companies module. Create and Update trim the name,
// normalise the domain (lower case, scheme, path and leading "www." stripped),
// require the website to be an http(s) URL, require owner_id to name a live
// account (apperrors.ErrAssigneeNotFound otherwise) and reject a domain another
// live company already uses with apperrors.ErrDuplicateCompanyDomain. Plain
// validation failures unwrap to apperrors.ErrValidation.
type CompanyService interface {
	Create(company *models.Company) error
	// GetByID returns the company with its owner and its customer and lead
	// counts.
	GetByID(id uint) (*models.Company, error)
	Update(company *models.Company) error
	// Delete clears company_id on every lead and customer pointing at the
	// company and soft-deletes it, in one transaction.
	Delete(id uint) error
	// List returns one page plus the total matching the search; sortBy is
	// checked against the companies allowlist and an unknown column is an error.
	List(offset, limit int, search, sortBy, sortOrder string) ([]models.Company, int64, error)
	// ListCustomers and ListLeads page through the rows linked to the company
	// and report apperrors.ErrNotFound for an unknown company. A nil ownerID
	// means every owner.
	ListCustomers(companyID uint, offset, limit int) ([]models.Customer, int64, error)
	ListLeads(companyID uint, ownerID *uint, offset, limit int) ([]models.Lead, int64, error)
}

// DealService drives the deals module. Every write validates the links
// (company, customer, lead and owner must be live rows, else the matching
// *NotFound sentinel, which the handler answers with 400 INVALID_REFERENCE),
// fills the defaults (currency from configuration, probability from the stage)
// and applies the stage rules: won is 100 and lost is 0 whatever was sent,
// closed_at is set on entering won or lost and cleared on leaving them, and
// lost_reason is kept only while lost. Every stage change, the create
// included, writes one history row in the same transaction.
//
// A lead link that Create or Update checks must also be visible to the acting
// user (actorID) under CanViewLead; a hidden lead is apperrors.ErrLeadNotFound,
// like a missing one. Reads return the stored link as is: callers answering a
// user pass the result through RedactDealLead / RedactDealLeads.
type DealService interface {
	// Create writes the deal and its first history row (from_stage NULL). A
	// nil probability takes the stage's default.
	Create(deal *models.Deal, probability *int, actorID uint) error
	// GetByID returns the deal with owner, company, customer and lead.
	GetByID(id uint) (*models.Deal, error)
	// Update saves the deal as given. When its stage differs from the stored
	// one the change goes through the same transition rules as ChangeStage and
	// records a history row; a nil probability then takes the new stage's
	// default, and otherwise leaves the stored probability alone. leadIDSent
	// reports that the request carried lead_id: a lead link it carries is
	// checked even when it equals the stored one, so a stored link hidden from
	// the actor cannot be confirmed by sending its id back.
	Update(deal *models.Deal, probability *int, leadIDSent bool, actorID uint) error
	// ChangeStage moves the deal to stage. The same stage again is a no-op
	// that writes nothing. A nil lostReason keeps whatever a lost deal had.
	ChangeStage(id uint, stage models.DealStage, probability *int, lostReason *string, actorID uint) (*models.Deal, error)
	// Delete soft-deletes the deal; its history rows stay.
	Delete(id uint) error
	// List returns one page plus the total matching the filter; the sort
	// column is checked against the deals allowlist and an unknown one is an
	// error.
	List(offset, limit int, filter repository.DealListFilter) ([]models.Deal, int64, error)
	// ListByCompany and ListByCustomer page through the deals linked to the
	// record, newest first, and report apperrors.ErrNotFound for an unknown
	// company or customer. A nil ownerID means every owner.
	ListByCompany(companyID uint, ownerID *uint, offset, limit int) ([]models.Deal, int64, error)
	ListByCustomer(customerID uint, ownerID *uint, offset, limit int) ([]models.Deal, int64, error)
	// History returns the stage changes oldest first with the acting user.
	History(dealID uint) ([]models.DealStageChange, error)
	// Pipeline returns every stage in pipeline order with its deal count and
	// per-currency totals (amount and weighted amount) over the live deals
	// matching the filter. The caller's role scopes it: an admin gets the
	// filter as given, a sales user always its own deals (filter.OwnerID is
	// replaced by actorID), and any other role apperrors.ErrForbidden.
	Pipeline(filter repository.DealPipelineFilter, actorID uint, role models.UserRole) (*models.DealPipeline, error)
	// DashboardPipeline is Pipeline without filters plus the deals won in the
	// current calendar month in UTC, with the same role scoping: admin all,
	// sales own, anybody else (support included) apperrors.ErrForbidden.
	DashboardPipeline(actorID uint, role models.UserRole) (*models.DealDashboardPipeline, error)
}

// ProductService drives the catalogue. Create and Update trim the name,
// require it (1 to 200 characters), bound the SKU, the description and the
// price (0 to models.QuoteAmountCentsMax) and require the currency to be three
// upper-case letters, defaulting it to the deals.default_currency setting on
// create. Validation failures unwrap to apperrors.ErrValidation; a missing
// product is apperrors.ErrProductNotFound.
type ProductService interface {
	// Create writes the product. is_active is reported as given; the handler
	// defaults an absent flag to true.
	Create(product *models.Product) error
	GetByID(id uint) (*models.Product, error)
	// Update saves the product as given. Existing quote lines are not touched:
	// they copied the name and the price when they were added.
	Update(product *models.Product) error
	// Delete soft-deletes the product and hard-deletes its external-id
	// mappings in one transaction on ctx (#239), so their triples can be
	// mapped again; quote lines keep their product_id and copied values.
	// apperrors.ErrProductNotFound for a missing or already-deleted product;
	// ErrProductExternalIDsUnavailable when built without
	// WithProductExternalIDs.
	Delete(ctx context.Context, id uint) error
	// List returns one page plus the total matching the filter; the sort
	// column is checked against the products allowlist.
	List(offset, limit int, filter repository.ProductListFilter) ([]models.Product, int64, error)
}

// QuoteCreatePolicy says what the caller of QuoteService.CreateWithPolicy may
// do beyond quoting its own deals for itself. The type lives in models so the
// service mocks, which this package's tests import, can name it.
type QuoteCreatePolicy = models.QuoteCreatePolicy

// QuoteService drives the quotes module. The line input and patch types live
// in models (models.QuoteLineInput, models.QuoteLinePatch) so the shared mock
// in internal/mocks can name them without importing this package. Every write to a quote or its lines
// runs in one transaction that starts with a locking read of the quote (FOR
// UPDATE on MySQL, a plain read on SQLite) and refuses anything but a draft
// with apperrors.ErrQuoteLocked; the totals are recomputed from the lines in
// that transaction and written by a conditional update WHERE status = 'draft'.
// Every status transition is a conditional claim (WHERE status = from) plus
// one history row in one transaction; a lost race or a disallowed pair is
// apperrors.ErrInvalidQuoteTransition. Bound and link failures unwrap to
// apperrors.ErrValidation or to the matching *NotFound sentinel, which the
// handler answers with 400 INVALID_REFERENCE.
type QuoteService interface {
	// Create writes the quote and its first history row (from_status NULL).
	// At least one of deal_id and customer_id is required; with only a deal,
	// the customer is taken from the deal. A deal that is won or lost is
	// apperrors.ErrDealClosed; a sales caller (role) naming another owner's
	// deal or another owner is apperrors.ErrForbidden.
	Create(quote *models.Quote, actorID uint, role models.UserRole) error
	// CreateWithPolicy is Create with its two decisions named one by one, for
	// a caller whose permissions are not those of a system role. Create
	// delegates to it with both set for an admin and neither for anyone else.
	CreateWithPolicy(quote *models.Quote, actorID uint, policy QuoteCreatePolicy) error
	// GetByID returns the quote with its owner, customer, deal and lines.
	GetByID(id uint) (*models.Quote, error)
	// Update saves the editable fields of a draft as given. A currency change
	// once the quote has lines is apperrors.ErrQuoteCurrencyLocked.
	Update(quote *models.Quote) error
	// Delete soft-deletes the quote. With draftOnly (a sales caller) a quote
	// that is not a draft is apperrors.ErrQuoteLocked.
	Delete(id uint, draftOnly bool) error
	// List returns one page plus the total matching the filter; the sort
	// column is checked against the quotes allowlist.
	List(offset, limit int, filter repository.QuoteListFilter) ([]models.Quote, int64, error)
	// ListByDeal and ListByCustomer page through the quotes linked to the
	// record and report apperrors.ErrNotFound for an unknown deal or
	// customer. A nil ownerID means every owner; a non-nil one narrows the
	// rows and the total, and on ListByDeal refuses another owner's deal with
	// apperrors.ErrForbidden.
	ListByDeal(dealID uint, ownerID *uint, offset, limit int, sortBy, sortOrder string) ([]models.Quote, int64, error)
	ListByCustomer(customerID uint, ownerID *uint, offset, limit int, sortBy, sortOrder string) ([]models.Quote, int64, error)
	// AddLine copies the product's name and price onto a new line (unless
	// overridden), computes its total and the quote's, and returns the quote.
	// An inactive or deleted product is apperrors.ErrProductNotFound; another
	// currency apperrors.ErrQuoteCurrencyMismatch.
	AddLine(quoteID uint, input models.QuoteLineInput) (*models.Quote, error)
	// UpdateLine applies the patch to the line and recomputes the totals. A
	// line of another quote is apperrors.ErrQuoteLineNotFound.
	UpdateLine(quoteID, lineID uint, patch models.QuoteLinePatch) (*models.Quote, error)
	// DeleteLine removes the line and recomputes the totals.
	DeleteLine(quoteID, lineID uint) (*models.Quote, error)
	// ChangeStatus moves the quote to `to`. draft→sent reads the line count
	// after the claim and rolls back with apperrors.ErrQuoteHasNoLines on
	// zero; accepted after valid_until is apperrors.ErrQuoteExpired.
	ChangeStatus(id uint, to models.QuoteStatus, actorID uint) (*models.Quote, error)
	// History returns the status changes oldest first with the acting user.
	History(quoteID uint) ([]models.QuoteStatusChange, error)
	// Email renders the quote as plaintext and sends it to the linked
	// customer's current address, then stamps last_emailed_at with a
	// conditional update. See the sentinels in internal/errors for the
	// refusals; a mailer failure is apperrors.ErrMailDelivery. On a service
	// built with NewQuoteServiceWithResponseLinks the email of a sent quote
	// ends with a single-use client response link (#382).
	Email(id uint) (*models.Quote, error)
	// GetPublicResponse shows the quote behind a client response token and
	// never writes, so opening the link does not spend it. Every refusal is
	// apperrors.ErrQuoteResponseRejected.
	GetPublicResponse(token string) (*models.QuotePublicView, error)
	// RespondPublic spends the token and moves the quote from sent to
	// accepted (decision accept) or rejected (decline) with closed_at and one
	// history row, in one transaction; a failure after the claim rolls it
	// back and the link stays usable. The history row has no user and source
	// client.
	// A decision outside the two is apperrors.ErrValidation; every other
	// refusal is apperrors.ErrQuoteResponseRejected.
	RespondPublic(token, decision string) (*models.QuotePublicView, error)
	// PDFAvailable reports whether this service can produce a quote PDF: it
	// was given a renderer (WithQuotePDF) and has an APP_BASE_URL to open.
	PDFAvailable() bool
	// RenderPDF prints the quote's render page to PDF. It is
	// apperrors.ErrQuotePDFUnavailable when PDF output is off,
	// apperrors.ErrQuoteNotFound for an unknown quote, and
	// apperrors.ErrQuotePDFRenderFailed for every failed, late or queued-out
	// render (bound: QuotePDFRenderTimeout).
	RenderPDF(id uint) ([]byte, error)
	// GetRenderView answers the render page: the quote behind a render
	// credential, readable only while its render runs. Every refusal is
	// apperrors.ErrQuoteRenderRejected. It never writes.
	GetRenderView(token string) (*models.QuoteRenderView, error)
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

// BulkStatusService backs the three routed bulk status endpoints. Each call is
// all-or-nothing, authorized per record against the same rules the
// single-record update applies, and, once it passes the initial checks
// (status, list size, role), recorded as a bulk_operations row.
type BulkStatusService interface {
	BulkSetLeadStatus(ctx context.Context, actorID uint, actorRole models.UserRole, ids []uint, status models.LeadStatus) (*models.BulkStatusUpdateResult, error)
	BulkSetTicketStatus(ctx context.Context, actorID uint, actorRole models.UserRole, ids []uint, status models.TicketStatus) (*models.BulkStatusUpdateResult, error)
	BulkSetTaskStatus(ctx context.Context, actorID uint, actorRole models.UserRole, ids []uint, status models.TaskStatus) (*models.BulkStatusUpdateResult, error)
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

	// StartRun claims a run (one atomic insert guarded by the unique
	// running_lock, so a run on any replica sharing the database blocks it
	// with apperrors.ErrRunInProgress) and hands it to the engine on a
	// background goroutine, returning as soon as the row exists.
	StartRun(ctx context.Context, trigger string, triggeredByID *uint) (*models.AEORun, error)
	// StartPromptRun runs one prompt (active or not) against every configured
	// engine, under the same claim as a full run.
	StartPromptRun(ctx context.Context, promptID uint, triggeredByID *uint) (*models.AEORun, error)
	// StartScheduledRun starts the daily run of slot, the instant the
	// scheduler computed with aeo.NextRunAt. The slot is stored as
	// slot.UTC() in models.AEOScheduleSlotFormat under a unique index, so
	// each slot starts at most once across replicas; a slot already claimed
	// returns apperrors.ErrAEOScheduleSlotTaken.
	StartScheduledRun(ctx context.Context, slot time.Time) (*models.AEORun, error)
	// ReconcileRunningRuns fails the runs whose owner stopped renewing its
	// heartbeat (or that started more than 6 hours ago) and returns how many
	// it recovered. A running row with a fresh heartbeat is another replica's
	// live run and is left alone. Call it at startup, before arming the
	// scheduler.
	ReconcileRunningRuns() (int64, error)
	ListRuns(offset, limit int, sortBy, sortOrder string) ([]models.AEORun, int64, error)
	GetRun(id uint) (*models.AEORun, error)
	// GetPromptAnswers returns the answer transcript for one prompt, newest
	// first, optionally narrowed to a single run.
	GetPromptAnswers(promptID uint, runID *uint, offset, limit int) ([]models.AEOAnswer, int64, error)

	Dashboard(from, to time.Time) (*models.AEODashboard, error)
	Citations(from, to time.Time) (*models.AEOCitationsReport, error)
	// Export assembles the AEO export of one run (selection.RunID) or of a
	// 7/30/90-day window (selection.Days) ending at the start of tomorrow UTC
	// on the service clock (#244). An unknown run is apperrors.ErrNotFound; a
	// selection of more than AEOExportMaxAnswers answers is
	// ErrAEOExportTooLarge, detected before any citation is read.
	Export(ctx context.Context, selection models.AEOExportSelection) (*models.AEOExport, error)
	// Providers reports the engines this instance can actually query.
	Providers() []models.AEOProviderStatus
}

// AutomationService drives the follow-up automation API: the admin-only rule
// CRUD and dry run, and the run history that follows lead and deal
// visibility. Rule validation failures unwrap to apperrors.ErrValidation
// (400 INVALID_INPUT); a step naming an unknown, inactive or non-staff user
// unwraps to apperrors.ErrAssigneeNotFound (400 INVALID_REFERENCE); a
// missing rule or run is the matching *NotFound sentinel.
type AutomationService interface {
	// CreateRule validates the rule and writes it with its steps numbered
	// 1..n in the given order, version 1. An invalid rule writes nothing.
	CreateRule(rule *models.AutomationRule, actorID uint) error
	GetRule(id uint) (*models.AutomationRule, error)
	ListRules(offset, limit int, filter repository.AutomationRuleListFilter) ([]models.AutomationRule, int64, error)
	// UpdateRule replaces the rule in one transaction: version + 1, steps
	// replaced, pending/waiting/running runs cancelled with rule_changed.
	// enabled nil keeps the stored switch, read under the rule's row lock
	// inside the update transaction; otherwise *enabled is written.
	UpdateRule(id uint, rule *models.AutomationRule, enabled *bool) (*models.AutomationRule, error)
	// SetEnabled flips the switch; disabling cancels the active runs with
	// rule_disabled in the same transaction.
	SetEnabled(id uint, enabled bool) (*models.AutomationRule, error)
	// DeleteRule soft-deletes the rule and cancels its active runs with
	// rule_deleted.
	DeleteRule(id uint) error
	// DryRun simulates the rule against a record of the rule's entity type
	// and writes, sends and notifies nothing. A record of another entity
	// type is apperrors.ErrValidation; a missing record apperrors.ErrNotFound.
	DryRun(ruleID uint, entityType string, entityID uint) (*models.AutomationDryRunReport, error)
	// DryRunDraft simulates an unsaved rule body after the same normalisation
	// and validation as CreateRule and UpdateRule (a refusal is a
	// *RuleValidationError). It saves nothing, starts no transaction, takes no
	// lock and writes, sends and notifies nothing; the report has Draft set
	// and no rule id or version.
	DryRunDraft(rule *models.AutomationRule, entityType string, entityID uint) (*models.AutomationDryRunReport, error)

	// ListRuns and GetRun apply the viewer's role scope inside the query: an
	// admin sees every run, a sales user only runs whose lead or deal it owns
	// (a run outside the scope is apperrors.ErrAutomationRunNotFound), and
	// support and customer callers get apperrors.ErrForbidden before any query.
	ListRuns(viewer LeadViewer, offset, limit int, filter repository.AutomationRunListFilter) ([]models.AutomationRun, int64, error)
	GetRun(viewer LeadViewer, id uint) (*models.AutomationRun, error)
	// CancelRun cancels a pending, waiting or running run with cancel_reason
	// manual; a finished run is apperrors.ErrAutomationRunFinished.
	CancelRun(id uint) (*models.AutomationRun, error)
}

// NotificationService drives a user's own in-app notifications. Every call
// is keyed by the caller's id; a notification of another user is
// apperrors.ErrNotificationNotFound.
type NotificationService interface {
	List(userID uint, unreadOnly bool, offset, limit int) ([]models.Notification, int64, error)
	UnreadCount(userID uint) (int64, error)
	MarkRead(userID, id uint) (*models.Notification, error)
	MarkAllRead(userID uint) (int64, error)
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
	// ListSubmissions and GetSubmission attach each submission's mail
	// delivery rows (#216), [] when there are none.
	ListSubmissions(formID uint, offset, limit int, status string) ([]models.FormSubmission, int64, error)
	GetSubmission(id uint) (*models.FormSubmission, error)

	// ListDeliveries returns one page of form mail deliveries, id descending:
	// failed, log_only and stuck rows by default, or exactly the stored status
	// the filter names. An unknown status or kind is FieldErrors.
	ListDeliveries(filter FormMailDeliveryFilter, offset, limit int) ([]models.FormMailDelivery, int64, error)
	// RetryDelivery re-sends one failed, log_only or stuck delivery: one
	// WithTransactionAndRetry transaction that ends in a conditional claim,
	// then one send after the commit, stamped on a fresh bounded context. A
	// confirmation retry re-arms the submission's single token in place.
	// Refusals are ErrFormDeliveryNotRetryable (nothing written), an unknown
	// delivery or a vanished submission apperrors.ErrNotFound, a send failure
	// an error matching apperrors.ErrMailDelivery (the row is stamped failed
	// and returned with it). A ctx that already carries a transaction gets
	// utils.ErrTransactionRootRequired before any read.
	RetryDelivery(ctx context.Context, id uint) (*models.FormMailDelivery, error)

	// PublicDefinition returns what a visitor's browser needs to render a
	// published form, including a freshly minted time-trap challenge.
	PublicDefinition(publicID, origin string) (*PublicFormDefinition, error)
	// SubmitPublic validates, spam-checks and stores a submission, then creates
	// the lead and sends the mail the form configures — or, for a double-opt-in
	// form, defers all of that to ConfirmSubmission.
	SubmitPublic(ctx context.Context, publicID string, req *PublicSubmissionRequest, meta SubmissionMeta) (*SubmitOutcome, error)
	// ConfirmSubmission spends a confirmation token exactly once.
	ConfirmSubmission(ctx context.Context, rawToken string) error
}

// WebhookService is the admin registry of outbound webhook endpoints and the
// delivery log (#345). Invalid input is a *WebhookValidationError (it unwraps
// to apperrors.ErrValidation), an unknown id apperrors.ErrNotFound, and a
// service built without its repository or secret box ErrWebhooksUnavailable
// (TestEndpoint also without its ping client factory). RedeliverDelivery
// answers ErrWebhookDeliveryNotRedeliverable for a pending or sending row and
// ErrWebhookEndpointUnavailable for a deleted or inactive endpoint.
type WebhookService interface {
	// CreateEndpoint validates the URL and event types, generates the signing
	// secret, stores it sealed and returns it this once.
	CreateEndpoint(ctx context.Context, input WebhookEndpointInput) (*models.WebhookEndpointCreated, error)
	GetEndpoint(ctx context.Context, id uint) (*models.WebhookEndpoint, error)
	ListEndpoints(ctx context.Context, offset, limit int, sortBy, sortOrder string) ([]models.WebhookEndpoint, int64, error)
	// UpdateEndpoint writes the fields the patch carries; it never changes
	// the secret (RotateEndpointSecret does).
	UpdateEndpoint(ctx context.Context, id uint, patch WebhookEndpointPatch) (*models.WebhookEndpoint, error)
	// RotateEndpointSecret replaces the signing secret and returns the new
	// one this once; the replaced secret keeps signing next to it for
	// WebhookSecretOverlap (#503).
	RotateEndpointSecret(ctx context.Context, id uint) (*models.WebhookEndpointSecretRotated, error)
	// DeleteEndpoint hard-deletes the endpoint and its delivery rows in one
	// transaction, endpoint row locked first.
	DeleteEndpoint(ctx context.Context, id uint) error
	// ListDeliveries returns one page of the delivery log, newest first.
	ListDeliveries(ctx context.Context, filter repository.WebhookDeliveryFilter, offset, limit int) ([]models.WebhookDelivery, int64, error)
	// TestEndpoint sends one signed webhook.ping request to the endpoint,
	// active or not, synchronously and without writing anything. A refused,
	// failed or unsigned attempt is a failed result, not an error; a service
	// built without WithWebhookPingClients returns ErrWebhooksUnavailable.
	TestEndpoint(ctx context.Context, id uint) (*models.WebhookPingResult, error)
	// RedeliverDelivery puts a succeeded or failed delivery back into the
	// queue (pending, attempts 0, due now, same event_id) with one
	// conditional UPDATE and returns the requeued row.
	RedeliverDelivery(ctx context.Context, id uint) (*models.WebhookDelivery, error)
}

// SCIMGroupService is the SCIM 2.0 Groups resource (#506). The groups are
// the distinct groups-claim values of the effective single sign-on role
// mapping; members are the managed accounts (live, not customer, linked
// under the effective single sign-on issuer) of scim_group_members.
type SCIMGroupService interface {
	// List returns one page, ascending by value. displayName nil lists every
	// group; otherwise only the one whose value equals it byte for byte.
	List(ctx context.Context, displayName *string, startIndex, count int) (*SCIMGroupPage, error)
	// Get returns the group rawID names; an id that does not decode or names
	// no current group is apperrors.ErrNotFound.
	Get(ctx context.Context, rawID string) (*SCIMGroupRecord, error)
	// Patch changes the group's members and, in the same governed
	// transaction, recomputes each affected user's role from the groups
	// claim. A member an add or a replace names must be a managed account.
	// Refusals are ErrSCIMInvalidValue, ErrSCIMGroupLastAdmin or
	// apperrors.ErrNotFound and write nothing.
	Patch(ctx context.Context, rawID string, p SCIMGroupPatch) (*SCIMGroupRecord, error)
}

// SCIMUserService is the SCIM 2.0 Users resource: the read (#469) and the
// create and replace (#470). The read and the replace, patch and delete
// serve and change managed accounts only: live, not customer, and linked by a
// user_identities row whose issuer equals the effective single sign-on issuer
// exactly (repository.SCIMUserRepository). The read judges a userName filter
// with scim.UserNameMatches (ASCII-only case folding) on every candidate the
// database returns, and sets externalId from the identity under the
// configured single sign-on issuer, compared byte for byte.
type SCIMUserService interface {
	// Get returns the managed account rawID names; anything else, a
	// malformed id or an account not linked under the issuer included, is
	// apperrors.ErrNotFound.
	Get(ctx context.Context, rawID string) (*SCIMUserRecord, error)
	// List returns one page, id ascending. userName nil lists every managed
	// account; otherwise only the managed accounts whose email it matches.
	List(ctx context.Context, userName *string, startIndex, count int) (*SCIMUserPage, error)
	// Create provisions a staff or custom account linked by (issuer,
	// externalId) with the role the roles mapping names (#470). Refusals are
	// ErrSCIMInvalidValue or ErrSCIMUniqueness and write nothing.
	Create(ctx context.Context, in SCIMUserInput) (*SCIMUserRecord, error)
	// Replace overwrites the name, email, role and (when sent) active flag of
	// the managed account rawID names (#470); an unknown or not managed id
	// is apperrors.ErrNotFound.
	Replace(ctx context.Context, rawID string, in SCIMUserInput) (*SCIMUserRecord, error)
	// Patch applies a SCIM PatchOp to the managed account rawID names (#471);
	// active false also ends every session of the account. Refusals are those
	// of Replace and write nothing.
	Patch(ctx context.Context, rawID string, p SCIMUserPatch) (*SCIMUserRecord, error)
	// Delete deactivates the managed account rawID names (#504) as active
	// false does and never erases it. An unknown or not managed id is
	// apperrors.ErrNotFound; the last active admin is ErrSCIMLastAdmin; an
	// already inactive account is nil and writes nothing.
	Delete(ctx context.Context, rawID string) error
}
