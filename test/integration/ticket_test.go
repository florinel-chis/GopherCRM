package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/florinel-chis/gocrm/internal/models"
	"github.com/florinel-chis/gocrm/internal/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

type TicketIntegrationTestSuite struct {
	BaseIntegrationTestSuite
	adminToken   string
	supportToken string
	salesToken   string
	customerID   uint
}

func (suite *TicketIntegrationTestSuite) SetupSuite() {
	suite.BaseIntegrationTestSuite.SetupSuite()
	
	// Create users and get tokens
	suite.CreateUser("admin@example.com", "password123", models.RoleAdmin)
	suite.adminToken = suite.GetAuthToken("admin@example.com", "password123")
	
	suite.CreateUser("support@example.com", "password123", models.RoleSupport)
	suite.supportToken = suite.GetAuthToken("support@example.com", "password123")
	
	salesUser := suite.CreateUser("sales@example.com", "password123", models.RoleSales)
	suite.salesToken = suite.GetAuthToken("sales@example.com", "password123")
	
	// Create a customer for testing
	customer := &models.Customer{
		FirstName: "John",
		LastName:  "Doe",
		Email:     "john.doe@example.com",
		Phone:     "+1234567890",
		Company:   "ACME Corp",
		UserID:    &salesUser.ID,
	}
	err := suite.db.Create(customer).Error
	assert.NoError(suite.T(), err)
	suite.customerID = customer.ID
}

func (suite *TicketIntegrationTestSuite) TearDownSuite() {
	// Clean up test data
	suite.db.Unscoped().Where("email LIKE ?", "%@example.com").Delete(&models.User{})
	suite.db.Unscoped().Where("email = ?", "john.doe@example.com").Delete(&models.Customer{})
	suite.db.Unscoped().Delete(&models.Ticket{})
	
	suite.BaseIntegrationTestSuite.TearDownSuite()
}

func (suite *TicketIntegrationTestSuite) TestTicketLifecycle() {
	// Test 1: Create ticket as support user
	createReq := map[string]interface{}{
		"title":       "Test Ticket",
		"description": "This is a test ticket",
		"priority":    "high",
		"customer_id": suite.customerID,
	}
	
	body, _ := json.Marshal(createReq)
	req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/tickets", suite.baseURL), bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.supportToken)
	
	resp, err := suite.client.Do(req)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), http.StatusCreated, resp.StatusCode)
	
	var createResp utils.APIResponse
	err = json.NewDecoder(resp.Body).Decode(&createResp)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), createResp.Success)
	
	ticketData := createResp.Data.(map[string]interface{})
	ticketID := uint(ticketData["id"].(float64))
	
	// Test 2: Get ticket
	req, _ = http.NewRequest("GET", fmt.Sprintf("%s/api/v1/tickets/%d", suite.baseURL, ticketID), nil)
	req.Header.Set("Authorization", "Bearer "+suite.supportToken)
	
	resp, err = suite.client.Do(req)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), http.StatusOK, resp.StatusCode)
	
	// Test 3: Update ticket status
	updateReq := map[string]interface{}{
		"status": "in_progress",
	}
	
	body, _ = json.Marshal(updateReq)
	req, _ = http.NewRequest("PUT", fmt.Sprintf("%s/api/v1/tickets/%d", suite.baseURL, ticketID), bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.supportToken)
	
	resp, err = suite.client.Do(req)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), http.StatusOK, resp.StatusCode)
	
	// Test 4: List tickets
	req, _ = http.NewRequest("GET", fmt.Sprintf("%s/api/v1/tickets", suite.baseURL), nil)
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)
	
	resp, err = suite.client.Do(req)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), http.StatusOK, resp.StatusCode)
	
	// Test 5: Delete ticket (admin only)
	req, _ = http.NewRequest("DELETE", fmt.Sprintf("%s/api/v1/tickets/%d", suite.baseURL, ticketID), nil)
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)
	
	resp, err = suite.client.Do(req)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), http.StatusNoContent, resp.StatusCode)
}

