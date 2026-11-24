"""A green command exit must not conceal missing database coverage."""
import importlib.util
import json
from pathlib import Path
import re
import shlex
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
                    'TestProductsAndQuotesMigration',
                    'TestMigrationAutomationSchema',
                    'TestTaskDealLinkMigration',
                    'TestFormMailDeliveriesMigration',
                    'TestTicketSLAFieldsMigration',
                    'TestCustomerExternalIDsMigration',
                    'TestFormAttributionMigration',
                    'TestUserTokenVersionMigration',
                    'TestBulkOperationStatusIndexMigration',
                    'TestQuoteTaxMigration',
                    'TestQuoteQuantityMigration',
                    'TestRoleMigrationFiles',
                    'TestMigrationRolesSchema',
                    'TestMigrationRolesDownDemotesCustomUsers',
                    'TestQuoteResponseTokensMigration',
                    'TestQuoteHistorySourceMigration',
                    'TestUserIdentitiesMigration',
                    'TestAuthAuditLogMigration',
                    'TestAuthAuditLogAttemptsMigrationFiles',
                    'TestAuthAuditLogAttemptsMigration',
                    'TestSCIMTokensMigrationFiles',
                    'TestSCIMTokensMigration',
                    'TestWebhookSecretRotationMigrationFiles',
                    'TestWebhookSecretRotationMigration',
                    'TestSCIMGroupMembersMigrationFiles',
                    'TestSCIMGroupMembersMigration',
                    'TestAdminOnlyPermissionsMigrationFiles',
                    'TestAdminOnlyPermissionsMigration',
                    'TestAPIKeyRevokedAtMigrationFiles',
                    'TestAPIKeyRevokedAtMigration',
                    'TestAPIKeyLifetimeMigrationFiles',
                    'TestAPIKeyLifetimeMigration',
                    'TestRefreshTokenSessionMigrationFiles',
                    'TestRefreshTokenSessionMigration',
                    'TestWebhookEndpointPauseMigrationFiles',
                    'TestWebhookEndpointPauseMigration',
                    'TestAuthAuditLogAPIKeyIDMigrationFiles',
                    'TestAuthAuditLogAPIKeyIDMigration',
                    'TestWebhookEventTypesWidthMigrationFiles',
                    'TestWebhookEventTypesWidthMigration',
                    'TestRefreshTokenProviderSessionMigrationFiles',
                    'TestRefreshTokenProviderSessionMigration',
                    'TestSchemaParityAllowList',
                    'TestSchemaParityMigration',
                ],
                'internal/models': [
                    'TestMigrateDatabase_SQLiteUpgradeFromV120',
                    'TestMigrateDatabase_SQLiteUpgradeFromHalfAppliedV120',
                    'TestMigrateDatabase_SQLiteEnforcesForeignKeysAfterUpgrade',
                    'TestCustomerWebsiteUpgradeFromSQLiteFixture',
                    'TestTaskDealLinkSQLiteUpgradeFromV120',
                    'TestRoleSQLiteUpgradeFromV120',
                    'TestQuoteHistorySourceSQLiteUpgrade',
                    'TestWebhookSecretRotationSQLiteUpgrade',
                    'TestWebhookEndpointPauseSQLiteUpgrade',
                ],
                'internal/repository': [
                    'TestLookupIndexesServeLookupsSQLite',
                    'TestPaginationDeterminismSQLite',
                    'TestPaginationDeterminismMySQL',
                    'TestCompanyDomainNormalizationSQLite',
                    'TestCompanyDomainNormalizationMySQL',
                    'TestSystemRoleSeedSQLite',
                    'TestSystemRoleSeedMySQL',
                    'TestAdminOnlyPermissionStripSQLite',
                    'TestAdminOnlyPermissionStripMySQL',
                    'TestAPIKeyLifetimeStepSQLite',
                    'TestAPIKeyLifetimeStepMySQL',
                    'TestRefreshTokenSessionStepSQLite',
                    'TestRefreshTokenSessionStepMySQL',
                ],
            }.items() for name in names
        ]
        self.events.extend({'Action': 'pass', 'Package': PREFIX + package}
                           for package in ['cmd/migrate', 'internal/models', 'internal/repository'])

    def check(self):
        return sql.check('\n'.join(json.dumps(event) for event in self.events), ROOT,
                         migrations_only=True)

    def test_accepts_complete_sql_and_sqlite_evidence(self):
        self.assertEqual(len(self.check()), 70)

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


