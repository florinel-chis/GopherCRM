// Command docsymbols lists the symbols each given Go file declares, so the
// documentation citation check (scripts/ci/check-doc-citations.py) can confirm
// that a symbol cited next to a .go path is declared in that file.
//
// For every file it prints the package clause and the declared names:
// top-level funcs, methods as Recv.Method (receiver without * and type
// parameters), types, vars and consts (every name of a grouped declaration),
// the methods an interface type declares as Iface.Method (embedded interfaces
// are not expanded) and the fields of a struct type as Type.Field (an
// embedded field under its type name). Files are parsed only; nothing is
// type-checked or built.
//
// Usage: go run ./scripts/ci/docsymbols FILE...
//
// The output is one JSON object keyed by the path as given:
// {"path": {"package": "name", "symbols": ["A", "T.M", ...]}}. A file that
// does not parse is reported on stderr and the exit status is non-zero.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
)

// File is the package clause and declared symbols of one Go file.
type File struct {
	Package string   `json:"package"`
	Symbols []string `json:"symbols"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: docsymbols FILE...")
		os.Exit(2)
	}
	out := map[string]File{}
	failed := false
	for _, path := range os.Args[1:] {
		file, err := symbols(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			failed = true
			continue
		}
		out[path] = file
	}
	if failed {
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// symbols parses one file and collects the names it declares.
func symbols(path string) (File, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return File{}, err
	}
	set := map[string]bool{}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv != nil && len(d.Recv.List) > 0 {
				if recv := receiverName(d.Recv.List[0].Type); recv != "" {
					set[recv+"."+d.Name.Name] = true
					continue
				}
			}
			set[d.Name.Name] = true
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					set[s.Name.Name] = true
					members(s.Name.Name, s.Type, set)
				case *ast.ValueSpec:
					for _, name := range s.Names {
						set[name.Name] = true
					}
				}
			}
		}
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return File{Package: f.Name.Name, Symbols: names}, nil
}

// members adds the interface methods or struct fields of a type declaration.
func members(typeName string, expr ast.Expr, set map[string]bool) {
	switch t := expr.(type) {
	case *ast.InterfaceType:
		for _, field := range t.Methods.List {
			if _, ok := field.Type.(*ast.FuncType); !ok {
				continue // an embedded interface or a type-set term
			}
			for _, name := range field.Names {
				set[typeName+"."+name.Name] = true
			}
		}
	case *ast.StructType:
		for _, field := range t.Fields.List {
			if len(field.Names) == 0 {
				if name := embeddedName(field.Type); name != "" {
					set[typeName+"."+name] = true
				}
				continue
			}
			for _, name := range field.Names {
				set[typeName+"."+name.Name] = true
			}
		}
	}
}

// receiverName returns the receiver's type name without * and type parameters.
func receiverName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverName(t.X)
	case *ast.ParenExpr:
		return receiverName(t.X)
	case *ast.IndexExpr:
		return receiverName(t.X)
	case *ast.IndexListExpr:
		return receiverName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return ""
}

// embeddedName returns the type name an embedded struct field is known by.
func embeddedName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return embeddedName(t.X)
	case *ast.IndexExpr:
		return embeddedName(t.X)
	case *ast.IndexListExpr:
		return embeddedName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.Ident:
		return t.Name
	}
	return ""
}
