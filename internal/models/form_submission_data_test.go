package models

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm/schema"
)

// mediumtextMaxBytes is the size of a MySQL/MariaDB MEDIUMTEXT column.
const mediumtextMaxBytes = 1<<24 - 1

// form_submissions.data holds the JSON-encoded values. json.Marshal writes `<`,
// `>` and `&` as six-byte \u escapes, so an accepted submission could encode to
// more than a TEXT column's 65,535 bytes and fail the insert on MySQL. The public
// endpoint caps the request body at 64 KiB, and even a body of nothing but `<`
// stays far below MEDIUMTEXT once escaped.
func TestFormSubmissionDataColumnHoldsAnyAcceptedSubmission(t *testing.T) {
	parsed, err := schema.Parse(&FormSubmission{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	field := parsed.LookUpField("data")
	require.NotNil(t, field)
	assert.Equal(t, "mediumtext", mysql.Dialector{Config: &mysql.Config{}}.DataTypeOf(field))

	encoded, err := encodeJSONStringMap(map[string]string{"message": strings.Repeat("<", 64<<10)})
	require.NoError(t, err)
	assert.Less(t, len(encoded), mediumtextMaxBytes)
}
