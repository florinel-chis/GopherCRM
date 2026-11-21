package repository

// Erasure — the shared machinery behind "deleting" a person from the CRM.
//
// Why anonymisation instead of a hard DELETE
//
// GDPR Article 17 gives a data subject the right to have their personal data
// erased. Article 5(1)(e) (storage limitation) points the same way: personal
// data must not be kept in an identifiable form for longer than it is needed.
// Neither of them requires the ROW to be destroyed — they require the PERSON to
// be gone from it. That distinction is what this file implements.
//
// A hard DELETE would be actively harmful here. Tickets, tasks and leads
// reference users and customers by foreign key, and audit history is only
// meaningful while those references resolve. Dropping the row would either
// break those foreign keys or cascade away business records that the
// controller is entitled — and often legally obliged — to keep. So the row
// stays, and every personal-data column in it is overwritten in place with a
// non-personal value; the retained row is then soft-deleted so it disappears
// from every ordinary query. What is left behind is a numbered shell that no
// longer identifies anybody.
//
// Erasure is NOT deactivation. Deactivation (is_active = false) is the
// non-destructive, fully reversible way to suspend an account and it must never
// touch a single field of personal data. Everything in this file is
// irreversible by design: there is nothing left to restore afterwards.
//
// The scrub and the soft delete always run in ONE unit (runErasureUnit), and
// so does every other row of the same person (erasure_cascade.go). A crash
// between the steps would otherwise leave a live, still-listed,
// already-anonymised account behind — the worst of both worlds.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// anonymizedEmailDomain is the domain used for the placeholder addresses that
// replace a real email address on erasure. ".invalid" is reserved by RFC 2606
// and is guaranteed never to resolve, so a placeholder can never be delivered
// to, mistaken for a contact address, or accidentally re-used by a real person.
const anonymizedEmailDomain = "anonymized.invalid"

// unusablePasswordHash replaces the bcrypt hash of an erased account. It is
// deliberately NOT a valid bcrypt hash: bcrypt.CompareHashAndPassword rejects
// it for every candidate password (ErrHashTooShort), so an erased account can
// never authenticate again, and the original hash — which is personal data, and
// is crackable offline — is gone.
const unusablePasswordHash = "!erased"

// newAnonymizedEmail returns a unique, non-routable placeholder address.
//
// The random component is NOT derived from the address it replaces. Hashing the
// original address would keep it linkable (an attacker holding a candidate
// address can confirm it by re-hashing), and linkable data is still personal
// data under the GDPR — that would not be erasure. 128 bits of entropy make a
// collision with another placeholder effectively impossible, which matters
// because the unique index on the email column covers soft-deleted rows too.
func newAnonymizedEmail() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating anonymized email: %w", err)
	}
	return fmt.Sprintf("deleted-%s@%s", hex.EncodeToString(buf), anonymizedEmailDomain), nil
}

// erasurePlan describes how one entity is stripped of personal data. It is the
// only thing that differs between users, customers and leads — the
// transaction handling, the placeholder generation and the ordering of the
// scrub and the soft delete are shared by eraseRecord.
type erasurePlan struct {
	// Model is a pointer to a zero value of the entity (e.g. &models.User{}).
	// It identifies the table and carries the soft-delete semantics; a fresh
	// instance is derived from it for every statement, so a plan value can be
	// reused safely.
	Model interface{}

	// EmailColumn is the column that receives a unique non-routable placeholder
	// rather than a constant. It gets its own field because it is the one
	// personal-data column that cannot simply be blanked: the unique index on
	// it is not scoped to deleted_at, so every erased row still has to hold a
	// distinct value. Leave it empty for an entity with no email.
	EmailColumn string

	// Scrub maps every OTHER personal-data column to the non-personal value
	// that replaces it. Columns that are NOT NULL are blanked ("") rather than
	// nulled. Anything omitted here survives the erasure, so the map is the
	// authoritative list of what counts as personal data for this entity.
	Scrub map[string]interface{}

	// AfterScrub, when set, runs inside the SAME transaction after the columns
	// have been overwritten and before the row is soft-deleted. It exists for
	// data that lives outside the row itself — credentials, above all — which
	// must not outlive the person either.
	AfterScrub func(tx *gorm.DB, id uint) error

	// BeforeScrub runs inside the SAME transaction before the row is read or
	// touched. The lead plan's automation cancel lives here, and the erasure
	// unit runs that cancel itself for every lead of the graph, before its
	// first locking read of a lead (#337, #385): an automation step holds the
	// lead row FOR UPDATE while it runs, so an erasure that waited on the lead
	// before cancelling would let the step commit its task. Having modified
	// the run rows by the time it waits on the lead is what makes the erasure
	// the survivor of the resulting MySQL deadlock: the step (no rows modified
	// yet) is rolled back, retried, and then finds the run cancelled and the
	// lead gone. eraseRecord still runs a plan's BeforeScrub when the caller
	// has not cleared it.
	BeforeScrub func(tx *gorm.DB, id uint) error
}