func (suite *TicketIntegrationTestSuite) TestTicketPermissions() {
	// Create a ticket assigned to a specific support user
	supportUser := &models.User{}
	err := suite.db.Where("email = ?", "support@example.com").First(supportUser).Error
	assert.NoError(suite.T(), err)
	
	ticket := &models.Ticket{
		Title:        "Permission Test Ticket",
		Description:  "Testing permissions",
		Status:       models.TicketStatusOpen,
		Priority:     models.TicketPriorityMedium,
		CustomerID:   suite.customerID,
		AssignedToID: &supportUser.ID,
	}
	err = suite.db.Create(ticket).Error
	assert.NoError(suite.T(), err)
	
	// Test 1: Sales user cannot create tickets
	createReq := map[string]interface{}{
		"title":       "Sales Ticket",
		"description": "This should fail",
		"customer_id": suite.customerID,
	}
	
	body, _ := json.Marshal(createReq)
	req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/tickets", suite.baseURL), bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.salesToken)
	
	resp, err := suite.client.Do(req)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), http.StatusForbidden, resp.StatusCode)
	
	// Test 2: Support user can only update their assigned tickets
	// Create another support user
	suite.CreateUser("support2@example.com", "password123", models.RoleSupport)
	support2Token := suite.GetAuthToken("support2@example.com", "password123")
	
	updateReq := map[string]interface{}{
		"status": "resolved",
	}
	
	body, _ = json.Marshal(updateReq)
	req, _ = http.NewRequest("PUT", fmt.Sprintf("%s/api/v1/tickets/%d", suite.baseURL, ticket.ID), bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+support2Token)
	
	resp, err = suite.client.Do(req)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), http.StatusForbidden, resp.StatusCode)
	
	// Test 3: Only admin can delete tickets
	req, _ = http.NewRequest("DELETE", fmt.Sprintf("%s/api/v1/tickets/%d", suite.baseURL, ticket.ID), nil)
	req.Header.Set("Authorization", "Bearer "+suite.supportToken)
	
	resp, err = suite.client.Do(req)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), http.StatusForbidden, resp.StatusCode)
	
	// Admin can delete
	req, _ = http.NewRequest("DELETE", fmt.Sprintf("%s/api/v1/tickets/%d", suite.baseURL, ticket.ID), nil)
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)
	
	resp, err = suite.client.Do(req)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), http.StatusNoContent, resp.StatusCode)
	
	// Clean up
	suite.db.Unscoped().Where("email = ?", "support2@example.com").Delete(&models.User{})
}

func (suite *TicketIntegrationTestSuite) TestListByCustomer() {
	// Create multiple tickets for the customer
	for i := 1; i <= 3; i++ {
		ticket := &models.Ticket{
			Title:       fmt.Sprintf("Customer Ticket %d", i),
			Description: fmt.Sprintf("Ticket %d for customer", i),
			Status:      models.TicketStatusOpen,
			Priority:    models.TicketPriorityMedium,
			CustomerID:  suite.customerID,
		}
		err := suite.db.Create(ticket).Error
		assert.NoError(suite.T(), err)
	}
	
	// List tickets by customer
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/customers/%d/tickets", suite.baseURL, suite.customerID), nil)
	req.Header.Set("Authorization", "Bearer "+suite.supportToken)
	
	resp, err := suite.client.Do(req)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), http.StatusOK, resp.StatusCode)
	
	var listResp utils.APIResponse
	err = json.NewDecoder(resp.Body).Decode(&listResp)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), listResp.Success)
	
	data := listResp.Data.(map[string]interface{})
	tickets := data["tickets"].([]interface{})
	assert.GreaterOrEqual(suite.T(), len(tickets), 3)
}

