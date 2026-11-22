package idkit_test

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// probeDir is the nested package the probe subtests add through an overlay.
const probeDir = "internal/idkit/zzprobe/nested"

// TestIDKitImportBoundary checks that no non-test package under internal/idkit
// reaches gin, gorm, internal/models, internal/repository, internal/utils or
// logrus, directly or through other packages.
func TestIDKitImportBoundary(t *testing.T) {
	root, module := moduleRoot(t)
	probePkg := module + "/" + probeDir

	t.Run("current tree", func(t *testing.T) {
		pkgs := listIDKit(t, root, "")
		require.Contains(t, pkgs, module+"/internal/idkit", "go list ./internal/idkit/... did not list the package")
		require.Contains(t, pkgs, module+"/internal/idkit/scim", "go list ./internal/idkit/... did not list the scim package")
		require.Contains(t, pkgs, module+"/internal/idkit/rolemap", "go list ./internal/idkit/... did not list the rolemap package")
		require.Contains(t, pkgs, module+"/internal/idkit/oidc", "go list ./internal/idkit/... did not list the oidc package")
		require.Contains(t, pkgs, module+"/internal/idkit/entra", "go list ./internal/idkit/... did not list the entra package")
		found := violations(module, pkgs)
		require.Empty(t, found, "forbidden dependencies:\n%s", strings.Join(found, "\n"))
	})

	probes := []struct{ name, imp string }{
		{"gin", "github.com/gin-gonic/gin"},
		{"gorm", "gorm.io/gorm"},
		{"models", module + "/internal/models"},
		{"repository", module + "/internal/repository"},
		{"utils", module + "/internal/utils"},
		{"logrus", "github.com/sirupsen/logrus"},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			pkgs := listIDKit(t, root, writeProbe(t, root, probe.imp))
			require.Contains(t, pkgs, probePkg)
			require.Contains(t, violations(module, pkgs), probePkg+" reaches "+probe.imp)
		})
	}

	t.Run("transitive", func(t *testing.T) {
		pkgs := listIDKit(t, root, writeProbe(t, root, module+"/internal/errors"))
		require.Contains(t, pkgs, probePkg)
		found := violations(module, pkgs)
		require.Contains(t, found, probePkg+" reaches github.com/gin-gonic/gin")
		require.Contains(t, found, probePkg+" reaches gorm.io/gorm")
	})

	t.Run("allowed", func(t *testing.T) {
		pkgs := listIDKit(t, root, writeProbe(t, root, "net/http"))
		require.Contains(t, pkgs, probePkg, "the overlay was not honoured")
		found := violations(module, pkgs)
		require.Empty(t, found, "forbidden dependencies:\n%s", strings.Join(found, "\n"))
	})
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

// writeProbe writes a package that imports imp and an overlay that places it
// at probeDir, both under t.TempDir(), and returns the overlay file.
func writeProbe(t *testing.T, root, imp string) string {
	t.Helper()
	tmp := t.TempDir()
	source := filepath.Join(tmp, "probe.go")
	require.NoError(t, os.WriteFile(source, []byte("package probe\n\nimport _ \""+imp+"\"\n"), 0o600))
	overlay, err := json.Marshal(map[string]map[string]string{
		"Replace": {filepath.Join(root, filepath.FromSlash(probeDir), "probe.go"): source},
	})
	require.NoError(t, err)
	file := filepath.Join(tmp, "overlay.json")
	require.NoError(t, os.WriteFile(file, overlay, 0o600))
	return file
}

// listIDKit runs go list over ./internal/idkit/... and returns each package's
// import path mapped to its transitive non-test dependencies.
func listIDKit(t *testing.T, root, overlay string) map[string][]string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go binary not found: %v", err)
	}
	args := []string{"list"}
	if overlay != "" {
		args = append(args, "-overlay="+overlay)
	}
	args = append(args, "-f", "{{.ImportPath}}{{range .Deps}} {{.}}{{end}}", "./internal/idkit/...")
	cmd := exec.Command(goBin, args...)
	cmd.Dir = root
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list failed: %v\n%s", err, out)
	}
	pkgs := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pkgs[fields[0]] = fields[1:]
	}
	return pkgs
}

// forbidden lists the import paths no package under internal/idkit may reach.
// An entry also covers every path below it.
func forbidden(module string) []string {
	return []string{
		"github.com/gin-gonic/gin",
		"gorm.io",
		module + "/internal/models",
		module + "/internal/repository",
		module + "/internal/utils",
		"github.com/sirupsen/logrus",
	}
}

// violations returns the sorted "<pkg> reaches <dep>" lines for every
// dependency that equals a forbidden entry or lies below it.
func violations(module string, pkgs map[string][]string) []string {
	var found []string
	for pkg, deps := range pkgs {
		for _, dep := range deps {
			for _, entry := range forbidden(module) {
				if dep == entry || strings.HasPrefix(dep, entry+"/") {
					found = append(found, pkg+" reaches "+dep)
					break
				}
			}
		}
	}
	sort.Strings(found)
	return found
}