// newModel returns a fresh zero pointer of the plan's entity type. Every
// statement gets its own so that GORM writing back into the destination struct
// of one statement can never influence the next.
func (p erasurePlan) newModel() interface{} {
	return reflect.New(reflect.TypeOf(p.Model).Elem()).Interface()
}

// erasureScrubObserver, when set, is told about every row eraseRecord scrubs.
// It is a TEST-ONLY seam for the tests that prove every node of an identity
// graph is scrubbed exactly once; nothing in production sets it. It is held
// atomically so that setting it in a test never races a concurrent erasure.
var erasureScrubObserver atomic.Pointer[func(table string, id uint)]

// eraseRecord performs a GDPR Article 17 erasure of a single row: it overwrites
// the personal-data columns named by the plan, runs the plan's extra in-place
// clean-up, and only then soft-deletes the row.
//
// tx must already be the transactional handle of the erasure unit (see
// runErasureUnit): a person is erased as a whole graph of rows, and the unit
// that makes the graph atomic belongs to the cascade, never to a single row.
// The row is already locked by then: the unit locks the whole graph before
// its first scrub (erasure_cascade.go).
func eraseRecord(tx *gorm.DB, id uint, plan erasurePlan) error {
	if plan.BeforeScrub != nil {
		if err := plan.BeforeScrub(tx, id); err != nil {
			return err
		}
	}

	erased := make(map[string]interface{}, len(plan.Scrub))
	for column, value := range plan.Scrub {
		erased[column] = value
	}

	// The scrub of every column but the address comes first: by primary key,
	// it takes the row lock (or waits for a writer that holds it, and then
	// overwrites what that writer committed). Unscoped so that re-running an
	// erasure over an already soft-deleted row — a legacy row soft-deleted
	// before deletion became an erasure, and therefore still holding personal
	// data — still scrubs it.
	scrub := tx.Unscoped().Model(plan.newModel()).Where("id = ?", id).Updates(erased)
	if scrub.Error != nil {
		return scrub.Error
	}
	if observe := erasureScrubObserver.Load(); observe != nil {
		(*observe)(scrub.Statement.Table, id)
	}

	// The original address is read before the placeholder overwrites it: the
	// unlinked form submissions of the person are found by it (#214), and
	// AfterScrub only ever sees the placeholder. readErasedEmail is a locking
	// read by primary key (#337), so it returns the committed address, never
	// one from a REPEATABLE READ snapshot older than the lock.
	var original, placeholder string
	if plan.EmailColumn != "" {
		var err error
		if original, err = readErasedEmail(tx, plan, id); err != nil {
			return err
		}
		if placeholder, err = newAnonymizedEmail(); err != nil {
			return err
		}
		if err := tx.Unscoped().Model(plan.newModel()).Where("id = ?", id).
			Update(plan.EmailColumn, placeholder).Error; err != nil {
			return err
		}
	}

	if plan.AfterScrub != nil {
		if err := plan.AfterScrub(tx, id); err != nil {
			return err
		}
	}

	if plan.EmailColumn != "" {
		if err := scrubUnlinkedSubmissions(tx, scrub.Statement.Table, id, original, placeholder); err != nil {
			return err
		}
	}

	return softDeleteErased(tx, plan, id)
}

