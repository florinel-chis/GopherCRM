package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/idkit"
	"github.com/florinel-chis/gophercrm/internal/idkit/scim"
	"github.com/florinel-chis/gophercrm/internal/idkit/scim/server"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
)

// SCIMIssuerSource supplies the single sign-on issuer whose identities give
// a SCIM user its externalId. The SSO settings service satisfies it.
type SCIMIssuerSource interface {
	SSOConfig(ctx context.Context) (EffectiveSSOConfig, error)
}

// SCIMUserRecord is one managed account with its externalId ("" when none).
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
	// writes is the create, replace and patch half (#470, #471); nil without
	// WithSCIMUserWrites.
	writes *scimUserWrites
}

// SCIMUserServiceOption configures NewSCIMUserService.
type SCIMUserServiceOption func(*scimUserService)

// NewSCIMUserService builds the SCIM Users read (#469) over the repository
// and the issuer source. The create, replace and patch (#470, #471) need
// WithSCIMUserWrites; without it they answer a wiring error.
func NewSCIMUserService(repo repository.SCIMUserRepository, issuers SCIMIssuerSource, opts ...SCIMUserServiceOption) SCIMUserService {
	s := &scimUserService{repo: repo, issuers: issuers}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// readIssuer returns the effective single sign-on issuer; SCIM manages only
// the accounts linked under it (repository.SCIMUserRepository).
func (s *scimUserService) readIssuer(ctx context.Context) (string, error) {
	sso, err := s.issuers.SSOConfig(ctx)
	if err != nil {
		return "", fmt.Errorf("read the single sign-on issuer: %w", err)
	}
	return sso.IssuerURL, nil
}

// Get returns the managed account rawID names. An id that is not a positive
// decimal integer fitting a uint, or names no managed account (none, erased,
// a customer, or an account not linked under the configured issuer), is
// apperrors.ErrNotFound.
func (s *scimUserService) Get(ctx context.Context, rawID string) (*SCIMUserRecord, error) {
	id, err := strconv.ParseUint(rawID, 10, strconv.IntSize)
	if err != nil || id == 0 {
		return nil, fmt.Errorf("scim user not found: %w", apperrors.ErrNotFound)
	}
	issuer, err := s.readIssuer(ctx)
	if err != nil {
		return nil, err
	}
	user, err := s.repo.GetManagedByID(issuer, uint(id))
	if err != nil {
		if apperrors.IsNotFound(err) {
			return nil, fmt.Errorf("scim user %d not found: %w", id, apperrors.ErrNotFound)
		}
		return nil, fmt.Errorf("read scim user: %w", err)
	}
	records, err := s.withExternalIDs(issuer, []models.User{*user})
	if err != nil {
		return nil, err
	}
	return &records[0], nil
}

// List returns one page of managed accounts, id ascending; the issuer is read
// once. With userName nil it pages every managed account in SQL. Otherwise it fetches the folded
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
	issuer, err := s.readIssuer(ctx)
	if err != nil {
		return nil, err
	}

	var (
		users []models.User
		total int
	)
	if userName == nil {
		page, n, err := s.repo.ListManaged(issuer, offset, count)
		if err != nil {
			return nil, fmt.Errorf("list scim users: %w", err)
		}
		users, total = page, int(n)
	} else {
		candidates, err := s.repo.FindManagedByEmailFolded(issuer, *userName)
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

	records, err := s.withExternalIDs(issuer, users)
	if err != nil {
		return nil, err
	}
	return &SCIMUserPage{Total: total, StartIndex: startIndex, Users: records}, nil
}

// withExternalIDs pairs users with their externalId: server.ExternalID over
// the user's user_identities rows in ascending id order, so the subject of
// the lowest-id row whose issuer equals issuer byte for byte.
// The caller passes the issuer it already read, so one request judges and
// pairs under the same value; when it is empty no identity is read and every
// externalId is omitted. The returned slice is never nil.
func (s *scimUserService) withExternalIDs(issuer string, users []models.User) ([]SCIMUserRecord, error) {
	records := make([]SCIMUserRecord, len(users))
	for i := range users {
		records[i].User = users[i]
	}
	if len(users) == 0 {
		return records, nil
	}
	if issuer == "" {
		return records, nil
	}
	ids := make([]uint, len(users))
	for i := range users {
		ids[i] = users[i].ID
	}
	identities, err := s.repo.IdentitiesForUsers(issuer, ids)
	if err != nil {
		return nil, fmt.Errorf("read scim user identities: %w", err)
	}
	// The column compares under utf8mb4_bin PAD SPACE on MySQL/MariaDB, so
	// an issuer with a trailing space comes back too: the package judge
	// decides, over each user's rows in ascending id order.
	perUser := make(map[uint][]models.UserIdentity, len(users))
	for _, identity := range identities {
		perUser[identity.UserID] = append(perUser[identity.UserID], identity)
	}
	subjects := make(map[uint]string, len(perUser))
	for userID, rows := range perUser {
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		subjects[userID] = server.ExternalID(issuer, idkitIdentities(rows))
	}
	for i := range records {
		records[i].ExternalID = subjects[records[i].User.ID]
	}
	return records, nil
}

// idkitIdentities converts user_identities rows, in the given order, to the
// identities the SCIM server's managed-accounts judges take.
func idkitIdentities(rows []models.UserIdentity) []idkit.Identity {
	identities := make([]idkit.Identity, len(rows))
	for i, row := range rows {
		identities[i] = idkit.Identity{
			AccountID: strconv.FormatUint(uint64(row.UserID), 10),
			Issuer:    row.Issuer,
			Subject:   row.Subject,
			Source:    row.Source,
		}
	}
	return identities
}
