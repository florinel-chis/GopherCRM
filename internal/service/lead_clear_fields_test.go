package service

import (
	"testing"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLeadServiceUpdateStoresSentEmptyText pins the map contract the lead
// update handler relies on since #398: a string key is stored whatever its
// value, the empty string included, and a key that is not in the map leaves
// the stored value alone. Asserted on the committed row.
func TestLeadServiceUpdateStoresSentEmptyText(t *testing.T) {
	env := newLeadConversionEnv(t, openQuoteTestSQLite(t))

	seed := func(t *testing.T) *models.Lead {
		t.Helper()
		lead := env.newLead(models.LeadStatusNew, nil)
		lead.Phone = "+44 20 7946 0001"
		lead.Company = "Analytical Engines Ltd"
		lead.Position = "CEO"
		lead.Source = "referral"
		lead.ExternalID = "crm-legacy-1042"
		lead.Notes = "Demo booked for Tuesday."
		require.NoError(t, env.db.Save(lead).Error)
		return lead
	}

	t.Run("six empty strings are stored", func(t *testing.T) {
		lead := seed(t)

		updated, err := env.svc.Update(lead.ID, map[string]interface{}{
			"phone": "", "company": "", "position": "", "source": "", "external_id": "", "notes": "",
		})
		require.NoError(t, err)

		stored := env.committedLead(lead.ID)
		for _, got := range []*models.Lead{updated, &stored} {
			assert.Empty(t, got.Phone)
			assert.Empty(t, got.Company)
			assert.Empty(t, got.Position)
			assert.Empty(t, got.Source)
			assert.Empty(t, got.ExternalID)
			assert.Empty(t, got.Notes)
			assert.Equal(t, lead.FirstName, got.FirstName)
			assert.Equal(t, lead.LastName, got.LastName)
			assert.Equal(t, lead.Email, got.Email)
		}
	})

	t.Run("an empty map changes nothing", func(t *testing.T) {
		lead := seed(t)

		_, err := env.svc.Update(lead.ID, map[string]interface{}{})
		require.NoError(t, err)

		stored := env.committedLead(lead.ID)
		assert.Equal(t, lead.Phone, stored.Phone)
		assert.Equal(t, lead.Company, stored.Company)
		assert.Equal(t, lead.Position, stored.Position)
		assert.Equal(t, lead.Source, stored.Source)
		assert.Equal(t, lead.ExternalID, stored.ExternalID)
		assert.Equal(t, lead.Notes, stored.Notes)
	})
}
