package handler

import (
	"encoding/json"
	"net/http"

	"github.com/florinel-chis/gophercrm/internal/scim"
	"github.com/gin-gonic/gin"
)

// SCIMHandler serves the SCIM 2.0 discovery documents (#468). They are built
// once from PUBLIC_BASE_URL; query parameters are ignored. Not part of the
// OpenAPI document: /scim/v2 lies outside the API base path.
type SCIMHandler struct {
	serviceProviderConfig []byte
	schemas               []byte
	resourceTypes         []byte
}

// NewSCIMHandler builds the three documents with meta.location under
// publicBaseURL.
func NewSCIMHandler(publicBaseURL string) *SCIMHandler {
	return &SCIMHandler{
		serviceProviderConfig: mustMarshalSCIM(scim.NewServiceProviderConfig(publicBaseURL)),
		schemas:               mustMarshalSCIM(scim.NewSchemas(publicBaseURL)),
		resourceTypes:         mustMarshalSCIM(scim.NewResourceTypes(publicBaseURL)),
	}
}

func mustMarshalSCIM(document any) []byte {
	body, err := json.Marshal(document)
	if err != nil {
		panic("scim: discovery document does not marshal: " + err.Error())
	}
	return body
}

// ServiceProviderConfig answers GET /scim/v2/ServiceProviderConfig.
func (h *SCIMHandler) ServiceProviderConfig(c *gin.Context) {
	c.Data(http.StatusOK, scim.ContentType, h.serviceProviderConfig)
}

// Schemas answers GET /scim/v2/Schemas.
func (h *SCIMHandler) Schemas(c *gin.Context) {
	c.Data(http.StatusOK, scim.ContentType, h.schemas)
}

// ResourceTypes answers GET /scim/v2/ResourceTypes.
func (h *SCIMHandler) ResourceTypes(c *gin.Context) {
	c.Data(http.StatusOK, scim.ContentType, h.resourceTypes)
}
