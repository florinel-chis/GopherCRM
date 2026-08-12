package repository

// Erasure across the lead/customer conversion link.
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
// Both directions run in ONE transaction. A cascade that erased the customer,
// committed, and then failed on the lead would leave exactly the half-erased
// state that erasure.go goes out of its way to avoid.

import (
	"errors"
	"fmt"
	"maps"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
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
	}
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

// erasureCascade erases a person out of both the lead and the customer half of
// a conversion, following leads.customer_id in whichever direction it is
// entered from.
//
// It remembers what it has already erased. Without that, erasing a lead would
// erase its customer, which would look for the leads converted into that
// customer, which would erase the original lead again, forever. The record of
// what is done is kept per cascade (not per call) so that a bulk delete can
// share one cascade across a whole batch and recognise a row that an earlier
// item of the same batch already erased.
//
// A row is marked BEFORE it is erased, so a cycle terminates. That makes a mark
// a claim about the database — "this row no longer describes the person" — and
// the claim has to be true, because the bulk paths SKIP a marked row and count
// it as a success. A row that is marked but was never actually erased is
// reported to the operator as erased while every one of the person's fields is
// still sitting there in a live row: the invariant to hold is that a row is
// marked if and only if its erasure took effect.
//
// The marks are therefore rolled back where the DATABASE work is rolled back,
// and nowhere else — see asBatchItem. Undoing only the mark of the row that
// happened to fail is not enough: one cascade attempt walks a whole conversion
// graph and can mark several rows before it fails, and rolling the attempt back
// undoes the erasure of ALL of them.
type erasureCascade struct {
	leads     map[uint]struct{}
	customers map[uint]struct{}
}

func newErasureCascade() *erasureCascade {
	return &erasureCascade{
		leads:     make(map[uint]struct{}),
		customers: make(map[uint]struct{}),
	}
}

// leadErased and customerErased report whether this cascade has already erased
// the row, i.e. whether it is gone because of earlier work in the same unit of
// work rather than because it never existed.
func (c *erasureCascade) leadErased(id uint) bool {
	_, done := c.leads[id]
	return done
}

func (c *erasureCascade) customerErased(id uint) bool {
	_, done := c.customers[id]
	return done
}

// eraseLead erases the lead and, if the lead was converted, the customer it
// became. tx must be a transaction handle: the caller owns the atomicity of the
// whole cascade, not of the individual rows.
func (c *erasureCascade) eraseLead(tx *gorm.DB, id uint) error {
	if c.leadErased(id) {
		return nil
	}
	c.leads[id] = struct{}{}
	return c.eraseLeadRow(tx, id)
}

func (c *erasureCascade) eraseLeadRow(tx *gorm.DB, id uint) error {
	// Unscoped: a lead soft-deleted before deletion became an erasure still
	// holds the person's data and still has to be scrubbed. A row that is not
	// there at all is not an error here — the caller (the service layer) is
	// responsible for telling a caller that there was nobody to erase, exactly
	// as it is for users and customers.
	var lead models.Lead
	err := tx.Unscoped().Select("id", "customer_id").First(&lead, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("loading lead %d for erasure: %w", id, err)
	}

	if err := eraseRecord(tx, id, leadErasurePlan()); err != nil {
		return fmt.Errorf("erasing lead %d: %w", id, err)
	}

	if lead.CustomerID == nil {
		return nil
	}
	return c.eraseCustomer(tx, *lead.CustomerID)
}

// eraseCustomer erases the customer and every lead that was converted into it.
func (c *erasureCascade) eraseCustomer(tx *gorm.DB, id uint) error {
	if c.customerErased(id) {
		return nil
	}
	c.customers[id] = struct{}{}
	return c.eraseCustomerRow(tx, id)
}

func (c *erasureCascade) eraseCustomerRow(tx *gorm.DB, id uint) error {
	// The customer half is erased by the customer repository itself rather than
	// by a second copy of its erasure plan here: the authoritative list of a
	// customer's personal-data columns must exist exactly once.
	if err := NewCustomerRepository(tx).Delete(id); err != nil {
		return fmt.Errorf("erasing customer %d: %w", id, err)
	}

	// Unscoped, and a list rather than a single row: nothing stops two leads
	// from having been converted into the same customer, and a lead that was
	// soft-deleted earlier still holds personal data.
	var leadIDs []uint
	if err := tx.Unscoped().Model(&models.Lead{}).
		Where("customer_id = ?", id).Pluck("id", &leadIDs).Error; err != nil {
		return fmt.Errorf("finding the leads converted into customer %d: %w", id, err)
	}

	for _, leadID := range leadIDs {
		if err := c.eraseLead(tx, leadID); err != nil {
			return err
		}
	}
	return nil
}

