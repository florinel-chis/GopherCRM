package repository

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LockingRead returns a handle on tx whose reads lock the rows they return:
// SELECT ... FOR UPDATE on MySQL/MariaDB, while the SQLite dialector drops the
// clause (its single pooled connection serialises writers anyway). The fresh
// Session keeps the clause off every later statement on the same handle, so
// bind only reads to it and run the writes on tx itself.
func LockingRead(tx *gorm.DB) *gorm.DB {
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).Session(&gorm.Session{})
}

// SharedLockingRead returns a handle on tx whose reads take a shared lock on
// the rows they return: SELECT ... FOR SHARE on MySQL 8 and LOCK IN SHARE MODE
// on MariaDB (the gorm mysql driver picks the form from the server version;
// MariaDB rejects FOR SHARE), while the SQLite dialector drops the clause. A
// shared read is current under REPEATABLE READ like LockingRead, waits for a
// writer that holds the rows, and does not wait for other shared readers. As
// with LockingRead, the fresh Session keeps the clause off later statements.
func SharedLockingRead(tx *gorm.DB) *gorm.DB {
	return tx.Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).Session(&gorm.Session{})
}
