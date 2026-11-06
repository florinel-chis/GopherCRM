package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// #214: erasing a lead, a customer, a staff user or a customer-role user also
// scrubs, in the same erasure unit, every form_submissions row with no lead
// whose email is the erased row's original address or its lower-cased form.
// The matched rows' confirmation tokens and mail delivery rows are
// hard-deleted. Rows of other addresses and rows linked to another lead are
// untouched, a blank or placeholder address matches nothing, and a failure or
// a caller rollback leaves everything as it was. Run on the application's real
// schema, on SQLite and on a disposable TEST_MYSQL_DSN schema.

func TestUnlinkedSubmissionErasureSQLite(t *testing.T) {
	runUnlinkedSubmissionErasure(t, false)
}

func TestUnlinkedSubmissionErasureMySQL(t *testing.T) {
	if os.Getenv("TEST_MYSQL_DSN") == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
	runUnlinkedSubmissionErasure(t, true)
}

// unlinkedSubmissionTables are the tables the unlinked scrub writes.
var unlinkedSubmissionTables = []string{"form_submissions", "form_confirmation_tokens", "form_mail_deliveries"}

type unlinkedFixture struct {
	tag            string
	sales, admin   *models.User
	form, goneForm *models.Form
	original       string
	lower          string

	// matched are the unlinked rows of the person's address, as stored
	// before the erasure.
	matched []models.FormSubmission

	// tokenHash is the spendable token of the pending matched submission.
	tokenHash string

	// untouched are rows the erasure must leave exactly as they are: an
	// unlinked row of another address and a row linked to another lead with
	// the same address.
	untouched []models.FormSubmission
	otherLead *models.Lead
}

func unlinkedTokenHash(label string) string {
	sum := sha256.Sum256([]byte(label))
	return hex.EncodeToString(sum[:])
}

func unlinkedSubmission(t *testing.T, db *gorm.DB, formID uint, leadID *uint, email string, status models.FormSubmissionStatus, label string) *models.FormSubmission {
	t.Helper()
	submission := &models.FormSubmission{}
	submission.FormID = formID
	submission.Data = map[string]string{"email": email, "message": "Hello from " + label}
	submission.Email = email
	submission.Status = status
	submission.LeadID = leadID
	submission.IPAddress = "203.0.113.9"
	submission.UserAgent = "Agent/" + label
	submission.Referrer = "https://" + label + ".example/?utm_source=src-" + label
	submission.UTMSource = "src-" + label
	submission.UTMMedium = "medium-" + label
	submission.UTMCampaign = "campaign-" + label
	submission.UTMTerm = "term-" + label
	submission.UTMContent = "content-" + label
	submission.ReferrerHost = label + ".example"
	if status == models.FormSubmissionSpam {
		submission.SpamReason = "honeypot"
	}
	require.NoError(t, db.Create(submission).Error)
	return submission
}

func unlinkedDelivery(t *testing.T, db *gorm.DB, submission *models.FormSubmission, kind string) {
	t.Helper()
	require.NoError(t, db.Create(&models.FormMailDelivery{SubmissionID: submission.ID, FormID: submission.FormID, Kind: kind,
		Status: models.FormMailStatusFailed, ErrorCode: models.FormMailErrorMailFailed, Attempts: 1}).Error)
}

func unlinkedToken(t *testing.T, db *gorm.DB, submission *models.FormSubmission, hash string) {
	t.Helper()
	require.NoError(t, db.Create(&models.FormConfirmationToken{SubmissionID: submission.ID, TokenHash: hash,
		ExpiresAt: time.Now().Add(time.Hour)}).Error)
}

func unlinkedForm(t *testing.T, db *gorm.DB, tag, name string, owner, admin *models.User) *models.Form {
	t.Helper()
	form := &models.Form{Name: name + " " + tag, PublicID: "unlinked-" + name + "-" + tag, Status: models.FormStatusPublished,
		Fields:     []models.FormFieldDef{{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true}},
		CreateLead: false, DefaultOwnerID: owner.ID, CreatedByID: admin.ID}
	require.NoError(t, db.Create(form).Error)
	return form
}

