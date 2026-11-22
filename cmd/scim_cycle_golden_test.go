package main

// Issue #636: a recorded SCIM provisioning cycle through the router
// production uses. TestSCIMProvisioningCycleGolden sends one fixed sequence
// of 20 requests in Microsoft Entra ID's documented shapes (user lookup,
// create, read, patch, replace, the group sequence, error answers,
// deactivation and delete) over a fresh SQLite file and compares each answer's
// status, Content-Type, Location and ETag headers and body with
// testdata/scim_provisioning_cycle.json (-update-scim-cycle rewrites it).
// Only meta.created and meta.lastModified are replaced, after they are
// checked; ids and locations are kept as served. TestSCIMProvisioningCycleGoldenCompare
// checks that the comparison reports a changed status, a missing body field
// and a changed header by their step.

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/florinel-chis/gophercrm/internal/idkit/scim"
	"github.com/florinel-chis/gophercrm/internal/models"
)

var updateSCIMCycle = flag.Bool("update-scim-cycle", false, "rewrite testdata/scim_provisioning_cycle.json")

const scimCyclePath = "testdata/scim_provisioning_cycle.json"

// scimCycleStepNames is the fixed order of the cycle.
var scimCycleStepNames = []string{
	"users_lookup_empty", "users_create", "users_create_duplicate", "users_get", "users_lookup_found",
	"users_patch_path", "users_patch_no_path", "users_replace",
	"groups_lookup", "groups_create", "groups_add_member", "groups_get", "groups_remove_member", "groups_delete",
	"users_get_missing", "users_filter_invalid", "users_patch_malformed",
	"users_deactivate", "users_delete", "users_get_after_delete",
}

type scimCycleGolden struct {
	Steps []scimCycleStep `json:"steps"`
}

type scimCycleStep struct {
	Name        string      `json:"name"`
	Method      string      `json:"method"`
	Path        string      `json:"path"`
	RequestBody string      `json:"request_body"`
	Status      int         `json:"status"`
	ContentType string      `json:"content_type"`
	Location    string      `json:"location"`
	ETag        string      `json:"etag"`
	Body        interface{} `json:"body"`
}

// scimCycleRequest is one step's request; build sees the id the create
// answered ("" before it).
type scimCycleRequest struct {
	name  string
	build func(userID string) (method, path, body string)
}

const (
	scimCycleUserName = "cycle.person@crm.example.test"
	scimCycleIssuer   = "https://login.microsoftonline.com/6f0c3a1e-0636-4d2b-9a11-000000000636/v2.0"
)

