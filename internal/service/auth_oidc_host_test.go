package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/idkit"
	"github.com/florinel-chis/gophercrm/internal/idkit/rolemap"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/glebarez/sqlite"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// errIDKitRollback makes a WithTransaction callback roll back.
var errIDKitRollback = errors.New("roll back")

var idkitHostDBSeq atomic.Int64

// openIDKitHostDB is a private in-memory SQLite database with the full
// schema and, as in production, one pooled connection.
func openIDKitHostDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := fmt.Sprintf("file:idkit_host_%d_%d?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)",
		time.Now().UnixNano(), idkitHostDBSeq.Add(1))
	db, err := gorm.Open(sqlite.Open(name), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, models.MigrateDatabaseWith(db))
	return db
}

func idkitCreateUser(t *testing.T, db *gorm.DB, email string) *models.User {
	t.Helper()
	user := &models.User{Email: email, Password: "x", FirstName: "Id", LastName: "Kit", Role: models.RoleSales, IsActive: true}
	require.NoError(t, db.Create(user).Error)
	return user
}

func idkitCount(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Table(table).Count(&n).Error)
	return n
}

// idkitCaptureLogs points utils.Logger at a test logger for the test.
func idkitCaptureLogs(t *testing.T) *logrustest.Hook {
	t.Helper()
	log, hook := logrustest.NewNullLogger()
	log.SetLevel(logrus.DebugLevel)
	previous := utils.Logger
	utils.Logger = log
	t.Cleanup(func() { utils.Logger = previous })
	return hook
}

func TestIDKitIdentityStore(t *testing.T) {
	ctx := context.Background()

	t.Run("create_then_get", func(t *testing.T) {
		db := openIDKitHostDB(t)
		user := idkitCreateUser(t, db, "a@example.com")
		store := NewIDKitIdentityStore(repository.NewUserIdentityRepository(db))
		accountID := strconv.FormatUint(uint64(user.ID), 10)
		require.NoError(t, store.Create(ctx, idkit.Identity{AccountID: accountID, Issuer: "https://idp.example", Subject: "sub-1", Source: idkit.IdentitySourceOIDC}))
		got, err := store.Get(ctx, "https://idp.example", "sub-1")
		require.NoError(t, err)
		assert.Equal(t, idkit.Identity{AccountID: accountID, Issuer: "https://idp.example", Subject: "sub-1", Source: idkit.IdentitySourceOIDC}, *got)
	})

	t.Run("get_missing", func(t *testing.T) {
		db := openIDKitHostDB(t)
		store := NewIDKitIdentityStore(repository.NewUserIdentityRepository(db))
		_, err := store.Get(ctx, "https://idp.example", "nobody")
		assert.True(t, errors.Is(err, idkit.ErrNotFound), "got %v", err)
	})

	t.Run("create_taken", func(t *testing.T) {
		db := openIDKitHostDB(t)
		first := idkitCreateUser(t, db, "a@example.com")
		second := idkitCreateUser(t, db, "b@example.com")
		store := NewIDKitIdentityStore(repository.NewUserIdentityRepository(db))
		identity := idkit.Identity{AccountID: strconv.FormatUint(uint64(first.ID), 10), Issuer: "https://idp.example", Subject: "sub-1", Source: idkit.IdentitySourceOIDC}
		require.NoError(t, store.Create(ctx, identity))
		identity.AccountID = strconv.FormatUint(uint64(second.ID), 10)
		err := store.Create(ctx, identity)
		assert.True(t, errors.Is(err, idkit.ErrIdentityTaken), "got %v", err)
	})

	t.Run("joins_transaction", func(t *testing.T) {
		db := openIDKitHostDB(t)
		user := idkitCreateUser(t, db, "a@example.com")
		store := NewIDKitIdentityStore(repository.NewUserIdentityRepository(db))
		identity := idkit.Identity{AccountID: strconv.FormatUint(uint64(user.ID), 10), Issuer: "https://idp.example", Subject: "sub-1", Source: idkit.IdentitySourceOIDC}
		err := utils.NewTransactionManager(db).WithTransaction(ctx, func(txCtx context.Context) error {
			require.NoError(t, store.Create(txCtx, identity))
			got, err := store.Get(txCtx, identity.Issuer, identity.Subject)
			require.NoError(t, err, "the read joins the transaction and sees its write")
			assert.Equal(t, identity.Subject, got.Subject)
			return errIDKitRollback
		})
		require.ErrorIs(t, err, errIDKitRollback)
		assert.Zero(t, idkitCount(t, db, "user_identities"), "the rolled-back create left no row")

		require.NoError(t, store.Create(ctx, identity))
		assert.Equal(t, int64(1), idkitCount(t, db, "user_identities"))
	})

	t.Run("returns_stored_row", func(t *testing.T) {
		repo := &idkitStubIdentityRepo{row: &models.UserIdentity{UserID: 9, Issuer: "https://idp.example", Subject: "sub-1  ", Source: models.UserIdentitySourceOIDC}}
		store := NewIDKitIdentityStore(repo)
		got, err := store.Get(ctx, "https://idp.example", "sub-1")
		require.NoError(t, err)
		assert.Equal(t, "sub-1  ", got.Subject, "the stored subject is returned unfiltered")
		assert.Equal(t, "9", got.AccountID)
	})

	t.Run("invalid_account_id", func(t *testing.T) {
		db := openIDKitHostDB(t)
		idkitCreateUser(t, db, "a@example.com")
		store := NewIDKitIdentityStore(repository.NewUserIdentityRepository(db))
		for _, id := range []string{"01", "0", "x", " 1", "+1", "", "18446744073709551616"} {
			err := store.Create(ctx, idkit.Identity{AccountID: id, Issuer: "https://idp.example", Subject: "sub-" + id, Source: idkit.IdentitySourceOIDC})
			assert.True(t, errors.Is(err, idkit.ErrInvalidAccountID), "%q: got %v", id, err)
		}
		assert.Zero(t, idkitCount(t, db, "user_identities"))
	})
}

