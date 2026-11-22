package service

import (
	"context"
	"errors"
	"fmt"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type leadService struct {
	leadRepo     repository.LeadRepository
	customerRepo repository.CustomerRepository
	companyRepo  repository.CompanyRepository
	// userRepo checks the owner a lead write names. Nil refuses those writes
	// with apperrors.ErrLeadOwnerCheckUnavailable, so a service wired without
	// it can never skip the check.
	userRepo  repository.UserRepository
	txManager *utils.TransactionManager
	// automation records the outbox events of every committed lead write
	// (issue #164). Nil means no automation: the writes run as before.
	automation repository.AutomationRepository
	// config supplies the administrator's lead-conversion settings, which
	// ConvertToCustomer enforces (issue #104). Nil means no settings are
	// enforced: no status restriction, notes optional, no owner assignment.
	config ConfigurationService
	// lockHooks are instance-local test seams of the lead/company lock order
	// (#182); zero in production.
	lockHooks leadCompanyLockHooks
	// afterDeletePreRead is an instance-local test seam of Delete (#337): it
	// runs after the plain pre-read of the lead and before the erasure unit
	// starts. Nil in production.
	afterDeletePreRead func(leadID uint)
	// webhooks is the outbound webhook emitter (#345): Create emits
	// lead.created, Update lead.updated and ConvertToCustomer
	// customer.created in their own transaction. NewLeadService sets it to
	// webhook.Nop, so it is never nil.
	webhooks webhook.Emitter
}

// WithLeadWebhooks makes Create emit lead.created, Update lead.updated (#408,
// when a stored value changed) and ConvertToCustomer customer.created (inside
// each attempt) through e, bound to the transaction that commits the record
// (#345). A nil e means webhook.Nop: no webhooks.
func WithLeadWebhooks(e webhook.Emitter) LeadServiceOption {
	return func(s *leadService) {
		if e == nil {
			e = webhook.Nop
		}
		s.webhooks = e
	}
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

// WithLeadAutomationEvents makes Create, Update, Assign and ConvertToCustomer
// record their automation events inside the transaction that commits the write.
func WithLeadAutomationEvents(repo repository.AutomationRepository) LeadServiceOption {
	return func(s *leadService) { s.automation = repo }
}

func NewLeadService(leadRepo repository.LeadRepository, customerRepo repository.CustomerRepository, companyRepo repository.CompanyRepository, userRepo repository.UserRepository, txManager *utils.TransactionManager, opts ...LeadServiceOption) LeadService {
	s := &leadService{
		leadRepo:     leadRepo,
		customerRepo: customerRepo,
		companyRepo:  companyRepo,
		userRepo:     userRepo,
		txManager:    txManager,
		webhooks:     webhook.Nop,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// createInTransaction runs write inside one transaction together with the
// owner check and the automation events it produces, so an event is never
// recorded for a write that rolled back and the insert follows a locking read
// of its owner. Without a transaction manager the write runs directly, with a
// plain owner read, no events and no webhook outbox rows.
func (s *leadService) createInTransaction(write func(ctx context.Context, leads repository.LeadRepository, users repository.UserRepository, events repository.AutomationRepository, hooks webhook.TxEmitter) error) error {
	if s.txManager == nil {
		return write(context.Background(), s.leadRepo, s.userRepo, nil, webhook.NopTx)
	}
	return s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		return write(ctx, s.leadRepo.WithTx(tx), s.lockedUserRead(tx), automationTx(s.automation, tx), s.webhooks.Bind(tx))
	})
}

// The reason an owner was refused, as logged. It tells an operator what to
// look at; the API answer is the same for all three.
const (
	leadOwnerRefusalUnknown  = "unknown"
	leadOwnerRefusalInactive = "inactive"
	leadOwnerRefusalRole     = "role"
)

// checkLeadOwner refuses an owner that is not a live, active admin or sales
// user with apperrors.ErrInvalidLeadOwner. leadID is 0 on a create. An erased
// user's row still satisfies the foreign key but no scoped read finds it, so it
// counts as unknown. A lookup that fails for any other reason is returned as it
// is: a database fault is not a bad reference.
//
// The refusal logs and returns ids and the reason only: the user read here has
// a name and an address that must reach neither.
func checkLeadOwner(logger *logrus.Entry, users repository.UserRepository, leadID, ownerID uint) error {
	if users == nil {
		logger.Error("Lead owner check is unavailable: no user repository is wired")
		return apperrors.ErrLeadOwnerCheckUnavailable
	}
	if ownerID == 0 {
		return refuseLeadOwner(logger, leadID, ownerID, leadOwnerRefusalUnknown)
	}
	owner, err := users.GetByID(ownerID)
	if err != nil {
		if isNotFound(err) {
			return refuseLeadOwner(logger, leadID, ownerID, leadOwnerRefusalUnknown)
		}
		logger.WithError(utils.LogError(err)).Error("Failed to look up lead owner")
		return err
	}
	if !owner.IsActive {
		return refuseLeadOwner(logger, leadID, ownerID, leadOwnerRefusalInactive)
	}
	if !permissions.ForUser(owner).Has(permissions.AssignmentFormOwner) {
		return refuseLeadOwner(logger, leadID, ownerID, leadOwnerRefusalRole)
	}
	return nil
}

func refuseLeadOwner(logger *logrus.Entry, leadID, ownerID uint, reason string) error {
	entry := logger.WithFields(logrus.Fields{"owner_id": ownerID, "reason": reason})
	if leadID != 0 {
		entry = entry.WithField("lead_id", leadID)
	}
	entry.Warn("Lead owner refused")
	return fmt.Errorf("lead owner %d refused (%s): %w", ownerID, reason, apperrors.ErrInvalidLeadOwner)
}

// lockedUserRead returns a user repository bound to tx whose reads take a row
// lock, or nil when no user repository is wired. The lock is what makes the
// read current on MySQL: a plain read inside REPEATABLE READ answers from the
// snapshot of the transaction's first plain read (a company check, for one), so
// a deactivation committed after it would still look active. It does not stop a
// later deactivation; a shared protocol with user writes is #183.
func (s *leadService) lockedUserRead(tx *gorm.DB) repository.UserRepository {
	if s.userRepo == nil {
		return nil
	}
	return s.userRepo.WithTx(repository.LockingRead(tx))
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

	err := s.createInTransaction(func(ctx context.Context, leads repository.LeadRepository, users repository.UserRepository, events repository.AutomationRepository, hooks webhook.TxEmitter) error {
		if err := checkLeadOwner(logger, users, 0, lead.OwnerID); err != nil {
			return err
		}
		if err := leads.Create(lead); err != nil {
			return err
		}
		if err := automationLeadCreated(events, lead.ID, models.AutomationOriginUser, nil); err != nil {
			return err
		}
		return hooks.Created(ctx, webhook.Lead, lead.ID)
	})
	if err != nil && s.txManager != nil {
		// The transactional insert rolled back, with any outbox rows.
		lead.ID = 0
	}
	if err != nil {
		// A refused owner was logged, as a warning, by the check.
		if !errors.Is(err, apperrors.ErrInvalidLeadOwner) {
			logger.WithError(utils.LogError(err)).Error("Failed to create lead")
		}
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

// GetByIDContext reads the lead like GetByID on a handle bound to ctx.
func (s *leadService) GetByIDContext(ctx context.Context, id uint) (*models.Lead, error) {
	if s.txManager == nil {
		return s.GetByID(id)
	}
	return s.leadRepo.WithTx(s.txManager.DB(ctx)).GetByIDWithPreloads(id, "Owner", "CompanyRecord")
}

// lockedLeadRead returns a lead repository bound to tx whose reads take a row
// lock (SELECT ... FOR UPDATE; SQLite drops the clause and is serialised by
// its single connection). The fresh Session keeps the clause off every later
// statement on tx: writes go through s.leadRepo.WithTx(tx).
func (s *leadService) lockedLeadRead(tx *gorm.DB) repository.LeadRepository {
	return s.leadRepo.WithTx(repository.LockingRead(tx))
}

// Update applies a partial update. The lead is re-read with a row lock inside
// the transaction that writes it, so the whole-row save never carries back a
// stale copy of what a concurrent write (a conversion setting status and
// customer_id) committed in between. A changed owner_id is checked with a
// locking read of the user (see lockedUserRead); an unchanged one is not
// checked, so a lead whose owner has since left stays editable.
//
// With a transaction manager the update follows the lead/company lock order
// (#182): each attempt reads the stored company_id plainly, locks the stored
// company and the sent company_id (ascending id, CompanyRepository.LockLive),
// then locks the lead; a locked row whose company_id differs from the plain
// read restarts the attempt (at most 3 restarts, then an error matching
// apperrors.ErrLeadCompanyContended). A sent company_id is checked against
// that lock, so a company deleted while the update waited is
// apperrors.ErrCompanyNotFound; a stored company deleted in between is not an
// error (the delete already unlinked the lead, and the restart sees it).
// Without a transaction manager the company is checked with a plain read.
//
// `converted` is not a status an update can set: a lead becomes converted
// only through ConvertToCustomer, which creates the customer and enforces the
// conversion settings. Sending the status a converted lead already has (the
// edit form does) is fine.
func (s *leadService) Update(id uint, updates map[string]interface{}) (*models.Lead, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("lead_id", id), "LeadService", "Update")

	if s.txManager == nil {
		companyCheck := func(companyID *uint) error { return checkLeadCompanyLink(s.companyRepo, companyID) }
		return s.applyUpdate(context.Background(), logger, id, updates, s.leadRepo, s.leadRepo, companyCheck, nil, s.userRepo, nil, webhook.NopTx)
	}

	var sentCompanyID *uint
	if raw, ok := updates["company_id"]; ok {
		sentCompanyID, _ = raw.(*uint)
	}

	ctx := context.Background()
	hooks := s.lockHooks
	var updated *models.Lead
	err := runLeadCompanyAttempts(ctx, logger, leadCompanyOpUpdate, id, func(attempt int) error {
		return s.txManager.WithTransaction(ctx, func(ctx context.Context) (err error) {
			if hooks.attemptStarted != nil {
				hooks.attemptStarted(leadCompanyOpUpdate, id)
			}
			if hooks.attemptEnded != nil {
				defer func() { hooks.attemptEnded(leadCompanyOpUpdate, id, err) }()
			}
			tx, ok := utils.GetTxFromContext(ctx)
			if !ok {
				return utils.ErrNoTransaction
			}

			// (1) The stored company_id, read plainly on the transaction.
			plain, err := s.leadRepo.WithTx(tx).GetByID(id)
			if err != nil {
				logger.WithError(utils.LogError(err)).Error("Lead not found")
				return err
			}
			storedCompanyID := copyCompanyID(plain.CompanyID)
			if hooks.afterPlainRead != nil {
				hooks.afterPlainRead(leadCompanyOpUpdate, tx, id)
			}

			// (2) The companies, ascending id, before the lead.
			live, err := s.lockLeadCompanies(logger, tx, leadCompanyOpUpdate, id, storedCompanyID, sentCompanyID)
			if err != nil {
				return err
			}
			companyCheck := func(companyID *uint) error {
				if companyID == nil {
					return nil
				}
				if isLive, locked := live[*companyID]; locked {
					if isLive {
						return nil
					}
					return fmt.Errorf("unknown company_id %d: %w", *companyID, apperrors.ErrCompanyNotFound)
				}
				return checkLeadCompanyLink(s.companyRepo.WithTx(tx), companyID)
			}
			// (3) The lead, locked, compared with the plain read.
			afterRead := func(lead *models.Lead) error {
				return checkLockedLeadCompany(logger, leadCompanyOpUpdate, id, attempt, storedCompanyID, lead.CompanyID)
			}

			updated, err = s.applyUpdate(ctx, logger, id, updates, s.lockedLeadRead(tx), s.leadRepo.WithTx(tx), companyCheck, afterRead, s.lockedUserRead(tx), automationTx(s.automation, tx), s.webhooks.Bind(tx))
			if err != nil {
				return err
			}
			if hooks.beforeCommit != nil {
				hooks.beforeCommit(leadCompanyOpUpdate, id)
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	logger.Info("Lead updated successfully")
	return updated, nil
}

// applyUpdate reads the lead through reader, applies the updates and writes it
// through writer. companyCheck validates a sent company_id; afterRead, when
// set, runs on the row reader returned before anything is applied. hooks is
// the webhook outbox bound to the same transaction (nil without one): after
// the write it gets lead.updated when the written row differs from the row
// reader returned (#408).
func (s *leadService) applyUpdate(ctx context.Context, logger *logrus.Entry, id uint, updates map[string]interface{}, reader, writer repository.LeadRepository, companyCheck func(*uint) error, afterRead func(*models.Lead) error, users repository.UserRepository, events repository.AutomationRepository, hooks webhook.TxEmitter) (*models.Lead, error) {
	lead, err := reader.GetByID(id)
	if err != nil {
		logger.WithError(utils.LogError(err)).Error("Lead not found")
		return nil, err
	}
	if afterRead != nil {
		if err := afterRead(lead); err != nil {
			return nil, err
		}
	}
	previousStatus := lead.Status
	before := *lead

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
	// company_id is a *uint: a nil pointer clears the link, a non-nil one must
	// name a live company. The key being absent leaves the link alone.
	if raw, ok := updates["company_id"]; ok {
		companyID, _ := raw.(*uint)
		if err := companyCheck(companyID); err != nil {
			logger.WithError(utils.LogError(err)).Warn("Lead update rejected")
			return nil, err
		}
		lead.CompanyID = companyID
		lead.CompanyRecord = nil
	}
	if ownerID, ok := updates["owner_id"].(uint); ok && ownerID != lead.OwnerID {
		if err := checkLeadOwner(logger, users, id, ownerID); err != nil {
			return nil, err
		}
		lead.OwnerID = ownerID
	}

	if err := writer.Update(lead); err != nil {
		logger.WithError(utils.LogError(err)).Error("Failed to update lead")
		return nil, err
	}
	if err := automationLeadUpdated(events, lead.ID, previousStatus, lead.Status, models.AutomationOriginUser, nil); err != nil {
		return nil, err
	}
	if leadStoredValuesChanged(&before, lead) {
		if err := hooks.Updated(ctx, webhook.Lead, lead.ID); err != nil {
			return nil, err
		}
	}
	return lead, nil
}

// leadStoredValuesChanged reports whether an update changed any column it can
// write: the rule of the lead.updated webhook event (#408). updated_at is not
// part of it.
func leadStoredValuesChanged(before, after *models.Lead) bool {
	return before.FirstName != after.FirstName ||
		before.LastName != after.LastName ||
		before.Email != after.Email ||
		before.Phone != after.Phone ||
		before.Company != after.Company ||
		before.Position != after.Position ||
		before.Source != after.Source ||
		before.Status != after.Status ||
		before.Classification != after.Classification ||
		before.ExternalID != after.ExternalID ||
		before.Notes != after.Notes ||
		!equalUintPtr(before.CompanyID, after.CompanyID) ||
		before.OwnerID != after.OwnerID
}

// Assign changes a lead's owner in one transaction: a locking read of the lead,
// the visibility policy on that row (so a sales user who just handed the lead
// over is refused), a locking read of the new owner, the write and its
// lead_updated event. Unlike an update, it always checks the owner it names,
// even the lead's current one: an assignment states that this user should own
// the lead now. A converted lead can be assigned; status and customer_id stay
// as they were.
//
// There is no non-transactional path: without a transaction manager or a user
// repository it returns apperrors.ErrLeadOwnerCheckUnavailable and reads
// nothing.
func (s *leadService) Assign(ctx context.Context, leadID, ownerID uint, actor LeadViewer) (*models.Lead, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("lead_id", leadID), "LeadService", "Assign")

	if s.userRepo == nil || s.txManager == nil {
		logger.Error("Lead assignment is unavailable: no user repository or transaction manager is wired")
		return nil, apperrors.ErrLeadOwnerCheckUnavailable
	}

	var assigned *models.Lead
	err := s.txManager.WithTransaction(ctx, func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		lead, err := s.lockedLeadRead(tx).GetByID(leadID)
		if err != nil {
			return err
		}
		if !CanViewLead(actor, lead) {
			return fmt.Errorf("lead %d is not visible to user %d: %w", leadID, actor.UserID, apperrors.ErrForbidden)
		}
		if err := checkLeadOwner(logger, s.lockedUserRead(tx), leadID, ownerID); err != nil {
			return err
		}
		lead.OwnerID = ownerID
		if err := s.leadRepo.WithTx(tx).Update(lead); err != nil {
			return err
		}
		if err := automationLeadUpdated(automationTx(s.automation, tx), leadID, lead.Status, lead.Status, models.AutomationOriginUser, nil); err != nil {
			return err
		}
		assigned = lead
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, apperrors.ErrInvalidLeadOwner):
			// Logged, as a warning, by the check.
		case errors.Is(err, apperrors.ErrForbidden), isNotFound(err):
			logger.WithError(utils.LogError(err)).Warn("Lead assignment refused")
		default:
			logger.WithError(utils.LogError(err)).Error("Failed to assign lead")
		}
		return nil, err
	}

	logger.WithField("owner_id", ownerID).Info("Lead assigned successfully")
	return assigned, nil
}

// Delete erases the lead's personal data and then soft-deletes the row. It is a
// GDPR Article 17 erasure and it is IRREVERSIBLE: the email becomes a
// non-routable placeholder and the names, phone, company, position, external id
// and free-text notes are cleared. If the lead had been converted, the customer
// it became — and that customer's own login and other converted leads — are
// erased in the same unit: the conversion copied the person's data into that
// customer, so erasing one half alone erases nobody. See leadRepository.Delete
// and repository/erasure_cascade.go.
//
// actorRole decides how far the erasure may reach. Erasing a customer is an
// admin action, so only an admin may erase a converted lead; any other caller
// (an owning sales user) gets leadRepository.DeleteUnconverted, which refuses a
// converted lead with an apperrors.ErrForbidden inside the erasure unit, on the
// locked lead row, and rolls the unit back. Ownership of the lead stays the
// handler's check.
//
// Note the logging below deliberately records only the lead ID: an erasure
// audit trail that quotes the erased email would defeat the erasure.
func (s *leadService) Delete(ctx context.Context, id uint, actorRole models.UserRole) error {
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
	// The pre-read decides nothing but the 404: whether the lead is converted
	// is decided inside the erasure unit, on the lead row it locks (#337).
	if s.afterDeletePreRead != nil {
		s.afterDeletePreRead(id)
	}

	erase := s.leadRepo.DeleteUnconverted
	if actorRole == models.RoleAdmin {
		erase = s.leadRepo.Delete
	}
	if err := erase(ctx, id); err != nil {
		if errors.Is(err, apperrors.ErrForbidden) {
			logger.Warn("Erasure of a converted lead refused for a non-admin caller")
			return err
		}
		logger.WithError(utils.LogError(err)).Error("Failed to erase lead")
		return err
	}

	logger.Info("Lead erased successfully")
	return nil
}