func (suite *TicketIntegrationTestSuite) TestListMyTickets() {
	// Get support user
	supportUser := &models.User{}
	err := suite.db.Where("email = ?", "support@example.com").First(supportUser).Error
	assert.NoError(suite.T(), err)
	
	// Create tickets assigned to support user
	for i := 1; i <= 2; i++ {
		ticket := &models.Ticket{
			Title:        fmt.Sprintf("My Ticket %d", i),
			Description:  fmt.Sprintf("Assigned ticket %d", i),
			Status:       models.TicketStatusOpen,
			Priority:     models.TicketPriorityHigh,
			CustomerID:   suite.customerID,
			AssignedToID: &supportUser.ID,
		}
		err := suite.db.Create(ticket).Error
		assert.NoError(suite.T(), err)
	}
	
	// List my tickets
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/tickets/my", suite.baseURL), nil)
	req.Header.Set("Authorization", "Bearer "+suite.supportToken)
	
	resp, err := suite.client.Do(req)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), http.StatusOK, resp.StatusCode)
	
	var listResp utils.APIResponse
	err = json.NewDecoder(resp.Body).Decode(&listResp)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), listResp.Success)
	
	data := listResp.Data.(map[string]interface{})
	tickets := data["tickets"].([]interface{})
	assert.GreaterOrEqual(suite.T(), len(tickets), 2)
}

func (suite *TicketIntegrationTestSuite) TestTicketStatusTransitions() {
	// Create a ticket
	ticket := &models.Ticket{
		Title:       "Status Test Ticket",
		Description: "Testing status transitions",
		Status:      models.TicketStatusOpen,
		Priority:    models.TicketPriorityMedium,
		CustomerID:  suite.customerID,
	}
	err := suite.db.Create(ticket).Error
	assert.NoError(suite.T(), err)
	
	// Progress through status transitions
	statuses := []models.TicketStatus{
		models.TicketStatusInProgress,
		models.TicketStatusResolved,
		models.TicketStatusClosed,
	}
	
	for _, status := range statuses {
		updateReq := map[string]interface{}{
			"status": status,
		}
		
		body, _ := json.Marshal(updateReq)
		req, _ := http.NewRequest("PUT", fmt.Sprintf("%s/api/v1/tickets/%d", suite.baseURL, ticket.ID), bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+suite.adminToken)
		
		resp, err := suite.client.Do(req)
		assert.NoError(suite.T(), err)
		assert.Equal(suite.T(), http.StatusOK, resp.StatusCode)
	}
	
	// Test that closed tickets cannot be reopened
	updateReq := map[string]interface{}{
		"status": models.TicketStatusOpen,
	}
	
	body, _ := json.Marshal(updateReq)
	req, _ := http.NewRequest("PUT", fmt.Sprintf("%s/api/v1/tickets/%d", suite.baseURL, ticket.ID), bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.adminToken)
	
	resp, err := suite.client.Do(req)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), http.StatusBadRequest, resp.StatusCode)
}

func (suite *TicketIntegrationTestSuite) TestTicketValidation() {
	// Test 1: Missing required fields
	createReq := map[string]interface{}{
		"description": "Missing title",
		"customer_id": suite.customerID,
	}
	
	body, _ := json.Marshal(createReq)
	req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/tickets", suite.baseURL), bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.supportToken)
	
	resp, err := suite.client.Do(req)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), http.StatusBadRequest, resp.StatusCode)
	
	// Test 2: Invalid priority
	createReq = map[string]interface{}{
		"title":       "Invalid Priority",
		"description": "Test",
		"priority":    "invalid",
		"customer_id": suite.customerID,
	}
	
	body, _ = json.Marshal(createReq)
	req, _ = http.NewRequest("POST", fmt.Sprintf("%s/api/v1/tickets", suite.baseURL), bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.supportToken)
	
	resp, err = suite.client.Do(req)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), http.StatusBadRequest, resp.StatusCode)
	
	// Test 3: Non-existent customer
	createReq = map[string]interface{}{
		"title":       "Invalid Customer",
		"description": "Test",
		"customer_id": 99999,
	}
	
	body, _ = json.Marshal(createReq)
	req, _ = http.NewRequest("POST", fmt.Sprintf("%s/api/v1/tickets", suite.baseURL), bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+suite.supportToken)
	
	resp, err = suite.client.Do(req)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), http.StatusNotFound, resp.StatusCode)
}

func TestTicketIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(TicketIntegrationTestSuite))
}