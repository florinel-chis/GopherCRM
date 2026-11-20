package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/scim"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubSCIMGroups records the calls of the handler and answers what it is set
// to.
type stubSCIMGroups struct {
	getCalls   []string
	listCalls  []stubSCIMGroupListCall
	patchCalls []stubSCIMGroupPatchCall
	record     *service.SCIMGroupRecord
	page       *service.SCIMGroupPage
	err        error
}

type stubSCIMGroupListCall struct {
	displayName       *string
	startIndex, count int
}

type stubSCIMGroupPatchCall struct {
	rawID string
	patch service.SCIMGroupPatch
}

func (s *stubSCIMGroups) List(_ context.Context, displayName *string, startIndex, count int) (*service.SCIMGroupPage, error) {
	s.listCalls = append(s.listCalls, stubSCIMGroupListCall{displayName, startIndex, count})
	return s.page, s.err
}

func (s *stubSCIMGroups) Get(_ context.Context, rawID string) (*service.SCIMGroupRecord, error) {
	s.getCalls = append(s.getCalls, rawID)
	return s.record, s.err
}

func (s *stubSCIMGroups) Patch(_ context.Context, rawID string, p service.SCIMGroupPatch) (*service.SCIMGroupRecord, error) {
	s.patchCalls = append(s.patchCalls, stubSCIMGroupPatchCall{rawID, p})
	return s.record, s.err
}

func TestSCIMGroupHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const base = "https://crm.example.test"
	setup := func(stub *stubSCIMGroups) *gin.Engine {
		router := gin.New()
		SetupSCIMRoutes(router.Group("/scim/v2"), NewSCIMHandler(base), NewSCIMUserHandler(nil, base), NewSCIMGroupHandler(stub, base+"/"))
		return router
	}
	send := func(router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	errorBody := func(status, scimType, detail string) string {
		if scimType == "" {
			return `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"` + status + `","detail":"` + detail + `"}`
		}
		return `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"scimType":"` + scimType + `","status":"` + status + `","detail":"` + detail + `"}`
	}
	salesID := scim.GroupID("Sales")
	sales := &service.SCIMGroupRecord{Value: "Sales", MemberIDs: []uint{3, 9}}
	addBody := `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"add","path":"members","value":[{"value":"3"}]}]}`

	t.Run("list", func(t *testing.T) {
		stub := &stubSCIMGroups{page: &service.SCIMGroupPage{Total: 2, StartIndex: 1, Groups: []service.SCIMGroupRecord{
			{Value: "Admins", MemberIDs: []uint{}}, *sales}}}
		w := send(setup(stub), http.MethodGet, "/scim/v2/Groups", "")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, scim.ContentType, w.Header().Get("Content-Type"))
		adminsID := scim.GroupID("Admins")
		assert.JSONEq(t, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:ListResponse"],"totalResults":2,"itemsPerPage":2,"startIndex":1,"Resources":[
			{"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],"id":"`+adminsID+`","displayName":"Admins","members":[],
			 "meta":{"resourceType":"Group","location":"`+base+`/scim/v2/Groups/`+adminsID+`"}},
			{"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],"id":"`+salesID+`","displayName":"Sales","members":[{"value":"3"},{"value":"9"}],
			 "meta":{"resourceType":"Group","location":"`+base+`/scim/v2/Groups/`+salesID+`"}}]}`, w.Body.String())
		require.Len(t, stub.listCalls, 1)
		assert.Nil(t, stub.listCalls[0].displayName)
		assert.Equal(t, 1, stub.listCalls[0].startIndex)
		assert.Equal(t, scim.MaxResults, stub.listCalls[0].count)
	})

	t.Run("list filter and paging", func(t *testing.T) {
		stub := &stubSCIMGroups{page: &service.SCIMGroupPage{Total: 1, StartIndex: 2, Groups: nil}}
		w := send(setup(stub), http.MethodGet, `/scim/v2/Groups?filter=displayName+eq+%22Sales%22&startIndex=2&count=5`, "")
		require.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"Resources":[]`)
		require.Len(t, stub.listCalls, 1)
		require.NotNil(t, stub.listCalls[0].displayName)
		assert.Equal(t, "Sales", *stub.listCalls[0].displayName)
		assert.Equal(t, 2, stub.listCalls[0].startIndex)
		assert.Equal(t, 5, stub.listCalls[0].count)
	})

	t.Run("list refusals", func(t *testing.T) {
		for _, tc := range []struct{ query, scimType, detail string }{
			{`filter=userName+eq+%22a%22`, "invalidFilter", `Only the filter displayName eq \"value\" is supported`},
			{`filter=displayName+co+%22a%22`, "invalidFilter", `Only the filter displayName eq \"value\" is supported`},
			{`count=abc`, "invalidValue", "startIndex and count must be integers"},
		} {
			stub := &stubSCIMGroups{}
			w := send(setup(stub), http.MethodGet, "/scim/v2/Groups?"+tc.query, "")
			assert.Equal(t, http.StatusBadRequest, w.Code, tc.query)
			assert.Equal(t, errorBody("400", tc.scimType, tc.detail), w.Body.String(), tc.query)
			assert.Empty(t, stub.listCalls, tc.query)
		}
	})

	t.Run("get", func(t *testing.T) {
		stub := &stubSCIMGroups{record: sales}
		w := send(setup(stub), http.MethodGet, "/scim/v2/Groups/"+salesID, "")
		require.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"displayName":"Sales"`)
		assert.Equal(t, []string{salesID}, stub.getCalls)
	})

	t.Run("get not found", func(t *testing.T) {
		stub := &stubSCIMGroups{err: fmt.Errorf("scim group not found: %w", apperrors.ErrNotFound)}
		for _, method := range []string{http.MethodGet, http.MethodPatch} {
			w := send(setup(stub), method, "/scim/v2/Groups/nope", addBody)
			assert.Equal(t, http.StatusNotFound, w.Code, method)
			assert.Equal(t, errorBody("404", "", "Group not found"), w.Body.String(), method)
		}
	})

	t.Run("patch", func(t *testing.T) {
		stub := &stubSCIMGroups{record: sales}
		w := send(setup(stub), http.MethodPatch, "/scim/v2/Groups/"+salesID, addBody)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, scim.ContentType, w.Header().Get("Content-Type"))
		assert.Contains(t, w.Body.String(), `"members":[{"value":"3"},{"value":"9"}]`)
		require.Len(t, stub.patchCalls, 1)
		assert.Equal(t, salesID, stub.patchCalls[0].rawID)
		assert.Equal(t, []scim.GroupPatchOp{{Op: scim.GroupPatchAdd, Values: []string{"3"}}}, stub.patchCalls[0].patch.Ops)
	})

	t.Run("patch parse refusals call no service", func(t *testing.T) {
		for _, tc := range []struct{ name, body, scimType, detail string }{
			{"not json", `{`, "invalidSyntax", "The request body is not a valid SCIM PatchOp"},
			{"array", `[]`, "invalidSyntax", "The request body is not a valid SCIM PatchOp"},
			{"no operations", `{"schemas":[]}`, "invalidSyntax", "The request body is not a valid SCIM PatchOp"},
			{"displayName", `{"Operations":[{"op":"replace","path":"displayName","value":"Secret-Name"}]}`, "invalidPath", "Only the members of a Group can be changed"},
			{"remove without path", `{"Operations":[{"op":"remove"}]}`, "noTarget", "A remove operation needs a path"},
			{"member number", `{"Operations":[{"op":"add","path":"members","value":[{"value":3}]}]}`, "invalidValue",
				"A Group member is not a user this provider serves, or the role it maps to is invalid"},
		} {
			stub := &stubSCIMGroups{record: sales}
			w := send(setup(stub), http.MethodPatch, "/scim/v2/Groups/"+salesID, tc.body)
			assert.Equal(t, http.StatusBadRequest, w.Code, tc.name)
			assert.Equal(t, errorBody("400", tc.scimType, tc.detail), w.Body.String(), tc.name)
			assert.NotContains(t, w.Body.String(), "Secret-Name", tc.name)
			assert.Empty(t, stub.patchCalls, tc.name)
		}
	})

	t.Run("patch invalid value", func(t *testing.T) {
		stub := &stubSCIMGroups{err: fmt.Errorf("a member is not a visible user: %w", service.ErrSCIMInvalidValue)}
		w := send(setup(stub), http.MethodPatch, "/scim/v2/Groups/"+salesID, addBody)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Equal(t, errorBody("400", "invalidValue", "A Group member is not a user this provider serves, or the role it maps to is invalid"), w.Body.String())
	})

	t.Run("last_admin_mutability", func(t *testing.T) {
		stub := &stubSCIMGroups{err: fmt.Errorf("x: %w", service.ErrSCIMGroupLastAdmin)}
		w := send(setup(stub), http.MethodPatch, "/scim/v2/Groups/"+salesID, addBody)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Equal(t, errorBody("400", "mutability", "The last active admin must stay an active admin"), w.Body.String())
	})

	t.Run("last_admin_before_invalid_value", func(t *testing.T) {
		both := fmt.Errorf("%w: %w", service.ErrSCIMGroupLastAdmin, service.ErrSCIMInvalidValue)
		require.ErrorIs(t, both, service.ErrSCIMInvalidValue)
		stub := &stubSCIMGroups{err: both}
		w := send(setup(stub), http.MethodPatch, "/scim/v2/Groups/"+salesID, addBody)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Equal(t, errorBody("400", "mutability", "The last active admin must stay an active admin"), w.Body.String())
	})

	t.Run("method not allowed", func(t *testing.T) {
		for _, tc := range []struct{ method, path, allow string }{
			{http.MethodPost, "/scim/v2/Groups", "GET"},
			{http.MethodDelete, "/scim/v2/Groups/" + salesID, "GET, PATCH"},
		} {
			stub := &stubSCIMGroups{}
			w := send(setup(stub), tc.method, tc.path, `{"displayName":"New"}`)
			assert.Equal(t, http.StatusMethodNotAllowed, w.Code, tc.method)
			assert.Equal(t, tc.allow, w.Header().Get("Allow"), tc.method)
			assert.Equal(t, scim.ContentType, w.Header().Get("Content-Type"), tc.method)
			assert.Equal(t, errorBody("405", "", "Groups come from the single sign-on role mapping and cannot be created or deleted through SCIM"), w.Body.String(), tc.method)
			assert.Empty(t, stub.patchCalls)
			assert.Empty(t, stub.getCalls)
			assert.Empty(t, stub.listCalls)
		}
	})

	t.Run("service error is a SCIM 500 and logs no request value", func(t *testing.T) {
		var buf bytes.Buffer
		logger := logrus.New()
		logger.SetOutput(&buf)
		logger.SetFormatter(&logrus.JSONFormatter{})
		previous := utils.Logger
		utils.Logger = logger
		t.Cleanup(func() { utils.Logger = previous })

		const secretGroup = "Secret-Group-506"
		secretID := scim.GroupID(secretGroup)
		body := `{"Operations":[{"op":"add","path":"members","value":[{"value":"424242","display":"Secret-Display"}]}]}`
		for _, tc := range []struct{ method, path, body string }{
			{http.MethodGet, "/scim/v2/Groups/" + secretID, ""},
			{http.MethodGet, "/scim/v2/Groups?filter=displayName+eq+%22" + secretGroup + "%22", ""},
			{http.MethodPatch, "/scim/v2/Groups/" + secretID, body},
		} {
			buf.Reset()
			stub := &stubSCIMGroups{err: errors.New("driver: connection refused")}
			w := send(setup(stub), tc.method, tc.path, tc.body)
			assert.Equal(t, http.StatusInternalServerError, w.Code, tc.path)
			assert.Equal(t, errorBody("500", "", "Internal server error"), w.Body.String(), tc.path)
			logged := buf.String()
			assert.Contains(t, logged, "scim_groups", tc.path)
			assert.Contains(t, logged, "SCIM group request failed", tc.path)
			for _, secret := range []string{secretID, secretGroup, "424242", "Secret-Display"} {
				assert.NotContains(t, logged, secret, tc.path)
			}
		}
	})
}
