package service

import (
	"fmt"
	"io"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// BulkOperationPersistenceSuite exercises the bulk operation service against a
// real (in-memory SQLite) database so that assertions can compare what the API
// *reports* against what was actually *persisted*. Mock-based tests cannot
// catch a transaction that commits a partial write.
type BulkOperationPersistenceSuite struct {
	suite.Suite
	db      *gorm.DB
	service BulkOperationService
	actorID uint
}

func TestBulkOperationPersistenceSuite(t *testing.T) {
	suite.Run(t, new(BulkOperationPersistenceSuite))
}

func (s *BulkOperationPersistenceSuite) SetupTest() {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	s.Require().NoError(err)

	// api_keys and refresh_tokens are part of the schema under test even though
	// no test here creates one: bulk-deleting a user is a GDPR erasure and the
	// erasure purges their credentials unconditionally. A schema without those
	// tables would make every user deletion fail.
	s.Require().NoError(db.AutoMigrate(
		&models.User{},
		&models.Lead{},
		&models.Customer{},
		&models.Ticket{},
		&models.Task{},
		&models.APIKey{},
		&models.RefreshToken{},
		&models.PasswordResetToken{},
		&models.BulkOperation{},
		&models.BulkOperationItem{},
		&models.Form{},
		&models.FormSubmission{},
		&models.FormConfirmationToken{},
	))
	s.db = db

	logger := logrus.New()
	logger.SetOutput(io.Discard)

	s.service = NewBulkOperationService(
		repository.NewBulkOperationRepository(db),
		repository.NewBulkRepository(db),
		repository.NewUserRepository(db),
		repository.NewLeadRepository(db),
		repository.NewCustomerRepository(db),
		repository.NewTaskRepository(db),
		repository.NewTicketRepository(db),
		utils.NewTransactionManager(db),
		logger,
	)

	// The user performing the bulk operations.
	actor := &models.User{
		Email:     "actor@example.com",
		Password:  "hashed",
		FirstName: "Bulk",
		LastName:  "Actor",
		Role:      models.RoleAdmin,
		IsActive:  true,
	}
	s.Require().NoError(db.Create(actor).Error)
	s.actorID = actor.ID
}

func (s *BulkOperationPersistenceSuite) count(model interface{}) int64 {
	var n int64
	s.Require().NoError(s.db.Model(model).Count(&n).Error)
	return n
}

func userItem(i int) map[string]interface{} {
	return map[string]interface{}{
		"email":      fmt.Sprintf("bulk-user-%d@example.com", i),
		"password":   "hashed-password",
		"first_name": "Bulk",
		"last_name":  fmt.Sprintf("User%d", i),
		"role":       string(models.RoleCustomer),
	}
}

// TestBulkCreateUsers_DuplicateInLaterBatch_RollsBackEverything is the
// regression test for the reported data-integrity bug.
//
// CreateInBatches(…, 100) issues one INSERT per 100 rows. With 150 items where
// item 120 violates the unique email index, the first batch of 100 rows
// succeeds and the second fails. The old implementation returned the *input*
// slice as "results", swallowed the repository error inside the transaction
// closure (so the transaction COMMITTED), and reported SuccessItems: 150 /
// Status: partial while exactly 100 rows existed in the database.
func (s *BulkOperationPersistenceSuite) TestBulkCreateUsers_DuplicateInLaterBatch_RollsBackEverything() {
	items := make([]map[string]interface{}, 0, 150)
	for i := 0; i < 150; i++ {
		items = append(items, userItem(i))
	}
	// Item 120 lands in the second batch and duplicates item 5's email.
	items[120]["email"] = items[5]["email"]

	resp, err := s.service.BulkCreateUsers(&models.BulkCreateRequest{Items: items}, s.actorID)
	s.Require().NoError(err)
	s.Require().NotNil(resp)

	// The response must not claim rows succeeded.
	s.Equal(models.StatusFailed, resp.Status)
	s.Equal(0, resp.SuccessItems)
	s.Equal(150, resp.TotalItems)
	s.Greater(resp.FailedItems, 0)

	// The important half: nothing may be persisted. Only the actor remains.
	s.Equal(int64(1), s.count(&models.User{}), "bulk create must be all-or-nothing; a partial write was committed")

	// The recorded operation must agree with reality.
	op, err := s.service.GetBulkOperation(resp.OperationID)
	s.Require().NoError(err)
	s.Equal(models.StatusFailed, op.Status)
}

// TestBulkCreateUsers_DuplicateInSingleBatch_ReportsFailure covers the
// small-input case: the whole thing fits in one batch, so no partial write is
// possible, but the old code still reported every input item as a success.
func (s *BulkOperationPersistenceSuite) TestBulkCreateUsers_DuplicateInSingleBatch_ReportsFailure() {
	items := []map[string]interface{}{userItem(1), userItem(2), userItem(3)}
	items[2]["email"] = items[0]["email"]

	resp, err := s.service.BulkCreateUsers(&models.BulkCreateRequest{Items: items}, s.actorID)
	s.Require().NoError(err)
	s.Require().NotNil(resp)

	s.Equal(models.StatusFailed, resp.Status)
	s.Equal(0, resp.SuccessItems)
	s.Equal(int64(1), s.count(&models.User{}))
}

// TestBulkCreateUsers_Success verifies the happy path still persists and
// reports every row.
func (s *BulkOperationPersistenceSuite) TestBulkCreateUsers_Success() {
	items := make([]map[string]interface{}, 0, 150)
	for i := 0; i < 150; i++ {
		items = append(items, userItem(i))
	}

	resp, err := s.service.BulkCreateUsers(&models.BulkCreateRequest{Items: items}, s.actorID)
	s.Require().NoError(err)
	s.Require().NotNil(resp)

	s.Equal(models.StatusCompleted, resp.Status)
	s.Equal(150, resp.SuccessItems)
	s.Equal(0, resp.FailedItems)
	s.Equal(int64(151), s.count(&models.User{})) // 150 created + the actor

	op, err := s.service.GetBulkOperation(resp.OperationID)
	s.Require().NoError(err)
	s.Equal(models.StatusCompleted, op.Status)
}

// TestBulkCreate_AllResourceTypes_RollBackOnError applies the same
// all-or-nothing expectation to every BulkCreate* method. Each case seeds one
// row, then submits a batch in which one item collides with that row's primary
// key: the insert fails, so the whole batch must be rolled back and reported
// as a failure.
func (s *BulkOperationPersistenceSuite) TestBulkCreate_AllResourceTypes_RollBackOnError() {
	// Seed rows the bulk creates will collide with. IDs are chosen high so
	// they never clash with auto-increment values assigned during the test.
	seedCustomer := &models.Customer{
		BaseModel: models.BaseModel{ID: 900},
		FirstName: "Seed", LastName: "Customer", Email: "seed-customer@example.com",
	}
	s.Require().NoError(s.db.Create(seedCustomer).Error)

	seedLead := &models.Lead{
		BaseModel: models.BaseModel{ID: 901},
		FirstName: "Seed", LastName: "Lead", Email: "seed-lead@example.com",
		Status: models.LeadStatusNew, OwnerID: s.actorID,
	}
	s.Require().NoError(s.db.Create(seedLead).Error)

	seedTask := &models.Task{
		BaseModel: models.BaseModel{ID: 902},
		Title:     "Seed task", Status: models.TaskStatusPending,
		Priority: models.TaskPriorityMedium, AssignedToID: s.actorID,
	}
	s.Require().NoError(s.db.Create(seedTask).Error)

	seedTicket := &models.Ticket{
		BaseModel: models.BaseModel{ID: 903},
		Title:     "Seed ticket", Description: "Seed", Status: models.TicketStatusOpen,
		Priority: models.TicketPriorityMedium, CustomerID: seedCustomer.ID,
	}
	s.Require().NoError(s.db.Create(seedTicket).Error)

	seedUser := &models.User{
		BaseModel: models.BaseModel{ID: 904},
		Email:     "seed-user@example.com", Password: "hashed",
		FirstName: "Seed", LastName: "User", Role: models.RoleCustomer, IsActive: true,
	}
	s.Require().NoError(s.db.Create(seedUser).Error)

	cases := []struct {
		name      string
		call      func(*models.BulkCreateRequest) (*models.BulkResponse, error)
		model     interface{}
		baseCount int64
		items     []map[string]interface{}
	}{
		{
			name:      "users",
			call:      func(r *models.BulkCreateRequest) (*models.BulkResponse, error) { return s.service.BulkCreateUsers(r, s.actorID) },
			model:     &models.User{},
			baseCount: 2, // actor + seed user
			items: []map[string]interface{}{
				{"email": "u-a@example.com", "password": "p", "first_name": "A", "last_name": "A"},
				{"id": seedUser.ID, "email": "u-b@example.com", "password": "p", "first_name": "B", "last_name": "B"},
			},
		},
		{
			name:      "leads",
			call:      func(r *models.BulkCreateRequest) (*models.BulkResponse, error) { return s.service.BulkCreateLeads(r, s.actorID) },
			model:     &models.Lead{},
			baseCount: 1,
			items: []map[string]interface{}{
				{"first_name": "A", "last_name": "A", "email": "l-a@example.com"},
				{"id": seedLead.ID, "first_name": "B", "last_name": "B", "email": "l-b@example.com"},
			},
		},
		{
			name:      "customers",
			call:      func(r *models.BulkCreateRequest) (*models.BulkResponse, error) { return s.service.BulkCreateCustomers(r, s.actorID) },
			model:     &models.Customer{},
			baseCount: 1,
			items: []map[string]interface{}{
				{"first_name": "A", "last_name": "A", "email": "c-a@example.com"},
				{"id": seedCustomer.ID, "first_name": "B", "last_name": "B", "email": "c-b@example.com"},
			},
		},
		{
			name:      "tasks",
			call:      func(r *models.BulkCreateRequest) (*models.BulkResponse, error) { return s.service.BulkCreateTasks(r, s.actorID) },
			model:     &models.Task{},
			baseCount: 1,
			items: []map[string]interface{}{
				{"title": "A"},
				{"id": seedTask.ID, "title": "B"},
			},
		},
		{
			name:      "tickets",
			call:      func(r *models.BulkCreateRequest) (*models.BulkResponse, error) { return s.service.BulkCreateTickets(r, s.actorID) },
			model:     &models.Ticket{},
			baseCount: 1,
			items: []map[string]interface{}{
				{"title": "A", "description": "A", "customer_id": seedCustomer.ID},
				{"id": seedTicket.ID, "title": "B", "description": "B", "customer_id": seedCustomer.ID},
			},
		},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			before := s.count(tc.model)
			require.Equal(s.T(), tc.baseCount, before, "unexpected starting row count for %s", tc.name)

			resp, err := tc.call(&models.BulkCreateRequest{Items: tc.items})
			s.Require().NoError(err)
			s.Require().NotNil(resp)

			s.Equal(models.StatusFailed, resp.Status, "%s: failed bulk create must report failed", tc.name)
			s.Equal(0, resp.SuccessItems, "%s: no item may be reported as created", tc.name)
			s.Equal(before, s.count(tc.model), "%s: failed bulk create must not persist anything", tc.name)
		})
	}
}

