package integration

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The whole-database personal-data sweep.
//
// Every other test in this suite checks the table it already suspects: it erases
// a user and looks at "users", erases a customer and looks at "customers". That
// is exactly the assumption that let the leads leak survive review — erasing a
// converted customer left a complete copy of the person (name, email, phone,
// company) in the lead it came from, and no assertion anywhere was looking at
// the leads table. Worse, the erasure test database did not even create it.
//
// The sweep below makes the opposite assumption: after an erasure, the person's
// email, first name, last name and phone number must not appear ANYWHERE in the
// database. It enumerates the tables from the live schema rather than from a
// hand-written list, and it reads every row of every one of them — including
// soft-deleted rows, which an ordinary query hides and which are precisely where
// an unerased copy would be sitting. A table added to models.MigrateDatabase
// tomorrow is swept from the moment it exists, without anybody remembering to
// add it here.

// setupFullSchemaDB creates a database with the application's REAL schema, by
// running the very migration cmd/main.go runs. Nothing is hand-picked: if the
// application stores something somewhere, the sweep sees that table.
func setupFullSchemaDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupDB(t)

	// models.MigrateDatabase works off the package-level handle. It is swapped
	// for the duration of the call and put straight back, so the global is
	// exactly as it was for every other test.
	previous := models.DB
	models.DB = db
	err := models.MigrateDatabase()
	models.DB = previous
	require.NoError(t, err)

	return db
}

// erasureSubject is one identifiable person. Every field is tagged with the name
// of the test that owns it, so a hit found by the sweep can only have come from
// that person's own records and never from another fixture.
type erasureSubject struct {
	Email      string
	FirstName  string
	LastName   string
	Phone      string
	Company    string
	Position   string
	Notes      string
	ExternalID string
}

func newErasureSubject(tag string) erasureSubject {
	return erasureSubject{
		Email:      fmt.Sprintf("zoltan.%s@example.com", tag),
		FirstName:  "Zoltan-" + tag,
		LastName:   "Marinescu-" + tag,
		Phone:      "+40 799 " + tag,
		Company:    "Marinescu-" + tag + " SRL",
		Position:   "Proprietor-" + tag,
		Notes:      "Ring the mobile after six; do not write to the office address. Ref " + tag,
		ExternalID: "crm-import-" + tag,
	}
}

// identifiers are the four strings the sweep hunts for: the ones that single the
// person out no matter which table they turn up in.
func (s erasureSubject) identifiers() []string {
	return []string{s.Email, s.FirstName, s.LastName, s.Phone}
}

func (s erasureSubject) asUser(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()
	lastLogin := time.Now().Add(-time.Hour)
	user := &models.User{
		Email:       s.Email,
		Password:    "$2a$04$notarealhashbutlongenoughtolooklikeone",
		FirstName:   s.FirstName,
		LastName:    s.LastName,
		Role:        models.RoleSales,
		IsActive:    true,
		LastLoginAt: &lastLogin,
	}
	require.NoError(t, db.Create(user).Error)
	return user
}

func (s erasureSubject) asCustomer(t *testing.T, db *gorm.DB) *models.Customer {
	t.Helper()
	customer := &models.Customer{
		FirstName: s.FirstName,
		LastName:  s.LastName,
		Email:     s.Email,
		Phone:     s.Phone,
		Company:   s.Company,
		Position:  s.Position,
		Address:   "12 Privacy Lane",
		City:      "Bucharest",
		Country:   "Romania",
		Notes:     s.Notes,
	}
	require.NoError(t, db.Create(customer).Error)
	return customer
}

func (s erasureSubject) asLead(t *testing.T, db *gorm.DB, ownerID uint) *models.Lead {
	t.Helper()
	lead := &models.Lead{
		FirstName:      s.FirstName,
		LastName:       s.LastName,
		Email:          s.Email,
		Phone:          s.Phone,
		Company:        s.Company,
		Position:       s.Position,
		Source:         "trade show",
		Status:         models.LeadStatusQualified,
		Classification: models.LeadClassificationHotLead,
		ExternalID:     s.ExternalID,
		Notes:          s.Notes,
		OwnerID:        ownerID,
	}
	require.NoError(t, db.Create(lead).Error)
	return lead
}

