package repository

// Erasure across the identity links: the lead/customer conversion link and
// the customer/login link.
//
// A lead and the customer it was converted into describe the SAME person. The
// conversion (leadService.ConvertToCustomer) copies the lead's email, names,
// phone and company into a brand new customer row and then only flips the
// lead's status to "converted" and records the new customer's id on the lead —
// it does not move the data, it duplicates it. leads.customer_id is the link
// (see models.Lead.CustomerID and the fk_leads_customer foreign key in
// migrations/20250806120000_initial_schema.up.sql), and it is the only thing
// tying the two rows together.
//
// That duplication is what makes a per-table erasure worthless here: erasing
// the customer leaves a full, live copy of the person's name, email, phone and
// company sitting in the originating lead, where the lead list and the lead
// search will happily go on returning it. Erasing only the lead leaves the same
// data in the customer. An Article 17 request is about the PERSON, not about a
// table, so erasure has to follow the link in both directions.
//
// The same holds for a customer and its own login (customers.user_id, to a
// customer-role account): the login carries the person's name and email, and
// its credentials, so erasing one without the other erases half a person.
//
// Every direction runs in ONE unit. A cascade that erased the customer,
// committed, and then failed on the lead would leave exactly the half-erased
// state that erasure.go goes out of its way to avoid.

