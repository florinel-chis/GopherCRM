import importlib.util
import json
import os
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import go_checks

spec = importlib.util.spec_from_file_location('profile_checks', Path(__file__).resolve().parents[1] / 'check-go.py')
profiles = importlib.util.module_from_spec(spec)
spec.loader.exec_module(profiles)


class GoChecksTests(unittest.TestCase):
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
            self.assertEqual(run.call_args.kwargs['known_skips'], {})
        with patch.dict(os.environ, {}, clear=True), self.assertRaises(go_checks.GateError):
            profiles.run('migration')
        with patch.dict(os.environ, TEST_MYSQL_DSN='disposable'), \
             patch.object(go_checks, 'discover', return_value={'cmd/migrate': [], 'internal/models': []}), \
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
