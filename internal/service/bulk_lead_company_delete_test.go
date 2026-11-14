package service

// MySQL/MariaDB barrier root of #182 for bulk lead status: a batch over the
// leads of a company against that company's delete, in both orderings, on a
// disposable TEST_MYSQL_DSN schema. The batch takes no company lock and locks
// only its leads, by id ascending (captured statements); the second party
// waits on the lead rows (seen in information_schema.PROCESSLIST on two
// consecutive polls); neither returns error 1213; the batch commits its
// statuses and the leads end with company_id NULL.

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	// bulkLeadLockRead is BulkStatusRepository.GetLeadsByIDs as PROCESSLIST shows it.
	bulkLeadLockRead = regexp.MustCompile("(?s)^SELECT \\* FROM `leads` WHERE id IN .*ORDER BY id ASC FOR UPDATE$")
	// unlinkLeadsWrite is CompanyRepository.UnlinkLeads.
	// The value is NULL when the driver interpolates and ? when it prepares.
	unlinkLeadsWrite = regexp.MustCompile("^UPDATE `leads` SET `company_id`=(NULL|\\?)")
)

// bulkStatementRecorder captures every statement of the batch.
type bulkStatementRecorder struct {
	logger.Interface
	mu         sync.Mutex
	statements []string
}

func (r *bulkStatementRecorder) LogMode(logger.LogLevel) logger.Interface { return r }

func (r *bulkStatementRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	statement, _ := fc()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = append(r.statements, statement)
}

func (r *bulkStatementRecorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.statements...)
}

func TestBulkLeadStatusVsCompanyDeleteMySQL(t *testing.T) {
	for _, deleteFirst := range []bool{false, true} {
		name := map[bool]string{false: "bulk first", true: "delete first"}[deleteFirst]
		t.Run(name, func(t *testing.T) {
			db := openAutomationMySQL(t, 6)
			f := newLeadCompanyFixture(t, db)
			company := f.company()
			first, second, third := f.lead(company), f.lead(company), f.lead(company)

			// The batch runs on a session whose every statement is recorded,
			// its transaction included.
			recorder := &bulkStatementRecorder{Interface: logger.Discard}
			recorded := db.Session(&gorm.Session{Logger: recorder})
			quiet := logrus.New()
			quiet.SetLevel(logrus.PanicLevel)
			bulk := NewBulkStatusService(repository.NewBulkOperationRepository(recorded), repository.NewBulkStatusRepository(recorded),
				utils.NewTransactionManager(recorded), quiet, WithBulkAutomationEvents(f.env.repo)).(*bulkStatusService)

			gate := newParkGate()
			bulkDone := make(chan error, 1)
			deleteDone := make(chan error, 1)
			runBulk := func() {
				_, err := bulk.BulkSetLeadStatus(context.Background(), f.env.admin.ID, models.RoleAdmin,
					[]uint{third.ID, first.ID, second.ID}, models.LeadStatusContacted)
				bulkDone <- err
			}
			if deleteFirst {
				f.companySvc.afterDeleteUnlink = func(uint) { gate.park() }
				go func() { deleteDone <- f.companySvc.Delete(company.ID) }()
				gate.awaitReached(t, "delete")
				go runBulk()
				awaitLeadCompanyLockWait(t, db, bulkLeadLockRead)
			} else {
				bulk.afterLeadLock = func([]uint) { gate.park() }
				go runBulk()
				gate.awaitReached(t, "bulk")
				go func() { deleteDone <- f.companySvc.Delete(company.ID) }()
				awaitLeadCompanyLockWait(t, db, unlinkLeadsWrite)
			}
			close(gate.release)

			bulkErr := awaitDone(t, "bulk", bulkDone)
			deleteErr := awaitDone(t, "delete", deleteDone)
			assertNotDeadlock(t, bulkErr)
			assertNotDeadlock(t, deleteErr)
			require.NoError(t, deleteErr, "the delete succeeds")
			require.NoError(t, bulkErr, "the batch commits")

			for _, id := range []uint{first.ID, second.ID, third.ID} {
				stored := f.storedLead(id)
				assert.Equal(t, models.LeadStatusContacted, stored.Status)
				assert.Nil(t, stored.CompanyID)
			}
			f.assertInvariant()

			// No company lock, and the leads locked in id order.
			var leadLocks []string
			for _, statement := range recorder.all() {
				assert.NotContains(t, statement, "`companies`", "the batch takes no company lock: %s", statement)
				if strings.Contains(statement, "FOR UPDATE") {
					leadLocks = append(leadLocks, statement)
				}
			}
			require.Len(t, leadLocks, 1, "one locking read")
			assert.Regexp(t, bulkLeadLockRead, leadLocks[0])
		})
	}
}