// softDeleteErased soft-deletes the erased row with an UPDATE whose only
// predicate is the primary key. GORM's own soft delete adds
// `AND deleted_at IS NULL`, and MariaDB 10.11 may serve that predicate through
// the deleted_at secondary index (idx_customers_deleted_at,
// idx_leads_deleted_at, idx_users_deleted_at): a locking scan of the live
// range, which takes next-key locks on every live row of the table and a gap
// lock past its end. Held until the erasure commits, that gap lock made an
// unrelated conversion's INSERT INTO customers wait (#337, seen on MariaDB
// 10.11, not on MySQL 8).
//
// Whether the row is live is read first, by primary key under the row lock
// the scrub already holds; a row soft-deleted earlier keeps its deletion time,
// exactly as GORM's predicate kept it. The live row is then soft-deleted
// through GORM's own Delete (so the Delete callback chain, its hooks and every
// registered Delete callback still run; the erasure-atomicity tests inject
// their soft-delete fault there) with the deleted_at predicate suppressed by
// primaryKeyOnlySoftDelete. The statement is
// `UPDATE … SET deleted_at = ? WHERE id = ?`.
//
// GORM's missing-WHERE guard (checkMissingWhereConditions, gorm v1.30.0)
// counts the deleted_at predicate as one of the conditions whenever the
// soft_delete_enabled marker is present, so with the predicate suppressed it
// would refuse the single id condition with gorm.ErrMissingWhereClause. The
// statement therefore runs in a Session with AllowGlobalUpdate for this one
// call; the guard is replaced by the explicit check below that the id is
// non-zero, and the WHERE always carries `id = ?`
// (TestLeadErasureLockOrderSQLite asserts it on every erased table).
func softDeleteErased(tx *gorm.DB, plan erasurePlan, id uint) error {
	if id == 0 {
		return errors.New("soft-deleting an erased row: refusing a zero id")
	}
	var live []int64
	if err := lockingRead(tx).Unscoped().Model(plan.newModel()).Where("id = ?", id).
		Pluck("deleted_at IS NULL", &live).Error; err != nil {
		return fmt.Errorf("reading the deletion state of row %d before erasure: %w", id, err)
	}
	if len(live) == 0 || live[0] == 0 {
		return nil
	}
	if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).
		Clauses(primaryKeyOnlySoftDelete{}).
		Where("id = ?", id).
		Delete(plan.newModel()).Error; err != nil {
		return fmt.Errorf("soft-deleting row %d after erasure: %w", id, err)
	}
	return nil
}

// primaryKeyOnlySoftDelete stores the soft_delete_enabled marker on the
// statement before GORM's SoftDeleteDeleteClause runs. gorm.SoftDeleteQueryClause
// (gorm v1.30.0, soft_delete.go) adds `deleted_at IS NULL` only when that
// marker is absent, so its presence keeps the predicate, and with it any
// locking scan of the deleted_at index, out of the soft delete. The marker is
// never built into SQL: it is not in any clause build list.
type primaryKeyOnlySoftDelete struct{}

func (primaryKeyOnlySoftDelete) Name() string { return "soft_delete_enabled" }

func (primaryKeyOnlySoftDelete) Build(clause.Builder) {}

func (primaryKeyOnlySoftDelete) MergeClause(*clause.Clause) {}

// readErasedEmail returns the address stored on the row being erased,
// soft-deleted rows included, or "" when the row or the value is absent. It
// is a locking read by primary key (FOR UPDATE on MySQL/MariaDB; #337): the
// row is the one the erasure scrubs, so the lock is already its own. The
// error names the row by id only.
func readErasedEmail(tx *gorm.DB, plan erasurePlan, id uint) (string, error) {
	var emails []sql.NullString
	if err := lockingRead(tx).Unscoped().Model(plan.newModel()).Where("id = ?", id).
		Pluck(plan.EmailColumn, &emails).Error; err != nil {
		return "", fmt.Errorf("reading the address of row %d before erasure: %w", id, err)
	}
	if len(emails) == 0 || !emails[0].Valid {
		return "", nil
	}
	return emails[0].String, nil
}

