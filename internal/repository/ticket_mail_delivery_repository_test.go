package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Ticket comment mail delivery records (#219) on the full production schema:
// SQLite, and a disposable MySQL schema on TEST_MYSQL_DSN (skipped without it,
// failing when the server is unreachable).

func requireTicketMailMySQL(t *testing.T) {
	t.Helper()
	if os.Getenv("TEST_MYSQL_DSN") == "" {
		t.Skip("TEST_MYSQL_DSN is required for the disposable MySQL regression suite")
	}
}

func TestTicketMailDeliveryRepositorySQLite(t *testing.T) {
	runTicketMailDeliveryRepository(t, openIdentityErasureDB(t, false))
}

func TestTicketMailDeliveryRepositoryMySQL(t *testing.T) {
	requireTicketMailMySQL(t)
	runTicketMailDeliveryRepository(t, openIdentityErasureDB(t, true))
}

func TestTicketDeletePurgesMailDeliveriesSQLite(t *testing.T) {
	runTicketDeletePurgesMailDeliveries(t, openIdentityErasureDB(t, false))
}

func TestTicketDeletePurgesMailDeliveriesMySQL(t *testing.T) {
	requireTicketMailMySQL(t)
	runTicketDeletePurgesMailDeliveries(t, openIdentityErasureDB(t, true))
}

func TestTicketMailDeliveryErasureSQLite(t *testing.T) {
	runTicketMailDeliveryErasure(t, openIdentityErasureDB(t, false))
}

func TestTicketMailDeliveryErasureMySQL(t *testing.T) {
	requireTicketMailMySQL(t)
	runTicketMailDeliveryErasure(t, openIdentityErasureDB(t, true))
}

func countTicketMailDeliveries(t *testing.T, db *gorm.DB, where string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	q := db.Model(&models.TicketMailDelivery{})
	if where != "" {
		q = q.Where(where, args...)
	}
	require.NoError(t, q.Count(&n).Error)
	return n
}

func seedMailTicket(t *testing.T, db *gorm.DB, customerID uint, label string) *models.Ticket {
	t.Helper()
	ticket := &models.Ticket{Title: "Mail ticket " + label, Description: "d", Status: models.TicketStatusOpen,
		Priority: models.TicketPriorityMedium, CustomerID: customerID}
	require.NoError(t, db.Create(ticket).Error)
	return ticket
}

func seedMailComment(t *testing.T, db *gorm.DB, ticketID, authorID uint) *models.TicketComment {
	t.Helper()
	comment := &models.TicketComment{TicketID: ticketID, AuthorUserID: authorID, Body: "Staff reply"}
	require.NoError(t, db.Create(comment).Error)
	return comment
}

// seedMailDelivery writes a stamped delivery of a new staff comment on ticketID
// recorded for customerID.
func seedMailDelivery(t *testing.T, db *gorm.DB, ticketID, customerID, authorID uint) *models.TicketMailDelivery {
	t.Helper()
	comment := seedMailComment(t, db, ticketID, authorID)
	delivery := &models.TicketMailDelivery{TicketID: ticketID, CommentID: comment.ID, CustomerID: customerID, Status: models.TicketMailStatusSent}
	require.NoError(t, NewTicketMailDeliveryRepository(db).Create(context.Background(), delivery))
	return delivery
}

func runTicketMailDeliveryRepository(t *testing.T, db *gorm.DB) {
	ctx := context.Background()
	repo := NewTicketMailDeliveryRepository(db)
	staff := seedIdentityStaff(t, db, nextIdentityTag(), models.RoleSupport)
	customer := seedExternalIDCustomer(t, db, "tmd")
	ticket := seedMailTicket(t, db, customer.ID, "repo")
	first := seedMailComment(t, db, ticket.ID, staff.ID)
	second := seedMailComment(t, db, ticket.ID, staff.ID)

	pending := &models.TicketMailDelivery{TicketID: ticket.ID, CommentID: first.ID, CustomerID: customer.ID, Status: models.TicketMailStatusPending}
	require.NoError(t, repo.Create(ctx, pending))
	assert.NotZero(t, pending.ID)

	t.Run("unique comment", func(t *testing.T) {
		err := repo.Create(ctx, &models.TicketMailDelivery{TicketID: ticket.ID, CommentID: first.ID, CustomerID: customer.ID, Status: models.TicketMailStatusPending})
		require.Error(t, err, "a second row for the same comment is refused by the unique index")
		assert.EqualValues(t, 1, countTicketMailDeliveries(t, db, "comment_id = ?", first.ID))
	})

	t.Run("get by comment", func(t *testing.T) {
		got, err := repo.GetByCommentID(ctx, first.ID)
		require.NoError(t, err)
		assert.Equal(t, pending.ID, got.ID)
		assert.Equal(t, models.TicketMailStatusPending, got.Status)
		assert.Equal(t, "", got.ErrorCode)
		assert.Nil(t, got.SentAt)
		_, err = repo.GetByCommentID(ctx, 99999999)
		assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	})

	t.Run("finish pending once", func(t *testing.T) {
		now := time.Now()
		sentAt := now.UTC()
		require.NoError(t, repo.FinishPending(ctx, pending.ID, TicketMailDeliveryOutcome{Status: models.TicketMailStatusSent, SentAt: &sentAt, Now: now}))
		got, err := repo.GetByCommentID(ctx, first.ID)
		require.NoError(t, err)
		assert.Equal(t, models.TicketMailStatusSent, got.Status)
		require.NotNil(t, got.SentAt)
		assert.Equal(t, "", got.ErrorCode)

		err = repo.FinishPending(ctx, pending.ID, TicketMailDeliveryOutcome{Status: models.TicketMailStatusFailed, ErrorCode: models.TicketMailErrorMailFailed, Now: time.Now()})
		assert.ErrorIs(t, err, ErrTicketMailDeliveryNotPending, "a second stamp matches no pending row")
		assert.False(t, errors.Is(err, gorm.ErrRecordNotFound), "the sentinel is its own")
		again, err := repo.GetByCommentID(ctx, first.ID)
		require.NoError(t, err)
		assert.Equal(t, models.TicketMailStatusSent, again.Status, "the first stamp is never overwritten")
		assert.Equal(t, "", again.ErrorCode)

		assert.ErrorIs(t, repo.FinishPending(ctx, 99999999, TicketMailDeliveryOutcome{Status: models.TicketMailStatusSent, Now: time.Now()}),
			ErrTicketMailDeliveryNotPending, "an unknown row")
		err = repo.FinishPending(ctx, pending.ID, TicketMailDeliveryOutcome{Status: models.TicketMailStatusPending, Now: time.Now()})
		require.Error(t, err, "pending is not an outcome")
		assert.False(t, errors.Is(err, ErrTicketMailDeliveryNotPending))
	})

	t.Run("withtx", func(t *testing.T) {
		rollback := errors.New("caller rolls back")
		err := db.Transaction(func(tx *gorm.DB) error {
			row := &models.TicketMailDelivery{TicketID: ticket.ID, CommentID: second.ID, CustomerID: customer.ID, Status: models.TicketMailStatusPending}
			if err := repo.WithTx(tx).Create(ctx, row); err != nil {
				return err
			}
			if err := repo.WithTx(tx).FinishPending(ctx, row.ID, TicketMailDeliveryOutcome{Status: models.TicketMailStatusSkipped,
				ErrorCode: models.TicketMailErrorNoMailer, Now: time.Now()}); err != nil {
				return err
			}
			return rollback
		})
		require.ErrorIs(t, err, rollback)
		assert.Zero(t, countTicketMailDeliveries(t, db, "comment_id = ?", second.ID), "a rolled-back caller keeps nothing")

		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return repo.WithTx(tx).Create(ctx, &models.TicketMailDelivery{TicketID: ticket.ID, CommentID: second.ID, CustomerID: customer.ID, Status: models.TicketMailStatusPending})
		}))
		got, err := repo.GetByCommentID(ctx, second.ID)
		require.NoError(t, err)
		require.NoError(t, repo.FinishPending(ctx, got.ID, TicketMailDeliveryOutcome{Status: models.TicketMailStatusFailed,
			ErrorCode: models.TicketMailErrorTimeout, Now: time.Now()}))
		got, err = repo.GetByCommentID(ctx, second.ID)
		require.NoError(t, err)
		assert.Equal(t, models.TicketMailStatusFailed, got.Status)
		assert.Equal(t, models.TicketMailErrorTimeout, got.ErrorCode)
		assert.Nil(t, got.SentAt, "sent_at stays null unless sent")
	})
}

