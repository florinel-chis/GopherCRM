package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type leadService struct {
	leadRepo     repository.LeadRepository
	customerRepo repository.CustomerRepository
	companyRepo  repository.CompanyRepository
	txManager    *utils.TransactionManager
	// automation records the outbox events of every committed lead write
	// (issue #164). Nil means no automation: the writes run as before.
	automation repository.AutomationRepository
	// config supplies the administrator's lead-conversion settings, which
	// ConvertToCustomer enforces (issue #104). Nil means no settings are
	// enforced: no status restriction, notes optional, no owner assignment.
	config ConfigurationService
}

// WithLeadConversionRules makes ConvertToCustomer enforce the lead-conversion
// settings (leads.conversion.allowed_statuses, require_notes and
// auto_assign_owner) read from config on every conversion. The application
// always wires it; services built without it convert unrestricted.
func WithLeadConversionRules(config ConfigurationService) LeadServiceOption {
	return func(s *leadService) { s.config = config }
}

// LeadServiceOption configures NewLeadService beyond its required
// dependencies.
type LeadServiceOption func(*leadService)

// WithLeadAutomationEvents makes Create, Update and ConvertToCustomer record
// their automation events inside the transaction that commits the write.
func WithLeadAutomationEvents(repo repository.AutomationRepository) LeadServiceOption {
	return func(s *leadService) { s.automation = repo }
}

func NewLeadService(leadRepo repository.LeadRepository, customerRepo repository.CustomerRepository, companyRepo repository.CompanyRepository, txManager *utils.TransactionManager, opts ...LeadServiceOption) LeadService {
	s := &leadService{
		leadRepo:     leadRepo,
		customerRepo: customerRepo,
		companyRepo:  companyRepo,
		txManager:    txManager,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// writeWithEvents runs write against the lead repository inside one
// transaction together with the automation events it produces, so an event
// is never recorded for a write that rolled back. Without automation (or
// without a transaction manager) the write runs directly, as it always did.
func (s *leadService) writeWithEvents(write func(leads repository.LeadRepository, events repository.AutomationRepository) error) error {
	if s.automation == nil || s.txManager == nil {
		return write(s.leadRepo, nil)
	}
	return s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		return write(s.leadRepo.WithTx(tx), s.automation.WithTx(tx))
	})
}

// checkCompanyLink rejects a company_id that matches no live company with
// apperrors.ErrCompanyNotFound, which the handler answers with 400
// INVALID_REFERENCE. A nil id is no link and always fine.
func (s *leadService) checkCompanyLink(companyID *uint) error {
	return checkLeadCompanyLink(s.companyRepo, companyID)
}

func checkLeadCompanyLink(companies repository.CompanyRepository, companyID *uint) error {
	if companyID == nil {
		return nil
	}
	if _, err := companies.GetByID(*companyID); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("unknown company_id %d: %w", *companyID, apperrors.ErrCompanyNotFound)
		}
		return err
	}
	return nil
}

func (s *leadService) Create(lead *models.Lead) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "lead"), "LeadService", "Create")

	// Set default status if not provided
	if lead.Status == "" {
		lead.Status = models.LeadStatusNew
	}

	if err := s.checkCompanyLink(lead.CompanyID); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Lead rejected")
		return err
	}

	err := s.writeWithEvents(func(leads repository.LeadRepository, events repository.AutomationRepository) error {
		if err := leads.Create(lead); err != nil {
			return err
		}
		return automationLeadCreated(events, lead.ID, models.AutomationOriginUser, nil)
	})
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to create lead")
		return err
	}

	logger.WithField("lead_id", lead.ID).Info("Lead created successfully")
	return nil
}

func (s *leadService) GetByID(id uint) (*models.Lead, error) {
	// For individual lead retrieval, preload Owner and the linked company for
	// display purposes
	return s.leadRepo.GetByIDWithPreloads(id, "Owner", "CompanyRecord")
}