// idkitStubIdentityRepo returns a fixed row from GetByIssuerSubject.
type idkitStubIdentityRepo struct {
	repository.UserIdentityRepository
	row *models.UserIdentity
}

func (r *idkitStubIdentityRepo) GetByIssuerSubject(string, string) (*models.UserIdentity, error) {
	copied := *r.row
	return &copied, nil
}

func (r *idkitStubIdentityRepo) WithTx(*gorm.DB) repository.UserIdentityRepository { return r }

func TestIDKitAttemptStore(t *testing.T) {
	ctx := context.Background()

	newAttempt := func(stateHash string, expiresAt time.Time) idkit.Attempt {
		return idkit.Attempt{StateHash: stateHash, Nonce: "nonce-" + stateHash, Verifier: "verifier-" + stateHash, Issuer: "https://idp.example", ExpiresAt: expiresAt}
	}

	t.Run("create_claim_once", func(t *testing.T) {
		db := openIDKitHostDB(t)
		store := NewIDKitAttemptStore(repository.NewOIDCLoginAttemptRepository(db))
		now := time.Now().UTC()
		id, err := store.Create(ctx, newAttempt("hash-1", now.Add(10*time.Minute)))
		require.NoError(t, err)
		_, err = parseIDKitID(id)
		require.NoError(t, err, "the id is canonical decimal")

		got, err := store.Claim(ctx, "hash-1", now)
		require.NoError(t, err)
		assert.Equal(t, id, got.ID)
		assert.Equal(t, "nonce-hash-1", got.Nonce)
		assert.Equal(t, "verifier-hash-1", got.Verifier)
		assert.Equal(t, "https://idp.example", got.Issuer)
		assert.WithinDuration(t, now.Add(10*time.Minute), got.ExpiresAt, time.Second)

		_, err = store.Claim(ctx, "hash-1", now)
		assert.True(t, errors.Is(err, idkit.ErrNotFound), "second claim: got %v", err)
		_, err = store.Claim(ctx, "unknown", now)
		assert.True(t, errors.Is(err, idkit.ErrNotFound), "unknown state: got %v", err)
	})

	t.Run("claim_expired", func(t *testing.T) {
		db := openIDKitHostDB(t)
		store := NewIDKitAttemptStore(repository.NewOIDCLoginAttemptRepository(db))
		now := time.Now().UTC()
		_, err := store.Create(ctx, newAttempt("hash-1", now.Add(-time.Minute)))
		require.NoError(t, err)
		_, err = store.Claim(ctx, "hash-1", now)
		assert.True(t, errors.Is(err, idkit.ErrNotFound), "got %v", err)
	})

	t.Run("prune", func(t *testing.T) {
		db := openIDKitHostDB(t)
		store := NewIDKitAttemptStore(repository.NewOIDCLoginAttemptRepository(db))
		now := time.Now().UTC()
		for i, created := range []time.Time{now.Add(-3 * time.Hour), now.Add(-2 * time.Hour), now} {
			attempt := newAttempt(fmt.Sprintf("hash-%d", i), now.Add(time.Hour))
			attempt.CreatedAt = created
			_, err := store.Create(ctx, attempt)
			require.NoError(t, err)
		}
		deleted, err := store.Prune(ctx, now.Add(-time.Hour), 100)
		require.NoError(t, err)
		assert.Equal(t, int64(2), deleted)
		var left []models.OIDCLoginAttempt
		require.NoError(t, db.Find(&left).Error)
		require.Len(t, left, 1)
		assert.Equal(t, "hash-2", left[0].StateHash)
	})

	t.Run("refuses_inside_transaction", func(t *testing.T) {
		db := openIDKitHostDB(t)
		store := NewIDKitAttemptStore(repository.NewOIDCLoginAttemptRepository(db))
		now := time.Now().UTC()
		attempt := newAttempt("hash-1", now.Add(10*time.Minute))
		attempt.CreatedAt = now.Add(-2 * time.Hour)
		_, err := store.Create(ctx, attempt)
		require.NoError(t, err)

		var createErr, claimErr, pruneErr error
		err = utils.NewTransactionManager(db).WithTransaction(ctx, func(txCtx context.Context) error {
			_, createErr = store.Create(txCtx, newAttempt("hash-2", now.Add(10*time.Minute)))
			_, claimErr = store.Claim(txCtx, "hash-1", now)
			_, pruneErr = store.Prune(txCtx, now, 100)
			return nil
		})
		require.NoError(t, err)
		assert.True(t, errors.Is(createErr, idkit.ErrInTransaction), "create: got %v", createErr)
		assert.True(t, errors.Is(claimErr, idkit.ErrInTransaction), "claim: got %v", claimErr)
		assert.True(t, errors.Is(pruneErr, idkit.ErrInTransaction), "prune: got %v", pruneErr)

		assert.Equal(t, int64(1), idkitCount(t, db, "oidc_login_attempts"))
		var row models.OIDCLoginAttempt
		require.NoError(t, db.Where("state_hash = ?", "hash-1").First(&row).Error)
		assert.Nil(t, row.UsedAt, "the attempt is still unused")
	})
}

