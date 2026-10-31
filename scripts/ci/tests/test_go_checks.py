import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import go_checks

spec = importlib.util.spec_from_file_location('profile_checks', Path(__file__).resolve().parents[1] / 'check-go.py')
profiles = importlib.util.module_from_spec(spec)
spec.loader.exec_module(profiles)


class GoChecksTests(unittest.TestCase):
    def test_go_processes_isolate_application_database_settings_and_keep_fixture_dsn(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            executable = root / 'go'
            executable.write_text('#!' + sys.executable + '\n' + '''
import json, os, sys
from pathlib import Path
with Path('calls.jsonl').open('a') as output:
    output.write(json.dumps({'command': sys.argv[1:], 'env': dict(os.environ)}) + '\\n')
if sys.argv[1] == 'list':
    print('example/fixture')
elif sys.argv[1] == 'test':
    for event in [{'Action': 'pass', 'Package': 'example/fixture', 'Test': 'TestBoundary'},
                  {'Action': 'pass', 'Package': 'example/fixture'}]:
        print(json.dumps(event))
''')
            executable.chmod(0o755)
            environment = dict(PATH=str(root), TEST_MYSQL_DSN='disposable-fixture',
                               JWT_SECRET='test-only-secret', GOCACHE='test-cache')
            for key in ('DB_HOST', 'DB_PORT', 'DB_USER', 'DB_PASSWORD', 'DB_NAME',
                        'DB_DRIVER', 'DB_PATH'):
                environment[key] = 'application-setting'
            with patch.dict(os.environ, environment, clear=True):
                before = dict(os.environ)
                go_checks.run({'fixture': ['TestBoundary']}, root=root)
                self.assertEqual(dict(os.environ), before)
            calls = [json.loads(line) for line in (root / 'calls.jsonl').read_text().splitlines()]
            self.assertEqual([call['command'][0] for call in calls], ['build', 'vet', 'list', 'test'])
            for call in calls:
                with self.subTest(command=call['command'][0]):
                    self.assertFalse(any(key.startswith('DB_') for key in call['env']))
                    for key in ('TEST_MYSQL_DSN', 'JWT_SECRET', 'GOCACHE'):
                        self.assertEqual(call['env'][key], environment[key])

    def test_transaction_selection_covers_boundaries_and_callers(self):
        selected = go_checks.discover(profiles.TRANSACTION_SOURCES)
        self.assertIn('TestTransactionRecoveredNestedPanicCannotCommitFailedRollback', selected['internal/utils'])
        for package in ('internal/service', 'internal/repository'):
            for feature in ('Lead', 'Form', 'Deal', 'Company', 'AEO', 'Bulk'):
                self.assertTrue(any(feature in name for name in selected[package]), (package, feature))
        self.assertIn('TestLeadConversionTransaction', selected['test/integration'])

    def test_missing_database_or_boundary_cannot_reduce_profile(self):
        with patch.dict(os.environ, {}, clear=True), self.assertRaises(go_checks.GateError):
            profiles.run('transaction')
        with patch.dict(os.environ, TEST_MYSQL_DSN='disposable'), \
             patch.object(go_checks, 'discover', return_value={'internal/utils': ['TestOld']}), \
             self.assertRaises(go_checks.GateError):
            profiles.run('transaction')

    def test_migration_profile_requires_sql_and_sqlite_fixtures(self):
        with patch.dict(os.environ, TEST_MYSQL_DSN='disposable'), \
             patch.object(go_checks, 'run') as run:
            profiles.run('migration')
            selected = run.call_args.args[0]
            self.assertIn('TestCustomerWebsiteMigration', selected['cmd/migrate'])
            self.assertIn('TestMigrateDatabase_SQLiteEnforcesForeignKeysAfterUpgrade', selected['internal/models'])
            self.assertIn('TestLookupIndexesMigration', selected['cmd/migrate'])
            self.assertIn('TestProductsAndQuotesMigration', selected['cmd/migrate'])
            self.assertIn('TestMigrationAutomationSchema', selected['cmd/migrate'])
            for name in ('TestLookupIndexesServeLookupsSQLite', 'TestPaginationDeterminismSQLite',
                         'TestPaginationDeterminismMySQL', 'TestCompanyDomainNormalizationSQLite',
                         'TestCompanyDomainNormalizationMySQL'):
                self.assertIn(name, selected['internal/repository'])
            self.assertEqual(run.call_args.kwargs['known_skips'], {})
        with patch.dict(os.environ, {}, clear=True), self.assertRaises(go_checks.GateError):
            profiles.run('migration')
        with patch.dict(os.environ, TEST_MYSQL_DSN='disposable'), \
             patch.object(go_checks, 'discover', return_value={'cmd/migrate': [], 'internal/models': [], 'internal/repository': []}), \
             self.assertRaises(go_checks.GateError):
            profiles.run('migration')
        with self.assertRaises(go_checks.GateError):
            profiles.run('migration', storage_only=True)

    def test_only_exact_known_placeholder_is_exempt_and_completion_required(self):
        module, root = 'example/pkg', 'TestBoundary'
        def event(action, name=None):
            return json.dumps({'Package': module, 'Test': name, 'Action': action}) + '\n'
        complete = event('pass', root) + event('pass')
        known = {(module, root + '/old'): 'Documented pre-existing gap'}
        go_checks.verify(event('skip', root + '/old') + complete, module, [root], known)
        for output in [event('skip', root + '/new') + complete,
                       event('skip', root) + event('pass'),
                       event('fail', root + '/old') + complete,
                       event('pass', root), complete + event('pass')]:
            with self.subTest(output=output), self.assertRaises(go_checks.GateError):
                go_checks.verify(output, module, [root], known)

    def test_aeo_selection_covers_runtime_security_and_callers_and_requires_all_roots(self):
        selected = go_checks.discover(profiles.AEO_SOURCES)
        self.assertIn('TestAEOServiceSuite', selected['internal/service'])
        self.assertIn('TestAEORepository_RunLifecycle', selected['internal/repository'])
        self.assertIn('TestAEOHandlerTestSuite', selected['internal/handler'])
        self.assertIn('TestHealthPayloadReportsTheBuildRevision', selected['cmd'])
        self.assertTrue(selected['internal/aeo'])
        self.assertTrue(selected['internal/config'])
        with patch.dict(os.environ, {}, clear=True), self.assertRaises(go_checks.GateError):
            profiles.run('aeo')
        without_runtime = dict(selected, cmd=['TestHealthPayloadReportsTheBuildRevision'])
        with patch.dict(os.environ, TEST_MYSQL_DSN='disposable'), \
             patch.object(go_checks, 'discover', return_value=without_runtime), \
             self.assertRaisesRegex(go_checks.GateError, 'Missing required AEO'):
            profiles.run('aeo')
        for package, names in profiles.AEO_REQUIRED_ROOTS.items():
            selected[package] = sorted(set(selected[package]) | names)
        with patch.dict(os.environ, TEST_MYSQL_DSN='disposable'), \
             patch.object(go_checks, 'discover', return_value=selected), patch.object(go_checks, 'run') as run:
            profiles.run('aeo')
            self.assertEqual(run.call_args.kwargs['known_skips'], {})
        for package, required in profiles.AEO_REQUIRED_ROOTS.items():
            for name in required:
                missing = dict(selected, **{package: [n for n in selected[package] if n != name]})
                with patch.dict(os.environ, TEST_MYSQL_DSN='disposable'), \
                     patch.object(go_checks, 'discover', return_value=missing), self.assertRaises(go_checks.GateError):
                    profiles.run('aeo')
