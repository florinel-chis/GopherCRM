package handler

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/permissions"
)

// The API reference describes each protected operation by the permission key
// that opens it. The route guard handler.Protected builds from
// permissions.RouteKeys answers the 403, and scripts/ci/apiroutes does not
// count that guard, so this test holds the generated spec to the table.

const apiReferencePath = "../../api/swagger.json"

// roleConditionPattern matches wording that states a system role as the
// condition of a route. Server messages quoted with a capital letter, such as
// 'Customers cannot update tickets', do not match. The [ ] keeps this line out
// of a plain grep for the phrase over the handler files.
var roleConditionPattern = regexp.MustCompile(`role[ ]is not|roles? required|caller is not (an )?admin|caller is neither|caller is a customer|caller is support|\bRoles?: |a non-admin|customers cannot|sales and customer users cannot|a (sales|support|customer) user`)

type apiReferenceResponse struct {
	Description string `json:"description"`
}

type apiReferenceOperation struct {
	Description string                          `json:"description"`
	Responses   map[string]apiReferenceResponse `json:"responses"`
}

func readAPIReference(t *testing.T) map[string]map[string]apiReferenceOperation {
	t.Helper()
	data, err := os.ReadFile(apiReferencePath)
	if err != nil {
		t.Fatalf("read %s: %v", apiReferencePath, err)
	}
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatalf("parse %s: %v", apiReferencePath, err)
	}
	ops := make(map[string]map[string]apiReferenceOperation, len(spec.Paths))
	for path, methods := range spec.Paths {
		ops[path] = make(map[string]apiReferenceOperation, len(methods))
		for method, raw := range methods {
			if method == "parameters" {
				continue
			}
			var op apiReferenceOperation
			if err := json.Unmarshal(raw, &op); err != nil {
				t.Fatalf("parse %s %s: %v", method, path, err)
			}
			ops[path][method] = op
		}
	}
	return ops
}

var routeParamPattern = regexp.MustCompile(`:([A-Za-z_]+)`)

func TestAPIReferenceNamesRouteKeys(t *testing.T) {
	spec := readAPIReference(t)

	routes := make([]string, 0, len(permissions.RouteKeys))
	for route := range permissions.RouteKeys {
		routes = append(routes, route)
	}
	sort.Strings(routes)

	checked := 0
	for _, route := range routes {
		rk := permissions.RouteKeys[route]
		method, pattern, ok := strings.Cut(route, " ")
		if !ok {
			t.Errorf("RouteKeys entry %q is not \"METHOD /path\"", route)
			continue
		}
		path := routeParamPattern.ReplaceAllString(pattern, "{$1}")
		op, ok := spec[path][strings.ToLower(method)]
		if !ok {
			t.Errorf("%s: no operation %s %s in the API reference", route, strings.ToLower(method), path)
			continue
		}
		checked++
		forbidden, ok := op.Responses["403"]
		if !ok {
			t.Errorf("%s: the operation documents no 403 response", route)
			continue
		}
		// Every Needs key of the table is one the handler refuses with 403
		// when it is missing (users.assign_role on POST /users,
		// leads.assign_owner, tasks.assign_others, tasks.view on
		// GET /deals/{id}/tasks, deals.view on GET /dashboard/pipeline), so
		// the 403 text names it as well.
		keys := append(append(append([]string{}, rk.AnyOf...), rk.AllOf...), rk.Needs...)
		for _, key := range keys {
			if !strings.Contains(forbidden.Description, key) {
				t.Errorf("%s: the 403 description does not name %s: %q", route, key, forbidden.Description)
			}
		}
	}
	if checked != 165 {
		t.Errorf("checked %d RouteKeys operations, want 165", checked)
	}

	for path, methods := range spec {
		for method, op := range methods {
			if m := roleConditionPattern.FindString(op.Description); m != "" {
				t.Errorf("%s %s: the description states a system role as the condition (%q)", method, path, m)
			}
			if m := roleConditionPattern.FindString(op.Responses["403"].Description); m != "" {
				t.Errorf("%s %s: the 403 description states a system role as the condition (%q)", method, path, m)
			}
		}
	}
}