// TestBulkDeleteUsers_MissingIDIsNotASuccess covers the delete path: deletes
// are executed one statement per ID so partial success is genuine, but an ID
// that matched no row must not be counted as deleted.
func (s *BulkOperationPersistenceSuite) TestBulkDeleteUsers_MissingIDIsNotASuccess() {
	victim := &models.User{
		Email: "victim@example.com", Password: "hashed",
		FirstName: "V", LastName: "V", Role: models.RoleCustomer, IsActive: true,
	}
	s.Require().NoError(s.db.Create(victim).Error)

	resp, err := s.service.BulkDeleteUsers(&models.BulkDeleteRequest{
		IDs: []uint{victim.ID, 999999},
	}, s.actorID)
	s.Require().NoError(err)
	s.Require().NotNil(resp)

	s.Equal(models.StatusPartial, resp.Status)
	s.Equal(1, resp.SuccessItems, "only the row that actually existed may count as deleted")
	s.Equal(1, resp.FailedItems)
	s.Equal(int64(1), s.count(&models.User{})) // only the actor left
}

// TestBulkDeleteUsers_SelfDeleteIsReported ensures the silently skipped
// self-deletion is accounted for rather than vanishing from the totals.
func (s *BulkOperationPersistenceSuite) TestBulkDeleteUsers_SelfDeleteIsReported() {
	victim := &models.User{
		Email: "victim2@example.com", Password: "hashed",
		FirstName: "V", LastName: "V", Role: models.RoleCustomer, IsActive: true,
	}
	s.Require().NoError(s.db.Create(victim).Error)

	resp, err := s.service.BulkDeleteUsers(&models.BulkDeleteRequest{
		IDs: []uint{s.actorID, victim.ID},
	}, s.actorID)
	s.Require().NoError(err)
	s.Require().NotNil(resp)

	s.Equal(2, resp.TotalItems)
	s.Equal(1, resp.SuccessItems)
	s.Equal(1, resp.FailedItems)
	s.Equal(models.StatusPartial, resp.Status)

	// The actor is still there, the victim is gone.
	s.Equal(int64(1), s.count(&models.User{}))
}

