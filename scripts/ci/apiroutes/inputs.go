package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Inputs is what one route's handler reads from the request, found by
// reading the handler the router registers for it (which must carry the
// route's @Router annotation) and the helpers it hands the *gin.Context to.
type Inputs struct {
	Method string `json:"method"`
	// Path is the @Router path, relative to the base path, in {param} form.
	Path string `json:"path"`
	// Query lists the query parameters read (Query, DefaultQuery, GetQuery,
	// QueryArray, GetQueryArray).
	Query []string `json:"query"`
	// Form lists the urlencoded form fields read (PostForm, DefaultPostForm,
	// GetPostForm).
	Form []string `json:"form"`
	// Body reports whether a request body is bound (ShouldBindJSON,
	// BindJSON, ShouldBind, Bind, ShouldBindWith, ShouldBindBodyWith).
	Body bool `json:"body"`
	// BodyTypes lists the Go types bound, qualified ("handler.CreateTaskRequest").
	BodyTypes []string `json:"body_types"`
	// BodyRequired reports whether every bind is the handler's own top-level
	// `if err := c.ShouldBindJSON(&req); err != nil { ...; return }` (or the
	// same split over two statements), so a missing body is refused. A bind
	// nested under a condition (the content length) or whose error is ignored
	// makes the body optional; an error branch that can fall through is an
	// extraction error.
	BodyRequired bool `json:"body_required"`
	// Handler is the registered handler (see Route.Handler).
	Handler string `json:"handler"`
	File    string `json:"file"`
	Line    int    `json:"line"`
}

// InputSources are the packages whose functions the input walk may enter:
// the handlers and the shared request helpers (utils.ParseOffsetLimit).
var InputSources = []Source{{"handler", "internal/handler"}, {"utils", "internal/utils"}}

var routerLine = regexp.MustCompile(`^@Router\s+(\S+)\s+\[(\w+)\]`)

var (
	queryReaders = map[string]bool{"Query": true, "DefaultQuery": true, "GetQuery": true, "QueryArray": true, "GetQueryArray": true}
	formReaders  = map[string]bool{"PostForm": true, "DefaultPostForm": true, "GetPostForm": true}
	bodyBinders  = map[string]bool{
		"ShouldBindJSON": true, "BindJSON": true, "ShouldBind": true, "Bind": true, "ShouldBindWith": true,
		"ShouldBindBodyWith": true,
	}
	// Readers the walk does not model; using one is an error so that an
	// input cannot slip past the documentation check.
	unmodelledReaders = map[string]bool{
		"QueryMap": true, "GetQueryMap": true, "PostFormArray": true, "GetPostFormArray": true, "PostFormMap": true,
		"GetPostFormMap": true, "ShouldBindQuery": true, "BindQuery": true, "ShouldBindUri": true, "BindUri": true,
		"ShouldBindHeader": true, "BindHeader": true, "FormFile": true, "MultipartForm": true, "GetRawData": true,
		"ShouldBindXML": true, "BindXML": true, "ShouldBindYAML": true, "BindYAML": true, "ShouldBindTOML": true,
		"BindTOML": true,
	}
)

type inputWalker struct {
	root     string
	fset     *token.FileSet
	funcs    map[string]*ast.FuncDecl // "pkg.Name" and "pkg.Recv.Name"
	keyOf    map[*ast.FuncDecl]string
	pkgOf    map[*ast.FuncDecl]string
	active   map[*ast.FuncDecl]bool
	problems []string
}

type inputSet struct {
	query     map[string]bool
	form      map[string]bool
	body      bool
	bodyTypes map[string]bool
	optional  bool // some bind is not the required top-level shape
}

// routerAnnotations lists the "METHOD path" pairs of a function's @Router lines.
func routerAnnotations(fn *ast.FuncDecl) []string {
	if fn.Doc == nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(fn.Doc.Text(), "\n") {
		if m := routerLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			out = append(out, strings.ToUpper(m[2])+" "+m[1])
		}
	}
	return out
}

