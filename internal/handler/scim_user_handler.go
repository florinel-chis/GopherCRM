package handler

import (
	"encoding/json"
	"errors"
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
)

// SCIMUserHandler serves the read of the SCIM 2.0 Users resource (#469):
// GET /scim/v2/Users/:id and GET /scim/v2/Users with startIndex/count paging
// and the one filter userName eq "<value>". attributes and
// excludedAttributes are ignored. Not part of the OpenAPI document: /scim/v2
// lies outside the API base path.
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

func (h *SCIMUserHandler) write(c *gin.Context, document any) {
	body, err := json.Marshal(document)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.Data(http.StatusOK, scim.ContentType, body)
}

// fail maps a not-found to 404 and anything else to 500, logged without the
// request's values.
func (h *SCIMUserHandler) fail(c *gin.Context, err error) {
	if errors.Is(err, apperrors.ErrNotFound) {
		scim.WriteError(c, http.StatusNotFound, scimUserNotFoundDetail)
		return
	}
	scimUserLogger(c).WithError(utils.LogError(err)).Error("SCIM user read failed")
	scim.WriteError(c, http.StatusInternalServerError, scimUserInternalErrorDetail)
}
