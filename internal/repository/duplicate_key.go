package repository

import (
	"errors"
	"strings"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// isDuplicateKeyError reports whether err is a unique-constraint violation raised
// by the underlying database driver.
//
// Backends are checked in order of preference:
//
//  1. gorm.ErrDuplicatedKey — GORM's portable translation. It is only produced
//     when the connection was opened with gorm.Config{TranslateError: true},
//     which this project does not currently enable on any connection, so the
//     fallbacks below are what actually fire today.
//  2. MySQL 8 (production): error 1062, ER_DUP_ENTRY.
//  3. SQLite (integration/unit tests): "UNIQUE constraint failed: <table>.<col>".
//     Matched on the message rather than by asserting on sqlite3.Error so that
//     production builds do not take a cgo dependency on mattn/go-sqlite3.
//
// Callers must only use this on statements where the sole unique constraint on
// the target table is the one they intend to report on. Both `users` and
// `customers` have exactly one unique index (on `email`), so a hit there
// unambiguously means a duplicate email; `labels` likewise has exactly one (on
// `name`).
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}

	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return true
	}

	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
