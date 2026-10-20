package release_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

// cmd/migrate's integration harness appends disposable schema names to its
// base DSN. Check the actual workflow input against the driver's parser, so a
// query on the base cannot silently turn real database coverage into skips.
func TestWorkflowDSNSupportsDisposableMigrationSchemas(t *testing.T) {
	workflow, err := os.ReadFile("../../.github/workflows/release.yml")
	require.NoError(t, err)
	matches := regexp.MustCompile(`(?m)^\s+TEST_MYSQL_DSN: (\S+)$`).FindAllStringSubmatch(string(workflow), -1)
	require.Len(t, matches, 1)
	base := matches[0][1]
	require.True(t, strings.HasSuffix(base, "/"), "migration base DSN must end with /")
	parsed, err := mysql.ParseDSN(base)
	require.NoError(t, err)
	require.Empty(t, parsed.DBName)
	require.Empty(t, parsed.Params)
	parsed, err = mysql.ParseDSN(base + "gocrm_release_contract?multiStatements=true&parseTime=true")
	require.NoError(t, err)
	require.Equal(t, "gocrm_release_contract", parsed.DBName)
	require.True(t, parsed.MultiStatements)
	require.True(t, parsed.ParseTime)
}
