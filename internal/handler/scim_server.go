package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/idkit"
	"github.com/florinel-chis/gophercrm/internal/idkit/scim"
	"github.com/florinel-chis/gophercrm/internal/idkit/scim/server"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
)

// NewSCIMServer builds the SCIM 2.0 provider (package
// internal/idkit/scim/server) over the SCIM user and group services, with
// meta.location under publicBaseURL. SetupSCIMRoutes mounts it behind the
// SCIM token authentication. Not part of the OpenAPI document: /scim/v2 lies
// outside the API base path.
func NewSCIMServer(users service.SCIMUserService, groups service.SCIMGroupService, publicBaseURL string) (http.Handler, error) {
	if users == nil || groups == nil {
		return nil, errors.New("scim server needs the user and the group service")
	}
	return server.New(server.Options{
		BaseURL: publicBaseURL,
		Users:   scimUsers{users: users},
		Groups:  scimGroups{groups: groups},
		Logger:  scimLogger{},
	})
}

// scimUserData is the SCIM view of the account u, with externalID ("" omits
// it in the resource).
func scimUserData(u *models.User, externalID string) scim.UserData {
	return scim.UserData{
		ID:           strconv.FormatUint(uint64(u.ID), 10),
		ExternalID:   externalID,
		Email:        u.Email,
		GivenName:    u.FirstName,
		FamilyName:   u.LastName,
		Active:       u.IsActive,
		Created:      u.CreatedAt,
		LastModified: u.UpdatedAt,
	}
}

// scimTranslated is a service error that also matches a server sentinel. Its
// message is the service error's, so the log line does not change.
type scimTranslated struct {
	sentinel error
	err      error
}

func (e *scimTranslated) Error() string   { return e.err.Error() }
func (e *scimTranslated) Unwrap() []error { return []error{e.sentinel, e.err} }

// scimTranslate maps the service refusals to the server sentinels, the
// last-admin refusal first; lastAdmin is the one sentinel of that side's
// service. A not-found is apperrors.ErrNotFound under errors.Is, so a raw
// driver not-found stays a 500.
func scimTranslate(err, lastAdmin error) error {
	var sentinel error
	switch {
	case err == nil:
		return nil
	case errors.Is(err, lastAdmin):
		sentinel = server.ErrLastAdmin
	case errors.Is(err, service.ErrSCIMInvalidValue):
		sentinel = server.ErrInvalidValue
	case errors.Is(err, service.ErrSCIMUniqueness):
		sentinel = server.ErrUniqueness
	case errors.Is(err, apperrors.ErrNotFound):
		sentinel = server.ErrNotFound
	default:
		return err
	}
	return &scimTranslated{sentinel: sentinel, err: err}
}

// errSCIMNoRecord is a service that answered neither a record nor an error.
var errSCIMNoRecord = errors.New("scim service returned no record")

// scimUsers adapts service.SCIMUserService to server.Users.
type scimUsers struct {
	users service.SCIMUserService
}

func (a scimUsers) record(record *service.SCIMUserRecord, err error) (scim.UserData, error) {
	if err != nil {
		return scim.UserData{}, scimTranslate(err, service.ErrSCIMLastAdmin)
	}
	if record == nil {
		return scim.UserData{}, errSCIMNoRecord
	}
	return scimUserData(&record.User, record.ExternalID), nil
}

func (a scimUsers) Get(ctx context.Context, id string) (scim.UserData, error) {
	return a.record(a.users.Get(ctx, id))
}

func (a scimUsers) List(ctx context.Context, userName *string, startIndex, count int) (server.UserPage, error) {
	page, err := a.users.List(ctx, userName, startIndex, count)
	if err != nil {
		return server.UserPage{}, scimTranslate(err, service.ErrSCIMLastAdmin)
	}
	if page == nil {
		return server.UserPage{}, errSCIMNoRecord
	}
	users := make([]scim.UserData, len(page.Users))
	for i := range page.Users {
		users[i] = scimUserData(&page.Users[i].User, page.Users[i].ExternalID)
	}
	return server.UserPage{Total: page.Total, StartIndex: page.StartIndex, Users: users}, nil
}

func (a scimUsers) Create(ctx context.Context, in server.UserInput) (scim.UserData, error) {
	return a.record(a.users.Create(ctx, service.SCIMUserInput(in)))
}

func (a scimUsers) Replace(ctx context.Context, id string, in server.UserInput) (scim.UserData, error) {
	return a.record(a.users.Replace(ctx, id, service.SCIMUserInput(in)))
}

func (a scimUsers) Patch(ctx context.Context, id string, p scim.UserPatch) (scim.UserData, error) {
	return a.record(a.users.Patch(ctx, id, p))
}

func (a scimUsers) Delete(ctx context.Context, id string) error {
	return scimTranslate(a.users.Delete(ctx, id), service.ErrSCIMLastAdmin)
}

// scimGroups adapts service.SCIMGroupService to server.Groups; member ids are
// decimal.
type scimGroups struct {
	groups service.SCIMGroupService
}

func scimGroupData(record *service.SCIMGroupRecord) server.GroupData {
	ids := make([]string, len(record.MemberIDs))
	for i, id := range record.MemberIDs {
		ids[i] = strconv.FormatUint(uint64(id), 10)
	}
	return server.GroupData{Value: record.Value, MemberIDs: ids}
}

func (a scimGroups) record(record *service.SCIMGroupRecord, err error) (server.GroupData, error) {
	if err != nil {
		return server.GroupData{}, scimTranslate(err, service.ErrSCIMGroupLastAdmin)
	}
	if record == nil {
		return server.GroupData{}, errSCIMNoRecord
	}
	return scimGroupData(record), nil
}

func (a scimGroups) List(ctx context.Context, displayName *string, startIndex, count int) (server.GroupPage, error) {
	page, err := a.groups.List(ctx, displayName, startIndex, count)
	if err != nil {
		return server.GroupPage{}, scimTranslate(err, service.ErrSCIMGroupLastAdmin)
	}
	if page == nil {
		return server.GroupPage{}, errSCIMNoRecord
	}
	groups := make([]server.GroupData, len(page.Groups))
	for i := range page.Groups {
		groups[i] = scimGroupData(&page.Groups[i])
	}
	return server.GroupPage{Total: page.Total, StartIndex: page.StartIndex, Groups: groups}, nil
}

func (a scimGroups) Get(ctx context.Context, id string) (server.GroupData, error) {
	return a.record(a.groups.Get(ctx, id))
}

func (a scimGroups) Create(ctx context.Context, externalID, displayName *string) (server.GroupData, error) {
	return a.record(a.groups.Create(ctx, externalID, displayName))
}

func (a scimGroups) Patch(ctx context.Context, id string, p scim.GroupPatch) error {
	_, err := a.groups.Patch(ctx, id, p)
	return scimTranslate(err, service.ErrSCIMGroupLastAdmin)
}

// scimLogger writes the server's failures at error level through
// utils.Logger with the request id of ctx and the redacted error.
type scimLogger struct{}

func (scimLogger) Error(ctx context.Context, msg string, err error, fields ...idkit.Field) {
	base := utils.Logger
	if base == nil {
		base = logrus.StandardLogger()
	}
	data := logrus.Fields{"request_id": utils.RequestIDFromContext(ctx)}
	for _, field := range fields {
		data[field.Key] = field.Value
	}
	base.WithFields(data).WithError(utils.LogError(err)).Error(msg)
}