// unlinkedSubmissionAddresses returns the values form_submissions.email is
// matched against for an erased row's original address: the address as
// stored and its lower-cased form (form intake stores the address
// lower-cased). Nothing is matched for a blank address or one that is already
// a placeholder: those identify nobody, and a blank would match every
// submission without an address.
func unlinkedSubmissionAddresses(original string) []string {
	if strings.TrimSpace(original) == "" {
		return nil
	}
	lower := strings.ToLower(original)
	if strings.HasSuffix(strings.TrimSpace(lower), "@"+anonymizedEmailDomain) {
		return nil
	}
	if lower == original {
		return []string{original}
	}
	return []string{original, lower}
}

// scrubUnlinkedSubmissions extends an erasure to the form submissions that
// no lead owns (lead_id IS NULL: spam, never-confirmed opt-ins, forms with
// create_lead off) but that carry the erased person's address (#214). The
// match is on the email column only, `email IN (original, lower(original))`:
// exact or lower-cased on SQLite, and decided by the utf8mb4_unicode_ci
// collation on MySQL/MariaDB (case- and accent-insensitive, trailing spaces
// ignored), the same rule form intake's lead linking uses. The submitted data
// is not searched. Every submission from that address is affected, a shared
// inbox's included.
//
// Unscoped reads and writes: live and soft-deleted submissions, rows of
// soft-deleted forms and every status. Each matched row gets the erased row's
// placeholder and empty values, and keeps its form, status, null lead_id and
// own deletion state. Its confirmation tokens and mail delivery rows are
// hard-deleted.
//
// A schema without form_submissions (fixtures that migrate users, customers
// and credentials only) skips the scrub through the migrator probe; when the
// table exists every failure propagates and rolls the erasure back. Errors
// name the erased row by table and id, never by address.
func scrubUnlinkedSubmissions(tx *gorm.DB, table string, id uint, original, placeholder string) error {
	addresses := unlinkedSubmissionAddresses(original)
	if len(addresses) == 0 {
		return nil
	}
	if !tx.Migrator().HasTable(&models.FormSubmission{}) {
		return nil
	}

	var submissionIDs []uint
	if err := tx.Unscoped().Model(&models.FormSubmission{}).
		Where("lead_id IS NULL AND email IN ?", addresses).
		Order("id").Pluck("id", &submissionIDs).Error; err != nil {
		return fmt.Errorf("listing unlinked form submissions of %s %d: %w", table, id, err)
	}
	if len(submissionIDs) == 0 {
		return nil
	}

	err := tx.Unscoped().Model(&models.FormSubmission{}).
		Where("id IN ?", submissionIDs).
		Updates(map[string]interface{}{
			"data":          "{}",
			"email":         placeholder,
			"ip_address":    "",
			"user_agent":    "",
			"referrer":      "",
			"utm_source":    "",
			"utm_medium":    "",
			"utm_campaign":  "",
			"utm_term":      "",
			"utm_content":   "",
			"referrer_host": "",
		}).Error
	if err != nil {
		return fmt.Errorf("scrubbing unlinked form submissions of %s %d: %w", table, id, err)
	}

	if err := tx.Unscoped().Where("submission_id IN ?", submissionIDs).
		Delete(&models.FormConfirmationToken{}).Error; err != nil {
		return fmt.Errorf("purging confirmation tokens of unlinked submissions of %s %d: %w", table, id, err)
	}
	if tx.Migrator().HasTable(&models.FormMailDelivery{}) {
		if err := tx.Unscoped().Where("submission_id IN ?", submissionIDs).
			Delete(&models.FormMailDelivery{}).Error; err != nil {
			return fmt.Errorf("purging form mail deliveries of unlinked submissions of %s %d: %w", table, id, err)
		}
	}
	// Their idempotency keys and form ticket key rows (#213) go too.
	if err := scrubSubmissionTicketKeys(tx, submissionIDs); err != nil {
		return fmt.Errorf("scrubbing form ticket keys of unlinked submissions of %s %d: %w", table, id, err)
	}
	return nil
}

// erasureMaxRetries bounds the replays of one erasure transaction that lost a
// deadlock or a lock wait (or met a busy SQLite file) and rolled back cleanly.
// erasureGraphMaxRestarts bounds the whole-root restarts of an erasure whose
// graph moved between its discovery and its locks (ErrErasureGraphChanged):
// each restart discovers the graph again. The worst case of one erasure is
// (erasureGraphMaxRestarts+1) discoveries of (erasureMaxRetries+1)
// transactions each.
const (
	erasureMaxRetries       = 3
	erasureGraphMaxRestarts = 3
)

