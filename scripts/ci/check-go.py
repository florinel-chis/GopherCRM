#!/usr/bin/env python3
"""Run a shared Go validation profile; no ticket-specific execution helpers."""

import argparse
import os
import go_checks
from gophercrm_runner.validation import AEO_REQUIRED_ROOTS, MAILER_REQUIRED_ROOTS

MAILER_SOURCES = {
    'internal/mailer': ('*_test.go',),
    'internal/service': ('auth_session_test.go', 'form_service_test.go', 'quote_email_test.go',
                         'automation_step_execution_test.go', 'automation_log_privacy_test.go'),
    'internal/utils': ('log_error_test.go',),
}
# Only the mail-delivery callers in these larger test files are affected by a
# transport-only change. Missing or renamed roots must fail before Go execution.
MAILER_CALLER_ROOTS = {
    'TestAuthService_RequestPasswordReset',
    'TestFormServiceSubmitCreatesLeadAndNotifies',
    'TestFormServiceSubmitSendsImmediateFollowUpWhenNotOptIn',
    'TestFormServiceSubmitSurvivesMailFailure',
    'TestFormServiceOptInDefersEverythingToConfirmation',
    'TestFormServiceConfirmCompletesTheSubmission',
    'TestFormServiceConfirmationMailUsesTheFormsOwnCopy',
    'TestFormServiceConfirmationMailAppendsAMissingPlaceholder',
    'TestQuoteEmailDelivery',
    'TestQuoteEmailLogsOmitPersonalData',
    'TestQuoteEmailRevisedDuringSend',
    # The SQLite half covers send_email; the MySQL twin needs a SQL server this profile omits.
    'TestAutomationStepExecutionSQLite',
    'TestAutomationLogsOmitPersonalData',
}

TRANSACTION_SOURCES = {
    'internal/utils': ('transaction*_test.go',),
    'internal/service': ('lead*_test.go', 'form*_test.go', 'deal*_test.go',
                         'company*_test.go', 'aeo*_test.go', 'bulk*_test.go'),
    'internal/repository': ('lead*_test.go', 'form*_test.go', 'deal*_test.go',
                            'company*_test.go', 'aeo*_test.go', 'bulk*_test.go'),
    'test/integration': ('lead*_test.go', 'company*_test.go', 'deal*_test.go',
                         'bulk*_test.go', 'form*_test.go', 'erasure_atomicity_test.go'),
}
AEO_SOURCES = {
    'cmd': ('*_test.go',),
    'internal/aeo': ('*_test.go',),
    'internal/service': ('aeo*_test.go',),
    'internal/repository': ('aeo*_test.go',),
    'internal/handler': ('aeo*_test.go',),
    'internal/config': ('*_test.go',),
    'internal/models': ('aeo*_test.go',),
    'internal/utils': ('transaction*_test.go',),
}
MIGRATION_SOURCES = {
    'cmd/migrate': ('main_test.go', 'customer_website_test.go', 'lookup_indexes_migration_test.go',
                    'products_quotes_migration_test.go', 'automation_migration_test.go'),
    'internal/models': ('database_sqlite_upgrade_test.go', 'customer_website_upgrade_test.go'),
    'internal/repository': ('lookup_index_plan_test.go', 'pagination_determinism_test.go',
                            'company_domain_normalization_test.go'),
}
KNOWN_SKIPS = {
    ('github.com/florinel-chis/gophercrm/test/integration',
     'TestLeadConversionTransaction/concurrent_conversion_attempts'):
        'Existing empty SQLite concurrency placeholder; not exercised coverage.',
}


def run(profile, *, storage_only=False, root=go_checks.ROOT):
    if profile not in {'transaction', 'migration', 'aeo', 'mailer'} or (storage_only and profile != 'transaction'):
        raise go_checks.GateError('Unknown Go validation profile')
    if profile == 'mailer':
        selected = go_checks.discover(MAILER_SOURCES, root=root, label='Mailer')
        if (not MAILER_REQUIRED_ROOTS.issubset(selected.get('internal/mailer', ())) or
                not MAILER_CALLER_ROOTS.issubset(selected.get('internal/service', ()))):
            raise go_checks.GateError('Missing required SMTP boundary or mail caller regressions')
        selected['internal/service'] = sorted(MAILER_CALLER_ROOTS)
        go_checks.run(selected, root=root, label='Mailer')
        return
    if not os.environ.get('TEST_MYSQL_DSN'):
        raise go_checks.GateError('Database checks require a disposable TEST_MYSQL_DSN server')
    sources = ({'internal/utils': TRANSACTION_SOURCES['internal/utils']}
               if storage_only else TRANSACTION_SOURCES if profile == 'transaction' else AEO_SOURCES if profile == 'aeo' else MIGRATION_SOURCES)
    selected = go_checks.discover(sources, root=root, label=profile.title())
    required = {'TestTransactionPanicSQLite', 'TestTransactionPanicMySQL',
                'TestTransactionNestedSQLite', 'TestTransactionNestedMySQL',
                'TestTransactionCancellationRetrySQLite', 'TestTransactionCancellationRetryMySQL',
                'TestTransactionFinalizationFailures',
                'TestTransactionRecoveredNestedPanicCannotCommitFailedRollback'}
    if profile in {'transaction', 'aeo'}:
        if not required.issubset(selected['internal/utils']):
            raise go_checks.GateError('Missing required transaction boundary regressions')
    else:
        required_by_package = {
            'cmd/migrate': {'TestMigrationFresh_FullUpAndDown',
                            'TestMigrationUpgrade_FromMainAutoMigratedSchema', 'TestCustomerWebsiteMigration',
                            'TestLookupIndexesMigration', 'TestProductsAndQuotesMigration',
                            'TestMigrationAutomationSchema'},
            'internal/models': {'TestMigrateDatabase_SQLiteUpgradeFromV120',
                                'TestMigrateDatabase_SQLiteUpgradeFromHalfAppliedV120',
                                'TestMigrateDatabase_SQLiteEnforcesForeignKeysAfterUpgrade',
                                'TestCustomerWebsiteUpgradeFromSQLiteFixture'},
            'internal/repository': {'TestLookupIndexesServeLookupsSQLite',
                                    'TestPaginationDeterminismSQLite', 'TestPaginationDeterminismMySQL',
                                    'TestCompanyDomainNormalizationSQLite',
                                    'TestCompanyDomainNormalizationMySQL'},
        }
        if any(not names.issubset(selected.get(package, [])) for package, names in required_by_package.items()):
            raise go_checks.GateError('Missing required SQL migration or SQLite upgrade regressions')
    if profile == 'aeo' and any(not names.issubset(selected.get(package, []))
                                for package, names in AEO_REQUIRED_ROOTS.items()):
        raise go_checks.GateError('Missing required AEO startup/shutdown or database regressions')
    go_checks.run(selected, root=root, label=profile.title(),
                  known_skips=KNOWN_SKIPS if profile == 'transaction' else {})


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('profile', choices=['transaction', 'migration', 'aeo', 'mailer'])
    parser.add_argument('--storage-only', action='store_true',
                        help='Only transaction boundaries; used for the additional MariaDB CI engine')
    args = parser.parse_args()
    run(args.profile, storage_only=args.storage_only)
