package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const inputsHandler = `package handler

// List godoc
// @Router /widgets [get]
func (h *WidgetHandler) List(c *gin.Context) {
	offset, limit := utils.ParseOffsetLimit(c)
	search := c.Query("search")
	colour := queryValue(c, "colour")
	h.sorted(c)
	_, _, _, _ = offset, limit, search, colour
}

// Create godoc
// @Router /widgets [post]
// @Router /widgets/draft [post]
func (h *WidgetHandler) Create(c *gin.Context) {
	var req CreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return
	}
	logStart(c, "Create")
}

// Patch godoc
// @Router /widgets/patch [post]
func (h *WidgetHandler) Patch(c *gin.Context) {
	var req PatchRequest
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			return
		}
	}
}

// Confirm godoc
// @Router /widgets/confirm [post]
func Confirm(svc Service) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		_ = ctx.PostForm("token")
	}
}

func (h *WidgetHandler) sorted(c *gin.Context) {
	_ = c.DefaultQuery("sort_by", "id")
}

func queryValue(c *gin.Context, name string) string {
	return c.Query(name)
}

func logStart(c *gin.Context, name string) {}
`

const inputsUtils = `package utils

func ParseOffsetLimit(c *gin.Context) (int, int) {
	_ = c.Query("offset")
	_ = c.Query("limit")
	return 0, 20
}
`

func writeInputsTree(t *testing.T, handler string) string {
	t.Helper()
	root := t.TempDir()
	for dir, src := range map[string]string{"internal/handler": handler, "internal/utils": inputsUtils} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "x.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func describe(in Inputs) string {
	out := in.Method + " " + in.Path + " q=" + strings.Join(in.Query, "|") + " f=" + strings.Join(in.Form, "|")
	if in.Body {
		out += " body=" + strings.Join(in.BodyTypes, "|") + map[bool]string{true: " required", false: " optional"}[in.BodyRequired]
	}
	return out
}

// inputRoutes registers each annotated widget operation with its handler.
func inputRoutes() []Route {
	return []Route{
		{Method: "GET", Path: "/widgets", Base: true, Handler: "handler.WidgetHandler.List"},
		{Method: "POST", Path: "/widgets", Base: true, Handler: "handler.WidgetHandler.Create"},
		{Method: "POST", Path: "/widgets/draft", Base: true, Handler: "handler.WidgetHandler.Create"},
		{Method: "POST", Path: "/widgets/patch", Base: true, Handler: "handler.WidgetHandler.Patch"},
		{Method: "POST", Path: "/widgets/confirm", Base: true, Handler: "handler.Confirm"},
		{Method: "GET", Path: "/health", Base: false},
	}
}

func TestExtractInputsFollowsHelpersAndClosures(t *testing.T) {
	inputs, err := ExtractInputs(writeInputsTree(t, inputsHandler), InputSources, inputRoutes())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, in := range inputs {
		got = append(got, describe(in))
	}
	want := []string{
		"GET /widgets q=colour|limit|offset|search|sort_by f=",
		"POST /widgets q= f= body=handler.CreateRequest required",
		"POST /widgets/confirm q= f=token",
		"POST /widgets/draft q= f= body=handler.CreateRequest required",
		"POST /widgets/patch q= f= body=handler.PatchRequest optional",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("inputs\n got: %q\nwant: %q", got, want)
	}
}

func TestExtractInputsReadsTheRegisteredHandler(t *testing.T) {
	v2 := inputsHandler + `
func (h *WidgetHandler) ListV2(c *gin.Context) {
	_ = c.Query("cursor")
}
`
	routes := inputRoutes()
	routes[0].Handler = "handler.WidgetHandler.ListV2"
	// The router now serves GET /widgets with ListV2, which carries no
	// annotation, while the old annotated List still exists.
	_, err := ExtractInputs(writeInputsTree(t, v2), InputSources, routes)
	if err == nil || !strings.Contains(err.Error(), "GET /widgets is handled by handler.WidgetHandler.ListV2, which has no @Router /widgets [get] annotation") {
		t.Fatalf("error = %v", err)
	}
	// Annotating ListV2 as well leaves the stale annotation on List.
	annotated := strings.Replace(v2, "func (h *WidgetHandler) ListV2", "// @Router /widgets [get]\nfunc (h *WidgetHandler) ListV2", 1)
	_, err = ExtractInputs(writeInputsTree(t, annotated), InputSources, routes)
	if err == nil || !strings.Contains(err.Error(), "@Router for GET /widgets sits on handler.WidgetHandler.List, but the router registers handler.WidgetHandler.ListV2") {
		t.Fatalf("error = %v", err)
	}
	// Moving the annotation reads the new handler's inputs.
	moved := strings.Replace(annotated, "// List godoc\n// @Router /widgets [get]\n", "", 1)
	inputs, err := ExtractInputs(writeInputsTree(t, moved), InputSources, routes)
	if err != nil {
		t.Fatal(err)
	}
	if got := describe(inputs[0]); got != "GET /widgets q=cursor f=" {
		t.Fatalf("inputs[0] = %s", got)
	}
	routes[0].Handler = "handler.WidgetHandler.Missing"
	if _, err := ExtractInputs(writeInputsTree(t, inputsHandler), InputSources, routes); err == nil ||
		!strings.Contains(err.Error(), "which the API docs input check cannot find") {
		t.Fatalf("error = %v", err)
	}
}

