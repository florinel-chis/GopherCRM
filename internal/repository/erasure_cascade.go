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

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
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
// submitted values verbatim, the email address, the visitor's IP, user agent
// and the page they submitted from — so leaving it behind would undo the
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
// not to create leads) are outside any lead's erasure and are deliberately not
// touched here; their clean-up is a retention concern, not an Article 17 one.
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

	err := tx.Unscoped().Model(&models.FormSubmission{}).
		Where("id IN ?", submissionIDs).
		Updates(map[string]interface{}{
			"data":       "{}",
			"email":      lead.Email,
			"ip_address": "",
			"user_agent": "",
			"referrer":   "",
		}).Error
	if err != nil {
		return fmt.Errorf("scrubbing form submissions of lead %d: %w", leadID, err)
	}

	if err := tx.Unscoped().Where("submission_id IN ?", submissionIDs).
		Delete(&models.FormConfirmationToken{}).Error; err != nil {
		return fmt.Errorf("purging confirmation tokens of lead %d: %w", leadID, err)
	}
	return nil
}

// ErrConvertedLeadNeedsAdmin refuses the erasure of a converted lead by a
// caller that may erase only unconverted ones. A converted lead is the same
// person as the customer it became (and that customer's login), so erasing it
// erases those too — and erasing a customer is an admin action. It matches
// apperrors.ErrForbidden.
var ErrConvertedLeadNeedsAdmin = fmt.Errorf("%w: erasing a converted lead also erases its customer, which requires an admin", apperrors.ErrForbidden)

// erasureCascade erases one person out of every row that stores them,
// following the identity links in whichever direction it is entered from:
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
// It remembers what it has already visited, and checks that before reading or
// writing anything, so every node is scrubbed exactly once and a cycle
// (user -> customer -> lead -> customer -> user) terminates. A row is marked
// BEFORE it is erased.
//
// A cascade lives for exactly one erasure unit (runErasureUnit). If that unit
// rolls back, the cascade and its marks are discarded with it, so a mark can
// never outlive the database work it describes.
type erasureCascade struct {
	leads     map[uint]struct{}
	customers map[uint]struct{}
	users     map[uint]struct{}

	// allowCustomers is false for an erasure that may only remove an
	// unconverted lead (a non-admin caller): reaching a customer is refused
	// before anything is written.
	allowCustomers bool
}

func newErasureCascade(allowCustomers bool) *erasureCascade {
	return &erasureCascade{
		leads:          make(map[uint]struct{}),
		customers:      make(map[uint]struct{}),
		users:          make(map[uint]struct{}),
		allowCustomers: allowCustomers,
	}
}

// eraseGraph is the one entry point: it runs root against a fresh cascade
// inside one erasure unit. Nothing else creates a cascade, so no step of an
// erasure can start a second traversal with its own visited set.
func eraseGraph(db *gorm.DB, allowCustomers bool, root func(c *erasureCascade, tx *gorm.DB) error) error {
	return runErasureUnit(db, func(tx *gorm.DB) error {
		return root(newErasureCascade(allowCustomers), tx)
	})
}

// lockingRead returns a handle for a read that locks the rows it returns
// (FOR UPDATE on MySQL/MariaDB; SQLite drops the clause and serialises writers
// on its single connection anyway). The fresh Session keeps the clause from
// leaking into the next statement on the same handle.
func lockingRead(tx *gorm.DB) *gorm.DB {
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).Session(&gorm.Session{})
}

