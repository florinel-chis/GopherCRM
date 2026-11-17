package main

// Issue #337: the lead erasure hooks (repository.LeadErasureHooks, set through
// the repository.WithLeadErasureHooks option of NewLeadRepository) are test
// seams. No production file may reference them; only their own definition file
// names them.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLeadErasureHooksNotWiredInProduction(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	const definition = "internal/repository/lead_erasure_hooks.go"
	forbidden := map[string]bool{"WithLeadErasureHooks": true, "LeadErasureHooks": true}

	parsed := 0
	sawMain, sawDefinition := false, false
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				if name := d.Name(); name == "testdata" || (strings.HasPrefix(name, ".") && path != filepath.Join(root, dir)) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			parsed++
			switch rel {
			case "cmd/main.go":
				sawMain = true
			case definition:
				sawDefinition = true
			}

			var refs []string
			ast.Inspect(file, func(n ast.Node) bool {
				if ident, ok := n.(*ast.Ident); ok && forbidden[ident.Name] {
					refs = append(refs, ident.Name)
				}
				return true
			})
			if rel == definition {
				assert.NotEmpty(t, refs, "%s defines the hooks", rel)
				return nil
			}
			assert.Empty(t, refs, "%s references the test-only lead erasure hooks", rel)
			return nil
		})
		require.NoError(t, err)
	}
	assert.True(t, sawMain, "cmd/main.go was parsed")
	assert.True(t, sawDefinition, "%s was parsed", definition)
	assert.Greater(t, parsed, 50, "the production sources were walked")
}
