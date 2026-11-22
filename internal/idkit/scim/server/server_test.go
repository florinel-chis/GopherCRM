package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/idkit"
	"github.com/florinel-chis/gophercrm/internal/idkit/scim"
)

const testBase = "https://crm.example.test"

// call is one provider call the stubs recorded.
type call struct {
	method string
	id     string
}

type stubUsers struct {
	calls  []call
	inputs []UserInput
	patch  *scim.UserPatch
	list   []*string
	data   scim.UserData
	err    error
}

func (s *stubUsers) Get(_ context.Context, id string) (scim.UserData, error) {
	s.calls = append(s.calls, call{"Get", id})
	return s.data, s.err
}

func (s *stubUsers) List(_ context.Context, userName *string, _, _ int) (UserPage, error) {
	s.calls = append(s.calls, call{"List", ""})
	s.list = append(s.list, userName)
	return UserPage{Total: 1, StartIndex: 1, Users: []scim.UserData{s.data}}, s.err
}

func (s *stubUsers) Create(_ context.Context, in UserInput) (scim.UserData, error) {
	s.calls = append(s.calls, call{"Create", ""})
	s.inputs = append(s.inputs, in)
	return s.data, s.err
}

func (s *stubUsers) Replace(_ context.Context, id string, in UserInput) (scim.UserData, error) {
	s.calls = append(s.calls, call{"Replace", id})
	s.inputs = append(s.inputs, in)
	return s.data, s.err
}

func (s *stubUsers) Patch(_ context.Context, id string, p scim.UserPatch) (scim.UserData, error) {
	s.calls = append(s.calls, call{"Patch", id})
	s.patch = &p
	return s.data, s.err
}

func (s *stubUsers) Delete(_ context.Context, id string) error {
	s.calls = append(s.calls, call{"Delete", id})
	return s.err
}

type stubGroups struct {
	calls []call
	data  GroupData
	err   error
}

func (s *stubGroups) List(_ context.Context, _ *string, _, _ int) (GroupPage, error) {
	s.calls = append(s.calls, call{"List", ""})
	return GroupPage{Total: 1, StartIndex: 1, Groups: []GroupData{s.data}}, s.err
}

func (s *stubGroups) Get(_ context.Context, id string) (GroupData, error) {
	s.calls = append(s.calls, call{"Get", id})
	return s.data, s.err
}

func (s *stubGroups) Create(_ context.Context, _, _ *string) (GroupData, error) {
	s.calls = append(s.calls, call{"Create", ""})
	return s.data, s.err
}

func (s *stubGroups) Patch(_ context.Context, id string, _ scim.GroupPatch) error {
	s.calls = append(s.calls, call{"Patch", id})
	return s.err
}

type logEntry struct {
	msg    string
	err    error
	fields []idkit.Field
}

type stubLogger struct{ entries []logEntry }

func (l *stubLogger) Error(_ context.Context, msg string, err error, fields ...idkit.Field) {
	l.entries = append(l.entries, logEntry{msg, err, fields})
}

type fixture struct {
	users  *stubUsers
	groups *stubGroups
	logger *stubLogger
	server *Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		users:  &stubUsers{data: scim.UserData{ID: "7", Email: "jane@x.test", Active: true, Created: time.Unix(0, 0), LastModified: time.Unix(0, 0)}},
		groups: &stubGroups{data: GroupData{Value: "Sales", MemberIDs: []string{"3", "9"}}},
		logger: &stubLogger{},
	}
	s, err := New(Options{BaseURL: testBase + "/", Users: f.users, Groups: f.groups, Logger: f.logger})
	if err != nil {
		t.Fatal(err)
	}
	f.server = s
	return f
}

func (f *fixture) do(method, target, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	f.server.ServeHTTP(w, r)
	return w
}

func errorBody(status int, scimType, detail string) string {
	b, _ := json.Marshal(map[string]any{"detail": detail})
	d := strings.TrimSuffix(strings.TrimPrefix(string(b), `{"detail":`), "}")
	if scimType == "" {
		return fmt.Sprintf(`{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"%d","detail":%s}`, status, d)
	}
	return fmt.Sprintf(`{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"scimType":"%s","status":"%d","detail":%s}`, scimType, status, d)
}

