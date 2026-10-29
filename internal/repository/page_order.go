package repository

import (
	"github.com/florinel-chis/gophercrm/internal/utils"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Paginated reads must walk a TOTAL order. OFFSET/LIMIT pages are separate
// statements, and when the ORDER BY leaves rows tied (every lead created in the
// same import shares created_at; a status column has a handful of values) the
// engine may return the tied rows in a different order on every statement.
// MySQL's filesort with a LIMIT does exactly that, so consecutive pages used
// to repeat some rows and never show others (#110).
//
// The helpers below therefore always end the ORDER BY with the primary key,
// qualified with the table name so a joined query (tasks filtered by label)
// can never resolve it against the other table. Identifiers are emitted
// through clause.OrderByColumn, which quotes them for the dialect; the sort
// column itself still comes only from the utils.ValidateSort allowlist.

// orderByColumnThenID orders by table.column in the given direction and then
// by table.id in the same direction.
func orderByColumnThenID(db *gorm.DB, table, column string, desc bool) *gorm.DB {
	if column != "id" {
		db = db.Order(clause.OrderByColumn{Column: clause.Column{Table: table, Name: column}, Desc: desc})
	}
	return db.Order(clause.OrderByColumn{Column: clause.Column{Table: table, Name: "id"}, Desc: desc})
}

// orderByID orders by table.id ascending: the stable default of the legacy
// list endpoints that take no sort parameter.
func orderByID(db *gorm.DB, table string) *gorm.DB {
	return orderByColumnThenID(db, table, "id", false)
}

// orderPage applies the ordering of a legacy list or search endpoint. With no
// sort requested the rows come in ascending id order, which is the order these
// endpoints returned implicitly before the tie-breaker existed. With a sort the
// validated column is followed by the id in the same direction. table both
// selects the allowlist in utils.AllowedSortColumns and qualifies the columns.
func orderPage(db *gorm.DB, table, sortBy, sortOrder string) (*gorm.DB, error) {
	if sortBy == "" {
		return orderByID(db, table), nil
	}
	column, direction, err := utils.ValidateSort(table, sortBy, sortOrder)
	if err != nil {
		return nil, err
	}
	return orderByColumnThenID(db, table, column, direction == "desc"), nil
}
