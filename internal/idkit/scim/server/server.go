package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/florinel-chis/gophercrm/internal/idkit"
	"github.com/florinel-chis/gophercrm/internal/idkit/scim"
)

// UserInput is what a SCIM User create or replace body carries. Absent
// strings are "". Active is nil when absent. Roles holds the value of every
// roles entry; nil when absent.
type UserInput struct {
	UserName   string
	ExternalID string
	GivenName  string
	FamilyName string
	Active     *bool
	Roles      []string
}

// UserPage is one page of a Users list. Total counts every match, StartIndex
// is the effective 1-based start index.
type UserPage struct {
	Total      int
	StartIndex int
	Users      []scim.UserData
}

// Users is the host's Users resource. It serves managed accounts only: List
// returns only managed accounts, and an id that names no managed account
// (none, or one Manages refuses) is ErrNotFound for Get, Replace, Patch and
// Delete, which the server answers with the same 404 as an unknown id.
// Writes return ErrLastAdmin, ErrInvalidValue, ErrUniqueness or ErrNotFound
// for a refusal; any other error is a failure of the host.
type Users interface {
	// Get returns the account id names.
	Get(ctx context.Context, id string) (scim.UserData, error)
	// List returns one page, startIndex 1-based and count 0..scim.MaxResults.
	// userName nil lists every managed account; otherwise only those whose
	// userName it matches.
	List(ctx context.Context, userName *string, startIndex, count int) (UserPage, error)
	// Create provisions an account from in.
	Create(ctx context.Context, in UserInput) (scim.UserData, error)
	// Replace overwrites the account id names with in.
	Replace(ctx context.Context, id string, in UserInput) (scim.UserData, error)
	// Patch applies a parsed PatchOp to the account id names.
	Patch(ctx context.Context, id string, p scim.UserPatch) (scim.UserData, error)
	// Delete deactivates the account id names; a repeat is nil.
	Delete(ctx context.Context, id string) error
}

// GroupData is what a Group resource is built from: the group's value
// (displayName) and its members' User ids.
type GroupData struct {
	Value     string
	MemberIDs []string
}

// GroupPage is one page of a Groups list, as UserPage.
type GroupPage struct {
	Total      int
	StartIndex int
	Groups     []GroupData
}

// Groups is the host's Groups resource. An id that names no group is
// ErrNotFound. Patch returns ErrLastAdmin, ErrInvalidValue or ErrNotFound for
// a refusal and Create ErrInvalidValue; any other error is a failure of the
// host.
type Groups interface {
	// List returns one page; displayName nil lists every group, otherwise
	// only the one whose value equals it.
	List(ctx context.Context, displayName *string, startIndex, count int) (GroupPage, error)
	// Get returns the group id names.
	Get(ctx context.Context, id string) (GroupData, error)
	// Create resolves a Group create to an existing group by externalID or,
	// failing that, displayName.
	Create(ctx context.Context, externalID, displayName *string) (GroupData, error)
	// Patch changes the members of the group id names.
	Patch(ctx context.Context, id string, p scim.GroupPatch) error
}

// Logger records a failure the server answers with 500. err is the host's
// own error, passed as the host returned it; the host redacts it before it
// writes it. The server passes only fixed messages and fields (the
// component), never a request value.
type Logger interface {
	Error(ctx context.Context, msg string, err error, fields ...idkit.Field)
}

type nopLogger struct{}

func (nopLogger) Error(context.Context, string, error, ...idkit.Field) {}

// Options configure New. Users and Groups are required; a nil Logger drops
// the log lines.
type Options struct {
	// BaseURL is the public origin resource locations lie under.
	BaseURL string
	Users   Users
	Groups  Groups
	Logger  Logger
}

// Server is the SCIM 2.0 provider; it is an http.Handler.
type Server struct {
	baseURL string
	users   Users
	groups  Groups
	logger  Logger

	serviceProviderConfig []byte
	schemas               []byte
	resourceTypes         []byte
}

// New builds the server and its three discovery documents.
func New(opts Options) (*Server, error) {
	if opts.Users == nil {
		return nil, errors.New("scim server: Users is required")
	}
	if opts.Groups == nil {
		return nil, errors.New("scim server: Groups is required")
	}
	s := &Server{baseURL: opts.BaseURL, users: opts.Users, groups: opts.Groups, logger: opts.Logger}
	if s.logger == nil {
		s.logger = nopLogger{}
	}
	for _, doc := range []struct {
		target   *[]byte
		document any
	}{
		{&s.serviceProviderConfig, scim.NewServiceProviderConfig(opts.BaseURL)},
		{&s.schemas, scim.NewSchemas(opts.BaseURL)},
		{&s.resourceTypes, scim.NewResourceTypes(opts.BaseURL)},
	} {
		body, err := json.Marshal(doc.document)
		if err != nil {
			return nil, fmt.Errorf("scim server: discovery document does not marshal: %w", err)
		}
		*doc.target = body
	}
	return s, nil
}

