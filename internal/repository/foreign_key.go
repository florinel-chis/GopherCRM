package repository

import (
	"errors"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// isForeignKeyRefusal reports whether err is the refusal to delete or update a
// parent row that another row still references (#344).
//
//   - MySQL and MariaDB: error 1451, ER_ROW_IS_REFERENCED_2. A driver error
//     with any other number (1452, a child row naming a missing parent; 1062,
//     a duplicate key) is not one.
//   - SQLite with foreign_keys(1): "FOREIGN KEY constraint failed", matched on
//     the message so the check does not depend on which SQLite driver is
//     linked. SQLite uses the same text for a child row naming a missing
//     parent; callers only use this on a parent delete.
//
// TranslateError is not enabled on any connection, so gorm's
// ErrForeignKeyViolated never appears here (see duplicate_key.go).
func isForeignKeyRefusal(err error) bool {
	if err == nil {
		return false
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1451
	}
	return strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}
