package main

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Backdating.
//
// The services stamp the current time on every write (created_at,
// updated_at, a deal's closed_at and stage-change times, a quote's sent_at,
// closed_at and status-change times, a ticket's first response, a
// notification's times) and have no clock option. A database generated in
// one run would therefore hold a history that is a few seconds long. The
// generator records the time each record should have while it writes it,
// and once everything is written this plan moves the timestamps into the
// backdated window. It goes through GORM's model API (UpdateColumns, which
// writes exactly the given columns and does not touch updated_at itself),
// never through raw INSERT or UPDATE statements.

type backdateRow struct {
	model any
	id    uint
	cols  map[string]any
}

type backdateChildren struct {
	model  any
	fk     string
	parent uint
	column string
	// times are given to the child rows in id order; extra rows take the
	// last time.
	times []time.Time
}

type backdatePlan struct {
	rows []backdateRow
	kids []backdateChildren
}

// at sets created_at and updated_at of one row to t.
func (p *backdatePlan) at(model any, id uint, t time.Time) {
	p.stamp(model, id, t, t)
}

// stamp sets created_at and updated_at of one row.
func (p *backdatePlan) stamp(model any, id uint, created, updated time.Time) {
	p.set(model, id, map[string]any{"created_at": created, "updated_at": updated})
}

// set writes the given columns of one row.
func (p *backdatePlan) set(model any, id uint, cols map[string]any) {
	for k, v := range cols {
		if t, ok := v.(time.Time); ok {
			cols[k] = t.UTC()
		}
	}
	p.rows = append(p.rows, backdateRow{model: model, id: id, cols: cols})
}

// children sets one time column of the rows whose fk is parent.
func (p *backdatePlan) children(model any, fk string, parent uint, column string, times []time.Time) {
	p.kids = append(p.kids, backdateChildren{model: model, fk: fk, parent: parent, column: column, times: times})
}

func (p *backdatePlan) apply(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		for _, row := range p.rows {
			if err := tx.Model(row.model).Where("id = ?", row.id).UpdateColumns(row.cols).Error; err != nil {
				return fmt.Errorf("backdating %T %d: %w", row.model, row.id, err)
			}
		}
		for _, c := range p.kids {
			if len(c.times) == 0 {
				continue
			}
			var ids []uint
			if err := tx.Model(c.model).Where(c.fk+" = ?", c.parent).Order("id").Pluck("id", &ids).Error; err != nil {
				return err
			}
			for k, id := range ids {
				t := c.times[len(c.times)-1]
				if k < len(c.times) {
					t = c.times[k]
				}
				if err := tx.Model(c.model).Where("id = ?", id).UpdateColumn(c.column, t.UTC()).Error; err != nil {
					return fmt.Errorf("backdating %T %d: %w", c.model, id, err)
				}
			}
		}
		return nil
	})
}