// TestBulkUpdateUsers_PartialSuccessIsReal verifies the update path, which was
// already correct: it updates one row per statement, so the successes it
// reports really are persisted and the failures really are not.
func (s *BulkOperationPersistenceSuite) TestBulkUpdateUsers_PartialSuccessIsReal() {
	target := &models.User{
		Email: "target@example.com", Password: "hashed",
		FirstName: "T", LastName: "T", Role: models.RoleCustomer, IsActive: true,
	}
	s.Require().NoError(s.db.Create(target).Error)

	resp, err := s.service.BulkUpdateUsers(&models.BulkUpdateRequest{
		Items: []models.BulkUpdateItem{
			{ID: target.ID, Updates: map[string]interface{}{"first_name": "Updated"}},
			{ID: 999999, Updates: map[string]interface{}{"first_name": "Ghost"}},
		},
	}, s.actorID)
	s.Require().NoError(err)
	s.Require().NotNil(resp)

	s.Equal(models.StatusPartial, resp.Status)
	s.Equal(1, resp.SuccessItems)
	s.Equal(1, resp.FailedItems)

	var reloaded models.User
	s.Require().NoError(s.db.First(&reloaded, target.ID).Error)
	s.Equal("Updated", reloaded.FirstName, "the row reported as updated must actually be updated")
}

