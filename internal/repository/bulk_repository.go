package repository

import (
	"encoding/json"
	"fmt"
	
	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
)

type bulkRepository struct {
	db *gorm.DB
}

func NewBulkRepository(db *gorm.DB) BulkRepository {
	return &bulkRepository{db: db}
}

func (r *bulkRepository) WithTx(tx *gorm.DB) BulkRepository {
	return &bulkRepository{db: tx}
}

// bulkCreate inserts entities in batches.
//
// Bulk create is all-or-nothing: CreateInBatches issues several multi-row
// INSERT statements, so a failure part way through leaves an arbitrary prefix
// of the input persisted and the remainder untouched. There is no reliable way
// to say which individual rows made it, so on any error we report NO results
// at all and expect the caller to roll the surrounding transaction back. The
// caller must never treat the input slice as "what was persisted".
func bulkCreate[T any](db *gorm.DB, entities []T, tableName string) ([]T, []error) {
	if len(entities) == 0 {
		return nil, nil
	}

	// Use CreateInBatches for better performance
	if err := db.CreateInBatches(&entities, 100).Error; err != nil {
		return nil, []error{fmt.Errorf("failed to bulk create %s: %w", tableName, err)}
	}

	return entities, nil
}

// Helper function to handle bulk deletes of entities that hold no personal
// data of their own — tasks and tickets. They carry a title, a description and
// foreign keys to the people involved, and those people are erased through
// their own rows, so a plain soft delete is the whole of it here.
//
// Deletes are performed one statement per ID, so partial success is real: the
// rows reported as deleted are exactly the rows removed. An ID that matched no
// row is reported as an error rather than silently counted as a success.
func (r *bulkRepository) bulkDelete(ids []uint, model interface{}, tableName string) []error {
	var errors []error

	for _, id := range ids {
		result := r.db.Delete(model, id)
		if result.Error != nil {
			errors = append(errors, fmt.Errorf("failed to delete %s with ID %d: %w", tableName, id, result.Error))
			continue
		}
		if result.RowsAffected == 0 {
			errors = append(errors, fmt.Errorf("failed to delete %s with ID %d: %w", tableName, id, gorm.ErrRecordNotFound))
		}
	}

	return errors
}

// bulkErase is bulkDelete for the entities that DO hold personal data: users,
// customers and leads.
//
// Deleting one of those is a GDPR Article 17 erasure — anonymise the row in
// place, purge whatever must not outlive the person, then soft-delete — and
// which endpoint the request came through cannot change that. The bulk paths
// used to issue a bare soft delete instead, so the very same operation erased
// the person when it went through DELETE /users/:id and merely hid them when it
// went through the bulk endpoint, leaving their name, email and credentials in
// the database and their address locked in the unique index for good. There is
// no separate "bulk delete" semantics: erase is passed the single-record
// erasure and this function only handles the per-item accounting around it.
//
// That accounting is unchanged from bulkDelete. Each ID is erased on its own so
// partial success stays meaningful, and an ID that matches no live row is
// reported as an error instead of being counted as a success — checked up front
// here because an erasure spans several statements and has no single
// RowsAffected to test.
//
// "On its own" is a requirement on erase, not a description of this loop. This
// function appends a failure and continues, and the caller commits, so an erase
// that is not isolated has its half-finished statements committed by the very
// batch that reported it as failed. Every caller therefore passes an erase built
// on runIsolated (see BulkDeleteUsers) or on the cascade's batch-item helpers.
func bulkErase[T any](db *gorm.DB, ids []uint, tableName string, erasedInThisBatch func(id uint) bool, erase func(id uint) error) []error {
	var errs []error

	for _, id := range ids {
		// The row may already be gone because an earlier ID in this same batch
		// cascaded into it — a lead and the customer it was converted into are
		// one person, and erasing either erases both. It is gone, which is what
		// was asked for, so it counts as a success rather than as a missing row.
		if erasedInThisBatch != nil && erasedInThisBatch(id) {
			continue
		}

		var entity T
		var found int64
		if err := db.Model(&entity).Where("id = ?", id).Count(&found).Error; err != nil {
			errs = append(errs, fmt.Errorf("failed to delete %s with ID %d: %w", tableName, id, err))
			continue
		}
		if found == 0 {
			errs = append(errs, fmt.Errorf("failed to delete %s with ID %d: %w", tableName, id, gorm.ErrRecordNotFound))
			continue
		}

		if err := erase(id); err != nil {
			errs = append(errs, fmt.Errorf("failed to delete %s with ID %d: %w", tableName, id, err))
		}
	}

	return errs
}