func (s *leadService) GetByExternalID(externalID string) (*models.Lead, error) {
	return s.leadRepo.GetByExternalID(externalID)
}

func (s *leadService) GetByOwner(ownerID uint, offset, limit int) ([]models.Lead, int64, error) {
	// For owner-filtered lists, no need to preload Owner since we already know it
	leads, err := s.leadRepo.GetByOwnerID(ownerID, offset, limit)
	if err != nil {
		return nil, 0, err
	}

	total, err := s.leadRepo.CountByOwnerID(ownerID)
	if err != nil {
		return nil, 0, err
	}

	return leads, total, nil
}

func (s *leadService) GetByClassification(classification models.LeadClassification, offset, limit int) ([]models.Lead, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "LeadService", "GetByClassification")
	switch classification {
	case models.LeadClassificationUnclassified, models.LeadClassificationTest, models.LeadClassificationSpam, models.LeadClassificationLead, models.LeadClassificationHotLead:
		logger = logger.WithField("classification", classification)
	}

	leads, err := s.leadRepo.GetByClassification(classification, offset, limit)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list leads by classification")
		return nil, 0, err
	}

	total, err := s.leadRepo.CountByClassification(classification)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count leads by classification")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Leads listed by classification successfully")
	return leads, total, nil
}

// lockedLeadRead returns a lead repository bound to tx whose reads take a row
// lock (SELECT ... FOR UPDATE; SQLite drops the clause and is serialised by
// its single connection). The fresh Session keeps the clause off every later
// statement on tx: writes go through s.leadRepo.WithTx(tx).
func (s *leadService) lockedLeadRead(tx *gorm.DB) repository.LeadRepository {
	return s.leadRepo.WithTx(tx.Clauses(clause.Locking{Strength: "UPDATE"}).Session(&gorm.Session{}))
}

// Update applies a partial update. The lead is re-read with a row lock inside
// the transaction that writes it, so the whole-row save never carries back a
// stale copy of what a concurrent write (a conversion setting status and
// customer_id) committed in between.
//
// `converted` is not a status an update can set: a lead becomes converted
// only through ConvertToCustomer, which creates the customer and enforces the
// conversion settings. Sending the status a converted lead already has (the
// edit form does) is fine.
func (s *leadService) Update(id uint, updates map[string]interface{}) (*models.Lead, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("lead_id", id), "LeadService", "Update")

	if s.txManager == nil {
		return s.applyUpdate(logger, id, updates, s.leadRepo, s.leadRepo, s.companyRepo, nil)
	}
	var updated *models.Lead
	err := s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		var err error
		updated, err = s.applyUpdate(logger, id, updates, s.lockedLeadRead(tx), s.leadRepo.WithTx(tx), s.companyRepo.WithTx(tx), automationTx(s.automation, tx))
		return err
	})
	if err != nil {
		return nil, err
	}
	logger.Info("Lead updated successfully")
	return updated, nil
}