// TestBulkDeleteUsers_ErasesRatherThanHides is the delete path's privacy
// contract, checked through the service the handler calls.
//
// A bulk delete is a deletion of a PERSON and has to mean what a single delete
// means: the row is anonymised in place, the credentials are purged, and only
// then is it soft-deleted. It used to be a bare soft delete, so the same
// operation erased the person through one endpoint and left every field of
// their data — and working API keys — in the database through the other.
func (s *BulkOperationPersistenceSuite) TestBulkDeleteUsers_ErasesRatherThanHides() {
	victim := &models.User{
		Email: "bulk-victim@example.com", Password: "hashed",
		FirstName: "Bulk", LastName: "Victim", Role: models.RoleCustomer, IsActive: true,
	}
	s.Require().NoError(s.db.Create(victim).Error)
	s.Require().NoError(s.db.Create(&models.APIKey{
		Name: "ci", KeyHash: "hash-bulk-victim", Prefix: "gcrm_v",
		UserID: victim.ID, IsActive: true,
	}).Error)

	resp, err := s.service.BulkDeleteUsers(&models.BulkDeleteRequest{IDs: []uint{victim.ID}}, s.actorID)
	s.Require().NoError(err)
	s.Equal(models.StatusCompleted, resp.Status)
	s.Equal(1, resp.SuccessItems)

	// Read the retained row: it must no longer describe anybody.
	var erased models.User
	s.Require().NoError(s.db.Unscoped().First(&erased, victim.ID).Error)
	s.True(erased.DeletedAt.Valid)
	s.NotContains(erased.Email, "bulk-victim")
	s.Contains(erased.Email, "@anonymized.invalid")
	s.Empty(erased.FirstName)
	s.Empty(erased.LastName)
	s.False(erased.IsActive)

	var keys int64
	s.Require().NoError(s.db.Unscoped().Model(&models.APIKey{}).
		Where("user_id = ?", victim.ID).Count(&keys).Error)
	s.Zero(keys, "a bulk-deleted user's API keys must not go on authenticating")
}

