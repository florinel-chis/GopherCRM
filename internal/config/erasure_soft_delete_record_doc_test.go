package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// erasureSoftDeleteTransientCaveat is the sentence #355 removed once the
// Delete-chain form of the primary-key-only soft delete was recorded on MySQL 8.
const erasureSoftDeleteTransientCaveat = "still has to be recorded on MySQL 8"

// erasureSoftDeleteRecordedClause is the recorded result both documents carry.
const erasureSoftDeleteRecordedClause = "in the Delete-chain form used now, together with " +
	"`TestIdentityErasureMySQL`, on MariaDB 10.11 (QA) and on MySQL 8.0 (the #355 acceptance run)"

// TestErasureSoftDeleteRecordedDocs pins #355: docs/CONCURRENCY.md (Lead-rooted
// erasure) and docs/DEV_ERASURE.md (Lead erasure against lead conversion) state
// the recorded MariaDB 10.11 and MySQL 8.0 results of the Delete-chain soft
// delete instead of the transient caveat.
func TestErasureSoftDeleteRecordedDocs(t *testing.T) {
	for _, path := range []string{concurrencyDocFile, "docs/DEV_ERASURE.md"} {
		t.Run(path, func(t *testing.T) {
			text := concurrencyDocNormalize(readRepositoryFile(t, path))
			assert.NotContains(t, text, erasureSoftDeleteTransientCaveat,
				"%s still carries the transient MySQL 8 caveat", path)
			assert.Equal(t, 1, strings.Count(text, erasureSoftDeleteRecordedClause),
				"%s states the recorded Delete-chain result exactly once", path)
		})
	}

	t.Run("CONCURRENCY.md clause lies in the Lead-rooted erasure flow before its Transaction line", func(t *testing.T) {
		raw := readRepositoryFile(t, concurrencyDocFile)
		const heading = "\n### Lead-rooted erasure\n"
		start := strings.Index(raw, heading)
		require.GreaterOrEqual(t, start, 0, "Lead-rooted erasure heading not found")
		flow := raw[start+len(heading):]
		if next := strings.Index(flow, "\n### "); next >= 0 {
			flow = flow[:next]
		}
		transaction := strings.Index(flow, "\n- **Transaction:**")
		require.GreaterOrEqual(t, transaction, 0, "Lead-rooted erasure has no Transaction line")
		before := concurrencyDocNormalize(flow[:transaction])
		assert.Contains(t, before, erasureSoftDeleteRecordedClause,
			"the recorded result belongs to the Lead-rooted erasure flow, before its Transaction line")
	})
}
