import asyncio
from dataclasses import asdict, replace
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import test_runner as fixtures
from gophercrm_runner.config import DATABASE_CHECKS, EnvironmentError, GateError
from gophercrm_runner.engine import Runner
from gophercrm_runner.evidence import named_go_tests


class ReviewRegressionTests(fixtures.EngineTests):
    async def test_completed_rejected_call_recovers_its_verdict_before_another_review(self):
        self.commands.reject_reviews = 1
        original = self.runner.agent
        async def crash_after_saved_call(*args, **kwargs):
            result = await original(*args, **kwargs)
            if kwargs.get('review'):
                raise asyncio.CancelledError()
            return result
        self.runner.agent = crash_after_saved_call
        with self.assertRaises(asyncio.CancelledError):
            await self.runner.run_issue(95, execute=True)
        self.runner = Runner(self.config, self.commands)
        await self.runner.run_issue(95, execute=True)
        self.assertEqual((self.commands.implementations, self.commands.reviews), (2, 2))
        self.assertEqual(self.runner.store.read('issue-95')['repairs'], 1)

    async def test_completed_implementation_call_recovers_without_duplicate_model_call(self):
        original = self.runner.agent
        async def crash_after_saved_call(*args, **kwargs):
            await original(*args, **kwargs)
            raise asyncio.CancelledError()
        self.runner.agent = crash_after_saved_call
        with self.assertRaises(asyncio.CancelledError):
            await self.runner.run_issue(95, execute=True)
        self.runner = Runner(self.config, self.commands)
        await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.implementations, 1)

    async def recover_terminal_with_graph(self, target):
        from gophercrm_runner.flows import run_flow
        config = replace(self.config, completion_target=target)
        self.runner = Runner(config, self.commands)
        with patch.object(self.runner.store, 'release', side_effect=RuntimeError('simulated crash before active release')):
            with self.assertRaisesRegex(RuntimeError, 'simulated crash'):
                await self.runner.run_issue(95, execute=True)
        before = (self.commands.implementations, self.commands.reviews, self.commands.creates, self.commands.merges)
        path = self.root / 'policy.json'
        path.write_text(json.dumps(asdict(config), default=str))
        with patch('gophercrm_runner.engine.Commands', return_value=self.commands):
            result = await run_flow(path, execute=True, limit=1)
        rendered = json.dumps([row.model_dump(mode='json') for row in result])
        self.assertIn('pr_ready' if target == 'pr' else 'merged', rendered)
        self.assertEqual(self.runner.store.read('active'), {})
        self.assertEqual((self.commands.implementations, self.commands.reviews, self.commands.creates, self.commands.merges), before)
        self.assertEqual([i['issue'] for i in await self.runner.queue(2)], [96])

    async def test_graph_recovers_pr_ready_before_selecting_next_issue(self):
        await self.recover_terminal_with_graph('pr')

    async def test_graph_recovers_merged_before_selecting_next_issue(self):
        await self.recover_terminal_with_graph('merge')

    async def test_browser_readiness_rejects_missing_or_short_jwt_before_paid_work(self):
        browser = self.root / 'chromium'
        browser.write_text('fixture'); browser.chmod(0o700)
        config = replace(self.config, disposable_database=True, chromium_path=str(browser), database_test_commands=DATABASE_CHECKS)
        for secret in ['', 'short']:
            with patch.dict(os.environ, {'JWT_SECRET': secret, 'TEST_MYSQL_DSN': 'private-dsn'}):
                with self.assertRaisesRegex(EnvironmentError, 'JWT_SECRET'):
                    await Runner(config, self.commands).run_issue(95, execute=True)
            self.assertFalse((self.config.state_dir / 'worktrees').exists())
        self.assertEqual(self.commands.implementations, 0)

    async def test_later_jwt_configuration_error_is_environment_failure(self):
        original = self.commands.run
        async def invalid_jwt(args, **kwargs):
            if tuple(args) == ('make', 'verify'):
                from gophercrm_runner.config import CommandError
                raise CommandError('JWT_SECRET must be at least 32 characters long for security')
            return await original(args, **kwargs)
        self.commands.run = invalid_jwt
        with self.assertRaisesRegex(EnvironmentError, 'environment'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.runner.store.read('issue-95')['repairs'], 0)


for _name in list(fixtures.EngineTests.__dict__):
    if _name.startswith('test_'):
        setattr(ReviewRegressionTests, _name, None)


class RealGoEvidenceTests(unittest.TestCase):
    def test_testify_parent_pass_cannot_hide_skipped_acceptance_cases(self):
        root = Path(__file__).resolve().parents[3]
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / 'acceptance_test.go'
            source.write_text('''package acceptance
import ("testing"; "github.com/stretchr/testify/suite")
type AcceptanceSuite struct { suite.Suite }
func (s *AcceptanceSuite) TestOne() { s.T().Skip("missing database prerequisite") }
func (s *AcceptanceSuite) TestTwo() { s.T().Skip("missing database prerequisite") }
func TestAcceptanceSuite(t *testing.T) { suite.Run(t, new(AcceptanceSuite)) }
''')
            result = subprocess.run(['go', 'test', '-json', '-count=1', str(source)], cwd=root,
                                    capture_output=True, text=True, timeout=180)
            self.assertEqual(result.returncode, 0, result.stderr)
            events = [json.loads(line) for line in result.stdout.splitlines()]
            self.assertEqual(sum(e['Action'] == 'skip' for e in events), 2)
            with self.assertRaisesRegex(GateError, 'skipped'):
                named_go_tests(result.stdout, [{'package': 'command-line-arguments', 'name': 'TestAcceptanceSuite'}])
