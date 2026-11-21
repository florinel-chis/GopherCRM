package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// ---------------------------------------------------------------- prompts ---

func (s *aeoService) ListPrompts(from, to time.Time, activeOnly bool, offset, limit int, sortBy, sortOrder string) ([]models.AEOPrompt, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "aeo_prompt"), "AEOService", "ListPrompts")

	prompts, err := s.repo.ListPrompts(activeOnly, offset, limit, sortBy, sortOrder)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}
	if prompts == nil {
		prompts = []models.AEOPrompt{}
	}

	total, err := s.repo.CountPrompts(activeOnly)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}

	// Visibility is a property of the requested window, never of the row, so it
	// is recomputed on every listing rather than denormalized onto the table.
	if len(prompts) > 0 {
		ids := make([]uint, 0, len(prompts))
		for i := range prompts {
			ids = append(ids, prompts[i].ID)
		}

		stats, err := s.repo.PromptVisibility(from, to, ids)
		if err != nil {
			utils.LogServiceResponse(logger, err)
			return nil, 0, err
		}
		for i := range prompts {
			stat := stats[prompts[i].ID]
			prompts[i].AnswerCount = stat.Answers
			prompts[i].MentionCount = stat.Mentions
			prompts[i].LastRunAt = stat.LastRunAt
			prompts[i].Visibility = aeoPercent(stat.Mentions, stat.Answers)
		}
	}

	return prompts, total, nil
}

func (s *aeoService) CreatePrompts(texts []string, createdByID uint) ([]models.AEOPrompt, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "aeo_prompt"), "AEOService", "CreatePrompts")

	if len(texts) == 0 {
		return nil, fmt.Errorf("no prompts supplied: %w", ErrAEOInvalidPrompt)
	}

	// Normalize first so the duplicate checks — both within the batch and
	// against the database — compare exactly the strings that will be stored.
	normalized := make([]string, 0, len(texts))
	seen := make(map[string]bool, len(texts))
	for _, raw := range texts {
		text, err := normalizeAEOPromptText(raw)
		if err != nil {
			logger.WithError(utils.LogError(err)).Warn("Invalid AEO prompt text")
			return nil, err
		}
		key := strings.ToLower(text)
		if seen[key] {
			logger.Warn("Duplicate prompt text within the batch")
			return nil, fmt.Errorf("prompt %q is repeated in this request: %w", text, apperrors.ErrDuplicatePrompt)
		}
		seen[key] = true
		normalized = append(normalized, text)
	}

	var created []models.AEOPrompt
	var ownerID *uint
	if createdByID != 0 {
		id := createdByID
		ownerID = &id
	}

	// All-or-nothing: a batch that trips the cap or a duplicate on its last
	// entry must leave nothing behind. The cap and the duplicate checks run
	// inside the transaction so a concurrent create cannot slip past them.
	err := s.txManager.WithTransaction(context.Background(), func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		txRepo := s.repo.WithTx(tx)

		active, err := txRepo.CountPrompts(true)
		if err != nil {
			return err
		}
		if active+int64(len(normalized)) > aeoMaxActivePrompts {
			return ErrAEOPromptLimit
		}

		for _, text := range normalized {
			exists, err := txRepo.ExistsByTextInsensitive(text, 0)
			if err != nil {
				return err
			}
			if exists {
				return fmt.Errorf("prompt %q already exists: %w", text, apperrors.ErrDuplicatePrompt)
			}

			prompt := models.AEOPrompt{Text: text, IsActive: true, CreatedByID: ownerID}
			if err := txRepo.CreatePrompt(&prompt); err != nil {
				return err
			}
			created = append(created, prompt)
		}
		return nil
	})
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	logger.WithField("count", len(created)).Info("AEO prompts created")
	return created, nil
}

func (s *aeoService) UpdatePrompt(id uint, text *string, isActive *bool) (*models.AEOPrompt, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("aeo_prompt_id", id), "AEOService", "UpdatePrompt")

	prompt, err := s.repo.GetPromptByID(id)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("aeo prompt %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	if text != nil {
		newText, err := normalizeAEOPromptText(*text)
		if err != nil {
			logger.WithError(utils.LogError(err)).Warn("Invalid AEO prompt text")
			return nil, err
		}
		// The prompt being edited is allowed to collide with itself: toggling
		// is_active while leaving the text alone is not a duplicate.
		exists, err := s.repo.ExistsByTextInsensitive(newText, id)
		if err != nil {
			utils.LogServiceResponse(logger, err)
			return nil, err
		}
		if exists {
			return nil, fmt.Errorf("prompt %q already exists: %w", newText, apperrors.ErrDuplicatePrompt)
		}
		prompt.Text = newText
	}

	if isActive != nil {
		// Reactivating counts against the cap; deactivating never can.
		if *isActive && !prompt.IsActive {
			active, err := s.repo.CountPrompts(true)
			if err != nil {
				utils.LogServiceResponse(logger, err)
				return nil, err
			}
			if active >= aeoMaxActivePrompts {
				return nil, ErrAEOPromptLimit
			}
		}
		prompt.IsActive = *isActive
	}

	if err := s.repo.UpdatePrompt(prompt); err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("aeo prompt %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	logger.Info("AEO prompt updated")
	return prompt, nil
}

func (s *aeoService) DeletePrompt(id uint) error {
	logger := utils.LogServiceCall(utils.Logger.WithField("aeo_prompt_id", id), "AEOService", "DeletePrompt")

	if _, err := s.repo.GetPromptByID(id); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("aeo prompt %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return err
	}

	if err := s.repo.DeletePrompt(id); err != nil {
		if isNotFound(err) {
			return fmt.Errorf("aeo prompt %d not found: %w", id, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return err
	}

	logger.Info("AEO prompt deleted")
	return nil
}

func (s *aeoService) GetPromptAnswers(promptID uint, runID *uint, offset, limit int) ([]models.AEOAnswer, int64, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("aeo_prompt_id", promptID), "AEOService", "GetPromptAnswers")

	// An unknown prompt is a 404, not an empty transcript.
	if _, err := s.repo.GetPromptByID(promptID); err != nil {
		if isNotFound(err) {
			return nil, 0, fmt.Errorf("aeo prompt %d not found: %w", promptID, apperrors.ErrNotFound)
		}
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}

	answers, err := s.repo.ListAnswersByPrompt(promptID, runID, offset, limit)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}
	if answers == nil {
		answers = []models.AEOAnswer{}
	}

	total, err := s.repo.CountAnswersByPrompt(promptID, runID)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, 0, err
	}
	return answers, total, nil
}
