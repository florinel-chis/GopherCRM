package database

import (
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"
)

// Migrate runs migrate against db. On MySQL and MariaDB that is a plain call.
// On SQLite it is wrapped in the procedure SQLite itself documents for schema
// changes that rebuild a table (https://www.sqlite.org/lang_altertable.html,
// "Making Other Kinds Of Table Schema Changes"): switch foreign-key
// enforcement off, run the change, verify the result with
// PRAGMA foreign_key_check, switch enforcement back on.
//
// Why: the SQLite driver adds a constraint to an existing table by rebuilding
// it — CREATE `<table>__temp`, INSERT … SELECT, DROP TABLE `<table>`, RENAME —
// inside one transaction. With foreign_keys(1) on, as sqliteDSN sets it for
// the application, the DROP TABLE performs an implicit DELETE of every row,
// and rows in other tables that reference them reject it with "FOREIGN KEY
// constraint failed". A v1.2.0 file with one ticket, one task on a customer or
// one converted lead could therefore not be upgraded. The pragma is per
// connection and a no-op inside a transaction, so it is flipped here, on one
// pinned connection, outside the driver's transaction, and put back before the
// connection returns to the pool. A check that finds violations fails the
// migration: startup must stop rather than serve a file with dangling
// references.
func Migrate(db *gorm.DB, migrate func(tx *gorm.DB) error) error {
	if db.Dialector.Name() != "sqlite" {
		return migrate(db)
	}

	return db.Connection(func(conn *gorm.DB) (err error) {
		// A fresh session keeps every statement below on the pinned
		// connection while giving each one a clean statement.
		conn = conn.Session(&gorm.Session{NewDB: true})

		var enforced int
		if err := conn.Raw("PRAGMA foreign_keys").Scan(&enforced).Error; err != nil {
			return fmt.Errorf("failed to read foreign_keys pragma: %w", err)
		}
		if err := conn.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
			return fmt.Errorf("failed to disable foreign keys for migration: %w", err)
		}
		defer func() {
			restoreErr := conn.Exec(fmt.Sprintf("PRAGMA foreign_keys = %d", enforced)).Error
			if err == nil && restoreErr != nil {
				err = fmt.Errorf("failed to restore foreign_keys pragma: %w", restoreErr)
			}
		}()

		if err := migrate(conn); err != nil {
			return err
		}

		violations, err := foreignKeyViolations(conn)
		if err != nil {
			return err
		}
		if len(violations) > 0 {
			return fmt.Errorf("foreign key check after migration failed: dangling references in %s",
				strings.Join(violations, ", "))
		}
		return nil
	})
}

// foreignKeyViolations runs PRAGMA foreign_key_check and returns one
// "child -> parent" entry per referencing table and parent, sorted, so the
// error names what is broken without listing every row.
func foreignKeyViolations(conn *gorm.DB) ([]string, error) {
	var rows []struct {
		Table  string `gorm:"column:table"`
		RowID  int64  `gorm:"column:rowid"`
		Parent string `gorm:"column:parent"`
		FKID   int    `gorm:"column:fkid"`
	}
	if err := conn.Raw("PRAGMA foreign_key_check").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to run foreign_key_check: %w", err)
	}

	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		seen[row.Table+" -> "+row.Parent] = struct{}{}
	}
	pairs := make([]string, 0, len(seen))
	for pair := range seen {
		pairs = append(pairs, pair)
	}
	sort.Strings(pairs)
	return pairs, nil
}