// User bulk operations
func (r *bulkRepository) BulkCreateUsers(users []models.User) ([]models.User, []error) {
	return bulkCreate(r.db, users, "users")
}

func (r *bulkRepository) BulkUpdateUsers(updates []models.BulkUpdateItem) ([]models.User, []error) {
	var users []models.User
	var errors []error
	
	for _, update := range updates {
		user := models.User{}
		err := r.db.Model(&user).Where("id = ?", update.ID).Updates(update.Updates).Error
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to update user with ID %d: %w", update.ID, err))
			continue
		}
		
		// Fetch the updated record
		err = r.db.First(&user, update.ID).Error
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to fetch updated user with ID %d: %w", update.ID, err))
			continue
		}
		
		users = append(users, user)
	}
	
	return users, errors
}

// BulkDeleteUsers erases every listed user: personal fields scrubbed, password
// hash made unusable, API keys and refresh tokens purged, row soft-deleted. It
// delegates to the user repository so there is exactly one definition of what
// erasing a user means.
//
// Each user is erased through runIsolated, so an item that fails rolls back on
// its own. Handing the repository r.db directly would let a failing item's scrub
// and credential purge ride out on the transaction the caller wrapped the batch
// in and be committed alongside the items that worked — reported as a failure,
// persisted as a half-erasure.
func (r *bulkRepository) BulkDeleteUsers(ids []uint) []error {
	return bulkErase[models.User](r.db, ids, "users", nil, func(id uint) error {
		return runIsolated(r.db, func(tx *gorm.DB) error {
			return NewUserRepository(tx).Delete(id)
		})
	})
}

// Lead bulk operations
func (r *bulkRepository) BulkCreateLeads(leads []models.Lead) ([]models.Lead, []error) {
	return bulkCreate(r.db, leads, "leads")
}

func (r *bulkRepository) BulkUpdateLeads(updates []models.BulkUpdateItem) ([]models.Lead, []error) {
	var leads []models.Lead
	var errors []error
	
	for _, update := range updates {
		lead := models.Lead{}
		err := r.db.Model(&lead).Where("id = ?", update.ID).Updates(update.Updates).Error
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to update lead with ID %d: %w", update.ID, err))
			continue
		}
		
		// Fetch the updated record
		err = r.db.First(&lead, update.ID).Error
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to fetch updated lead with ID %d: %w", update.ID, err))
			continue
		}
		
		leads = append(leads, lead)
	}
	
	return leads, errors
}

// BulkDeleteLeads erases every listed lead, and with each converted lead the
// customer it became — one cascade is shared by the whole batch so that a lead
// erased as a side effect of an earlier ID is recognised rather than reported
// as missing. eraseLeadAsBatchItem keeps that shared memory honest: an item that
// fails rolls back its statements AND every mark it left behind, so the batch
// can never skip a row it did not actually erase.
func (r *bulkRepository) BulkDeleteLeads(ids []uint) []error {
	cascade := newErasureCascade()
	return bulkErase[models.Lead](r.db, ids, "leads", cascade.leadErased, func(id uint) error {
		return cascade.eraseLeadAsBatchItem(r.db, id)
	})
}

// Customer bulk operations
func (r *bulkRepository) BulkCreateCustomers(customers []models.Customer) ([]models.Customer, []error) {
	return bulkCreate(r.db, customers, "customers")
}

func (r *bulkRepository) BulkUpdateCustomers(updates []models.BulkUpdateItem) ([]models.Customer, []error) {
	var customers []models.Customer
	var errors []error
	
	for _, update := range updates {
		customer := models.Customer{}
		err := r.db.Model(&customer).Where("id = ?", update.ID).Updates(update.Updates).Error
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to update customer with ID %d: %w", update.ID, err))
			continue
		}
		
		// Fetch the updated record
		err = r.db.First(&customer, update.ID).Error
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to fetch updated customer with ID %d: %w", update.ID, err))
			continue
		}
		
		customers = append(customers, customer)
	}
	
	return customers, errors
}

// BulkDeleteCustomers erases every listed customer, and with each converted
// customer the lead it came from, which still holds a copy of the same person's
// name, email, phone and company.
func (r *bulkRepository) BulkDeleteCustomers(ids []uint) []error {
	cascade := newErasureCascade()
	return bulkErase[models.Customer](r.db, ids, "customers", cascade.customerErased, func(id uint) error {
		return cascade.eraseCustomerAsBatchItem(r.db, id)
	})
}

// Task bulk operations
func (r *bulkRepository) BulkCreateTasks(tasks []models.Task) ([]models.Task, []error) {
	return bulkCreate(r.db, tasks, "tasks")
}