// Route is one method and path the server answers. Path is relative to
// scim.BasePath; {id} stands for one path segment.
type Route struct {
	Method string
	Path   string
}

// Routes lists every route the server answers. Anything else answers the
// SCIM 404.
func Routes() []Route {
	return []Route{
		{http.MethodGet, "/ServiceProviderConfig"},
		{http.MethodGet, "/Schemas"},
		{http.MethodGet, "/ResourceTypes"},
		{http.MethodGet, "/Users"},
		{http.MethodGet, "/Users/{id}"},
		{http.MethodPost, "/Users"},
		{http.MethodPut, "/Users/{id}"},
		{http.MethodPatch, "/Users/{id}"},
		{http.MethodDelete, "/Users/{id}"},
		{http.MethodGet, "/Groups"},
		{http.MethodGet, "/Groups/{id}"},
		{http.MethodPatch, "/Groups/{id}"},
		{http.MethodPost, "/Groups"},
		{http.MethodDelete, "/Groups/{id}"},
	}
}

// ServeHTTP routes on r.Method and the decoded r.URL.Path under
// scim.BasePath. The path is used as sent, without cleaning.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, scim.BasePath)
	if !ok {
		s.notFound(w)
		return
	}
	switch rest {
	case "/ServiceProviderConfig":
		s.discovery(w, r, s.serviceProviderConfig)
		return
	case "/Schemas":
		s.discovery(w, r, s.schemas)
		return
	case "/ResourceTypes":
		s.discovery(w, r, s.resourceTypes)
		return
	case "/Users":
		switch r.Method {
		case http.MethodGet:
			s.listUsers(w, r)
			return
		case http.MethodPost:
			s.createUser(w, r)
			return
		}
	case "/Groups":
		switch r.Method {
		case http.MethodGet:
			s.listGroups(w, r)
			return
		case http.MethodPost:
			s.createGroup(w, r)
			return
		}
	}
	if id, ok := pathID(rest, "/Users/"); ok {
		switch r.Method {
		case http.MethodGet:
			s.getUser(w, r, id)
			return
		case http.MethodPut:
			s.replaceUser(w, r, id)
			return
		case http.MethodPatch:
			s.patchUser(w, r, id)
			return
		case http.MethodDelete:
			s.deleteUser(w, r, id)
			return
		}
	}
	if id, ok := pathID(rest, "/Groups/"); ok {
		switch r.Method {
		case http.MethodGet:
			s.getGroup(w, r, id)
			return
		case http.MethodPatch:
			s.patchGroup(w, r, id)
			return
		case http.MethodDelete:
			s.groupMethodNotAllowed(w)
			return
		}
	}
	s.notFound(w)
}

// pathID returns the single, non-empty path segment after prefix.
func pathID(rest, prefix string) (string, bool) {
	id, ok := strings.CutPrefix(rest, prefix)
	if !ok || id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return id, true
}

func (s *Server) discovery(w http.ResponseWriter, r *http.Request, body []byte) {
	if r.Method != http.MethodGet {
		s.notFound(w)
		return
	}
	writeBody(w, http.StatusOK, body)
}

func (s *Server) notFound(w http.ResponseWriter) {
	scim.WriteError(w, http.StatusNotFound, resourceNotFoundDetail)
}

// query returns the first value of the query parameter key and whether it
// was sent.
func query(r *http.Request, key string) (string, bool) {
	values, ok := r.URL.Query()[key]
	if !ok || len(values) == 0 {
		return "", false
	}
	return values[0], true
}

// paging parses startIndex and count; false means the 400 is written.
func paging(w http.ResponseWriter, r *http.Request) (scim.Paging, bool) {
	startIndex, startPresent := query(r, "startIndex")
	count, countPresent := query(r, "count")
	page, err := scim.ParsePaging(startIndex, startPresent, count, countPresent)
	if err != nil {
		scim.WriteErrorType(w, http.StatusBadRequest, scim.ScimTypeInvalidValue, invalidPagingDetail)
		return scim.Paging{}, false
	}
	return page, true
}

// writeBody sets the SCIM content type, keeping every header set before it,
// then writes status and body.
func writeBody(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", scim.ContentType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// patchDetail is the detail of a refused PatchOp; group selects the Group
// texts.
func patchDetail(scimType string, group bool) string {
	switch scimType {
	case scim.ScimTypeInvalidPath:
		if group {
			return groupInvalidPathDetail
		}
		return invalidPathDetail
	case scim.ScimTypeNoTarget:
		return noTargetDetail
	case scim.ScimTypeInvalidValue:
		if group {
			return groupInvalidValueDetail
		}
		return invalidValueDetail
	}
	return invalidPatchDetail
}
