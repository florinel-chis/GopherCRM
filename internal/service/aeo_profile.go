package service

import (
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// ---------------------------------------------------------------- profile ---

func (s *aeoService) GetProfile() (*models.AEOProfile, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "aeo_profile"), "AEOService", "GetProfile")

	profile, err := s.repo.GetProfile()
	if err != nil {
		if isNotFound(err) {
			logger.Debug("AEO profile is not configured yet")
			return nil, apperrors.ErrProfileNotConfigured
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	return profile, nil
}

func (s *aeoService) SaveProfile(profile *models.AEOProfile) (*models.AEOProfile, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "aeo_profile"), "AEOService", "SaveProfile")

	if profile == nil {
		return nil, ErrAEOInvalidProfile
	}
	if err := normalizeAEOProfile(profile); err != nil {
		logger.WithError(utils.LogError(err)).Warn("Invalid AEO profile")
		return nil, err
	}

	if err := s.repo.UpsertProfile(profile); err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	logger.Info("AEO profile saved")
	return profile, nil
}

// ---------------------------------------------------------------- helpers ---

// profileOrNil returns the brand profile, or nil when none has been configured.
// The metrics endpoints must still answer before setup: an empty dashboard is a
// better first-run experience than an error page.
func (s *aeoService) profileOrNil() (*models.AEOProfile, error) {
	profile, err := s.repo.GetProfile()
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return profile, nil
}