func TestExtractInputsRejectsWhatItCannotModel(t *testing.T) {
	cases := map[string]struct{ handler, want string }{
		"bound value of unknown type": {
			strings.Replace(inputsHandler, `c.ShouldBindJSON(&req)`, `c.ShouldBindJSON(&h.req)`, 1),
			"whose declared type the API docs input check cannot read",
		},
		"dynamic key": {
			strings.Replace(inputsHandler, `c.Query("search")`, `c.Query(field)`, 1),
			"is neither a string literal nor a parameter bound to one",
		},
		"unmodelled reader": {
			strings.Replace(inputsHandler, `c.Query("search")`, `c.QueryMap("search")`, 1),
			"request reader QueryMap is not modelled",
		},
		"query struct binding": {
			strings.Replace(inputsHandler, `c.ShouldBindJSON(&req)`, `c.ShouldBindQuery(&req)`, 1),
			"request reader ShouldBindQuery is not modelled",
		},
		"bind error branch that can fall through": {
			strings.Replace(inputsHandler, "\tif err := c.ShouldBindJSON(&req); err != nil {\n\t\treturn\n\t}\n\tlogStart(c, \"Create\")", `	if err := c.ShouldBindJSON(&req); err != nil {
		if !errors.Is(err, io.EOF) {
			return
		}
	}
	logStart(c, "Create")`, 1),
			"error branch can fall through",
		},
		"bound variable shadowed in a nested block": {
			strings.Replace(inputsHandler, "\tif err := c.ShouldBindJSON(&req); err != nil {\n\t\treturn\n\t}\n\tlogStart(c, \"Create\")", `	if err := c.ShouldBindJSON(&req); err != nil {
		return
	}
	if true {
		req := EventRequest{}
		_ = req
	}
	logStart(c, "Create")`, 1),
			"whose declared type the API docs input check cannot read",
		},
		"context handed to an unknown function": {
			strings.Replace(inputsHandler, `logStart(c, "Create")`, `other.Read(c)`, 1),
			"the request context is passed to other.Read",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ExtractInputs(writeInputsTree(t, tc.handler), InputSources, inputRoutes())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// The real handlers: every annotated operation is read without an error and
// a known list operation reports its pagination, sort and filter inputs.
func TestExtractInputsTreatsASplitBindAsRequired(t *testing.T) {
	split := strings.Replace(inputsHandler, "\tif err := c.ShouldBindJSON(&req); err != nil {\n\t\treturn\n\t}\n\tlogStart(c, \"Create\")",
		"\terr := c.ShouldBindJSON(&req)\n\tif err != nil {\n\t\treturn\n\t}\n\tlogStart(c, \"Create\")", 1)
	if split == inputsHandler {
		t.Fatal("fixture did not change")
	}
	inputs, err := ExtractInputs(writeInputsTree(t, split), InputSources, inputRoutes())
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range inputs {
		if in.Method == "POST" && in.Path == "/widgets" && !in.BodyRequired {
			t.Fatalf("POST /widgets body_required=false, want the split bind to count as required")
		}
	}
}

func TestExtractInputsRepositoryHandlers(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	routes, err := Extract(root, DefaultSources)
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := ExtractInputs(root, InputSources, routes)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) < 100 {
		t.Fatalf("got %d operations", len(inputs))
	}
	for _, in := range inputs {
		if in.Method == "POST" && in.Path == "/tasks" &&
			(strings.Join(in.BodyTypes, ",") != "handler.CreateTaskRequest" || !in.BodyRequired) {
			t.Errorf("POST /tasks body = %v required=%t", in.BodyTypes, in.BodyRequired)
		}
		if in.Method == "POST" && in.Path == "/auth/logout" && (!in.Body || in.BodyRequired) {
			t.Errorf("POST /auth/logout body=%t required=%t, want an optional body", in.Body, in.BodyRequired)
		}
	}
	for _, in := range inputs {
		if in.Method == "GET" && in.Path == "/tasks" {
			got := strings.Join(in.Query, ",")
			for _, want := range []string{"label_id", "limit", "offset", "page", "search", "sort_by", "sort_order"} {
				if !strings.Contains(","+got+",", ","+want+",") {
					t.Errorf("GET /tasks query %s lacks %s", got, want)
				}
			}
			return
		}
	}
	t.Fatal("GET /tasks not found")
}