// personalDataHit is one place a forbidden string was found.
type personalDataHit struct {
	Table  string
	Column string
	Needle string
	Value  string
}

func (h personalDataHit) String() string {
	value := h.Value
	if len(value) > 80 {
		value = value[:80] + "..."
	}
	return fmt.Sprintf("%s.%s still holds %q (value: %q)", h.Table, h.Column, h.Needle, value)
}

// sweepDatabaseFor reads EVERY row of EVERY table in the live schema and returns
// every place one of the needles still appears.
//
// Deliberately model-free: the query goes through Table(), so GORM applies no
// soft-delete scope and no field selection. A soft-deleted row that still holds
// the person — the exact shape of the leads leak — is read like any other.
func sweepDatabaseFor(t *testing.T, db *gorm.DB, needles []string) []personalDataHit {
	t.Helper()
	require.NotEmpty(t, needles, "a sweep with no needles would pass unconditionally")

	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	require.NotEmpty(t, tables, "the sweep found no tables at all")

	var hits []personalDataHit
	for _, table := range tables {
		if strings.HasPrefix(table, "sqlite_") {
			continue // the driver's own bookkeeping, not application data
		}

		var rows []map[string]interface{}
		require.NoError(t, db.Table(table).Find(&rows).Error, "reading %s", table)

		for _, row := range rows {
			for column, value := range row {
				if value == nil {
					continue
				}
				text := fmt.Sprintf("%v", value)
				if b, ok := value.([]byte); ok {
					text = string(b)
				}
				for _, needle := range needles {
					if needle == "" {
						continue
					}
					if strings.Contains(strings.ToLower(text), strings.ToLower(needle)) {
						hits = append(hits, personalDataHit{Table: table, Column: column, Needle: needle, Value: text})
					}
				}
			}
		}
	}
	return hits
}

// assertNoPersonalDataAnywhere is the headline assertion: after the erasure the
// person is not in the database. Anywhere.
func assertNoPersonalDataAnywhere(t *testing.T, db *gorm.DB, needles []string) {
	t.Helper()
	hits := sweepDatabaseFor(t, db, needles)
	if len(hits) == 0 {
		return
	}
	messages := make([]string, 0, len(hits))
	for _, hit := range hits {
		messages = append(messages, hit.String())
	}
	sort.Strings(messages)
	assert.Fail(t, "personal data survived the erasure", strings.Join(messages, "\n"))
}

// tablesHolding is the counterpart used BEFORE an erasure. Asserting which
// tables the sweep can see the person in is what keeps the assertion above
// honest: a sweep that quietly stopped reading a table (or stopped reading
// soft-deleted rows) would find nothing afterwards and pass, so each test first
// proves the sweep does see the copies that the erasure then has to remove.
func tablesHolding(t *testing.T, db *gorm.DB, needles []string) []string {
	t.Helper()
	seen := map[string]struct{}{}
	for _, hit := range sweepDatabaseFor(t, db, needles) {
		seen[hit.Table] = struct{}{}
	}
	tables := make([]string, 0, len(seen))
	for table := range seen {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	return tables
}

// --- The sweep's own coverage ------------------------------------------------

// The leads leak was invisible partly because the erasure test database did not
// create the leads table: a sweep is only as good as the schema it runs against.
// This pins the schema the sweeps below run on to the application's real one.
func TestThePersonalDataSweepCoversEveryTableTheApplicationMigrates(t *testing.T) {
	db := setupFullSchemaDB(t)

	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)

	for _, expected := range []string{
		"users", "leads", "customers", "tickets", "tasks",
		"api_keys", "configurations", "refresh_tokens",
		"bulk_operations", "bulk_operation_items",
	} {
		assert.Contains(t, tables, expected,
			"the sweep must run against a database that has %s, or nothing it says about %s means anything", expected, expected)
	}
}

// --- Users -------------------------------------------------------------------

