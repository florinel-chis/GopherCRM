// Command apiroutes lists the HTTP routes the API registers by reading the
// router setup source with go/ast. Nothing is built or started, so the list
// can be compared with the OpenAPI spec in CI.
//
// The walk starts at setupRouter in cmd/ and follows calls into functions of
// the cmd and internal/handler packages that receive a router group, tracking
// each group's prefix through Group calls and plain aliases. Paths must be
// string literals; the only accepted non-literal prefix is the API base path
// (cfg.API.Prefix), and routes under it are reported relative to it, as the
// spec's paths are. Registration forms the walk cannot resolve (Any, Handle,
// Match, Static*, a non-literal path, a registration the walk never reaches)
// are errors rather than silent omissions.
//
// Each route also carries the handler it registers (resolved through the
// typed parameters of the setup functions and the constructor calls in cmd)
// and the status codes its route middleware can answer with (401 for
// middleware.Auth, 403 for the role and registration guards, 429 for the
// rate limiters).
//
// It also reads, for every registered handler, which query parameters and
// form fields it reads and which body type it binds, following the helpers it
// hands the *gin.Context to (see inputs.go), so the gate can compare them
// with the documented parameters of that route's operation.
//
// Usage: go run ./scripts/ci/apiroutes [-root DIR]
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Route is one registered method and path.
type Route struct {
	Method string `json:"method"`
	// Path uses the spec's {param} form. Under the base path it is relative
	// to it; otherwise it is the absolute path.
	Path string `json:"path"`
	// Base reports whether the route sits under the API base path.
	Base bool `json:"base"`
	// Auth reports whether the authentication middleware (middleware.Auth)
	// runs before the handler: installed with Use on the route's group or an
	// ancestor before the group was derived, or passed to Group or to the
	// registration itself.
	Auth bool `json:"auth"`
	// Handler identifies the registered handler as "pkg.Type.Method" or
	// "pkg.Function" (a handler value or a factory call); empty when it is a
	// function literal.
	Handler string `json:"handler"`
	// Responses lists the status codes the route middleware can answer with
	// before the handler runs, sorted.
	Responses []int  `json:"responses"`
	File      string `json:"file"`
	Line      int    `json:"line"`
}

// Source is one Go package directory the walk may enter.
type Source struct {
	Package string // the package name used to qualify calls, e.g. "handler"
	Dir     string // relative to the root
}

// DefaultSources are the packages that register routes today. A router
// group handed to a function outside them (or to a method) is an error, so
// a registration helper added elsewhere cannot hide its routes.
var DefaultSources = []Source{{"main", "cmd"}, {"handler", "internal/handler"}}

const (
	entryFunction = "main.setupRouter"
	baseExpr      = "cfg.API.Prefix"
	// authMiddleware is the call that installs authentication.
	authMiddleware = "middleware.Auth"
)

// middlewareResponses maps the route middleware the API uses to the status
// code each one answers with when it stops a request.
var middlewareResponses = map[string]int{
	authMiddleware:                          401,
	"middleware.RequireRole":                403,
	"middleware.RequireRegistrationEnabled": 403,
	"middleware.RateLimitStrict":            429,
	"middleware.RateLimitModerate":          429,
	"middleware.RateLimitGenerous":          429,
}

var routeMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true, "OPTIONS": true,
}

var unsupportedMethods = map[string]bool{
	"Any": true, "Handle": true, "Match": true, "Static": true, "StaticFS": true, "StaticFile": true,
	"StaticFileFS": true, "NoRoute": true, "NoMethod": true,
}

type group struct {
	prefix string
	base   bool
	auth   bool
	codes  []int // middleware status codes, sorted; never mutated in place
}

// scope tracks, per walked function, the router group variables, the
// variables holding a handler (by qualified type) and the variables holding
// a route middleware (by its constructor, e.g. "middleware.RequireRole").
type scope struct {
	groups   map[string]group
	handlers map[string]string
	mw       map[string]string
}

func newScope() scope {
	return scope{groups: map[string]group{}, handlers: map[string]string{}, mw: map[string]string{}}
}

func (s scope) forget(name string) {
	delete(s.groups, name)
	delete(s.handlers, name)
	delete(s.mw, name)
}

