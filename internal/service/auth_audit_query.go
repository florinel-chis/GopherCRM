package service

import (
	"context"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
)

// The read side of the sign-in audit log (#455). The filter values are
// matched exactly and case-sensitively against closed sets before any SQL
// runs, so MySQL and MariaDB (case-insensitive collation) and SQLite answer
// the same: "PASSWORD" or "password " is refused on every engine. The sets
// are built in auth_audit_vocabulary.go.

// AuthAuditLogService reads the sign-in audit log. It is separate from the
// writer AuthAuditService. The handler validates the filter first.
type AuthAuditLogService interface {
	// ListSignIns returns one page of rows, newest first, and the number of
	// rows matching filter.
	ListSignIns(ctx context.Context, filter repository.AuthAuditFilter, offset, limit int) ([]models.AuthAuditLog, int64, error)
}

type authAuditLogService struct {
	repo repository.AuthAuditRepository
}

// NewAuthAuditLogService returns the audit reader over repo.
func NewAuthAuditLogService(repo repository.AuthAuditRepository) AuthAuditLogService {
	return &authAuditLogService{repo: repo}
}

func (s *authAuditLogService) ListSignIns(_ context.Context, filter repository.AuthAuditFilter, offset, limit int) ([]models.AuthAuditLog, int64, error) {
	return s.repo.List(filter, offset, limit)
}
