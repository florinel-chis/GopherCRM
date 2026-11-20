package handler

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

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubSCIMUsers records the calls of the handler and answers what it is set
// to.
type stubSCIMUsers struct {
	getCalls     []string
	listCalls    []stubSCIMListCall
	createCalls  []service.SCIMUserInput
	replaceCalls []stubSCIMReplaceCall
	patchCalls   []stubSCIMPatchCall
	record       *service.SCIMUserRecord
	page         *service.SCIMUserPage
	err          error
}

type stubSCIMListCall struct {
	userName          *string
	startIndex, count int
}

func (s *stubSCIMUsers) Get(_ context.Context, rawID string) (*service.SCIMUserRecord, error) {
	s.getCalls = append(s.getCalls, rawID)
	return s.record, s.err
}

func (s *stubSCIMUsers) Create(_ context.Context, in service.SCIMUserInput) (*service.SCIMUserRecord, error) {
	s.createCalls = append(s.createCalls, in)
	return s.record, s.err
}

func (s *stubSCIMUsers) Replace(_ context.Context, rawID string, in service.SCIMUserInput) (*service.SCIMUserRecord, error) {
	s.replaceCalls = append(s.replaceCalls, stubSCIMReplaceCall{rawID, in})
	return s.record, s.err
}

func (s *stubSCIMUsers) Patch(_ context.Context, rawID string, p service.SCIMUserPatch) (*service.SCIMUserRecord, error) {
	s.patchCalls = append(s.patchCalls, stubSCIMPatchCall{rawID, p})
	return s.record, s.err
}

type stubSCIMPatchCall struct {
	rawID string
	patch service.SCIMUserPatch
}

type stubSCIMReplaceCall struct {
	rawID string
	in    service.SCIMUserInput
}

func (s *stubSCIMUsers) List(_ context.Context, userName *string, startIndex, count int) (*service.SCIMUserPage, error) {
	s.listCalls = append(s.listCalls, stubSCIMListCall{userName, startIndex, count})
	return s.page, s.err
}

func TestSCIMUserHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const base = "https://crm.example.test"
	setup := func(stub *stubSCIMUsers) *gin.Engine {
		router := gin.New()
		SetupSCIMRoutes(router.Group("/scim/v2"), NewSCIMHandler(base), NewSCIMUserHandler(stub, base+"/"))
		return router
	}
	get := func(router *gin.Engine, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}
	user := models.User{Email: "jane@x.test", FirstName: "Jane", LastName: "Doe", IsActive: true}
	user.ID = 7
	user.CreatedAt = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	user.UpdatedAt = user.CreatedAt
	emptyPage := &service.SCIMUserPage{Total: 0, StartIndex: 1, Users: []service.SCIMUserRecord{}}

	t.Run("get returns the resource", func(t *testing.T) {
		stub := &stubSCIMUsers{record: &service.SCIMUserRecord{User: user, ExternalID: "sub-7"}}
		w := get(setup(stub), "/scim/v2/Users/7?attributes=userName")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "application/scim+json", w.Header().Get("Content-Type"))
		assert.Equal(t, []string{"7"}, stub.getCalls)
		assert.JSONEq(t, `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"id":"7","externalId":"sub-7",
			"userName":"jane@x.test","name":{"givenName":"Jane","familyName":"Doe"},
			"emails":[{"value":"jane@x.test","type":"work","primary":true}],"active":true,
			"meta":{"resourceType":"User","created":"2026-11-01T00:00:00Z","lastModified":"2026-11-01T00:00:00Z",
			"location":"https://crm.example.test/scim/v2/Users/7"}}`, w.Body.String())
	})

	t.Run("get not found", func(t *testing.T) {
		// The real service refuses each of these ids as not found; the
		// handler maps that to the SCIM 404 without echoing the id.
		for _, id := range []string{"abc", "0", "-1", "18446744073709551616"} {
			stub := &stubSCIMUsers{err: fmt.Errorf("scim user not found: %w", apperrors.ErrNotFound)}
			w := get(setup(stub), "/scim/v2/Users/"+id)
			assert.Equal(t, http.StatusNotFound, w.Code, id)
			assert.Equal(t, "application/scim+json", w.Header().Get("Content-Type"), id)
			assert.Equal(t, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"404","detail":"User not found"}`, w.Body.String(), id)
			assert.Equal(t, []string{id}, stub.getCalls)
		}
	})

	t.Run("list paging", func(t *testing.T) {
		for _, tc := range []struct {
			query             string
			startIndex, count int
		}{
			{"", 1, 100},
			{"?startIndex=0&count=5", 1, 5},
			{"?startIndex=-3", 1, 100},
			{"?startIndex=4&count=-2", 4, 0},
			{"?count=500", 1, 100},
			{"?count=0", 1, 0},
		} {
			stub := &stubSCIMUsers{page: emptyPage}
			w := get(setup(stub), "/scim/v2/Users"+tc.query)
			require.Equal(t, http.StatusOK, w.Code, tc.query)
			require.Len(t, stub.listCalls, 1, tc.query)
			assert.Nil(t, stub.listCalls[0].userName, tc.query)
			assert.Equal(t, tc.startIndex, stub.listCalls[0].startIndex, tc.query)
			assert.Equal(t, tc.count, stub.listCalls[0].count, tc.query)
		}
	})

	t.Run("list invalid paging", func(t *testing.T) {
		for _, query := range []string{"count=abc", "startIndex=1.5", "count=", "startIndex=abc&filter=userName+eq+%22a%22"} {
			stub := &stubSCIMUsers{page: emptyPage}
			w := get(setup(stub), "/scim/v2/Users?"+query)
			assert.Equal(t, http.StatusBadRequest, w.Code, query)
			assert.Equal(t, "application/scim+json", w.Header().Get("Content-Type"), query)
			assert.Equal(t, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"scimType":"invalidValue","status":"400","detail":"startIndex and count must be integers"}`, w.Body.String(), query)
			assert.Empty(t, stub.listCalls, query)
		}
	})

	t.Run("list invalid filters", func(t *testing.T) {
		for _, filter := range []string{`externalId eq "x"`, `userName ne "x"`, `userName co "x"`, `userName pr`, `userName eq x`,
			`userName eq "a" and active eq true`, `(userName eq "a")`, ``, `emails.value eq "x"`, `id eq "1"`} {
			stub := &stubSCIMUsers{page: emptyPage}
			w := get(setup(stub), "/scim/v2/Users?filter="+url.QueryEscape(filter))
			assert.Equal(t, http.StatusBadRequest, w.Code, filter)
			assert.Equal(t, "application/scim+json", w.Header().Get("Content-Type"), filter)
			assert.Equal(t, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"scimType":"invalidFilter","status":"400","detail":"Only the filter userName eq \"value\" is supported"}`, w.Body.String(), filter)
			assert.Empty(t, stub.listCalls, filter)
		}
	})

	t.Run("list with a filter", func(t *testing.T) {
		stub := &stubSCIMUsers{page: &service.SCIMUserPage{Total: 3, StartIndex: 2, Users: []service.SCIMUserRecord{{User: user}}}}
		w := get(setup(stub), "/scim/v2/Users?filter=userName%20eq%20%22Jane%40X.test%22&startIndex=2&count=1&excludedAttributes=emails")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "application/scim+json", w.Header().Get("Content-Type"))
		require.Len(t, stub.listCalls, 1)
		require.NotNil(t, stub.listCalls[0].userName)
		assert.Equal(t, "Jane@X.test", *stub.listCalls[0].userName)
		var doc struct {
			Schemas      []string         `json:"schemas"`
			TotalResults int              `json:"totalResults"`
			ItemsPerPage int              `json:"itemsPerPage"`
			StartIndex   int              `json:"startIndex"`
			Resources    []map[string]any `json:"Resources"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &doc))
		assert.Equal(t, []string{"urn:ietf:params:scim:api:messages:2.0:ListResponse"}, doc.Schemas)
		assert.Equal(t, 3, doc.TotalResults)
		assert.Equal(t, 1, doc.ItemsPerPage)
		assert.Equal(t, 2, doc.StartIndex)
		require.Len(t, doc.Resources, 1)
		assert.Equal(t, "7", doc.Resources[0]["id"])
		assert.NotContains(t, doc.Resources[0], "externalId")
	})

	t.Run("empty list serialises Resources as an array", func(t *testing.T) {
		stub := &stubSCIMUsers{page: &service.SCIMUserPage{Total: 4, StartIndex: 1}}
		w := get(setup(stub), "/scim/v2/Users?count=0")
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:ListResponse"],"totalResults":4,"itemsPerPage":0,"startIndex":1,"Resources":[]}`, w.Body.String())
	})

	t.Run("service error is a SCIM 500", func(t *testing.T) {
		for _, path := range []string{"/scim/v2/Users", "/scim/v2/Users/7"} {
			stub := &stubSCIMUsers{err: errors.New("driver: connection refused")}
			w := get(setup(stub), path)
			assert.Equal(t, http.StatusInternalServerError, w.Code, path)
			assert.Equal(t, "application/scim+json", w.Header().Get("Content-Type"), path)
			assert.Equal(t, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"500","detail":"Internal server error"}`, w.Body.String(), path)
		}
	})
}

func TestSCIMUserWriteHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const base = "https://crm.example.test"
	const secretName, secretSub = "secret-jane@x.test", "secret-sub-470"
	setup := func(stub *stubSCIMUsers) *gin.Engine {
		router := gin.New()
		SetupSCIMRoutes(router.Group("/scim/v2"), NewSCIMHandler(base), NewSCIMUserHandler(stub, base+"/"))
		return router
	}
	send := func(router *gin.Engine, method, path, body, contentType string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		if contentType != "" {
			request.Header.Set("Content-Type", contentType)
		}
		router.ServeHTTP(w, request)
		return w
	}
	user := models.User{Email: secretName, FirstName: "Jane", LastName: "Doe", IsActive: true}
	user.ID = 9
	user.CreatedAt = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	user.UpdatedAt = user.CreatedAt
	valid := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"` + secretName + `","externalId":"` + secretSub + `",
		"name":{"givenName":"Jane","familyName":"Doe"},"active":true,"password":"Ignored-Passw0rd!",
		"emails":[{"value":"other@x.test"}],"roles":[{"value":"CRM.Sales","display":"Sales","type":"x","primary":true}]}`

	t.Run("create answers 201 with Location", func(t *testing.T) {
		stub := &stubSCIMUsers{record: &service.SCIMUserRecord{User: user, ExternalID: secretSub}}
		w := send(setup(stub), http.MethodPost, "/scim/v2/Users", valid, "application/scim+json")
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		assert.Equal(t, "application/scim+json", w.Header().Get("Content-Type"))
		assert.Equal(t, "https://crm.example.test/scim/v2/Users/9", w.Header().Get("Location"))
		var doc map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &doc))
		assert.Equal(t, "9", doc["id"])
		assert.Equal(t, secretSub, doc["externalId"])
		assert.Equal(t, w.Header().Get("Location"), doc["meta"].(map[string]any)["location"])
		require.Len(t, stub.createCalls, 1)
		active := true
		assert.Equal(t, service.SCIMUserInput{UserName: secretName, ExternalID: secretSub, GivenName: "Jane", FamilyName: "Doe",
			Active: &active, Roles: []string{"CRM.Sales"}}, stub.createCalls[0])
	})

	t.Run("replace answers 200", func(t *testing.T) {
		stub := &stubSCIMUsers{record: &service.SCIMUserRecord{User: user}}
		w := send(setup(stub), http.MethodPut, "/scim/v2/Users/9", `{"userName":"a@x.test"}`, "application/json")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "application/scim+json", w.Header().Get("Content-Type"))
		require.Len(t, stub.replaceCalls, 1)
		assert.Equal(t, "9", stub.replaceCalls[0].rawID)
		assert.Equal(t, service.SCIMUserInput{UserName: "a@x.test"}, stub.replaceCalls[0].in, "absent name, active and roles")
	})

	t.Run("invalidSyntax", func(t *testing.T) {
		oversized := `{"userName":"` + secretName + `","x":"` + strings.Repeat("a", 64<<10) + `"}`
		for name, body := range map[string]string{
			"malformed":        `{"userName":"` + secretName + `",`,
			"array":            `[` + valid + `]`,
			"null":             `null`,
			"empty":            ``,
			"trailing data":    valid + ` {}`,
			"trailing garbage": valid + `x`,
			"over 64 KiB":      oversized,
			"active string":    `{"userName":"` + secretName + `","externalId":"` + secretSub + `","active":"False"}`,
			"userName number":  `{"userName":7}`,
			"roles string":     `{"userName":"` + secretName + `","roles":"CRM.Sales"}`,
			"role value int":   `{"userName":"` + secretName + `","roles":[{"value":1}]}`,
			"role no value":    `{"userName":"` + secretName + `","roles":[{"display":"x"}]}`,
			"name string":      `{"userName":"` + secretName + `","name":"Jane"}`,
		} {
			for _, method := range []string{http.MethodPost, http.MethodPut} {
				stub := &stubSCIMUsers{record: &service.SCIMUserRecord{User: user}}
				path := "/scim/v2/Users"
				if method == http.MethodPut {
					path += "/9"
				}
				w := send(setup(stub), method, path, body, "application/scim+json")
				require.Equal(t, http.StatusBadRequest, w.Code, "%s %s: %s", method, name, w.Body.String())
				var doc map[string]any
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &doc), name)
				assert.Equal(t, "invalidSyntax", doc["scimType"], name)
				assert.Empty(t, stub.createCalls, "%s: the service is not called", name)
				assert.Empty(t, stub.replaceCalls, "%s: the service is not called", name)
				assert.NotContains(t, w.Body.String(), secretName, name)
				assert.NotContains(t, w.Body.String(), secretSub, name)
			}
		}
	})

	t.Run("refusals", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			err      error
			status   int
			scimType string
		}{
			{"invalid value", fmt.Errorf("x: %w", service.ErrSCIMInvalidValue), http.StatusBadRequest, "invalidValue"},
			{"last admin", fmt.Errorf("x: %w", service.ErrSCIMLastAdmin), http.StatusBadRequest, "invalidValue"},
			{"uniqueness", fmt.Errorf("x: %w", service.ErrSCIMUniqueness), http.StatusConflict, "uniqueness"},
			{"not found", fmt.Errorf("x: %w", apperrors.ErrNotFound), http.StatusNotFound, ""},
			{"anything else", errors.New(secretName + " " + secretSub), http.StatusInternalServerError, ""},
		} {
			for _, method := range []string{http.MethodPost, http.MethodPut} {
				stub := &stubSCIMUsers{err: tc.err}
				path := "/scim/v2/Users"
				if method == http.MethodPut {
					path += "/9"
				}
				w := send(setup(stub), method, path, valid, "")
				require.Equal(t, tc.status, w.Code, "%s %s: %s", method, tc.name, w.Body.String())
				assert.Equal(t, "application/scim+json", w.Header().Get("Content-Type"))
				var doc map[string]any
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &doc))
				if tc.scimType == "" {
					assert.NotContains(t, doc, "scimType", tc.name)
				} else {
					assert.Equal(t, tc.scimType, doc["scimType"], tc.name)
				}
				if tc.status == http.StatusNotFound {
					assert.Equal(t, "User not found", doc["detail"])
				}
				assert.NotContains(t, w.Body.String(), secretName, tc.name)
				assert.NotContains(t, w.Body.String(), secretSub, tc.name)
				assert.Empty(t, w.Header().Get("Location"), tc.name)
			}
		}
	})
}

