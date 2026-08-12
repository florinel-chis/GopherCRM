package models

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupFormDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.AutoMigrate(&Form{}, &FormSubmission{}, &FormConfirmationToken{}))
	return db
}

// validContactForm is the shape every negative case below mutates.
func validContactForm() *Form {
	return &Form{
		Name:           "Contact us",
		SubmitAction:   FormSubmitActionMessage,
		CreateLead:     true,
		DefaultOwnerID: 7,
		Fields: []FormFieldDef{
			{Name: "first_name", Label: "First name", Type: FormFieldText, Required: true},
			{Name: "email", Label: "Email", Type: FormFieldEmail, Required: true},
			{Name: "message", Label: "Message", Type: FormFieldTextarea},
		},
	}
}

func TestFormValidateDefinition(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(f *Form)
		wantErr string
	}{
		{
			name:   "valid contact form",
			mutate: func(f *Form) {},
		},
		{
			name:    "no name",
			mutate:  func(f *Form) { f.Name = "  " },
			wantErr: "name is required",
		},
		{
			name:    "zero fields",
			mutate:  func(f *Form) { f.Fields = nil },
			wantErr: "at least one field",
		},
		{
			name: "too many fields",
			mutate: func(f *Form) {
				f.Fields = make([]FormFieldDef, 0, FormMaxFields+1)
				f.Fields = append(f.Fields, FormFieldDef{Name: "email", Label: "Email", Type: FormFieldEmail})
				for i := 0; i <= FormMaxFields; i++ {
					f.Fields = append(f.Fields, FormFieldDef{Name: fieldName(i), Label: "X", Type: FormFieldText})
				}
			},
			wantErr: "more than 50 fields",
		},
		{
			name: "duplicate field names",
			mutate: func(f *Form) {
				f.Fields[2].Name = "first_name"
			},
			wantErr: "duplicate field name",
		},
		{
			name:    "field name with illegal characters",
			mutate:  func(f *Form) { f.Fields[0].Name = "First Name" },
			wantErr: "lowercase letters",
		},
		{
			name:    "field name starting with a digit",
			mutate:  func(f *Form) { f.Fields[0].Name = "1st" },
			wantErr: "lowercase letters",
		},
		{
			name:    "unknown field type",
			mutate:  func(f *Form) { f.Fields[0].Type = "date" },
			wantErr: "unknown field type",
		},
		{
			name: "no email field",
			mutate: func(f *Form) {
				f.Fields[1].Type = FormFieldText
			},
			wantErr: "exactly one field of type",
		},
		{
			name: "two email fields",
			mutate: func(f *Form) {
				f.Fields[0].Type = FormFieldEmail
			},
			wantErr: `the email field must be named "email"`,
		},
		{
			name: "email field under another name",
			mutate: func(f *Form) {
				f.Fields[1].Name = "work_email"
			},
			wantErr: `must be named "email"`,
		},
		{
			name: "select without options",
			mutate: func(f *Form) {
				f.Fields[2] = FormFieldDef{Name: "topic", Label: "Topic", Type: FormFieldSelect}
			},
			wantErr: "at least one option",
		},
		{
			name: "select with an empty option",
			mutate: func(f *Form) {
				f.Fields[2] = FormFieldDef{Name: "topic", Label: "Topic", Type: FormFieldSelect, Options: []string{"Sales", " "}}
			},
			wantErr: "options cannot be empty",
		},
		{
			name: "options on a non-select field",
			mutate: func(f *Form) {
				f.Fields[0].Options = []string{"nope"}
			},
			wantErr: "only a select field can have options",
		},
		{
			name: "required hidden field",
			mutate: func(f *Form) {
				f.Fields[2] = FormFieldDef{Name: "campaign", Label: "Campaign", Type: FormFieldHidden, Required: true}
			},
			wantErr: "hidden field cannot be required",
		},
		{
			name:    "redirect without a URL",
			mutate:  func(f *Form) { f.SubmitAction = FormSubmitActionRedirect },
			wantErr: "needs a redirect URL",
		},
		{
			name: "redirect to a non http url",
			mutate: func(f *Form) {
				f.SubmitAction = FormSubmitActionRedirect
				f.RedirectURL = "javascript:alert(1)"
			},
			wantErr: "http(s) URL",
		},
		{
			name:    "unknown submit action",
			mutate:  func(f *Form) { f.SubmitAction = "teleport" },
			wantErr: "unknown submit action",
		},
		{
			name:    "lead creation without an owner",
			mutate:  func(f *Form) { f.DefaultOwnerID = 0 },
			wantErr: "needs a default owner",
		},
		{
			name: "no owner needed when leads are off",
			mutate: func(f *Form) {
				f.CreateLead = false
				f.DefaultOwnerID = 0
			},
		},
		{
			name:    "invalid notification address",
			mutate:  func(f *Form) { f.NotifyEmails = []string{"sales@example.com", "not-an-address"} },
			wantErr: "not a valid email address",
		},
		{
			name:    "allowed domain with a scheme",
			mutate:  func(f *Form) { f.AllowedDomains = []string{"https://example.com"} },
			wantErr: "bare host name",
		},
		{
			name:    "allowed domain with a path",
			mutate:  func(f *Form) { f.AllowedDomains = []string{"example.com/contact"} },
			wantErr: "bare host name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			form := validContactForm()
			tt.mutate(form)

			err := form.ValidateDefinition()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.True(t, errors.Is(err, ErrInvalidFormDefinition),
				"every definition error must wrap ErrInvalidFormDefinition, got %v", err)
		})
	}
}

