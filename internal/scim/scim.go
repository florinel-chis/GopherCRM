// Package scim holds the wire shapes of the SCIM 2.0 provider (#468): the
// application/scim+json content type, the RFC 7644 §3.12 error body and the
// three discovery documents of RFC 7643 (ServiceProviderConfig, Schemas and
// ResourceTypes), and, for the Users read of #469, the User resource, the
// userName filter grammar, the ASCII-only userName judge and the paging
// rules, and, for the Users patch of #471, the PatchOp parser. It has no
// state and no database access.
package scim

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// ContentType is the media type of every SCIM answer (RFC 7644 §3.1).
const ContentType = "application/scim+json"

// URNs of the messages, schemas and resource types this provider serves.
const (
	ErrorSchema                 = "urn:ietf:params:scim:api:messages:2.0:Error"
	ListResponseSchema          = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	ServiceProviderConfigSchema = "urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"
	ResourceTypeSchema          = "urn:ietf:params:scim:schemas:core:2.0:ResourceType"
	SchemaSchema                = "urn:ietf:params:scim:schemas:core:2.0:Schema"
	UserSchema                  = "urn:ietf:params:scim:schemas:core:2.0:User"
)

// BasePath is where the provider is mounted, outside the API prefix.
const BasePath = "/scim/v2"

// MaxResults is the most resources one list returns.
const MaxResults = 100

// errorBody is the RFC 7644 §3.12 error response. // status is the HTTP status code as a string; scimType is set only by
// WriteErrorType.
type errorBody struct {
	Schemas  []string `json:"schemas"`
	ScimType string   `json:"scimType,omitempty"`
	Status   string   `json:"status"`
	Detail   string   `json:"detail"`
}

// WriteError answers status with the SCIM error body and the SCIM content
// type. It does not abort the chain; the caller does.
func WriteError(c *gin.Context, status int, detail string) {
	body, err := json.Marshal(errorBody{Schemas: []string{ErrorSchema}, Status: strconv.Itoa(status), Detail: detail})
	if err != nil {
		// A struct of strings always marshals.
		body = []byte(`{"schemas":["` + ErrorSchema + `"],"status":"` + strconv.Itoa(status) + `"}`)
	}
	c.Data(status, ContentType, body)
}

// SCIM error types of RFC 7644 §3.12 this provider answers with.
const (
	ScimTypeInvalidFilter = "invalidFilter"
	ScimTypeInvalidValue  = "invalidValue"
	// ScimTypeUniqueness and ScimTypeInvalidSyntax answer the Users create
	// and replace (#470).
	ScimTypeUniqueness    = "uniqueness"
	ScimTypeInvalidSyntax = "invalidSyntax"
	// ScimTypeInvalidPath and ScimTypeNoTarget answer the Users patch
	// (#471).
	ScimTypeInvalidPath = "invalidPath"
	ScimTypeNoTarget    = "noTarget"
)

// WriteErrorType answers status with the SCIM error body carrying scimType,
// and the SCIM content type. It does not abort the chain; the caller does.
func WriteErrorType(c *gin.Context, status int, scimType, detail string) {
	body, err := json.Marshal(errorBody{Schemas: []string{ErrorSchema}, ScimType: scimType, Status: strconv.Itoa(status), Detail: detail})
	if err != nil {
		// A struct of strings always marshals.
		body = []byte(`{"schemas":["` + ErrorSchema + `"],"status":"` + strconv.Itoa(status) + `"}`)
	}
	c.Data(status, ContentType, body)
}

// Meta is the meta attribute of a discovery document.
type Meta struct {
	ResourceType string `json:"resourceType"`
	Location     string `json:"location"`
}

// Supported is a feature that is only switched on or off.
type Supported struct {
	Supported bool `json:"supported"`
}

// Bulk is the bulk feature of the ServiceProviderConfig.
type Bulk struct {
	Supported      bool `json:"supported"`
	MaxOperations  int  `json:"maxOperations"`
	MaxPayloadSize int  `json:"maxPayloadSize"`
}

// Filter is the filter feature of the ServiceProviderConfig.
type Filter struct {
	Supported  bool `json:"supported"`
	MaxResults int  `json:"maxResults"`
}

// AuthenticationScheme is one entry of authenticationSchemes.
type AuthenticationScheme struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Primary     bool   `json:"primary"`
}

// ServiceProviderConfig is the RFC 7643 §5 document.
type ServiceProviderConfig struct {
	Schemas               []string               `json:"schemas"`
	Patch                 Supported              `json:"patch"`
	Bulk                  Bulk                   `json:"bulk"`
	Filter                Filter                 `json:"filter"`
	ChangePassword        Supported              `json:"changePassword"`
	Sort                  Supported              `json:"sort"`
	ETag                  Supported              `json:"etag"`
	AuthenticationSchemes []AuthenticationScheme `json:"authenticationSchemes"`
	Meta                  Meta                   `json:"meta"`
}

// Attribute is one attribute definition of a schema (RFC 7643 §7). CaseExact
// is set on string attributes only; SubAttributes on complex ones only.
type Attribute struct {
	Name            string      `json:"name"`
	Type            string      `json:"type"`
	MultiValued     bool        `json:"multiValued"`
	Description     string      `json:"description"`
	Required        bool        `json:"required"`
	CaseExact       *bool       `json:"caseExact,omitempty"`
	CanonicalValues []string    `json:"canonicalValues,omitempty"`
	Mutability      string      `json:"mutability"`
	Returned        string      `json:"returned"`
	Uniqueness      string      `json:"uniqueness"`
	SubAttributes   []Attribute `json:"subAttributes,omitempty"`
}