// withCodes returns codes plus extra, sorted and unique, as a new slice.
func withCodes(codes []int, extra ...int) []int {
	seen := map[int]bool{}
	var out []int
	for _, c := range append(append([]int{}, codes...), extra...) {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Ints(out)
	return out
}

type extractor struct {
	root     string
	fset     *token.FileSet
	funcs    map[string]*ast.FuncDecl
	pkgOf    map[*ast.FuncDecl]string
	calls    map[token.Pos]bool // registration-shaped calls in the sources
	visited  map[token.Pos]bool
	active   map[string]bool
	routes   []Route
	problems []string
}

// Extract walks the sources under root and returns the routes, sorted by
// path then method, or every problem found.
func Extract(root string, sources []Source) ([]Route, error) {
	x := &extractor{
		root: root, fset: token.NewFileSet(), funcs: map[string]*ast.FuncDecl{},
		pkgOf: map[*ast.FuncDecl]string{}, calls: map[token.Pos]bool{},
		visited: map[token.Pos]bool{}, active: map[string]bool{},
	}
	for _, src := range sources {
		if err := x.load(src); err != nil {
			return nil, err
		}
	}
	entry, ok := x.funcs[entryFunction]
	if !ok {
		return nil, fmt.Errorf("entry function %s not found", entryFunction)
	}
	x.walkFunc(entryFunction, entry, newScope())
	var unreached []token.Pos
	for pos := range x.calls {
		if !x.visited[pos] {
			unreached = append(unreached, pos)
		}
	}
	sort.Slice(unreached, func(i, j int) bool { return unreached[i] < unreached[j] })
	for _, pos := range unreached {
		x.problem(pos, "route registration not reached from %s (unsupported form or unrouted code)", entryFunction)
	}
	seen := map[string]bool{}
	for _, r := range x.routes {
		key := fmt.Sprintf("%s %t %s", r.Method, r.Base, r.Path)
		if seen[key] {
			x.problems = append(x.problems, fmt.Sprintf("%s:%d: duplicate route %s %s", r.File, r.Line, r.Method, r.Path))
		}
		seen[key] = true
	}
	if len(x.problems) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(x.problems, "\n"))
	}
	sort.Slice(x.routes, func(i, j int) bool {
		a, b := x.routes[i], x.routes[j]
		if a.Base != b.Base {
			return a.Base
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Method < b.Method
	})
	return x.routes, nil
}

func (x *extractor) load(src Source) error {
	dir := filepath.Join(x.root, src.Dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(x.fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return err
		}
		if file.Name.Name != src.Package {
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Recv != nil {
				continue
			}
			key := src.Package + "." + fn.Name.Name
			x.funcs[key] = fn
			x.pkgOf[fn] = src.Package
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if routeMethods[sel.Sel.Name] || unsupportedMethods[sel.Sel.Name] {
					x.calls[call.Pos()] = true
				}
			}
			return true
		})
	}
	return nil
}

func (x *extractor) problem(pos token.Pos, format string, args ...interface{}) {
	p := x.fset.Position(pos)
	file, err := filepath.Rel(x.root, p.Filename)
	if err != nil {
		file = p.Filename
	}
	x.problems = append(x.problems, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(file), p.Line, fmt.Sprintf(format, args...)))
}

func render(n ast.Node) string {
	var b bytes.Buffer
	if err := format.Node(&b, token.NewFileSet(), n); err != nil {
		return ""
	}
	return b.String()
}

func (x *extractor) walkFunc(key string, fn *ast.FuncDecl, s scope) {
	if x.active[key] {
		x.problem(fn.Pos(), "recursive route setup through %s", key)
		return
	}
	x.active[key] = true
	defer delete(x.active, key)
	pkg := x.pkgOf[fn]
	for _, field := range fn.Type.Params.List {
		typ := qualifiedPointer(field.Type, pkg)
		if typ == "" {
			continue
		}
		for _, name := range field.Names {
			if _, isGroup := s.groups[name.Name]; !isGroup {
				s.handlers[name.Name] = typ
			}
		}
	}
	x.walkStmts(fn.Body.List, s, pkg)
}