func (s *leadService) applyUpdate(logger *logrus.Entry, id uint, updates map[string]interface{}, reader, writer repository.LeadRepository, companies repository.CompanyRepository, events repository.AutomationRepository) (*models.Lead, error) {
	lead, err := reader.GetByID(id)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Lead not found")
		return nil, err
	}
	previousStatus := lead.Status

	// Apply updates
	if firstName, ok := updates["first_name"].(string); ok {
		lead.FirstName = firstName
	}
	if lastName, ok := updates["last_name"].(string); ok {
		lead.LastName = lastName
	}
	if email, ok := updates["email"].(string); ok {
		lead.Email = email
	}
	if phone, ok := updates["phone"].(string); ok {
		lead.Phone = phone
	}
	if company, ok := updates["company"].(string); ok {
		lead.Company = company
	}
	if position, ok := updates["position"].(string); ok {
		lead.Position = position
	}
	if source, ok := updates["source"].(string); ok {
		lead.Source = source
	}
	if status, ok := updates["status"].(models.LeadStatus); ok {
		if status == models.LeadStatusConverted && previousStatus != models.LeadStatusConverted {
			logger.Warn("Lead update refused: converted is set only by conversion")
			return nil, ConvertedStatusRefusal()
		}
		lead.Status = status
	}
	if classification, ok := updates["classification"].(models.LeadClassification); ok {
		lead.Classification = classification
	}
	if externalID, ok := updates["external_id"].(string); ok {
		lead.ExternalID = externalID
	}
	if notes, ok := updates["notes"].(string); ok {
		lead.Notes = notes
	}
	if ownerID, ok := updates["owner_id"].(uint); ok {
		lead.OwnerID = ownerID
	}
	// company_id is a *uint: a nil pointer clears the link, a non-nil one must
	// name a live company. The key being absent leaves the link alone.
	if raw, ok := updates["company_id"]; ok {
		companyID, _ := raw.(*uint)
		if err := checkLeadCompanyLink(companies, companyID); err != nil {
			logger.WithError(utils.LogError(err)).Warn("Lead update rejected")
			return nil, err
		}
		lead.CompanyID = companyID
		lead.CompanyRecord = nil
	}

	if err := writer.Update(lead); err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to update lead")
		return nil, err
	}
	if err := automationLeadUpdated(events, lead.ID, previousStatus, lead.Status, models.AutomationOriginUser, nil); err != nil {
		return nil, err
	}
	return lead, nil
}

// Delete erases the lead's personal data and then soft-deletes the row. It is a
// GDPR Article 17 erasure and it is IRREVERSIBLE: the email becomes a
// non-routable placeholder and the names, phone, company, position, external id
// and free-text notes are cleared. If the lead had been converted, the customer
// it became is erased in the same transaction — the conversion copied the
// person's data into that customer, so erasing one half alone erases nobody.
// See leadRepository.Delete and repository/erasure_cascade.go.
//
// Note the logging below deliberately records only the lead ID: an erasure
// audit trail that quotes the erased email would defeat the erasure.
func (s *leadService) Delete(id uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("lead_id", id), "LeadService", "Delete")

	// An erasure that matched no row must never report success: the scrub and
	// the soft delete both match by primary key, and zero matched rows is not an
	// error in SQL. Reported as the not-found sentinel so the caller can tell
	// "there was nobody to erase" from "the erasure failed".
	if _, err := s.leadRepo.GetByID(id); err != nil {
		if isNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Lead not found")
			return fmt.Errorf("lead %d not found: %w", id, apperrors.ErrNotFound)
		}
		logger.WithError(utils.LogError(err)).Error("Failed to look up lead for erasure")
		return err
	}

	if err := s.leadRepo.Delete(id); err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to erase lead")
		return err
	}

	logger.Info("Lead erased successfully")
	return nil
}

func (s *leadService) List(offset, limit int) ([]models.Lead, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "LeadService", "List")

	// For general lists, preload Owner for display purposes
	leads, err := s.leadRepo.ListWithPreloads(offset, limit, "Owner")
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list leads")
		return nil, 0, err
	}

	total, err := s.leadRepo.Count()
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count leads")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Leads listed successfully")
	return leads, total, nil
}

func (s *leadService) ListSorted(offset, limit int, sortBy, sortOrder string) ([]models.Lead, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "LeadService", "ListSorted")

	leads, err := s.leadRepo.ListSortedWithPreloads(offset, limit, sortBy, sortOrder, "Owner")
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to list leads sorted")
		return nil, 0, err
	}

	total, err := s.leadRepo.Count()
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count leads")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Leads listed sorted successfully")
	return leads, total, nil
}

func (s *leadService) Search(query string, offset, limit int, sortBy, sortOrder string) ([]models.Lead, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "LeadService", "Search")

	leads, err := s.leadRepo.Search(query, offset, limit, sortBy, sortOrder, "Owner")
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to search leads")
		return nil, 0, err
	}

	total, err := s.leadRepo.CountSearch(query)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to count search results")
		return nil, 0, err
	}

	logger.WithField("total", total).Info("Lead search completed")
	return leads, total, nil
}