func TestServerRoutes(t *testing.T) {
	want := []Route{
		{"GET", "/ServiceProviderConfig"}, {"GET", "/Schemas"}, {"GET", "/ResourceTypes"},
		{"GET", "/Users"}, {"GET", "/Users/{id}"}, {"POST", "/Users"}, {"PUT", "/Users/{id}"},
		{"PATCH", "/Users/{id}"}, {"DELETE", "/Users/{id}"},
		{"GET", "/Groups"}, {"GET", "/Groups/{id}"}, {"PATCH", "/Groups/{id}"}, {"POST", "/Groups"},
		{"DELETE", "/Groups/{id}"},
	}
	got := Routes()
	if len(got) != len(want) {
		t.Fatalf("Routes() has %d routes, want %d", len(got), len(want))
	}
	seen := map[Route]bool{}
	for _, route := range got {
		seen[route] = true
	}
	for _, route := range want {
		if !seen[route] {
			t.Errorf("Routes() lacks %v", route)
		}
	}

	userBody := `{"userName":"a@x.test"}`
	patchBody := `{"Operations":[{"op":"replace","path":"active","value":false}]}`
	groupPatch := `{"Operations":[{"op":"add","path":"members","value":[{"value":"3"}]}]}`
	groupID := scim.GroupID("Sales")
	for _, tc := range []struct {
		method, path, body string
		status             int
		users, groups      []call
	}{
		{"GET", "/ServiceProviderConfig", "", 200, nil, nil},
		{"GET", "/Schemas", "", 200, nil, nil},
		{"GET", "/ResourceTypes", "", 200, nil, nil},
		{"GET", "/Users", "", 200, []call{{"List", ""}}, nil},
		{"GET", "/Users/18446744073709551616", "", 200, []call{{"Get", "18446744073709551616"}}, nil},
		{"POST", "/Users", userBody, 201, []call{{"Create", ""}}, nil},
		{"PUT", "/Users/7", userBody, 200, []call{{"Replace", "7"}}, nil},
		{"PATCH", "/Users/7", patchBody, 200, []call{{"Patch", "7"}}, nil},
		{"DELETE", "/Users/7", "", 204, []call{{"Delete", "7"}}, nil},
		{"GET", "/Groups", "", 200, nil, []call{{"List", ""}}},
		{"GET", "/Groups/" + groupID, "", 200, nil, []call{{"Get", groupID}}},
		{"PATCH", "/Groups/" + groupID, groupPatch, 204, nil, []call{{"Patch", groupID}}},
		{"POST", "/Groups", `{"displayName":"Sales"}`, 201, nil, []call{{"Create", ""}}},
		{"DELETE", "/Groups/" + groupID, "", 405, nil, nil},
	} {
		f := newFixture(t)
		w := f.do(tc.method, scim.BasePath+tc.path, tc.body)
		if w.Code != tc.status {
			t.Errorf("%s %s: status %d, want %d (%s)", tc.method, tc.path, w.Code, tc.status, w.Body.String())
		}
		if fmt.Sprint(f.users.calls) != fmt.Sprint(tc.users) || fmt.Sprint(f.groups.calls) != fmt.Sprint(tc.groups) {
			t.Errorf("%s %s: calls users %v groups %v, want %v %v", tc.method, tc.path, f.users.calls, f.groups.calls, tc.users, tc.groups)
		}
	}

	t.Run("outside Routes is the SCIM 404", func(t *testing.T) {
		for _, tc := range []struct{ method, path string }{
			{"PUT", "/Groups/x"}, {"POST", "/Users/7"}, {"DELETE", "/Users"}, {"POST", "/Schemas"},
			{"GET", "/Users/7/x"}, {"GET", "/Users/"}, {"GET", "/Other"}, {"GET", "/scim/v3/Users"},
		} {
			f := newFixture(t)
			target := scim.BasePath + tc.path
			if strings.HasPrefix(tc.path, "/scim/") {
				target = tc.path
			}
			w := f.do(tc.method, target, "")
			if w.Code != http.StatusNotFound || w.Body.String() != errorBody(404, "", "Resource not found") {
				t.Errorf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
			}
			if w.Header().Get("Content-Type") != scim.ContentType {
				t.Errorf("%s %s: Content-Type %q", tc.method, tc.path, w.Header().Get("Content-Type"))
			}
			if len(f.users.calls)+len(f.groups.calls) != 0 {
				t.Errorf("%s %s: provider called", tc.method, tc.path)
			}
		}
	})
}

