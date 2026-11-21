package service

import (
	"context"
	"fmt"
	"sort"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/scim"
)

// SCIM 2.0 Groups (#506). A group is a distinct value of the effective
// single sign-on role mapping's groups-claim entries, read on every call and
// ordered by bytes. A POST only resolves an existing value (#630); a group
// cannot be renamed or deleted through SCIM. Its id is scim.GroupID(value). Membership is stored in scim_group_members;
// a group serves only its managed members, id ascending: live, not customer,
// and linked under the effective issuer (repository.SCIMUserRepository).
// The patch half is in scim_group_writes.go.

// SCIMGroupRecord is one group: its mapping value and its managed members'
// user ids, ascending (never nil).
type SCIMGroupRecord struct {
	Value     string
	MemberIDs []uint
}

// SCIMGroupPage is one page of a SCIM group list. Total counts every match,
// StartIndex is the effective 1-based start index, Groups is never nil.
type SCIMGroupPage struct {
	Total      int
	StartIndex int
	Groups     []SCIMGroupRecord
}

type scimGroupService struct {
	members repository.SCIMGroupMemberRepository
	sso     SCIMIssuerSource
	// writes is the patch half; nil without WithSCIMGroupWrites.
	writes *scimGroupWrites
}

// SCIMGroupServiceOption configures NewSCIMGroupService.
type SCIMGroupServiceOption func(*scimGroupService)

// NewSCIMGroupService builds the SCIM Groups read over the membership
// repository and the single sign-on settings. The patch needs
// WithSCIMGroupWrites; without it, it answers a wiring error.
func NewSCIMGroupService(members repository.SCIMGroupMemberRepository, sso SCIMIssuerSource, opts ...SCIMGroupServiceOption) SCIMGroupService {
	s := &scimGroupService{members: members, sso: sso}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// scimGroupValues returns the distinct groups-claim values of the mapping,
// ascending by bytes.
func scimGroupValues(cfg EffectiveSSOConfig) []string {
	seen := map[string]bool{}
	values := []string{}
	for _, entry := range cfg.RoleMapping {
		if entry.Claim != SSOClaimGroups || seen[entry.Value] {
			continue
		}
		seen[entry.Value] = true
		values = append(values, entry.Value)
	}
	sort.Strings(values)
	return values
}

// scimGroupNotFound is the not-found of a group; it never names the value.
func scimGroupNotFound() error {
	return fmt.Errorf("scim group not found: %w", apperrors.ErrNotFound)
}

// resolveGroup returns the current group value rawID names: an id that does
// not decode, or whose value is not a current group, is apperrors.ErrNotFound.
func resolveGroup(cfg EffectiveSSOConfig, rawID string) (string, error) {
	value, err := scim.ParseGroupID(rawID)
	if err != nil {
		return "", scimGroupNotFound()
	}
	for _, group := range scimGroupValues(cfg) {
		if group == value {
			return value, nil
		}
	}
	return "", scimGroupNotFound()
}

// withMembers pairs the values with their members managed under issuer in
// one query.
func (s *scimGroupService) withMembers(issuer string, values []string) ([]SCIMGroupRecord, error) {
	records := make([]SCIMGroupRecord, len(values))
	if len(values) == 0 {
		return records, nil
	}
	members, err := s.members.ListManagedMembers(issuer, values)
	if err != nil {
		return nil, fmt.Errorf("read scim group members: %w", err)
	}
	for i, value := range values {
		ids := members[value]
		if ids == nil {
			ids = []uint{}
		}
		records[i] = SCIMGroupRecord{Value: value, MemberIDs: ids}
	}
	return records, nil
}

// List returns one page of groups, ascending by value. With displayName set,
// only the group whose value equals it byte for byte. startIndex is 1-based;
// count is 0..scim.MaxResults.
func (s *scimGroupService) List(ctx context.Context, displayName *string, startIndex, count int) (*SCIMGroupPage, error) {
	if startIndex < 1 {
		startIndex = 1
	}
	if count < 0 {
		count = 0
	}
	if count > scim.MaxResults {
		count = scim.MaxResults
	}
	cfg, err := s.sso.SSOConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the single sign-on settings: %w", err)
	}
	values := scimGroupValues(cfg)
	if displayName != nil {
		matched := []string{}
		for _, value := range values {
			if value == *displayName {
				matched = append(matched, value)
			}
		}
		values = matched
	}
	total := len(values)
	offset := startIndex - 1
	page := []string{}
	if offset < total {
		end := total
		if count < end-offset {
			end = offset + count
		}
		page = values[offset:end]
	}
	records, err := s.withMembers(cfg.IssuerURL, page)
	if err != nil {
		return nil, err
	}
	return &SCIMGroupPage{Total: total, StartIndex: startIndex, Groups: records}, nil
}

// Get returns the group rawID names with its managed members.
func (s *scimGroupService) Get(ctx context.Context, rawID string) (*SCIMGroupRecord, error) {
	cfg, err := s.sso.SSOConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the single sign-on settings: %w", err)
	}
	value, err := resolveGroup(cfg, rawID)
	if err != nil {
		return nil, err
	}
	records, err := s.withMembers(cfg.IssuerURL, []string{value})
	if err != nil {
		return nil, err
	}
	return &records[0], nil
}

// Create resolves a Group create (#630) to an existing group: the value
// externalID equals byte for byte, otherwise the one displayName equals; no
// match is ErrSCIMInvalidValue. It writes nothing, so a repeat answers the
// same group.
func (s *scimGroupService) Create(ctx context.Context, externalID, displayName *string) (*SCIMGroupRecord, error) {
	cfg, err := s.sso.SSOConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the single sign-on settings: %w", err)
	}
	values := scimGroupValues(cfg)
	value, found := "", false
	for _, candidate := range []*string{externalID, displayName} {
		if candidate == nil {
			continue
		}
		for _, group := range values {
			if group == *candidate {
				value, found = group, true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("the group is not a groups value of the role mapping: %w", ErrSCIMInvalidValue)
	}
	records, err := s.withMembers(cfg.IssuerURL, []string{value})
	if err != nil {
		return nil, err
	}
	return &records[0], nil
}
