#!/usr/bin/env python3
"""Run a shared Go validation profile; no ticket-specific execution helpers."""

import argparse
import os
import go_checks

TRANSACTION_SOURCES = {
    'internal/utils': ('transaction*_test.go',),
    'internal/service': ('lead*_test.go', 'form*_test.go', 'deal*_test.go',
                         'company*_test.go', 'aeo*_test.go', 'bulk*_test.go'),
    'internal/repository': ('lead*_test.go', 'form*_test.go', 'deal*_test.go',
                            'company*_test.go', 'aeo*_test.go', 'bulk*_test.go'),
    'test/integration': ('lead*_test.go', 'company*_test.go', 'deal*_test.go',
                         'bulk*_test.go', 'form*_test.go', 'erasure_atomicity_test.go'),
}
MIGRATION_SOURCES = {
    'cmd/migrate': ('main_test.go', 'customer_website_test.go'),
    'internal/models': ('database_sqlite_upgrade_test.go', 'customer_website_upgrade_test.go'),
}
KNOWN_SKIPS = {
    ('github.com/florinel-chis/gophercrm/test/integration',
     'TestLeadConversionTransaction/concurrent_conversion_attempts'):
        'Existing empty SQLite concurrency placeholder; not exercised coverage.',
}


def run(profile, *, storage_only=False, root=go_checks.ROOT):
    if profile not in {'transaction', 'migration'} or (storage_only and profile != 'transaction'):
        raise go_checks.GateError('Unknown Go validation profile')
    if not os.environ.get('TEST_MYSQL_DSN'):
        raise go_checks.GateError('Database checks require a disposable TEST_MYSQL_DSN server')
    sources = ({'internal/utils': TRANSACTION_SOURCES['internal/utils']}
               if storage_only else TRANSACTION_SOURCES if profile == 'transaction' else MIGRATION_SOURCES)
    selected = go_checks.discover(sources, root=root, label=profile.title())
    required = {'TestTransactionPanicSQLite', 'TestTransactionPanicMySQL',
                'TestTransactionNestedSQLite', 'TestTransactionNestedMySQL',
                'TestTransactionCancellationRetrySQLite', 'TestTransactionCancellationRetryMySQL',
                'TestTransactionFinalizationFailures',
                'TestTransactionRecoveredNestedPanicCannotCommitFailedRollback'}
    if profile == 'transaction':
        if not required.issubset(selected['internal/utils']):
            raise go_checks.GateError('Missing required transaction boundary regressions')
    else:
        required_by_package = {
            'cmd/migrate': {'TestMigrationFresh_FullUpAndDown',
                            'TestMigrationUpgrade_FromMainAutoMigratedSchema', 'TestCustomerWebsiteMigration'},
            'internal/models': {'TestMigrateDatabase_SQLiteUpgradeFromV120',
                                'TestMigrateDatabase_SQLiteUpgradeFromHalfAppliedV120',
                                'TestMigrateDatabase_SQLiteEnforcesForeignKeysAfterUpgrade',
                                'TestCustomerWebsiteUpgradeFromSQLiteFixture'},
        }
        if any(not names.issubset(selected[package]) for package, names in required_by_package.items()):
            raise go_checks.GateError('Missing required SQL migration or SQLite upgrade regressions')
    go_checks.run(selected, root=root, label=profile.title(),
                  known_skips=KNOWN_SKIPS if profile == 'transaction' else {})


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('profile', choices=['transaction', 'migration'])
    parser.add_argument('--storage-only', action='store_true',
                        help='Only transaction boundaries; used for the additional MariaDB CI engine')
    args = parser.parse_args()
    run(args.profile, storage_only=args.storage_only)
