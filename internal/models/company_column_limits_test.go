package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The handler enforces the Company*MaxLength constants; the migration and
// AutoMigrate enforce the `type:varchar(N)` tags. This test is what keeps the
// two in step: widen a column and forget the constant, or the other way round,
// and it fails.
func TestCompanyColumnLimitsMatchTheModel(t *testing.T) {
	for _, tc := range []struct {
		field string
		limit int
	}{
		{"Name", CompanyNameMaxLength},
		{"Domain", CompanyDomainMaxLength},
		{"Website", CompanyWebsiteMaxLength},
		{"Industry", CompanyIndustryMaxLength},
		{"EmployeeRange", CompanyEmployeeRangeMaxLength},
		{"Phone", CompanyPhoneMaxLength},
		{"Address", CompanyAddressMaxLength},
		{"City", CompanyCityMaxLength},
		{"State", CompanyStateMaxLength},
		{"Country", CompanyCountryMaxLength},
		{"PostalCode", CompanyPostalCodeMaxLength},
	} {
		assert.Equal(t, columnSize(t, &Company{}, tc.field), tc.limit, "limit for Company.%s disagrees with its column width", tc.field)
	}
}

// Every admitted employee range has to fit the column it is stored in.
func TestCompanyEmployeeRangesFitTheColumn(t *testing.T) {
	for _, value := range CompanyEmployeeRanges {
		assert.LessOrEqual(t, len(value), CompanyEmployeeRangeMaxLength, value)
	}
}
