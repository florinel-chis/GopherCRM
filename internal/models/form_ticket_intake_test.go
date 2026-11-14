package models

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #213: the ticket target of a form, the ticket columns of a submission and
// the form_ticket_keys table on SQLite.

func ticketFormDefinition() *Form {
	return &Form{
		Name: "Complaints",
		Fields: []FormFieldDef{
			{Name: "email", Label: "Email", Type: FormFieldEmail, Required: true},
			{Name: "complaint", Label: "Complaint", Type: FormFieldTextarea, Required: true},
		},
		DoubleOptIn:  true,
		CreateTicket: true,
	}
}

func TestFormTicketTargetDefinition(t *testing.T) {
	t.Run("create_ticket with double opt-in and no default owner is accepted", func(t *testing.T) {
		form := ticketFormDefinition()
		require.NoError(t, form.ValidateDefinition())
		assert.Zero(t, form.DefaultOwnerID)
	})

	t.Run("create_ticket with create_lead is refused", func(t *testing.T) {
		form := ticketFormDefinition()
		form.CreateLead = true
		form.DefaultOwnerID = 4
		err := form.ValidateDefinition()
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrInvalidFormDefinition))
		assert.Contains(t, err.Error(), "cannot both create leads and create tickets")
	})

	t.Run("create_ticket without double opt-in is refused", func(t *testing.T) {
		form := ticketFormDefinition()
		form.DoubleOptIn = false
		err := form.ValidateDefinition()
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrInvalidFormDefinition))
		assert.Contains(t, err.Error(), "needs double opt-in")
	})

	t.Run("a form without create_ticket is unchanged", func(t *testing.T) {
		form := ticketFormDefinition()
		form.CreateTicket = false
		form.DoubleOptIn = false
		require.NoError(t, form.ValidateDefinition())

		form.CreateLead = true
		err := form.ValidateDefinition()
		require.Error(t, err, "a lead form still needs its default owner")
		assert.True(t, errors.Is(err, ErrInvalidFormDefinition))
	})
}

func openFormTicketIntakeDB(t *testing.T, name string) {
	t.Helper()
	origDB := DB
	t.Cleanup(func() {
		require.NoError(t, CloseDatabase())
		DB = origDB
	})
	require.NoError(t, InitDatabase(&config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), name),
	}))
}

// assertFormTicketIntakeSchemaSQLite checks every object #213 adds.
func assertFormTicketIntakeSchemaSQLite(t *testing.T) {
	t.Helper()
	columnsOf := func(table string) map[string]sqliteColumn {
		var columns []sqliteColumn
		require.NoError(t, DB.Raw("SELECT name, type, \"notnull\", dflt_value, pk FROM pragma_table_info(?)", table).Scan(&columns).Error)
		out := map[string]sqliteColumn{}
		for _, column := range columns {
			out[column.Name] = column
		}
		return out
	}

	forms := columnsOf("forms")
	require.Contains(t, forms, "create_ticket")
	assert.Equal(t, 1, forms["create_ticket"].NotNull, "forms.create_ticket is NOT NULL")
	require.NotNil(t, forms["create_ticket"].Dflt, "forms.create_ticket has a default")
	assert.Contains(t, []string{"false", "0"}, strings.ToLower(*forms["create_ticket"].Dflt))

	submissions := columnsOf("form_submissions")
	for name, typ := range map[string]string{"idempotency_key": "varchar(128)", "ticket_outcome": "varchar(20)"} {
		require.Contains(t, submissions, name)
		assert.Equal(t, typ, strings.ToLower(submissions[name].Type), name)
		assert.Equal(t, 1, submissions[name].NotNull, "%s is NOT NULL", name)
		require.NotNil(t, submissions[name].Dflt, "%s has a default", name)
		assert.Equal(t, "", strings.Trim(*submissions[name].Dflt, `'"`), "%s defaults to ''", name)
	}
	require.Contains(t, submissions, "ticket_id")
	assert.Equal(t, 0, submissions["ticket_id"].NotNull, "ticket_id is nullable")
	assert.Equal(t, []string{"ticket_id"}, customerExternalIDIndexColumns(t, "idx_form_submissions_ticket_id"))

	require.True(t, DB.Migrator().HasTable("form_ticket_keys"))
	keys := columnsOf("form_ticket_keys")
	names := make([]string, 0, len(keys))
	for name := range keys {
		names = append(names, name)
	}
	sort.Strings(names)
	assert.Equal(t, []string{"created_at", "customer_id", "form_id", "id", "idempotency_key", "submission_id", "ticket_id", "updated_at"}, names,
		"exactly these columns, no deleted_at")
	assert.Equal(t, "varchar(128)", strings.ToLower(keys["idempotency_key"].Type))
	for _, name := range []string{"form_id", "idempotency_key", "ticket_id", "submission_id", "customer_id"} {
		assert.Equal(t, 1, keys[name].NotNull, "%s is NOT NULL", name)
	}

	var indexes []sqliteIndex
	require.NoError(t, DB.Raw("SELECT name, \"unique\", origin FROM pragma_index_list('form_ticket_keys')").Scan(&indexes).Error)
	unique := []string{}
	all := map[string]sqliteIndex{}
	for _, index := range indexes {
		all[index.Name] = index
		if index.Unique == 1 && index.Origin != "pk" {
			unique = append(unique, index.Name)
		}
	}
	assert.Equal(t, []string{"idx_form_ticket_keys_identity"}, unique, "the identity index is the only unique index")
	assert.Equal(t, []string{"form_id", "idempotency_key"}, customerExternalIDIndexColumns(t, "idx_form_ticket_keys_identity"))
	for index, column := range map[string]string{
		"idx_form_ticket_keys_ticket_id":     "ticket_id",
		"idx_form_ticket_keys_submission_id": "submission_id",
		"idx_form_ticket_keys_customer_id":   "customer_id",
	} {
		require.Contains(t, all, index)
		assert.Equal(t, 0, all[index].Unique, "%s is not unique", index)
		assert.Equal(t, []string{column}, customerExternalIDIndexColumns(t, index))
	}

	// The identity index compares with BINARY.
	var collations []struct {
		Name string  `gorm:"column:name"`
		Coll *string `gorm:"column:coll"`
	}
	require.NoError(t, DB.Raw("SELECT name, coll FROM pragma_index_xinfo('idx_form_ticket_keys_identity') WHERE \"key\" = 1").Scan(&collations).Error)
	require.Len(t, collations, 2)
	for _, column := range collations {
		require.NotNil(t, column.Coll)
		assert.Equal(t, "BINARY", strings.ToUpper(*column.Coll), "%s compares binary", column.Name)
	}
}