func scimCycleRequests() []scimCycleRequest {
	lookup := "/scim/v2/Users?filter=" + url.QueryEscape(`userName eq "`+scimCycleUserName+`"`)
	createBody := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User","urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"],` +
		`"externalId":"0b5e7d2c-0636-4f1a-8e3d-00000000c1c1","userName":"` + scimCycleUserName + `","active":true,` +
		`"emails":[{"primary":true,"type":"work","value":"` + scimCycleUserName + `"}],"meta":{"resourceType":"User"},` +
		`"name":{"formatted":"Cycle Person","familyName":"Person","givenName":"Cycle"},` +
		`"roles":[{"primary":false,"type":"WindowsAzureActiveDirectoryRole","value":"CRM.Sales"}]}`
	groupID := scim.GroupID(entraGroupObjectID)
	user := func(id string) string { return "/scim/v2/Users/" + id }
	fixed := func(method, path, body string) func(string) (string, string, string) {
		return func(string) (string, string, string) { return method, path, body }
	}
	member := func(op string) func(string) (string, string, string) {
		return func(id string) (string, string, string) {
			return http.MethodPatch, "/scim/v2/Groups/" + groupID,
				`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"` + op + `","path":"members","value":[{"value":"` + id + `"}]}]}`
		}
	}
	return []scimCycleRequest{
		{"users_lookup_empty", fixed(http.MethodGet, lookup, "")},
		{"users_create", fixed(http.MethodPost, "/scim/v2/Users", createBody)},
		{"users_create_duplicate", fixed(http.MethodPost, "/scim/v2/Users", createBody)},
		{"users_get", func(id string) (string, string, string) { return http.MethodGet, user(id), "" }},
		{"users_lookup_found", fixed(http.MethodGet, lookup, "")},
		{"users_patch_path", func(id string) (string, string, string) {
			return http.MethodPatch, user(id), `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],` +
				`"Operations":[{"op":"Replace","path":"name.givenName","value":"Cyclist"}]}`
		}},
		{"users_patch_no_path", func(id string) (string, string, string) {
			return http.MethodPatch, user(id), `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],` +
				`"Operations":[{"op":"Replace","value":{"name.familyName":"Rider","active":true}}]}`
		}},
		{"users_replace", func(id string) (string, string, string) {
			return http.MethodPut, user(id), `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],` +
				`"externalId":"0b5e7d2c-0636-4f1a-8e3d-00000000c1c1","userName":"` + scimCycleUserName + `","active":true,` +
				`"name":{"familyName":"Person","givenName":"Cycle"},"roles":[{"value":"CRM.Sales"}]}`
		}},
		{"groups_lookup", fixed(http.MethodGet, "/scim/v2/Groups?excludedAttributes=members&filter="+url.QueryEscape(`displayName eq "CRM Sales"`), "")},
		{"groups_create", fixed(http.MethodPost, "/scim/v2/Groups",
			`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group","http://schemas.microsoft.com/2006/11/ResourceManagement/ADSCIM/2.0/Group"],`+
				`"externalId":"`+entraGroupObjectID+`","displayName":"CRM Sales","meta":{"resourceType":"Group"}}`)},
		{"groups_add_member", member("Add")},
		{"groups_get", fixed(http.MethodGet, "/scim/v2/Groups/"+groupID, "")},
		{"groups_remove_member", member("Remove")},
		// DELETE /Groups/:id is answered 405; the group stays.
		{"groups_delete", fixed(http.MethodDelete, "/scim/v2/Groups/"+groupID, "")},
		{"users_get_missing", fixed(http.MethodGet, "/scim/v2/Users/999999", "")},
		{"users_filter_invalid", fixed(http.MethodGet, "/scim/v2/Users?filter="+url.QueryEscape(`userName eq`), "")},
		{"users_patch_malformed", func(id string) (string, string, string) { return http.MethodPatch, user(id), `{` }},
		{"users_deactivate", func(id string) (string, string, string) {
			return http.MethodPatch, user(id), `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],` +
				`"Operations":[{"op":"Replace","path":"active","value":"False"}]}`
		}},
		{"users_delete", func(id string) (string, string, string) { return http.MethodDelete, user(id), "" }},
		{"users_get_after_delete", func(id string) (string, string, string) { return http.MethodGet, user(id), "" }},
	}
}

// scimCycleBody decodes an answer body and replaces meta.created and
// meta.lastModified, after checking both are RFC 3339 and in order. An empty
// body is "".
func scimCycleBody(t *testing.T, step string, raw []byte) interface{} {
	t.Helper()
	if len(raw) == 0 {
		return ""
	}
	var decoded interface{}
	require.NoError(t, json.Unmarshal(raw, &decoded), "step %s: the body is JSON: %s", step, raw)
	var walk func(value interface{})
	walk = func(value interface{}) {
		switch typed := value.(type) {
		case map[string]interface{}:
			if meta, ok := typed["meta"].(map[string]interface{}); ok {
				var created, modified time.Time
				for key, at := range map[string]*time.Time{"created": &created, "lastModified": &modified} {
					text, present := meta[key].(string)
					if !present {
						continue
					}
					parsed, err := time.Parse(time.RFC3339, text)
					require.NoError(t, err, "step %s: meta.%s is RFC 3339", step, key)
					*at = parsed
					meta[key] = "<timestamp>"
				}
				if !created.IsZero() && !modified.IsZero() {
					require.False(t, modified.Before(created), "step %s: lastModified is not before created", step)
				}
			}
			for _, inner := range typed {
				walk(inner)
			}
		case []interface{}:
			for _, inner := range typed {
				walk(inner)
			}
		}
	}
	walk(decoded)
	return decoded
}

