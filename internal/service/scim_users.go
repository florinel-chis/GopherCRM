package service

import (
	"context"
	"fmt"
	"strconv"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/scim"
)

// SCIMIssuerSource supplies the single sign-on issuer whose identities give
// a SCIM user its externalId. The SSO settings service satisfies it.
type SCIMIssuerSource interface {
	SSOConfig(ctx context.Context) (EffectiveSSOConfig, error)
}

// SCIMUserRecord is one visible user with its externalId ("" when none).
type SCIMUserRecord struct {
	User       models.User
	ExternalID string
}

// SCIMUserPage is one page of a SCIM user list. Total counts every match,
// StartIndex is the effective 1-based start index, Users is never nil.
type SCIMUserPage struct {
	Total      int
	StartIndex int
	Users      []SCIMUserRecord
}

type scimUserService struct {
	repo    repository.SCIMUserRepository
	issuers SCIMIssuerSource
	// writes is the create and replace half (#470); nil without
	// WithSCIMUserWrites.
	writes *scimUserWrites
}

// SCIMUserServiceOption configures NewSCIMUserService.
type SCIMUserServiceOption func(*scimUserService)

// NewSCIMUserService builds the SCIM Users read (#469) over the repository
// and the issuer source. The create and replace (#470) need
// WithSCIMUserWrites; without it they answer a wiring error.
func NewSCIMUserService(repo repository.SCIMUserRepository, issuers SCIMIssuerSource, opts ...SCIMUserServiceOption) SCIMUserService {
	s := &scimUserService{repo: repo, issuers: issuers}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Get returns the visible user rawID names. An id that is not a positive
// decimal integer fitting a uint, or names no visible user (none, erased or
// a customer), is apperrors.ErrNotFound.
func (s *scimUserService) Get(ctx context.Context, rawID string) (*SCIMUserRecord, error) {
	id, err := strconv.ParseUint(rawID, 10, strconv.IntSize)
	if err != nil || id == 0 {
		return nil, fmt.Errorf("scim user not found: %w", apperrors.ErrNotFound)
	}
	user, err := s.repo.GetVisibleByID(uint(id))
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, fmt.Errorf("scim user %d not found: %w", id, apperrors.ErrNotFound)
		}
		return nil, fmt.Errorf("read scim user: %w", err)
	}
	records, err := s.withExternalIDs(ctx, []models.User{*user})
	if err != nil {
		return nil, err
	}
	return &records[0], nil
}

// List returns one page of visible users, id ascending. With userName nil it
// pages every visible user in SQL. Otherwise it fetches the folded
// candidates, keeps only those scim.UserNameMatches judges equal, and pages
// that set in Go. startIndex is 1-based; count is 0..scim.MaxResults.
func (s *scimUserService) List(ctx context.Context, userName *string, startIndex, count int) (*SCIMUserPage, error) {
	if startIndex < 1 {
		startIndex = 1
	}
	if count < 0 {
		count = 0
	}
	if count > scim.MaxResults {
		count = scim.MaxResults
	}
	offset := startIndex - 1

	var (
		users []models.User
		total int
	)
	if userName == nil {
		page, n, err := s.repo.ListVisible(offset, count)
		if err != nil {
			return nil, fmt.Errorf("list scim users: %w", err)
		}
		users, total = page, int(n)
	} else {
		candidates, err := s.repo.FindVisibleByEmailFolded(*userName)
		if err != nil {
			return nil, fmt.Errorf("find scim users: %w", err)
		}
		judged := make([]models.User, 0, len(candidates))
		for _, candidate := range candidates {
			if scim.UserNameMatches(candidate.Email, *userName) {
				judged = append(judged, candidate)
			}
		}
		total = len(judged)
		if offset < len(judged) {
			end := len(judged)
			if count < end-offset {
				end = offset + count
			}
			users = judged[offset:end]
		}
	}

	records, err := s.withExternalIDs(ctx, users)
	if err != nil {
		return nil, err
	}
	return &SCIMUserPage{Total: total, StartIndex: startIndex, Users: records}, nil
}

// withExternalIDs pairs users with their externalId: the subject of the
// lowest-id user_identities row whose issuer equals the configured issuer
// byte for byte. The configured issuer is read once; when it is empty no
// identity is read and every externalId is omitted. The returned slice is
// never nil.
func (s *scimUserService) withExternalIDs(ctx context.Context, users []models.User) ([]SCIMUserRecord, error) {
	records := make([]SCIMUserRecord, len(users))
	for i := range users {
		records[i].User = users[i]
	}
	if len(users) == 0 {
		return records, nil
	}
	sso, err := s.issuers.SSOConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the single sign-on issuer: %w", err)
	}
	if sso.IssuerURL == "" {
		return records, nil
	}
	ids := make([]uint, len(users))
	for i := range users {
		ids[i] = users[i].ID
	}
	identities, err := s.repo.IdentitiesForUsers(sso.IssuerURL, ids)
	if err != nil {
		return nil, fmt.Errorf("read scim user identities: %w", err)
	}
	subjects := make(map[uint]string, len(identities))
	lowest := make(map[uint]uint, len(identities))
	for _, identity := range identities {
		// The column compares under utf8mb4_bin PAD SPACE on MySQL/MariaDB,
		// so an issuer with a trailing space comes back too: judge it here.
		if identity.Issuer != sso.IssuerURL {
			continue
		}
		if seen, ok := lowest[identity.UserID]; ok && seen < identity.ID {
			continue
		}
		lowest[identity.UserID] = identity.ID
		subjects[identity.UserID] = identity.Subject
	}
	for i := range records {
		records[i].ExternalID = subjects[records[i].User.ID]
	}
	return records, nil
}
