package service

// Customer external identity mappings (#212): validation, the column widths,
// the unwired service and the log lines.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/glebarez/sqlite"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

// openExternalIDSQLite is a file-backed SQLite database with the production
// pragmas, the one-connection pool and the full schema of MigrateDatabase.
func openExternalIDSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "external-ids.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	migrateExternalIDSchema(t, db)
	return db
}

// migrateExternalIDSchema runs the application's MigrateDatabase on db. It
// works off the package-level handle, which is swapped for the call.
func migrateExternalIDSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	previous := models.DB
	models.DB = db
	err := models.MigrateDatabase()
	models.DB = previous
	require.NoError(t, err)
}

func seedExternalIDServiceCustomer(t *testing.T, db *gorm.DB, label string) *models.Customer {
	t.Helper()
	customer := &models.Customer{FirstName: "Svc" + label, LastName: "Customer", Email: "svc-" + label + "@external-id.test"}
	require.NoError(t, db.Create(customer).Error)
	return customer
}

// countingExternalIDRepo counts every call, so a test can prove the service
// refused before touching its repository.
type countingExternalIDRepo struct {
	calls atomic.Int32
}

func (r *countingExternalIDRepo) LockLiveCustomer(context.Context, uint) error {
	r.calls.Add(1)
	return nil
}

func (r *countingExternalIDRepo) Create(context.Context, *models.CustomerExternalID) error {
	r.calls.Add(1)
	return nil
}

func (r *countingExternalIDRepo) FindCustomerByIdentity(context.Context, string, string, string) (*models.Customer, error) {
	r.calls.Add(1)
	return nil, nil
}

func (r *countingExternalIDRepo) DeleteForCustomer(context.Context, uint, uint) error {
	r.calls.Add(1)
	return nil
}

func (r *countingExternalIDRepo) WithTx(*gorm.DB) repository.CustomerExternalIDRepository { return r }

// countStatements counts every statement GORM runs on db.
func countStatements(t *testing.T, db *gorm.DB) *atomic.Int32 {
	t.Helper()
	count := &atomic.Int32{}
	hook := func(*gorm.DB) { count.Add(1) }
	name := "customer_external_id_test:count"
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(name, hook))
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(name, hook))
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(name, hook))
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register(name, hook))
	require.NoError(t, db.Callback().Raw().Before("gorm:raw").Register(name, hook))
	require.NoError(t, db.Callback().Row().Before("gorm:row").Register(name, hook))
	return count
}

func TestCustomerExternalIDValidation(t *testing.T) {
	ok := func(source, scope, externalID string) {
		t.Helper()
		assert.NoError(t, ValidateCustomerExternalIdentity(source, scope, externalID), "%q %q %q", source, scope, externalID)
	}
	ok("shopify", "", "1")
	ok("shopify", "store-eu", "gid://shopify/Customer/1001")
	ok(strings.Repeat("s", 64), strings.Repeat("c", 128), strings.Repeat("e", 255))
	// Lengths are counted in runes: 64 two-byte runes are 128 bytes.
	ok(strings.Repeat("é", 64), strings.Repeat("ß", 128), strings.Repeat("ü", 255))
	ok("a b", "x y", "inner spaces are kept")
	ok("Shop-A", "", "X1")

	cases := []struct {
		name, source, scope, externalID, field, reasonFragment string
	}{
		{"source missing", "", "", "1", "source", "required"},
		{"source too long", strings.Repeat("é", 65), "", "1", "source", "at most 64"},
		{"source leading space", " shop", "", "1", "source", "whitespace"},
		{"source trailing space", "shop ", "", "1", "source", "whitespace"},
		{"source tab", "sh\top", "", "1", "source", "control"},
		{"source invalid utf8", "sh\xffop", "", "1", "source", "UTF-8"},
		{"scope too long", "shop", strings.Repeat("c", 129), "1", "scope", "at most 128"},
		{"scope trailing newline", "shop", "eu\n", "1", "scope", "control"},
		{"scope leading nbsp", "shop", " eu", "1", "scope", "whitespace"},
		{"scope only space", "shop", " ", "1", "scope", "whitespace"},
		{"external_id missing", "shop", "", "", "external_id", "required"},
		{"external_id too long", "shop", "", strings.Repeat("e", 256), "external_id", "at most 255"},
		{"external_id control", "shop", "", "a\x00b", "external_id", "control"},
		{"external_id del", "shop", "", "a\x7fb", "external_id", "control"},
		{"external_id trailing space", "shop", "", "X1 ", "external_id", "whitespace"},
		{"external_id invalid utf8", "shop", "", "\xc3", "external_id", "UTF-8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCustomerExternalIdentity(tc.source, tc.scope, tc.externalID)
			require.Error(t, err)
			var fieldErr *CustomerExternalIDFieldError
			require.True(t, errors.As(err, &fieldErr), "a *CustomerExternalIDFieldError, got %T", err)
			assert.Equal(t, tc.field, fieldErr.Field)
			assert.Contains(t, fieldErr.Reason, tc.reasonFragment)
			assert.True(t, errors.Is(err, apperrors.ErrValidation), "matches apperrors.ErrValidation")
			for _, value := range []string{tc.source, tc.scope, tc.externalID} {
				if trimmed := strings.TrimSpace(value); len(trimmed) > 2 {
					assert.NotContains(t, err.Error(), trimmed, "the error never carries the value")
					assert.NotContains(t, utils.LogError(err).Error(), trimmed)
				}
			}
		})
	}

	// The service validates before any read or write.
	db := openExternalIDSQLite(t)
	repo := &countingExternalIDRepo{}
	statements := countStatements(t, db)
	svc := NewCustomerExternalIDService(repo, utils.NewTransactionManager(db))
	ctx := context.Background()
	for _, tc := range cases {
		_, err := svc.AddExternalID(ctx, 1, tc.source, tc.scope, tc.externalID)
		assert.True(t, errors.Is(err, apperrors.ErrValidation), "AddExternalID %s: %v", tc.name, err)
		customer, err := svc.LookupCustomer(ctx, tc.source, tc.scope, tc.externalID)
		assert.Nil(t, customer)
		assert.True(t, errors.Is(err, apperrors.ErrValidation), "LookupCustomer %s: %v", tc.name, err)
	}
	assert.Zero(t, repo.calls.Load(), "a refused input reaches no repository method")
	assert.Zero(t, statements.Load(), "a refused input runs no statement")
}