func (r *bulkRepository) BulkUpdateTasks(updates []models.BulkUpdateItem) ([]models.Task, []error) {
	var tasks []models.Task
	var errors []error
	
	for _, update := range updates {
		task := models.Task{}
		err := r.db.Model(&task).Where("id = ?", update.ID).Updates(update.Updates).Error
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to update task with ID %d: %w", update.ID, err))
			continue
		}
		
		// Fetch the updated record
		err = r.db.First(&task, update.ID).Error
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to fetch updated task with ID %d: %w", update.ID, err))
			continue
		}
		
		tasks = append(tasks, task)
	}
	
	return tasks, errors
}

// BulkDeleteTasks is a plain soft delete. A task holds a title, a description
// and foreign keys to the user, lead or customer it concerns; the people are
// erased through their own rows, so there is nothing here to anonymise.
func (r *bulkRepository) BulkDeleteTasks(ids []uint) []error {
	return r.bulkDelete(ids, &models.Task{}, "tasks")
}

// Ticket bulk operations
func (r *bulkRepository) BulkCreateTickets(tickets []models.Ticket) ([]models.Ticket, []error) {
	return bulkCreate(r.db, tickets, "tickets")
}

func (r *bulkRepository) BulkUpdateTickets(updates []models.BulkUpdateItem) ([]models.Ticket, []error) {
	var tickets []models.Ticket
	var errors []error
	
	for _, update := range updates {
		ticket := models.Ticket{}
		err := r.db.Model(&ticket).Where("id = ?", update.ID).Updates(update.Updates).Error
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to update ticket with ID %d: %w", update.ID, err))
			continue
		}
		
		// Fetch the updated record
		err = r.db.First(&ticket, update.ID).Error
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to fetch updated ticket with ID %d: %w", update.ID, err))
			continue
		}
		
		tickets = append(tickets, ticket)
	}
	
	return tickets, errors
}

// BulkDeleteTickets is a plain soft delete, for the same reason as tasks: a
// ticket's subject and description belong to the support case, and the customer
// it points at is erased through the customers table.
func (r *bulkRepository) BulkDeleteTickets(ids []uint) []error {
	return r.bulkDelete(ids, &models.Ticket{}, "tickets")
}

// Bulk status updates
//
// These are the read and write halves of a bulk status change. They are kept
// apart so the service can authorize every row before a single one is written:
// the read returns whatever exists (a caller comparing the result against the
// requested IDs learns which are missing, without a per-ID round trip), and the
// write sets one column on all of them in a single statement.
//
// Unlike the bulk delete paths, nothing here touches personal data — status is
// a workflow field — so no erasure is involved and no per-item isolation is
// needed. All-or-nothing is the whole contract: the caller runs both halves
// inside one transaction and returns an error to roll it back.

// getByIDs loads whatever rows exist among ids. Soft-deleted rows are excluded
// by GORM's default scope, so a deleted record reads as missing, which is what
// it is as far as the API is concerned.
func getByIDs[T any](db *gorm.DB, ids []uint) ([]T, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var entities []T
	if err := db.Where("id IN ?", ids).Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// setStatus writes one status onto every listed row.
//
// RowsAffected is deliberately not treated as a count of updated records: MySQL
// reports rows *changed*, not rows *matched*, so a record that already holds the
// requested status is not counted. Existence is established by the caller's
// preceding read instead.
func setStatus(db *gorm.DB, model interface{}, ids []uint, status interface{}) error {
	if len(ids) == 0 {
		return nil
	}
	return db.Model(model).Where("id IN ?", ids).Update("status", status).Error
}

func (r *bulkRepository) GetLeadsByIDs(ids []uint) ([]models.Lead, error) {
	return getByIDs[models.Lead](r.db, ids)
}

func (r *bulkRepository) GetTicketsByIDs(ids []uint) ([]models.Ticket, error) {
	return getByIDs[models.Ticket](r.db, ids)
}

func (r *bulkRepository) GetTasksByIDs(ids []uint) ([]models.Task, error) {
	return getByIDs[models.Task](r.db, ids)
}

func (r *bulkRepository) SetLeadStatus(ids []uint, status models.LeadStatus) error {
	return setStatus(r.db, &models.Lead{}, ids, status)
}

func (r *bulkRepository) SetTicketStatus(ids []uint, status models.TicketStatus) error {
	return setStatus(r.db, &models.Ticket{}, ids, status)
}

func (r *bulkRepository) SetTaskStatus(ids []uint, status models.TaskStatus) error {
	return setStatus(r.db, &models.Task{}, ids, status)
}

// Helper functions for data conversion
func convertMapToModel(data map[string]interface{}, model interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(jsonData, model)
}