import (
	"errors"
	"fmt"
	"slices"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// leadErasurePlan lists what counts as personal data on a lead.
//
// A lead carries the same categories as a customer — the names, the email, the
// phone number, the employer and job title, and the free-text notes, which are
// the highest-risk field because unstructured personal data accumulates there.
//
// ExternalID is scrubbed too. It is the lead's identifier in whatever system
// the lead was imported from; an identifier that still singles the person out
// in another system keeps the row linkable to them, and linkable data is still
// personal data. Source ("website", "trade show"), Status and Classification
// describe the enquiry rather than the person and are deliberately kept, as are
// OwnerID (the staff member responsible) and CustomerID (a foreign key, needed
// for referential integrity — and, in the cascade below, to find the other half
// of the person).
//
// Unlike users and customers, leads.email carries no unique index, so the
// placeholder is not needed to keep the index satisfied. It is used anyway: a
// placeholder in the reserved .invalid domain can never be delivered to, while
// a blanked column would leave an empty address that some future "email the
// leads" job would have to special-case.
func leadErasurePlan() erasurePlan {
	return erasurePlan{
		Model:       &models.Lead{},
		EmailColumn: "email",
		Scrub: map[string]interface{}{
			"first_name":  "",
			"last_name":   "",
			"phone":       "",
			"company":     "",
			"position":    "",
			"notes":       "",
			"external_id": "",
		},
		AfterScrub: scrubLeadFormSubmissions,
		// The automation state of the lead ends before the row is touched; see
		// erasurePlan.BeforeScrub for why the order matters under MySQL.
		BeforeScrub: cancelLeadAutomation,
	}
}

// cancelLeadAutomation cancels the lead's pending, waiting and running
// automation runs with record_deleted and hard-deletes the notifications
// whose entity is the lead, inside the erasure transaction.
func cancelLeadAutomation(tx *gorm.DB, leadID uint) error {
	return cancelAutomationForEntity(tx, models.AutomationEntityLead, leadID)
}

// scrubLeadFormSubmissions extends a lead's erasure to the form submissions
// that produced or fed it. A submission duplicates the person's data — the
// submitted values verbatim, the email address, the visitor's IP, user agent,
// the page they submitted from and its attribution — so leaving it behind would undo the
// erasure the same way a surviving conversion twin would.
//
// It runs as the lead plan's AfterScrub: same transaction, after the lead's
// columns are overwritten (so the placeholder address is already on the row)
// and before the soft delete. The submission keeps the lead's placeholder
// email rather than a blank so the two rows stay visibly linked without either
// identifying anybody. Confirmation tokens are hard-deleted outright: they
// exist only to prove control of an address the erasure just destroyed.
//
// Submissions with no lead_id (spam, never-confirmed opt-ins, forms configured
// not to create leads) are not touched here. Those whose email is the erased
// row's original address are scrubbed by scrubUnlinkedSubmissions (erasure.go,
// #214), which eraseRecord runs for every plan with an email column; the rest
// are a retention concern.
func scrubLeadFormSubmissions(tx *gorm.DB, leadID uint) error {
	var lead models.Lead
	if err := tx.Unscoped().Select("email").First(&lead, leadID).Error; err != nil {
		return fmt.Errorf("loading erased lead %d for submission scrub: %w", leadID, err)
	}

	var submissionIDs []uint
	if err := tx.Unscoped().Model(&models.FormSubmission{}).
		Where("lead_id = ?", leadID).Pluck("id", &submissionIDs).Error; err != nil {
		return fmt.Errorf("listing form submissions of lead %d: %w", leadID, err)
	}
	if len(submissionIDs) == 0 {
		return nil
	}

	// The attribution of each enquiry (#220) is client-supplied text (utm_*
	// values and the page's host) that can carry the person, so it goes with
	// the rest of the submission, live and soft-deleted rows alike.
	err := tx.Unscoped().Model(&models.FormSubmission{}).
		Where("id IN ?", submissionIDs).
		Updates(map[string]interface{}{
			"data":          "{}",
			"email":         lead.Email,
			"ip_address":    "",
			"user_agent":    "",
			"referrer":      "",
			"utm_source":    "",
			"utm_medium":    "",
			"utm_campaign":  "",
			"utm_term":      "",
			"utm_content":   "",
			"referrer_host": "",
		}).Error
	if err != nil {
		return fmt.Errorf("scrubbing form submissions of lead %d: %w", leadID, err)
	}

	if err := tx.Unscoped().Where("submission_id IN ?", submissionIDs).
		Delete(&models.FormConfirmationToken{}).Error; err != nil {
		return fmt.Errorf("purging confirmation tokens of lead %d: %w", leadID, err)
	}

	// The mail delivery rows (#216) of those submissions go right after their
	// tokens, so no retry can mail the erased address again. A fixture that
	// migrates a subset without the table skips this through the same
	// migrator probe the notification purge uses.
	if tx.Migrator().HasTable(&models.FormMailDelivery{}) {
		if err := tx.Unscoped().Where("submission_id IN ?", submissionIDs).
			Delete(&models.FormMailDelivery{}).Error; err != nil {
			return fmt.Errorf("purging form mail deliveries of lead %d: %w", leadID, err)
		}
	}
	// The idempotency keys (#213) are client-supplied text that can carry
	// the person: emptied on the submissions, and their key rows removed.
	if err := scrubSubmissionTicketKeys(tx, submissionIDs); err != nil {
		return fmt.Errorf("scrubbing form ticket keys of lead %d: %w", leadID, err)
	}
	return nil
}

// purgeCustomerExternalIDs hard-deletes the customer's external identity
// mappings (#212) inside the erasure unit. It runs as the customer plan's
// AfterScrub, so every path that reaches a customer — DELETE /customers/{id},
// the erasure of a lead converted into it, the erasure of its customer-role
// login — removes them in the same transaction, and a unit that rolls back
// keeps them. An external id identifies the person in another system, so it
// is personal data like the lead's external_id. Once the rows are gone the
// triple is free to be mapped again.
//
// A schema without the table (a fixture that migrates a subset) is skipped by
// a migrator probe, never by swallowing an error: when the table exists every
// failure propagates and rolls the erasure back. The error names the table and
// the customer id only.
func purgeCustomerExternalIDs(tx *gorm.DB, customerID uint) error {
	if !tx.Migrator().HasTable(&models.CustomerExternalID{}) {
		return nil
	}
	if err := tx.Where("customer_id = ?", customerID).Delete(&models.CustomerExternalID{}).Error; err != nil {
		return fmt.Errorf("purging customer_external_ids of customer %d: %w", customerID, err)
	}
	return nil
}

// customerAfterScrub is the customer plan's AfterScrub: the external identity
// mappings (#212), the ticket comment mail delivery records (#219), then the
// form ticket keys (#213).
func customerAfterScrub(tx *gorm.DB, customerID uint) error {
	if err := purgeCustomerExternalIDs(tx, customerID); err != nil {
		return err
	}
	if err := purgeCustomerTicketMailDeliveries(tx, customerID); err != nil {
		return err
	}
	// The form ticket keys (#213) of the customer and of its tickets.
	return purgeCustomerFormTicketKeys(tx, customerID)
}

// purgeCustomerTicketMailDeliveries hard-deletes the ticket comment mail
// delivery records (#219) of an erased customer inside the erasure unit: every
// row whose customer_id is the customer, and every row whose ticket_id names a
// ticket of the customer, soft-deleted tickets included. A row records that
// mail went to the person, so it goes with them. It runs from the customer
// plan's AfterScrub, so all three roots — DELETE /customers/{id}, the erasure
// of a lead converted into the customer and the erasure of its customer-role
// login — reach it.
//
// A schema without the table is skipped by a migrator probe; when the table
// exists every failure propagates and rolls the erasure back. The error names
// the table and the customer id only.
func purgeCustomerTicketMailDeliveries(tx *gorm.DB, customerID uint) error {
	if !tx.Migrator().HasTable(&models.TicketMailDelivery{}) {
		return nil
	}
	var ticketIDs []uint
	if tx.Migrator().HasTable(&models.Ticket{}) {
		if err := tx.Unscoped().Model(&models.Ticket{}).
			Where("customer_id = ?", customerID).Pluck("id", &ticketIDs).Error; err != nil {
			return fmt.Errorf("purging ticket_mail_deliveries of customer %d: %w", customerID, err)
		}
	}
	query := tx.Where("customer_id = ?", customerID)
	if len(ticketIDs) > 0 {
		query = tx.Where("customer_id = ? OR ticket_id IN ?", customerID, ticketIDs)
	}
	if err := query.Delete(&models.TicketMailDelivery{}).Error; err != nil {
		return fmt.Errorf("purging ticket_mail_deliveries of customer %d: %w", customerID, err)
	}
	return nil
}

// ErrConvertedLeadNeedsAdmin refuses the erasure of a converted lead by a
// caller that may erase only unconverted ones. A converted lead is the same
// person as the customer it became (and that customer's login), so erasing it
// erases those too — and erasing a customer is an admin action. It matches
// apperrors.ErrForbidden.
var ErrConvertedLeadNeedsAdmin = fmt.Errorf("%w: erasing a converted lead also erases its customer, which requires an admin", apperrors.ErrForbidden)

// ErrErasureGraphChanged reports that the rows of the person moved between
// the erasure's discovery and its locks: a lead was converted, a customer was
// linked to the login, a role changed. The unit rolled back and nothing of it
// is kept. An erasure on the database handle discovers the graph again and
// restarts, at most erasureGraphMaxRestarts times; an erasure joined to a
// caller's transaction (WithTx) returns it, and the caller restarts its whole
// root.
var ErrErasureGraphChanged = errors.New("the records of the person changed while the erasure was taking its locks")

// The lock stages of an erasure, in the order they are taken (#385). The
// names are the table names and are what the test seams receive.
const (
	erasureStageLeads          = "leads"
	erasureStageCustomers      = "customers"
	erasureStageUsers          = "users"
	erasureStageRefreshTokens  = "refresh_tokens"
	erasureStagePasswordResets = "password_reset_tokens"
	erasureStageAPIKeys        = "api_keys"
)

// erasureCredentialLockChunk bounds the ids of one credential locking read.
const erasureCredentialLockChunk = 500

// erasureRootKind names the table an erasure is entered from.
type erasureRootKind int

const (
	erasureRootLead erasureRootKind = iota
	erasureRootCustomer
	erasureRootUser
)

// erasureRoot is the row an erasure starts from.
type erasureRoot struct {
	kind erasureRootKind
	id   uint
}

// erasureGraph is one person as the identity links describe them:
//
//	lead --leads.customer_id--> customer --customers.user_id--> login user
//
// The customers.user_id edge is followed only to and from a user whose role is
// customer. That column means "this customer's own login"; a staff account in
// it is a data error, not evidence that the two are the same person, and
// following it would let a customer (or lead) erasure destroy an admin, sales
// or support account past the admin-only user route and its self-delete
// guard. A staff user erased directly is erased alone. assigned_to_id,
// owner_id and every other staff or business link are never followed.
//
// Every slice is in ascending id order, which is the order the rows are
// locked in.
type erasureGraph struct {
	// leads are the leads of the person; the root lead is listed even when no
	// row has its id.
	leads []uint
	// customers are the customers of the person; an id a lead names is listed
	// even when no row has it.
	customers []uint
	// linkedUsers are the users the unit locks: a user root and every user a
	// reached customers.user_id names, staff accounts included (the role that
	// decides the link is read under the lock).
	linkedUsers []uint
	// users are the users that are erased: a user root that exists and every
	// customer-role login among linkedUsers.
	users []uint
}

func (g *erasureGraph) equal(other *erasureGraph) bool {
	return slices.Equal(g.leads, other.leads) && slices.Equal(g.customers, other.customers) &&
		slices.Equal(g.linkedUsers, other.linkedUsers) && slices.Equal(g.users, other.users)
}

// erasureLinks is how a graph walk reads the identity links. The discovery
// reads all of them with plain reads; the re-check under the locks takes the
// forward links from the locked rows and the reverse links from plain reads
// issued after the last graph lock.
type erasureLinks struct {
	// lead returns the lead's customer_id, and whether the row exists.
	lead func(id uint) (customerID *uint, found bool, err error)
	// customer returns the customer's user_id, and whether the row exists.
	customer func(id uint) (userID *uint, found bool, err error)
	// user returns the user's role, and whether the row exists.
	user func(id uint) (role models.UserRole, found bool, err error)
	// leadsOf returns the leads converted into the customer.
	leadsOf func(customerID uint) ([]uint, error)
	// customersOf returns the customers whose login is the user.
	customersOf func(userID uint) ([]uint, error)
}

// erasureWalk follows the identity links from a root. It marks a row before
// it reads it, so every node is visited once and a cycle
// (user -> customer -> lead -> customer -> user) terminates.
type erasureWalk struct {
	links          erasureLinks
	allowCustomers bool
	leads          map[uint]struct{}
	customers      map[uint]struct{}
	linkedUsers    map[uint]struct{}
	users          map[uint]struct{}
}

// walkErasureGraph returns the graph reached from root through links.
// allowCustomers is false for an erasure that may only remove an unconverted
// lead: the walk then stops at the root lead and reads no customer and no
// user.
func walkErasureGraph(root erasureRoot, allowCustomers bool, links erasureLinks) (*erasureGraph, error) {
	w := &erasureWalk{
		links:          links,
		allowCustomers: allowCustomers,
		leads:          make(map[uint]struct{}),
		customers:      make(map[uint]struct{}),
		linkedUsers:    make(map[uint]struct{}),
		users:          make(map[uint]struct{}),
	}
	var err error
	switch root.kind {
	case erasureRootLead:
		err = w.lead(root.id)
	case erasureRootCustomer:
		err = w.customer(root.id)
	case erasureRootUser:
		err = w.user(root.id, false)
	default:
		err = fmt.Errorf("unknown erasure root kind %d", root.kind)
	}
	if err != nil {
		return nil, err
	}
	return &erasureGraph{
		leads:       sortedIDs(w.leads),
		customers:   sortedIDs(w.customers),
		linkedUsers: sortedIDs(w.linkedUsers),
		users:       sortedIDs(w.users),
	}, nil
}

func (w *erasureWalk) lead(id uint) error {
	if _, done := w.leads[id]; done {
		return nil
	}
	w.leads[id] = struct{}{}
	customerID, found, err := w.links.lead(id)
	if err != nil {
		return err
	}
	if !found || customerID == nil || !w.allowCustomers {
		return nil
	}
	return w.customer(*customerID)
}

func (w *erasureWalk) customer(id uint) error {
	if _, done := w.customers[id]; done {
		return nil
	}
	w.customers[id] = struct{}{}
	userID, found, err := w.links.customer(id)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	// A list rather than a single row: nothing stops two leads from having
	// been converted into the same customer.
	leadIDs, err := w.links.leadsOf(id)
	if err != nil {
		return err
	}
	for _, leadID := range leadIDs {
		if err := w.lead(leadID); err != nil {
			return err
		}
	}
	if userID == nil {
		return nil
	}
	return w.user(*userID, true)
}

// user visits a user. viaLink is true when the user was reached through
// customers.user_id: a staff account reached that way is locked and left
// alone. A customer-role user takes every customer row linked to it along.
func (w *erasureWalk) user(id uint, viaLink bool) error {
	if _, done := w.linkedUsers[id]; done {
		return nil
	}
	w.linkedUsers[id] = struct{}{}
	role, found, err := w.links.user(id)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	isCustomerLogin := role == models.RoleCustomer
	if viaLink && !isCustomerLogin {
		return nil
	}
	w.users[id] = struct{}{}
	if !isCustomerLogin {
		return nil
	}
	customerIDs, err := w.links.customersOf(id)
	if err != nil {
		return err
	}
	for _, customerID := range customerIDs {
		if err := w.customer(customerID); err != nil {
			return err
		}
	}
	return nil
}

// convertedLeadsOf is the reverse read of leads.customer_id. Unscoped: a lead
// that was soft-deleted earlier still holds personal data. A plain read on
// purpose: a locking read through the leads.customer_id index would take
// next-key locks there and block unrelated conversions.
func convertedLeadsOf(db *gorm.DB, customerID uint) ([]uint, error) {
	var leadIDs []uint
	if err := db.Unscoped().Model(&models.Lead{}).
		Where("customer_id = ?", customerID).Order("id").Pluck("id", &leadIDs).Error; err != nil {
		return nil, fmt.Errorf("finding the leads converted into customer %d: %w", customerID, err)
	}
	return leadIDs, nil
}

// customersOfLogin is the reverse read of customers.user_id, plain for the
// reason convertedLeadsOf is.
func customersOfLogin(db *gorm.DB, userID uint) ([]uint, error) {
	var customerIDs []uint
	if err := db.Unscoped().Model(&models.Customer{}).
		Where("user_id = ?", userID).Order("id").Pluck("id", &customerIDs).Error; err != nil {
		return nil, fmt.Errorf("finding the customers of login %d: %w", userID, err)
	}
	return customerIDs, nil
}

// erasureDiscovery is what one discovery hands to the transactions that
// follow it: the graph and the schema answers of the automation cancel.
type erasureDiscovery struct {
	graph  *erasureGraph
	tables automationTables
}

// discoverErasureGraph is step 1 of an erasure: it reads the graph of root
// with plain reads only, and resolves the schema probes of the automation
// cancel. db is the root handle whenever the erasure owns its transaction, so
// that none of these reads fixes the snapshot of that transaction; rows are
// read Unscoped, because a row soft-deleted before deletion became an erasure
// still holds the person's data.
func discoverErasureGraph(db *gorm.DB, root erasureRoot, allowCustomers bool) (*erasureDiscovery, error) {
	links := erasureLinks{
		lead: func(id uint) (*uint, bool, error) {
			var rows []models.Lead
			if err := db.Unscoped().Select("id", "customer_id").Where("id = ?", id).Limit(1).Find(&rows).Error; err != nil {
				return nil, false, fmt.Errorf("discovering lead %d for erasure: %w", id, err)
			}
			if len(rows) == 0 {
				return nil, false, nil
			}
			return rows[0].CustomerID, true, nil
		},
		customer: func(id uint) (*uint, bool, error) {
			var rows []models.Customer
			if err := db.Unscoped().Select("id", "user_id").Where("id = ?", id).Limit(1).Find(&rows).Error; err != nil {
				return nil, false, fmt.Errorf("discovering customer %d for erasure: %w", id, err)
			}
			if len(rows) == 0 {
				return nil, false, nil
			}
			return rows[0].UserID, true, nil
		},
		user: func(id uint) (models.UserRole, bool, error) {
			var rows []models.User
			if err := db.Unscoped().Select("id", "role").Where("id = ?", id).Limit(1).Find(&rows).Error; err != nil {
				return "", false, fmt.Errorf("discovering user %d for erasure: %w", id, err)
			}
			if len(rows) == 0 {
				return "", false, nil
			}
			return rows[0].Role, true, nil
		},
		leadsOf:     func(customerID uint) ([]uint, error) { return convertedLeadsOf(db, customerID) },
		customersOf: func(userID uint) ([]uint, error) { return customersOfLogin(db, userID) },
	}
	graph, err := walkErasureGraph(root, allowCustomers, links)
	if err != nil {
		return nil, err
	}
	return &erasureDiscovery{graph: graph, tables: probeAutomationTables(db)}, nil
}

// erasureCascade is the state of ONE erasure unit: the rows it scrubbed, which
// the business-text scrub (business_text_erasure.go, #245) starts from. A
// cascade lives for exactly one unit (runErasureUnit). If that unit rolls
// back, the cascade and its marks are discarded with it, so a mark can never
// outlive the database work it describes.
type erasureCascade struct {
	leads     map[uint]struct{}
	customers map[uint]struct{}
	users     map[uint]struct{}

	// allowCustomers is false for an erasure that may only remove an
	// unconverted lead (a non-admin caller): a converted lead is refused
	// before anything is written to it.
	allowCustomers bool

	// seams and hooks are test seams, copied from the repository that started
	// the unit; nil everywhere in production.
	seams *erasureSeams
	hooks *leadErasureSeams
}

// erasureSeams are the instance-local test seams of the erasure protocol
// (#385). They are set only through the test options in erasure_hooks.go,
// which a production file never calls; a nil set or a nil func does nothing.
type erasureSeams struct {
	// afterDiscovery runs once per discovery, after the graph and the schema
	// probes were read and before the transaction of the attempt begins.
	afterDiscovery func()
	// beforeLock runs before the locking reads of a stage, with its table
	// name. An error it returns ends the attempt as that error. The call for
	// the leads stage is the first of a transaction, after the automation
	// cancel, and happens once per transaction.
	beforeLock func(table string) error
	// afterLock runs after the locking reads of a stage returned.
	afterLock func(table string)
}

func (s *erasureSeams) runAfterDiscovery() {
	if s != nil && s.afterDiscovery != nil {
		s.afterDiscovery()
	}
}

func (s *erasureSeams) runBeforeLock(table string) error {
	if s != nil && s.beforeLock != nil {
		return s.beforeLock(table)
	}
	return nil
}

func (s *erasureSeams) runAfterLock(table string) {
	if s != nil && s.afterLock != nil {
		s.afterLock(table)
	}
}

// leadErasureSeams are the instance-local test seams of the lead path (#337).
// They are set only through the test option in lead_erasure_hooks.go, which a
// production file never calls; a nil set or a nil func does nothing.
type leadErasureSeams struct {
	// afterAutomationCancel runs after the automation runs of the graph's
	// leads are cancelled and before the first locking read of a lead, once
	// for every lead, with the unit's tx.
	afterAutomationCancel func(tx *gorm.DB, leadID uint)
	// afterLeadLock runs right after the locking read of a lead found it,
	// while the unit holds the lead's row lock and before any decision.
	afterLeadLock func(leadID uint)
}

func (s *leadErasureSeams) runAfterAutomationCancel(tx *gorm.DB, leadID uint) {
	if s != nil && s.afterAutomationCancel != nil {
		s.afterAutomationCancel(tx, leadID)
	}
}

func (s *leadErasureSeams) runAfterLeadLock(leadID uint) {
	if s != nil && s.afterLeadLock != nil {
		s.afterLeadLock(leadID)
	}
}

func newErasureCascade(allowCustomers bool) *erasureCascade {
	return &erasureCascade{
		leads:          make(map[uint]struct{}),
		customers:      make(map[uint]struct{}),
		users:          make(map[uint]struct{}),
		allowCustomers: allowCustomers,
	}
}

// eraseGraph is the one entry point of every erasure: userRepository.Delete,
// customerRepository.Delete, leadRepository.Delete and DeleteUnconverted. All
// of them erase the same graph under the same protocol (#385), whichever row
// they are entered from:
//
//  1. Discover the graph from the root with plain reads (discoverErasureGraph).
//  2. Cancel the automation runs of every discovered lead, ids ascending,
//     before the first lock on leads (#337).
//  3. Lock, by primary key only and ids ascending inside each table:
//     leads → customers → users → refresh_tokens → password_reset_tokens →
//     api_keys.
//  4. Re-read the graph under the locks; when it differs from the discovery,
//     roll back with ErrErasureGraphChanged.
//  5. Decide the non-admin refusal on the locked lead, then scrub, purge and
//     soft-delete every row, and the staff-typed text of the linked deals and
//     quotes (business_text_erasure.go, #245).
//
// Two erasures of one person therefore queue on its first row, whichever
// roots they came from, where they used to deadlock.
//
// Snapshot rule. Under REPEATABLE READ the first plain read of a transaction
// fixes its snapshot, and the re-read of step 4 must see what committed
// before the locks were granted. When db is not a transaction, the discovery
// and the schema probes of the cancel run on db before the transaction
// begins, and the transaction issues no plain read of any kind before the
// locking read of users in step 3: only the writes of the cancel and FOR
// UPDATE reads by primary key. A migrator probe counts: on MySQL 8 a select
// on information_schema fixes the snapshot (on MariaDB 10.11 it does not).
//
// Retry. When db is not a transaction, a transaction that lost a deadlock or
// a lock wait is replayed on the same discovery (runErasureUnit, the re-check
// catches a stale one), and ErrErasureGraphChanged starts over from a new
// discovery, at most erasureGraphMaxRestarts times and only after a clean
// rollback. When db is a caller's transaction (WithTx) the discovery answers
// from the caller's snapshot, the unit is one checked SAVEPOINT that runs
// once, and both kinds of error go to the caller.
func eraseGraph(db *gorm.DB, allowCustomers bool, root erasureRoot, seams *erasureSeams, hooks *leadErasureSeams) error {
	attempt := func() error {
		discovery, err := discoverErasureGraph(db, root, allowCustomers)
		if err != nil {
			return err
		}
		seams.runAfterDiscovery()
		return runErasureUnit(db, func(tx *gorm.DB) error {
			cascade := newErasureCascade(allowCustomers)
			cascade.seams, cascade.hooks = seams, hooks
			return cascade.erase(tx, root, discovery)
		})
	}
	if inTransaction(db) {
		return attempt()
	}

	for restarts := 0; ; restarts++ {
		err := attempt()
		if !shouldRestartErasure(err) {
			return err
		}
		if restarts >= erasureGraphMaxRestarts {
			return fmt.Errorf("erasure abandoned after %d discoveries: %w", restarts+1, err)
		}
	}
}

// shouldRestartErasure reports whether a failed erasure on the database handle
// may start over from a new discovery: only ErrErasureGraphChanged, and only
// when its transaction rolled back cleanly (an error that also carries
// ErrRollbackFailed, ErrTransactionPoisoned, ErrCommitFailed or ErrBeginFailed
// leaves the outcome unknown).
func shouldRestartErasure(err error) bool {
	if err == nil || !errors.Is(err, ErrErasureGraphChanged) {
		return false
	}
	for _, unclean := range []error{utils.ErrRollbackFailed, utils.ErrTransactionPoisoned, utils.ErrCommitFailed, utils.ErrBeginFailed} {
		if errors.Is(err, unclean) {
			return false
		}
	}
	return true
}

// lockingRead returns a handle for a read that locks the rows it returns
// (FOR UPDATE on MySQL/MariaDB; SQLite drops the clause and serialises writers
// on its single connection anyway). The fresh Session keeps the clause from
// leaking into the next statement on the same handle.
func lockingRead(tx *gorm.DB) *gorm.DB {
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).Session(&gorm.Session{})
}