func TestIDKitSessionIssuer(t *testing.T) {
	ctx := context.Background()

	newIssuer := func(t *testing.T, db *gorm.DB) (*authService, idkit.SessionIssuer) {
		t.Helper()
		userRepo := repository.NewUserRepository(db)
		refreshRepo := repository.NewRefreshTokenRepository(db)
		auth := NewAuthServiceWithSessions(userRepo, repository.NewAPIKeyRepository(db), refreshRepo,
			repository.NewPasswordResetTokenRepository(db), utils.NewTransactionManager(db), nil,
			config.JWTConfig{Secret: oidcTestJWTSecret, ExpiryHours: 1, RefreshTokenDays: 30},
			oidcTestAppBaseURL, oidcTestAPIKeySecret).(*authService)
		issuer, err := NewIDKitSessionIssuer(auth, userRepo, refreshRepo)
		require.NoError(t, err)
		return auth, issuer
	}

	t.Run("records_the_provider_session", func(t *testing.T) {
		db := openIDKitHostDB(t)
		user := idkitCreateUser(t, db, "sid@example.com")
		auth, issuer := newIssuer(t, db)
		accountID := strconv.FormatUint(uint64(user.ID), 10)
		const iss = "https://issuer.example.test"
		for sid, want := range map[string]string{
			"sid-host-0634": hashOpaqueToken(iss+"\n"+"sid-host-0634", auth.sessionTokenSecret()),
			"":              "",
		} {
			session, err := issuer.Issue(ctx, idkit.SessionRequest{AccountID: accountID, Method: idkit.SignInMethodOIDC,
				SignedInAt: time.Now(), Issuer: iss, ProviderSessionID: sid})
			require.NoError(t, err)
			assert.Empty(t, session.IDToken, "the issuer never sets the ID token")
			var row models.RefreshToken
			require.NoError(t, db.Where("token_hash = ?", hashOpaqueToken(session.RefreshToken, auth.sessionTokenSecret())).First(&row).Error)
			assert.Equal(t, want, row.ProviderSessionHash, "sid %q", sid)
		}
	})

	t.Run("issues_oidc_session", func(t *testing.T) {
		db := openIDKitHostDB(t)
		user := idkitCreateUser(t, db, "a@example.com")
		auth, issuer := newIssuer(t, db)
		signedInAt := time.Now().UTC().Add(-23 * time.Hour).Truncate(time.Second)
		accountID := strconv.FormatUint(uint64(user.ID), 10)

		session, err := issuer.Issue(ctx, idkit.SessionRequest{AccountID: accountID, Method: idkit.SignInMethodOIDC, SignedInAt: signedInAt})
		require.NoError(t, err)
		assert.Equal(t, accountID, session.AccountID)

		validated, err := auth.ValidateToken(session.AccessToken)
		require.NoError(t, err)
		assert.Equal(t, user.ID, validated.ID)

		var row models.RefreshToken
		require.NoError(t, db.Where("token_hash = ?", hashOpaqueToken(session.RefreshToken, auth.sessionTokenSecret())).First(&row).Error)
		assert.Equal(t, user.ID, row.UserID)
		assert.Equal(t, models.RefreshTokenSignInOIDC, row.SignInMethod)
		assert.Len(t, row.FamilyID, 32)
		require.NotNil(t, row.SessionStartedAt)
		assert.True(t, row.SessionStartedAt.UTC().Equal(signedInAt), "session start %v, want %v", row.SessionStartedAt.UTC(), signedInAt)
		assert.WithinDuration(t, signedInAt.Add(24*time.Hour), row.ExpiresAt.UTC(), time.Second,
			"the lifetime cap follows the sign-in time")
	})

	t.Run("joins_transaction", func(t *testing.T) {
		db := openIDKitHostDB(t)
		user := idkitCreateUser(t, db, "a@example.com")
		_, issuer := newIssuer(t, db)
		err := utils.NewTransactionManager(db).WithTransaction(ctx, func(txCtx context.Context) error {
			_, err := issuer.Issue(txCtx, idkit.SessionRequest{AccountID: strconv.FormatUint(uint64(user.ID), 10), Method: idkit.SignInMethodOIDC, SignedInAt: time.Now()})
			require.NoError(t, err)
			return errIDKitRollback
		})
		require.ErrorIs(t, err, errIDKitRollback)
		assert.Zero(t, idkitCount(t, db, "refresh_tokens"))
	})

	t.Run("refuses_input", func(t *testing.T) {
		db := openIDKitHostDB(t)
		user := idkitCreateUser(t, db, "a@example.com")
		_, issuer := newIssuer(t, db)
		accountID := strconv.FormatUint(uint64(user.ID), 10)
		for name, req := range map[string]idkit.SessionRequest{
			"password_method":  {AccountID: accountID, Method: "password", SignedInAt: time.Now()},
			"zero_signed_in":   {AccountID: accountID, Method: idkit.SignInMethodOIDC},
			"leading_zero_id":  {AccountID: "0" + accountID, Method: idkit.SignInMethodOIDC, SignedInAt: time.Now()},
			"missing_account":  {AccountID: strconv.FormatUint(uint64(user.ID)+1000, 10), Method: idkit.SignInMethodOIDC, SignedInAt: time.Now()},
			"empty_account_id": {Method: idkit.SignInMethodOIDC, SignedInAt: time.Now()},
		} {
			_, err := issuer.Issue(ctx, req)
			assert.Error(t, err, name)
		}
		assert.Zero(t, idkitCount(t, db, "refresh_tokens"))
	})

	t.Run("not_wired", func(t *testing.T) {
		db := openIDKitHostDB(t)
		issuer, err := NewIDKitSessionIssuer(idkitOtherAuth{}, repository.NewUserRepository(db), repository.NewRefreshTokenRepository(db))
		assert.ErrorIs(t, err, errOIDCNotWired)
		assert.Nil(t, issuer)
	})
}