// assertFormTicketKeyUniqueness proves the pair is unique and case-sensitive.
func assertFormTicketKeyUniqueness(t *testing.T) {
	t.Helper()
	first := &FormTicketKey{FormID: 7, IdempotencyKey: "Order-1", TicketID: 1, SubmissionID: 1, CustomerID: 1}
	require.NoError(t, DB.Create(first).Error)
	err := DB.Create(&FormTicketKey{FormID: 7, IdempotencyKey: "Order-1", TicketID: 2, SubmissionID: 2, CustomerID: 2}).Error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UNIQUE constraint failed")
	require.NoError(t, DB.Create(&FormTicketKey{FormID: 7, IdempotencyKey: "order-1", TicketID: 3, SubmissionID: 3, CustomerID: 3}).Error,
		"keys differing in case are distinct")
	require.NoError(t, DB.Create(&FormTicketKey{FormID: 8, IdempotencyKey: "Order-1", TicketID: 4, SubmissionID: 4, CustomerID: 4}).Error,
		"the same key on another form is distinct")
}

func TestFormTicketIntakeSchemaSQLite(t *testing.T) {
	openFormTicketIntakeDB(t, "form-ticket-intake.db")
	require.NoError(t, MigrateDatabase())
	assertFormTicketIntakeSchemaSQLite(t)
	assertFormTicketKeyUniqueness(t)

	// A raw insert that omits the new columns takes their defaults.
	require.NoError(t, DB.Exec("INSERT INTO forms (name, public_id, status, submit_action, create_lead) VALUES ('Raw', 'raw-public-id', 'draft', 'message', 0)").Error)
	var raw struct {
		ID           uint
		CreateTicket bool
	}
	require.NoError(t, DB.Raw("SELECT id, create_ticket FROM forms WHERE public_id = 'raw-public-id'").Scan(&raw).Error)
	require.NotZero(t, raw.ID)
	assert.False(t, raw.CreateTicket)
	require.NoError(t, DB.Exec("INSERT INTO form_submissions (form_id, data, email, status) VALUES (?, '{}', 'raw@example.com', 'received')", raw.ID).Error)
	var submission struct {
		IdempotencyKey string
		TicketID       *uint
		TicketOutcome  string
	}
	require.NoError(t, DB.Raw("SELECT idempotency_key, ticket_id, ticket_outcome FROM form_submissions WHERE form_id = ?", raw.ID).Scan(&submission).Error)
	assert.Equal(t, "", submission.IdempotencyKey)
	assert.Nil(t, submission.TicketID)
	assert.Equal(t, "", submission.TicketOutcome)

	schema := schemaSnapshot(t)
	require.NoError(t, MigrateDatabase())
	assert.Equal(t, schema, schemaSnapshot(t), "a second MigrateDatabase leaves the schema alone")
}