// seedUnlinkedFixture stores, for the address original, unlinked submissions
// in every status, on a live and a soft-deleted form, one of them
// soft-deleted, stored lower-cased as form intake does (plus one stored
// exactly as given), and the rows that must survive.
func seedUnlinkedFixture(t *testing.T, db *gorm.DB, original string) *unlinkedFixture {
	t.Helper()
	tag := nextIdentityTag()
	f := &unlinkedFixture{tag: tag, original: original, lower: strings.ToLower(original)}
	f.sales = seedIdentityStaff(t, db, tag, models.RoleSales)
	f.admin = seedIdentityStaff(t, db, tag, models.RoleAdmin)
	f.form = unlinkedForm(t, db, tag, "newsletter", f.sales, f.admin)
	f.goneForm = unlinkedForm(t, db, tag, "retired", f.sales, f.admin)

	var matched []*models.FormSubmission
	received := unlinkedSubmission(t, db, f.form.ID, nil, f.lower, models.FormSubmissionReceived, "received-"+tag)
	unlinkedDelivery(t, db, received, models.FormMailKindNotification)
	pending := unlinkedSubmission(t, db, f.form.ID, nil, f.lower, models.FormSubmissionPending, "pending-"+tag)
	f.tokenHash = unlinkedTokenHash("pending-" + tag)
	unlinkedToken(t, db, pending, f.tokenHash)
	unlinkedDelivery(t, db, pending, models.FormMailKindConfirmation)
	spam := unlinkedSubmission(t, db, f.form.ID, nil, f.lower, models.FormSubmissionSpam, "spam-"+tag)
	confirmed := unlinkedSubmission(t, db, f.goneForm.ID, nil, f.lower, models.FormSubmissionConfirmed, "confirmed-"+tag)
	unlinkedDelivery(t, db, confirmed, models.FormMailKindFollowUp)
	deleted := unlinkedSubmission(t, db, f.form.ID, nil, f.lower, models.FormSubmissionReceived, "deleted-"+tag)
	unlinkedDelivery(t, db, deleted, models.FormMailKindNotification)
	require.NoError(t, db.Delete(&models.FormSubmission{}, deleted.ID).Error, "a soft-deleted submission still holds the person")
	matched = append(matched, received, pending, spam, confirmed, deleted)
	if f.original != f.lower {
		exact := unlinkedSubmission(t, db, f.form.ID, nil, f.original, models.FormSubmissionReceived, "exact-"+tag)
		matched = append(matched, exact)
	}
	require.NoError(t, db.Delete(&models.Form{}, f.goneForm.ID).Error, "rows of a soft-deleted form are covered")

	other := unlinkedSubmission(t, db, f.form.ID, nil, "other.person."+tag+"@example.com", models.FormSubmissionReceived, "other-"+tag)
	unlinkedToken(t, db, other, unlinkedTokenHash("other-"+tag))
	unlinkedDelivery(t, db, other, models.FormMailKindNotification)

	f.otherLead = &models.Lead{FirstName: "Namesake", LastName: tag, Email: f.lower, Source: "website",
		Status: models.LeadStatusNew, OwnerID: f.sales.ID}
	require.NoError(t, db.Create(f.otherLead).Error)
	linked := unlinkedSubmission(t, db, f.form.ID, &f.otherLead.ID, f.lower, models.FormSubmissionReceived, "linked-"+tag)
	unlinkedToken(t, db, linked, unlinkedTokenHash("linked-"+tag))
	unlinkedDelivery(t, db, linked, models.FormMailKindNotification)

	for _, s := range matched {
		f.matched = append(f.matched, loadSubmission(t, db, s.ID))
	}
	for _, s := range []*models.FormSubmission{other, linked} {
		f.untouched = append(f.untouched, loadSubmission(t, db, s.ID))
	}
	return f
}

// unlinkedRoot creates the erased person's row with the given address and
// returns how to erase it and how to read its stored address.
type unlinkedRoot struct {
	name string
	seed func(t *testing.T, db *gorm.DB, f *unlinkedFixture, email string) (erase func() error, model interface{}, id uint)
}