// lockedErasureRows are the rows the lock stages returned, by id. An id that
// was locked for but names no row is absent from its map.
type lockedErasureRows struct {
	leads     map[uint]models.Lead
	customers map[uint]models.Customer
	users     map[uint]models.User
}

// erase is one attempt at the graph of discovery, inside the erasure unit:
// steps 2 to 5 of eraseGraph.
func (c *erasureCascade) erase(tx *gorm.DB, root erasureRoot, discovery *erasureDiscovery) error {
	graph := discovery.graph

	// Step 2. The cancel stays ahead of the lead locks on purpose (#337): an
	// automation step holds the lead row FOR UPDATE while it runs, and an
	// erasure that has modified the run rows by the time it waits on the lead
	// is the survivor of the resulting deadlock (see erasurePlan.BeforeScrub).
	// It writes only: the schema probes were answered by the discovery. A
	// refusal or a changed graph returns an error and the unit rolls
	// everything back, the cancel included.
	for _, leadID := range graph.leads {
		if err := cancelAutomationInTables(tx, models.AutomationEntityLead, leadID, discovery.tables); err != nil {
			return fmt.Errorf("erasing lead %d: %w", leadID, err)
		}
	}
	for _, leadID := range graph.leads {
		c.hooks.runAfterAutomationCancel(tx, leadID)
	}

	// Step 3, the graph locks.
	locked, err := c.lockGraph(tx, graph)
	if err != nil {
		return err
	}

	// Step 4. The forward links come from the locked rows; the reverse reads
	// are the first plain reads of the transaction, so under REPEATABLE READ
	// they see everything that committed before the users lock was granted.
	current, err := walkErasureGraph(root, c.allowCustomers, locked.links(tx, graph))
	if err != nil {
		return err
	}
	if !current.equal(graph) {
		return ErrErasureGraphChanged
	}

	// The rest of step 3: the credential rows of the users that are erased.
	if err := c.lockCredentials(tx, graph.users); err != nil {
		return err
	}

	// Step 5. Every row is locked; the scrub order no longer matters for
	// other transactions.
	for _, leadID := range graph.leads {
		if _, found := locked.leads[leadID]; !found {
			// A lead that is not there at all is not an error here: the
			// service layer tells a caller that there was nobody to erase.
			continue
		}
		c.leads[leadID] = struct{}{}
		plan := leadErasurePlan()
		plan.BeforeScrub = nil // the cancel ran in step 2
		if err := eraseRecord(tx, leadID, plan); err != nil {
			return fmt.Errorf("erasing lead %d: %w", leadID, err)
		}
	}
	for _, customerID := range graph.customers {
		// A customer id that names no row scrubs nothing.
		c.customers[customerID] = struct{}{}
		if err := eraseRecord(tx, customerID, customerErasurePlan()); err != nil {
			return fmt.Errorf("erasing customer %d: %w", customerID, err)
		}
	}
	for _, userID := range graph.users {
		c.users[userID] = struct{}{}
		if err := eraseRecord(tx, userID, userErasurePlan()); err != nil {
			return fmt.Errorf("erasing user %d: %w", userID, err)
		}
	}
	return c.scrubBusinessText(tx)
}