// erasureSavepointSeq numbers the savepoints of erasure units so that two
// erasures joined to the same caller transaction never reuse a name.
var erasureSavepointSeq atomic.Uint64

// runErasureUnit runs fn as ONE all-or-nothing unit.
//
// Every repository exposes WithTx, so the handle may be the database or a
// transaction the caller opened. On the database a transaction is begun and
// committed or rolled back here (GORM rolls it back on a panic too). On a
// caller's transaction the unit is bracketed by a SAVEPOINT of its own: joining
// the caller's transaction bare would let a caller that handles a late erasure
// error — or recovers a panic — and then commits keep half a person's erasure:
// some rows scrubbed, some credentials purged, the rest intact.
//
//   - The SAVEPOINT statement is checked (utils.ErrSavepointFailed).
//   - On success the savepoint is released; the unit's writes stay provisional
//     in the caller's transaction.
//   - On an error, and on a panic unwinding through fn, the unit is rolled
//     back to the savepoint. A panic then continues to propagate unchanged.
//   - When a ROLLBACK TO SAVEPOINT (or a failed RELEASE's rollback) itself
//     fails, the unit's writes may still be in the caller's transaction, so the
//     whole caller transaction is rolled back through its committer and the
//     error carries utils.ErrRollbackFailed and utils.ErrTransactionPoisoned:
//     nothing of it can be committed any more.
//
// The control statements are sent to the underlying transaction, bypassing
// GORM's prepared-statement wrapper: MySQL cannot prepare SAVEPOINT, RELEASE or
// ROLLBACK TO.
//
// Retry (#385). On the database the transaction runs under
// utils.TransactionManager.WithTransactionAndRetry: an attempt that lost a
// deadlock (1213) or a lock wait (1205), or met a busy SQLite file, and rolled
// back cleanly is replayed, at most erasureMaxRetries times. On a caller's
// transaction nothing is retried here: the unit runs once, and a deadlock, a
// lock timeout or ErrErasureGraphChanged reaches the caller, whose whole
// transaction is the only safe unit to replay.
//
// Context (#571). ctx is the caller's request context. On the database the
// transaction is bound to it: a done context fails before begin, and one that
// ends before the commit rolls the whole unit back; a cancellation is never
// replayed. On a caller's transaction the SAVEPOINT and the unit's statements
// carry ctx, and the rollback to the savepoint runs on an uncancellable copy
// of it (rollbackErasureUnit). Either way the error wraps ctx's error.
func runErasureUnit(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) (err error) {
	if !inTransaction(db) {
		return utils.NewTransactionManager(db).WithTransactionAndRetry(ctx, func(ctx context.Context) error {
			tx, ok := utils.GetTxFromContext(ctx)
			if !ok {
				return utils.ErrNoTransaction
			}
			return fn(tx)
		}, erasureMaxRetries)
	}

	db = db.WithContext(ctx)
	name := fmt.Sprintf("gocrm_erasure_%d", erasureSavepointSeq.Add(1))
	if execErr := erasureControlExec(db, "SAVEPOINT "+name); execErr != nil {
		return fmt.Errorf("%w: %w", utils.ErrSavepointFailed, execErr)
	}

	finished := false
	defer func() {
		if finished {
			return
		}
		// fn is unwinding through a panic (or runtime.Goexit). Discard the
		// unit before the caller can recover and commit; the panic itself is
		// not recovered here and reaches the caller unchanged. A failed
		// rollback aborts the caller's transaction instead.
		_ = rollbackErasureUnit(db, name)
	}()
	err = fn(db)
	finished = true

	if err == nil {
		releaseErr := erasureControlExec(db, "RELEASE SAVEPOINT "+name)
		if releaseErr == nil {
			return nil
		}
		err = fmt.Errorf("releasing erasure savepoint: %w", releaseErr)
	}
	if abort := rollbackErasureUnit(db, name); abort != nil {
		return errors.Join(err, abort)
	}
	return err
}

