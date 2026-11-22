package server

import "errors"

// Errors a Users or Groups implementation returns, classified with
// errors.Is. Any other error answers 500 and is logged.
var (
	// ErrNotFound is an id that names no resource the provider serves (404).
	ErrNotFound = errors.New("scim server: not found")
	// ErrInvalidValue is an attribute that is missing or invalid (400
	// invalidValue).
	ErrInvalidValue = errors.New("scim server: invalid value")
	// ErrUniqueness is a userName or externalId already in use (409
	// uniqueness).
	ErrUniqueness = errors.New("scim server: uniqueness")
	// ErrLastAdmin is a write that would leave no active administrator: 400
	// invalidValue on a user write, 400 mutability on a group patch. It is
	// checked before ErrInvalidValue.
	ErrLastAdmin = errors.New("scim server: the last active admin must stay an active admin")
)

// Details of the answers. None echoes the request.
const (
	userNotFoundDetail     = "User not found"
	invalidFilterDetail    = `Only the filter userName eq "value" is supported`
	invalidPagingDetail    = "startIndex and count must be integers"
	internalErrorDetail    = "Internal server error"
	invalidSyntaxDetail    = "The request body is not a valid SCIM User"
	invalidValueDetail     = "A User attribute is missing or invalid"
	lastAdminDetail        = "The last active admin must stay an active admin"
	uniquenessDetail       = "The userName or externalId is already in use"
	invalidPatchDetail     = "The request body is not a valid SCIM PatchOp"
	invalidPathDetail      = "The patch names an attribute this provider does not support"
	noTargetDetail         = "A remove operation needs a path"
	resourceNotFoundDetail = "Resource not found"

	groupNotFoundDetail         = "Group not found"
	groupInvalidFilterDetail    = `Only the filter displayName eq "value" is supported`
	groupInvalidValueDetail     = "A Group member is not a user this provider serves, or the role it maps to is invalid"
	groupLastAdminDetail        = "The last active admin must stay an active admin"
	groupInvalidPathDetail      = "Only the members of a Group can be changed"
	groupMethodNotAllowedDetail = "Groups come from the single sign-on role mapping and cannot be deleted through SCIM"
	groupResourceAllow          = "GET, PATCH"
	groupCreateSyntaxDetail     = "The request body is not a valid SCIM Group"
	groupCreateMembersDetail    = "Add the members of a Group with PATCH"
	groupCreateValueDetail      = "A Group can be created only for a groups value of the single sign-on role mapping"
)

// Log messages and the component field of a 500.
const (
	userFailedLogMessage  = "SCIM user request failed"
	groupFailedLogMessage = "SCIM group request failed"
	userLogComponent      = "scim_users"
	groupLogComponent     = "scim_groups"
)
