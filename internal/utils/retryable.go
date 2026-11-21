package utils

import (
	"errors"

	gosqlite "github.com/glebarez/go-sqlite"
	"github.com/go-sql-driver/mysql"
)

// RetryReason names why a database error is worth replaying as a fresh
// transaction. RetryNone means it is not.
type RetryReason uint8

const (
	// RetryNone: the error is not transient contention.
	RetryNone RetryReason = iota
	// RetryDeadlock: MySQL/MariaDB error 1213 (ER_LOCK_DEADLOCK).
	RetryDeadlock
	// RetryLockWaitTimeout: MySQL/MariaDB error 1205 (ER_LOCK_WAIT_TIMEOUT).
	RetryLockWaitTimeout
	// RetryRecordChanged: MariaDB error 1020 (ER_CHECKREAD, "Record has
	// changed since last read"), raised under innodb_snapshot_isolation.
	RetryRecordChanged
	// RetrySQLiteBusy: SQLite primary result code SQLITE_BUSY (5) or
	// SQLITE_LOCKED (6), extended codes included.
	RetrySQLiteBusy
)

// MySQL/MariaDB error numbers classified as retryable.
const (
	mysqlNumCheckRead       uint16 = 1020 // ER_CHECKREAD
	mysqlNumLockWaitTimeout uint16 = 1205 // ER_LOCK_WAIT_TIMEOUT
	mysqlNumDeadlock        uint16 = 1213 // ER_LOCK_DEADLOCK
)

// SQLite primary result codes classified as retryable.
const (
	sqliteBusy   = 5 // SQLITE_BUSY
	sqliteLocked = 6 // SQLITE_LOCKED
)

// RetryReasonOf classifies err by the driver's typed error only; the message
// text is never read. It looks through %w wrapping and errors.Join, so an
// error tagged with ErrNestedRetryDeferred still classifies.
//
//   - *mysql.MySQLError 1213 (deadlock) is RetryDeadlock, 1205 (lock wait
//     timeout) is RetryLockWaitTimeout and 1020 (MariaDB "record has changed
//     since last read") is RetryRecordChanged. The server rolls the whole
//     transaction back on 1213 and on 1020, so a fresh root attempt is safe;
//     after 1205 the caller rolls back before replaying.
//   - *gosqlite.Error whose primary code (Code()&0xff) is SQLITE_BUSY (5) or
//     SQLITE_LOCKED (6) is RetrySQLiteBusy; extended codes such as 261, 517
//     and 262 count.
//
// Everything else, nil included, is RetryNone.
func RetryReasonOf(err error) RetryReason {
	if err == nil {
		return RetryNone
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		switch mysqlErr.Number {
		case mysqlNumDeadlock:
			return RetryDeadlock
		case mysqlNumLockWaitTimeout:
			return RetryLockWaitTimeout
		case mysqlNumCheckRead:
			return RetryRecordChanged
		}
	}
	var sqliteErr *gosqlite.Error
	if errors.As(err, &sqliteErr) {
		switch sqliteErr.Code() & 0xff {
		case sqliteBusy, sqliteLocked:
			return RetrySQLiteBusy
		}
	}
	return RetryNone
}

// IsRetryable reports whether err is transient contention that a fresh
// transaction attempt may clear (see RetryReasonOf).
func IsRetryable(err error) bool {
	return RetryReasonOf(err) != RetryNone
}
