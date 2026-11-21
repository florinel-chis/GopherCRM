package service

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// ---------------------------------------------------------------------------
// Administration
// ---------------------------------------------------------------------------

func (s *formService) Create(form *models.Form, actorID uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "form"), "FormService", "Create")

	form.CreatedByID = actorID
	if err := s.prepare(form); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Rejected form definition")
		return err
	}

	if err := s.assignPublicID(form); err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}

	if err := s.repo.Create(form); err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.WithField("form_id", form.ID).Info("Form created successfully")
	return nil
}

func (s *formService) GetByID(id uint) (*models.Form, error) {
	form, err := s.repo.GetByID(id)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, fmt.Errorf("form %d not found: %w", id, apperrors.ErrNotFound)
		}
		return nil, err
	}
	return form, nil
}

// List returns one page of forms, the submission count of each form on that
// page, and the total number of forms matching the status filter. An unknown
// sort column is a bad request rather than a silent fallback, so a typo in a
// query string is visible instead of quietly reordering the list.
func (s *formService) List(offset, limit int, status, sortBy, sortOrder string) ([]models.Form, map[uint]int64, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithFields(map[string]interface{}{
		"offset": offset,
		"limit":  limit,
	}), "FormService", "List")

	if sortBy != "" && !formSortColumns[sortBy] {
		return nil, nil, 0, FieldErrors{"sort_by": fmt.Sprintf("cannot sort forms by %q", sortBy)}
	}
	if status != "" {
		switch models.FormStatus(status) {
		case models.FormStatusDraft, models.FormStatusPublished, models.FormStatusArchived:
		default:
			return nil, nil, 0, FieldErrors{"status": fmt.Sprintf("unknown form status %q", status)}
		}
	}

	logger = logger.WithField("status", status)
	formList, total, err := s.repo.List(offset, limit, status, sortBy, sortOrder)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, nil, 0, err
	}

	ids := make([]uint, 0, len(formList))
	for i := range formList {
		ids = append(ids, formList[i].ID)
	}

	counts, err := s.repo.SubmissionCounts(ids)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, nil, 0, err
	}

	return formList, counts, total, nil
}

// Update replaces a form's definition and settings wholesale. The public
// identifier and the author are copied from the stored row: the first is a
// published address that must keep working, the second is an audit fact.
func (s *formService) Update(id uint, form *models.Form) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("form_id", id), "FormService", "Update")

	existing, err := s.repo.GetByID(id)
	if err != nil {
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Form not found")
			return fmt.Errorf("form %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return err
	}

	form.ID = existing.ID
	form.CreatedAt = existing.CreatedAt
	form.DeletedAt = existing.DeletedAt
	form.PublicID = existing.PublicID
	form.CreatedByID = existing.CreatedByID

	if err := s.prepare(form); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Rejected form definition")
		return err
	}

	if err := s.repo.Update(form); err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.Info("Form updated successfully")
	return nil
}

func (s *formService) Delete(id uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("form_id", id), "FormService", "Delete")

	if err := s.repo.Delete(id); err != nil {
		if apperrors.IsNotFound(err) {
			logger.WithError(utils.LogError(err)).Warn("Form not found")
			return fmt.Errorf("form %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.Info("Form deleted successfully")
	return nil
}

func (s *formService) ListSubmissions(formID uint, offset, limit int, status string) ([]models.FormSubmission, int64, error) {
	if _, err := s.GetByID(formID); err != nil {
		return nil, 0, err
	}
	return s.withDeliveries(s.repo.ListSubmissions(formID, offset, limit, status))
}

func (s *formService) GetSubmission(id uint) (*models.FormSubmission, error) {
	submission, err := s.repo.GetSubmissionByID(id)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, fmt.Errorf("form submission %d not found: %w", id, apperrors.ErrNotFound)
		}
		return nil, err
	}
	return s.withSubmissionDeliveries(submission)
}

// prepare normalises and validates a form for storage: it fills in the
// defaults the model does not carry as column defaults, runs the definition
// rules, and checks the lead owner.
func (s *formService) prepare(form *models.Form) error {
	form.Name = strings.TrimSpace(form.Name)
	if form.Status == "" {
		form.Status = models.FormStatusDraft
	}
	if form.SubmitAction == "" {
		form.SubmitAction = models.FormSubmitActionMessage
	}

	switch form.Status {
	case models.FormStatusDraft, models.FormStatusPublished, models.FormStatusArchived:
	default:
		return FieldErrors{"status": fmt.Sprintf("unknown form status %q", form.Status)}
	}

	// ValidateDefinition wraps models.ErrInvalidFormDefinition, which is the
	// models package's own sentinel; the service boundary is where it becomes
	// the application-wide validation sentinel a handler answers with 400.
	if err := form.ValidateDefinition(); err != nil {
		return fmt.Errorf("%w: %w", err, apperrors.ErrValidation)
	}

	return s.checkLeadOwner(form)
}

// checkLeadOwner makes sure created leads land with someone who can work them:
// an existing, active user holding a role that owns leads.
func (s *formService) checkLeadOwner(form *models.Form) error {
	if form.DefaultOwnerID == 0 {
		return nil
	}

	owner, err := s.userRepo.GetByID(form.DefaultOwnerID)
	if err != nil {
		if apperrors.IsNotFound(err) {
			return FieldErrors{"default_owner_id": fmt.Sprintf("user %d does not exist", form.DefaultOwnerID)}
		}
		return err
	}
	if !owner.IsActive {
		return FieldErrors{"default_owner_id": "the lead owner is deactivated"}
	}
	if !permissions.ForUser(owner).Has(permissions.AssignmentFormOwner) {
		return FieldErrors{"default_owner_id": "leads can only be owned by an admin or sales user"}
	}
	return nil
}

// assignPublicID picks an identifier no live form is using. A 128-bit value
// makes a genuine collision a theoretical concern only; the lookup is what
// turns that theory into a retry instead of a failed insert, since the
// repository's unique-constraint classifier is package-private and a create
// error the service cannot classify must never be retried blindly.
func (s *formService) assignPublicID(form *models.Form) error {
	for attempt := 0; attempt < formPublicIDAttempts; attempt++ {
		candidate, err := newFormPublicID()
		if err != nil {
			return err
		}

		_, err = s.repo.GetByPublicID(candidate)
		if err == nil {
			continue
		}
		if !apperrors.IsNotFound(err) {
			return err
		}

		form.PublicID = candidate
		return nil
	}
	return errors.New("failed to allocate a unique public form id")
}

func newFormPublicID() (string, error) {
	buf := make([]byte, formPublicIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate public form id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
