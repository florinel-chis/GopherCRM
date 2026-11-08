package repository

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Regression roots for #234: LeadRepository.ListAllForExport reads the whole
// book of leads in ONE ordered statement whose order is total (#105): the
// validated sort column and then leads.id in the same direction, or id
// ascending without a sort column.
//
// The fixture is adversarial to any batching shortcut or partial order:
//
//   - more than 1,000 rows (1,203 inserted, 24 soft-deleted);
//   - every sort value runs AGAINST id order, so a primary-key cursor would
//     drop most rows;
//   - classification, company, names and timestamps come in runs of
//     duplicates, so the id tie-breaker decides the order inside each run;
//   - a block of NULL and a block of empty classification values, and a block
//     of NULL and a block of empty company values, which must order
//     deterministically too (NULL first ascending, on both engines);
//   - converted leads (status converted, customer_id set), which are live and
//     must be exported, and soft-deleted leads, which must not;
//   - two owners, a search term and classification values to filter on.
//
// Every value is lowercase ASCII whose order Go's byte comparison, MySQL's
// case-insensitive collation and SQLite's BINARY collation agree on.
const (
	leadExportTraversalRows   = 1203
	leadExportTraversalErased = 24 // the lowest ids
)

var leadExportTraversalBase = time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)

// leadExportTraversalClasses is in ascending byte order, which is ascending
// rank order.
var leadExportTraversalClasses = []models.LeadClassification{
	models.LeadClassificationHotLead, models.LeadClassificationLead, models.LeadClassificationSpam,
	models.LeadClassificationTest, models.LeadClassificationUnclassified,
}

var leadExportTraversalModels = []interface{}{
	&models.User{}, &models.Company{}, &models.Customer{}, &models.Lead{},
}

type leadExportTraversalFixture struct {
	live      []models.Lead
	erasedIDs []uint
	converted []uint
	owners    [2]uint
	nullClass map[uint]bool
	nullCo    map[uint]bool
}

func TestLeadExportTraversalSQLite(t *testing.T) {
	runLeadExportTraversal(t, openDisposableDB(t, "lead_export_traversal", false, leadExportTraversalModels...))
}

func TestLeadExportTraversalMySQL(t *testing.T) {
	runLeadExportTraversal(t, openDisposableDB(t, "lead_export_traversal", true, leadExportTraversalModels...))
}

