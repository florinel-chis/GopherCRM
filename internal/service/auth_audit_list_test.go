package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAuthAuditListSQLite(t *testing.T) {
	db, _ := openOIDCTestSQLite(t)
	runAuthAuditList(t, db)
}

func TestAuthAuditListMySQL(t *testing.T) {
	runAuthAuditList(t, openMigratedTestMySQL(t, 3))
}

// runAuthAuditList seeds rows with explicit UTC created_at values, inserted in
// an order where id order and time order differ, and checks the order, every
// filter, the range boundaries and the pagination.
func runAuthAuditList(t *testing.T, db *gorm.DB) {
	base := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	uid := func(v uint) *uint { return &v }
	type seed struct {
		name    string
		at      time.Time
		method  string
		outcome string
		reason  string
		userID  *uint
	}
	// Insertion order sets the ids: "old" gets the highest id but the oldest
	// time; "tieA" and "tieB" share one second.
	seeds := []seed{
		{"newest", base.Add(3 * time.Second), AuthAuditMethodPassword, AuthAuditOutcomeSuccess, "", uid(7)},
		{"tieA", base.Add(2 * time.Second), AuthAuditMethodOIDC, AuthAuditOutcomeRefused, "no_mapping_match", nil},
		{"tieB", base.Add(2 * time.Second), AuthAuditMethodRefresh, AuthAuditOutcomeRefused, AuthAuditReasonTokenInvalid, uid(7)},
		{"middle", base.Add(1 * time.Second), AuthAuditMethodAPIKey, AuthAuditOutcomeRefused, AuthAuditReasonKeyInvalid, uid(9)},
		{"old", base, AuthAuditMethodPassword, AuthAuditOutcomeRefused, AuthAuditReasonInvalidCredentials, nil},
	}
	ids := map[string]uint{}
	for _, s := range seeds {
		row := &models.AuthAuditLog{CreatedAt: s.at, Method: s.method, Outcome: s.outcome, Reason: s.reason, UserID: s.userID}
		require.NoError(t, db.Create(row).Error)
		ids[s.name] = row.ID
	}
	require.Greater(t, ids["old"], ids["newest"], "the oldest row must have the highest id")
	require.Greater(t, ids["tieB"], ids["tieA"])

	svc := NewAuthAuditLogService(repository.NewAuthAuditRepository(db))
	list := func(t *testing.T, filter repository.AuthAuditFilter, offset, limit int) ([]string, int64) {
		t.Helper()
		rows, total, err := svc.ListSignIns(context.Background(), filter, offset, limit)
		require.NoError(t, err)
		byID := map[uint]string{}
		for name, id := range ids {
			byID[id] = name
		}
		names := make([]string, 0, len(rows))
		for _, row := range rows {
			names = append(names, byID[row.ID])
		}
		return names, total
	}
	at := func(d time.Duration) *time.Time { v := base.Add(d); return &v }

	t.Run("no filter: created_at desc, then id desc", func(t *testing.T) {
		names, total := list(t, repository.AuthAuditFilter{}, 0, 100)
		assert.Equal(t, []string{"newest", "tieB", "tieA", "middle", "old"}, names)
		assert.EqualValues(t, 5, total)
	})

	cases := []struct {
		name   string
		filter repository.AuthAuditFilter
		want   []string
	}{
		{"method", repository.AuthAuditFilter{Method: AuthAuditMethodPassword}, []string{"newest", "old"}},
		{"outcome", repository.AuthAuditFilter{Outcome: AuthAuditOutcomeSuccess}, []string{"newest"}},
		{"reason", repository.AuthAuditFilter{Reason: "no_mapping_match"}, []string{"tieA"}},
		{"user_id", repository.AuthAuditFilter{UserID: uid(7)}, []string{"newest", "tieB"}},
		{"from inclusive", repository.AuthAuditFilter{From: at(2 * time.Second)}, []string{"newest", "tieB", "tieA"}},
		{"to exclusive", repository.AuthAuditFilter{To: at(2 * time.Second)}, []string{"middle", "old"}},
		{"from inclusive at the oldest", repository.AuthAuditFilter{From: at(0)}, []string{"newest", "tieB", "tieA", "middle", "old"}},
		{"to exclusive at the newest", repository.AuthAuditFilter{To: at(3 * time.Second)}, []string{"tieB", "tieA", "middle", "old"}},
		{"range", repository.AuthAuditFilter{From: at(time.Second), To: at(3 * time.Second)}, []string{"tieB", "tieA", "middle"}},
		{"combined", repository.AuthAuditFilter{Outcome: AuthAuditOutcomeRefused, UserID: uid(7)}, []string{"tieB"}},
		{"no match", repository.AuthAuditFilter{Method: AuthAuditMethodOIDC, Outcome: AuthAuditOutcomeSuccess}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			names, total := list(t, tc.filter, 0, 100)
			assert.Equal(t, tc.want, names)
			assert.EqualValues(t, len(tc.want), total)
		})
	}

	t.Run("pages share the total", func(t *testing.T) {
		var all []string
		for offset := 0; offset < 5; offset += 2 {
			names, total := list(t, repository.AuthAuditFilter{}, offset, 2)
			assert.EqualValues(t, 5, total)
			all = append(all, names...)
		}
		assert.Equal(t, []string{"newest", "tieB", "tieA", "middle", "old"}, all)
		names, total := list(t, repository.AuthAuditFilter{Outcome: AuthAuditOutcomeRefused}, 2, 2)
		assert.Equal(t, []string{"middle", "old"}, names)
		assert.EqualValues(t, 4, total)
	})
}

func TestAuthAuditReasonVocabulary(t *testing.T) {
	reasons := AuthAuditReasons()
	assert.Len(t, reasons, 44)
	assert.True(t, sort.StringsAreSorted(reasons))
	for _, reason := range reasons {
		assert.True(t, IsAuthAuditReason(reason), reason)
	}
	assert.False(t, IsAuthAuditReason("NO_MAPPING_MATCH"))
	assert.False(t, IsAuthAuditReason("no_mapping_match "))
	assert.False(t, IsAuthAuditReason(""))
	assert.False(t, IsAuthAuditMethod("password "))
	assert.False(t, IsAuthAuditMethod("PASSWORD"))
	assert.False(t, IsAuthAuditOutcome("Refused"))
	assert.Equal(t, []string{"api_key", "api_key_create", "api_key_enable", "api_key_revoke", "logout", "oidc", "password", "password_change", "refresh", "reset_confirm", "reset_request", "scim"}, AuthAuditMethods())
	assert.Equal(t, []string{"refused", "success"}, AuthAuditOutcomes())

	// The swagger enums of GET /audit/sign-ins (what the SPA's selects offer)
	// equal the Go sets.
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "swagger.json"))
	require.NoError(t, err)
	var spec struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name string `json:"name"`
				In   string `json:"in"`
				Enum []any  `json:"enum"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(raw, &spec))
	op, ok := spec.Paths["/audit/sign-ins"]["get"]
	require.True(t, ok, "api/swagger.json has no GET /audit/sign-ins")
	enums := map[string][]string{}
	for _, p := range op.Parameters {
		if p.In == "query" {
			values := []string{}
			for _, v := range p.Enum {
				values = append(values, fmt.Sprint(v))
			}
			sort.Strings(values)
			enums[p.Name] = values
		}
	}
	assert.Equal(t, AuthAuditMethods(), enums["method"])
	assert.Equal(t, AuthAuditOutcomes(), enums["outcome"])
	assert.Equal(t, reasons, enums["reason"])
}
