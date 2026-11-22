package service

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The sign-in audit writers (#549): every expression that becomes the method,
// outcome or reason of an auth_audit_log row is a constant of the registry
// in auth_audit_vocabulary.go, or a parameter or local variable whose own
// source is such a site. A variable forwarded into an AuthAuditEntry literal
// has its single-value assignments in the same function body checked (#577).
//
// The sign-in package internal/idkit/sso declares its own code constants: a
// site there passes one of them, and it counts as the registry constant of
// the same value (a value the registry lacks is a finding).

const (
	authAuditVocabularyFile = "internal/service/auth_audit_vocabulary.go"
	authAuditServiceDir     = "internal/service"
	// authAuditIDKitOIDCDir is the package of the sign-in error types.
	authAuditIDKitOIDCDir = "internal/idkit/oidc"
	// authAuditIDKitDir is the package of idkit.AuditEvent.
	authAuditIDKitDir = "internal/idkit"
	// authAuditSSODir is the sign-in package, whose own constants carry the
	// codes it decides.
	authAuditSSODir = "internal/idkit/sso"
)

// authAuditKind is the registry list a site's value must be in.
type authAuditKind string

const (
	authAuditKindMethod  authAuditKind = "method"
	authAuditKindOutcome authAuditKind = "outcome"
	authAuditKindReason  authAuditKind = "reason"
)

// authAuditSiteKinds are the writer sites the scan of the real sources must
// each match at least once.
var authAuditSiteKinds = []string{
	"authRefusal", "oidcRefuse", "oidcRefuseWith", "refuse",
	"outcomeEntry.method", "outcomeEntry.fallback",
	"AuthAuditEntry.Method", "AuthAuditEntry.Outcome",
	"service.AuthAuditEntry.Method", "service.AuthAuditEntry.Outcome", "service.AuthAuditEntry.Reason",
	"authRefusalError.reason", "idkitoidc.RefusalError.Code", "idkitoidc.UnavailableError.Code",
	"AuthAuditReason.return", "Authenticate.return", "ownerRejected",
	"AuthAuditEntry.Reason.assigned", "service.AuthAuditEntry.Reason.assigned",
	"sso.refuse", "sso.refuseWith", "sso.unavailable", "sso.auditReason.return",
	"idkit.AuditEvent.Method", "idkit.AuditEvent.Outcome", "idkit.AuditEvent.Reason",
}

// authAuditSourceFile is one parsed file and the repository-relative
// directory of its package.
type authAuditSourceFile struct {
	path string
	dir  string
	file *ast.File
}

// authAuditRegistry holds the constant names of each registry list.
type authAuditRegistry map[authAuditKind]map[string]bool

// authAuditScan is the result of one run of the checker.
type authAuditScan struct {
	findings []string
	// sites counts the matched sites per kind.
	sites map[string]int
	// used is every registered constant name some site passes.
	used map[string]bool
	// oidcCodes is the value of every constant an oidcRefuse,
	// oidcRefuseWith, sso.refuse or sso.refuseWith site passes.
	oidcCodes map[string]bool
	// declared maps every AuthAudit{Reason,Method,Outcome}* constant of the
	// service package to the file declaring it.
	declared map[string]string
}

// authAuditKindOfConst is the registry list a constant name belongs to by its
// prefix, or "" for any other name.
func authAuditKindOfConst(name string) authAuditKind {
	switch {
	case strings.HasPrefix(name, "AuthAuditReason"):
		return authAuditKindReason
	case strings.HasPrefix(name, "AuthAuditMethod"):
		return authAuditKindMethod
	case strings.HasPrefix(name, "AuthAuditOutcome"):
		return authAuditKindOutcome
	}
	return ""
}

// loadAuthAuditRegistry reads the constant names of the three lists from the
// parsed vocabulary file.
func loadAuthAuditRegistry(t *testing.T, vocabulary *ast.File) authAuditRegistry {
	t.Helper()
	lists := map[string]authAuditKind{
		"authAuditMethodList":  authAuditKindMethod,
		"authAuditOutcomeList": authAuditKindOutcome,
		"authAuditReasonList":  authAuditKindReason,
	}
	registry := authAuditRegistry{}
	for _, decl := range vocabulary.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			for i, name := range value.Names {
				kind, ok := lists[name.Name]
				if !ok || i >= len(value.Values) {
					continue
				}
				literal, ok := value.Values[i].(*ast.CompositeLit)
				require.True(t, ok, "%s is a slice literal", name.Name)
				names := map[string]bool{}
				for _, element := range literal.Elts {
					ident, ok := element.(*ast.Ident)
					require.True(t, ok, "%s lists constants only", name.Name)
					require.False(t, names[ident.Name], "%s lists %s twice", name.Name, ident.Name)
					names[ident.Name] = true
				}
				registry[kind] = names
			}
		}
	}
	for _, kind := range lists {
		require.NotEmpty(t, registry[kind], "the %s list is declared in %s", kind, authAuditVocabularyFile)
	}
	return registry
}