// seedLeadExportTraversal inserts the fixture and returns the live rows as the
// database stores them. Row i (the i-th smallest id) has rank rows-1-i and
// every sort value grows with rank.
func seedLeadExportTraversal(t *testing.T, db *gorm.DB) leadExportTraversalFixture {
	t.Helper()

	var fixture leadExportTraversalFixture
	for k := 0; k < 2; k++ {
		owner := models.User{
			Email:     fmt.Sprintf("owner%d@lead-export.test", k),
			Password:  "not-a-real-hash",
			FirstName: "Owner",
			LastName:  fmt.Sprint(k),
			Role:      models.RoleSales,
			IsActive:  true,
		}
		require.NoError(t, db.Create(&owner).Error)
		fixture.owners[k] = owner.ID
	}

	rows := make([]models.Lead, 0, leadExportTraversalRows)
	for i := 0; i < leadExportTraversalRows; i++ {
		rank := leadExportTraversalRows - 1 - i
		notes := "beta-notes"
		if i%3 != 0 {
			notes = "alpha-notes"
		}
		status := models.LeadStatusNew
		if i%2 == 0 {
			status = models.LeadStatusContacted
		}
		rows = append(rows, models.Lead{
			BaseModel: models.BaseModel{
				CreatedAt: leadExportTraversalBase.Add(time.Duration(rank/9) * time.Second),
				UpdatedAt: leadExportTraversalBase.Add(time.Duration(rank/3) * time.Second),
			},
			FirstName:      fmt.Sprintf("fn%04d", rank/7),
			LastName:       fmt.Sprintf("ln%04d", rank/11),
			Email:          fmt.Sprintf("u%04d@lead-export.test", rank),
			Phone:          fmt.Sprintf("+1 555 %04d", i),
			Company:        fmt.Sprintf("co%04d", rank/13),
			Source:         "website",
			Status:         status,
			Classification: leadExportTraversalClasses[rank*len(leadExportTraversalClasses)/leadExportTraversalRows],
			Notes:          notes,
			OwnerID:        fixture.owners[i%2],
		})
	}
	require.NoError(t, db.CreateInBatches(&rows, 200).Error)
	for i := 1; i < len(rows); i++ {
		require.Greater(t, rows[i].ID, rows[i-1].ID, "ids must grow in insertion order for the fixture to run against them")
	}

	// NULL and empty blocks. UpdateColumn leaves updated_at alone.
	idsWhere := func(match func(rank int) bool) []uint {
		var ids []uint
		for i := range rows {
			if match(leadExportTraversalRows - 1 - i) {
				ids = append(ids, rows[i].ID)
			}
		}
		return ids
	}
	nullClassIDs := idsWhere(func(rank int) bool { return rank >= 600 && rank < 620 })
	emptyClassIDs := idsWhere(func(rank int) bool { return rank >= 620 && rank < 640 })
	nullCoIDs := idsWhere(func(rank int) bool { return rank/13 == 40 })
	emptyCoIDs := idsWhere(func(rank int) bool { return rank/13 == 41 })
	require.NoError(t, db.Model(&models.Lead{}).Where("id IN ?", nullClassIDs).UpdateColumn("classification", nil).Error)
	require.NoError(t, db.Model(&models.Lead{}).Where("id IN ?", emptyClassIDs).UpdateColumn("classification", "").Error)
	require.NoError(t, db.Model(&models.Lead{}).Where("id IN ?", nullCoIDs).UpdateColumn("company", nil).Error)
	require.NoError(t, db.Model(&models.Lead{}).Where("id IN ?", emptyCoIDs).UpdateColumn("company", "").Error)

	// Converted leads: live rows with a customer, which the export includes.
	for i := 50; i < len(rows); i += 101 {
		customer := models.Customer{FirstName: "conv", LastName: fmt.Sprint(i), Email: fmt.Sprintf("conv%04d@lead-export.test", i)}
		require.NoError(t, db.Create(&customer).Error)
		require.NoError(t, db.Model(&models.Lead{}).Where("id = ?", rows[i].ID).UpdateColumns(map[string]interface{}{
			"status": models.LeadStatusConverted, "customer_id": customer.ID,
		}).Error)
		fixture.converted = append(fixture.converted, rows[i].ID)
	}

	// Soft-deleted leads (an erasure ends in a soft delete).
	for i := 0; i < leadExportTraversalErased; i++ {
		require.NoError(t, db.Delete(&models.Lead{}, rows[i].ID).Error)
		fixture.erasedIDs = append(fixture.erasedIDs, rows[i].ID)
	}

	require.NoError(t, db.Order("id").Find(&fixture.live).Error)
	require.Len(t, fixture.live, leadExportTraversalRows-leadExportTraversalErased)

	pluckSet := func(where string) map[uint]bool {
		var ids []uint
		require.NoError(t, db.Model(&models.Lead{}).Where(where).Pluck("id", &ids).Error)
		set := make(map[uint]bool, len(ids))
		for _, id := range ids {
			set[id] = true
		}
		return set
	}
	fixture.nullClass = pluckSet("classification IS NULL")
	fixture.nullCo = pluckSet("company IS NULL")
	require.Len(t, fixture.nullClass, 20, "the NULL classification rows must be stored as NULL")
	require.Len(t, fixture.nullCo, 13, "the NULL company rows must be stored as NULL")
	require.Len(t, pluckSet("classification = ''"), 20, "the empty classification rows must be stored as ''")
	require.Len(t, pluckSet("company = ''"), 13, "the empty company rows must be stored as ''")

	distinctCreated := map[int64]bool{}
	for _, row := range fixture.live {
		distinctCreated[row.CreatedAt.Unix()] = true
	}
	require.Greater(t, len(distinctCreated), 100, "created_at fixture values were not stored as set")
	return fixture
}