// scimCycleRecord runs the cycle on a fresh router and returns what it
// recorded and the SCIM token it used.
func scimCycleRecord(t *testing.T) (scimCycleGolden, string) {
	t.Helper()
	// The SCIM limits stay live: every request comes from its own client
	// address, so none is throttled.
	t.Setenv("DISABLE_RATE_LIMIT", "")
	var adminRole, salesRole models.Role
	router, boot := scimWiredRouterWith(t, func(boot *automationBoot) {
		require.NoError(t, models.DB.Where("system_key = ?", "admin").First(&adminRole).Error)
		require.NoError(t, models.DB.Where("system_key = ?", "sales").First(&salesRole).Error)
		boot.cfg.SSO.IssuerURL = scimCycleIssuer
		boot.cfg.SSO.RoleMapping = fmt.Sprintf(`[{"claim":"groups","value":"Admins","role_id":%d},{"claim":"groups","value":"Sales","role_id":%d},{"claim":"roles","value":"CRM.Sales","role_id":%d},{"claim":"groups","value":"%s","role_id":%d}]`,
			adminRole.ID, salesRole.ID, salesRole.ID, entraGroupObjectID, salesRole.ID)
	})
	prefix := boot.cfg.API.Prefix
	addr := 0
	nextRemote := func() string {
		addr++
		return fmt.Sprintf("192.0.2.%d:1234", 10+addr)
	}

	const password = "Cycle-Passw0rd!-0636"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	admin := &models.User{Email: "admin-0636@crm.example.test", Password: string(hash), FirstName: "Scim", LastName: "Admin", Role: models.RoleAdmin, IsActive: true}
	require.NoError(t, models.DB.Create(admin).Error)
	recorder := scimWiredSend(router, http.MethodPost, prefix+"/auth/login", `{"email":"`+admin.Email+`","password":"`+password+`"}`, nextRemote(), "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var login struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &login))
	recorder = scimWiredSend(router, http.MethodPost, prefix+"/settings/scim/tokens", `{"name":"Entra"}`, nextRemote(), "Bearer "+login.Data.Token)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	var created struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &created))
	require.NotEmpty(t, created.Data.Token)

	var golden scimCycleGolden
	userID := ""
	for _, request := range scimCycleRequests() {
		method, path, body := request.build(userID)
		recorder := scimWiredSend(router, method, path, body, nextRemote(), "Bearer "+created.Data.Token)
		require.NotContains(t, []int{http.StatusUnauthorized, http.StatusTooManyRequests}, recorder.Code,
			"step %s: the harness is refused: %s", request.name, recorder.Body.String())
		if request.name == "users_create" {
			require.Equal(t, http.StatusCreated, recorder.Code, "step %s: %s", request.name, recorder.Body.String())
			var user struct {
				ID string `json:"id"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &user))
			require.NotEmpty(t, user.ID)
			userID = user.ID
		}
		golden.Steps = append(golden.Steps, scimCycleStep{
			Name: request.name, Method: method, Path: path, RequestBody: body,
			Status:      recorder.Code,
			ContentType: recorder.Header().Get("Content-Type"),
			Location:    recorder.Header().Get("Location"),
			ETag:        recorder.Header().Get("ETag"),
			Body:        scimCycleBody(t, request.name, recorder.Body.Bytes()),
		})
	}
	return golden, created.Data.Token
}

func scimCycleMarshal(t *testing.T, golden scimCycleGolden) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(golden, "", "  ")
	require.NoError(t, err)
	return append(raw, '\n')
}

func scimCycleLoad(t *testing.T) scimCycleGolden {
	t.Helper()
	raw, err := os.ReadFile(scimCyclePath)
	require.NoError(t, err, "the committed golden; write it with -update-scim-cycle")
	var golden scimCycleGolden
	require.NoError(t, json.Unmarshal(raw, &golden))
	return golden
}

// scimCycleDiff compares two cycles step by step and returns one line per
// difference, each naming the step and the field.
func scimCycleDiff(want, got scimCycleGolden) []string {
	var diffs []string
	for i := 0; i < len(want.Steps) || i < len(got.Steps); i++ {
		switch {
		case i >= len(got.Steps):
			diffs = append(diffs, fmt.Sprintf("step %s: missing from the run", want.Steps[i].Name))
			continue
		case i >= len(want.Steps):
			diffs = append(diffs, fmt.Sprintf("step %s: not in the golden", got.Steps[i].Name))
			continue
		}
		name := want.Steps[i].Name
		if got.Steps[i].Name != name {
			name = want.Steps[i].Name + "/" + got.Steps[i].Name
		}
		scimCycleJSONDiff("step "+name, "", scimCycleJSONValue(want.Steps[i]), scimCycleJSONValue(got.Steps[i]), &diffs)
	}
	return diffs
}

func scimCycleJSONValue(value interface{}) interface{} {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var decoded interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		panic(err)
	}
	return decoded
}

// scimCycleJSONDiff appends "<prefix>: <path>: golden <want>, run <got>" for
// every leaf that differs.
func scimCycleJSONDiff(prefix, path string, want, got interface{}, diffs *[]string) {
	field := path
	if field == "" {
		field = "."
	}
	switch w := want.(type) {
	case map[string]interface{}:
		g, ok := got.(map[string]interface{})
		if !ok {
			break
		}
		keys := map[string]bool{}
		for key := range w {
			keys[key] = true
		}
		for key := range g {
			keys[key] = true
		}
		sorted := make([]string, 0, len(keys))
		for key := range keys {
			sorted = append(sorted, key)
		}
		sort.Strings(sorted)
		for _, key := range sorted {
			wv, wok := w[key]
			gv, gok := g[key]
			switch {
			case !gok:
				*diffs = append(*diffs, fmt.Sprintf("%s: %s.%s: golden %s, run has no field", prefix, path, key, scimCycleJSONText(wv)))
			case !wok:
				*diffs = append(*diffs, fmt.Sprintf("%s: %s.%s: golden has no field, run %s", prefix, path, key, scimCycleJSONText(gv)))
			default:
				scimCycleJSONDiff(prefix, path+"."+key, wv, gv, diffs)
			}
		}
		return
	case []interface{}:
		g, ok := got.([]interface{})
		if !ok {
			break
		}
		if len(w) != len(g) {
			*diffs = append(*diffs, fmt.Sprintf("%s: %s: golden %d entries, run %d: golden %s, run %s",
				prefix, field, len(w), len(g), scimCycleJSONText(w), scimCycleJSONText(g)))
			return
		}
		for i := range w {
			scimCycleJSONDiff(prefix, fmt.Sprintf("%s[%d]", path, i), w[i], g[i], diffs)
		}
		return
	}
	if !reflect.DeepEqual(want, got) {
		*diffs = append(*diffs, fmt.Sprintf("%s: %s: golden %s, run %s", prefix, field, scimCycleJSONText(want), scimCycleJSONText(got)))
	}
}

func scimCycleJSONText(value interface{}) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(raw)
}

func scimCycleNames(golden scimCycleGolden) []string {
	names := make([]string, 0, len(golden.Steps))
	for _, step := range golden.Steps {
		names = append(names, step.Name)
	}
	return names
}

func TestSCIMProvisioningCycleGolden(t *testing.T) {
	got, token := scimCycleRecord(t)
	require.Equal(t, scimCycleStepNames, scimCycleNames(got), "the fixed step list")
	if *updateSCIMCycle {
		require.NoError(t, os.MkdirAll(filepath.Dir(scimCyclePath), 0o755))
		require.NoError(t, os.WriteFile(scimCyclePath, scimCycleMarshal(t, got), 0o644))
	}
	raw, err := os.ReadFile(scimCyclePath)
	require.NoError(t, err)
	require.NotContains(t, string(raw), token, "the golden never holds the SCIM token")
	require.NotContains(t, string(raw), strings.TrimPrefix(token, "scim_"))
	want := scimCycleLoad(t)
	require.Equal(t, scimCycleStepNames, scimCycleNames(want), "the fixed step list of the golden")
	if diffs := scimCycleDiff(want, got); len(diffs) > 0 {
		t.Fatalf("the cycle differs from %s:\n%s", scimCyclePath, strings.Join(diffs, "\n"))
	}
}

func TestSCIMProvisioningCycleGoldenCompare(t *testing.T) {
	committed := scimCycleLoad(t)
	require.Equal(t, scimCycleStepNames, scimCycleNames(committed))
	clone := func(t *testing.T) scimCycleGolden {
		t.Helper()
		var copied scimCycleGolden
		require.NoError(t, json.Unmarshal(scimCycleMarshal(t, committed), &copied))
		return copied
	}
	stepIndex := func(t *testing.T, golden scimCycleGolden, name string) int {
		t.Helper()
		for i, step := range golden.Steps {
			if step.Name == name {
				return i
			}
		}
		require.FailNow(t, "no such step", name)
		return -1
	}
	assertNamed := func(t *testing.T, diffs []string, step string) {
		t.Helper()
		require.NotEmpty(t, diffs, "the change is reported")
		for _, diff := range diffs {
			assert.Contains(t, diff, "step "+step+":", "the difference names the step")
		}
	}

	t.Run("changed_status", func(t *testing.T) {
		altered := clone(t)
		i := stepIndex(t, altered, "groups_delete")
		altered.Steps[i].Status = http.StatusNoContent
		assertNamed(t, scimCycleDiff(committed, altered), "groups_delete")
	})
	t.Run("removed_body_field", func(t *testing.T) {
		altered := clone(t)
		i := stepIndex(t, altered, "users_get")
		body, ok := altered.Steps[i].Body.(map[string]interface{})
		require.True(t, ok, "users_get answers a JSON object")
		require.Contains(t, body, "userName")
		delete(body, "userName")
		assertNamed(t, scimCycleDiff(committed, altered), "users_get")
	})
	t.Run("changed_header", func(t *testing.T) {
		altered := clone(t)
		i := stepIndex(t, altered, "users_create")
		altered.Steps[i].Location = "https://crm.example.test/scim/v2/Users/0"
		assertNamed(t, scimCycleDiff(committed, altered), "users_create")
	})
	t.Run("unaltered", func(t *testing.T) {
		assert.Empty(t, scimCycleDiff(committed, clone(t)))
	})
}
