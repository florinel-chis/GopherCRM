"""A green command exit must not conceal missing database coverage."""
import importlib.util
import json
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[3]
spec = importlib.util.spec_from_file_location('sql_coverage', ROOT / 'scripts/release/check-sql-tests.py')
sql = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sql)
PREFIX = 'github.com/florinel-chis/gophercrm/'


class MigrationCoverageTests(unittest.TestCase):
    def setUp(self):
        self.events = [
            {'Action': 'pass', 'Package': PREFIX + package, 'Test': name}
            for package, names in {
                'cmd/migrate': [
                    'TestMigrationFresh_FullUpAndDown',
                    'TestMigrationUpgrade_FromMainAutoMigratedSchema',
                    'TestCustomerWebsiteMigration',
                    'TestLookupIndexesMigration',
                ],
                'internal/models': [
                    'TestMigrateDatabase_SQLiteUpgradeFromV120',
                    'TestMigrateDatabase_SQLiteUpgradeFromHalfAppliedV120',
                    'TestMigrateDatabase_SQLiteEnforcesForeignKeysAfterUpgrade',
                    'TestCustomerWebsiteUpgradeFromSQLiteFixture',
                ],
                'internal/repository': [
                    'TestLookupIndexesServeLookupsSQLite',
                    'TestPaginationDeterminismSQLite',
                    'TestPaginationDeterminismMySQL',
                    'TestCompanyDomainNormalizationSQLite',
                    'TestCompanyDomainNormalizationMySQL',
                ],
            }.items() for name in names
        ]
        self.events.extend({'Action': 'pass', 'Package': PREFIX + package}
                           for package in ['cmd/migrate', 'internal/models', 'internal/repository'])

    def check(self):
        return sql.check('\n'.join(json.dumps(event) for event in self.events), ROOT,
                         migrations_only=True)

    def test_accepts_complete_sql_and_sqlite_evidence(self):
        self.assertEqual(len(self.check()), 13)

    def test_unavailable_database_cannot_pass_as_skips(self):
        for event in self.events:
            if event.get('Test') and event['Package'].endswith('/cmd/migrate'):
                event['Action'] = 'skip'
        with self.assertRaisesRegex(ValueError, 'skipped'):
            self.check()

    def test_missing_populated_upgrade_root_fails(self):
        self.events = [event for event in self.events
                       if event.get('Test') != 'TestCustomerWebsiteMigration']
        with self.assertRaisesRegex(ValueError, 'TestCustomerWebsiteMigration'):
            self.check()

    def test_missing_sqlite_upgrade_fails(self):
        self.events = [event for event in self.events
                       if event.get('Test') != 'TestMigrateDatabase_SQLiteUpgradeFromHalfAppliedV120']
        with self.assertRaisesRegex(ValueError, 'HalfAppliedV120'):
            self.check()

    def test_missing_lookup_index_roots_fail(self):
        for name in ('TestLookupIndexesMigration', 'TestLookupIndexesServeLookupsSQLite'):
            with self.subTest(name=name):
                kept = self.events
                self.events = [event for event in kept if event.get('Test') != name]
                with self.assertRaisesRegex(ValueError, name):
                    self.check()
                self.events = kept

    def test_missing_repository_sql_roots_fail(self):
        for name in ('TestPaginationDeterminismMySQL', 'TestPaginationDeterminismSQLite',
                     'TestCompanyDomainNormalizationMySQL', 'TestCompanyDomainNormalizationSQLite'):
            with self.subTest(name=name):
                kept = self.events
                self.events = [event for event in kept if event.get('Test') != name]
                with self.assertRaisesRegex(ValueError, name):
                    self.check()
                self.events = kept

    def test_skipped_mysql_pagination_root_fails(self):
        for event in self.events:
            if event.get('Test') == 'TestPaginationDeterminismMySQL':
                event['Action'] = 'skip'
        with self.assertRaisesRegex(ValueError, 'skipped'):
            self.check()

    def test_skipped_lookup_index_migration_fails(self):
        for event in self.events:
            if event.get('Test') == 'TestLookupIndexesMigration':
                event['Action'] = 'skip'
        with self.assertRaisesRegex(ValueError, 'skipped'):
            self.check()

    def test_skipped_or_failed_descendant_fails_even_with_passed_parent(self):
        for action in ('skip', 'fail'):
            with self.subTest(action=action):
                self.events.append({'Action': action, 'Package': PREFIX + 'cmd/migrate',
                                    'Test': 'TestCustomerWebsiteMigration/AutoMigrateFirst=true'})
                with self.assertRaisesRegex(ValueError, 'failed or skipped'):
                    self.check()
                self.events.pop()

    def test_wrong_package_cannot_supply_a_missing_root(self):
        self.events[0]['Package'] = PREFIX + 'unrelated'
        with self.assertRaisesRegex(ValueError, 'missing'):
            self.check()

    def test_duplicate_required_pass_is_rejected(self):
        self.events.append(dict(self.events[0]))
        with self.assertRaisesRegex(ValueError, 'Duplicate'):
            self.check()

    def test_truncated_or_failed_package_is_rejected(self):
        self.events.pop()
        with self.assertRaisesRegex(ValueError, 'finish successfully'):
            self.check()
        self.events.append({'Action': 'fail', 'Package': PREFIX + 'internal/models'})
        with self.assertRaisesRegex(ValueError, 'failed or skipped'):
            self.check()


if __name__ == '__main__':
    unittest.main()
