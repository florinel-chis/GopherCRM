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