// TestSCIMUserPatchHandler (#471): PATCH /scim/v2/Users/:id answers each
// parse refusal with 400, its scimType and a fixed detail that echoes nothing
// of the request, without calling the service; a parsed patch reaches the
// service and answers 200 with the resource; service refusals map as for a
// replace.
func TestSCIMUserPatchHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const base = "https://crm.example.test"
	const secret = "secret-471"
	setup := func(stub *stubSCIMUsers) *gin.Engine {
		router := gin.New()
		SetupSCIMRoutes(router.Group("/scim/v2"), NewSCIMHandler(base), NewSCIMUserHandler(stub, base+"/"))
		return router
	}
	send := func(router *gin.Engine, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPatch, "/scim/v2/Users/9", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/scim+json")
		router.ServeHTTP(w, request)
		return w
	}
	user := models.User{Email: "jane@x.test", FirstName: "Jane", LastName: "Doe"}
	user.ID = 9
	user.CreatedAt = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	user.UpdatedAt = user.CreatedAt

	t.Run("entra deactivation answers 200", func(t *testing.T) {
		stub := &stubSCIMUsers{record: &service.SCIMUserRecord{User: user}}
		w := send(setup(stub), `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Replace","path":"active","value":"False"}]}`)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "application/scim+json", w.Header().Get("Content-Type"))
		var doc map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &doc))
		assert.Equal(t, "9", doc["id"])
		assert.Equal(t, false, doc["active"])
		require.Len(t, stub.patchCalls, 1)
		assert.Equal(t, "9", stub.patchCalls[0].rawID)
		inactive := false
		assert.Equal(t, service.SCIMUserPatch{Active: &inactive}, stub.patchCalls[0].patch)
	})

	t.Run("parse refusals", func(t *testing.T) {
		oversized := `{"Operations":[{"op":"replace","path":"userName","value":"` + secret + strings.Repeat("a", 64<<10) + `"}]}`
		for _, tc := range []struct {
			name, body, scimType, detail string
		}{
			{"malformed", `{"Operations":[{"op":"replace","path":"` + secret, "invalidSyntax", "The request body is not a valid SCIM PatchOp"},
			{"array", `[{"op":"replace"}]`, "invalidSyntax", "The request body is not a valid SCIM PatchOp"},
			{"empty", ``, "invalidSyntax", "The request body is not a valid SCIM PatchOp"},
			{"trailing data", `{"Operations":[{"op":"replace","path":"active","value":true}]} {}`, "invalidSyntax", "The request body is not a valid SCIM PatchOp"},
			{"over 64 KiB", oversized, "invalidSyntax", "The request body is not a valid SCIM PatchOp"},
			{"empty Operations", `{"Operations":[]}`, "invalidSyntax", "The request body is not a valid SCIM PatchOp"},
			{"unknown op", `{"Operations":[{"op":"` + secret + `","path":"active","value":true}]}`, "invalidSyntax", "The request body is not a valid SCIM PatchOp"},
			{"externalId", `{"Operations":[{"op":"replace","path":"externalId","value":"` + secret + `"}]}`, "invalidPath", "The patch names an attribute this provider does not support"},
			{"displayName", `{"Operations":[{"op":"replace","value":{"displayName":"` + secret + `"}}]}`, "invalidPath", "The patch names an attribute this provider does not support"},
			{"name.formatted", `{"Operations":[{"op":"replace","path":"name.formatted","value":"` + secret + `"}]}`, "invalidPath", "The patch names an attribute this provider does not support"},
			{"unknown path", `{"Operations":[{"op":"replace","path":"` + secret + `","value":true}]}`, "invalidPath", "The patch names an attribute this provider does not support"},
			{"remove without path", `{"Operations":[{"op":"remove","value":{"active":"` + secret + `"}}]}`, "noTarget", "A remove operation needs a path"},
			{"active yes", `{"Operations":[{"op":"replace","path":"active","value":"yes"}]}`, "invalidValue", "A User attribute is missing or invalid"},
			{"active null", `{"Operations":[{"op":"replace","path":"active","value":null}]}`, "invalidValue", "A User attribute is missing or invalid"},
			{"active secret", `{"Operations":[{"op":"replace","path":"active","value":"` + secret + `"}]}`, "invalidValue", "A User attribute is missing or invalid"},
			{"remove roles", `{"Operations":[{"op":"remove","path":"roles"}]}`, "invalidValue", "A User attribute is missing or invalid"},
		} {
			stub := &stubSCIMUsers{record: &service.SCIMUserRecord{User: user}}
			w := send(setup(stub), tc.body)
			require.Equal(t, http.StatusBadRequest, w.Code, "%s: %s", tc.name, w.Body.String())
			assert.Equal(t, "application/scim+json", w.Header().Get("Content-Type"), tc.name)
			var doc map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &doc), tc.name)
			assert.Equal(t, tc.scimType, doc["scimType"], tc.name)
			assert.Equal(t, tc.detail, doc["detail"], tc.name)
			assert.Equal(t, "400", doc["status"], tc.name)
			assert.Empty(t, stub.patchCalls, "%s: the service is not called", tc.name)
			assert.NotContains(t, w.Body.String(), secret, tc.name)
			assert.NotContains(t, w.Body.String(), "externalId", tc.name)
			assert.NotContains(t, w.Body.String(), "displayName", tc.name)
			assert.NotContains(t, w.Body.String(), "formatted", tc.name)
		}
	})

	t.Run("accepted forms", func(t *testing.T) {
		for name, body := range map[string]string{
			"TRUE":      `{"Operations":[{"op":"replace","path":"active","value":"TRUE"}]}`,
			"False":     `{"Operations":[{"op":"Replace","path":"active","value":"False"}]}`,
			"okta":      `{"Operations":[{"op":"replace","value":{"active":true}}]}`,
			"name":      `{"Operations":[{"op":"Add","value":{"name.givenName":"Ann"}}]}`,
			"emails":    `{"Operations":[{"op":"replace","path":"emails[type eq \"work\"].value","value":"a@x.test"}]}`,
			"roles":     `{"Operations":[{"op":"replace","path":"roles","value":[{"value":"CRM.Sales"}]}]}`,
			"remove fn": `{"Operations":[{"op":"remove","path":"name.familyName"}]}`,
		} {
			stub := &stubSCIMUsers{record: &service.SCIMUserRecord{User: user}}
			w := send(setup(stub), body)
			require.Equal(t, http.StatusOK, w.Code, "%s: %s", name, w.Body.String())
			assert.Len(t, stub.patchCalls, 1, name)
		}
	})

	t.Run("service refusals", func(t *testing.T) {
		valid := `{"Operations":[{"op":"replace","path":"userName","value":"` + secret + `@x.test"}]}`
		for _, tc := range []struct {
			name     string
			err      error
			status   int
			scimType string
			detail   string
		}{
			{"invalid value", fmt.Errorf("x: %w", service.ErrSCIMInvalidValue), http.StatusBadRequest, "invalidValue", "A User attribute is missing or invalid"},
			{"last admin", fmt.Errorf("x: %w", service.ErrSCIMLastAdmin), http.StatusBadRequest, "invalidValue", "The last active admin must stay an active admin"},
			{"uniqueness", fmt.Errorf("x: %w", service.ErrSCIMUniqueness), http.StatusConflict, "uniqueness", "The userName or externalId is already in use"},
			{"not found", fmt.Errorf("x: %w", apperrors.ErrNotFound), http.StatusNotFound, "", "User not found"},
			{"anything else", errors.New(secret), http.StatusInternalServerError, "", "Internal server error"},
		} {
			stub := &stubSCIMUsers{err: tc.err}
			w := send(setup(stub), valid)
			require.Equal(t, tc.status, w.Code, "%s: %s", tc.name, w.Body.String())
			assert.Equal(t, "application/scim+json", w.Header().Get("Content-Type"))
			var doc map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &doc))
			if tc.scimType == "" {
				assert.NotContains(t, doc, "scimType", tc.name)
			} else {
				assert.Equal(t, tc.scimType, doc["scimType"], tc.name)
			}
			assert.Equal(t, tc.detail, doc["detail"], tc.name)
			assert.NotContains(t, w.Body.String(), secret, tc.name)
		}
	})
}