// leadCompareExportColumn orders two rows by one sort column as the database
// does: NULL first, then the value. The caller breaks ties by id.
func leadCompareExportColumn(column string, a, b *models.Lead, f *leadExportTraversalFixture) int {
	nullable := func(aNull, bNull bool, av, bv string) int {
		switch {
		case aNull && bNull:
			return 0
		case aNull:
			return -1
		case bNull:
			return 1
		}
		return strings.Compare(av, bv)
	}
	times := func(x, y time.Time) int {
		switch {
		case x.Before(y):
			return -1
		case x.After(y):
			return 1
		}
		return 0
	}
	switch column {
	case "created_at":
		return times(a.CreatedAt, b.CreatedAt)
	case "updated_at":
		return times(a.UpdatedAt, b.UpdatedAt)
	case "first_name":
		return strings.Compare(a.FirstName, b.FirstName)
	case "last_name":
		return strings.Compare(a.LastName, b.LastName)
	case "email":
		return strings.Compare(a.Email, b.Email)
	case "company":
		return nullable(f.nullCo[a.ID], f.nullCo[b.ID], a.Company, b.Company)
	case "classification":
		return nullable(f.nullClass[a.ID], f.nullClass[b.ID], string(a.Classification), string(b.Classification))
	}
	panic("unknown lead export sort column " + column)
}