// qualifiedPointer names the type behind *T or *pkg.T as "pkg.T" (T is
// qualified with the package the expression appears in), or "" for any
// other type expression.
func qualifiedPointer(expr ast.Expr, pkg string) string {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return ""
	}
	switch t := star.X.(type) {
	case *ast.Ident:
		return pkg + "." + t.Name
	case *ast.SelectorExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name + "." + t.Sel.Name
		}
	}
	return ""
}

// bindValue records what an assigned value is: a router group, a handler
// built by a constructor of a walked package, or a route middleware. It
// reports whether the value was a group (the caller then skips walking it).
func (x *extractor) bindValue(name string, value ast.Expr, s scope, pkg string) bool {
	s.forget(name)
	if g, ok := x.groupOf(value, s, pkg); ok {
		s.groups[name] = g
		return true
	}
	call, ok := unparen(value).(*ast.CallExpr)
	if !ok {
		return false
	}
	if _, ok := middlewareResponses[render(call.Fun)]; ok {
		s.mw[name] = render(call.Fun)
		return false
	}
	if key, ok := x.funcKey(call.Fun, pkg); ok {
		fn := x.funcs[key]
		if fn.Type.Results != nil && len(fn.Type.Results.List) == 1 {
			if typ := qualifiedPointer(fn.Type.Results.List[0].Type, x.pkgOf[fn]); typ != "" {
				s.handlers[name] = typ
			}
		}
	}
	return false
}

// funcKey resolves a function expression to a loaded function: F in the
// current package or pkg.F.
func (x *extractor) funcKey(expr ast.Expr, pkg string) (string, bool) {
	switch f := unparen(expr).(type) {
	case *ast.Ident:
		key := pkg + "." + f.Name
		_, ok := x.funcs[key]
		return key, ok
	case *ast.SelectorExpr:
		if id, ok := f.X.(*ast.Ident); ok {
			key := id.Name + "." + f.Sel.Name
			_, ok := x.funcs[key]
			return key, ok
		}
	}
	return "", false
}

func (x *extractor) walkStmts(stmts []ast.Stmt, s scope, pkg string) {
	for _, stmt := range stmts {
		x.walkStmt(stmt, s, pkg)
	}
}

func (x *extractor) walkStmt(stmt ast.Stmt, s scope, pkg string) {
	switch st := stmt.(type) {
	case *ast.BlockStmt:
		x.walkStmts(st.List, s, pkg)
	case *ast.ExprStmt:
		x.walkExpr(st.X, s, pkg)
	case *ast.AssignStmt:
		for i, rhs := range st.Rhs {
			if len(st.Lhs) == len(st.Rhs) {
				if id, ok := st.Lhs[i].(*ast.Ident); ok {
					if x.bindValue(id.Name, rhs, s, pkg) {
						continue
					}
				}
			}
			x.walkExpr(rhs, s, pkg)
		}
	case *ast.DeclStmt:
		gen, ok := st.Decl.(*ast.GenDecl)
		if !ok {
			return
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, value := range vs.Values {
				if len(vs.Names) == len(vs.Values) && x.bindValue(vs.Names[i].Name, value, s, pkg) {
					continue
				}
				x.walkExpr(value, s, pkg)
			}
		}
	case *ast.IfStmt:
		if st.Init != nil {
			x.walkStmt(st.Init, s, pkg)
		}
		x.walkStmts(st.Body.List, s, pkg)
		if st.Else != nil {
			x.walkStmt(st.Else, s, pkg)
		}
	case *ast.ReturnStmt:
		for _, result := range st.Results {
			x.walkExpr(result, s, pkg)
		}
	case *ast.DeferStmt:
		x.walkExpr(st.Call, s, pkg)
	case *ast.GoStmt:
		x.walkExpr(st.Call, s, pkg)
	}
}