func TestCustomerExternalIDMaxLengthsMatchTheColumns(t *testing.T) {
	parsed, err := schema.Parse(&models.CustomerExternalID{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	width := regexp.MustCompile(`(?i)^varchar\((\d+)\)$`)
	columnWidth := func(column string) int {
		field := parsed.LookUpField(column)
		require.NotNil(t, field, column)
		m := width.FindStringSubmatch(string(field.DataType))
		require.Len(t, m, 2, "%s is a varchar(n) column, got %q", column, field.DataType)
		n, err := strconv.Atoi(m[1])
		require.NoError(t, err)
		return n
	}
	assert.Equal(t, columnWidth("source"), CustomerExternalIDSourceMaxLength)
	assert.Equal(t, columnWidth("scope"), CustomerExternalIDScopeMaxLength)
	assert.Equal(t, columnWidth("external_id"), CustomerExternalIDValueMaxLength)
	assert.Equal(t, 64, CustomerExternalIDSourceMaxLength)
	assert.Equal(t, 128, CustomerExternalIDScopeMaxLength)
	assert.Equal(t, 255, CustomerExternalIDValueMaxLength)

	// The limits are what validation enforces.
	require.NoError(t, ValidateCustomerExternalIdentity(strings.Repeat("s", CustomerExternalIDSourceMaxLength),
		strings.Repeat("c", CustomerExternalIDScopeMaxLength), strings.Repeat("e", CustomerExternalIDValueMaxLength)))
	assert.Error(t, ValidateCustomerExternalIdentity(strings.Repeat("s", CustomerExternalIDSourceMaxLength+1), "", "e"))
	assert.Error(t, ValidateCustomerExternalIdentity("s", strings.Repeat("c", CustomerExternalIDScopeMaxLength+1), "e"))
	assert.Error(t, ValidateCustomerExternalIdentity("s", "", strings.Repeat("e", CustomerExternalIDValueMaxLength+1)))
}

func TestCustomerExternalIDServiceRefusedWhenUnwired(t *testing.T) {
	db := openExternalIDSQLite(t)
	statements := countStatements(t, db)
	repo := &countingExternalIDRepo{}
	tm := utils.NewTransactionManager(db)
	ctx := context.Background()

	for name, svc := range map[string]CustomerExternalIDService{
		"no repository or manager": NewCustomerExternalIDService(nil, nil),
		"no manager":               NewCustomerExternalIDService(repo, nil),
		"no repository":            NewCustomerExternalIDService(nil, tm),
	} {
		t.Run(name, func(t *testing.T) {
			mapping, err := svc.AddExternalID(ctx, 1, "shop", "", "X1")
			assert.Nil(t, mapping)
			assert.ErrorIs(t, err, ErrCustomerExternalIDsUnavailable)
			assert.ErrorIs(t, svc.RemoveExternalID(ctx, 1, 1), ErrCustomerExternalIDsUnavailable)
			customer, err := svc.LookupCustomer(ctx, "shop", "", "X1")
			assert.Nil(t, customer)
			assert.ErrorIs(t, err, ErrCustomerExternalIDsUnavailable)
		})
	}
	assert.Zero(t, repo.calls.Load(), "the repository is never called")
	assert.Zero(t, statements.Load(), "the database is never touched")
	var mappings int64
	require.NoError(t, db.Model(&models.CustomerExternalID{}).Count(&mappings).Error)
	assert.Zero(t, mappings)
}

func TestCustomerExternalIDLogsOmitValues(t *testing.T) {
	// The package init() leaves the logger at Warn; raise it to Debug for the
	// capture, or the Info and Debug lines would never be examined.
	var buf bytes.Buffer
	previousLevel, previousOut, previousFormatter := utils.Logger.GetLevel(), utils.Logger.Out, utils.Logger.Formatter
	utils.Logger.SetOutput(&buf)
	utils.Logger.SetLevel(logrus.DebugLevel)
	utils.Logger.SetFormatter(&logrus.JSONFormatter{})
	t.Cleanup(func() {
		utils.Logger.SetOutput(previousOut)
		utils.Logger.SetLevel(previousLevel)
		utils.Logger.SetFormatter(previousFormatter)
	})

	db := openExternalIDSQLite(t)
	svc := NewCustomerExternalIDService(repository.NewCustomerExternalIDRepository(db), utils.NewTransactionManager(db))
	ctx := context.Background()
	a := seedExternalIDServiceCustomer(t, db, "logs-a")
	b := seedExternalIDServiceCustomer(t, db, "logs-b")

	const (
		source     = "CanarySourceQ7"
		scope      = "CanaryScopeZ9"
		externalID = "CanaryExternalK3"
	)

	// Create.
	mapping, err := svc.AddExternalID(ctx, a.ID, source, scope, externalID)
	require.NoError(t, err)
	// Duplicate refusals, for the same and for another customer.
	_, err = svc.AddExternalID(ctx, a.ID, source, scope, externalID)
	require.ErrorIs(t, err, apperrors.ErrCustomerExternalIDTaken)
	_, err = svc.AddExternalID(ctx, b.ID, source, scope, externalID)
	require.ErrorIs(t, err, apperrors.ErrCustomerExternalIDTaken)
	// A missing customer.
	_, err = svc.AddExternalID(ctx, 99999999, source, scope, externalID+"Other")
	require.ErrorIs(t, err, apperrors.ErrNotFound)
	// Validation refusals, the refused value carrying the canary.
	_, err = svc.AddExternalID(ctx, a.ID, source+" ", scope, externalID)
	require.ErrorIs(t, err, apperrors.ErrValidation)
	_, err = svc.AddExternalID(ctx, a.ID, source, scope+"\n", externalID)
	require.ErrorIs(t, err, apperrors.ErrValidation)
	_, err = svc.AddExternalID(ctx, a.ID, source, scope, externalID+strings.Repeat("x", 300))
	require.ErrorIs(t, err, apperrors.ErrValidation)
	// Lookups: a hit, a miss, a refusal.
	found, err := svc.LookupCustomer(ctx, source, scope, externalID)
	require.NoError(t, err)
	require.NotNil(t, found)
	found, err = svc.LookupCustomer(ctx, source, scope, externalID+"Miss")
	require.NoError(t, err)
	require.Nil(t, found)
	_, err = svc.LookupCustomer(ctx, "", scope, externalID)
	require.ErrorIs(t, err, apperrors.ErrValidation)
	_, err = svc.LookupCustomer(ctx, source, scope, " "+externalID)
	require.ErrorIs(t, err, apperrors.ErrValidation)
	// Deletes: another customer's id, then the real one.
	require.ErrorIs(t, svc.RemoveExternalID(ctx, b.ID, mapping.ID), apperrors.ErrNotFound)
	require.NoError(t, svc.RemoveExternalID(ctx, a.ID, mapping.ID))

	output := buf.String()
	require.NotEmpty(t, output, "the service paths wrote log lines")
	for _, op := range []string{"AddExternalID", "RemoveExternalID", "LookupCustomer"} {
		assert.Contains(t, output, op, "the operation is named")
	}
	assert.Contains(t, output, fmt.Sprintf(`"customer_id":%d`, a.ID))
	assert.Contains(t, output, fmt.Sprintf(`"mapping_id":%d`, mapping.ID))
	for _, needle := range []string{"Canary", "CanarySourceQ7", "CanaryScopeZ9", "CanaryExternalK3"} {
		assert.NotContains(t, output, needle, "no log line carries a submitted or stored value")
	}
	assert.NotContains(t, strings.ToLower(output), "canary")
}