// Schema is one schema resource (RFC 7643 §7).
type Schema struct {
	Schemas     []string    `json:"schemas"`
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Attributes  []Attribute `json:"attributes"`
	Meta        Meta        `json:"meta"`
}

// ResourceType is one resource type (RFC 7643 §6).
type ResourceType struct {
	Schemas     []string `json:"schemas"`
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Endpoint    string   `json:"endpoint"`
	Schema      string   `json:"schema"`
	Meta        Meta     `json:"meta"`
}

// ListResponse is the RFC 7644 §3.4.2 list envelope.
type ListResponse[T any] struct {
	Schemas      []string `json:"schemas"`
	TotalResults int      `json:"totalResults"`
	ItemsPerPage int      `json:"itemsPerPage"`
	StartIndex   int      `json:"startIndex"`
	Resources    []T      `json:"Resources"`
}

func newListResponse[T any](items ...T) ListResponse[T] {
	return ListResponse[T]{
		Schemas:      []string{ListResponseSchema},
		TotalResults: len(items),
		ItemsPerPage: len(items),
		StartIndex:   1,
		Resources:    items,
	}
}

// location is baseURL (PUBLIC_BASE_URL, trailing slashes dropped) +
// /scim/v2 + path.
func location(baseURL, path string) string {
	return strings.TrimRight(baseURL, "/") + BasePath + path
}

// NewServiceProviderConfig is the ServiceProviderConfig served at
// baseURL/scim/v2/ServiceProviderConfig.
func NewServiceProviderConfig(baseURL string) ServiceProviderConfig {
	return ServiceProviderConfig{
		Schemas:        []string{ServiceProviderConfigSchema},
		Patch:          Supported{Supported: true},
		Bulk:           Bulk{Supported: false, MaxOperations: 0, MaxPayloadSize: 0},
		Filter:         Filter{Supported: true, MaxResults: MaxResults},
		ChangePassword: Supported{Supported: false},
		Sort:           Supported{Supported: false},
		ETag:           Supported{Supported: false},
		AuthenticationSchemes: []AuthenticationScheme{{
			Type:        "oauthbearertoken",
			Name:        "OAuth Bearer Token",
			Description: "A SCIM provider token issued by a GopherCRM administrator, sent as Authorization: Bearer <token>.",
			Primary:     true,
		}},
		Meta: Meta{ResourceType: "ServiceProviderConfig", Location: location(baseURL, "/ServiceProviderConfig")},
	}
}

func boolPtr(v bool) *bool { return &v }

// userAttributes are the core User attributes this provider understands.
func userAttributes() []Attribute {
	return []Attribute{
		{
			Name: "userName", Type: "string", Description: "Unique identifier for the user, the sign-in name. Matched case-insensitively.",
			Required: true, CaseExact: boolPtr(false), Mutability: "readWrite", Returned: "default", Uniqueness: "server",
		},
		{
			Name: "name", Type: "complex", Description: "The components of the user's name.",
			Mutability: "readWrite", Returned: "default", Uniqueness: "none",
			SubAttributes: []Attribute{
				{
					Name: "givenName", Type: "string", Description: "The given (first) name of the user.",
					CaseExact: boolPtr(false), Mutability: "readWrite", Returned: "default", Uniqueness: "none",
				},
				{
					Name: "familyName", Type: "string", Description: "The family (last) name of the user.",
					CaseExact: boolPtr(false), Mutability: "readWrite", Returned: "default", Uniqueness: "none",
				},
			},
		},
		{
			Name: "emails", Type: "complex", MultiValued: true, Description: "Email addresses of the user.",
			Mutability: "readWrite", Returned: "default", Uniqueness: "none",
			SubAttributes: []Attribute{
				{
					Name: "value", Type: "string", Description: "The email address.",
					CaseExact: boolPtr(false), Mutability: "readWrite", Returned: "default", Uniqueness: "none",
				},
				{
					Name: "type", Type: "string", Description: "The kind of address: work, home or other.",
					CaseExact: boolPtr(false), CanonicalValues: []string{"work", "home", "other"},
					Mutability: "readWrite", Returned: "default", Uniqueness: "none",
				},
				{
					Name: "primary", Type: "boolean", Description: "Whether this is the primary address.",
					Mutability: "readWrite", Returned: "default", Uniqueness: "none",
				},
			},
		},
		{
			Name: "active", Type: "boolean", Description: "Whether the user may sign in.",
			Mutability: "readWrite", Returned: "default", Uniqueness: "none",
		},
	}
}

// NewSchemas is the /Schemas list: the core User schema.
func NewSchemas(baseURL string) ListResponse[Schema] {
	return newListResponse(Schema{
		Schemas:     []string{SchemaSchema},
		ID:          UserSchema,
		Name:        "User",
		Description: "User account",
		Attributes:  userAttributes(),
		Meta:        Meta{ResourceType: "Schema", Location: location(baseURL, "/Schemas/"+UserSchema)},
	})
}

// NewResourceTypes is the /ResourceTypes list: the User resource type.
func NewResourceTypes(baseURL string) ListResponse[ResourceType] {
	return newListResponse(ResourceType{
		Schemas:     []string{ResourceTypeSchema},
		ID:          "User",
		Name:        "User",
		Description: "User account",
		Endpoint:    "/Users",
		Schema:      UserSchema,
		Meta:        Meta{ResourceType: "ResourceType", Location: location(baseURL, "/ResourceTypes/User")},
	})
}