// groupOf resolves an expression to a router group: a tracked identifier,
// gin.New()/gin.Default(), or a Group call on a tracked group.
func (x *extractor) groupOf(expr ast.Expr, s scope, pkg string) (group, bool) {
	switch e := expr.(type) {
	case *ast.Ident:
		g, ok := s.groups[e.Name]
		return g, ok
	case *ast.ParenExpr:
		return x.groupOf(e.X, s, pkg)
	case *ast.CallExpr:
		sel, ok := e.Fun.(*ast.SelectorExpr)
		if !ok {
			return group{}, false
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == "gin" && (sel.Sel.Name == "New" || sel.Sel.Name == "Default") {
			return group{}, true
		}
		if sel.Sel.Name != "Group" {
			return group{}, false
		}
		parent, ok := x.groupOf(sel.X, s, pkg)
		if !ok || len(e.Args) == 0 {
			return group{}, false
		}
		x.walkCallArgs(e, s, pkg)
		mw := middlewareNames(e.Args[1:], s)
		auth := parent.auth || hasAuth(mw)
		codes := withCodes(parent.codes, codesOf(mw)...)
		if lit, ok := stringLiteral(e.Args[0]); ok {
			return group{prefix: parent.prefix + lit, base: parent.base, auth: auth, codes: codes}, true
		}
		if render(e.Args[0]) == baseExpr && !parent.base && parent.prefix == "" {
			return group{base: true, auth: auth, codes: codes}, true
		}
		x.problem(e.Pos(), "group prefix %q is not a string literal", render(e.Args[0]))
		return parent, true
	}
	return group{}, false
}

// middlewareNames lists the known route middleware among handler arguments:
// a direct constructor call or a variable that holds one.
func middlewareNames(args []ast.Expr, s scope) []string {
	var names []string
	for _, arg := range args {
		switch a := unparen(arg).(type) {
		case *ast.CallExpr:
			if _, ok := middlewareResponses[render(a.Fun)]; ok {
				names = append(names, render(a.Fun))
			}
		case *ast.Ident:
			if name, ok := s.mw[a.Name]; ok {
				names = append(names, name)
			}
		}
	}
	return names
}

func hasAuth(names []string) bool {
	for _, name := range names {
		if name == authMiddleware {
			return true
		}
	}
	return false
}

func codesOf(names []string) []int {
	var codes []int
	for _, name := range names {
		codes = append(codes, middlewareResponses[name])
	}
	return codes
}

func stringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	return value, err == nil
}

func (x *extractor) walkCallArgs(call *ast.CallExpr, s scope, pkg string) {
	for _, arg := range call.Args {
		if inner, ok := arg.(*ast.CallExpr); ok {
			x.walkExpr(inner, s, pkg)
		}
	}
}

func (x *extractor) walkExpr(expr ast.Expr, s scope, pkg string) {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return
	}
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		if g, ok := x.groupOf(fun.X, s, pkg); ok {
			if fun.Sel.Name == "Use" {
				x.use(fun.X, call, g, s, pkg)
				return
			}
			x.registration(call, fun.Sel.Name, g, s, pkg)
			return
		}
		if id, ok := fun.X.(*ast.Ident); ok {
			if fn, ok := x.funcs[id.Name+"."+fun.Sel.Name]; ok {
				x.follow(id.Name+"."+fun.Sel.Name, fn, call, s, pkg)
				return
			}
		}
	case *ast.Ident:
		if fn, ok := x.funcs[pkg+"."+fun.Name]; ok {
			x.follow(pkg+"."+fun.Name, fn, call, s, pkg)
			return
		}
	}
	for _, arg := range call.Args {
		if _, isGroup := x.groupOf(arg, s, pkg); isGroup {
			x.problem(call.Pos(), "router group %s is passed to %s, which the route walk cannot follow "+
				"(only functions of the cmd and internal/handler packages are followed)", render(arg), render(call.Fun))
		}
	}
	x.walkCallArgs(call, s, pkg)
}

func unparen(expr ast.Expr) ast.Expr {
	for {
		p, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = p.X
	}
}

// use records route middleware installed on a tracked group variable; it
// applies to the routes registered on that group afterwards and to the
// groups derived from it afterwards.
func (x *extractor) use(target ast.Expr, call *ast.CallExpr, g group, s scope, pkg string) {
	x.walkCallArgs(call, s, pkg)
	mw := middlewareNames(call.Args, s)
	if len(mw) == 0 {
		return
	}
	id, ok := unparen(target).(*ast.Ident)
	if !ok {
		x.problem(call.Pos(), "route middleware installed on %s, which is not a group variable", render(target))
		return
	}
	g.auth = g.auth || hasAuth(mw)
	g.codes = withCodes(g.codes, codesOf(mw)...)
	s.groups[id.Name] = g
}