// legacyTicketlessForm and legacyTicketlessSubmission are the two tables as
// they were before #213 (only the columns the upgrade cares about).
type legacyTicketlessForm struct {
	BaseModel
	Name         string `gorm:"not null;type:varchar(255)"`
	PublicID     string `gorm:"not null;type:varchar(32);uniqueIndex"`
	Status       string `gorm:"not null;type:varchar(20);default:'draft'"`
	FieldsJSON   string `gorm:"column:fields;type:text"`
	SubmitAction string `gorm:"not null;type:varchar(20);default:'message'"`
	DoubleOptIn  bool   `gorm:"not null;default:false"`
	CreateLead   bool   `gorm:"not null"`
}

func (legacyTicketlessForm) TableName() string { return "forms" }

type legacyTicketlessSubmission struct {
	BaseModel
	FormID   uint   `gorm:"not null;index"`
	DataJSON string `gorm:"column:data;type:mediumtext"`
	Email    string `gorm:"type:varchar(255);index"`
	Status   string `gorm:"not null;type:varchar(20);index"`
}

func (legacyTicketlessSubmission) TableName() string { return "form_submissions" }

func TestFormTicketIntakeSQLiteUpgrade(t *testing.T) {
	openFormTicketIntakeDB(t, "form-ticket-intake-upgrade.db")
	require.NoError(t, DB.AutoMigrate(&legacyTicketlessForm{}, &legacyTicketlessSubmission{}))
	require.False(t, DB.Migrator().HasColumn("forms", "create_ticket"), "the fixture predates create_ticket")
	require.False(t, DB.Migrator().HasColumn("form_submissions", "idempotency_key"))
	require.False(t, DB.Migrator().HasTable("form_ticket_keys"))

	existing := &legacyTicketlessForm{Name: "Existing", PublicID: "existing-public-id", Status: "published", SubmitAction: "message", DoubleOptIn: true, CreateLead: true}
	require.NoError(t, DB.Create(existing).Error)
	oldSubmission := &legacyTicketlessSubmission{FormID: existing.ID, DataJSON: `{"email":"old@example.com"}`, Email: "old@example.com", Status: "confirmed"}
	require.NoError(t, DB.Create(oldSubmission).Error)

	require.NoError(t, MigrateDatabase())
	assertFormTicketIntakeSchemaSQLite(t)

	var form Form
	require.NoError(t, DB.First(&form, existing.ID).Error)
	assert.False(t, form.CreateTicket, "an existing form reads create_ticket false")
	assert.True(t, form.CreateLead, "the rest of the form is kept")
	var submission FormSubmission
	require.NoError(t, DB.First(&submission, oldSubmission.ID).Error)
	assert.Equal(t, "", submission.IdempotencyKey)
	assert.Nil(t, submission.TicketID)
	assert.Equal(t, "", submission.TicketOutcome)
	assert.Equal(t, "old@example.com", submission.Email)

	assertFormTicketKeyUniqueness(t)

	schema := schemaSnapshot(t)
	require.NoError(t, MigrateDatabase())
	assert.Equal(t, schema, schemaSnapshot(t), "a second MigrateDatabase leaves the schema alone")
}

func TestFormSubmissionTicketJSON(t *testing.T) {
	unset := FormSubmission{FormID: 3, Status: FormSubmissionPending}
	raw, err := json.Marshal(unset)
	require.NoError(t, err)
	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Contains(t, decoded, "idempotency_key")
	require.Contains(t, decoded, "ticket_id")
	require.Contains(t, decoded, "ticket_outcome")
	assert.JSONEq(t, `""`, string(decoded["idempotency_key"]))
	assert.Equal(t, "null", string(decoded["ticket_id"]), "ticket_id is null when unset")
	assert.JSONEq(t, `""`, string(decoded["ticket_outcome"]))

	ticketID := uint(311)
	linked := FormSubmission{FormID: 3, Status: FormSubmissionConfirmed, IdempotencyKey: "order-2026-1187",
		TicketID: &ticketID, TicketOutcome: FormTicketOutcomeDuplicate}
	raw, err = json.Marshal(linked)
	require.NoError(t, err)
	decoded = map[string]json.RawMessage{}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.JSONEq(t, `"order-2026-1187"`, string(decoded["idempotency_key"]))
	assert.JSONEq(t, `311`, string(decoded["ticket_id"]))
	assert.JSONEq(t, `"duplicate"`, string(decoded["ticket_outcome"]))
	assert.JSONEq(t, `[]`, string(decoded["deliveries"]), "the existing deliveries rule is unchanged")

	// The form carries create_ticket.
	raw, err = json.Marshal(Form{Name: "F", CreateTicket: true})
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"create_ticket":true`)
}