// rollbackErasureUnit rolls the caller's transaction back to the unit's
// savepoint. When that fails the whole caller transaction is rolled back, and
// the returned error says nothing of it may be committed.
//
// The rollback runs on an uncancellable copy of the unit's context (#571), the
// rule of utils runNested: a cancelled or expired request must still discard
// the unit's writes, and must not poison a caller transaction whose own
// context is alive. All three rollbacks of runErasureUnit (after a panic, a
// failed RELEASE and an error of fn) come through here.
func rollbackErasureUnit(db *gorm.DB, name string) error {
	if ctx := db.Statement.Context; ctx != nil {
		db = db.WithContext(context.WithoutCancel(ctx))
	}
	rbErr := erasureControlExec(db, "ROLLBACK TO SAVEPOINT "+name)
	if rbErr == nil {
		return nil
	}
	abort := fmt.Errorf("%w: %w: %w", utils.ErrTransactionPoisoned, utils.ErrRollbackFailed, rbErr)
	if committer, ok := db.Statement.ConnPool.(gorm.TxCommitter); ok && committer != nil {
		if rootErr := committer.Rollback(); rootErr != nil && !errors.Is(rootErr, sql.ErrTxDone) {
			abort = errors.Join(abort, fmt.Errorf("%w: %w", utils.ErrRollbackFailed, rootErr))
		}
	}
	return abort
}

// erasureControlExec runs a transaction-control statement (SAVEPOINT, RELEASE
// SAVEPOINT, ROLLBACK TO SAVEPOINT) on the transaction behind db. It is a
// variable only so a test can make one of them fail; production never replaces
// it.
var erasureControlExec = execTransactionControl

// execTransactionControl sends stmt straight to the transaction's connection
// pool, unwrapping GORM's prepared-statement transaction the way GORM's own
// SavePoint/RollbackTo do — but without swapping the pool on the shared
// statement, and without the SQLite dialector's habit of dropping the error.
func execTransactionControl(db *gorm.DB, stmt string) error {
	pool := db.Statement.ConnPool
	if prepared, ok := pool.(*gorm.PreparedStmtTX); ok {
		pool = prepared.Tx
	}
	ctx := db.Statement.Context
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := pool.ExecContext(ctx, stmt)
	return err
}

// inTransaction reports whether db is already backed by an open transaction.
// The test is the same one GORM itself uses to decide whether Transaction has
// been called on a transaction: a connection pool that can commit and roll back
// is a transaction, an *sql.DB is not.
func inTransaction(db *gorm.DB) bool {
	if db == nil || db.Statement == nil {
		return false
	}
	committer, ok := db.Statement.ConnPool.(gorm.TxCommitter)
	return ok && committer != nil
}

// purgeCredentials hard-deletes the credentials belonging to a user so they do
// not outlive the account. An erased account whose API keys still authenticate
// is a security hole, and the tokens themselves are linked to the person.
//
// The delete is unconditional and any error propagates, which rolls the erasure
// back. Both tables belong to the schema and are created by auto-migration, so
// a failure here is a real failure — it is never evidence that the deployment
// simply has no such table. Probing for the table first and skipping it would
// be worse than useless: a probe that cannot distinguish "absent" from "the
// query failed" turns a transient database error into a silently skipped purge,
// and the transaction would still commit, leaving an anonymised user whose
// credentials still work.
func purgeCredentials(tx *gorm.DB, userID uint) error {
	credentialModels := []interface{}{&models.APIKey{}, &models.RefreshToken{}, &models.PasswordResetToken{}}
	for _, model := range credentialModels {
		if err := tx.Unscoped().Where("user_id = ?", userID).Delete(model).Error; err != nil {
			return fmt.Errorf("purging credentials of user %d: %w", userID, err)
		}
	}
	// The in-app notifications addressed to the user go with the credentials:
	// they are personal to the recipient, their title and body may render a
	// lead's name, and nothing of either may outlive the person. They share
	// this purge because every user erasure reaches it.
	return purgeUserNotifications(tx, userID)
}