// LeadConversionRuleError is a conversion refused by one of the
// administrator's lead-conversion settings. It unwraps to
// apperrors.ErrValidation; the handler answers it with 400 VALIDATION_ERROR,
// its message, and the field that failed.
type LeadConversionRuleError struct {
	// Field is the request field the rule is about: "status" (the lead's
	// status is not one the settings allow) or "notes" (notes are required).
	Field   string
	Message string
	// Status and AllowedStatuses are set for a status refusal.
	Status          models.LeadStatus
	AllowedStatuses []string
}

func (e *LeadConversionRuleError) Error() string { return e.Message }
func (e *LeadConversionRuleError) Unwrap() error { return apperrors.ErrValidation }

// Details is the error's `details` object: the field, and for a refusal by
// the allowed statuses the lead's status and the allowed list.
func (e *LeadConversionRuleError) Details() map[string]interface{} {
	details := map[string]interface{}{"field": e.Field}
	if e.Field == "status" && e.AllowedStatuses != nil {
		details["status"] = e.Status
		details["allowed_statuses"] = e.AllowedStatuses
	}
	return details
}

// ConvertedStatusRefusal is the answer to any write other than the
// conversion that would set a lead's status to converted: a lead create, a
// lead update or a bulk status change. Conversion is the only way to reach
// the status, because it creates the customer and enforces the conversion
// settings.
func ConvertedStatusRefusal() *LeadConversionRuleError {
	return &LeadConversionRuleError{
		Field:   "status",
		Status:  models.LeadStatusConverted,
		Message: "A lead's status cannot be set to converted directly. Convert the lead with POST /leads/{id}/convert, which creates the customer.",
	}
}

// leadConversionRules is one read of the lead-conversion settings.
type leadConversionRules struct {
	enforced        bool
	allowedStatuses []string
	requireNotes    bool
	autoAssignOwner bool
}

// loadConversionRules reads the settings once per conversion, so a change an
// administrator saves applies to the next conversion without a restart. A
// failed read fails the conversion: guessing a policy would either let
// through a conversion the administrator forbade or refuse one they allowed.
func (s *leadService) loadConversionRules() (leadConversionRules, error) {
	if s.config == nil {
		return leadConversionRules{}, nil
	}
	statuses, err := s.config.GetLeadConversionStatuses()
	if err != nil {
		return leadConversionRules{}, fmt.Errorf("read allowed conversion statuses: %w", err)
	}
	requireNotes, err := s.config.IsLeadConversionRequireNotes()
	if err != nil {
		return leadConversionRules{}, fmt.Errorf("read conversion notes setting: %w", err)
	}
	autoAssign, err := s.config.IsLeadConversionAutoAssignOwner()
	if err != nil {
		return leadConversionRules{}, fmt.Errorf("read conversion owner setting: %w", err)
	}
	return leadConversionRules{
		enforced:        true,
		allowedStatuses: statuses,
		requireNotes:    requireNotes,
		autoAssignOwner: autoAssign,
	}, nil
}

// checkStatus refuses a lead whose status is not in the allowed list. An
// empty list allows nothing: that is what the administrator configured.
func (r leadConversionRules) checkStatus(status models.LeadStatus) error {
	if !r.enforced {
		return nil
	}
	for _, allowed := range r.allowedStatuses {
		if string(status) == allowed {
			return nil
		}
	}
	message := fmt.Sprintf("Leads with status %q cannot be converted. Allowed statuses: %s.", status, strings.Join(r.allowedStatuses, ", "))
	if len(r.allowedStatuses) == 0 {
		message = "Lead conversion is disabled: no lead status is allowed for conversion."
	}
	return &LeadConversionRuleError{
		Field:           "status",
		Message:         message,
		Status:          status,
		AllowedStatuses: append([]string{}, r.allowedStatuses...),
	}
}