// idkitOtherAuth is an AuthService that is not the session-issuing one.
type idkitOtherAuth struct{ AuthService }

func TestIDKitAuditSink(t *testing.T) {
	ctx := context.Background()

	t.Run("records_event", func(t *testing.T) {
		db := openIDKitHostDB(t)
		sink := NewIDKitAuditSink(NewAuthAuditService(repository.NewAuthAuditRepository(db)))
		id := sink.Record(ctx, idkit.AuditEvent{
			Method: AuthAuditMethodOIDC, Outcome: AuthAuditOutcomeRefused, Reason: AuthAuditReasonStateInvalid,
			AttemptID: "7", ClaimGroups: []string{"g1", "g2"}, ClaimRoles: []string{"r1"},
		})
		require.NotEmpty(t, id)
		var rows []models.AuthAuditLog
		require.NoError(t, db.Find(&rows).Error)
		require.Len(t, rows, 1)
		row := rows[0]
		assert.Equal(t, strconv.FormatUint(uint64(row.ID), 10), id)
		assert.Equal(t, "oidc", row.Method)
		assert.Equal(t, "refused", row.Outcome)
		assert.Equal(t, "state_invalid", row.Reason)
		require.NotNil(t, row.AttemptID)
		assert.Equal(t, uint(7), *row.AttemptID)
		assert.Nil(t, row.UserID)
		require.NotNil(t, row.ClaimGroups)
		require.NotNil(t, row.ClaimRoles)
		var groups, roles []string
		require.NoError(t, json.Unmarshal([]byte(*row.ClaimGroups), &groups))
		require.NoError(t, json.Unmarshal([]byte(*row.ClaimRoles), &roles))
		assert.Equal(t, []string{"g1", "g2"}, groups)
		assert.Equal(t, []string{"r1"}, roles)
	})

	t.Run("unregistered_values", func(t *testing.T) {
		db := openIDKitHostDB(t)
		hook := idkitCaptureLogs(t)
		sink := NewIDKitAuditSink(NewAuthAuditService(repository.NewAuthAuditRepository(db)))
		for name, event := range map[string]idkit.AuditEvent{
			"method":  {Method: "unregistered", Outcome: AuthAuditOutcomeRefused, Reason: AuthAuditReasonStateInvalid},
			"outcome": {Method: AuthAuditMethodOIDC, Outcome: "unregistered", Reason: AuthAuditReasonStateInvalid},
			"reason":  {Method: AuthAuditMethodOIDC, Outcome: AuthAuditOutcomeRefused, Reason: "unregistered"},
		} {
			assert.Empty(t, sink.Record(ctx, event), name)
		}
		assert.Zero(t, idkitCount(t, db, "auth_audit_log"))
		assert.Empty(t, hook.AllEntries(), "no log line")
	})

	t.Run("invalid_ids", func(t *testing.T) {
		db := openIDKitHostDB(t)
		sink := NewIDKitAuditSink(NewAuthAuditService(repository.NewAuthAuditRepository(db)))
		for _, event := range []idkit.AuditEvent{
			{Method: AuthAuditMethodOIDC, Outcome: AuthAuditOutcomeRefused, Reason: AuthAuditReasonStateInvalid, AttemptID: "07"},
			{Method: AuthAuditMethodOIDC, Outcome: AuthAuditOutcomeRefused, Reason: AuthAuditReasonStateInvalid, AccountID: "0"},
		} {
			assert.Empty(t, sink.Record(ctx, event))
		}
		assert.Zero(t, idkitCount(t, db, "auth_audit_log"))
	})

	t.Run("nil_service", func(t *testing.T) {
		sink := NewIDKitAuditSink(nil)
		assert.Empty(t, sink.Record(ctx, idkit.AuditEvent{Method: AuthAuditMethodOIDC, Outcome: AuthAuditOutcomeRefused, Reason: AuthAuditReasonStateInvalid}))
	})

	t.Run("inside_transaction", func(t *testing.T) {
		db := openIDKitHostDB(t)
		sink := NewIDKitAuditSink(NewAuthAuditService(repository.NewAuthAuditRepository(db)))
		var id string
		err := utils.NewTransactionManager(db).WithTransaction(ctx, func(txCtx context.Context) error {
			id = sink.Record(txCtx, idkit.AuditEvent{Method: AuthAuditMethodOIDC, Outcome: AuthAuditOutcomeRefused, Reason: AuthAuditReasonStateInvalid})
			return nil
		})
		require.NoError(t, err)
		assert.Empty(t, id)
		assert.Zero(t, idkitCount(t, db, "auth_audit_log"))
	})
}

