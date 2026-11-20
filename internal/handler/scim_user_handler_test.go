package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	getCalls  []string
	listCalls []stubSCIMListCall
	record    *service.SCIMUserRecord
	page      *service.SCIMUserPage
	err       error
}

type stubSCIMListCall struct {
	userName          *string
	startIndex, count int
}

func (s *stubSCIMUsers) Get(_ context.Context, rawID string) (*service.SCIMUserRecord, error) {
	s.getCalls = append(s.getCalls, rawID)
	return s.record, s.err
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
