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

// Helper function to handle bulk operations with error tracking
func (r *bulkRepository) bulkCreate(entities interface{}, tableName string) (interface{}, []error) {
	var errors []error
	
	// Use CreateInBatches for better performance
	if err := r.db.CreateInBatches(entities, 100).Error; err != nil {
		errors = append(errors, err)
		return entities, errors
	}
	
	return entities, errors
}

// Helper function to handle bulk updates
func (r *bulkRepository) bulkUpdate(updates []models.BulkUpdateItem, model interface{}, tableName string) (interface{}, []error) {
	var errors []error
	var results []interface{}
	
	for _, update := range updates {
		result := model
		err := r.db.Model(result).Where("id = ?", update.ID).Updates(update.Updates).Error
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to update %s with ID %d: %w", tableName, update.ID, err))
			continue
		}
		
		// Fetch the updated record
		err = r.db.First(result, update.ID).Error
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to fetch updated %s with ID %d: %w", tableName, update.ID, err))
			continue
		}
		
		results = append(results, result)
	}
	
	return results, errors
}

// Helper function to handle bulk deletes
func (r *bulkRepository) bulkDelete(ids []uint, model interface{}, tableName string) []error {
	var errors []error
	
	for _, id := range ids {
		err := r.db.Delete(model, id).Error
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to delete %s with ID %d: %w", tableName, id, err))
		}
	}
	
	return errors
}

// User bulk operations
func (r *bulkRepository) BulkCreateUsers(users []models.User) ([]models.User, []error) {
	result, errors := r.bulkCreate(&users, "users")
	return result.([]models.User), errors
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

func (r *bulkRepository) BulkDeleteUsers(ids []uint) []error {
	return r.bulkDelete(ids, &models.User{}, "users")
}

// Lead bulk operations
func (r *bulkRepository) BulkCreateLeads(leads []models.Lead) ([]models.Lead, []error) {
	result, errors := r.bulkCreate(&leads, "leads")
	return result.([]models.Lead), errors
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

func (r *bulkRepository) BulkDeleteLeads(ids []uint) []error {
	return r.bulkDelete(ids, &models.Lead{}, "leads")
}

// Customer bulk operations
func (r *bulkRepository) BulkCreateCustomers(customers []models.Customer) ([]models.Customer, []error) {
	result, errors := r.bulkCreate(&customers, "customers")
	return result.([]models.Customer), errors
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

func (r *bulkRepository) BulkDeleteCustomers(ids []uint) []error {
	return r.bulkDelete(ids, &models.Customer{}, "customers")
}

// Task bulk operations
func (r *bulkRepository) BulkCreateTasks(tasks []models.Task) ([]models.Task, []error) {
	result, errors := r.bulkCreate(&tasks, "tasks")
	return result.([]models.Task), errors
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

func (r *bulkRepository) BulkDeleteTasks(ids []uint) []error {
	return r.bulkDelete(ids, &models.Task{}, "tasks")
}

// Ticket bulk operations
func (r *bulkRepository) BulkCreateTickets(tickets []models.Ticket) ([]models.Ticket, []error) {
	result, errors := r.bulkCreate(&tickets, "tickets")
	return result.([]models.Ticket), errors
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

func (r *bulkRepository) BulkDeleteTickets(ids []uint) []error {
	return r.bulkDelete(ids, &models.Ticket{}, "tickets")
}

// Helper functions for data conversion
func convertMapToModel(data map[string]interface{}, model interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(jsonData, model)
}