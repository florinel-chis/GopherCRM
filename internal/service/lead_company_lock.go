package service

import (
	"context"
	"errors"
	"fmt"
	"sort"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// Lead and company lock order (#182).
//
// Lead conversion, lead update, bulk lead status and company delete share one
// lock order: companies (CompanyRepository.LockLive, by primary key) -> leads
// (id ascending) -> customers -> deals. Conversion and update lock the
// companies their attempt involves, in ascending id order, before the lead;
// company delete locks its company before it unlinks leads, customers and
// deals; bulk lead status locks only its leads, in id order, and no company.
//
// The companies a conversion or update must lock depend on the lead's
// company_id, which can only be read before the lead is locked (the lead lock
// comes second). Each attempt therefore reads company_id plainly, locks the
// companies, locks the lead and compares: when the locked row names another
// company (a company delete unlinked it, or another write moved it, in
// between) the attempt rolls back with errLeadCompanyMoved and is restarted,
// at most leadCompanyMaxRestarts times (see shouldRestartLeadCompanyAttempt).

// errLeadCompanyMoved ends an attempt whose locked lead names another company
// than the attempt's plain read. It is never returned to a caller: it is
// either restarted or turned into apperrors.ErrLeadCompanyContended.
var errLeadCompanyMoved = errors.New("the lead's company_id changed before its row lock was taken")

// leadCompanyMaxRestarts bounds the restarts of one conversion or update: 3
// restarts, 4 attempts.
const leadCompanyMaxRestarts = 3

// Operations named in the lock-order log lines and test seams.
const (
	leadCompanyOpConvert = "convert"
	leadCompanyOpUpdate  = "update"
)

// shouldRestartLeadCompanyAttempt reports whether a failed attempt may be
// restarted: only an errLeadCompanyMoved whose transaction rolled back cleanly
// (no ErrRollbackFailed, ErrTransactionPoisoned, ErrCommitFailed or
// ErrBeginFailed, which leave the outcome unknown) on a context that is not
// done. The restart bound is the caller's.
func shouldRestartLeadCompanyAttempt(ctx context.Context, err error) bool {
	if err == nil || !errors.Is(err, errLeadCompanyMoved) {
		return false
	}
	for _, unclean := range []error{utils.ErrRollbackFailed, utils.ErrTransactionPoisoned, utils.ErrCommitFailed, utils.ErrBeginFailed} {
		if errors.Is(err, unclean) {
			return false
		}
	}
	return ctx.Err() == nil
}

// leadCompanyLockHooks are instance-local test seams of the lock-order path,
// nil in production. op is leadCompanyOpConvert or leadCompanyOpUpdate.
type leadCompanyLockHooks struct {
	// attemptStarted runs at the start of every attempt (every transaction
	// callback, a retry of a deadlock included).
	attemptStarted func(op string, leadID uint)
	// attemptEnded runs with the error the attempt's callback returns.
	attemptEnded func(op string, leadID uint, err error)
	// afterPlainRead runs on the attempt's transaction after the plain read of
	// the lead's company_id, before the company locks.
	afterPlainRead func(op string, tx *gorm.DB, leadID uint)
	// beforeCommit runs after the attempt's writes, before its callback
	// returns, while every lock is still held.
	beforeCommit func(op string, leadID uint)
}

// sameCompanyID compares two nullable company ids.
func sameCompanyID(a, b *uint) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func copyCompanyID(id *uint) *uint {
	if id == nil {
		return nil
	}
	v := *id
	return &v
}

// lockLeadCompanies locks the distinct non-nil company ids in ascending id
// order on tx and reports which were live. A missing or soft-deleted company
// is recorded as not live, not returned: the caller decides what that means.
// Any other failure is returned.
func (s *leadService) lockLeadCompanies(logger *logrus.Entry, tx *gorm.DB, op string, leadID uint, ids ...*uint) (map[uint]bool, error) {
	distinct := make([]uint, 0, len(ids))
	seen := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		if id == nil {
			continue
		}
		if _, dup := seen[*id]; dup {
			continue
		}
		seen[*id] = struct{}{}
		distinct = append(distinct, *id)
	}
	sort.Slice(distinct, func(i, j int) bool { return distinct[i] < distinct[j] })

	live := make(map[uint]bool, len(distinct))
	if len(distinct) == 0 {
		return live, nil
	}
	companies := s.companyRepo.WithTx(tx)
	for _, id := range distinct {
		err := companies.LockLive(id)
		switch {
		case err == nil:
			live[id] = true
		case isNotFound(err):
			live[id] = false
			logger.WithFields(logrus.Fields{"lead_id": leadID, "company_id": id, "operation": op}).
				Warn("Company lock found no live company")
		default:
			logger.WithFields(logrus.Fields{"lead_id": leadID, "company_id": id, "operation": op}).
				WithError(utils.LogError(err)).Error("Failed to lock company")
			return nil, err
		}
	}
	return live, nil
}

// checkLockedLeadCompany returns errLeadCompanyMoved when the locked lead names
// another company than the attempt's plain read.
func checkLockedLeadCompany(logger *logrus.Entry, op string, leadID uint, attempt int, plain, locked *uint) error {
	if sameCompanyID(plain, locked) {
		return nil
	}
	fields := logrus.Fields{"lead_id": leadID, "operation": op, "attempt": attempt, "error_code": "lead_company_moved"}
	if plain != nil {
		fields["company_id"] = *plain
	}
	if locked != nil {
		fields["locked_company_id"] = *locked
	}
	logger.WithFields(fields).Info("Lead's company changed before its row lock; the attempt rolls back")
	return fmt.Errorf("lead %d (attempt %d): %w", leadID, attempt, errLeadCompanyMoved)
}

// runLeadCompanyAttempts runs attempt until it succeeds, fails for a reason
// other than a clean errLeadCompanyMoved, or has been restarted
// leadCompanyMaxRestarts times, in which case it returns an error matching
// apperrors.ErrLeadCompanyContended.
func runLeadCompanyAttempts(ctx context.Context, logger *logrus.Entry, op string, leadID uint, attempt func(n int) error) error {
	for n := 1; ; n++ {
		err := attempt(n)
		if err == nil || !shouldRestartLeadCompanyAttempt(ctx, err) {
			return err
		}
		if n > leadCompanyMaxRestarts {
			logger.WithFields(logrus.Fields{"lead_id": leadID, "operation": op, "attempts": n, "error_code": "lead_company_contended"}).
				Warn("Lead's company kept changing; giving up")
			return fmt.Errorf("lead %d: company_id changed in each of %d attempts: %w", leadID, n, apperrors.ErrLeadCompanyContended)
		}
		logger.WithFields(logrus.Fields{"lead_id": leadID, "operation": op, "attempt": n, "restart": n, "error_code": "lead_company_moved"}).
			Info("Restarting after the lead's company changed")
	}
}
