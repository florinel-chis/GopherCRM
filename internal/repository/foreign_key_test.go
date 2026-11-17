package repository

import (
	"errors"
	"fmt"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
)

type foreignKeyCase struct {
	name string
	err  error
}

// #344: isForeignKeyRefusal recognises the refusal to delete a referenced
// parent row (MySQL 1451, SQLite's FOREIGN KEY constraint failed) and nothing
// else.
func TestForeignKeyRefusalClassifier(t *testing.T) {
	mysql1451 := &mysqldriver.MySQLError{Number: 1451, Message: "Cannot delete or update a parent row: a foreign key constraint fails"}
	mysql1452 := &mysqldriver.MySQLError{Number: 1452, Message: "Cannot add or update a child row: a foreign key constraint fails"}
	sqliteRefusal := errors.New("constraint failed: FOREIGN KEY constraint failed (787)")

	for _, tc := range []foreignKeyCase{
		{"mysql 1451", mysql1451},
		{"wrapped mysql 1451", fmt.Errorf("delete role 7: %w", mysql1451)},
		{"sqlite", sqliteRefusal},
		{"wrapped sqlite", fmt.Errorf("delete role 7: %w", sqliteRefusal)},
	} {
		assert.True(t, isForeignKeyRefusal(tc.err), tc.name)
	}

	for _, tc := range []foreignKeyCase{
		{"nil", nil},
		{"mysql 1062", &mysqldriver.MySQLError{Number: 1062, Message: "Duplicate entry for key 'roles.idx_roles_name_key'"}},
		{"mysql 1452", mysql1452},
		{"wrapped mysql 1452", fmt.Errorf("insert: %w", mysql1452)},
		{"sqlite unique", errors.New("constraint failed: UNIQUE constraint failed: roles.name_key (2067)")},
		{"unrelated", errors.New("connection refused")},
		{"1451 text without the driver error", errors.New("Cannot delete or update a parent row")},
	} {
		assert.False(t, isForeignKeyRefusal(tc.err), tc.name)
	}
}
