package models

import (
	"strings"
	"sync"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/schema"
)

// The stored key hash is "hmac$" plus 64 hex characters (69 in total). The
// column was declared varchar(64), which SQLite ignores but MySQL and MariaDB
// enforce: every API key creation failed there with Error 1406 "Data too long
// for column 'key_hash'", while the SQLite-backed suites stayed green.
func TestAPIKeyHashColumnFitsTheStoredHash(t *testing.T) {
	parsed, err := schema.Parse(&APIKey{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	field := parsed.LookUpField("KeyHash")
	require.NotNil(t, field)

	hash := utils.HashAPIKeyHMAC("gcrm_"+strings.Repeat("a", 32), "a-secret")

	assert.GreaterOrEqual(t, field.Size, len(hash),
		"key_hash must hold an HMAC hash of %d characters on MySQL, which enforces the column size", len(hash))
}
