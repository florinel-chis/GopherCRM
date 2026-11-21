package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/scim"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// Details of the SCIM Groups answers. None echoes the request.
const (
	scimGroupNotFoundDetail          = "Group not found"
	scimGroupInvalidFilterDetail     = `Only the filter displayName eq "value" is supported`
	scimGroupInvalidValueDetail      = "A Group member is not a user this provider serves, or the role it maps to is invalid"
	scimGroupLastAdminDetail         = "The last active admin must stay an active admin"
	scimGroupInvalidPathDetail       = "Only the members of a Group can be changed"
	scimGroupMethodNotAllowedDetail  = "Groups come from the single sign-on role mapping and cannot be deleted through SCIM"
	scimGroupResourceAllow           = "GET, PATCH"
	scimGroupCreateSyntaxDetail      = "The request body is not a valid SCIM Group"
	scimGroupCreateMembersDetail     = "Add the members of a Group with PATCH"
	scimGroupCreateValueDetail       = "A Group can be created only for a groups value of the single sign-on role mapping"
	scimGroupInternalErrorLogMessage = "SCIM group request failed"
)

// SCIMGroupHandler serves the SCIM 2.0 Groups resource (#506): GET
// /scim/v2/Groups with startIndex/count paging and the one filter
// displayName eq "<value>", GET /scim/v2/Groups/:id and PATCH
// /scim/v2/Groups/:id, which changes the members and recomputes their roles.
// Both reads honour excludedAttributes=members. The groups come from the
// single sign-on role mapping, so POST /scim/v2/Groups (#630) only resolves
// a mapping value to its existing group and DELETE answers 405. Not part of the OpenAPI document: /scim/v2 lies outside the API
// base path.
type SCIMGroupHandler struct {
	groups        service.SCIMGroupService
	publicBaseURL string
}

// NewSCIMGroupHandler builds the handler; meta.location lies under
// publicBaseURL.
func NewSCIMGroupHandler(groups service.SCIMGroupService, publicBaseURL string) *SCIMGroupHandler {
	return &SCIMGroupHandler{groups: groups, publicBaseURL: publicBaseURL}
}

func scimGroupLogger(c *gin.Context) *logrus.Entry {
	base := utils.Logger
	if base == nil {
		base = logrus.StandardLogger()
	}
	return base.WithFields(logrus.Fields{"component": "scim_groups", "request_id": c.GetString("request_id")})
}

// resource builds the Group of record; it omits members when the request's
// excludedAttributes names them.
func (h *SCIMGroupHandler) resource(c *gin.Context, record *service.SCIMGroupRecord) scim.Group {
	ids := make([]string, len(record.MemberIDs))
	for i, id := range record.MemberIDs {
		ids[i] = strconv.FormatUint(uint64(id), 10)
	}
	group := scim.NewGroup(h.publicBaseURL, record.Value, ids)
	if excluded, present := c.GetQuery("excludedAttributes"); present && scim.ExcludesMembers(excluded) {
		group = group.WithoutMembers()
	}
	return group
}

// List answers GET /scim/v2/Groups.
func (h *SCIMGroupHandler) List(c *gin.Context) {
	startIndex, startPresent := c.GetQuery("startIndex")
	count, countPresent := c.GetQuery("count")
	page, err := scim.ParsePaging(startIndex, startPresent, count, countPresent)
	if err != nil {
		scim.WriteErrorType(c, http.StatusBadRequest, scim.ScimTypeInvalidValue, scimInvalidPagingDetail)
		return
	}
	var displayName *string
	if raw, present := c.GetQuery("filter"); present {
		value, err := scim.ParseGroupFilter(raw)
		if err != nil {
			scim.WriteErrorType(c, http.StatusBadRequest, scim.ScimTypeInvalidFilter, scimGroupInvalidFilterDetail)
			return
		}
		displayName = &value
	}
	result, err := h.groups.List(c.Request.Context(), displayName, page.StartIndex, page.Count)
	if err != nil {
		h.fail(c, err)
		return
	}
	resources := make([]scim.Group, 0, len(result.Groups))
	for i := range result.Groups {
		resources = append(resources, h.resource(c, &result.Groups[i]))
	}
	h.write(c, http.StatusOK, scim.NewGroupList(result.Total, result.StartIndex, resources))
}

// Get answers GET /scim/v2/Groups/:id.
func (h *SCIMGroupHandler) Get(c *gin.Context) {
	record, err := h.groups.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	h.write(c, http.StatusOK, h.resource(c, record))
}

