package models

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm/schema"
)

var varcharLength = regexp.MustCompile(`^varchar\((\d+)\)`)

// The stored key hash is "hmac$" plus 64 hex characters (69 in total). The
// column was declared varchar(64), which SQLite ignores but MySQL and MariaDB
// enforce: every API key creation failed there with Error 1406 "Data too long
// for column 'key_hash'", while the SQLite-backed suites stayed green.
//
// The check reads the column type the MySQL dialect actually emits, so an
// explicit `type:` override cannot hide a size that is too small.
func TestAPIKeyHashColumnFitsTheStoredHash(t *testing.T) {
	parsed, err := schema.Parse(&APIKey{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	field := parsed.LookUpField("KeyHash")
	require.NotNil(t, field)

	columnType := mysql.Dialector{Config: &mysql.Config{}}.DataTypeOf(field)
	match := varcharLength.FindStringSubmatch(columnType)
	require.NotNil(t, match, "key_hash should be a varchar column on MySQL, got %q", columnType)
	length, err := strconv.Atoi(match[1])
	require.NoError(t, err)

	hash := utils.HashAPIKeyHMAC("gcrm_"+strings.Repeat("a", 32), "a-secret")

	assert.GreaterOrEqual(t, length, len(hash),
		"key_hash is %s on MySQL but must hold an HMAC hash of %d characters", columnType, len(hash))
}
