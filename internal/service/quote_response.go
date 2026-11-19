package service

import (
	"fmt"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// The client response link (#382). The email of a sent quote carries a
// single-use token; these two methods are what the unauthenticated routes
// call with it. Every refusal is apperrors.ErrQuoteResponseRejected, whatever
// its cause, and the logs carry the quote id only: never the token, its hash
// or the link.

// rejectQuoteResponse wraps the one public refusal. reason says which case
// it was for the log and carries ids and statuses only.
func rejectQuoteResponse(reason string) error {
	return fmt.Errorf("%s: %w", reason, apperrors.ErrQuoteResponseRejected)
}

// findUsableResponseToken is the plain read both methods start with: the
// token is hashed and looked up with the predicates of the claim. A service
// without a token store, an empty token and a miss are all the rejection.
func (s *quoteService) findUsableResponseToken(token string) (*models.QuoteResponseToken, error) {
	if s.tokenRepo == nil {
		return nil, rejectQuoteResponse("response links are not configured")
	}
	if token == "" {
		return nil, rejectQuoteResponse("no token")
	}
	row, err := s.tokenRepo.FindUsableByHash(hashOpaqueToken(token, s.tokenSecret), s.stamp())
	if err != nil {
		if isNotFound(err) {
			return nil, rejectQuoteResponse("no usable token")
		}
		return nil, err
	}
	return row, nil
}

// publicQuoteView loads the quote with its lines for the public view. A
// quote deleted since the token was read is the rejection.
func (s *quoteService) publicQuoteView(quoteID uint) (*models.QuotePublicView, error) {
	quote, err := s.quoteRepo.GetByID(quoteID)
	if err != nil {
		if isNotFound(err) {
			return nil, rejectQuoteResponse(fmt.Sprintf("quote %d is gone", quoteID))
		}
		return nil, err
	}
	return models.NewQuotePublicView(quote), nil
}

func (s *quoteService) GetPublicResponse(token string) (*models.QuotePublicView, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "quote"), "QuoteService", "GetPublicResponse")

	row, err := s.findUsableResponseToken(token)
	if err != nil {
		logServiceOutcome(logger, err, "Quote response link refused")
		return nil, err
	}
	logger = logger.WithField("quote_id", row.QuoteID)
	view, err := s.publicQuoteView(row.QuoteID)
	if err != nil {
		logServiceOutcome(logger, err, "Quote response link refused")
		return nil, err
	}
	return view, nil
}

// RespondPublic is the client's answer. After the plain read of the token,
// one transaction in the order quotes → quote_response_tokens: the locking
// read of the quote, the conditional claim of the token (unused, unexpired,
// quote live and sent, all in the statement), the conditional claim of the
// status (WHERE status = 'sent'), and the history row. Of two calls with one
// token the second blocks on the quote lock on MySQL (on SQLite the single
// connection serialises them) and then claims nothing. A failure after the
// claim rolls the claim back, so the link stays usable. The deal is not
// touched. The history row carries no user (changed_by_id NULL) and source
// client, so the history shows the answer as the client's (#400); the token
// row (used_at, decision) records which link was spent.
func (s *quoteService) RespondPublic(token, decision string) (*models.QuotePublicView, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "quote"), "QuoteService", "RespondPublic")

	var to models.QuoteStatus
	switch decision {
	case models.QuoteResponseAccept:
		to = models.QuoteStatusAccepted
	case models.QuoteResponseDecline:
		to = models.QuoteStatusRejected
	default:
		err := fmt.Errorf("decision must be %s or %s: %w", models.QuoteResponseAccept, models.QuoteResponseDecline, apperrors.ErrValidation)
		logger.WithError(utils.LogError(err)).Warn("Quote response refused")
		return nil, err
	}

	row, err := s.findUsableResponseToken(token)
	if err != nil {
		logServiceOutcome(logger, err, "Quote response refused")
		return nil, err
	}
	quoteID := row.QuoteID
	logger = logger.WithFields(map[string]interface{}{"quote_id": quoteID, "status": to})

	err = s.inQuoteTokenTransaction(func(repo repository.QuoteRepository, tokens repository.QuoteResponseTokenRepository) error {
		quote, err := repo.GetByIDForUpdate(quoteID)
		if err != nil {
			if isNotFound(err) {
				return rejectQuoteResponse(fmt.Sprintf("quote %d is gone", quoteID))
			}
			return err
		}
		now := s.stamp()
		if err := tokens.Claim(row.ID, decision, now); err != nil {
			if isNotFound(err) {
				return rejectQuoteResponse(fmt.Sprintf("the token of quote %d is no longer usable", quoteID))
			}
			return err
		}
		rows, err := repo.ClaimStatus(quoteID, models.QuoteStatusSent, to,
			repository.QuoteStatusStamps{SentAt: quote.SentAt, ClosedAt: &now})
		if err != nil {
			return err
		}
		if rows != 1 {
			return rejectQuoteResponse(fmt.Sprintf("quote %d is no longer sent", quoteID))
		}
		from := models.QuoteStatusSent
		return repo.CreateStatusChange(&models.QuoteStatusChange{
			QuoteID:     quoteID,
			FromStatus:  &from,
			ToStatus:    to,
			ChangedByID: nil,
			Source:      models.QuoteChangeSourceClient,
			ChangedAt:   now,
		})
	})
	if err != nil {
		logServiceOutcome(logger, err, "Quote response refused")
		return nil, err
	}

	logger.Info("Quote answered through the client response link")
	return s.publicQuoteView(quoteID)
}
