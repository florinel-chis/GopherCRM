package models

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #219: ticket_mail_deliveries on SQLite, created by MigrateDatabase, and the
// JSON of a delivery and of a comment that carries one.

func TestTicketNumber(t *testing.T) {
	assert.Equal(t, "T-000042", TicketNumber(42))
	assert.Equal(t, "T-000001", TicketNumber(1))
	assert.Equal(t, "T-000000", TicketNumber(0))
	assert.Equal(t, "T-1234567", TicketNumber(1234567), "ids beyond six digits are not truncated")
}

func TestTicketMailDeliveryJSON(t *testing.T) {
	created := time.Date(2026, 11, 13, 9, 30, 0, 0, time.UTC)
	sent := created.Add(time.Second)
	delivery := TicketMailDelivery{
		ID: 7, CreatedAt: created, UpdatedAt: sent, TicketID: 42, CommentID: 108, CustomerID: 3,
		Status: TicketMailStatusSent, SentAt: &sent,
	}
	raw, err := json.Marshal(delivery)
	require.NoError(t, err)

	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	keys := make([]string, 0, len(decoded))
	for key := range decoded {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	assert.Equal(t, []string{"comment_id", "created_at", "customer_id", "error_code", "id", "sent_at", "status", "ticket_id", "updated_at"}, keys,
		"the delivery carries exactly these fields")
	assert.EqualValues(t, 42, decoded["ticket_id"])
	assert.EqualValues(t, 108, decoded["comment_id"])
	assert.EqualValues(t, 3, decoded["customer_id"])
	assert.Equal(t, "sent", decoded["status"])
	assert.Equal(t, "", decoded["error_code"])
	assert.Equal(t, "2026-11-13T09:30:01Z", decoded["sent_at"])

	raw, err = json.Marshal(TicketMailDelivery{Status: TicketMailStatusSkipped, ErrorCode: TicketMailErrorNoMailer})
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"sent_at":null`, "sent_at is null, not omitted, when nothing was sent")
	assert.Contains(t, string(raw), `"error_code":"no_mailer"`)

	t.Run("comment without a delivery has no delivery key", func(t *testing.T) {
		raw, err := json.Marshal(TicketComment{TicketID: 42, AuthorUserID: 5, Body: "hello"})
		require.NoError(t, err)
		var decoded map[string]interface{}
		require.NoError(t, json.Unmarshal(raw, &decoded))
		_, present := decoded["delivery"]
		assert.False(t, present, "no delivery key on a plain comment")
	})

	t.Run("comment with a delivery", func(t *testing.T) {
		raw, err := json.Marshal(TicketComment{TicketID: 42, AuthorUserID: 5, Body: "hello", Delivery: &delivery})
		require.NoError(t, err)
		var decoded map[string]interface{}
		require.NoError(t, json.Unmarshal(raw, &decoded))
		nested, ok := decoded["delivery"].(map[string]interface{})
		require.True(t, ok, "delivery is an object")
		assert.Equal(t, "sent", nested["status"])
	})
}

func openTicketMailDeliveryDB(t *testing.T) {
	t.Helper()
	origDB := DB
	t.Cleanup(func() {
		require.NoError(t, CloseDatabase())
		DB = origDB
	})
	require.NoError(t, InitDatabase(&config.DatabaseConfig{
		Driver: config.DriverSQLite,
		Path:   filepath.Join(t.TempDir(), "ticket-mail-deliveries.db"),
	}))
	require.NoError(t, MigrateDatabase())
}

func TestTicketMailDeliverySchemaSQLite(t *testing.T) {
	openTicketMailDeliveryDB(t)
	require.True(t, DB.Migrator().HasTable("ticket_mail_deliveries"))

	var columns []sqliteColumn
	require.NoError(t, DB.Raw("SELECT name, type, \"notnull\", dflt_value, pk FROM pragma_table_info('ticket_mail_deliveries')").Scan(&columns).Error)
	byName := map[string]sqliteColumn{}
	names := []string{}
	for _, column := range columns {
		byName[column.Name] = column
		names = append(names, column.Name)
	}
	sort.Strings(names)
	assert.Equal(t, []string{"comment_id", "created_at", "customer_id", "error_code", "id", "sent_at", "status", "ticket_id", "updated_at"}, names,
		"exactly these columns, no deleted_at")
	assert.Equal(t, 1, byName["id"].PK)
	for _, name := range []string{"ticket_id", "comment_id", "customer_id", "status", "error_code"} {
		assert.Equal(t, 1, byName[name].NotNull, "%s is NOT NULL", name)
	}
	assert.Equal(t, 0, byName["sent_at"].NotNull, "sent_at is nullable")
	assert.Equal(t, "varchar(20)", strings.ToLower(byName["status"].Type))
	assert.Equal(t, "varchar(50)", strings.ToLower(byName["error_code"].Type))
	require.NotNil(t, byName["error_code"].Dflt, "error_code has a default")
	assert.Equal(t, "", strings.Trim(*byName["error_code"].Dflt, `'"`), "error_code defaults to ''")

	var fkCount int64
	require.NoError(t, DB.Raw("SELECT COUNT(*) FROM pragma_foreign_key_list('ticket_mail_deliveries')").Scan(&fkCount).Error)
	assert.Zero(t, fkCount, "no foreign key")

	var indexes []sqliteIndex
	require.NoError(t, DB.Raw("SELECT name, \"unique\", origin FROM pragma_index_list('ticket_mail_deliveries')").Scan(&indexes).Error)
	all := map[string]sqliteIndex{}
	unique := []string{}
	for _, index := range indexes {
		all[index.Name] = index
		if index.Unique == 1 && index.Origin != "pk" {
			unique = append(unique, index.Name)
		}
	}
	assert.Equal(t, []string{"idx_ticket_mail_deliveries_comment_id"}, unique, "the comment index is the only unique index")
	assert.Equal(t, []string{"comment_id"}, customerExternalIDIndexColumns(t, "idx_ticket_mail_deliveries_comment_id"))
	for index, column := range map[string]string{
		"idx_ticket_mail_deliveries_ticket_id":   "ticket_id",
		"idx_ticket_mail_deliveries_customer_id": "customer_id",
	} {
		require.Contains(t, all, index)
		assert.Equal(t, 0, all[index].Unique, "%s is not unique", index)
		assert.Equal(t, []string{column}, customerExternalIDIndexColumns(t, index))
	}

	first := &TicketMailDelivery{TicketID: 1, CommentID: 10, CustomerID: 3, Status: TicketMailStatusPending}
	require.NoError(t, DB.Create(first).Error)
	err := DB.Create(&TicketMailDelivery{TicketID: 1, CommentID: 10, CustomerID: 3, Status: TicketMailStatusPending}).Error
	require.Error(t, err, "a second delivery of the same comment is refused")
	assert.Contains(t, err.Error(), "UNIQUE constraint failed")

	schema := schemaSnapshot(t)
	require.NoError(t, MigrateDatabase())
	assert.Equal(t, schema, schemaSnapshot(t), "a second MigrateDatabase changes nothing")
}