func runTicketDeletePurgesMailDeliveries(t *testing.T, db *gorm.DB) {
	staff := seedIdentityStaff(t, db, nextIdentityTag(), models.RoleSupport)
	customer := seedExternalIDCustomer(t, db, "tdel")
	repo := NewTicketRepository(db)

	t.Run("purges only that ticket", func(t *testing.T) {
		target := seedMailTicket(t, db, customer.ID, "target")
		other := seedMailTicket(t, db, customer.ID, "other")
		seedMailDelivery(t, db, target.ID, customer.ID, staff.ID)
		seedMailDelivery(t, db, target.ID, customer.ID, staff.ID)
		seedMailDelivery(t, db, other.ID, customer.ID, staff.ID)
		var commentsBefore int64
		require.NoError(t, db.Model(&models.TicketComment{}).Where("ticket_id = ?", target.ID).Count(&commentsBefore).Error)
		require.EqualValues(t, 2, commentsBefore)

		require.NoError(t, repo.Delete(target.ID))

		var raw int64
		require.NoError(t, db.Raw("SELECT COUNT(*) FROM ticket_mail_deliveries WHERE ticket_id = ?", target.ID).Scan(&raw).Error)
		assert.Zero(t, raw, "the ticket's deliveries are hard-deleted")
		assert.EqualValues(t, 1, countTicketMailDeliveries(t, db, "ticket_id = ?", other.ID), "other tickets keep theirs")
		var deleted models.Ticket
		require.NoError(t, db.Unscoped().First(&deleted, target.ID).Error)
		assert.True(t, deleted.DeletedAt.Valid, "the ticket is soft-deleted")
		var commentsAfter int64
		require.NoError(t, db.Model(&models.TicketComment{}).Where("ticket_id = ?", target.ID).Count(&commentsAfter).Error)
		assert.Equal(t, commentsBefore, commentsAfter, "the comments stay as before")
	})

	t.Run("soft delete failure keeps the deliveries", func(t *testing.T) {
		ticket := seedMailTicket(t, db, customer.ID, "fail")
		seedMailDelivery(t, db, ticket.ID, customer.ID, staff.ID)
		sentinel := errors.New("injected failure soft-deleting the ticket")
		name := "ticket_mail_delivery_test:fail_ticket_soft_delete"
		calls := failOn(t, db, name, "tickets", sentinel)

		err := repo.Delete(ticket.ID)
		require.ErrorIs(t, err, sentinel)
		require.Positive(t, calls.Load(), "the failure was injected")
		require.NoError(t, db.Callback().Delete().Remove(name))
		assert.EqualValues(t, 1, countTicketMailDeliveries(t, db, "ticket_id = ?", ticket.ID), "the purge rolled back with the soft delete")
		var live models.Ticket
		require.NoError(t, db.First(&live, ticket.ID).Error, "the ticket is still live")
	})

	t.Run("withtx caller rollback keeps the deliveries", func(t *testing.T) {
		ticket := seedMailTicket(t, db, customer.ID, "rollback")
		seedMailDelivery(t, db, ticket.ID, customer.ID, staff.ID)
		rollback := errors.New("caller rolls back")
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := repo.WithTx(tx).Delete(ticket.ID); err != nil {
				return err
			}
			var inside int64
			if err := tx.Model(&models.TicketMailDelivery{}).Where("ticket_id = ?", ticket.ID).Count(&inside).Error; err != nil {
				return err
			}
			if inside != 0 {
				return fmt.Errorf("the delete inside the transaction left %d deliveries", inside)
			}
			return rollback
		})
		require.ErrorIs(t, err, rollback)
		assert.EqualValues(t, 1, countTicketMailDeliveries(t, db, "ticket_id = ?", ticket.ID))
		var live models.Ticket
		require.NoError(t, db.First(&live, ticket.ID).Error)
	})
}