// idkitStubSSO is an SSOSettingsService whose SSOConfig returns fixed values
// and counts its calls.
type idkitStubSSO struct {
	SSOSettingsService
	cfg   EffectiveSSOConfig
	err   error
	calls int
}

func (s *idkitStubSSO) SSOConfig(context.Context) (EffectiveSSOConfig, error) {
	s.calls++
	return s.cfg, s.err
}

func idkitTestCertificate(t *testing.T) *SSOClientCertificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &SSOClientCertificate{Certificate: cert, PrivateKey: key, ThumbprintSHA1: "AB", NotAfter: cert.NotAfter}
}

func TestIDKitConfigSource(t *testing.T) {
	ctx := context.Background()
	full := func(t *testing.T) EffectiveSSOConfig {
		return EffectiveSSOConfig{
			Enabled:             true,
			AllowPassword:       true,
			IssuerURL:           "https://idp.example/tenant",
			ClientID:            "client-id",
			ClientSecret:        "client-value",
			Scopes:              []string{"openid", "email", "profile"},
			RoleMapping:         []SSORoleMapping{{Claim: SSOClaimGroups, Value: "g1", RoleID: 3}, {Claim: SSOClaimRoles, Value: "r1", RoleID: 4}},
			GraphMaxPages:       7,
			EntraAppSigningKeys: true,
			ClientCertificate:   idkitTestCertificate(t),
		}
	}

	t.Run("copies_fields", func(t *testing.T) {
		src := full(t)
		got, err := NewIDKitConfigSource(&idkitStubSSO{cfg: src}).Config(ctx)
		require.NoError(t, err)
		assert.Equal(t, src.Enabled, got.Enabled)
		assert.Equal(t, src.IssuerURL, got.IssuerURL)
		assert.Equal(t, src.ClientID, got.ClientID)
		assert.Equal(t, src.ClientSecret, got.ClientSecret)
		assert.Equal(t, src.Scopes, got.Scopes)
		assert.Equal(t, []rolemap.Rule(src.RoleMapping), got.RoleMapping)
		assert.Equal(t, src.GraphMaxPages, got.GraphMaxPages)
		assert.Equal(t, src.EntraAppSigningKeys, got.EntraAppSigningKeys)
		require.NotNil(t, got.ClientCertificate)
		assert.Same(t, src.ClientCertificate.Certificate, got.ClientCertificate.Certificate)
		assert.Same(t, src.ClientCertificate.PrivateKey, got.ClientCertificate.PrivateKey)

		src.ClientCertificate = nil
		got, err = NewIDKitConfigSource(&idkitStubSSO{cfg: src}).Config(ctx)
		require.NoError(t, err)
		assert.Nil(t, got.ClientCertificate)
	})

	t.Run("copies_slices", func(t *testing.T) {
		src := full(t)
		got, err := NewIDKitConfigSource(&idkitStubSSO{cfg: src}).Config(ctx)
		require.NoError(t, err)
		got.Scopes[0] = "changed"
		got.RoleMapping[0].Value = "changed"
		assert.Equal(t, "openid", src.Scopes[0])
		assert.Equal(t, "g1", src.RoleMapping[0].Value)
	})

	t.Run("returns_error", func(t *testing.T) {
		want := errors.New("settings unavailable")
		_, err := NewIDKitConfigSource(&idkitStubSSO{err: want}).Config(ctx)
		assert.Same(t, want, err)
	})

	t.Run("inside_transaction", func(t *testing.T) {
		db := openIDKitHostDB(t)
		stub := &idkitStubSSO{cfg: full(t)}
		source := NewIDKitConfigSource(stub)
		var cfgErr error
		require.NoError(t, utils.NewTransactionManager(db).WithTransaction(ctx, func(txCtx context.Context) error {
			_, cfgErr = source.Config(txCtx)
			return nil
		}))
		assert.True(t, errors.Is(cfgErr, idkit.ErrInTransaction), "got %v", cfgErr)
		assert.Zero(t, stub.calls)
	})
}

