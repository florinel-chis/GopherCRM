package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/florinel-chis/gophercrm/internal/idkit"
	"github.com/florinel-chis/gophercrm/internal/idkit/scim"
)

func (s *Server) getUser(w http.ResponseWriter, r *http.Request, id string) {
	data, err := s.users.Get(r.Context(), id)
	if err != nil {
		s.failUser(w, r, err)
		return
	}
	s.writeUser(w, r, http.StatusOK, scim.NewUser(s.baseURL, data))
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	page, ok := paging(w, r)
	if !ok {
		return
	}
	var userName *string
	if raw, present := query(r, "filter"); present {
		value, err := scim.ParseUserFilter(raw)
		if err != nil {
			scim.WriteErrorType(w, http.StatusBadRequest, scim.ScimTypeInvalidFilter, invalidFilterDetail)
			return
		}
		userName = &value
	}
	result, err := s.users.List(r.Context(), userName, page.StartIndex, page.Count)
	if err != nil {
		s.failUser(w, r, err)
		return
	}
	resources := make([]scim.User, 0, len(result.Users))
	for _, data := range result.Users {
		resources = append(resources, scim.NewUser(s.baseURL, data))
	}
	s.writeUser(w, r, http.StatusOK, scim.NewUserList(result.Total, result.StartIndex, resources))
}

// createUser answers 201 with the new resource and a Location equal to its
// meta.location.
func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	in, ok := readUserInput(w, r)
	if !ok {
		scim.WriteErrorType(w, http.StatusBadRequest, scim.ScimTypeInvalidSyntax, invalidSyntaxDetail)
		return
	}
	data, err := s.users.Create(r.Context(), in)
	if err != nil {
		s.failUserWrite(w, r, err)
		return
	}
	resource := scim.NewUser(s.baseURL, data)
	body, err := json.Marshal(resource)
	if err != nil {
		s.failUser(w, r, err)
		return
	}
	w.Header().Set("Location", resource.Meta.Location)
	writeBody(w, http.StatusCreated, body)
}

func (s *Server) replaceUser(w http.ResponseWriter, r *http.Request, id string) {
	in, ok := readUserInput(w, r)
	if !ok {
		scim.WriteErrorType(w, http.StatusBadRequest, scim.ScimTypeInvalidSyntax, invalidSyntaxDetail)
		return
	}
	data, err := s.users.Replace(r.Context(), id, in)
	if err != nil {
		s.failUserWrite(w, r, err)
		return
	}
	s.writeUser(w, r, http.StatusOK, scim.NewUser(s.baseURL, data))
}

func (s *Server) patchUser(w http.ResponseWriter, r *http.Request, id string) {
	raw, ok := readObject(w, r)
	if !ok {
		scim.WriteErrorType(w, http.StatusBadRequest, scim.ScimTypeInvalidSyntax, invalidPatchDetail)
		return
	}
	patch, err := scim.ParseUserPatch(raw)
	if err != nil {
		var patchErr *scim.PatchError
		if !errors.As(err, &patchErr) {
			s.failUser(w, r, err)
			return
		}
		scim.WriteErrorType(w, http.StatusBadRequest, patchErr.ScimType, patchDetail(patchErr.ScimType, false))
		return
	}
	data, err := s.users.Patch(r.Context(), id, patch)
	if err != nil {
		s.failUserWrite(w, r, err)
		return
	}
	s.writeUser(w, r, http.StatusOK, scim.NewUser(s.baseURL, data))
}

// deleteUser answers 204 with an empty body; the body is not read.
func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request, id string) {
	if err := s.users.Delete(r.Context(), id); err != nil {
		s.failUserWrite(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writeUser(w http.ResponseWriter, r *http.Request, status int, document any) {
	body, err := json.Marshal(document)
	if err != nil {
		s.failUser(w, r, err)
		return
	}
	writeBody(w, status, body)
}

// failUserWrite maps the refusals of a create, replace, patch or delete; the
// last-admin refusal first. Anything else goes to failUser.
func (s *Server) failUserWrite(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrLastAdmin):
		scim.WriteErrorType(w, http.StatusBadRequest, scim.ScimTypeInvalidValue, lastAdminDetail)
	case errors.Is(err, ErrInvalidValue):
		scim.WriteErrorType(w, http.StatusBadRequest, scim.ScimTypeInvalidValue, invalidValueDetail)
	case errors.Is(err, ErrUniqueness):
		scim.WriteErrorType(w, http.StatusConflict, scim.ScimTypeUniqueness, uniquenessDetail)
	default:
		s.failUser(w, r, err)
	}
}

// failUser maps ErrNotFound to 404 and anything else to a logged 500.
func (s *Server) failUser(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrNotFound) {
		scim.WriteError(w, http.StatusNotFound, userNotFoundDetail)
		return
	}
	s.logger.Error(r.Context(), userFailedLogMessage, err, idkit.Field{Key: "component", Value: userLogComponent})
	scim.WriteError(w, http.StatusInternalServerError, internalErrorDetail)
}