// purgeUserNotifications hard-deletes every notification addressed to the
// user. A failure rolls the whole erasure back.
//
// Unlike the credential purge it first asks the migrator whether the table
// exists, and skips a table that is not part of the schema: the automation
// tables arrived in one release and a number of fixtures migrate only the
// tables they exercise. The probe is a schema fact, not an error swallowed —
// when the table is there, every failure of the delete itself propagates.
func purgeUserNotifications(tx *gorm.DB, userID uint) error {
	if !tx.Migrator().HasTable(&models.Notification{}) {
		return nil
	}
	if err := tx.Unscoped().Where("user_id = ?", userID).Delete(&models.Notification{}).Error; err != nil {
		return fmt.Errorf("purging notifications of user %d: %w", userID, err)
	}
	return nil
}

// automationTables records which of the two automation tables the schema has.
// A number of fixtures migrate only the tables they exercise, so the cancel
// skips a table that is not there (see purgeUserNotifications).
type automationTables struct {
	runs          bool
	notifications bool
}

// probeAutomationTables asks the migrator for both tables. On MySQL the probe
// is a plain SELECT on information_schema, and on MySQL 8 that select fixes
// the REPEATABLE READ snapshot of the transaction it runs on. An erasure
// therefore resolves it on the root handle, together with its discovery, and
// never on its transaction before the last graph lock (#385).
func probeAutomationTables(db *gorm.DB) automationTables {
	return automationTables{
		runs:          db.Migrator().HasTable(&models.AutomationRun{}),
		notifications: db.Migrator().HasTable(&models.Notification{}),
	}
}

// cancelAutomationForEntity ends the automation state that refers to a
// record about to be erased: every pending, waiting or running run of the
// record is cancelled with record_deleted, so no further step runs against
// it, and the run lease (owner_instance, heartbeat_at) is cleared in the same
// conditional update, as automationRepository.cancel does (#333), so a
// cancelled run never looks claimed. A run already completed, failed or
// cancelled matches the status predicate no more and is left untouched.
// Every notification whose entity is the record is hard-deleted,
// because its rendered title or body may name the person. A failure of
// either statement rolls the erasure back; a table absent from the schema
// (see purgeUserNotifications) is skipped.
//
// This form probes the schema on the handle it is given. The erasure of a
// person uses cancelAutomationInTables with the answers of its discovery.
func cancelAutomationForEntity(tx *gorm.DB, entityType string, entityID uint) error {
	return cancelAutomationInTables(tx, entityType, entityID, probeAutomationTables(tx))
}

// cancelAutomationInTables is cancelAutomationForEntity with the schema
// answers supplied by the caller: it issues writes only, no read of any kind.
func cancelAutomationInTables(tx *gorm.DB, entityType string, entityID uint, tables automationTables) error {
	now := time.Now().UTC()
	if tables.runs {
		err := tx.Model(&models.AutomationRun{}).
			Where("entity_type = ? AND entity_id = ?", entityType, entityID).
			Where("status IN ?", []string{models.AutomationRunPending, models.AutomationRunWaiting, models.AutomationRunRunning}).
			Updates(map[string]interface{}{
				"status":         models.AutomationRunCancelled,
				"cancel_reason":  models.AutomationCancelRecordDeleted,
				"finished_at":    &now,
				"owner_instance": nil,
				"heartbeat_at":   nil,
				"updated_at":     now,
			}).Error
		if err != nil {
			return fmt.Errorf("cancelling automation runs of %s %d: %w", entityType, entityID, err)
		}
	}
	if tables.notifications {
		if err := tx.Unscoped().Where("entity_type = ? AND entity_id = ?", entityType, entityID).
			Delete(&models.Notification{}).Error; err != nil {
			return fmt.Errorf("purging notifications of %s %d: %w", entityType, entityID, err)
		}
	}
	return nil
}

// scrubUserTicketComments redacts the body of ticket comments authored by this
// user to "[deleted]" so personal data in comment text is scrubbed while the
// ticket discussion history remains coherent.
//
// Unscoped: a comment soft-deleted earlier still holds its body, and a hidden
// copy of the person is still a copy. Each comment keeps its own deletion
// state; only the body changes.
func scrubUserTicketComments(tx *gorm.DB, userID uint) error {
	if err := tx.Unscoped().Model(&models.TicketComment{}).Where("author_user_id = ?", userID).Update("body", "[deleted]").Error; err != nil {
		return fmt.Errorf("scrubbing ticket comments of user %d: %w", userID, err)
	}
	return nil
}