// checkNotes refuses a conversion without notes when notes are required.
// Whitespace alone is no note.
func (r leadConversionRules) checkNotes(notes string) error {
	if !r.enforced || !r.requireNotes || strings.TrimSpace(notes) != "" {
		return nil
	}
	return &LeadConversionRuleError{
		Field:   "notes",
		Message: "Conversion notes are required.",
	}
}

// maxConversionRestarts bounds how often a conversion starts over because the
// lead moved to another company between reading it and locking it.
const maxConversionRestarts = 3

// ErrLeadChangedDuringConversion is a conversion given up because the lead's
// company link kept changing under it; nothing was written and the caller
// may try again (409).
var ErrLeadChangedDuringConversion = errors.New("the lead changed while it was being converted")

// leadCompanyMovedError abandons a conversion attempt whose lead, once its row
// was locked, links another company than the one locked first. It is not a
// retryable database error: ConvertToCustomer catches it and starts a new
// transaction that locks companyID first.
type leadCompanyMovedError struct {
	companyID uint
}

func (e *leadCompanyMovedError) Error() string {
	return fmt.Sprintf("lead moved to company %d during conversion", e.companyID)
}

// logConversionFailure logs a refusal the caller can act on — a setting, a
// missing or taken email, an already converted or missing lead, a lead that
// kept changing — as a warning, and anything else as an error. The handler
// answers the first kind with 4xx and the second with 500.
func logConversionFailure(logger *logrus.Entry, err error, message string) {
	entry := logger.WithError(utils.LogError(err))
	switch {
	case errors.Is(err, apperrors.ErrValidation),
		errors.Is(err, apperrors.ErrLeadConverted),
		errors.Is(err, apperrors.ErrLeadMissingEmail),
		errors.Is(err, apperrors.ErrDuplicateEmail),
		errors.Is(err, ErrLeadChangedDuringConversion),
		isNotFound(err):
		entry.Warn(message)
	default:
		entry.Error(message)
	}
}

// lockLiveCompany locks the company a conversion is about to link and reports
// whether it is live. A nil id locks nothing.
func lockLiveCompany(companies repository.CompanyRepository, companyID *uint) (*uint, bool, error) {
	if companyID == nil {
		return nil, false, nil
	}
	if err := companies.LockByID(*companyID); err != nil {
		if isNotFound(err) {
			return companyID, false, nil
		}
		return nil, false, err
	}
	return companyID, true, nil
}

