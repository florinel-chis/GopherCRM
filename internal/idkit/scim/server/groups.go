package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/florinel-chis/gophercrm/internal/idkit"
	"github.com/florinel-chis/gophercrm/internal/idkit/scim"
)

// groupResource builds the Group of data; it omits members when the
// request's excludedAttributes names them.
func (s *Server) groupResource(r *http.Request, data GroupData) scim.Group {
	group := scim.NewGroup(s.baseURL, data.Value, data.MemberIDs)
	if excluded, present := query(r, "excludedAttributes"); present && scim.ExcludesMembers(excluded) {
		group = group.WithoutMembers()
	}
	return group
}

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	page, ok := paging(w, r)
	if !ok {
		return
	}
	var displayName *string
	if raw, present := query(r, "filter"); present {
		value, err := scim.ParseGroupFilter(raw)
		if err != nil {
			scim.WriteErrorType(w, http.StatusBadRequest, scim.ScimTypeInvalidFilter, groupInvalidFilterDetail)
			return
		}
		displayName = &value
	}
	result, err := s.groups.List(r.Context(), displayName, page.StartIndex, page.Count)
	if err != nil {
		s.failGroup(w, r, err)
		return
	}
	resources := make([]scim.Group, 0, len(result.Groups))
	for _, data := range result.Groups {
		resources = append(resources, s.groupResource(r, data))
	}
	s.writeGroup(w, r, http.StatusOK, scim.NewGroupList(result.Total, result.StartIndex, resources))
}

func (s *Server) getGroup(w http.ResponseWriter, r *http.Request, id string) {
	data, err := s.groups.Get(r.Context(), id)
	if err != nil {
		s.failGroup(w, r, err)
		return
	}
	s.writeGroup(w, r, http.StatusOK, s.groupResource(r, data))
}

// createGroup answers 201 with the existing group the body's externalId or,
// failing that, displayName names, and its Location.
func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	raw, ok := readObject(w, r)
	if !ok {
		scim.WriteErrorType(w, http.StatusBadRequest, scim.ScimTypeInvalidSyntax, groupCreateSyntaxDetail)
		return
	}
	body, err := scim.ParseGroupCreate(raw)
	if err != nil {
		var createErr *scim.GroupCreateError
		if !errors.As(err, &createErr) {
			s.failGroup(w, r, err)
			return
		}
		switch {
		case createErr.ScimType == scim.ScimTypeInvalidSyntax:
			scim.WriteErrorType(w, http.StatusBadRequest, scim.ScimTypeInvalidSyntax, groupCreateSyntaxDetail)
		case createErr.Members:
			scim.WriteErrorType(w, http.StatusBadRequest, scim.ScimTypeInvalidValue, groupCreateMembersDetail)
		default:
			scim.WriteErrorType(w, http.StatusBadRequest, scim.ScimTypeInvalidValue, groupCreateValueDetail)
		}
		return
	}
	data, err := s.groups.Create(r.Context(), body.ExternalID, body.DisplayName)
	if err != nil {
		if errors.Is(err, ErrInvalidValue) {
			scim.WriteErrorType(w, http.StatusBadRequest, scim.ScimTypeInvalidValue, groupCreateValueDetail)
			return
		}
		s.failGroup(w, r, err)
		return
	}
	group := s.groupResource(r, data)
	encoded, err := json.Marshal(group)
	if err != nil {
		s.failGroup(w, r, err)
		return
	}
	w.Header().Set("Location", group.Meta.Location)
	writeBody(w, http.StatusCreated, encoded)
}

// patchGroup answers 204 without a body.
func (s *Server) patchGroup(w http.ResponseWriter, r *http.Request, id string) {
	raw, ok := readObject(w, r)
	if !ok {
		scim.WriteErrorType(w, http.StatusBadRequest, scim.ScimTypeInvalidSyntax, invalidPatchDetail)
		return
	}
	patch, err := scim.ParseGroupPatch(raw)
	if err != nil {
		var patchErr *scim.PatchError
		if !errors.As(err, &patchErr) {
			s.failGroup(w, r, err)
			return
		}
		scim.WriteErrorType(w, http.StatusBadRequest, patchErr.ScimType, patchDetail(patchErr.ScimType, true))
		return
	}
	if err := s.groups.Patch(r.Context(), id, patch); err != nil {
		// The last-admin refusal first: it answers mutability even when it
		// is also an invalid value.
		switch {
		case errors.Is(err, ErrLastAdmin):
			scim.WriteErrorType(w, http.StatusBadRequest, scim.ScimTypeMutability, groupLastAdminDetail)
		case errors.Is(err, ErrInvalidValue):
			scim.WriteErrorType(w, http.StatusBadRequest, scim.ScimTypeInvalidValue, groupInvalidValueDetail)
		default:
			s.failGroup(w, r, err)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// groupMethodNotAllowed answers DELETE with 405 and Allow: GET, PATCH; the
// body is not read.
func (s *Server) groupMethodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", groupResourceAllow)
	scim.WriteError(w, http.StatusMethodNotAllowed, groupMethodNotAllowedDetail)
}

func (s *Server) writeGroup(w http.ResponseWriter, r *http.Request, status int, document any) {
	body, err := json.Marshal(document)
	if err != nil {
		s.failGroup(w, r, err)
		return
	}
	writeBody(w, status, body)
}

// failGroup maps ErrNotFound to 404 and anything else to a logged 500.
func (s *Server) failGroup(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrNotFound) {
		scim.WriteError(w, http.StatusNotFound, groupNotFoundDetail)
		return
	}
	s.logger.Error(r.Context(), groupFailedLogMessage, err, idkit.Field{Key: "component", Value: groupLogComponent})
	scim.WriteError(w, http.StatusInternalServerError, internalErrorDetail)
}
