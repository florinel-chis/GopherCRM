from dataclasses import replace
import os
from types import SimpleNamespace
from unittest.mock import patch

import test_runner as fixtures
from gophercrm_runner.config import CommandError, GateError
from gophercrm_runner.engine import Runner


class ReadinessTests(fixtures.EngineTests):
    async def test_doctor_only_uses_readonly_probes_and_reports_unknown_protection(self):
        original = self.commands.run

        async def metadata_unavailable(args, **kwargs):
            if tuple(args[:2]) == ('gh', 'api'):
                return 1, 'Not available'
            return await original(args, **kwargs)

        self.commands.run = metadata_unavailable
        report = await self.runner.doctor()
        self.assertTrue(report['ready'])
        self.assertEqual(report['branch_protection']['status'], 'unknown')
        self.assertIn('claude', report['versions'])
        self.assertEqual((self.commands.implementations, self.commands.creates), (0, 0))
        self.assertFalse((self.config.state_dir / 'worktrees').exists())
        self.assertFalse(any(c[:2] in {('git', 'fetch'), ('git', 'push')} for c in self.commands.calls))

    async def test_missing_tools_or_auth_stop_before_worktree_or_paid_call(self):
        original = self.commands.run
        for broken in [('node', '--version'), ('claude', 'auth', 'status', '--json'), ('gh', 'auth', 'status')]:
            async def failure(args, **kwargs):
                if tuple(args) == broken:
                    return 1, 'Unavailable'
                return await original(args, **kwargs)
            self.commands.run = failure
            with self.subTest(probe=broken), self.assertRaisesRegex(GateError, 'readiness|environment'):
                await self.runner.run_issue(95, execute=True)
            self.assertFalse((self.config.state_dir / 'worktrees').exists())
            self.assertEqual(self.runner.store.read('active'), {})
        self.assertEqual(self.commands.implementations, 0)

    async def test_disk_capacity_is_checked_before_implementation(self):
        with patch('shutil.disk_usage', return_value=SimpleNamespace(free=0)):
            with self.assertRaisesRegex(GateError, 'disk'):
                await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.implementations, 0)
        self.assertFalse((self.config.state_dir / 'worktrees').exists())

    async def test_database_checks_require_explicit_disposable_ack_browser_and_probe(self):
        config = replace(self.config, database_test_commands=(('make', 'e2e', 'E2E_DB_DRIVER=mysql'),))
        with patch.dict(os.environ, {'TEST_MYSQL_DSN': 'private-dsn'}):
            with self.assertRaisesRegex(GateError, 'disposable'):
                await Runner(config, self.commands).doctor()
            config = replace(config, disposable_database=True)
            with self.assertRaisesRegex(GateError, 'Chromium'):
                await Runner(config, self.commands).doctor()
            browser = self.root / 'chromium'
            browser.write_text('fixture'); browser.chmod(0o700)
            config = replace(config, chromium_path=str(browser))
            original = self.commands.run

            async def unreachable(args, **kwargs):
                if tuple(args) == ('go', 'run', './automation/langflow/probes/mysql'):
                    return 1, 'Database probe failed'
                return await original(args, **kwargs)

            self.commands.run = unreachable
            with self.assertRaisesRegex(GateError, 'database'):
                await Runner(config, self.commands).doctor()
        self.assertEqual(self.commands.implementations, 0)

    async def test_verification_isolated_from_application_db_environment(self):
        original = self.commands.run
        captured = []

        async def capture(args, **kwargs):
            if tuple(args) == ('make', 'verify'):
                captured.append(kwargs.get('env'))
            return await original(args, **kwargs)

        self.commands.run = capture
        with patch.dict(os.environ, {'DB_HOST': 'wrong.example', 'DB_PORT': '13308',
                                     'TEST_MYSQL_DSN': 'private-dsn'}):
            await self.runner.run_issue(95, execute=True)
        self.assertIsNotNone(captured[0])
        self.assertNotIn('DB_HOST', captured[0])
        self.assertNotIn('DB_PORT', captured[0])
        self.assertEqual(captured[0]['TEST_MYSQL_DSN'], 'private-dsn')
        self.assertFalse(any(c[:2] == ('npm', 'ci') for c in self.commands.calls))

    async def test_environment_failure_does_not_spend_a_code_repair(self):
        original = self.commands.run

        async def no_space(args, **kwargs):
            if tuple(args) == ('make', 'verify'):
                raise CommandError('npm ERR! code ENOSPC: no space left on device')
            return await original(args, **kwargs)

        self.commands.run = no_space
        with self.assertRaisesRegex(GateError, 'environment'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.implementations, 1)
        self.assertEqual(self.runner.store.read('issue-95')['repairs'], 0)

    async def test_missing_migration_test_names_fail_readiness(self):
        browser = self.root / 'chromium'
        browser.write_text('fixture'); browser.chmod(0o700)
        config = replace(self.config, disposable_database=True, chromium_path=str(browser),
                         database_test_commands=(('go', 'test', '-json', './cmd/migrate'),))
        original = self.commands.run

        async def missing_test(args, **kwargs):
            if tuple(args[:2]) == ('go', 'test'):
                return 0, 'TestMigrationFresh_FullUpAndDown\n'
            return await original(args, **kwargs)

        self.commands.run = missing_test
        with patch.dict(os.environ, {'TEST_MYSQL_DSN': 'private-dsn'}):
            with self.assertRaisesRegex(GateError, 'migration.*test'):
                await Runner(config, self.commands).doctor()

    async def test_disk_is_rechecked_before_fresh_validation(self):
        original = self.commands.run

        async def fill_disk(args, **kwargs):
            result = await original(args, **kwargs)
            if args[0] == 'claude' and '-p' in args and 'Edit' in args[args.index('--tools') + 1]:
                self.disk_patch = patch('shutil.disk_usage', return_value=SimpleNamespace(free=0))
                self.disk_patch.start()
                self.addCleanup(self.disk_patch.stop)
            return result

        self.commands.run = fill_disk
        with self.assertRaisesRegex(GateError, 'disk'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.implementations, 1)
        self.assertEqual(self.commands.tests, 0)
        self.assertEqual(self.runner.store.read('issue-95')['repairs'], 0)


for _name in list(fixtures.EngineTests.__dict__):
    if _name.startswith('test_'):
        setattr(ReadinessTests, _name, None)