func unlinkedRoots() []unlinkedRoot {
	return []unlinkedRoot{
		{"lead", func(t *testing.T, db *gorm.DB, f *unlinkedFixture, email string) (func() error, interface{}, uint) {
			lead := &models.Lead{FirstName: "Jane", LastName: "Doe" + f.tag, Email: email, Source: "website",
				Status: models.LeadStatusNew, OwnerID: f.sales.ID}
			require.NoError(t, db.Create(lead).Error)
			return func() error { return NewLeadRepository(db).Delete(lead.ID) }, &models.Lead{}, lead.ID
		}},
		{"customer", func(t *testing.T, db *gorm.DB, f *unlinkedFixture, email string) (func() error, interface{}, uint) {
			customer := &models.Customer{FirstName: "Jane", LastName: "Doe" + f.tag, Email: email}
			require.NoError(t, db.Create(customer).Error)
			return func() error { return NewCustomerRepository(db).Delete(customer.ID) }, &models.Customer{}, customer.ID
		}},
		{"staff_user", func(t *testing.T, db *gorm.DB, f *unlinkedFixture, email string) (func() error, interface{}, uint) {
			user := &models.User{Email: email, Password: "$2a$10$personhash", FirstName: "Jane", LastName: "Doe" + f.tag,
				Role: models.RoleSupport, IsActive: true}
			require.NoError(t, db.Create(user).Error)
			return func() error { return NewUserRepository(db).Delete(user.ID) }, &models.User{}, user.ID
		}},
		{"customer_user", func(t *testing.T, db *gorm.DB, f *unlinkedFixture, email string) (func() error, interface{}, uint) {
			user := &models.User{Email: email, Password: "$2a$10$personhash", FirstName: "Jane", LastName: "Doe" + f.tag,
				Role: models.RoleCustomer, IsActive: true}
			require.NoError(t, db.Create(user).Error)
			return func() error { return NewUserRepository(db).Delete(user.ID) }, &models.User{}, user.ID
		}},
	}
}

func storedEmailOf(t *testing.T, db *gorm.DB, model interface{}, id uint) string {
	t.Helper()
	var emails []string
	require.NoError(t, db.Unscoped().Model(model).Where("id = ?", id).Pluck("email", &emails).Error)
	require.Len(t, emails, 1)
	return emails[0]
}

func unlinkedSnapshot(t *testing.T, db *gorm.DB) map[string][]string {
	t.Helper()
	all := snapshotIdentityDB(t, db)
	out := make(map[string][]string, len(unlinkedSubmissionTables))
	for _, table := range unlinkedSubmissionTables {
		out[table] = all[table]
	}
	return out
}

// mixedCaseAddress is an address as it may sit on a user or customer row,
// while form intake stores the lower-cased form.
func mixedCaseAddress(tag string) string {
	return fmt.Sprintf("Jane.Doe.%s@Example.COM", tag)
}

func assertUnlinkedScrubbed(t *testing.T, db *gorm.DB, f *unlinkedFixture, placeholder string) {
	t.Helper()
	require.True(t, strings.HasSuffix(placeholder, "@"+anonymizedEmailDomain), "the erased row holds a placeholder")
	for _, before := range f.matched {
		after := loadSubmission(t, db, before.ID)
		assert.Equal(t, placeholder, after.Email, "submission %d takes the erased row's placeholder", before.ID)
		assert.Equal(t, "{}", after.DataJSON, "submission %d", before.ID)
		assert.Empty(t, after.Data, "submission %d", before.ID)
		// ip_address, user_agent, referrer and the six attribution columns.
		emptied := []string{after.IPAddress, after.UserAgent, after.Referrer, after.UTMSource, after.UTMMedium,
			after.UTMCampaign, after.UTMTerm, after.UTMContent, after.ReferrerHost}
		for i, value := range emptied {
			assert.Empty(t, value, "submission %d: emptied column %d", before.ID, i)
		}
		assert.Equal(t, before.FormID, after.FormID, "submission %d keeps its form", before.ID)
		assert.Equal(t, before.Status, after.Status, "submission %d keeps its status", before.ID)
		assert.Nil(t, after.LeadID, "submission %d stays unlinked", before.ID)
		assert.Equal(t, before.DeletedAt.Valid, after.DeletedAt.Valid, "submission %d keeps its deletion state", before.ID)
		assert.Zero(t, countRows(t, db, &models.FormConfirmationToken{}, "submission_id = ?", before.ID), "tokens of %d", before.ID)
		assert.Zero(t, countRows(t, db, &models.FormMailDelivery{}, "submission_id = ?", before.ID), "deliveries of %d", before.ID)
	}
	_, err := NewFormRepository(db).GetConfirmationTokenByHash(f.tokenHash)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "the pending submission's link no longer confirms anything")
	assertUnlinkedUntouched(t, db, f)
}

func assertUnlinkedUntouched(t *testing.T, db *gorm.DB, f *unlinkedFixture) {
	t.Helper()
	for _, before := range f.untouched {
		assert.Equal(t, before, loadSubmission(t, db, before.ID), "submission %d is not the erased person's", before.ID)
		assert.EqualValues(t, 1, countRows(t, db, &models.FormConfirmationToken{}, "submission_id = ?", before.ID))
		assert.EqualValues(t, 1, countRows(t, db, &models.FormMailDelivery{}, "submission_id = ?", before.ID))
	}
	var lead models.Lead
	require.NoError(t, db.First(&lead, f.otherLead.ID).Error, "the namesake lead is not erased")
	assert.Equal(t, f.lower, lead.Email)
}