// parseAuthAuditSources parses every non-test Go file under internal/ and
// cmd/ of the repository.
func parseAuthAuditSources(t *testing.T, fset *token.FileSet) []authAuditSourceFile {
	t.Helper()
	root := filepath.Join("..", "..")
	var files []authAuditSourceFile
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			parsed, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			files = append(files, authAuditSourceFile{path: rel, dir: filepath.ToSlash(filepath.Dir(rel)), file: parsed})
			return nil
		})
		require.NoError(t, err)
	}
	require.NotEmpty(t, files)
	return files
}

// scanAuthAuditWriters runs the checker over files against registry.
func scanAuthAuditWriters(fset *token.FileSet, files []authAuditSourceFile, registry authAuditRegistry) authAuditScan {
	scan := authAuditScan{
		sites:     map[string]int{},
		used:      map[string]bool{},
		oidcCodes: map[string]bool{},
		declared:  map[string]string{},
	}
	// Every constant name declared in a package (at package level or inside
	// a function) and, for the service package, its string value.
	consts := map[string]map[string]bool{}
	serviceValues := map[string]string{}
	// ssoValues is the string value of every constant of the sign-in
	// package.
	ssoValues := map[string]string{}
	for _, f := range files {
		if consts[f.dir] == nil {
			consts[f.dir] = map[string]bool{}
		}
		topLevel := map[*ast.GenDecl]bool{}
		for _, decl := range f.file.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok {
				topLevel[gen] = true
			}
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			gen, ok := n.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				return true
			}
			for _, spec := range gen.Specs {
				value := spec.(*ast.ValueSpec)
				for i, name := range value.Names {
					consts[f.dir][name.Name] = true
					if f.dir == authAuditSSODir && i < len(value.Values) {
						if lit, ok := value.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							if unquoted, err := strconv.Unquote(lit.Value); err == nil {
								ssoValues[name.Name] = unquoted
							}
						}
					}
					if f.dir != authAuditServiceDir {
						continue
					}
					if i < len(value.Values) {
						if lit, ok := value.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							if unquoted, err := strconv.Unquote(lit.Value); err == nil {
								serviceValues[name.Name] = unquoted
							}
						}
					}
					kind := authAuditKindOfConst(name.Name)
					if kind == "" || !topLevel[gen] {
						continue
					}
					scan.declared[name.Name] = f.path
					if !registry[kind][name.Name] {
						scan.findings = append(scan.findings, fmt.Sprintf("%s: %s is declared but not in the %s list of %s",
							fset.Position(name.Pos()), name.Name, kind, authAuditVocabularyFile))
					}
				}
			}
			return true
		})
	}

	// registeredByValue is the registry constant of kind whose value is
	// value, or "".
	registeredByValue := func(kind authAuditKind, value string) string {
		var found []string
		for name := range registry[kind] {
			if serviceValues[name] == value {
				found = append(found, name)
			}
		}
		if len(found) != 1 {
			return ""
		}
		return found[0]
	}

	for _, f := range files {
		serviceAlias := ""
		if f.dir != authAuditServiceDir {
			for _, imp := range f.file.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				if strings.HasSuffix(path, "/"+authAuditServiceDir) {
					serviceAlias = "service"
					if imp.Name != nil {
						serviceAlias = imp.Name.Name
					}
				}
			}
		}
		// idkitOIDCAlias is the name this file imports the package of the
		// sign-in error types under, resolved from its import path only, so
		// an import of go-oidc as oidc is never mistaken for it.
		idkitOIDCAlias := ""
		for _, imp := range f.file.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasSuffix(path, "/"+authAuditIDKitOIDCDir) {
				idkitOIDCAlias = "oidc"
				if imp.Name != nil {
					idkitOIDCAlias = imp.Name.Name
				}
			}
		}

		// idkitAlias is the name this file imports the package of
		// idkit.AuditEvent under, resolved from its import path.
		idkitAlias := ""
		for _, imp := range f.file.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasSuffix(path, "/"+authAuditIDKitDir) {
				idkitAlias = "idkit"
				if imp.Name != nil {
					idkitAlias = imp.Name.Name
				}
			}
		}

		// check classifies expr at a site of kind site whose value must be
		// in the list of kind; allowEmpty admits "".
		check := func(site string, kind authAuditKind, expr ast.Expr, allowEmpty bool) {
			scan.sites[site]++
			fail := func(why string) {
				scan.findings = append(scan.findings, fmt.Sprintf("%s: %s: %s", fset.Position(expr.Pos()), site, why))
			}
			registered := func(name string) {
				if !registry[kind][name] {
					fail(name + " is not in the " + string(kind) + " list")
					return
				}
				scan.used[name] = true
				switch site {
				case "oidcRefuse", "oidcRefuseWith", "sso.refuse", "sso.refuseWith":
					scan.oidcCodes[serviceValues[name]] = true
				}
			}
			for {
				paren, ok := expr.(*ast.ParenExpr)
				if !ok {
					break
				}
				expr = paren.X
			}
			switch e := expr.(type) {
			case *ast.BasicLit:
				if allowEmpty && e.Value == `""` {
					return
				}
				fail("literal " + e.Value)
			case *ast.Ident:
				if f.dir == authAuditServiceDir && consts[f.dir][e.Name] {
					registered(e.Name)
				} else if f.dir == authAuditSSODir && consts[f.dir][e.Name] {
					value, ok := ssoValues[e.Name]
					name := registeredByValue(kind, value)
					if !ok || name == "" {
						fail("constant " + e.Name + " has a value that is not in the " + string(kind) + " list")
					} else {
						registered(name)
					}
				} else if consts[f.dir][e.Name] {
					fail("constant " + e.Name + " is not a registry constant")
				}
				// Otherwise a parameter, a local variable or a predeclared
				// identifier: its source is a site of its own.
			case *ast.SelectorExpr:
				if x, ok := e.X.(*ast.Ident); ok && serviceAlias != "" && x.Name == serviceAlias && consts[authAuditServiceDir][e.Sel.Name] {
					registered(e.Sel.Name)
				}
				// Otherwise a field of a local value, covered by the
				// composite-literal sites.
			case *ast.BinaryExpr:
				fail("built by an expression")
			case *ast.CallExpr:
				fail("built by a call")
			default:
				fail(fmt.Sprintf("unrecognised expression %T", expr))
			}
		}

		// compositeType is the type name of a composite literal and
		// "service.", "idkitoidc." or "idkit." when it is qualified by this
		// file's import of the service package, of the package of the sign-in
		// error types or of the package of idkit.AuditEvent.
		compositeType := func(node *ast.CompositeLit) (typeName, prefix string) {
			switch typ := node.Type.(type) {
			case *ast.Ident:
				typeName = typ.Name
			case *ast.SelectorExpr:
				if x, ok := typ.X.(*ast.Ident); ok && serviceAlias != "" && x.Name == serviceAlias {
					typeName, prefix = typ.Sel.Name, "service."
				} else if ok && idkitOIDCAlias != "" && x.Name == idkitOIDCAlias {
					typeName, prefix = typ.Sel.Name, "idkitoidc."
				} else if ok && idkitAlias != "" && x.Name == idkitAlias {
					typeName, prefix = typ.Sel.Name, "idkit."
				}
			}
			return typeName, prefix
		}
		auditEntryFields := map[string]authAuditKind{"Method": authAuditKindMethod, "Outcome": authAuditKindOutcome, "Reason": authAuditKindReason}
		// auditRecord reports whether a composite literal of typeName and
		// prefix becomes an audit row: AuthAuditEntry, or idkit.AuditEvent.
		auditRecord := func(typeName, prefix string) bool {
			return typeName == "AuthAuditEntry" || (prefix == "idkit." && typeName == "AuditEvent")
		}

		// Forwarded variables (#577): in one function body (a FuncDecl's or a
		// FuncLit's, without the FuncLits nested in it), an identifier passed as
		// the Method, Outcome or Reason of an AuthAuditEntry literal has every
		// single-value assignment (`=`, `:=`, `var x = ...`) in that body
		// checked as a site of its own. A multi-value assignment from one call
		// is skipped: the call's return is a site already. Not covered: a
		// closure assigning a variable of its enclosing function, and a chain
		// such as `x := "bogus"; reason = x`, whose identifier right-hand side
		// is accepted like any other identifier.
		walkBody := func(body *ast.BlockStmt, visit func(ast.Node)) {
			ast.Inspect(body, func(n ast.Node) bool {
				if n == nil {
					return true
				}
				if _, nested := n.(*ast.FuncLit); nested {
					return false
				}
				visit(n)
				return true
			})
		}
		type forwardedSite struct {
			site string
			kind authAuditKind
		}
		checkForwarded := func(body *ast.BlockStmt) {
			if body == nil {
				return
			}
			forwarded := map[string][]forwardedSite{}
			walkBody(body, func(n ast.Node) {
				node, ok := n.(*ast.CompositeLit)
				if !ok {
					return
				}
				typeName, prefix := compositeType(node)
				if !auditRecord(typeName, prefix) {
					return
				}
				for _, element := range node.Elts {
					pair, ok := element.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := pair.Key.(*ast.Ident)
					if !ok {
						continue
					}
					value, ok := pair.Value.(*ast.Ident)
					kind, known := auditEntryFields[key.Name]
					if !ok || !known {
						continue
					}
					site := forwardedSite{prefix + typeName + "." + key.Name + ".assigned", kind}
					seen := false
					for _, existing := range forwarded[value.Name] {
						seen = seen || existing == site
					}
					if !seen {
						forwarded[value.Name] = append(forwarded[value.Name], site)
					}
				}
			})
			if len(forwarded) == 0 {
				return
			}
			assigned := func(name *ast.Ident, value ast.Expr) {
				for _, site := range forwarded[name.Name] {
					check(site.site, site.kind, value, false)
				}
			}
			walkBody(body, func(n ast.Node) {
				switch node := n.(type) {
				case *ast.AssignStmt:
					if len(node.Lhs) != len(node.Rhs) {
						return
					}
					for i, lhs := range node.Lhs {
						if ident, ok := lhs.(*ast.Ident); ok {
							assigned(ident, node.Rhs[i])
						}
					}
				case *ast.ValueSpec:
					if len(node.Names) != len(node.Values) {
						return
					}
					for i, name := range node.Names {
						assigned(name, node.Values[i])
					}
				}
			})
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			switch fn := n.(type) {
			case *ast.FuncDecl:
				checkForwarded(fn.Body)
			case *ast.FuncLit:
				checkForwarded(fn.Body)
			}
			return true
		})

		var stack []ast.Node
		enclosingFunc := func() *ast.FuncDecl {
			for i := len(stack) - 1; i >= 0; i-- {
				switch fn := stack[i].(type) {
				case *ast.FuncLit:
					return nil
				case *ast.FuncDecl:
					return fn
				}
			}
			return nil
		}
		// insideErrNil reports whether the node on top of the stack is in
		// the body of an `if err == nil` statement.
		insideErrNil := func() bool {
			for i := len(stack) - 1; i >= 0; i-- {
				stmt, ok := stack[i].(*ast.IfStmt)
				if !ok {
					continue
				}
				cond, ok := stmt.Cond.(*ast.BinaryExpr)
				if !ok || cond.Op != token.EQL {
					continue
				}
				x, xok := cond.X.(*ast.Ident)
				y, yok := cond.Y.(*ast.Ident)
				if xok && yok && x.Name == "err" && y.Name == "nil" {
					return true
				}
			}
			return false
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			defer func() { stack = append(stack, n) }()
			switch node := n.(type) {
			case *ast.CallExpr:
				name := ""
				switch fn := node.Fun.(type) {
				case *ast.Ident:
					name = fn.Name
				case *ast.SelectorExpr:
					name = fn.Sel.Name
				}
				if f.dir == authAuditSSODir {
					switch name {
					case "refuse", "refuseWith", "unavailable":
						if len(node.Args) > 0 {
							check("sso."+name, authAuditKindReason, node.Args[0], false)
						}
						return true
					}
				}
				switch name {
				case "authRefusal", "oidcRefuse", "oidcRefuseWith", "refuse":
					if len(node.Args) > 0 {
						check(name, authAuditKindReason, node.Args[0], false)
					}
				case "outcomeEntry":
					if len(node.Args) == 4 {
						check("outcomeEntry.method", authAuditKindMethod, node.Args[0], false)
						check("outcomeEntry.fallback", authAuditKindReason, node.Args[3], false)
					}
				}
			case *ast.CompositeLit:
				typeName, prefix := compositeType(node)
				fields := map[string]authAuditKind{}
				switch {
				case auditRecord(typeName, prefix):
					fields = auditEntryFields
				case prefix == "" && typeName == "authRefusalError":
					fields = map[string]authAuditKind{"reason": authAuditKindReason}
				case prefix == "idkitoidc." && (typeName == "RefusalError" || typeName == "UnavailableError"):
					fields = map[string]authAuditKind{"Code": authAuditKindReason}
				}
				for _, element := range node.Elts {
					pair, ok := element.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := pair.Key.(*ast.Ident)
					if !ok {
						continue
					}
					if kind, ok := fields[key.Name]; ok {
						check(prefix+typeName+"."+key.Name, kind, pair.Value, false)
					}
				}
			case *ast.ReturnStmt:
				fn := enclosingFunc()
				if fn == nil {
					return true
				}
				if fn.Name.Name == "AuthAuditReason" && fn.Recv == nil && len(node.Results) == 2 {
					check("AuthAuditReason.return", authAuditKindReason, node.Results[0], insideErrNil())
				}
				if f.dir == authAuditSSODir && fn.Name.Name == "auditReason" && fn.Recv == nil && len(node.Results) == 2 {
					check("sso.auditReason.return", authAuditKindReason, node.Results[0], false)
				}
				if fn.Name.Name == "Authenticate" && authAuditReceiverIs(fn, "scimTokenService") && len(node.Results) == 2 {
					// "" only on success, where the token is returned.
					first, isIdent := node.Results[0].(*ast.Ident)
					success := !isIdent || first.Name != "nil"
					check("Authenticate.return", authAuditKindReason, node.Results[1], success)
				}
			case *ast.AssignStmt:
				for i, lhs := range node.Lhs {
					if ident, ok := lhs.(*ast.Ident); ok && ident.Name == "ownerRejected" && i < len(node.Rhs) && len(node.Lhs) == len(node.Rhs) {
						check("ownerRejected", authAuditKindReason, node.Rhs[i], node.Tok == token.DEFINE)
					}
				}
			case *ast.ValueSpec:
				for i, name := range node.Names {
					if name.Name == "ownerRejected" && i < len(node.Values) {
						check("ownerRejected", authAuditKindReason, node.Values[i], true)
					}
				}
			}
			return true
		})
	}
	sort.Strings(scan.findings)
	return scan
}