// Create answers POST /scim/v2/Groups (#630): 201 with the existing group
// whose value the body's externalId or, failing that, displayName equals,
// and its Location. It writes nothing, so a repeat gives the same answer.
func (h *SCIMGroupHandler) Create(c *gin.Context) {
	raw, ok := readSCIMObject(c)
	if !ok {
		scim.WriteErrorType(c, http.StatusBadRequest, scim.ScimTypeInvalidSyntax, scimGroupCreateSyntaxDetail)
		return
	}
	body, err := scim.ParseGroupCreate(raw)
	if err != nil {
		var createErr *scim.GroupCreateError
		if !errors.As(err, &createErr) {
			h.fail(c, err)
			return
		}
		switch {
		case createErr.ScimType == scim.ScimTypeInvalidSyntax:
			scim.WriteErrorType(c, http.StatusBadRequest, scim.ScimTypeInvalidSyntax, scimGroupCreateSyntaxDetail)
		case createErr.Members:
			scim.WriteErrorType(c, http.StatusBadRequest, scim.ScimTypeInvalidValue, scimGroupCreateMembersDetail)
		default:
			scim.WriteErrorType(c, http.StatusBadRequest, scim.ScimTypeInvalidValue, scimGroupCreateValueDetail)
		}
		return
	}
	record, err := h.groups.Create(c.Request.Context(), body.ExternalID, body.DisplayName)
	if err != nil {
		if errors.Is(err, service.ErrSCIMInvalidValue) {
			scim.WriteErrorType(c, http.StatusBadRequest, scim.ScimTypeInvalidValue, scimGroupCreateValueDetail)
			return
		}
		h.fail(c, err)
		return
	}
	group := h.resource(c, record)
	c.Header("Location", group.Meta.Location)
	h.write(c, http.StatusCreated, group)
}

// Patch answers PATCH /scim/v2/Groups/:id: 204 without a body.
func (h *SCIMGroupHandler) Patch(c *gin.Context) {
	raw, ok := readSCIMObject(c)
	if !ok {
		scim.WriteErrorType(c, http.StatusBadRequest, scim.ScimTypeInvalidSyntax, scimInvalidPatchDetail)
		return
	}
	patch, err := scim.ParseGroupPatch(raw)
	if err != nil {
		var patchErr *scim.PatchError
		if !errors.As(err, &patchErr) {
			h.fail(c, err)
			return
		}
		detail := scimInvalidPatchDetail
		switch patchErr.ScimType {
		case scim.ScimTypeInvalidPath:
			detail = scimGroupInvalidPathDetail
		case scim.ScimTypeNoTarget:
			detail = scimNoTargetDetail
		case scim.ScimTypeInvalidValue:
			detail = scimGroupInvalidValueDetail
		}
		scim.WriteErrorType(c, http.StatusBadRequest, patchErr.ScimType, detail)
		return
	}
	_, err = h.groups.Patch(scimWriteContext(c), c.Param("id"), patch)
	if err != nil {
		// The last-admin refusal first: it must answer mutability even if
		// it were ever also an invalidValue.
		switch {
		case errors.Is(err, service.ErrSCIMGroupLastAdmin):
			scim.WriteErrorType(c, http.StatusBadRequest, scim.ScimTypeMutability, scimGroupLastAdminDetail)
		case errors.Is(err, service.ErrSCIMInvalidValue):
			scim.WriteErrorType(c, http.StatusBadRequest, scim.ScimTypeInvalidValue, scimGroupInvalidValueDetail)
		default:
			h.fail(c, err)
		}
		return
	}
	c.Status(http.StatusNoContent)
}

// MethodNotAllowed answers DELETE /scim/v2/Groups/:id with a SCIM 405 and
// Allow: GET, PATCH: the groups are the role mapping's values. The request
// body is not read.
func (h *SCIMGroupHandler) MethodNotAllowed(c *gin.Context) {
	c.Header("Allow", scimGroupResourceAllow)
	scim.WriteError(c, http.StatusMethodNotAllowed, scimGroupMethodNotAllowedDetail)
}

func (h *SCIMGroupHandler) write(c *gin.Context, status int, document any) {
	body, err := json.Marshal(document)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.Data(status, scim.ContentType, body)
}

// fail maps a not-found to 404 and anything else to 500, logged without the
// request's values.
func (h *SCIMGroupHandler) fail(c *gin.Context, err error) {
	if errors.Is(err, apperrors.ErrNotFound) {
		scim.WriteError(c, http.StatusNotFound, scimGroupNotFoundDetail)
		return
	}
	scimGroupLogger(c).WithError(utils.LogError(err)).Error(scimGroupInternalErrorLogMessage)
	scim.WriteError(c, http.StatusInternalServerError, scimUserInternalErrorDetail)
}
