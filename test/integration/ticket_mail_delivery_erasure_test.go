package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/handler"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A staff comment emailed to the ticket's customer (#219) leaves a
// ticket_mail_deliveries row recording that mail went to the person. The
// whole-schema sweep runs against a schema that has the table; the person is
// seeded into every scrubbed target, a flagged comment is delivered to them,
// and after DELETE /customers/{id} no delivery row and no copy of the address
// is left in any table.
func TestTicketMailDeliveryErasureLeavesNoPersonalDataInAnyTable(t *testing.T) {
	db := setupFullSchemaDB(t)
	// The comment mail runs a transaction; on an in-memory database a second
	// pooled connection would be a second, empty database.
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)

	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	require.Contains(t, tables, "ticket_mail_deliveries", "the sweep must run against a schema that has the table")

	subject := newErasureSubject("ticket-mail-sweep")
	login, customer, _ := seedCustomerWithLogin(t, db, subject)

	// The other scrubbed targets: an unlinked form submission from the address
	// and an external identity mapping.
	form := &models.Form{Name: "Newsletter", PublicID: "ticket-mail-sweep", Status: models.FormStatusPublished,
		Fields: []models.FormFieldDef{{Name: "email", Label: "Email", Type: models.FormFieldEmail, Required: true}}, CreateLead: false}
	require.NoError(t, db.Create(form).Error)
	submission := &models.FormSubmission{FormID: form.ID, Email: strings.ToLower(subject.Email), Status: models.FormSubmissionReceived,
		Data: map[string]string{"email": strings.ToLower(subject.Email), "phone": subject.Phone}}
	require.NoError(t, db.Create(submission).Error)
	require.NoError(t, repository.NewCustomerExternalIDRepository(db).Create(context.Background(),
		&models.CustomerExternalID{CustomerID: customer.ID, Source: "storefront", ExternalID: subject.ExternalID}))

	// A staff reply, emailed to the customer.
	support := &models.User{Email: "support@ticket-mail-sweep.test", Password: "x", FirstName: "Staff", LastName: "Support",
		Role: models.RoleSupport, IsActive: true}
	require.NoError(t, db.Create(support).Error)
	ticket := &models.Ticket{Title: "Invoice access", Description: "Portal problem", Status: models.TicketStatusOpen,
		Priority: models.TicketPriorityMedium, CustomerID: customer.ID, AssignedToID: &support.ID}
	require.NoError(t, db.Create(ticket).Error)
	mailer := &recordingMailer{}
	ticketService := service.NewTicketService(repository.NewTicketRepository(db), repository.NewCustomerRepository(db),
		repository.NewUserRepository(db),
		service.WithTicketCommentMail(repository.NewTicketMailDeliveryRepository(db), utils.NewTransactionManager(db), mailer))
	comment := &models.TicketComment{TicketID: ticket.ID, AuthorUserID: support.ID, Body: "We have reset your portal access."}
	require.NoError(t, ticketService.CreateCommentWithCustomerEmail(context.Background(), comment))
	require.NotNil(t, comment.Delivery)
	require.Equal(t, models.TicketMailStatusSent, comment.Delivery.Status)
	messages := mailer.messages()
	require.Len(t, messages, 1)
	require.Equal(t, subject.Email, messages[0].To, "the mail went to the person")

	countDeliveries := func() int64 {
		var n int64
		require.NoError(t, db.Raw("SELECT COUNT(*) FROM ticket_mail_deliveries WHERE customer_id = ? OR ticket_id = ?", customer.ID, ticket.ID).Scan(&n).Error)
		return n
	}
	require.EqualValues(t, 1, countDeliveries())

	needles := append(subject.identifiers(), subject.ExternalID)
	before := tablesHolding(t, db, needles)
	for _, table := range []string{"customers", "leads", "users", "ticket_comments", "form_submissions", "customer_external_ids"} {
		require.Contains(t, before, table, "the sweep must see the person in %s before the erasure", table)
	}
	require.NotContains(t, before, "ticket_mail_deliveries", "the delivery row never holds the address")

	// DELETE /customers/{id} as an admin, through the real handler and route.
	customerService := service.NewCustomerService(repository.NewCustomerRepository(db), repository.NewUserRepository(db), repository.NewCompanyRepository(db))
	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api/v1")
	group.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
		c.Set("user_role", string(models.RoleAdmin))
		c.Next()
	})
	handler.SetupCustomerRoutes(group, handler.NewCustomerHandler(customerService))
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/customers/%d", customer.ID), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	assertNoPersonalDataAnywhere(t, db, needles)
	assert.Zero(t, countDeliveries(), "no ticket_mail_deliveries row is left")

	// The business records survive: the ticket and the staff comment.
	var keptTicket models.Ticket
	require.NoError(t, db.First(&keptTicket, ticket.ID).Error)
	var keptComment models.TicketComment
	require.NoError(t, db.First(&keptComment, comment.ID).Error)
	assert.Equal(t, "We have reset your portal access.", keptComment.Body, "a staff-written comment is kept")
	var erasedLogin models.User
	require.NoError(t, db.Unscoped().First(&erasedLogin, login.ID).Error)
	assert.True(t, erasedLogin.DeletedAt.Valid, "the customer's login went with it")
}