func TestIDKitLogger(t *testing.T) {
	hook := idkitCaptureLogs(t)
	log := NewIDKitLogger("idkit_test")
	ctx := utils.WithRequestID(context.Background(), "req-1")
	log.Info(ctx, "info message", idkit.Field{Key: "count", Value: 2})
	log.Warn(ctx, "warn message", idkit.Field{Key: "code", Value: "c"}, idkit.Field{Key: "attempt_id", Value: "7"})

	entries := hook.AllEntries()
	require.Len(t, entries, 2)
	keys := func(entry *logrus.Entry) []string {
		var out []string
		for key := range entry.Data {
			out = append(out, key)
		}
		sort.Strings(out)
		return out
	}
	assert.Equal(t, logrus.InfoLevel, entries[0].Level)
	assert.Equal(t, "info message", entries[0].Message)
	assert.Equal(t, []string{"component", "count", "request_id"}, keys(entries[0]))
	assert.Equal(t, "idkit_test", entries[0].Data["component"])
	assert.Equal(t, "req-1", entries[0].Data["request_id"])
	assert.Equal(t, 2, entries[0].Data["count"])

	assert.Equal(t, logrus.WarnLevel, entries[1].Level)
	assert.Equal(t, "warn message", entries[1].Message)
	assert.Equal(t, []string{"attempt_id", "code", "component", "request_id"}, keys(entries[1]))
	assert.False(t, strings.Contains(entries[1].Message, "req-1"))
}

