package main

// Issue #337: the lead erasure hooks (repository.LeadErasureHooks, set through
// the repository.WithLeadErasureHooks option of NewLeadRepository) are test
// seams. Issue #385 added the hooks of the erasure protocol
// (repository.ErasureHooks, set through WithUserErasureHooks,
// WithCustomerErasureHooks and WithLeadErasureLockHooks). No production file
// may reference any of them; only their own definition files name them.

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
	definitions := map[string]bool{
		"internal/repository/lead_erasure_hooks.go": false,
		"internal/repository/erasure_hooks.go":      false,
	}
	forbidden := map[string]bool{
		"WithLeadErasureHooks": true, "LeadErasureHooks": true,
		"ErasureHooks": true, "WithUserErasureHooks": true, "WithCustomerErasureHooks": true, "WithLeadErasureLockHooks": true,
	}

	parsed := 0
	sawMain := false
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
			if rel == "cmd/main.go" {
				sawMain = true
			}

			var refs []string
			ast.Inspect(file, func(n ast.Node) bool {
				if ident, ok := n.(*ast.Ident); ok && forbidden[ident.Name] {
					refs = append(refs, ident.Name)
				}
				return true
			})
			if _, isDefinition := definitions[rel]; isDefinition {
				definitions[rel] = true
				assert.NotEmpty(t, refs, "%s defines the hooks", rel)
				return nil
			}
			assert.Empty(t, refs, "%s references the test-only erasure hooks", rel)
			return nil
		})
		require.NoError(t, err)
	}
	assert.True(t, sawMain, "cmd/main.go was parsed")
	for definition, saw := range definitions {
		assert.True(t, saw, "%s was parsed", definition)
	}
	assert.Greater(t, parsed, 50, "the production sources were walked")
}