// authAuditReceiverIs reports whether fn is a method on *name.
func authAuditReceiverIs(fn *ast.FuncDecl, name string) bool {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return false
	}
	star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == name
}

// scanAuthAuditRepository parses the repository and runs the checker.
func scanAuthAuditRepository(t *testing.T) (authAuditScan, authAuditRegistry, map[string]string) {
	t.Helper()
	fset := token.NewFileSet()
	files := parseAuthAuditSources(t, fset)
	var vocabulary *ast.File
	for _, f := range files {
		if f.path == authAuditVocabularyFile {
			vocabulary = f.file
		}
	}
	require.NotNil(t, vocabulary, "%s is parsed", authAuditVocabularyFile)
	registry := loadAuthAuditRegistry(t, vocabulary)
	scan := scanAuthAuditWriters(fset, files, registry)
	values := map[string]string{}
	for _, decl := range vocabulary.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			for i, name := range value.Names {
				if lit, ok := value.Values[i].(*ast.BasicLit); ok {
					values[name.Name], _ = strconv.Unquote(lit.Value)
				}
			}
		}
	}
	return scan, registry, values
}

// authAuditOIDCRefusalCodes is the value of every registry constant an
// oidcRefuse or oidcRefuseWith site passes, sorted.
func authAuditOIDCRefusalCodes(t *testing.T) []string {
	t.Helper()
	scan, _, _ := scanAuthAuditRepository(t)
	require.Empty(t, scan.findings)
	codes := make([]string, 0, len(scan.oidcCodes))
	for code := range scan.oidcCodes {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

func TestAuthAuditWritersUseRegisteredValues(t *testing.T) {
	scan, registry, values := scanAuthAuditRepository(t)

	t.Run("every writer site passes a registered value", func(t *testing.T) {
		assert.Empty(t, scan.findings)
	})

	t.Run("every site kind is matched", func(t *testing.T) {
		for _, kind := range authAuditSiteKinds {
			assert.Positive(t, scan.sites[kind], "site %s matched nothing: the scan is vacuous", kind)
		}
	})

	t.Run("the registry lists are the runtime sets", func(t *testing.T) {
		want := map[authAuditKind][]string{
			authAuditKindMethod:  AuthAuditMethods(),
			authAuditKindOutcome: AuthAuditOutcomes(),
			authAuditKindReason:  AuthAuditReasons(),
		}
		for kind, names := range registry {
			got := []string{}
			for name := range names {
				value, ok := values[name]
				require.True(t, ok, "%s is declared in %s", name, authAuditVocabularyFile)
				got = append(got, value)
			}
			sort.Strings(got)
			assert.Equal(t, want[kind], got, "the %s list", kind)
		}
		assert.Len(t, registry[authAuditKindReason], 44)
	})

	t.Run("constants are declared in the vocabulary file only", func(t *testing.T) {
		require.NotEmpty(t, scan.declared)
		for name, file := range scan.declared {
			assert.Equal(t, authAuditVocabularyFile, file, "%s is declared outside the vocabulary file", name)
		}
	})

	t.Run("every registered value is written", func(t *testing.T) {
		for kind, names := range registry {
			for name := range names {
				assert.True(t, scan.used[name], "%s (%s) is registered but no writer site passes it", name, kind)
			}
		}
	})

	t.Run("single sign-on refusal codes", func(t *testing.T) {
		codes := make([]string, 0, len(scan.oidcCodes))
		for code := range scan.oidcCodes {
			codes = append(codes, code)
		}
		sort.Strings(codes)
		assert.Equal(t, []string{
			"account_erased", "account_inactive", "binding_mismatch", "binding_missing", "claims_invalid",
			"customer_login_role", "email_missing", "email_taken", "group_overage", "groups_malformed",
			"groups_unavailable", "id_token_invalid", "id_token_missing", "identity_mismatch", "identity_taken",
			"issuer_changed", "issuer_invalid", "last_admin", "no_mapping_match", "nonce_mismatch",
			"role_missing", "roles_malformed", "state_invalid", "subject_invalid", "userinfo_subject_mismatch",
		}, codes)
	})

	t.Run("self-check", func(t *testing.T) {
		snippets := []struct {
			name     string
			dir      string
			source   string
			findings int
			// check holds the further assertions of a snippet.
			check func(t *testing.T, got authAuditScan)
		}{
			{"literal oidcRefuse", authAuditServiceDir,
				"package service\nfunc f() error { return oidcRefuse(\"made_up\") }\n", 1, nil},
			{"non-registry constant to authRefusal", authAuditServiceDir,
				"package service\nconst localCode = \"x\"\nfunc f(err error) error { return authRefusal(localCode, 0, err) }\n", 1, nil},
			{"literal Method of service.AuthAuditEntry", "internal/middleware",
				"package middleware\nimport \"github.com/florinel-chis/gophercrm/internal/service\"\nvar e = service.AuthAuditEntry{Method: \"made_up\"}\n", 1, nil},
			{"literal Reason of AuthAuditEntry", authAuditServiceDir,
				"package service\nvar e = AuthAuditEntry{Reason: \"made_up\"}\n", 1, nil},
			{"declared but unlisted reason constant", authAuditServiceDir,
				"package service\nconst AuthAuditReasonMadeUp = \"made_up\"\n", 1, nil},
			{"registered constant", authAuditServiceDir,
				"package service\nfunc f() error { return oidcRefuse(AuthAuditReasonNoMappingMatch) }\n", 0,
				func(t *testing.T, got authAuditScan) {
					assert.Equal(t, 1, got.sites["oidcRefuse"])
					assert.True(t, got.used["AuthAuditReasonNoMappingMatch"])
					assert.True(t, got.oidcCodes["no_mapping_match"])
				}},
			{"forwarded SCIM reason: literal fallback", "internal/middleware",
				scimReasonSnippet("", `"token_bogus"`), 1, nil},
			{"forwarded SCIM reason: non-registry constant fallback", "internal/middleware",
				scimReasonSnippet(`const bogus = "token_bogus"`, "bogus"), 1, nil},
			{"forwarded SCIM reason: registered constant fallback", "internal/middleware",
				scimReasonSnippet("", "service.AuthAuditReasonTokenInvalid"), 0,
				func(t *testing.T, got authAuditScan) {
					assert.Equal(t, 1, got.sites["service.AuthAuditEntry.Reason.assigned"])
					assert.True(t, got.used["AuthAuditReasonTokenInvalid"])
				}},
			{"literal Code of idkitoidc.RefusalError", authAuditServiceDir,
				"package service\nimport idkitoidc \"github.com/florinel-chis/gophercrm/internal/idkit/oidc\"\nfunc f() error { return &idkitoidc.RefusalError{Code: \"made_up\"} }\n", 1,
				func(t *testing.T, got authAuditScan) {
					assert.Equal(t, 1, got.sites["idkitoidc.RefusalError.Code"])
				}},
			{"go-oidc imported as oidc is not the error package", authAuditServiceDir,
				"package service\nimport \"github.com/coreos/go-oidc/v3/oidc\"\nvar e = &oidc.RefusalError{Code: \"made_up\"}\n", 0, nil},
			{"forwarded reason defined from a literal", authAuditServiceDir,
				"package service\nfunc f() { reason := \"made_up\"; _ = AuthAuditEntry{Reason: reason} }\n", 1, nil},
			{"literal code in the sign-in package", authAuditSSODir,
				"package sso\nfunc f() error { return refuse(\"made_up\") }\n", 1,
				func(t *testing.T, got authAuditScan) {
					assert.Equal(t, 1, got.sites["sso.refuse"])
				}},
			{"sign-in package constant with an unregistered value", authAuditSSODir,
				"package sso\nconst CodeMadeUp = \"made_up\"\nfunc f() error { return refuse(CodeMadeUp) }\n", 1, nil},
			{"sign-in package constant with a registered value", authAuditSSODir,
				"package sso\nconst CodeNoMappingMatch = \"no_mapping_match\"\nfunc f() error { return refuseWith(CodeNoMappingMatch, nil) }\n", 0,
				func(t *testing.T, got authAuditScan) {
					assert.Equal(t, 1, got.sites["sso.refuseWith"])
					assert.True(t, got.used["AuthAuditReasonNoMappingMatch"])
					assert.True(t, got.oidcCodes["no_mapping_match"])
				}},
			{"literal Method of idkit.AuditEvent", authAuditSSODir,
				"package sso\nimport \"github.com/florinel-chis/gophercrm/internal/idkit\"\nvar e = idkit.AuditEvent{Method: \"made_up\"}\n", 1, nil},
		}
		for _, snippet := range snippets {
			t.Run(snippet.name, func(t *testing.T) {
				fset := token.NewFileSet()
				vocabulary, err := parser.ParseFile(fset, filepath.Join("..", "..", authAuditVocabularyFile), nil, parser.SkipObjectResolution)
				require.NoError(t, err)
				parsed, err := parser.ParseFile(fset, snippet.dir+"/snippet.go", snippet.source, parser.SkipObjectResolution)
				require.NoError(t, err)
				got := scanAuthAuditWriters(fset, []authAuditSourceFile{
					{path: authAuditVocabularyFile, dir: authAuditServiceDir, file: vocabulary},
					{path: snippet.dir + "/snippet.go", dir: snippet.dir, file: parsed},
				}, registry)
				assert.Len(t, got.findings, snippet.findings, "findings: %v", got.findings)
				if snippet.check != nil {
					snippet.check(t, got)
				}
			})
		}
	})
}

// scimReasonSnippet is a middleware source in the shape of the SCIM refusal:
// the reason of a call, replaced by fallback when empty, forwarded into a
// service.AuthAuditEntry inside a func literal. decl is a package-level
// declaration, or "".
func scimReasonSnippet(decl, fallback string) string {
	return "package middleware\n" +
		"import \"github.com/florinel-chis/gophercrm/internal/service\"\n" +
		decl + "\n" +
		"func g() (*int, string) { return nil, \"\" }\n" +
		"func h() func() {\n" +
		"\treturn func() {\n" +
		"\t\ttoken, reason := g()\n" +
		"\t\tif token == nil {\n" +
		"\t\t\tif reason == \"\" {\n" +
		"\t\t\t\treason = " + fallback + "\n" +
		"\t\t\t}\n" +
		"\t\t\t_ = service.AuthAuditEntry{Reason: reason}\n" +
		"\t\t}\n" +
		"\t}\n" +
		"}\n"
}