// ExtractInputs lists the inputs of the handler registered for every route
// under the base path, sorted by path then method. The handler must carry
// the route's @Router annotation, and an @Router annotation of a routed
// method and path must sit on the handler registered for it, so the inputs
// always come from the code that serves the route.
func ExtractInputs(root string, sources []Source, routes []Route) ([]Inputs, error) {
	w := &inputWalker{
		root: root, fset: token.NewFileSet(), funcs: map[string]*ast.FuncDecl{}, keyOf: map[*ast.FuncDecl]string{},
		pkgOf: map[*ast.FuncDecl]string{}, active: map[*ast.FuncDecl]bool{},
	}
	var annotated []*ast.FuncDecl
	for _, src := range sources {
		found, err := w.load(src)
		if err != nil {
			return nil, err
		}
		annotated = append(annotated, found...)
	}
	handlerOf := map[string]string{}
	var result []Inputs
	for _, route := range routes {
		if !route.Base {
			continue
		}
		key := route.Method + " " + route.Path
		handlerOf[key] = route.Handler
		fn := w.funcs[route.Handler]
		if fn == nil {
			w.problems = append(w.problems, fmt.Sprintf("%s:%d: %s is handled by %s, which the API docs input "+
				"check cannot find in the handler package", route.File, route.Line, key, route.Handler))
			continue
		}
		annotatedHere := false
		for _, a := range routerAnnotations(fn) {
			annotatedHere = annotatedHere || a == key
		}
		if !annotatedHere {
			w.problem(fn.Pos(), "%s is handled by %s, which has no @Router %s [%s] annotation",
				key, route.Handler, route.Path, strings.ToLower(route.Method))
			continue
		}
		set := inputSet{query: map[string]bool{}, form: map[string]bool{}, bodyTypes: map[string]bool{}}
		w.walk(fn, nil, contextParams(fn.Type, nil), &set)
		p := w.fset.Position(fn.Pos())
		file, err := filepath.Rel(w.root, p.Filename)
		if err != nil {
			file = p.Filename
		}
		result = append(result, Inputs{
			Method: route.Method, Path: route.Path, Query: keys(set.query), Form: keys(set.form),
			Body: set.body, BodyTypes: keys(set.bodyTypes), BodyRequired: set.body && !set.optional,
			Handler: route.Handler, File: filepath.ToSlash(file), Line: p.Line,
		})
	}
	for _, fn := range annotated {
		for _, a := range routerAnnotations(fn) {
			if h, routed := handlerOf[a]; routed && h != w.keyOf[fn] {
				w.problem(fn.Pos(), "@Router for %s sits on %s, but the router registers %s for it",
					a, w.keyOf[fn], h)
			}
		}
	}
	if len(w.problems) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(w.problems, "\n"))
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Path != result[j].Path {
			return result[i].Path < result[j].Path
		}
		return result[i].Method < result[j].Method
	})
	return result, nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (w *inputWalker) load(src Source) ([]*ast.FuncDecl, error) {
	dir := filepath.Join(w.root, src.Dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var annotated []*ast.FuncDecl
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(w.fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		if file.Name.Name != src.Package {
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			key := src.Package + "." + fn.Name.Name
			if recv := receiverType(fn); recv != "" {
				key = src.Package + "." + recv + "." + fn.Name.Name
			}
			w.funcs[key] = fn
			w.keyOf[fn] = key
			w.pkgOf[fn] = src.Package
			if fn.Doc != nil && strings.Contains(fn.Doc.Text(), "@Router") {
				annotated = append(annotated, fn)
			}
		}
	}
	return annotated, nil
}

func receiverType(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	t := fn.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func receiverName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 || len(fn.Recv.List[0].Names) == 0 {
		return ""
	}
	return fn.Recv.List[0].Names[0].Name
}

func isGinContext(expr ast.Expr) bool {
	star, ok := expr.(*ast.StarExpr)
	return ok && render(star.X) == "gin.Context"
}

// contextParams adds the names of the *gin.Context parameters of a function
// type to names.
func contextParams(ft *ast.FuncType, names map[string]bool) map[string]bool {
	if names == nil {
		names = map[string]bool{}
	}
	for _, field := range ft.Params.List {
		if isGinContext(field.Type) {
			for _, name := range field.Names {
				names[name.Name] = true
			}
		}
	}
	return names
}

func (w *inputWalker) problem(pos token.Pos, format string, args ...interface{}) {
	p := w.fset.Position(pos)
	file, err := filepath.Rel(w.root, p.Filename)
	if err != nil {
		file = p.Filename
	}
	w.problems = append(w.problems, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(file), p.Line, fmt.Sprintf(format, args...)))
}