func assertUnlinkedSeeded(t *testing.T, db *gorm.DB, f *unlinkedFixture) {
	t.Helper()
	for _, s := range f.matched {
		require.NotEmpty(t, s.Email, "submission %d holds the address", s.ID)
		require.NotEmpty(t, s.IPAddress)
		require.NotEmpty(t, s.UTMSource)
	}
	token, err := NewFormRepository(db).GetConfirmationTokenByHash(f.tokenHash)
	require.NoError(t, err, "the pending submission's token is spendable before the erasure")
	require.Equal(t, f.matched[1].ID, token.SubmissionID)
	require.EqualValues(t, 1, countRows(t, db, &models.FormConfirmationToken{}, "submission_id = ?", f.matched[1].ID))
	require.EqualValues(t, 1, countRows(t, db, &models.FormMailDelivery{}, "submission_id = ?", f.matched[0].ID))
}

func runUnlinkedSubmissionErasure(t *testing.T, useMySQL bool) {
	for _, root := range unlinkedRoots() {
		root := root
		t.Run(root.name+"_erasure_scrubs_matching_unlinked_submissions", func(t *testing.T) {
			db := openIdentityErasureDB(t, useMySQL)
			f := seedUnlinkedFixture(t, db, mixedCaseAddress(nextIdentityTag()))
			erase, model, id := root.seed(t, db, f, f.original)
			assertUnlinkedSeeded(t, db, f)

			require.NoError(t, erase())

			assertUnlinkedScrubbed(t, db, f, storedEmailOf(t, db, model, id))
		})
	}

	t.Run("a_lower_cased_address_on_the_row_matches_too", func(t *testing.T) {
		db := openIdentityErasureDB(t, useMySQL)
		f := seedUnlinkedFixture(t, db, strings.ToLower(mixedCaseAddress(nextIdentityTag())))
		erase, model, id := unlinkedRoots()[1].seed(t, db, f, f.original)
		assertUnlinkedSeeded(t, db, f)
		require.NoError(t, erase())
		assertUnlinkedScrubbed(t, db, f, storedEmailOf(t, db, model, id))
	})

	t.Run("a_blank_original_address_matches_nothing", func(t *testing.T) {
		db := openIdentityErasureDB(t, useMySQL)
		f := seedUnlinkedFixture(t, db, "")
		erase, _, _ := unlinkedRoots()[0].seed(t, db, f, "")
		before := unlinkedSnapshot(t, db)
		require.NoError(t, erase())
		assert.Equal(t, before, unlinkedSnapshot(t, db), "no submission, token or delivery changes")
		_, err := NewFormRepository(db).GetConfirmationTokenByHash(f.tokenHash)
		assert.NoError(t, err, "the token is still spendable")
	})

	t.Run("an_original_address_that_is_already_a_placeholder_matches_nothing", func(t *testing.T) {
		db := openIdentityErasureDB(t, useMySQL)
		placeholder, err := newAnonymizedEmail()
		require.NoError(t, err)
		f := seedUnlinkedFixture(t, db, placeholder)
		// The lead, customer and staff-user roots each hold the same
		// placeholder once (users and customers carry a unique index, so the
		// customer-role user root is left out).
		for _, root := range unlinkedRoots()[:3] {
			erase, _, _ := root.seed(t, db, f, placeholder)
			before := unlinkedSnapshot(t, db)
			require.NoError(t, erase(), "root %s", root.name)
			assert.Equal(t, before, unlinkedSnapshot(t, db), "root %s: no submission, token or delivery changes", root.name)
		}
	})

	t.Run("a_failure_after_the_scrub_leaves_every_row_as_it_was", func(t *testing.T) {
		db := openIdentityErasureDB(t, useMySQL)
		f := seedUnlinkedFixture(t, db, mixedCaseAddress(nextIdentityTag()))
		erase, _, _ := unlinkedRoots()[1].seed(t, db, f, f.original)
		assertUnlinkedSeeded(t, db, f)
		before := snapshotIdentityDB(t, db)

		sentinel := errors.New("injected failure soft-deleting customers")
		name := "unlinked_submission_erasure_test:fail_customers"
		watched := f.matched[1].ID
		var reached, scrubbedFirst bool
		require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register(name, func(d *gorm.DB) {
			if d.Statement == nil || d.Statement.Schema == nil || d.Statement.Schema.Table != "customers" {
				return
			}
			reached = true
			// The scrub's writes are visible inside the erasure unit by now.
			var emails []string
			if err := d.Session(&gorm.Session{NewDB: true}).Unscoped().Model(&models.FormSubmission{}).
				Where("id = ?", watched).Pluck("email", &emails).Error; err == nil && len(emails) == 1 {
				scrubbedFirst = strings.HasSuffix(emails[0], "@"+anonymizedEmailDomain)
			}
			_ = d.AddError(sentinel)
		}))
		t.Cleanup(func() { _ = db.Callback().Delete().Remove(name) })

		err := erase()
		require.NoError(t, db.Callback().Delete().Remove(name))
		require.ErrorIs(t, err, sentinel)
		assert.True(t, reached, "the root's soft delete was reached")
		assert.True(t, scrubbedFirst, "the failure came after the unlinked scrub")
		assert.NotContains(t, strings.ToLower(err.Error()), f.lower, "the error carries no address")

		assert.Equal(t, before, snapshotIdentityDB(t, db), "nothing may change")
		_, err = NewFormRepository(db).GetConfirmationTokenByHash(f.tokenHash)
		assert.NoError(t, err, "the token is still spendable")
	})

	t.Run("a_caller_rollback_leaves_every_row_as_it_was", func(t *testing.T) {
		db := openIdentityErasureDB(t, useMySQL)
		f := seedUnlinkedFixture(t, db, mixedCaseAddress(nextIdentityTag()))
		customer := &models.Customer{FirstName: "Jane", LastName: "Doe" + f.tag, Email: f.original}
		require.NoError(t, db.Create(customer).Error)
		assertUnlinkedSeeded(t, db, f)
		before := snapshotIdentityDB(t, db)

		abort := errors.New("abort after the erasure")
		err := utils.NewTransactionManager(db).WithTransaction(context.Background(), func(ctx context.Context) error {
			tx, ok := utils.GetTxFromContext(ctx)
			require.True(t, ok)
			require.NoError(t, NewCustomerRepository(db).WithTx(tx).Delete(customer.ID))
			// Inside the transaction the scrub has happened.
			inside := loadSubmission(t, tx, f.matched[1].ID)
			assert.True(t, strings.HasSuffix(inside.Email, "@"+anonymizedEmailDomain))
			assert.Zero(t, countRows(t, tx, &models.FormConfirmationToken{}, "submission_id = ?", f.matched[1].ID))
			return abort
		})
		require.ErrorIs(t, err, abort)
		assert.NotContains(t, strings.ToLower(err.Error()), f.lower, "the error carries no address")

		assert.Equal(t, before, snapshotIdentityDB(t, db), "the rollback restores every row")
		_, err = NewFormRepository(db).GetConfirmationTokenByHash(f.tokenHash)
		assert.NoError(t, err, "the token is still spendable")
	})

	t.Run("a_schema_without_form_submissions_still_erases", func(t *testing.T) {
		db := openIdentityErasureDB(t, useMySQL)
		for _, model := range []interface{}{&models.FormMailDelivery{}, &models.FormConfirmationToken{}, &models.FormSubmission{}} {
			require.NoError(t, db.Migrator().DropTable(model))
		}
		require.False(t, db.Migrator().HasTable(&models.FormSubmission{}))
		tag := nextIdentityTag()

		login := &models.User{Email: mixedCaseAddress(tag), Password: "$2a$10$personhash", FirstName: "Jane", LastName: "Doe" + tag,
			Role: models.RoleCustomer, IsActive: true}
		require.NoError(t, db.Create(login).Error)
		require.NoError(t, NewUserRepository(db).Delete(login.ID), "a user erasure succeeds")
		assert.True(t, strings.HasSuffix(storedEmailOf(t, db, &models.User{}, login.ID), "@"+anonymizedEmailDomain))

		customer := &models.Customer{FirstName: "Jane", LastName: "Doe" + tag, Email: "Jane.Customer." + tag + "@Example.COM"}
		require.NoError(t, db.Create(customer).Error)
		require.NoError(t, NewCustomerRepository(db).Delete(customer.ID), "a customer erasure succeeds")
		assert.True(t, strings.HasSuffix(storedEmailOf(t, db, &models.Customer{}, customer.ID), "@"+anonymizedEmailDomain))
	})
}