// eraseLead erases the lead and, if the lead was converted, the customer it
// became.
func (c *erasureCascade) eraseLead(tx *gorm.DB, id uint) error {
	if _, done := c.leads[id]; done {
		return nil
	}

	// Unscoped: a lead soft-deleted before deletion became an erasure still
	// holds the person's data and still has to be scrubbed. A row that is not
	// there at all is not an error here — the service layer tells a caller
	// that there was nobody to erase.
	//
	// This first read is deliberately NOT a locking read. An automation step
	// holds the lead row under FOR UPDATE while it runs, and the lead plan
	// cancels the lead's runs before touching the row (see
	// erasurePlan.BeforeScrub); waiting on the lead here, before anything is
	// modified, would undo that ordering.
	var lead models.Lead
	err := tx.Unscoped().Select("id", "customer_id").First(&lead, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("loading lead %d for erasure: %w", id, err)
	}
	// A caller that may erase only unconverted leads is refused before the
	// first write of the unit.
	if lead.CustomerID != nil && !c.allowCustomers {
		return ErrConvertedLeadNeedsAdmin
	}

	c.leads[id] = struct{}{}
	if err := eraseRecord(tx, id, leadErasurePlan()); err != nil {
		return fmt.Errorf("erasing lead %d: %w", id, err)
	}

	// The scrub holds the row lock now, so this read is current: a
	// conversion committed after the first read is seen here. A refused
	// erasure returns an error and the unit rolls everything back, the
	// automation cancel included, so a refusal changes nothing.
	var current models.Lead
	if err := lockingRead(tx).Unscoped().Select("id", "customer_id").First(&current, id).Error; err != nil {
		return fmt.Errorf("re-reading erased lead %d: %w", id, err)
	}
	if current.CustomerID == nil {
		return nil
	}
	if !c.allowCustomers {
		return ErrConvertedLeadNeedsAdmin
	}
	return c.eraseCustomer(tx, *current.CustomerID)
}

// eraseCustomer erases the customer, every lead converted into it and, when
// it is a customer-role account, its login.
func (c *erasureCascade) eraseCustomer(tx *gorm.DB, id uint) error {
	if _, done := c.customers[id]; done {
		return nil
	}
	if !c.allowCustomers {
		return ErrConvertedLeadNeedsAdmin
	}

	// Read plainly: the scrub below takes the row lock straight away.
	var customer models.Customer
	err := tx.Unscoped().Select("id", "user_id").First(&customer, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("loading customer %d for erasure: %w", id, err)
	}

	c.customers[id] = struct{}{}
	if err := eraseRecord(tx, id, customerErasurePlan()); err != nil {
		return fmt.Errorf("erasing customer %d: %w", id, err)
	}

	// Unscoped, and a list rather than a single row: nothing stops two leads
	// from having been converted into the same customer, and a lead that was
	// soft-deleted earlier still holds personal data.
	var leadIDs []uint
	if err := tx.Unscoped().Model(&models.Lead{}).
		Where("customer_id = ?", id).Order("id").Pluck("id", &leadIDs).Error; err != nil {
		return fmt.Errorf("finding the leads converted into customer %d: %w", id, err)
	}
	for _, leadID := range leadIDs {
		if err := c.eraseLead(tx, leadID); err != nil {
			return err
		}
	}

	if customer.UserID == nil {
		return nil
	}
	return c.eraseUser(tx, *customer.UserID, true)
}

// eraseUser erases a user. viaLink is true when the user was reached through
// customers.user_id: a staff account reached that way is left alone. A
// customer-role user takes every customer row linked to it along.
func (c *erasureCascade) eraseUser(tx *gorm.DB, id uint, viaLink bool) error {
	if _, done := c.users[id]; done {
		return nil
	}

	// The role decides whether the identity link holds, so it is read with a
	// locking read inside the unit. Erasure never scrubs the role, so a login
	// erased earlier is still recognised (and re-scrubbed) as a customer.
	var user models.User
	err := lockingRead(tx).Unscoped().Select("id", "role").First(&user, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("loading user %d for erasure: %w", id, err)
	}
	isCustomerLogin := user.Role == models.RoleCustomer
	if viaLink && !isCustomerLogin {
		return nil
	}

	c.users[id] = struct{}{}
	if err := eraseRecord(tx, id, userErasurePlan()); err != nil {
		return fmt.Errorf("erasing user %d: %w", id, err)
	}
	if !isCustomerLogin {
		return nil
	}

	var customerIDs []uint
	if err := tx.Unscoped().Model(&models.Customer{}).
		Where("user_id = ?", id).Order("id").Pluck("id", &customerIDs).Error; err != nil {
		return fmt.Errorf("finding the customers of login %d: %w", id, err)
	}
	for _, customerID := range customerIDs {
		if err := c.eraseCustomer(tx, customerID); err != nil {
			return err
		}
	}
	return nil
}