// --- One item of a batch ------------------------------------------------------

// eraseLeadAsBatchItem and eraseCustomerAsBatchItem erase one ID of a bulk
// request. A batch reports per-item failures and keeps going, so an item has to
// be a unit of work in its own right: it either happens completely or it never
// happened at all, and either way the rest of the batch is unaffected.
//
// The cascade must be shared across the whole batch (that is how a row erased as
// a side effect of an earlier ID is recognised instead of being reported as
// missing), so these are methods on the cascade rather than free functions.
func (c *erasureCascade) eraseLeadAsBatchItem(db *gorm.DB, id uint) error {
	return c.asBatchItem(db, func(tx *gorm.DB) error { return c.eraseLead(tx, id) })
}

func (c *erasureCascade) eraseCustomerAsBatchItem(db *gorm.DB, id uint) error {
	return c.asBatchItem(db, func(tx *gorm.DB) error { return c.eraseCustomer(tx, id) })
}

// asBatchItem runs one attempt with its database effects and its cascade
// bookkeeping tied together, which is the only way the two can agree.
//
// The marks are what the batch reports on: a marked row is skipped and counted
// as erased. If an attempt is rolled back, every mark it added describes work
// that no longer exists, so all of them go — not merely the one belonging to the
// row whose statement happened to fail. Keeping the others is how a fully intact
// person gets reported as erased: a later item of the same batch reaches the
// remembered row, believes it is done, and never touches it.
//
// The snapshot is taken before the attempt and restored wholesale afterwards,
// rather than tracking what each level added, because the cascade recurses
// through an arbitrary conversion graph and any per-level bookkeeping would be
// one more thing that can disagree with the transaction.
func (c *erasureCascade) asBatchItem(db *gorm.DB, fn func(tx *gorm.DB) error) error {
	leads, customers := maps.Clone(c.leads), maps.Clone(c.customers)

	if err := runIsolated(db, fn); err != nil {
		c.leads, c.customers = leads, customers
		return err
	}
	return nil
}

// eraseLeadWithConversionLink erases a lead and, when it was converted, the
// customer it became — in a single transaction, joining the caller's if the
// handle it is given is already one.
func eraseLeadWithConversionLink(db *gorm.DB, leadID uint) error {
	return runInTransaction(db, func(tx *gorm.DB) error {
		return newErasureCascade().eraseLead(tx, leadID)
	})
}

// eraseCustomerWithConversionLink erases a customer and every lead that was
// converted into it, in a single transaction.
func eraseCustomerWithConversionLink(db *gorm.DB, customerID uint) error {
	return runInTransaction(db, func(tx *gorm.DB) error {
		return newErasureCascade().eraseCustomer(tx, customerID)
	})
}

// NewCustomerRepositoryWithLeadErasure returns a customer repository whose
// Delete also erases the lead the customer was converted from.
//
// It is a wrapper rather than a change to customerRepository.Delete because the
// cascade is a rule ABOUT the relationship between the two entities: the
// customer repository knows nothing about leads, and the erasure of a lead
// belongs to the lead repository. Every other method is the plain customer
// repository's, including the WithTx handle, which stays wrapped so that a
// caller composing the erasure into its own transaction still gets the cascade.
//
// This is the constructor the application must be wired with (see
// cmd/main.go): erasing a customer through the unwrapped repository erases only
// half the person.
func NewCustomerRepositoryWithLeadErasure(db *gorm.DB) CustomerRepository {
	return &cascadingCustomerRepository{
		CustomerRepository: NewCustomerRepository(db),
		db:                 db,
	}
}

type cascadingCustomerRepository struct {
	CustomerRepository
	db *gorm.DB
}

func (r *cascadingCustomerRepository) Delete(id uint) error {
	return eraseCustomerWithConversionLink(r.db, id)
}

func (r *cascadingCustomerRepository) WithTx(tx *gorm.DB) CustomerRepository {
	return NewCustomerRepositoryWithLeadErasure(tx)
}