// ticketMailPerson is one person stored as a customer, the lead converted into
// it and its customer-role login, with a live and a soft-deleted ticket and
// four delivery records reachable through the customer id or its tickets.
type ticketMailPerson struct {
	customer   *models.Customer
	lead       *models.Lead
	login      *models.User
	deliveries []uint
}

func seedTicketMailPerson(t *testing.T, db *gorm.DB, owner, staff *models.User) *ticketMailPerson {
	t.Helper()
	tag := nextIdentityTag()
	p := &ticketMailPerson{}
	p.login = &models.User{Email: "tmd-login-" + tag + "@person.test", Password: "$2a$10$x", FirstName: "Tmd" + tag,
		LastName: "Login", Role: models.RoleCustomer, IsActive: true}
	require.NoError(t, db.Create(p.login).Error)
	p.customer = &models.Customer{FirstName: "Tmd" + tag, LastName: "Person", Email: "tmd-customer-" + tag + "@person.test", UserID: &p.login.ID}
	require.NoError(t, db.Create(p.customer).Error)
	p.lead = &models.Lead{FirstName: "Tmd" + tag, LastName: "Person", Email: "tmd-lead-" + tag + "@person.test",
		OwnerID: owner.ID, Status: models.LeadStatusConverted, CustomerID: &p.customer.ID}
	require.NoError(t, db.Create(p.lead).Error)

	live := seedMailTicket(t, db, p.customer.ID, "live-"+tag)
	gone := seedMailTicket(t, db, p.customer.ID, "gone-"+tag)
	unrelated := seedMailTicket(t, db, seedExternalIDCustomer(t, db, "tmd-unrelated").ID, "unrelated-"+tag)
	for _, d := range []*models.TicketMailDelivery{
		seedMailDelivery(t, db, live.ID, p.customer.ID, staff.ID),
		seedMailDelivery(t, db, gone.ID, p.customer.ID, staff.ID),
		// Recorded for another customer id but on the person's ticket.
		seedMailDelivery(t, db, live.ID, 0, staff.ID),
		// Recorded for the person on a ticket that is not theirs any more.
		seedMailDelivery(t, db, unrelated.ID, p.customer.ID, staff.ID),
	} {
		p.deliveries = append(p.deliveries, d.ID)
	}
	// A plain soft delete, not TicketRepository.Delete: the ticket is gone but
	// its delivery stays, so the erasure has to find it through the ticket.
	require.NoError(t, db.Delete(&models.Ticket{}, gone.ID).Error, "a soft-deleted ticket of the customer")
	require.EqualValues(t, 4, countTicketMailDeliveries(t, db, "id IN ?", p.deliveries))
	return p
}