// TestBulkDeleteCustomers_ErasesTheOriginatingLead covers the cross-table leak
// from the bulk side: the customer was converted from a lead, and that lead
// still holds a full copy of the person's name, email, phone and company.
func (s *BulkOperationPersistenceSuite) TestBulkDeleteCustomers_ErasesTheOriginatingLead() {
	lead := &models.Lead{
		FirstName: "Convert", LastName: "Me",
		Email:   "bulk-convert@example.com",
		Phone:   "+40 733 111 222",
		Company: "Convert SRL",
		Status:  models.LeadStatusQualified,
		OwnerID: s.actorID,
	}
	s.Require().NoError(s.db.Create(lead).Error)

	leadService := NewLeadService(
		repository.NewLeadRepository(s.db),
		repository.NewCustomerRepository(s.db),
		utils.NewTransactionManager(s.db),
	)
	customer, err := leadService.ConvertToCustomer(lead.ID, &models.Customer{})
	s.Require().NoError(err)

	resp, err := s.service.BulkDeleteCustomers(&models.BulkDeleteRequest{IDs: []uint{customer.ID}}, s.actorID)
	s.Require().NoError(err)
	s.Equal(models.StatusCompleted, resp.Status)
	s.Equal(1, resp.SuccessItems)

	var erasedLead models.Lead
	s.Require().NoError(s.db.Unscoped().First(&erasedLead, lead.ID).Error)
	s.True(erasedLead.DeletedAt.Valid, "the originating lead must be erased with the customer")
	s.NotContains(erasedLead.Email, "bulk-convert")
	s.Empty(erasedLead.FirstName)
	s.Empty(erasedLead.Phone)
	s.Empty(erasedLead.Company)
}

// TestBulkDeleteLeads_ErasesTheConvertedCustomer is the same leak from the lead
// side.
func (s *BulkOperationPersistenceSuite) TestBulkDeleteLeads_ErasesTheConvertedCustomer() {
	lead := &models.Lead{
		FirstName: "Other", LastName: "Direction",
		Email:   "bulk-other-direction@example.com",
		Phone:   "+40 744 555 666",
		Status:  models.LeadStatusQualified,
		OwnerID: s.actorID,
	}
	s.Require().NoError(s.db.Create(lead).Error)

	leadService := NewLeadService(
		repository.NewLeadRepository(s.db),
		repository.NewCustomerRepository(s.db),
		utils.NewTransactionManager(s.db),
	)
	customer, err := leadService.ConvertToCustomer(lead.ID, &models.Customer{})
	s.Require().NoError(err)

	resp, err := s.service.BulkDeleteLeads(&models.BulkDeleteRequest{IDs: []uint{lead.ID}}, s.actorID)
	s.Require().NoError(err)
	s.Equal(models.StatusCompleted, resp.Status)
	s.Equal(1, resp.SuccessItems)

	var erasedCustomer models.Customer
	s.Require().NoError(s.db.Unscoped().First(&erasedCustomer, customer.ID).Error)
	s.True(erasedCustomer.DeletedAt.Valid, "the customer the lead became must be erased with it")
	s.NotContains(erasedCustomer.Email, "bulk-other-direction")
	s.Empty(erasedCustomer.FirstName)
	s.Empty(erasedCustomer.Phone)
}
