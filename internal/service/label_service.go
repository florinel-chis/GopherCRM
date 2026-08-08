package service

import (
	"fmt"
	"regexp"
	"strings"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// labelColorPattern is the authoritative colour format: exactly six hex digits
// behind a hash. The handler's binding tag says the same thing, but the check
// lives here too so a colour can never reach the database through another
// entry point in a shape the frontend cannot render.
var labelColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// labelNameMaxLength mirrors the varchar(50) column. Rejecting an over-long
// name here turns a driver-level truncation error into a plain validation
// failure.
const labelNameMaxLength = 50

type labelService struct {
	labelRepo repository.LabelRepository
}

func NewLabelService(labelRepo repository.LabelRepository) LabelService {
	return &labelService{labelRepo: labelRepo}
}

func (s *labelService) Create(label *models.Label) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("label_name", label.Name), "LabelService", "Create")

	if err := normalizeLabel(label); err != nil {
		logger.WithError(err).Warn("Invalid label")
		return err
	}

	// Case-insensitive pre-check. MySQL's default collation would catch a
	// differently-cased duplicate at the unique index and SQLite's would not, so
	// without this the two backends would disagree about what counts as a
	// duplicate.
	exists, err := s.labelRepo.ExistsByNameInsensitive(label.Name, 0)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}
	if exists {
		logger.Warn("Duplicate label name")
		return fmt.Errorf("label %q already exists: %w", label.Name, apperrors.ErrDuplicateLabelName)
	}

	if err := s.labelRepo.Create(label); err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.WithField("label_id", label.ID).Info("Label created successfully")
	return nil
}

func (s *labelService) GetByID(id uint) (*models.Label, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("label_id", id), "LabelService", "GetByID")

	label, err := s.labelRepo.GetByID(id)
	if err != nil {
		if isNotFound(err) {
			logger.WithError(err).Warn("Label not found")
			return nil, fmt.Errorf("label %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	return label, nil
}

func (s *labelService) Update(label *models.Label) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("label_id", label.ID), "LabelService", "Update")

	if err := normalizeLabel(label); err != nil {
		logger.WithError(err).Warn("Invalid label")
		return err
	}

	// The label being renamed is allowed to collide with itself — a recolour
	// that leaves the name alone is not a duplicate.
	exists, err := s.labelRepo.ExistsByNameInsensitive(label.Name, label.ID)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}
	if exists {
		logger.Warn("Duplicate label name")
		return fmt.Errorf("label %q already exists: %w", label.Name, apperrors.ErrDuplicateLabelName)
	}

	if err := s.labelRepo.Update(label); err != nil {
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.WithField("label_id", label.ID).Info("Label updated successfully")
	return nil
}

func (s *labelService) Delete(id uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("label_id", id), "LabelService", "Delete")

	if _, err := s.labelRepo.GetByID(id); err != nil {
		if isNotFound(err) {
			logger.WithError(err).Warn("Label not found")
			return fmt.Errorf("label %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return err
	}

	if err := s.labelRepo.Delete(id); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("label %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.WithField("label_id", id).Info("Label deleted successfully")
	return nil
}

func (s *labelService) List() ([]models.Label, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "label"), "LabelService", "List")

	labels, err := s.labelRepo.List()
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	if labels == nil {
		labels = []models.Label{}
	}

	logger.WithField("count", len(labels)).Info("Labels listed successfully")
	return labels, nil
}

// normalizeLabel trims the name and validates both fields in place, so the
// stored value is exactly what was validated.
func normalizeLabel(label *models.Label) error {
	label.Name = strings.TrimSpace(label.Name)
	if label.Name == "" {
		return apperrors.ErrInvalidLabelName
	}
	if len([]rune(label.Name)) > labelNameMaxLength {
		return fmt.Errorf("label name is longer than %d characters: %w", labelNameMaxLength, apperrors.ErrInvalidLabelName)
	}

	label.Color = strings.TrimSpace(label.Color)
	if !labelColorPattern.MatchString(label.Color) {
		return fmt.Errorf("%q is not a valid colour: %w", label.Color, apperrors.ErrInvalidLabelColor)
	}

	return nil
}
