package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// MaxBodyBytes bounds a create, replace or patch body.
const MaxBodyBytes = 64 << 10

// readObject reads one JSON object of at most MaxBodyBytes, whatever the
// Content-Type, and reports false for anything else: an oversized body,
// another JSON value or trailing data.
func readObject(w http.ResponseWriter, r *http.Request) (json.RawMessage, bool) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
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

// userBody is the part of a SCIM User body a create or replace reads. Every
// other attribute (schemas, emails, password, extensions) is ignored.
type userBody struct {
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

// readUserInput reads the body of a create or replace through readObject.
// It reports false for what readObject refuses, a known attribute of the
// wrong type and a roles entry without a string value; the caller answers
// 400 invalidSyntax.
func readUserInput(w http.ResponseWriter, r *http.Request) (UserInput, bool) {
	raw, ok := readObject(w, r)
	if !ok {
		return UserInput{}, false
	}
	var body userBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return UserInput{}, false
	}
	var in UserInput
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
				return UserInput{}, false
			}
			in.Roles = append(in.Roles, *role.Value)
		}
	}
	return in, true
}