// lockStage brackets the locking reads of one stage with the test seams.
func (c *erasureCascade) lockStage(table string, lock func() error) error {
	if err := c.seams.runBeforeLock(table); err != nil {
		return err
	}
	if err := lock(); err != nil {
		return err
	}
	c.seams.runAfterLock(table)
	return nil
}

// lockGraph takes the row locks of the graph: leads, then customers, then
// users, each by primary key and in ascending id. No read here filters on
// leads.customer_id, customers.user_id or deleted_at: a locking read through
// one of those indexes takes next-key and gap locks that block unrelated
// conversions (#337). Rows are read Unscoped, a soft-deleted one included.
//
// The non-admin refusal is decided here, on the locked lead and before
// anything is written to it: a caller that may erase only unconverted leads
// stops at a lead that names a customer.
func (c *erasureCascade) lockGraph(tx *gorm.DB, graph *erasureGraph) (*lockedErasureRows, error) {
	locked := &lockedErasureRows{
		leads:     make(map[uint]models.Lead, len(graph.leads)),
		customers: make(map[uint]models.Customer, len(graph.customers)),
		users:     make(map[uint]models.User, len(graph.linkedUsers)),
	}

	err := c.lockStage(erasureStageLeads, func() error {
		for _, id := range graph.leads {
			var lead models.Lead
			err := lockingRead(tx).Unscoped().Select("id", "status", "customer_id").First(&lead, id).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return fmt.Errorf("loading lead %d for erasure: %w", id, err)
			}
			locked.leads[id] = lead
			c.hooks.runAfterLeadLock(id)
			if lead.CustomerID != nil && !c.allowCustomers {
				return ErrConvertedLeadNeedsAdmin
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	err = c.lockStage(erasureStageCustomers, func() error {
		for _, id := range graph.customers {
			var customer models.Customer
			err := lockingRead(tx).Unscoped().Select("id", "user_id").First(&customer, id).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return fmt.Errorf("loading customer %d for erasure: %w", id, err)
			}
			locked.customers[id] = customer
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// The role decides whether the identity link holds, so it is read under
	// the lock. Erasure never scrubs the role, so a login erased earlier is
	// still recognised (and re-scrubbed) as a customer.
	err = c.lockStage(erasureStageUsers, func() error {
		for _, id := range graph.linkedUsers {
			var user models.User
			err := lockingRead(tx).Unscoped().Select("id", "role").First(&user, id).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return fmt.Errorf("loading user %d for erasure: %w", id, err)
			}
			locked.users[id] = user
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return locked, nil
}

// links reads the graph again under the locks. A forward link is taken from
// the locked row; a row the discovery did not list was never locked, so
// asking for it means the graph moved. The reverse links are plain reads on
// the transaction.
func (l *lockedErasureRows) links(tx *gorm.DB, graph *erasureGraph) erasureLinks {
	listed := func(ids []uint, id uint) bool {
		_, found := slices.BinarySearch(ids, id)
		return found
	}
	return erasureLinks{
		lead: func(id uint) (*uint, bool, error) {
			if !listed(graph.leads, id) {
				return nil, false, ErrErasureGraphChanged
			}
			lead, found := l.leads[id]
			return lead.CustomerID, found, nil
		},
		customer: func(id uint) (*uint, bool, error) {
			if !listed(graph.customers, id) {
				return nil, false, ErrErasureGraphChanged
			}
			customer, found := l.customers[id]
			return customer.UserID, found, nil
		},
		user: func(id uint) (models.UserRole, bool, error) {
			if !listed(graph.linkedUsers, id) {
				return "", false, ErrErasureGraphChanged
			}
			user, found := l.users[id]
			return user.Role, found, nil
		},
		leadsOf:     func(customerID uint) ([]uint, error) { return convertedLeadsOf(tx, customerID) },
		customersOf: func(userID uint) ([]uint, error) { return customersOfLogin(tx, userID) },
	}
}

// lockCredentials locks the credential rows of the erased users, after their
// users rows: refresh_tokens, then password_reset_tokens, then api_keys. The
// ids are read with a plain select by user_id and then locked by primary key
// in ascending id; a locking read by user_id would lock the index range and
// its gaps. purgeCredentials still deletes by user_id and stays the authority
// for what is removed: a flow that writes a credential of the user locks the
// users row first, which this unit holds.
func (c *erasureCascade) lockCredentials(tx *gorm.DB, userIDs []uint) error {
	stages := []struct {
		table string
		model func() interface{}
	}{
		{erasureStageRefreshTokens, func() interface{} { return &models.RefreshToken{} }},
		{erasureStagePasswordResets, func() interface{} { return &models.PasswordResetToken{} }},
		{erasureStageAPIKeys, func() interface{} { return &models.APIKey{} }},
	}
	for _, stage := range stages {
		err := c.lockStage(stage.table, func() error {
			if len(userIDs) == 0 {
				return nil
			}
			var ids []uint
			if err := tx.Unscoped().Model(stage.model()).
				Where("user_id IN ?", userIDs).Order("id").Pluck("id", &ids).Error; err != nil {
				return fmt.Errorf("listing %s of the erased users: %w", stage.table, err)
			}
			for start := 0; start < len(ids); start += erasureCredentialLockChunk {
				chunk := ids[start:min(start+erasureCredentialLockChunk, len(ids))]
				var lockedIDs []uint
				if err := lockingRead(tx).Unscoped().Model(stage.model()).
					Where("id IN ?", chunk).Order("id").Pluck("id", &lockedIDs).Error; err != nil {
					return fmt.Errorf("locking %s of the erased users: %w", stage.table, err)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}