func sameCompanyID(a, b *uint) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ConvertToCustomer promotes a lead to a customer.
//
// The lead's personal data is COPIED into the new customer and the lead row is
// left standing with its own copy — conversion only flips the status and
// records the new customer's id on the lead. That recorded id (leads.customer_id)
// is the only link between the two rows, and erasure follows it: deleting
// either half erases both, or the person would survive in the other one. See
// repository/erasure_cascade.go.
//
// The administrator's lead-conversion settings are enforced before anything
// is written (issue #104): the lead's status must be one of
// leads.conversion.allowed_statuses, the customer notes must not be blank when
// leads.conversion.require_notes is on, and with
// leads.conversion.auto_assign_owner on the lead's owner becomes the
// customer's account manager (AssignedToID — never UserID, which is the
// customer's own login). The lead's company link is carried over when it
// still names a live company; a link to a deleted company is dropped rather
// than failing the conversion, because the caller did not send it and cannot
// correct it in this request, and deleting a company clears its links anyway.
func (s *leadService) ConvertToCustomer(leadID uint, customerData *models.Customer) (*models.Customer, error) {
	if err := normalizeCustomerWebsite(customerData); err != nil {
		return nil, err
	}
	logger := utils.LogServiceCall(utils.Logger.WithField("lead_id", leadID), "LeadService", "ConvertToCustomer")

	// First, validate the lead outside of transaction
	lead, err := s.leadRepo.GetByID(leadID)
	if err != nil {
		logConversionFailure(logger, err, "Lead not found")
		return nil, err
	}

	if lead.Status == models.LeadStatusConverted {
		logger.Warn("Attempted to convert already converted lead")
		return nil, apperrors.NewLeadAlreadyConverted(leadID)
	}

	rules, err := s.loadConversionRules()
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to read the lead conversion settings")
		return nil, err
	}
	if err := rules.checkStatus(lead.Status); err != nil {
		logger.Warn("Lead conversion refused by the allowed statuses")
		return nil, err
	}
	if err := rules.checkNotes(customerData.Notes); err != nil {
		logger.Warn("Lead conversion refused: notes are required")
		return nil, err
	}

	// Every attempt starts from what the caller sent: a retried attempt must
	// not inherit the id or the derived fields of one that rolled back.
	requested := *customerData

	// Execute conversion within a transaction with retry logic. The company
	// to lock first is the one the lead linked when last read; if the lead
	// has moved to another company by the time its row is locked, the
	// attempt is abandoned and started over for the new company (see
	// leadCompanyMovedError), at most maxConversionRestarts times.
	ctx := context.Background()
	var convertedCustomer *models.Customer
	expectedCompanyID := lead.CompanyID

	for restarts := 0; ; restarts++ {
		err = s.txManager.WithTransactionAndRetry(ctx, func(ctx context.Context) error {
			*customerData = requested

			// Get transaction from context
			tx, ok := utils.GetTxFromContext(ctx)
			if !ok {
				return utils.ErrNoTransaction
			}

			// Use transaction-aware repositories
			txLeadRepo := s.leadRepo.WithTx(tx)
			txCustomerRepo := s.customerRepo.WithTx(tx)
			txCompanyRepo := s.companyRepo.WithTx(tx)

			// Lock order: the company first, then the lead — the order a
			// company delete takes them (its row, then the leads it unlinks) —
			// so the two serialise instead of deadlocking. The company id comes
			// from the last read of the lead; a link changed since restarts the
			// attempt below rather than locking a company under the lead lock.
			lockedCompanyID, companyLive, err := lockLiveCompany(txCompanyRepo, expectedCompanyID)
			if err != nil {
				return err
			}

			// Re-read the lead with a row lock: the status checked here is the
			// status the commit converts, since a concurrent status change or
			// update waits for this transaction (it takes the same lock).
			txLead, err := s.lockedLeadRead(tx).GetByID(leadID)
			if err != nil {
				return err
			}
			if !sameCompanyID(txLead.CompanyID, lockedCompanyID) {
				if txLead.CompanyID != nil {
					// Locking the new company now would take it under the lead
					// lock, the reverse of a company delete's order. Roll back
					// and start over with the new company locked first.
					return &leadCompanyMovedError{companyID: *txLead.CompanyID}
				}
				// Unlinked since the read: there is no company to link.
				lockedCompanyID, companyLive = nil, false
			}

			if txLead.Status == models.LeadStatusConverted {
				return apperrors.NewLeadAlreadyConverted(leadID)
			}
			// The status may have changed since the read above.
			if err := rules.checkStatus(txLead.Status); err != nil {
				return err
			}

			// Prepare customer data from lead data
			if customerData.Email == "" {
				customerData.Email = txLead.Email
			}
			if customerData.FirstName == "" {
				customerData.FirstName = txLead.FirstName
			}
			if customerData.LastName == "" {
				customerData.LastName = txLead.LastName
			}
			if customerData.Phone == "" {
				customerData.Phone = txLead.Phone
			}
			if customerData.Company == "" {
				customerData.Company = txLead.Company
			}
			// A lead may carry only a phone number, but customers need a unique
			// email: refuse before anything is written.
			if customerData.Email == "" {
				return fmt.Errorf("lead %d: %w", leadID, apperrors.ErrLeadMissingEmail)
			}

			// The company row stays locked until the commit, so a concurrent
			// delete either finished before the lock (the link is dropped here)
			// or waits and then unlinks this customer with the others.
			if customerData.CompanyID == nil && lockedCompanyID != nil {
				if companyLive {
					companyID := *lockedCompanyID
					customerData.CompanyID = &companyID
				} else {
					logger.WithField("company_id", *lockedCompanyID).Warn("Lead links a company that no longer exists; converting without the link")
				}
			}

			if rules.autoAssignOwner && txLead.OwnerID != 0 {
				ownerID := txLead.OwnerID
				customerData.AssignedToID = &ownerID
			}

			// Create customer within transaction
			if err := txCustomerRepo.Create(customerData); err != nil {
				logConversionFailure(logger, err, "Failed to create customer within transaction")
				return err
			}

			// Update lead status within transaction
			if err := txLeadRepo.ConvertToCustomer(leadID, customerData.ID); err != nil {
				logger.WithError(utils.LogError(err)).Error("Failed to update lead status within transaction")
				return err
			}

			// The conversion's events ride in the same transaction: lead_updated
			// and the status change into converted, origin conversion.
			if err := automationLeadUpdated(automationTx(s.automation, tx), leadID, txLead.Status, models.LeadStatusConverted, models.AutomationOriginConversion, nil); err != nil {
				return err
			}

			convertedCustomer = customerData
			return nil
		}, 3) // Retry up to 3 times for deadlocks

		var moved *leadCompanyMovedError
		if errors.As(err, &moved) {
			if restarts < maxConversionRestarts {
				companyID := moved.companyID
				expectedCompanyID = &companyID
				continue
			}
			err = fmt.Errorf("lead %d: %w", leadID, ErrLeadChangedDuringConversion)
		}
		break
	}

	if err != nil {
		logConversionFailure(logger, err, "Lead conversion transaction failed")
		return nil, err
	}

	logger.WithFields(map[string]interface{}{
		"customer_id": convertedCustomer.ID,
	}).Info("Lead converted to customer successfully")

	return convertedCustomer, nil
}