func TestServerErrorMapping(t *testing.T) {
	userBody := `{"userName":"a@x.test"}`
	patchBody := `{"Operations":[{"op":"replace","path":"active","value":false}]}`
	groupPatch := `{"Operations":[{"op":"add","path":"members","value":[{"value":"3"}]}]}`
	groupID := scim.GroupID("Sales")
	both := fmt.Errorf("%w: %w", ErrLastAdmin, ErrInvalidValue)
	internal := errors.New("driver: connection refused")

	userWrites := []struct{ method, path, body string }{
		{"POST", "/Users", userBody}, {"PUT", "/Users/7", userBody}, {"PATCH", "/Users/7", patchBody}, {"DELETE", "/Users/7", ""},
	}
	for _, write := range userWrites {
		for _, tc := range []struct {
			err      error
			status   int
			scimType string
			detail   string
		}{
			{fmt.Errorf("x: %w", ErrLastAdmin), 400, "invalidValue", "The last active admin must stay an active admin"},
			{both, 400, "invalidValue", "The last active admin must stay an active admin"},
			{fmt.Errorf("x: %w", ErrInvalidValue), 400, "invalidValue", "A User attribute is missing or invalid"},
			{fmt.Errorf("x: %w", ErrUniqueness), 409, "uniqueness", "The userName or externalId is already in use"},
			{fmt.Errorf("x: %w", ErrNotFound), 404, "", "User not found"},
			{internal, 500, "", "Internal server error"},
		} {
			f := newFixture(t)
			f.users.err = tc.err
			w := f.do(write.method, scim.BasePath+write.path, write.body)
			if w.Code != tc.status || w.Body.String() != errorBody(tc.status, tc.scimType, tc.detail) {
				t.Errorf("%s %s %v: %d %s", write.method, write.path, tc.err, w.Code, w.Body.String())
			}
		}
	}

	userReads := []string{"/Users", "/Users/7"}
	for _, path := range userReads {
		for _, tc := range []struct {
			err    error
			status int
			detail string
		}{
			{fmt.Errorf("x: %w", ErrNotFound), 404, "User not found"},
			{fmt.Errorf("x: %w", ErrInvalidValue), 500, "Internal server error"},
			{fmt.Errorf("x: %w", ErrLastAdmin), 500, "Internal server error"},
			{internal, 500, "Internal server error"},
		} {
			f := newFixture(t)
			f.users.err = tc.err
			w := f.do("GET", scim.BasePath+path, "")
			if w.Code != tc.status || w.Body.String() != errorBody(tc.status, "", tc.detail) {
				t.Errorf("GET %s %v: %d %s", path, tc.err, w.Code, w.Body.String())
			}
		}
	}

	for _, tc := range []struct {
		method, path, body string
		err                error
		status             int
		scimType, detail   string
	}{
		{"PATCH", "/Groups/" + groupID, groupPatch, fmt.Errorf("x: %w", ErrLastAdmin), 400, "mutability", "The last active admin must stay an active admin"},
		{"PATCH", "/Groups/" + groupID, groupPatch, both, 400, "mutability", "The last active admin must stay an active admin"},
		{"PATCH", "/Groups/" + groupID, groupPatch, fmt.Errorf("x: %w", ErrInvalidValue), 400, "invalidValue",
			"A Group member is not a user this provider serves, or the role it maps to is invalid"},
		{"PATCH", "/Groups/" + groupID, groupPatch, fmt.Errorf("x: %w", ErrNotFound), 404, "", "Group not found"},
		{"PATCH", "/Groups/" + groupID, groupPatch, internal, 500, "", "Internal server error"},
		{"POST", "/Groups", `{"displayName":"Sales"}`, fmt.Errorf("x: %w", ErrInvalidValue), 400, "invalidValue",
			"A Group can be created only for a groups value of the single sign-on role mapping"},
		{"POST", "/Groups", `{"displayName":"Sales"}`, fmt.Errorf("x: %w", ErrNotFound), 404, "", "Group not found"},
		{"POST", "/Groups", `{"displayName":"Sales"}`, fmt.Errorf("x: %w", ErrUniqueness), 500, "", "Internal server error"},
		{"GET", "/Groups/" + groupID, "", fmt.Errorf("x: %w", ErrNotFound), 404, "", "Group not found"},
		{"GET", "/Groups/" + groupID, "", fmt.Errorf("x: %w", ErrInvalidValue), 500, "", "Internal server error"},
		{"GET", "/Groups", "", fmt.Errorf("x: %w", ErrInvalidValue), 500, "", "Internal server error"},
	} {
		f := newFixture(t)
		f.groups.err = tc.err
		w := f.do(tc.method, scim.BasePath+tc.path, tc.body)
		if w.Code != tc.status || w.Body.String() != errorBody(tc.status, tc.scimType, tc.detail) {
			t.Errorf("%s %s %v: %d %s", tc.method, tc.path, tc.err, w.Code, w.Body.String())
		}
	}

	t.Run("group DELETE is 405", func(t *testing.T) {
		f := newFixture(t)
		w := f.do("DELETE", scim.BasePath+"/Groups/"+groupID, `{"displayName":"x"}`)
		if w.Code != 405 || w.Header().Get("Allow") != "GET, PATCH" || w.Header().Get("Content-Type") != scim.ContentType ||
			w.Body.String() != errorBody(405, "", "Groups come from the single sign-on role mapping and cannot be deleted through SCIM") {
			t.Errorf("%d %v %s", w.Code, w.Header(), w.Body.String())
		}
		if len(f.groups.calls) != 0 {
			t.Errorf("provider called: %v", f.groups.calls)
		}
	})

	t.Run("successes", func(t *testing.T) {
		f := newFixture(t)
		w := f.do("POST", scim.BasePath+"/Users", userBody)
		var user scim.User
		if err := json.Unmarshal(w.Body.Bytes(), &user); err != nil || w.Code != 201 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if user.Meta.Location != testBase+"/scim/v2/Users/7" || w.Header().Get("Location") != user.Meta.Location {
			t.Errorf("Location %q, meta.location %q", w.Header().Get("Location"), user.Meta.Location)
		}
		if w.Header().Get("Content-Type") != scim.ContentType {
			t.Errorf("Content-Type %q", w.Header().Get("Content-Type"))
		}

		w = f.do("POST", scim.BasePath+"/Groups", `{"externalId":"Sales"}`)
		var group scim.Group
		if err := json.Unmarshal(w.Body.Bytes(), &group); err != nil || w.Code != 201 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if group.Meta.Location != testBase+"/scim/v2/Groups/"+groupID || w.Header().Get("Location") != group.Meta.Location {
			t.Errorf("Location %q, meta.location %q", w.Header().Get("Location"), group.Meta.Location)
		}
		if w.Header().Get("Content-Type") != scim.ContentType {
			t.Errorf("Content-Type %q", w.Header().Get("Content-Type"))
		}

		for _, tc := range []struct{ method, path, body string }{
			{"DELETE", "/Users/7", ""},
			{"PATCH", "/Groups/" + groupID, groupPatch},
		} {
			w := f.do(tc.method, scim.BasePath+tc.path, tc.body)
			if w.Code != 204 || w.Body.Len() != 0 {
				t.Errorf("%s %s: %d %q", tc.method, tc.path, w.Code, w.Body.String())
			}
		}
	})
}

