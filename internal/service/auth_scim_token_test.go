package service

// Issue #468: the SCIM provider token store. TestSCIMTokenStore{SQLite,MySQL}
// run one table of checks through the real repository and service on a
// SQLite file opened by the production connector and on a schema built by the
// real migrations (TEST_MYSQL_DSN, also pointed at MariaDB 10.11).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const scimTokenTestSecret = "scim-token-test-api-key-secret-0468-xxxxxxxx"

func TestSCIMTokenStoreSQLite(t *testing.T) {
	db, _ := openOIDCTestSQLite(t)
	runSCIMTokenStore(t, db)
}

func TestSCIMTokenStoreMySQL(t *testing.T) {
	db := openMigratedTestMySQL(t, 3)
	runSCIMTokenStore(t, db)
}

// scimRawRow reads every column of the scim_tokens row id as text.
func scimRawRow(t *testing.T, db *gorm.DB, id uint) map[string]string {
	t.Helper()
	rows, err := db.Raw("SELECT * FROM scim_tokens WHERE id = ?", id).Rows()
	require.NoError(t, err)
	defer rows.Close()
	columns, err := rows.Columns()
	require.NoError(t, err)
	require.True(t, rows.Next(), "the row exists")
	values := make([]interface{}, len(columns))
	pointers := make([]interface{}, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	require.NoError(t, rows.Scan(pointers...))
	out := map[string]string{}
	for i, column := range columns {
		switch v := values[i].(type) {
		case nil:
			out[column] = ""
		case []byte:
			out[column] = string(v)
		default:
			out[column] = fmt.Sprint(v)
		}
	}
	return out
}

func runSCIMTokenStore(t *testing.T, db *gorm.DB) {
	svc := NewSCIMTokenService(repository.NewSCIMTokenRepository(db), scimTokenTestSecret)
	ctx := context.Background()

	plaintext, row, err := svc.Create(ctx, "  Entra  ")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.True(t, strings.HasPrefix(plaintext, SCIMTokenPrefix), "the token starts with scim_")
	hexPart := strings.TrimPrefix(plaintext, SCIMTokenPrefix)
	assert.Len(t, hexPart, 64, "hex of 32 bytes")
	assert.Equal(t, "Entra", row.Name, "the name is trimmed")

	t.Run("create stores only the HMAC", func(t *testing.T) {
		var stored models.SCIMToken
		require.NoError(t, db.First(&stored, row.ID).Error)
		assert.Equal(t, utils.HashAPIKeyHMAC(plaintext, scimTokenTestSecret), stored.TokenHash)
		assert.True(t, strings.HasPrefix(stored.TokenHash, "hmac$"))
		assert.Nil(t, stored.RevokedAt)
		assert.Nil(t, stored.LastUsedAt)
		raw := scimRawRow(t, db, row.ID)
		require.Len(t, raw, 6, "id, name, token_hash, created_at, last_used_at, revoked_at")
		for column, value := range raw {
			assert.NotContains(t, value, plaintext, "column %s holds the plaintext", column)
			assert.NotContains(t, value, hexPart, "column %s holds the token's hex part", column)
		}
	})

	t.Run("name rules", func(t *testing.T) {
		for _, bad := range []string{"", "   ", strings.Repeat("n", 101)} {
			_, _, err := svc.Create(ctx, bad)
			assert.True(t, errors.Is(err, ErrSCIMTokenNameInvalid), "%q", bad)
			assert.True(t, errors.Is(err, apperrors.ErrValidation))
		}
		_, ok, err := svc.Create(ctx, strings.Repeat("é", 100))
		require.NoError(t, err, "100 characters are allowed")
		assert.NotNil(t, ok)
	})

	t.Run("list is newest first and carries no hash", func(t *testing.T) {
		_, newer, err := svc.Create(ctx, "Okta")
		require.NoError(t, err)
		tokens, err := svc.List(ctx)
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(tokens), 2)
		assert.Equal(t, newer.ID, tokens[0].ID)
		for i := 1; i < len(tokens); i++ {
			assert.Greater(t, tokens[i-1].ID, tokens[i].ID)
		}
		raw, err := json.Marshal(tokens)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "hmac$")
		assert.NotContains(t, string(raw), "token_hash")
		assert.NotContains(t, string(raw), hexPart)
	})

	t.Run("authenticate", func(t *testing.T) {
		got, reason := svc.Authenticate(ctx, "")
		assert.Nil(t, got)
		assert.Equal(t, AuthAuditReasonTokenMissing, reason)
		for _, header := range []string{"Bearer ", "ApiKey " + plaintext, "Basic abc", plaintext, "Bearer"} {
			got, reason = svc.Authenticate(ctx, header)
			assert.Nil(t, got, header)
			assert.Equal(t, AuthAuditReasonTokenMissing, reason, "%q", header)
		}
		for _, header := range []string{
			"Bearer scim_" + strings.Repeat("0", 64),
			"Bearer " + strings.ToUpper(plaintext),
			"Bearer " + plaintext + " ",
			"Bearer " + utils.HashAPIKeyHMAC(plaintext, scimTokenTestSecret),
		} {
			got, reason = svc.Authenticate(ctx, header)
			assert.Nil(t, got)
			assert.Equal(t, AuthAuditReasonTokenInvalid, reason, "%q", header)
		}

		before := time.Now().UTC().Add(-time.Second)
		got, reason = svc.Authenticate(ctx, "bearer "+plaintext)
		require.NotNil(t, got, "the scheme is case-insensitive")
		assert.Equal(t, "", reason)
		assert.Equal(t, row.ID, got.ID)
		var stored models.SCIMToken
		require.NoError(t, db.First(&stored, row.ID).Error)
		require.NotNil(t, stored.LastUsedAt, "last_used_at is set")
		assert.True(t, stored.LastUsedAt.After(before), "last_used_at is now")
	})

	t.Run("revoke is idempotent", func(t *testing.T) {
		revoked, err := svc.Revoke(ctx, row.ID)
		require.NoError(t, err)
		require.NotNil(t, revoked.RevokedAt)
		first := *revoked.RevokedAt

		svc.(*scimTokenService).now = func() time.Time { return time.Now().Add(time.Hour) }
		t.Cleanup(func() { svc.(*scimTokenService).now = time.Now })
		again, err := svc.Revoke(ctx, row.ID)
		require.NoError(t, err)
		require.NotNil(t, again.RevokedAt)
		assert.True(t, first.Equal(*again.RevokedAt), "a second revoke keeps the first revoked_at")

		got, reason := svc.Authenticate(ctx, "Bearer "+plaintext)
		assert.Nil(t, got)
		assert.Equal(t, AuthAuditReasonTokenRevoked, reason)

		_, err = svc.Revoke(ctx, 999999)
		assert.True(t, errors.Is(err, apperrors.ErrNotFound), "an unknown id is not found: %v", err)
	})
}