func runTicketMailDeliveryErasure(t *testing.T, db *gorm.DB) {
	owner := seedIdentityStaff(t, db, nextIdentityTag(), models.RoleSales)
	staff := seedIdentityStaff(t, db, nextIdentityTag(), models.RoleSupport)

	roots := []struct {
		name  string
		erase func(p *ticketMailPerson) error
	}{
		{"customer", func(p *ticketMailPerson) error { return NewCustomerRepository(db).Delete(p.customer.ID) }},
		{"converted_lead", func(p *ticketMailPerson) error { return NewLeadRepository(db).Delete(p.lead.ID) }},
		{"customer_login", func(p *ticketMailPerson) error { return NewUserRepository(db).Delete(p.login.ID) }},
	}
	for _, root := range roots {
		root := root
		t.Run(root.name, func(t *testing.T) {
			person := seedTicketMailPerson(t, db, owner, staff)
			bystander := seedTicketMailPerson(t, db, owner, staff)

			require.NoError(t, root.erase(person))

			var raw int64
			require.NoError(t, db.Raw("SELECT COUNT(*) FROM ticket_mail_deliveries WHERE id IN ?", person.deliveries).Scan(&raw).Error)
			assert.Zero(t, raw, "every delivery of the customer and of its tickets is hard-deleted")
			assert.Zero(t, countTicketMailDeliveries(t, db, "customer_id = ?", person.customer.ID))
			assert.EqualValues(t, 4, countTicketMailDeliveries(t, db, "id IN ?", bystander.deliveries), "other customers keep theirs")
			var erased models.Customer
			require.NoError(t, db.Unscoped().First(&erased, person.customer.ID).Error)
			assert.True(t, erased.DeletedAt.Valid, "the customer is erased")
		})
	}

	t.Run("failure_after_the_purge_keeps_every_delivery", func(t *testing.T) {
		person := seedTicketMailPerson(t, db, owner, staff)
		before := snapshotIdentityDB(t, db)
		sentinel := errors.New("injected failure soft-deleting the customer")
		name := "ticket_mail_delivery_erasure_test:fail_customer_soft_delete"
		calls := failOn(t, db, name, "customers", sentinel)

		err := NewCustomerRepository(db).Delete(person.customer.ID)
		require.ErrorIs(t, err, sentinel)
		require.Positive(t, calls.Load())
		require.NoError(t, db.Callback().Delete().Remove(name))
		assert.Equal(t, before, snapshotIdentityDB(t, db), "a failed erasure leaves every row in place")
		assert.EqualValues(t, 4, countTicketMailDeliveries(t, db, "id IN ?", person.deliveries))
	})

	t.Run("delivery_delete_failure_rolls_back_and_names_table_and_id_only", func(t *testing.T) {
		person := seedTicketMailPerson(t, db, owner, staff)
		before := snapshotIdentityDB(t, db)
		sentinel := errors.New("injected failure deleting deliveries")
		name := "ticket_mail_delivery_erasure_test:fail_delivery_delete"
		calls := failOn(t, db, name, "ticket_mail_deliveries", sentinel)

		err := NewCustomerRepository(db).Delete(person.customer.ID)
		require.ErrorIs(t, err, sentinel)
		require.EqualValues(t, 1, calls.Load(), "the failure was injected into the delivery purge")
		require.NoError(t, db.Callback().Delete().Remove(name))
		message := err.Error()
		assert.Contains(t, message, "ticket_mail_deliveries")
		assert.Contains(t, message, fmt.Sprintf("customer %d", person.customer.ID))
		assert.NotContains(t, message, person.customer.Email)
		assert.False(t, strings.Contains(message, person.customer.FirstName))
		assert.Equal(t, before, snapshotIdentityDB(t, db), "the erasure rolled back")
	})
}