func TestServerBodies(t *testing.T) {
	t.Run("create reads only the six fields, whatever the Content-Type", func(t *testing.T) {
		f := newFixture(t)
		body := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User","urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"],
			"userName":"a@x.test","externalId":"sub-1","name":{"givenName":"A","familyName":"B","formatted":"A B"},
			"emails":[{"value":"other@x.test","type":"work","primary":true}],"active":false,"roles":[{"value":"CRM.Sales","primary":true}],
			"password":"Ignored-1!","urn:ietf:params:scim:schemas:extension:enterprise:2.0:User":{"department":"X"}}`
		r := httptest.NewRequest("POST", scim.BasePath+"/Users", strings.NewReader(body))
		r.Header.Set("Content-Type", "text/plain")
		w := httptest.NewRecorder()
		f.server.ServeHTTP(w, r)
		if w.Code != 201 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if len(f.users.inputs) != 1 {
			t.Fatalf("Create calls: %d", len(f.users.inputs))
		}
		got := f.users.inputs[0]
		if got.UserName != "a@x.test" || got.ExternalID != "sub-1" || got.GivenName != "A" || got.FamilyName != "B" ||
			got.Active == nil || *got.Active || fmt.Sprint(got.Roles) != "[CRM.Sales]" {
			t.Errorf("Create got %+v", got)
		}
	})

	t.Run("unreadable bodies are invalidSyntax without a provider call", func(t *testing.T) {
		big := `{"userName":"` + strings.Repeat("a", MaxBodyBytes) + `"}`
		big = big[:MaxBodyBytes+1]
		for _, tc := range []struct{ name, method, path, body, detail string }{
			{"oversized create", "POST", "/Users", big, "The request body is not a valid SCIM User"},
			{"oversized patch", "PATCH", "/Users/7", big, "The request body is not a valid SCIM PatchOp"},
			{"array", "POST", "/Users", `[{"userName":"a@x.test"}]`, "The request body is not a valid SCIM User"},
			{"trailing data", "PUT", "/Users/7", `{"userName":"a@x.test"} {}`, "The request body is not a valid SCIM User"},
			{"role without a string value", "POST", "/Users", `{"userName":"a@x.test","roles":[{"display":"x"}]}`, "The request body is not a valid SCIM User"},
			{"role with a number", "PUT", "/Users/7", `{"userName":"a@x.test","roles":[{"value":3}]}`, "The request body is not a valid SCIM User"},
			{"group create array", "POST", "/Groups", `[]`, "The request body is not a valid SCIM Group"},
			{"group patch trailing", "PATCH", "/Groups/" + scim.GroupID("Sales"), `{"Operations":[]} x`, "The request body is not a valid SCIM PatchOp"},
		} {
			f := newFixture(t)
			w := f.do(tc.method, scim.BasePath+tc.path, tc.body)
			if w.Code != 400 || w.Body.String() != errorBody(400, "invalidSyntax", tc.detail) {
				t.Errorf("%s: %d %s", tc.name, w.Code, w.Body.String())
			}
			if len(f.users.calls)+len(f.groups.calls) != 0 {
				t.Errorf("%s: provider called", tc.name)
			}
		}
		f := newFixture(t)
		exact := `{"userName":"` + strings.Repeat("a", MaxBodyBytes-len(`{"userName":""}`)) + `"}`
		if w := f.do("POST", scim.BasePath+"/Users", exact); w.Code != 201 {
			t.Errorf("a body of exactly MaxBodyBytes: %d", w.Code)
		}
	})

	t.Run("Entra PATCH", func(t *testing.T) {
		f := newFixture(t)
		w := f.do("PATCH", scim.BasePath+"/Users/7", `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Replace","path":"active","value":"False"}]}`)
		if w.Code != 200 || f.users.patch == nil || f.users.patch.Active == nil || *f.users.patch.Active {
			t.Errorf("%d %s %+v", w.Code, w.Body.String(), f.users.patch)
		}
	})

	t.Run("PatchOp refusals", func(t *testing.T) {
		groupPath := "/Groups/" + scim.GroupID("Sales")
		for _, tc := range []struct{ path, body, scimType, detail string }{
			{"/Users/7", `{"Operations":[{"op":"replace","path":"password","value":"x"}]}`, "invalidPath", "The patch names an attribute this provider does not support"},
			{"/Users/7", `{"Operations":[{"op":"remove"}]}`, "noTarget", "A remove operation needs a path"},
			{"/Users/7", `{"Operations":[{"op":"replace","path":"active","value":"maybe"}]}`, "invalidValue", "A User attribute is missing or invalid"},
			{"/Users/7", `{"schemas":[]}`, "invalidSyntax", "The request body is not a valid SCIM PatchOp"},
			{groupPath, `{"Operations":[{"op":"replace","path":"displayName","value":"x"}]}`, "invalidPath", "Only the members of a Group can be changed"},
			{groupPath, `{"Operations":[{"op":"remove"}]}`, "noTarget", "A remove operation needs a path"},
			{groupPath, `{"Operations":[{"op":"add","path":"members","value":[{"value":3}]}]}`, "invalidValue",
				"A Group member is not a user this provider serves, or the role it maps to is invalid"},
		} {
			f := newFixture(t)
			w := f.do("PATCH", scim.BasePath+tc.path, tc.body)
			if w.Code != 400 || w.Body.String() != errorBody(400, tc.scimType, tc.detail) {
				t.Errorf("%s %s: %d %s", tc.path, tc.body, w.Code, w.Body.String())
			}
			if len(f.users.calls)+len(f.groups.calls) != 0 {
				t.Errorf("%s: provider called", tc.body)
			}
		}
	})

	t.Run("query refusals keep their details", func(t *testing.T) {
		for _, tc := range []struct{ path, scimType, detail string }{
			{"/Users?count=abc", "invalidValue", "startIndex and count must be integers"},
			{"/Users?filter=" + url.QueryEscape(`userName co "a"`), "invalidFilter", `Only the filter userName eq "value" is supported`},
			{"/Groups?startIndex=1.5", "invalidValue", "startIndex and count must be integers"},
			{"/Groups?filter=" + url.QueryEscape(`userName eq "a"`), "invalidFilter", `Only the filter displayName eq "value" is supported`},
		} {
			f := newFixture(t)
			w := f.do("GET", scim.BasePath+tc.path, "")
			if w.Code != 400 || w.Body.String() != errorBody(400, tc.scimType, tc.detail) {
				t.Errorf("%s: %d %s", tc.path, w.Code, w.Body.String())
			}
		}
		f := newFixture(t)
		f.do("GET", scim.BasePath+"/Users?filter="+url.QueryEscape(`userName eq "Jane@X.test"`), "")
		if len(f.users.list) != 1 || f.users.list[0] == nil || *f.users.list[0] != "Jane@X.test" {
			t.Errorf("filter value not passed: %v", f.users.list)
		}
	})

	t.Run("excludedAttributes=members omits members", func(t *testing.T) {
		groupPath := "/Groups/" + scim.GroupID("Sales")
		for _, tc := range []struct{ method, path, body string }{
			{"GET", groupPath + "?excludedAttributes=members", ""},
			{"GET", "/Groups?excludedAttributes=members", ""},
			{"POST", "/Groups?excludedAttributes=members", `{"displayName":"Sales"}`},
		} {
			f := newFixture(t)
			w := f.do(tc.method, scim.BasePath+tc.path, tc.body)
			if w.Code >= 300 || strings.Contains(w.Body.String(), "members") {
				t.Errorf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
			}
		}
		f := newFixture(t)
		w := f.do("GET", scim.BasePath+groupPath, "")
		if !strings.Contains(w.Body.String(), `"members":[{"value":"3"},{"value":"9"}]`) {
			t.Errorf("members missing without excludedAttributes: %s", w.Body.String())
		}
	})
}