# The ./cmd/migrate race run must fit on a slow runner. Release run 45154161780: the slow attempt
# took 2.4x as long (565 s against 239 s for the roots before TestWebhookEventTypesWidthMigration),
# so the slow-runner package total is about 615-625 s; 2.5x that is about 1560 s (26 min).
MIN_MIGRATE_GO_TIMEOUT_SECONDS = 1560
# The job limit must outlive the Go timeout so Go writes its goroutine dump and the later
# coverage and artifact steps still run instead of being cancelled with the job.
MIN_JOB_HEADROOM_SECONDS = 300
GO_DURATION = re.compile(r'(?:\d+(?:\.\d+)?[hms])+')
GO_DURATION_PART = re.compile(r'(\d+(?:\.\d+)?)([hms])')
JOB_KEY = re.compile(r'^  [A-Za-z0-9_-]+:\s*$')


def go_duration_seconds(value):
    if not GO_DURATION.fullmatch(value):
        raise ValueError(f'unsupported Go duration {value!r}; use h/m/s units such as 30m or 1h5m')
    units = {'h': 3600, 'm': 60, 's': 1}
    return sum(float(number) * units[unit] for number, unit in GO_DURATION_PART.findall(value))


def job_line_range(lines, job):
    start = next((i for i, line in enumerate(lines) if line.rstrip() == f'  {job}:'), None)
    if start is None:
        return None
    end = next((i for i in range(start + 1, len(lines)) if JOB_KEY.match(lines[i])), len(lines))
    return start, end


def migrate_commands(lines):
    """Return (first line index, joined command) for every go test line naming ./cmd/migrate."""
    commands = []
    i = 0
    while i < len(lines):
        first = i
        text = lines[i]
        while text.rstrip().endswith('\\') and i + 1 < len(lines):
            i += 1
            text = text.rstrip()[:-1] + ' ' + lines[i].strip()
        if 'go test' in text and './cmd/migrate' in text.split():
            commands.append((first, text.strip()))
        i += 1
    return commands


class MigrateWorkflowTimeoutTests(unittest.TestCase):
    WORKFLOWS = (
        ('.github/workflows/migrations.yml', 'migrations'),
        ('.github/workflows/release.yml', 'sql-validation'),
    )

    def test_go_duration_parser(self):
        self.assertEqual(go_duration_seconds('30m'), 1800)
        self.assertEqual(go_duration_seconds('1h5m'), 3900)
        self.assertEqual(go_duration_seconds('90s'), 90)
        for bad in ('30', '30min', '', '1d', '-5m'):
            with self.assertRaises(ValueError, msg=bad):
                go_duration_seconds(bad)

    def test_migrate_runs_have_go_timeout_and_job_headroom(self):
        for path, job in self.WORKFLOWS:
            with self.subTest(workflow=path, job=job):
                self.check_workflow(path, job)

    def check_workflow(self, path, job):
        lines = (ROOT / path).read_text().splitlines()
        span = job_line_range(lines, job)
        self.assertIsNotNone(span, f'{path}: job {job!r} not found')
        start, end = span

        commands = migrate_commands(lines)
        self.assertEqual(len(commands), 1,
                         f'{path}: expected exactly one go test command naming ./cmd/migrate, '
                         f'found {len(commands)}')
        line_index, command = commands[0]
        self.assertTrue(start < line_index < end,
                        f'{path}: the ./cmd/migrate go test command is outside job {job!r}')

        args = shlex.split(command)
        values = []
        for index, arg in enumerate(args):
            if arg == '-timeout':
                values.append(args[index + 1] if index + 1 < len(args) else '')
            elif arg.startswith('-timeout='):
                values.append(arg[len('-timeout='):])
        self.assertEqual(len(values), 1,
                         f'{path}: the ./cmd/migrate go test command must pass exactly one '
                         f'-timeout flag, found {len(values)} (without one Go defaults to 10m)')
        try:
            go_timeout = go_duration_seconds(values[0])
        except ValueError as error:
            self.fail(f'{path}: {error}')
        self.assertGreaterEqual(
            go_timeout, MIN_MIGRATE_GO_TIMEOUT_SECONDS,
            f'{path}: -timeout {values[0]} is below 26m (2.5x the slow-runner package total)')

        limits = [re.fullmatch(r'    timeout-minutes:\s*(\d+)\s*', line)
                  for line in lines[start:end]]
        limits = [match for match in limits if match]
        self.assertEqual(len(limits), 1, f'{path}: job {job!r} must set one timeout-minutes')
        job_seconds = int(limits[0].group(1)) * 60
        self.assertGreaterEqual(
            job_seconds, go_timeout + MIN_JOB_HEADROOM_SECONDS,
            f'{path}: job {job!r} timeout-minutes must exceed the Go -timeout by at least 5 minutes')


if __name__ == '__main__':
    unittest.main()
