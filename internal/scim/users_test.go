package scim

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseUserFilter(t *testing.T) {
	accepted := []struct{ name, filter, want string }{
		{"entra", `userName eq "a@contoso.test"`, "a@contoso.test"},
		{"okta", `userName eq "jane.doe@example.test"`, "jane.doe@example.test"},
		{"upper case attribute and operator", `USERNAME EQ "a@x.test"`, "a@x.test"},
		{"URN-qualified attribute", `urn:ietf:params:scim:schemas:core:2.0:User:userName eq "a@x.test"`, "a@x.test"},
		{"escaped quote", `userName eq "a\"b@x.test"`, `a"b@x.test`},
		{"unicode escape", `userName eq "josé@x.test"`, "josé@x.test"},
		{"extra inner and outer spaces", "  userName   eq    \"a@x.test\"  ", "a@x.test"},
		{"empty value", `userName eq ""`, ""},
	}
	for _, tc := range accepted {
		t.Run("accepts "+tc.name, func(t *testing.T) {
			got, err := ParseUserFilter(tc.filter)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
	refused := []struct{ name, filter string }{
		{"empty", ""},
		{"blank", "   "},
		{"externalId", `externalId eq "x"`},
		{"emails.value", `emails.value eq "x"`},
		{"id", `id eq "1"`},
		{"ne", `userName ne "x"`},
		{"co", `userName co "x"`},
		{"sw", `userName sw "x"`},
		{"pr", `userName pr`},
		{"and", `userName eq "a" and active eq true`},
		{"or", `userName eq "a" or userName eq "b"`},
		{"not", `not (userName eq "a")`},
		{"parentheses", `(userName eq "a")`},
		{"unquoted", `userName eq x`},
		{"number", `userName eq 1`},
		{"boolean", `userName eq true`},
		{"null", `userName eq null`},
		{"unterminated", `userName eq "a`},
		{"trailing token", `userName eq "a" b`},
		{"trailing quote", `userName eq "a""`},
		{"bad escape", `userName eq "a\x"`},
		{"tab separator", "userName\teq \"a\""},
		{"Unicode-folded attribute", "uſerName eq \"a\""},
		{"other schema URN", `urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:userName eq "a"`},
		{"bare URN", `urn:ietf:params:scim:schemas:core:2.0:User: eq "a"`},
	}
	for _, tc := range refused {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			_, err := ParseUserFilter(tc.filter)
			assert.ErrorIs(t, err, ErrInvalidFilter)
		})
	}
}

func TestUserNameMatches(t *testing.T) {
	for _, tc := range []struct {
		name              string
		stored, requested string
		want              bool
	}{
		{"identical", "jose@x.test", "jose@x.test", true},
		{"ASCII case variant", "Jose@X.test", "jOSE@x.TEST", true},
		{"trailing space", "jose@x.test", "jose@x.test ", false},
		{"accent variant", "jose@x.test", "josé@x.test", false},
		{"long s (U+017F)", "jose@x.test", "joſe@x.test", false},
		{"Kelvin sign (U+212A) for k", "kate@x.test", "Kate@x.test", false},
		{"Kelvin sign (U+212A) for K", "Kate@x.test", "Kate@x.test", false},
		{"different length", "jose@x.test", "jose@x.tes", false},
		{"non-ASCII stored byte against its ASCII-cased neighbour", "Émile@x.test", "émile@x.test", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, UserNameMatches(tc.stored, tc.requested))
		})
	}
}

func TestParsePaging(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		start                string
		startPresent         bool
		count                string
		countPresent         bool
		wantStart, wantCount int
	}{
		{"defaults", "", false, "", false, 1, MaxResults},
		{"window", "3", true, "2", true, 3, 2},
		{"start below one", "0", true, "5", true, 1, 5},
		{"negative start", "-4", true, "", false, 1, MaxResults},
		{"count zero", "", false, "0", true, 1, 0},
		{"negative count", "", false, "-1", true, 1, 0},
		{"count above the maximum", "", false, "500", true, 1, MaxResults},
		{"count out of range", "", false, "99999999999999999999", true, 1, MaxResults},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePaging(tc.start, tc.startPresent, tc.count, tc.countPresent)
			require.NoError(t, err)
			assert.Equal(t, Paging{StartIndex: tc.wantStart, Count: tc.wantCount}, got)
		})
	}
	for _, bad := range []struct{ start, count string }{{"abc", "1"}, {"1.5", "1"}, {"1", "abc"}, {"1", ""}, {"", "1"}, {"1", "0x10"}} {
		_, err := ParsePaging(bad.start, true, bad.count, true)
		assert.ErrorIs(t, err, ErrInvalidPaging, "%q %q", bad.start, bad.count)
	}
}

func TestUserResource(t *testing.T) {
	cest := time.FixedZone("CEST", 2*60*60)
	u := &models.User{Email: "jane@x.test", FirstName: "Jane", LastName: "Doe", IsActive: false}
	u.ID = 42
	u.CreatedAt = time.Date(2026, 11, 1, 10, 0, 0, 0, cest)
	u.UpdatedAt = time.Date(2026, 11, 2, 11, 30, 0, 0, cest)

	for _, base := range []string{"https://crm.example.test", "https://crm.example.test/", "https://crm.example.test//"} {
		body, err := json.Marshal(NewUser(base, u, "sub-1"))
		require.NoError(t, err)
		assert.JSONEq(t, `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"id":"42","externalId":"sub-1",
			"userName":"jane@x.test","name":{"givenName":"Jane","familyName":"Doe"},
			"emails":[{"value":"jane@x.test","type":"work","primary":true}],"active":false,
			"meta":{"resourceType":"User","created":"2026-11-01T08:00:00Z","lastModified":"2026-11-02T09:30:00Z",
			"location":"https://crm.example.test/scim/v2/Users/42"}}`, string(body), base)
	}
	body, err := json.Marshal(NewUser("https://crm.example.test", u, ""))
	require.NoError(t, err)
	assert.NotContains(t, string(body), "externalId")

	list, err := json.Marshal(NewUserList(7, 3, nil))
	require.NoError(t, err)
	assert.JSONEq(t, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:ListResponse"],"totalResults":7,"itemsPerPage":0,"startIndex":3,"Resources":[]}`, string(list))
}

func TestWriteErrorType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	WriteErrorType(c, http.StatusBadRequest, ScimTypeInvalidFilter, `Only "this"`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, ContentType, w.Header().Get("Content-Type"))
	assert.Equal(t, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"scimType":"invalidFilter","status":"400","detail":"Only \"this\""}`, w.Body.String())
}