func TestServerLogging(t *testing.T) {
	const (
		secretID   = "424242"
		secretName = "secret-649@x.test"
	)
	secretGroup := scim.GroupID("Secret-Group")
	internal := errors.New("driver: connection refused")
	for _, tc := range []struct {
		method, path, body string
		group              bool
	}{
		{"GET", "/Users/" + secretID, "", false},
		{"GET", "/Users?filter=" + url.QueryEscape(`userName eq "`+secretName+`"`), "", false},
		{"POST", "/Users", `{"userName":"` + secretName + `"}`, false},
		{"PUT", "/Users/" + secretID, `{"userName":"` + secretName + `"}`, false},
		{"PATCH", "/Users/" + secretID, `{"Operations":[{"op":"replace","path":"userName","value":"` + secretName + `"}]}`, false},
		{"DELETE", "/Users/" + secretID, "", false},
		{"GET", "/Groups/" + secretGroup, "", true},
		{"GET", "/Groups?filter=" + url.QueryEscape(`displayName eq "Secret-Group"`), "", true},
		{"POST", "/Groups", `{"displayName":"Secret-Group"}`, true},
		{"PATCH", "/Groups/" + secretGroup, `{"Operations":[{"op":"add","path":"members","value":[{"value":"` + secretID + `"}]}]}`, true},
	} {
		f := newFixture(t)
		f.users.err = internal
		f.groups.err = internal
		w := f.do(tc.method, scim.BasePath+tc.path, tc.body)
		if w.Code != 500 || w.Body.String() != errorBody(500, "", "Internal server error") {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
		if len(f.logger.entries) != 1 {
			t.Fatalf("%s %s: %d log calls, want 1", tc.method, tc.path, len(f.logger.entries))
		}
		entry := f.logger.entries[0]
		msg, component := "SCIM user request failed", "scim_users"
		if tc.group {
			msg, component = "SCIM group request failed", "scim_groups"
		}
		if entry.msg != msg || len(entry.fields) != 1 || entry.fields[0] != (idkit.Field{Key: "component", Value: component}) {
			t.Errorf("%s %s: logged %q %v", tc.method, tc.path, entry.msg, entry.fields)
		}
		if !errors.Is(entry.err, internal) {
			t.Errorf("%s %s: logged error %v is not the host's", tc.method, tc.path, entry.err)
		}
		logged := entry.msg + fmt.Sprint(entry.fields)
		for _, secret := range []string{secretID, secretName, secretGroup, "Secret-Group"} {
			if strings.Contains(logged, secret) {
				t.Errorf("%s %s: log holds %q", tc.method, tc.path, secret)
			}
		}
	}

	t.Run("refusals log nothing", func(t *testing.T) {
		f := newFixture(t)
		f.users.err = fmt.Errorf("x: %w", ErrNotFound)
		f.do("GET", scim.BasePath+"/Users/7", "")
		f.do("POST", scim.BasePath+"/Users", `[]`)
		if len(f.logger.entries) != 0 {
			t.Errorf("logged %v", f.logger.entries)
		}
	})
}

func TestManagedJudges(t *testing.T) {
	const issuer = "https://idp.example.test/tenant/v2.0"
	identity := func(iss, sub string) idkit.Identity {
		return idkit.Identity{AccountID: "7", Issuer: iss, Subject: sub, Source: idkit.IdentitySourceSCIM}
	}
	for _, tc := range []struct {
		name       string
		issuer     string
		identities []idkit.Identity
		managed    bool
		externalID string
	}{
		{"no identities", issuer, nil, false, ""},
		{"empty issuer", "", []idkit.Identity{identity("", "sub-1")}, false, ""},
		{"trailing space", issuer, []idkit.Identity{identity(issuer+" ", "sub-1")}, false, ""},
		{"case variant", issuer, []idkit.Identity{identity(strings.ToUpper(issuer), "sub-1")}, false, ""},
		{"another issuer", issuer, []idkit.Identity{identity("https://other.example.test", "sub-1")}, false, ""},
		{"exact", issuer, []idkit.Identity{identity(issuer, "sub-1")}, true, "sub-1"},
		{"first exact wins", issuer, []idkit.Identity{
			identity(issuer+" ", "sub-0"), identity("https://other.example.test", "sub-x"),
			identity(issuer, "sub-1"), identity(issuer, "sub-2"),
		}, true, "sub-1"},
	} {
		if got := Manages(tc.issuer, tc.identities); got != tc.managed {
			t.Errorf("%s: Manages = %v, want %v", tc.name, got, tc.managed)
		}
		if got := ExternalID(tc.issuer, tc.identities); got != tc.externalID {
			t.Errorf("%s: ExternalID = %q, want %q", tc.name, got, tc.externalID)
		}
	}
}

func TestNewRefusesMissingProviders(t *testing.T) {
	if s, err := New(Options{Groups: &stubGroups{}}); err == nil || s != nil {
		t.Errorf("nil Users: %v %v", s, err)
	}
	if s, err := New(Options{Users: &stubUsers{}}); err == nil || s != nil {
		t.Errorf("nil Groups: %v %v", s, err)
	}
	s, err := New(Options{BaseURL: testBase, Users: &stubUsers{}, Groups: &stubGroups{}})
	if err != nil || s == nil {
		t.Fatalf("New: %v", err)
	}
	// A nil Logger drops the line; the answer is unchanged.
	w := httptest.NewRecorder()
	s.users.(*stubUsers).err = errors.New("boom")
	s.ServeHTTP(w, httptest.NewRequest("GET", scim.BasePath+"/Users/7", nil))
	if w.Code != 500 {
		t.Errorf("status %d", w.Code)
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", scim.BasePath+"/ServiceProviderConfig", nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != scim.ContentType || !strings.Contains(w.Body.String(), testBase+"/scim/v2/ServiceProviderConfig") {
		t.Errorf("discovery: %d %s", w.Code, w.Body.String())
	}
}
