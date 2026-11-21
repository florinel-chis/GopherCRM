package service

import (
	"context"
	"errors"
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Customer create and update run their transaction on the caller's context:
// a cancelled request writes nothing.
func TestCustomerServiceUsesTheCallersContext(t *testing.T) {
	env := newWebhookEnv(t, openTicketIntakeDB(t, false), nil)
	env.wire(env.repo)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("Create", func(t *testing.T) {
		before := env.count(&models.Customer{}, "")
		customer := env.newCustomer()
		err := env.customers.Create(cancelled, customer)
		assert.True(t, errors.Is(err, context.Canceled), "got %v", err)
		assert.Zero(t, customer.ID)
		assert.Equal(t, before, env.count(&models.Customer{}, ""), "no customer is written")
	})

	t.Run("Update", func(t *testing.T) {
		stored := env.seedCustomer()
		changed := *stored
		changed.FirstName = "Changed"
		changed.Notes = "changed notes"
		err := env.customers.Update(cancelled, &changed)
		assert.True(t, errors.Is(err, context.Canceled), "got %v", err)

		var after models.Customer
		require.NoError(t, env.db.First(&after, stored.ID).Error)
		assert.Equal(t, stored.FirstName, after.FirstName, "no column changes")
		assert.Equal(t, stored.Notes, after.Notes, "no column changes")
		assert.Equal(t, stored.UpdatedAt.UTC(), after.UpdatedAt.UTC(), "the row is not touched")
	})
}