func TestIDKitTxRunner(t *testing.T) {
	ctx := context.Background()
	db := openIDKitHostDB(t)
	user := idkitCreateUser(t, db, "a@example.com")
	var runner idkit.TxRunner = utils.NewTransactionManager(db)
	store := NewIDKitIdentityStore(repository.NewUserIdentityRepository(db))
	accountID := strconv.FormatUint(uint64(user.ID), 10)

	err := runner.WithTransaction(ctx, func(txCtx context.Context) error {
		require.NoError(t, store.Create(txCtx, idkit.Identity{AccountID: accountID, Issuer: "https://idp.example", Subject: "rolled-back", Source: idkit.IdentitySourceOIDC}))
		return errIDKitRollback
	})
	require.ErrorIs(t, err, errIDKitRollback)
	assert.Zero(t, idkitCount(t, db, "user_identities"))

	require.NoError(t, runner.WithTransaction(ctx, func(txCtx context.Context) error {
		return store.Create(txCtx, idkit.Identity{AccountID: accountID, Issuer: "https://idp.example", Subject: "committed", Source: idkit.IdentitySourceOIDC})
	}))
	_, err = store.Get(ctx, "https://idp.example", "committed")
	require.NoError(t, err)
	assert.Equal(t, int64(1), idkitCount(t, db, "user_identities"))
}