func (s *leadService) GetCount() (int64, error) {
	return s.leadRepo.Count()
}

func (s *leadService) GetCountByClassification(classification models.LeadClassification) (int64, error) {
	return s.leadRepo.CountByClassification(classification)
}

// GetStatusCounts returns the live lead count per status, for the dashboard's
// status distribution chart. Statuses with no rows are simply missing from the
// map — the handler owns the label set the chart must always show.
func (s *leadService) GetStatusCounts() (map[string]int64, error) {
	return s.leadRepo.CountByStatus()
}

// GetRecent returns the newest leads across every owner.
func (s *leadService) GetRecent(limit int) ([]models.Lead, error) {
	return s.leadRepo.ListRecent(nil, limit)
}

// GetRecentByOwner returns the newest leads owned by one user. Sales users only
// ever see their own leads, so the dashboard widget narrows through here.
func (s *leadService) GetRecentByOwner(ownerID uint, limit int) ([]models.Lead, error) {
	return s.leadRepo.ListRecent(&ownerID, limit)
}

// GetRecentlyConverted returns the leads most recently marked converted across
// every owner, for the admin activity feed.
func (s *leadService) GetRecentlyConverted(limit int) ([]models.Lead, error) {
	return s.leadRepo.ListRecentlyConverted(nil, limit)
}

// GetRecentlyConvertedByOwner returns the leads most recently marked converted
// that one user owns. The sales activity feed narrows through here.
func (s *leadService) GetRecentlyConvertedByOwner(ownerID uint, limit int) ([]models.Lead, error) {
	return s.leadRepo.ListRecentlyConverted(&ownerID, limit)
}

// GetConversionTimestamps returns the conversion time of every lead converted
// at or after `since`, oldest first, for the sales-performance chart. The
// caller buckets them in Go; see the repository for why not in SQL.
func (s *leadService) GetConversionTimestamps(since time.Time) ([]time.Time, error) {
	return s.leadRepo.ConversionTimestampsSince(since)
}
