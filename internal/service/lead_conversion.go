package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/sirupsen/logrus"
)

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

// logConversionFailure logs a refusal the caller can act on — a setting, a
// missing or taken email, an already converted or missing lead — as a
// warning, and anything else as an error. The handler answers the first kind
// with 4xx and the second with 500.
func logConversionFailure(logger *logrus.Entry, err error, message string) {
	entry := logger.WithError(utils.LogError(err))
	switch {
	case errors.Is(err, apperrors.ErrValidation),
		errors.Is(err, apperrors.ErrLeadConverted),
		errors.Is(err, apperrors.ErrLeadMissingEmail),
		errors.Is(err, apperrors.ErrDuplicateEmail),
		errors.Is(err, apperrors.ErrLeadCompanyContended),
		errors.Is(err, apperrors.ErrCompanyNotFound),
		isNotFound(err):
		entry.Warn(message)
	default:
		entry.Error(message)
	}
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
// still names a live company under the company row lock the attempt takes
// before the lead's (the lead/company lock order, #182); a link to a deleted
// company is dropped rather than failing the conversion, because the caller
// did not send it and cannot correct it in this request, and deleting a
// company clears its links anyway. A lead whose company_id keeps changing
// between the attempt's plain read and its locked read is restarted at most 3
// times, then refused with apperrors.ErrLeadCompanyContended.
//
// ctx is the caller's request (issue #107). It bounds the lead read, the wait
// for a connection and for the lead's row lock, and the retries; a cancelled
// request returns the context's error and writes nothing. The conversion
// settings are read through the configuration service, which does not take a
// context yet.
//
// The conversion is a transaction root: a ctx that already carries a
// transaction is refused with utils.ErrTransactionRootRequired before anything
// is read, because the settings reads would need a second connection and the
// conversion could not be undone by the enclosing rollback it cannot see.
func (s *leadService) ConvertToCustomer(ctx context.Context, leadID uint, customerData *models.Customer) (*models.Customer, error) {
	if utils.InTransaction(ctx) {
		return nil, utils.ErrTransactionRootRequired
	}
	if err := normalizeCustomerWebsite(customerData); err != nil {
		return nil, err
	}
	logger := utils.LogServiceCall(utils.Logger.WithField("lead_id", leadID), "LeadService", "ConvertToCustomer")

	// First, validate the lead outside of transaction. The read is bound to
	// the request: a caller that has gone away stops waiting for a connection.
	lead, err := s.leadRepo.WithTx(s.txManager.DB(ctx)).GetByID(leadID)
	if err != nil {
		logConversionFailure(logger, err, "Lead not found")
		return nil, err
	}

	if lead.Status == models.LeadStatusConverted || lead.CustomerID != nil {
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

	// Execute conversion within a transaction with retry logic. The request
	// context bounds the wait for a connection and for the company and lead
	// row locks, and a cancelled request stops the retries and restarts; the
	// transaction then rolls back, so a conversion is either committed whole or
	// not at all.
	//
	// Each attempt follows the lead/company lock order (#182): a plain read of
	// the lead's company_id on the transaction, the row locks of that company
	// and of a caller-supplied company (ascending id), then the lead's row
	// lock. A locked lead that names another company than the plain read ends
	// the attempt with errLeadCompanyMoved, and runLeadCompanyAttempts restarts
	// it (at most 3 times, then apperrors.ErrLeadCompanyContended).
	var convertedCustomer *models.Customer
	hooks := s.lockHooks

	err = runLeadCompanyAttempts(ctx, logger, leadCompanyOpConvert, leadID, func(attempt int) error {
		return s.txManager.WithTransactionAndRetry(ctx, func(ctx context.Context) (err error) {
			if hooks.attemptStarted != nil {
				hooks.attemptStarted(leadCompanyOpConvert, leadID)
			}
			if hooks.attemptEnded != nil {
				defer func() { hooks.attemptEnded(leadCompanyOpConvert, leadID, err) }()
			}
			return s.convertAttempt(ctx, logger, leadID, attempt, customerData, requested, rules, hooks, &convertedCustomer)
		}, 3) // Retry up to 3 times for deadlocks
	})

	if err != nil {
		logConversionFailure(logger, err, "Lead conversion transaction failed")
		return nil, err
	}

	logger.WithFields(map[string]interface{}{
		"customer_id": convertedCustomer.ID,
	}).Info("Lead converted to customer successfully")

	return convertedCustomer, nil
}

// convertAttempt is one attempt of ConvertToCustomer inside its transaction.
func (s *leadService) convertAttempt(ctx context.Context, logger *logrus.Entry, leadID uint, attempt int, customerData *models.Customer, requested models.Customer, rules leadConversionRules, hooks leadCompanyLockHooks, converted **models.Customer) error {
	*customerData = requested

	// Get transaction from context
	tx, ok := utils.GetTxFromContext(ctx)
	if !ok {
		return utils.ErrNoTransaction
	}

	// Use transaction-aware repositories
	txLeadRepo := s.leadRepo.WithTx(tx)
	txCustomerRepo := s.customerRepo.WithTx(tx)

	// (1) The lead's company_id, read plainly on the transaction.
	plain, err := txLeadRepo.GetByID(leadID)
	if err != nil {
		return err
	}
	plainCompanyID := copyCompanyID(plain.CompanyID)
	if hooks.afterPlainRead != nil {
		hooks.afterPlainRead(leadCompanyOpConvert, tx, leadID)
	}

	// (2) The companies, ascending id, before the lead.
	live, err := s.lockLeadCompanies(logger, tx, leadCompanyOpConvert, leadID, plainCompanyID, requested.CompanyID)
	if err != nil {
		return err
	}
	if requested.CompanyID != nil && !live[*requested.CompanyID] {
		return fmt.Errorf("unknown company_id %d: %w", *requested.CompanyID, apperrors.ErrCompanyNotFound)
	}

	// (3) Re-read the lead with a row lock: the status checked here is the
	// status the commit converts, since a concurrent status change or update
	// waits for this transaction (it takes the same lock).
	txLead, err := s.lockedLeadRead(tx).GetByID(leadID)
	if err != nil {
		return err
	}
	if err := checkLockedLeadCompany(logger, leadCompanyOpConvert, leadID, attempt, plainCompanyID, txLead.CompanyID); err != nil {
		return err
	}

	// A lead that already has a customer was converted before, even if
	// its status has since moved on: converting it again would create a
	// second customer for the same person. Checked on the locked row,
	// before any insert.
	if txLead.Status == models.LeadStatusConverted || txLead.CustomerID != nil {
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

	// Carry the company link over when the company is live under the lock
	// taken above (#182): a company delete either committed first (the lock
	// found it deleted, and the delete had unlinked the lead, so the attempt
	// restarted on the fresh value) or waits for this commit and then unlinks
	// the new customer.
	if customerData.CompanyID == nil && txLead.CompanyID != nil {
		companyID := *txLead.CompanyID
		if live[companyID] {
			customerData.CompanyID = &companyID
		} else {
			logger.WithFields(logrus.Fields{"lead_id": leadID, "company_id": companyID, "error_code": "company_not_found"}).
				Warn("Lead links a company that no longer exists; converting without the link")
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

	// The new customer's customer.created outbox rows (#345), written inside
	// this attempt: a rolled-back attempt takes its rows with it, so a replay
	// commits exactly one set.
	if err := s.webhooks.Bind(tx).Created(ctx, webhook.Customer, customerData.ID); err != nil {
		return err
	}
	// And the lead's lead.updated rows (#408), in the same attempt. The attempt
	// refused a converted lead above, so status and customer_id always change.
	if err := s.webhooks.Bind(tx).Updated(ctx, webhook.Lead, leadID); err != nil {
		return err
	}

	if hooks.beforeCommit != nil {
		hooks.beforeCommit(leadCompanyOpConvert, leadID)
	}
	*converted = customerData
	return nil
}