// handlerOf identifies the handler argument of a registration: a method
// value on a variable whose type is known, a function value, or a call of a
// handler factory. It returns "" for anything else (a function literal).
func (x *extractor) handlerOf(expr ast.Expr, s scope, pkg string) string {
	switch h := unparen(expr).(type) {
	case *ast.SelectorExpr:
		if id, ok := h.X.(*ast.Ident); ok {
			if typ, ok := s.handlers[id.Name]; ok {
				return typ + "." + h.Sel.Name
			}
		}
		if key, ok := x.funcKey(h, pkg); ok {
			return key
		}
	case *ast.Ident:
		if key, ok := x.funcKey(h, pkg); ok {
			return key
		}
	case *ast.CallExpr:
		if key, ok := x.funcKey(h.Fun, pkg); ok {
			return key
		}
	}
	return ""
}

func (x *extractor) registration(call *ast.CallExpr, method string, g group, s scope, pkg string) {
	switch {
	case routeMethods[method]:
		x.visited[call.Pos()] = true
		if len(call.Args) < 2 {
			x.problem(call.Pos(), "%s registration without a handler", method)
			return
		}
		path, ok := stringLiteral(call.Args[0])
		if !ok {
			x.problem(call.Pos(), "%s path %q is not a string literal", method, render(call.Args[0]))
			return
		}
		p := x.fset.Position(call.Pos())
		file, err := filepath.Rel(x.root, p.Filename)
		if err != nil {
			file = p.Filename
		}
		last := call.Args[len(call.Args)-1]
		mw := middlewareNames(call.Args[1:len(call.Args)-1], s)
		handler := x.handlerOf(last, s, pkg)
		if handler == "" && g.base {
			x.problem(call.Pos(), "%s %s: cannot identify the handler %q (use a method value on a typed handler "+
				"variable, a function, or a factory call)", method, path, render(last))
		}
		codes := withCodes(g.codes, codesOf(mw)...)
		if codes == nil {
			codes = []int{}
		}
		x.routes = append(x.routes, Route{
			Method: method, Path: specPath(g.prefix + path), Base: g.base,
			Auth: g.auth || hasAuth(mw), Handler: handler, Responses: codes,
			File: filepath.ToSlash(file), Line: p.Line,
		})
	case unsupportedMethods[method]:
		x.visited[call.Pos()] = true
		x.problem(call.Pos(), "unsupported route registration form %s", method)
	}
}

// follow walks into a setup function, binding each parameter that receives
// a router group. A call that passes no group cannot register routes on one
// of ours and is skipped; registrations it holds would surface as unreached.
func (x *extractor) follow(key string, fn *ast.FuncDecl, call *ast.CallExpr, s scope, pkg string) {
	inner := newScope()
	var names []string
	for _, field := range fn.Type.Params.List {
		if len(field.Names) == 0 {
			names = append(names, "_")
		}
		for _, name := range field.Names {
			names = append(names, name.Name)
		}
	}
	bound := false
	for i, arg := range call.Args {
		if i >= len(names) {
			break
		}
		if g, ok := x.groupOf(arg, s, pkg); ok {
			inner.groups[names[i]] = g
			bound = true
		}
	}
	if !bound {
		x.walkCallArgs(call, s, pkg)
		return
	}
	x.walkFunc(key, fn, inner)
}

// specPath converts Gin's :param and *param segments to {param}.
func specPath(path string) string {
	if path == "" {
		return "/"
	}
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if strings.HasPrefix(segment, ":") || strings.HasPrefix(segment, "*") {
			segments[i] = "{" + segment[1:] + "}"
		}
	}
	return strings.Join(segments, "/")
}

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	routes, err := Extract(*root, DefaultSources)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	inputs, err := ExtractInputs(*root, InputSources, routes)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	out, err := json.MarshalIndent(map[string]interface{}{"routes": routes, "operations": inputs}, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(string(out))
}
