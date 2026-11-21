package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/scim"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// Details of the SCIM Users answers. None echoes the request.
const (
	scimUserNotFoundDetail      = "User not found"
	scimInvalidFilterDetail     = `Only the filter userName eq "value" is supported`
	scimInvalidPagingDetail     = "startIndex and count must be integers"
	scimUserInternalErrorDetail = "Internal server error"
	scimInvalidSyntaxDetail     = "The request body is not a valid SCIM User"
	scimInvalidValueDetail      = "A User attribute is missing or invalid"
	scimLastAdminDetail         = "The last active admin must stay an active admin"
	scimUniquenessDetail        = "The userName or externalId is already in use"
	scimInvalidPatchDetail      = "The request body is not a valid SCIM PatchOp"
	scimInvalidPathDetail       = "The patch names an attribute this provider does not support"
	scimNoTargetDetail          = "A remove operation needs a path"
)

// scimUserBodyMaxBytes bounds a create, replace or patch body.
const scimUserBodyMaxBytes = 64 << 10

// SCIMUserHandler serves the SCIM 2.0 Users resource: the read (#469),
// GET /scim/v2/Users/:id and GET /scim/v2/Users with startIndex/count paging
// and the one filter userName eq "<value>" (attributes and
// excludedAttributes are ignored), the create and replace (#470), POST
// /scim/v2/Users and PUT /scim/v2/Users/:id, the patch (#471), PATCH
// /scim/v2/Users/:id, and the delete (#504), DELETE /scim/v2/Users/:id, which
// deactivates and never erases. Not part of the OpenAPI document: /scim/v2 lies outside
// the API base path.
type SCIMUserHandler struct {
	users         service.SCIMUserService
	publicBaseURL string
}

// NewSCIMUserHandler builds the handler; meta.location lies under
// publicBaseURL.
func NewSCIMUserHandler(users service.SCIMUserService, publicBaseURL string) *SCIMUserHandler {
	return &SCIMUserHandler{users: users, publicBaseURL: publicBaseURL}
}

func scimUserLogger(c *gin.Context) *logrus.Entry {
	base := utils.Logger
	if base == nil {
		base = logrus.StandardLogger()
	}
	return base.WithFields(logrus.Fields{"component": "scim_users", "request_id": c.GetString("request_id")})
}

// Get answers GET /scim/v2/Users/:id.
func (h *SCIMUserHandler) Get(c *gin.Context) {
	record, err := h.users.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	h.write(c, scim.NewUser(h.publicBaseURL, &record.User, record.ExternalID))
}

// List answers GET /scim/v2/Users.
func (h *SCIMUserHandler) List(c *gin.Context) {
	startIndex, startPresent := c.GetQuery("startIndex")
	count, countPresent := c.GetQuery("count")
	page, err := scim.ParsePaging(startIndex, startPresent, count, countPresent)
	if err != nil {
		scim.WriteErrorType(c, http.StatusBadRequest, scim.ScimTypeInvalidValue, scimInvalidPagingDetail)
		return
	}
	var userName *string
	if raw, present := c.GetQuery("filter"); present {
		value, err := scim.ParseUserFilter(raw)
		if err != nil {
			scim.WriteErrorType(c, http.StatusBadRequest, scim.ScimTypeInvalidFilter, scimInvalidFilterDetail)
			return
		}
		userName = &value
	}
	result, err := h.users.List(c.Request.Context(), userName, page.StartIndex, page.Count)
	if err != nil {
		h.fail(c, err)
		return
	}
	resources := make([]scim.User, 0, len(result.Users))
	for i := range result.Users {
		resources = append(resources, scim.NewUser(h.publicBaseURL, &result.Users[i].User, result.Users[i].ExternalID))
	}
	h.write(c, scim.NewUserList(result.Total, result.StartIndex, resources))
}

// Create answers POST /scim/v2/Users: 201 with the new resource and a
// Location header equal to its meta.location.
func (h *SCIMUserHandler) Create(c *gin.Context) {
	in, ok := readSCIMUserBody(c)
	if !ok {
		return
	}
	record, err := h.users.Create(scimWriteContext(c), in)
	if err != nil {
		h.failWrite(c, err)
		return
	}
	resource := scim.NewUser(h.publicBaseURL, &record.User, record.ExternalID)
	c.Header("Location", resource.Meta.Location)
	h.writeStatus(c, http.StatusCreated, resource)
}

// Replace answers PUT /scim/v2/Users/:id: 200 with the replaced resource.
// A changed userName (the login email) also ends every password reset link
// issued for the account before the change.
func (h *SCIMUserHandler) Replace(c *gin.Context) {
	in, ok := readSCIMUserBody(c)
	if !ok {
		return
	}
	record, err := h.users.Replace(scimWriteContext(c), c.Param("id"), in)
	if err != nil {
		h.failWrite(c, err)
		return
	}
	h.write(c, scim.NewUser(h.publicBaseURL, &record.User, record.ExternalID))
}

