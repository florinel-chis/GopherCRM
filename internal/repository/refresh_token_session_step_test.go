package repository

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// #546: every start gives every refresh token that holds no session values
// yet its own id as family_id, sign_in_method oidc when its owner has any
// user_identities row (either source) and password otherwise, and
// created_at as session_started_at. Revoked and soft-deleted rows are
// included, a row holding values is untouched, updated_at is not changed and
// a second run changes nothing.

func TestRefreshTokenSessionStepSQLite(t *testing.T) {
	runRefreshTokenSessionStep(t, openRoleDB(t, "refresh_token_session", false))
}

func TestRefreshTokenSessionStepMySQL(t *testing.T) {
	runRefreshTokenSessionStep(t, openRoleDB(t, "refresh_token_session", true))
}

// sessionTokenRow is every column the step may write, plus updated_at.
type sessionTokenRow struct {
	ID               uint
	FamilyID         string
	SignInMethod     string
	SessionStartedAt *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func readSessionTokens(t *testing.T, db *gorm.DB) map[uint]sessionTokenRow {
	t.Helper()
	var rows []sessionTokenRow
	require.NoError(t, db.Raw("SELECT id, family_id, sign_in_method, session_started_at, created_at, updated_at FROM refresh_tokens ORDER BY id").
		Scan(&rows).Error)
	out := map[uint]sessionTokenRow{}
	for _, row := range rows {
		out[row.ID] = row
	}
	return out
}

func runRefreshTokenSessionStep(t *testing.T, db *gorm.DB) {
	// The columns exist with the declared nullability and the index.
	columns, err := db.Migrator().ColumnTypes(&models.RefreshToken{})
	require.NoError(t, err)
	found := map[string]bool{}
	for _, column := range columns {
		nullable, ok := column.Nullable()
		require.True(t, ok)
		switch column.Name() {
		case "family_id", "sign_in_method":
			found[column.Name()] = true
			assert.False(t, nullable, "%s is NOT NULL", column.Name())
		case "session_started_at":
			found[column.Name()] = true
			assert.True(t, nullable, "session_started_at is nullable")
			assert.Contains(t, strings.ToLower(column.DatabaseTypeName()), "datetime")
		}
	}
	assert.Len(t, found, 3, "the three session columns exist")
	assert.True(t, db.Migrator().HasIndex(&models.RefreshToken{}, "idx_refresh_tokens_family_id"))

	user := func(email string) uint {
		u := &models.User{Email: email, Password: "x", FirstName: "Refresh", LastName: "Session", Role: models.RoleSales, IsActive: true}
		require.NoError(t, db.Create(u).Error)
		return u.ID
	}
	password := user("session-password@example.com")
	oidc := user("session-oidc@example.com")
	scim := user("session-scim@example.com")
	for owner, source := range map[uint]string{oidc: models.UserIdentitySourceOIDC, scim: models.UserIdentitySourceSCIM} {
		require.NoError(t, db.Create(&models.UserIdentity{UserID: owner, Issuer: "https://issuer.example.com",
			Subject: fmt.Sprintf("sub-%d", owner), Source: source}).Error)
	}

	seq := 0
	token := func(owner uint, revoked, deleted bool) uint {
		seq++
		rt := &models.RefreshToken{UserID: owner, TokenHash: fmt.Sprintf("session-step-hash-%d", seq),
			ExpiresAt: time.Now().Add(24 * time.Hour), Revoked: revoked}
		require.NoError(t, db.Create(rt).Error)
		if deleted {
			require.NoError(t, db.Delete(&models.RefreshToken{}, rt.ID).Error)
		}
		// As the previous release wrote it: no session values.
		require.NoError(t, db.Exec("UPDATE refresh_tokens SET family_id = '', sign_in_method = '', session_started_at = NULL WHERE id = ?", rt.ID).Error)
		return rt.ID
	}
	want := map[uint]string{
		token(password, false, false): models.RefreshTokenSignInPassword,
		token(password, true, false):  models.RefreshTokenSignInPassword,
		token(password, false, true):  models.RefreshTokenSignInPassword,
		token(oidc, false, false):     models.RefreshTokenSignInOIDC,
		token(oidc, true, false):      models.RefreshTokenSignInOIDC,
		token(scim, false, true):      models.RefreshTokenSignInOIDC,
	}

	// A row that already holds values, as a current release writes it.
	started := time.Date(2024, 6, 30, 12, 0, 0, 0, time.Local)
	current := &models.RefreshToken{UserID: oidc, TokenHash: "session-step-current", ExpiresAt: time.Now().Add(24 * time.Hour),
		FamilyID: "0123456789abcdef0123456789abcdef", SignInMethod: models.RefreshTokenSignInPassword, SessionStartedAt: &started}
	require.NoError(t, db.Create(current).Error)

	before := readSessionTokens(t, db)
	for id := range want {
		require.Empty(t, before[id].FamilyID)
		require.Nil(t, before[id].SessionStartedAt)
	}

	require.NoError(t, models.MigrateDatabaseWith(db))
	got := readSessionTokens(t, db)

	hex32 := regexp.MustCompile(`^[0-9a-f]{32}$`)
	for id, method := range want {
		row := got[id]
		assert.Equal(t, strconv.FormatUint(uint64(id), 10), row.FamilyID, "row %d is its own family", id)
		assert.False(t, hex32.MatchString(row.FamilyID), "a legacy family never looks like a new one")
		assert.Equal(t, method, row.SignInMethod, "row %d sign-in method", id)
		if assert.NotNil(t, row.SessionStartedAt, "row %d gets a session start", id) {
			assert.True(t, row.SessionStartedAt.Equal(row.CreatedAt), "row %d starts at created_at: %v != %v", id, *row.SessionStartedAt, row.CreatedAt)
		}
	}
	assert.Equal(t, before[current.ID], got[current.ID], "a row holding values is untouched")
	for id, row := range got {
		assert.True(t, row.UpdatedAt.Equal(before[id].UpdatedAt), "updated_at of row %d is not touched", id)
	}

	require.NoError(t, models.MigrateDatabaseWith(db))
	assert.Equal(t, got, readSessionTokens(t, db), "a second start changes nothing")
}