// walk collects the inputs read in fn. ctx names its *gin.Context
// parameters (function literals inside fn that take one add theirs); bound
// maps fn's string parameters to the literals the caller passed.
func (w *inputWalker) walk(fn *ast.FuncDecl, bound map[string]string, ctx map[string]bool, set *inputSet) {
	if w.active[fn] {
		return
	}
	w.active[fn] = true
	defer delete(w.active, fn)
	pkg := w.pkgOf[fn]
	required, unmodelled := requiredBinds(fn.Body, ctx)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			contextParams(node.Type, ctx)
		case *ast.CallExpr:
			w.call(fn, pkg, node, bound, ctx, set, required, unmodelled)
		}
		return true
	})
}

// requiredBinds finds the binds written as a top-level statement of body in
// the shape `if err := ctx.Bind...(&v); err != nil { ...; return }`, or split
// as `err := ctx.Bind...(&v)` directly followed by `if err != nil { ...; return }`:
// the bind error, an empty body included, always ends the request. A bind in
// that shape whose error branch can fall through (see refusesAndReturns) is
// reported in unmodelled, because the check could not tell whether a missing
// body is refused.
func requiredBinds(body *ast.BlockStmt, ctx map[string]bool) (found, unmodelled map[token.Pos]bool) {
	found, unmodelled = map[token.Pos]bool{}, map[token.Pos]bool{}
	bindCall := func(assign *ast.AssignStmt) (string, *ast.CallExpr, bool) {
		if len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return "", nil, false
		}
		errName, ok := assign.Lhs[0].(*ast.Ident)
		call, isCall := assign.Rhs[0].(*ast.CallExpr)
		if !ok || !isCall {
			return "", nil, false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !bodyBinders[sel.Sel.Name] {
			return "", nil, false
		}
		if id, ok := sel.X.(*ast.Ident); !ok || !ctx[id.Name] {
			return "", nil, false
		}
		return errName.Name, call, true
	}
	classify := func(call *ast.CallExpr, branch *ast.BlockStmt) {
		if refusesAndReturns(branch) {
			found[call.Pos()] = true
		} else {
			unmodelled[call.Pos()] = true
		}
	}
	for i, stmt := range body.List {
		switch st := stmt.(type) {
		case *ast.IfStmt:
			assign, ok := st.Init.(*ast.AssignStmt)
			if !ok {
				continue
			}
			if errName, call, ok := bindCall(assign); ok && render(st.Cond) == errName+" != nil" && st.Else == nil {
				classify(call, st.Body)
			}
		case *ast.AssignStmt:
			errName, call, ok := bindCall(st)
			if !ok || i+1 >= len(body.List) {
				continue
			}
			next, ok := body.List[i+1].(*ast.IfStmt)
			if ok && next.Init == nil && next.Else == nil && render(next.Cond) == errName+" != nil" {
				classify(call, next.Body)
			}
		}
	}
	return found, unmodelled
}

// refusesAndReturns reports whether a bind error branch ends in a return and
// holds no goto, break or continue, so every path through it ends the
// request. A branch that only returns under a nested condition (an io.EOF
// exemption) does not end in a return and fails.
func refusesAndReturns(branch *ast.BlockStmt) bool {
	if len(branch.List) == 0 {
		return false
	}
	if _, ok := branch.List[len(branch.List)-1].(*ast.ReturnStmt); !ok {
		return false
	}
	branching := false
	ast.Inspect(branch, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.BranchStmt:
			branching = true
		}
		return !branching
	})
	return !branching
}

// boundType names the type of the variable a bind fills (`&req`), qualified
// with pkg for a local type, from its declaration in fn.
func boundType(fn *ast.FuncDecl, pkg string, call *ast.CallExpr) (string, bool) {
	if len(call.Args) == 0 {
		return "", false
	}
	addr, ok := call.Args[0].(*ast.UnaryExpr)
	if !ok || addr.Op != token.AND {
		return "", false
	}
	target, ok := addr.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	qualify := func(expr ast.Expr) (string, bool) {
		switch t := expr.(type) {
		case *ast.Ident:
			return pkg + "." + t.Name, true
		case *ast.SelectorExpr:
			if id, ok := t.X.(*ast.Ident); ok {
				return id.Name + "." + t.Sel.Name, true
			}
		}
		return "", false
	}
	var typ string
	var found bool
	declarations := 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch d := n.(type) {
		case *ast.ValueSpec:
			for _, name := range d.Names {
				if name.Name == target.Name {
					declarations++
					if d.Type != nil {
						typ, found = qualify(d.Type)
					}
				}
			}
		case *ast.AssignStmt:
			if d.Tok != token.DEFINE || len(d.Lhs) != len(d.Rhs) {
				return true
			}
			for i, lhs := range d.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name == target.Name {
					declarations++
					if lit, ok := d.Rhs[i].(*ast.CompositeLit); ok {
						typ, found = qualify(lit.Type)
					}
				}
			}
		}
		return true
	})
	// Two declarations of the name (a shadowing one in a nested block) leave
	// the bound variable ambiguous without scope resolution: refuse to guess.
	return typ, found && declarations == 1
}