// TestTicketMailDeliveryErasureWithoutTable erases on a schema that has no
// ticket_mail_deliveries table: the migrator probe skips the purge.
func TestTicketMailDeliveryErasureWithoutTable(t *testing.T) {
	db := openBusinessTextSubsetDB(t, &models.Ticket{}, &models.TicketComment{})
	require.False(t, db.Migrator().HasTable(&models.TicketMailDelivery{}), "the fixture must not have the table")
	owner := seedIdentityStaff(t, db, nextIdentityTag(), models.RoleSales)

	erased := func(t *testing.T, customerID uint) {
		t.Helper()
		var c models.Customer
		require.NoError(t, db.Unscoped().First(&c, customerID).Error)
		assert.True(t, c.DeletedAt.Valid)
		assert.True(t, strings.HasSuffix(c.Email, "@"+anonymizedEmailDomain))
	}
	newPerson := func(label string) (*models.User, *models.Customer, *models.Lead) {
		tag := nextIdentityTag()
		login := &models.User{Email: "tmd-nt-login-" + label + tag + "@person.test", Password: "x", FirstName: "Nt", LastName: "Login",
			Role: models.RoleCustomer, IsActive: true}
		require.NoError(t, db.Create(login).Error)
		customer := &models.Customer{FirstName: "Nt" + label, LastName: "Person", Email: "tmd-nt-" + label + tag + "@person.test", UserID: &login.ID}
		require.NoError(t, db.Create(customer).Error)
		lead := &models.Lead{FirstName: "Nt" + label, LastName: "Lead", Email: "tmd-nt-lead-" + label + tag + "@person.test",
			OwnerID: owner.ID, Status: models.LeadStatusConverted, CustomerID: &customer.ID}
		require.NoError(t, db.Create(lead).Error)
		seedMailTicket(t, db, customer.ID, label+tag)
		return login, customer, lead
	}

	_, c1, _ := newPerson("a")
	require.NoError(t, NewCustomerRepository(db).Delete(c1.ID))
	erased(t, c1.ID)

	_, c2, l2 := newPerson("b")
	require.NoError(t, NewLeadRepository(db).Delete(l2.ID))
	erased(t, c2.ID)

	u3, c3, _ := newPerson("c")
	require.NoError(t, NewUserRepository(db).Delete(u3.ID))
	erased(t, c3.ID)

	// The ticket delete skips the purge on such a schema too.
	ticket := seedMailTicket(t, db, c3.ID, "delete")
	require.NoError(t, NewTicketRepository(db).Delete(ticket.ID))
}