// Patch answers PATCH /scim/v2/Users/:id: 200 with the patched resource.
// A changed userName (the login email) also ends every password reset link
// issued for the account before the change.
func (h *SCIMUserHandler) Patch(c *gin.Context) {
	raw, ok := readSCIMObject(c)
	if !ok {
		scim.WriteErrorType(c, http.StatusBadRequest, scim.ScimTypeInvalidSyntax, scimInvalidPatchDetail)
		return
	}
	patch, err := scim.ParseUserPatch(raw)
	if err != nil {
		var patchErr *scim.PatchError
		if !errors.As(err, &patchErr) {
			h.fail(c, err)
			return
		}
		detail := scimInvalidPatchDetail
		switch patchErr.ScimType {
		case scim.ScimTypeInvalidPath:
			detail = scimInvalidPathDetail
		case scim.ScimTypeNoTarget:
			detail = scimNoTargetDetail
		case scim.ScimTypeInvalidValue:
			detail = scimInvalidValueDetail
		}
		scim.WriteErrorType(c, http.StatusBadRequest, patchErr.ScimType, detail)
		return
	}
	record, err := h.users.Patch(scimWriteContext(c), c.Param("id"), patch)
	if err != nil {
		h.failWrite(c, err)
		return
	}
	h.write(c, scim.NewUser(h.publicBaseURL, &record.User, record.ExternalID))
}

// Delete answers DELETE /scim/v2/Users/:id (#504): 204 with an empty body. It
// deactivates the account and never erases it; a repeat answers 204 too. The
// request body is not read.
func (h *SCIMUserHandler) Delete(c *gin.Context) {
	if err := h.users.Delete(scimWriteContext(c), c.Param("id")); err != nil {
		h.failWrite(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// scimWriteContext carries the request id and the client facts the sign-in
// audit row is written with, as middleware.SCIMAuth builds them.
func scimWriteContext(c *gin.Context) context.Context {
	return utils.WithClientInfo(
		utils.WithRequestID(c.Request.Context(), c.GetString("request_id")),
		c.ClientIP(), c.Request.UserAgent())
}

// scimUserBody is the part of a SCIM User body a write reads. Every other
// attribute (schemas, emails, password, extensions) is ignored.
type scimUserBody struct {
	UserName   *string `json:"userName"`
	ExternalID *string `json:"externalId"`
	Name       *struct {
		GivenName  *string `json:"givenName"`
		FamilyName *string `json:"familyName"`
	} `json:"name"`
	Active *bool `json:"active"`
	Roles  []struct {
		Value *string `json:"value"`
	} `json:"roles"`
}

// readSCIMObject reads one JSON object of at most scimUserBodyMaxBytes,
// whatever the Content-Type, and reports false for anything else: an
// oversized body, another JSON value or trailing data.
func readSCIMObject(c *gin.Context) (json.RawMessage, bool) {
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, scimUserBodyMaxBytes))
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	if trimmed := bytes.TrimLeft(raw, " \t\r\n"); len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false
	}
	return raw, true
}

// readSCIMUserBody reads the body of a create or replace through
// readSCIMObject. Anything else (what readSCIMObject refuses, a known
// attribute of the wrong type, a roles entry without a string value) answers
// 400 invalidSyntax and reports false.
func readSCIMUserBody(c *gin.Context) (service.SCIMUserInput, bool) {
	var in service.SCIMUserInput
	invalid := func() (service.SCIMUserInput, bool) {
		scim.WriteErrorType(c, http.StatusBadRequest, scim.ScimTypeInvalidSyntax, scimInvalidSyntaxDetail)
		return service.SCIMUserInput{}, false
	}
	raw, ok := readSCIMObject(c)
	if !ok {
		return invalid()
	}
	var body scimUserBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return invalid()
	}
	if body.UserName != nil {
		in.UserName = *body.UserName
	}
	if body.ExternalID != nil {
		in.ExternalID = *body.ExternalID
	}
	if body.Name != nil {
		if body.Name.GivenName != nil {
			in.GivenName = *body.Name.GivenName
		}
		if body.Name.FamilyName != nil {
			in.FamilyName = *body.Name.FamilyName
		}
	}
	in.Active = body.Active
	if body.Roles != nil {
		in.Roles = make([]string, 0, len(body.Roles))
		for _, role := range body.Roles {
			if role.Value == nil {
				return invalid()
			}
			in.Roles = append(in.Roles, *role.Value)
		}
	}
	return in, true
}

func (h *SCIMUserHandler) write(c *gin.Context, document any) {
	h.writeStatus(c, http.StatusOK, document)
}

func (h *SCIMUserHandler) writeStatus(c *gin.Context, status int, document any) {
	body, err := json.Marshal(document)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.Data(status, scim.ContentType, body)
}

// failWrite maps the refusals of a create, replace, patch or delete; anything
// else goes to fail. No answer echoes a request value.
func (h *SCIMUserHandler) failWrite(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrSCIMLastAdmin):
		scim.WriteErrorType(c, http.StatusBadRequest, scim.ScimTypeInvalidValue, scimLastAdminDetail)
	case errors.Is(err, service.ErrSCIMInvalidValue):
		scim.WriteErrorType(c, http.StatusBadRequest, scim.ScimTypeInvalidValue, scimInvalidValueDetail)
	case errors.Is(err, service.ErrSCIMUniqueness):
		scim.WriteErrorType(c, http.StatusConflict, scim.ScimTypeUniqueness, scimUniquenessDetail)
	default:
		h.fail(c, err)
	}
}

// fail maps a not-found to 404 and anything else to 500, logged without the
// request's values.
func (h *SCIMUserHandler) fail(c *gin.Context, err error) {
	if errors.Is(err, apperrors.ErrNotFound) {
		scim.WriteError(c, http.StatusNotFound, scimUserNotFoundDetail)
		return
	}
	scimUserLogger(c).WithError(utils.LogError(err)).Error("SCIM user request failed")
	scim.WriteError(c, http.StatusInternalServerError, scimUserInternalErrorDetail)
}