func (w *inputWalker) call(fn *ast.FuncDecl, pkg string, call *ast.CallExpr, bound map[string]string,
	ctx map[string]bool, set *inputSet, required, unmodelled map[token.Pos]bool) {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if id, ok := sel.X.(*ast.Ident); ok && ctx[id.Name] {
			name := sel.Sel.Name
			switch {
			case queryReaders[name], formReaders[name]:
				key, ok := w.key(call, bound)
				if !ok {
					w.problem(call.Pos(), "%s.%s key %q is neither a string literal nor a parameter bound to one",
						id.Name, name, render(call.Args[0]))
					return
				}
				if queryReaders[name] {
					set.query[key] = true
				} else {
					set.form[key] = true
				}
			case bodyBinders[name]:
				set.body = true
				if unmodelled[call.Pos()] {
					w.problem(call.Pos(), "%s.%s error branch can fall through (it must end in return, without goto/break/continue); the API docs input check "+
						"cannot tell whether a missing body is refused", id.Name, name)
					return
				}
				if !required[call.Pos()] {
					set.optional = true
				}
				typ, ok := boundType(fn, pkg, call)
				if !ok {
					w.problem(call.Pos(), "%s.%s binds %q, whose declared type the API docs input check cannot read "+
						"(declare it once in the handler as var v T or v := T{})", id.Name, name, render(call.Args[0]))
					return
				}
				set.bodyTypes[typ] = true
			case unmodelledReaders[name]:
				w.problem(call.Pos(), "request reader %s is not modelled by the API docs input check", name)
			}
			return
		}
	}
	// A call that hands the context on: follow it when the callee is known.
	position := -1
	for i, arg := range call.Args {
		if id, ok := unparen(arg).(*ast.Ident); ok && ctx[id.Name] {
			position = i
			break
		}
	}
	if position < 0 {
		return
	}
	callee := w.callee(fn, pkg, call)
	if callee == nil {
		w.problem(call.Pos(), "the request context is passed to %s, which the API docs input check cannot follow",
			render(call.Fun))
		return
	}
	names := paramNames(callee.Type)
	if position >= len(names) {
		return
	}
	inner := map[string]string{}
	for i, arg := range call.Args {
		if i >= len(names) {
			break
		}
		if lit, ok := stringLiteral(arg); ok {
			inner[names[i]] = lit
		} else if id, ok := arg.(*ast.Ident); ok {
			if value, ok := bound[id.Name]; ok {
				inner[names[i]] = value
			}
		}
	}
	w.walk(callee, inner, map[string]bool{names[position]: true}, set)
}

func (w *inputWalker) key(call *ast.CallExpr, bound map[string]string) (string, bool) {
	if len(call.Args) == 0 {
		return "", false
	}
	if lit, ok := stringLiteral(call.Args[0]); ok {
		return lit, true
	}
	if id, ok := call.Args[0].(*ast.Ident); ok {
		value, ok := bound[id.Name]
		return value, ok
	}
	return "", false
}

// callee resolves a function of a walked package: a plain call in the same
// package, a package-qualified call, or a method called on fn's receiver.
func (w *inputWalker) callee(fn *ast.FuncDecl, pkg string, call *ast.CallExpr) *ast.FuncDecl {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return w.funcs[pkg+"."+fun.Name]
	case *ast.SelectorExpr:
		id, ok := fun.X.(*ast.Ident)
		if !ok {
			return nil
		}
		if recv := receiverName(fn); recv != "" && id.Name == recv {
			return w.funcs[pkg+"."+receiverType(fn)+"."+fun.Sel.Name]
		}
		return w.funcs[id.Name+"."+fun.Sel.Name]
	}
	return nil
}

func paramNames(ft *ast.FuncType) []string {
	var names []string
	for _, field := range ft.Params.List {
		if len(field.Names) == 0 {
			names = append(names, "_")
		}
		for _, name := range field.Names {
			names = append(names, name.Name)
		}
	}
	return names
}