// leadExpectedExportIDs is the contract computed in Go: the rows the filter
// keeps, ordered by the column and then id in the requested direction, or by
// id ascending without a column.
func leadExpectedExportIDs(f *leadExportTraversalFixture, filter LeadExportFilter) []uint {
	rows := make([]models.Lead, 0, len(f.live))
	for _, row := range f.live {
		if filter.OwnerID != nil && row.OwnerID != *filter.OwnerID {
			continue
		}
		if filter.Classification != "" && (f.nullClass[row.ID] || row.Classification != filter.Classification) {
			continue
		}
		if filter.Search != "" {
			company := row.Company
			if f.nullCo[row.ID] {
				company = "" // NULL LIKE x is not true
			}
			matched := false
			for _, field := range []string{row.FirstName, row.LastName, row.Email, company, row.Phone, row.Notes} {
				if strings.Contains(field, filter.Search) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		rows = append(rows, row)
	}
	desc := filter.SortBy != "" && filter.SortOrder == "desc"
	sort.SliceStable(rows, func(i, j int) bool {
		c := 0
		if filter.SortBy != "" {
			c = leadCompareExportColumn(filter.SortBy, &rows[i], &rows[j], f)
		}
		if c == 0 {
			switch {
			case rows[i].ID < rows[j].ID:
				c = -1
			case rows[i].ID > rows[j].ID:
				c = 1
			}
		}
		if desc {
			return c > 0
		}
		return c < 0
	})
	return leadExportIDs(rows)
}

func leadExportIDs(rows []models.Lead) []uint {
	ids := make([]uint, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	return ids
}

// leadCountQueries counts the SELECT statements GORM runs on db.
func leadCountQueries(t *testing.T, db *gorm.DB) *int {
	t.Helper()
	count := new(int)
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("lead_export_query_counter", func(*gorm.DB) {
		*count++
	}))
	return count
}

func runLeadExportTraversal(t *testing.T, db *gorm.DB) {
	fixture := seedLeadExportTraversal(t, db)
	repo := NewLeadRepository(db)
	queries := leadCountQueries(t, db)

	assertSequence := func(t *testing.T, filter LeadExportFilter) []models.Lead {
		t.Helper()
		before := *queries
		got, err := repo.ListAllForExport(filter)
		require.NoError(t, err)
		assert.Equal(t, 1, *queries-before, "the export must be read by exactly one statement")
		ids := leadExportIDs(got)
		seen := make(map[uint]bool, len(ids))
		for _, id := range ids {
			require.False(t, seen[id], "lead %d exported twice", id)
			seen[id] = true
		}
		require.Equal(t, leadExpectedExportIDs(&fixture, filter), ids, "exported ids out of the expected sequence")
		return got
	}

	t.Run("fixture_is_adversarial", func(t *testing.T) {
		require.Greater(t, len(fixture.live), 1000)
		for _, column := range []string{"classification", "company", "email", "created_at"} {
			ids := leadExpectedExportIDs(&fixture, LeadExportFilter{SortBy: column, SortOrder: "asc"})
			assert.False(t, sort.SliceIsSorted(ids, func(i, j int) bool { return ids[i] < ids[j] }),
				"%s ascending must disagree with id order", column)
		}
		require.NotEmpty(t, fixture.converted)
	})

	t.Run("no_sort_is_id_ascending", func(t *testing.T) {
		got := assertSequence(t, LeadExportFilter{})
		require.Len(t, got, len(fixture.live))
		// A direction without a column changes nothing.
		assertSequence(t, LeadExportFilter{SortOrder: "desc"})
	})

	for _, column := range []string{"classification", "company", "email", "first_name", "created_at", "updated_at"} {
		for _, direction := range []string{"asc", "desc"} {
			column, direction := column, direction
			t.Run(column+"_"+direction, func(t *testing.T) {
				assertSequence(t, LeadExportFilter{SortBy: column, SortOrder: direction})
			})
		}
		column := column
		t.Run(column+"_desc_is_the_exact_reverse_of_asc", func(t *testing.T) {
			asc, err := repo.ListAllForExport(LeadExportFilter{SortBy: column, SortOrder: "asc"})
			require.NoError(t, err)
			desc, err := repo.ListAllForExport(LeadExportFilter{SortBy: column, SortOrder: "desc"})
			require.NoError(t, err)
			require.Len(t, desc, len(asc))
			for i := range asc {
				require.Equal(t, asc[i].ID, desc[len(desc)-1-i].ID, "descending must be the exact reverse of ascending")
			}
		})
	}

	t.Run("filters_apply_only_when_given", func(t *testing.T) {
		owner := fixture.owners[1]
		for _, filter := range []LeadExportFilter{
			{OwnerID: &owner},
			{Search: "alpha"},
			{Search: "u0700"},
			{Classification: models.LeadClassificationSpam},
			{Classification: models.LeadClassificationHotLead, SortBy: "email", SortOrder: "desc"},
			{OwnerID: &owner, Search: "alpha", SortBy: "classification", SortOrder: "asc"},
			{OwnerID: &owner, Search: "alpha", Classification: models.LeadClassificationLead, SortBy: "company", SortOrder: "desc"},
		} {
			got := assertSequence(t, filter)
			require.NotEmpty(t, got, "filter %+v must match some rows", filter)
			require.Less(t, len(got), len(fixture.live), "filter %+v must narrow the rows", filter)
		}
		var none uint = 999999
		got, err := repo.ListAllForExport(LeadExportFilter{OwnerID: &none})
		require.NoError(t, err)
		assert.Empty(t, got)
		assert.NotNil(t, got, "an empty export is an empty slice, not nil")
	})

	t.Run("converted_included_soft_deleted_excluded", func(t *testing.T) {
		got, err := repo.ListAllForExport(LeadExportFilter{SortBy: "email", SortOrder: "asc"})
		require.NoError(t, err)
		exported := make(map[uint]models.Lead, len(got))
		for _, row := range got {
			exported[row.ID] = row
		}
		for _, id := range fixture.erasedIDs {
			assert.NotContains(t, exported, id, "soft-deleted lead %d reappeared in the export", id)
		}
		for _, id := range fixture.converted {
			row, ok := exported[id]
			require.True(t, ok, "converted lead %d is missing from the export", id)
			assert.Equal(t, models.LeadStatusConverted, row.Status)
			assert.NotNil(t, row.CustomerID)
		}
	})

	t.Run("unknown_sort_column_is_an_error_without_a_query", func(t *testing.T) {
		for _, column := range []string{"password", "notes", "id; DROP TABLE leads", "owner_id"} {
			before := *queries
			got, err := repo.ListAllForExport(LeadExportFilter{SortBy: column, SortOrder: "asc"})
			require.Error(t, err, column)
			assert.Nil(t, got, column)
			assert.Equal(t, before, *queries, "%s: no statement may run for an unknown sort column", column)
		}
	})

	t.Run("output_is_repeatable", func(t *testing.T) {
		first, err := repo.ListAllForExport(LeadExportFilter{Search: "alpha", SortBy: "company", SortOrder: "desc"})
		require.NoError(t, err)
		second, err := repo.ListAllForExport(LeadExportFilter{Search: "alpha", SortBy: "company", SortOrder: "desc"})
		require.NoError(t, err)
		require.Equal(t, leadExportIDs(first), leadExportIDs(second))
	})
}
