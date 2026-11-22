package lifecycle_test

import (
	"bufio"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLifecycleImportBoundary checks that the runtime lifecycle types depend on the standard library only.
// It follows the module imports of the non-test files recursively.
func TestLifecycleImportBoundary(t *testing.T) {
	root, module := moduleRoot(t)
	reached := moduleImports(t, root, module, "internal/lifecycle")
	require.Empty(t, reached)
}

// moduleRoot walks up from the package directory to go.mod and returns the
// repository root and the module path.
func moduleRoot(t *testing.T) (string, string) {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		file, err := os.Open(filepath.Join(dir, "go.mod"))
		if err == nil {
			defer file.Close()
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				if line := strings.TrimSpace(scanner.Text()); strings.HasPrefix(line, "module ") {
					return dir, strings.TrimSpace(strings.TrimPrefix(line, "module "))
				}
			}
			t.Fatal("go.mod has no module line")
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "no go.mod above the package")
		dir = parent
	}
}

// moduleImports returns every module package (relative to the root) that
// start reaches through the imports of non-test files, start excluded.
func moduleImports(t *testing.T, root, module, start string) []string {
	t.Helper()
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		rel := queue[0]
		queue = queue[1:]
		files, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(rel), "*.go"))
		require.NoError(t, err)
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			require.NoError(t, err)
			for _, spec := range parsed.Imports {
				imported, err := strconv.Unquote(spec.Path.Value)
				require.NoError(t, err)
				if !strings.HasPrefix(imported, module+"/") {
					continue
				}
				next := strings.TrimPrefix(imported, module+"/")
				if !seen[next] {
					seen[next] = true
					queue = append(queue, next)
				}
			}
		}
	}
	delete(seen, start)
	reached := make([]string, 0, len(seen))
	for rel := range seen {
		reached = append(reached, rel)
	}
	sort.Strings(reached)
	return reached
}