func TestUserErasureLeavesNoPersonalDataInAnyTable(t *testing.T) {
	db := setupFullSchemaDB(t)
	subject := newErasureSubject("user-sweep")
	bystander := newErasureSubject("bystander")

	user := subject.asUser(t, db)
	survivor := bystander.asUser(t, db)

	// The kind of records that accumulate around an account and that a
	// table-by-table check would never look at.
	require.NoError(t, db.Create(&models.APIKey{
		Name: "deployment key", KeyHash: "hash", Prefix: "gcrm_swp", UserID: user.ID, IsActive: true,
	}).Error)
	require.NoError(t, db.Create(&models.RefreshToken{
		UserID: user.ID, TokenHash: "refresh", ExpiresAt: time.Now().Add(24 * time.Hour),
	}).Error)
	require.NoError(t, db.Create(&models.Task{
		Title: "Quarterly pipeline review", Status: models.TaskStatusPending,
		Priority: models.TaskPriorityMedium, AssignedToID: user.ID,
	}).Error)
	require.NoError(t, db.Create(&models.BulkOperation{
		UserID: user.ID, ResourceType: "customers", Type: models.BulkDelete,
		Status: models.StatusCompleted, TotalItems: 1,
	}).Error)

	require.Equal(t, []string{"users"}, tablesHolding(t, db, subject.identifiers()),
		"the sweep must be able to see the person before the erasure, or its silence afterwards proves nothing")

	require.NoError(t, repository.NewUserRepository(db).Delete(user.ID))

	assertNoPersonalDataAnywhere(t, db, subject.identifiers())

	// ...and the sweep is still working: it can still find somebody who was not
	// erased. Without this the test above would also pass on a sweep that had
	// silently stopped reading rows.
	assert.Equal(t, []string{"users"}, tablesHolding(t, db, bystander.identifiers()),
		"an unrelated account must be untouched")
	assert.NotZero(t, survivor.ID)
}

// --- Customers ---------------------------------------------------------------

func TestCustomerErasureLeavesNoPersonalDataInAnyTable(t *testing.T) {
	db := setupFullSchemaDB(t)
	subject := newErasureSubject("customer-sweep")

	customer := subject.asCustomer(t, db)
	require.NoError(t, db.Create(&models.Ticket{
		Title: "Invoice PDF will not download", Description: "Reported over the phone.",
		Status: models.TicketStatusOpen, Priority: models.TicketPriorityMedium, CustomerID: customer.ID,
	}).Error)

	require.Equal(t, []string{"customers"}, tablesHolding(t, db, subject.identifiers()))

	require.NoError(t, repository.NewCustomerRepository(db).Delete(customer.ID))

	assertNoPersonalDataAnywhere(t, db, subject.identifiers())
}

// --- Leads -------------------------------------------------------------------

func TestLeadErasureLeavesNoPersonalDataInAnyTable(t *testing.T) {
	db := setupFullSchemaDB(t)
	subject := newErasureSubject("lead-sweep")

	owner := seedLeadOwner(t, db)
	lead := subject.asLead(t, db, owner.ID)
	require.NoError(t, db.Create(&models.Task{
		Title: "Call back about the quote", Status: models.TaskStatusPending,
		Priority: models.TaskPriorityMedium, AssignedToID: owner.ID, LeadID: &lead.ID,
	}).Error)

	require.Equal(t, []string{"leads"}, tablesHolding(t, db, subject.identifiers()))

	require.NoError(t, repository.NewLeadRepository(db).Delete(lead.ID))

	assertNoPersonalDataAnywhere(t, db, subject.identifiers())
}

// --- The conversion link -----------------------------------------------------

// THE test for the leak. A conversion leaves the same person described twice, in
// two different tables; erasing the customer used to leave the lead untouched
// and every assertion in the suite was pointed at the customers table, so
// nothing failed. This test does not need to know that leads exist — it asks
// whether the person is still in the database, and the answer was yes.
func TestErasingAConvertedCustomerLeavesNoPersonalDataInAnyTable(t *testing.T) {
	db := setupFullSchemaDB(t)
	subject := newErasureSubject("converted-customer-sweep")

	owner := seedLeadOwner(t, db)
	lead := subject.asLead(t, db, owner.ID)
	customer := convertLead(t, db, lead)

	// The conversion copied the person into a second table. Both copies have to
	// go, and the sweep can see both before the erasure.
	require.Equal(t, []string{"customers", "leads"}, tablesHolding(t, db, subject.identifiers()),
		"the conversion must have left a copy in each table for this test to mean anything")

	require.NoError(t, repository.NewCustomerRepositoryWithLeadErasure(db).Delete(customer.ID))

	assertNoPersonalDataAnywhere(t, db, subject.identifiers())
}