func fieldName(i int) string {
	return "f" + string(rune('a'+i%26)) + string(rune('a'+i/26))
}

// The length limits are a normalisation, not just a check: the submission
// validator reads MaxLength straight off the definition.
func TestFormValidateDefinitionNormalisesMaxLength(t *testing.T) {
	form := validContactForm()
	form.Fields = append(form.Fields, FormFieldDef{Name: "bio", Label: "Bio", Type: FormFieldText, MaxLength: 999999})

	require.NoError(t, form.ValidateDefinition())

	assert.Equal(t, FormDefaultMaxLength, form.Fields[0].MaxLength, "text default")
	assert.Equal(t, FormTextareaDefaultMaxLength, form.Fields[2].MaxLength, "textarea default")
	assert.Equal(t, FormHardMaxLength, form.Fields[3].MaxLength, "hard cap")
}

func TestFormValidateDefinitionNormalisesDomainsAndEmails(t *testing.T) {
	form := validContactForm()
	form.AllowedDomains = []string{"  WWW.Example.COM ", "shop.example.com:8443"}
	form.NotifyEmails = []string{"  sales@example.com "}

	require.NoError(t, form.ValidateDefinition())

	assert.Equal(t, []string{"www.example.com", "shop.example.com:8443"}, form.AllowedDomains)
	assert.Equal(t, []string{"sales@example.com"}, form.NotifyEmails)
}

func TestFormJSONCodecRoundTrip(t *testing.T) {
	db := setupFormDB(t)

	form := validContactForm()
	form.PublicID = "pub-round-trip"
	form.NotifyEmails = []string{"sales@example.com", "ops@example.com"}
	form.AllowedDomains = []string{"example.com"}
	form.Fields[2].Options = nil
	require.NoError(t, db.Create(form).Error)

	var loaded Form
	require.NoError(t, db.First(&loaded, form.ID).Error)

	assert.Equal(t, form.Fields, loaded.Fields)
	assert.Equal(t, []string{"sales@example.com", "ops@example.com"}, loaded.NotifyEmails)
	assert.Equal(t, []string{"example.com"}, loaded.AllowedDomains)
	assert.Equal(t, FormStatusDraft, loaded.Status, "status falls back to the column default")
	assert.Equal(t, FormSubmitActionMessage, loaded.SubmitAction)
}

// An empty definition must come back as an empty slice, never as nil, so the
// API emits [] instead of null.
func TestFormJSONCodecEmptyCollections(t *testing.T) {
	db := setupFormDB(t)

	form := &Form{Name: "Bare", PublicID: "pub-bare"}
	require.NoError(t, db.Create(form).Error)

	var loaded Form
	require.NoError(t, db.First(&loaded, form.ID).Error)

	assert.NotNil(t, loaded.Fields)
	assert.Empty(t, loaded.Fields)
	assert.NotNil(t, loaded.NotifyEmails)
	assert.Empty(t, loaded.NotifyEmails)
	assert.NotNil(t, loaded.AllowedDomains)
	assert.Empty(t, loaded.AllowedDomains)
}

// create_lead must survive as false. GORM substitutes a literal column default
// for a zero-valued field, which is exactly why the column carries no default.
func TestFormCreateLeadFalseIsPersisted(t *testing.T) {
	db := setupFormDB(t)

	form := &Form{Name: "No leads", PublicID: "pub-no-leads", CreateLead: false}
	require.NoError(t, db.Create(form).Error)

	var loaded Form
	require.NoError(t, db.First(&loaded, form.ID).Error)
	assert.False(t, loaded.CreateLead)
}

func TestFormSubmissionDataRoundTrip(t *testing.T) {
	db := setupFormDB(t)

	submission := &FormSubmission{
		FormID: 1,
		Email:  "visitor@example.com",
		Status: FormSubmissionReceived,
		Data: map[string]string{
			"first_name": "Ada",
			"email":      "visitor@example.com",
			"subscribe":  "true",
		},
		IPAddress: "203.0.113.7",
	}
	require.NoError(t, db.Create(submission).Error)

	var loaded FormSubmission
	require.NoError(t, db.First(&loaded, submission.ID).Error)

	assert.Equal(t, submission.Data, loaded.Data)
	assert.Equal(t, FormSubmissionReceived, loaded.Status)
	assert.Nil(t, loaded.LeadID)
}

func TestFormSubmissionEmptyDataDecodesToEmptyMap(t *testing.T) {
	db := setupFormDB(t)

	submission := &FormSubmission{FormID: 1, Status: FormSubmissionSpam, SpamReason: FormSpamReasonHoneypot}
	require.NoError(t, db.Create(submission).Error)

	var loaded FormSubmission
	require.NoError(t, db.First(&loaded, submission.ID).Error)

	assert.NotNil(t, loaded.Data)
	assert.Empty(t, loaded.Data)
}

func TestFormMigrationCreatesTables(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	origDB := DB
	DB = db
	defer func() { DB = origDB }()

	require.NoError(t, MigrateDatabase())

	for _, table := range []string{"forms", "form_submissions", "form_confirmation_tokens"} {
		assert.True(t, db.Migrator().HasTable(table), "table %s should exist", table)
	}
	assert.True(t, db.Migrator().HasColumn(&Form{}, "fields"))
	assert.True(t, db.Migrator().HasColumn(&FormSubmission{}, "data"))
}
