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
// The scrub and the soft delete always run in ONE transaction. A crash between
// the two steps would otherwise leave a live, still-listed, already-anonymised
// account behind — the worst of both worlds.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"reflect"

	"github.com/florinel-chis/gophercrm/internal/models"
	"gorm.io/gorm"
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
// only thing that differs between users, customers, leads and the bulk paths —
// the transaction handling, the placeholder generation and the ordering of the
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
}

// newModel returns a fresh zero pointer of the plan's entity type. Every
// statement gets its own so that GORM writing back into the destination struct
// of one statement can never influence the next.
func (p erasurePlan) newModel() interface{} {
	return reflect.New(reflect.TypeOf(p.Model).Elem()).Interface()
}

// eraseRecord performs a GDPR Article 17 erasure of a single row: it overwrites
// the personal-data columns named by the plan, runs the plan's extra in-place
// clean-up, and only then soft-deletes the row — all atomically.
//
// It runs inside a transaction, but starts one only if db is not already backed
// by one. Repositories expose WithTx, so db may well BE a transaction handle
// already, and calling Begin on a transaction is invalid; when that is the case
// the caller's transaction is used and the caller keeps control of the commit.
// Either way the scrub and the soft delete cannot be separated by a failure.
func eraseRecord(db *gorm.DB, id uint, plan erasurePlan) error {
	return runInTransaction(db, func(tx *gorm.DB) error {
		erased := make(map[string]interface{}, len(plan.Scrub)+1)
		for column, value := range plan.Scrub {
			erased[column] = value
		}

		if plan.EmailColumn != "" {
			placeholder, err := newAnonymizedEmail()
			if err != nil {
				return err
			}
			erased[plan.EmailColumn] = placeholder
		}

		// Unscoped so that re-running an erasure over an already soft-deleted
		// row — a legacy row soft-deleted before deletion became an erasure, and
		// therefore still holding personal data — still scrubs it.
		if err := tx.Unscoped().Model(plan.newModel()).Where("id = ?", id).Updates(erased).Error; err != nil {
			return err
		}

		if plan.AfterScrub != nil {
			if err := plan.AfterScrub(tx, id); err != nil {
				return err
			}
		}

		return tx.Delete(plan.newModel(), id).Error
	})
}

// runInTransaction executes fn against a transactional handle, starting a
// transaction only when db is not already one.
//
// This exists because every repository implements WithTx(tx) as part of its
// published interface: the *gorm.DB a repository holds may be the database
// handle or an open transaction, and the two need opposite treatment. Beginning
// a transaction on a handle that is already one is invalid, while running a
// multi-statement erasure without any transaction at all would sacrifice
// atomicity.
func runInTransaction(db *gorm.DB, fn func(tx *gorm.DB) error) error {
	if inTransaction(db) {
		return fn(db)
	}
	return db.Transaction(fn)
}

// runIsolated executes fn as a unit of work that either takes effect entirely
// or leaves the database exactly as it found it — WITHOUT disturbing the work
// the caller has already done in the same transaction, and without taking the
// caller's commit decision away.
//
// This is the one thing runInTransaction deliberately does not provide.
// runInTransaction JOINS an open transaction, which is right for a single
// erasure composed into a caller's unit of work: the caller owns the commit, and
// the erasure must live or die with everything else the caller did. It is wrong
// for one ITEM of a batch, because a batch reports per-item failures and CARRIES
// ON. A joined item that fails half way through leaves the statements it already
// issued sitting on the shared transaction, and the batch — having reported the
// item as a failure and returned no error of its own — commits them. What is
// left is the state the whole of this file exists to prevent: a live,
// still-listed account with a placeholder address, no name, an unusable password
// and no credentials, whose owner was told the deletion did not happen. Neither
// intact nor erased is worse than either.
//
// gorm.DB.Transaction already draws exactly the boundary an item needs: BEGIN /
// COMMIT / ROLLBACK when db is a plain handle, and SAVEPOINT / ROLLBACK TO when
// db is already a transaction, so a failing item rolls back to its own savepoint
// and its neighbours — before it and after it — are untouched. Savepoints are
// supported by both engines this project runs on (InnoDB and SQLite) and by both
// GORM drivers it uses.
//
// Nested transactions can be switched off globally in the GORM config, which
// would silently turn the savepoint back into a bare join and reinstate the
// defect. That is refused rather than downgraded: an erasure that cannot be
// isolated must not run.
func runIsolated(db *gorm.DB, fn func(tx *gorm.DB) error) error {
	if inTransaction(db) && db.DisableNestedTransaction {
		return fmt.Errorf("erasure cannot be isolated: it is running inside a transaction " +
			"but nested transactions (savepoints) are disabled")
	}
	return db.Transaction(fn)
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
	credentialModels := []interface{}{&models.APIKey{}, &models.RefreshToken{}}
	for _, model := range credentialModels {
		if err := tx.Unscoped().Where("user_id = ?", userID).Delete(model).Error; err != nil {
			return fmt.Errorf("purging credentials of user %d: %w", userID, err)
		}
	}
	return nil
}