// The same pair, entered from the lead end.
func TestErasingAConvertedLeadLeavesNoPersonalDataInAnyTable(t *testing.T) {
	db := setupFullSchemaDB(t)
	subject := newErasureSubject("converted-lead-sweep")

	owner := seedLeadOwner(t, db)
	lead := subject.asLead(t, db, owner.ID)
	convertLead(t, db, lead)

	require.Equal(t, []string{"customers", "leads"}, tablesHolding(t, db, subject.identifiers()))

	require.NoError(t, repository.NewLeadRepository(db).Delete(lead.ID))

	assertNoPersonalDataAnywhere(t, db, subject.identifiers())
}

// --- The bulk endpoints ------------------------------------------------------

// Deleting people through a bulk endpoint has to leave the database in the same
// state as deleting them one at a time — including in the tables the bulk path's
// own tests never mention.
func TestBulkErasureLeavesNoPersonalDataInAnyTable(t *testing.T) {
	db := setupFullSchemaDB(t)
	bulkRepo := repository.NewBulkRepository(db)

	owner := seedLeadOwner(t, db)
	userSubject := newErasureSubject("bulk-user-sweep")
	leadSubject := newErasureSubject("bulk-lead-sweep")
	customerSubject := newErasureSubject("bulk-customer-sweep")

	user := userSubject.asUser(t, db)
	require.NoError(t, db.Create(&models.APIKey{
		Name: "ci", KeyHash: "hash-bulk-sweep", Prefix: "gcrm_bsw", UserID: user.ID, IsActive: true,
	}).Error)

	// A converted lead: erasing it through the bulk path must take the customer
	// it became with it.
	lead := leadSubject.asLead(t, db, owner.ID)
	convertLead(t, db, lead)

	// ...and a customer converted from its own lead, erased from the other end.
	customerLead := customerSubject.asLead(t, db, owner.ID)
	customer := convertLead(t, db, customerLead)

	require.Equal(t, []string{"users"}, tablesHolding(t, db, userSubject.identifiers()))
	require.Equal(t, []string{"customers", "leads"}, tablesHolding(t, db, leadSubject.identifiers()))
	require.Equal(t, []string{"customers", "leads"}, tablesHolding(t, db, customerSubject.identifiers()))

	require.Empty(t, bulkRepo.BulkDeleteUsers([]uint{user.ID}))
	require.Empty(t, bulkRepo.BulkDeleteLeads([]uint{lead.ID}))
	require.Empty(t, bulkRepo.BulkDeleteCustomers([]uint{customer.ID}))

	assertNoPersonalDataAnywhere(t, db, userSubject.identifiers())
	assertNoPersonalDataAnywhere(t, db, leadSubject.identifiers())
	assertNoPersonalDataAnywhere(t, db, customerSubject.identifiers())
}

// --- Deactivation is not erasure ---------------------------------------------

// The mirror image, and the reason the sweep cannot simply be "delete
// everything": suspending an account must leave every one of those records
// exactly where they are. A sweep that came back empty here would mean
// deactivation had started destroying data.
func TestDeactivationLeavesThePersonalDataWhereItIs(t *testing.T) {
	db := setupFullSchemaDB(t)
	subject := newErasureSubject("deactivation-sweep")

	user := subject.asUser(t, db)
	require.NoError(t, db.Model(&models.User{}).Where("id = ?", user.ID).
		Update("is_active", false).Error)

	assert.Equal(t, []string{"users"}, tablesHolding(t, db, subject.identifiers()),
		"deactivation is reversible and must not anonymise anything")
}
