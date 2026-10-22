from pathlib import Path
import unittest
from unittest.mock import AsyncMock

import test_runner as fixtures
import test_batches as batch_fixtures
import test_pull_requests as pr_fixtures
from gophercrm_runner.config import CommandError, GateError


SQLITE = ('make', 'e2e', 'E2E_DB_DRIVER=sqlite')
MYSQL = ('make', 'e2e', 'E2E_DB_DRIVER=mysql')
INSTALL = ('npm', 'ci', '--prefix', 'gocrm-ui')


class ValidationResumeTests(unittest.IsolatedAsyncioTestCase):
    setUp = fixtures.EngineTests.setUp
    tearDown = fixtures.EngineTests.tearDown

    async def asyncSetUp(self):
        await fixtures.EngineTests.asyncSetUp(self)
        self.checks = [('local-0', ('make', 'verify'), None),
            ('acceptance-regression', ('go', 'test', '-json', '-count=1', './feature'),
             self.config.acceptance_checks['regression']['tests']),
            ('database-0', SQLITE, None), ('database-1', MYSQL, None)]
        self.runner.validation_checks = AsyncMock(side_effect=lambda *args: self.checks)
        self.browser_calls = []
        self.installs = []
        original = self.commands.run

        async def boundary(args, *, cwd, **kwargs):
            args = tuple(args)
            if args == INSTALL:
                self.installs.append(cwd)
                return 0, 'Locked frontend dependencies installed'
            if args in (SQLITE, MYSQL):
                self.browser_calls.append((args, cwd))
                if args == MYSQL and sum(c[0] == MYSQL for c in self.browser_calls) == 1:
                    raise CommandError('connection refused: temporary test environment failure')
                return 0, 'All browser checks passed'
            return await original(args, cwd=cwd, **kwargs)

        self.commands.run = boundary

    async def failed(self):
        with self.assertRaisesRegex(GateError, 'environment'):
            await self.runner.run_issue(95, execute=True)
        return self.runner.store.read('issue-95')

    async def test_same_candidate_resumes_only_missing_gate_in_fresh_checkout(self):
        old = await self.failed()
        original_receipts = {ref['path']: (self.config.state_dir / ref['path']).read_bytes()
                             for ref in old['receipts'].values()}
        result = await self.runner.run_issue(95, execute=True)
        self.assertEqual(result['status'], 'merged')
        final = self.runner.store.read('issue-95')
        self.assertEqual(self.commands.tests, 1)
        self.assertEqual(self.commands.implementations, 1)
        self.assertEqual(final['repairs'], 0)
        self.assertEqual(final['validation_attempt'], old['validation_attempt'])
        self.assertEqual(final['deadline'], old['deadline'])
        self.assertEqual([c[0] for c in self.browser_calls], [SQLITE, MYSQL, MYSQL])
        self.assertNotEqual(final['validation_worktree'], old['validation_worktree'])
        self.assertEqual(self.installs, [Path(final['validation_worktree'])])
        self.assertEqual(final['validation_resumptions'][0]['reused_checks'], list(old['receipts']))
        for key, reference in old['receipts'].items():
            self.assertEqual(final['receipts'][key], reference)
            self.assertEqual((self.config.state_dir / reference['path']).read_bytes(),
                             original_receipts[reference['path']])
        self.assertEqual(final['head'], final['tested_head'])
        self.assertEqual(final['head'], final['reviewed_head'])

    async def test_tampered_receipt_stops_before_reuse_or_reexecution(self):
        record = await self.failed()
        receipt = self.config.state_dir / record['receipts']['local-0']['path']
        receipt.write_text('{}')
        with self.assertRaisesRegex(GateError, 'digest'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.tests, 1)
        self.assertEqual(len(self.browser_calls), 2)

    async def test_dirty_prior_validation_checkout_cannot_supply_reused_evidence(self):
        record = await self.failed()
        (Path(record['validation_worktree']) / 'untracked-change.txt').write_text('tampered')
        with self.assertRaisesRegex(GateError, 'Worktree changed'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.tests, 1)

    async def test_changed_check_command_cannot_reuse_previous_success(self):
        await self.failed()
        self.checks[2] = ('database-0', SQLITE + ('SPECS=different.spec.ts',), None)
        with self.assertRaisesRegex(GateError, 'trusted successful check'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.tests, 1)

    async def test_source_repair_invalidates_prior_successes_and_runs_all_gates(self):
        old = await self.failed()
        await self.runner.request_validation_repair(95, head=old['head'], reason='Inspected source assertion')
        result = await self.runner.run_issue(95, execute=True)
        self.assertEqual(result['status'], 'merged')
        final = self.runner.store.read('issue-95')
        self.assertNotEqual(final['head'], old['head'])
        self.assertNotEqual(final['validation_attempt'], old['validation_attempt'])
        self.assertEqual(self.commands.tests, 2)
        self.assertEqual([c[0] for c in self.browser_calls], [SQLITE, MYSQL, SQLITE, MYSQL])
        self.assertFalse(final.get('validation_resumptions'))


class BatchValidationResumeTests(unittest.IsolatedAsyncioTestCase):
    setUp = batch_fixtures.BatchTests.setUp
    asyncSetUp = batch_fixtures.BatchTests.asyncSetUp
    tearDown = batch_fixtures.BatchTests.tearDown
    prepare = batch_fixtures.BatchTests.prepare
    fresh = batch_fixtures.BatchTests.fresh

    async def test_batch_resumes_after_passed_core_without_spending_repair_or_resetting_limits(self):
        self.spec['entries'] = self.spec['entries'][:1]
        await self.prepare()
        original = self.commands.run
        failed = False

        async def boundary(args, **kwargs):
            nonlocal failed
            if tuple(args) == ('go', 'test', '-json', '-count=1', './feature') and not failed:
                failed = True
                raise CommandError('connection refused')
            return await original(args, **kwargs)

        self.commands.run = boundary
        with self.assertRaisesRegex(GateError, 'environment'):
            await self.batch.run('fixture', execute=True)
        old = self.batch.store.read('issue-95')
        result = await self.fresh().run('fixture', execute=True, resume=True)
        self.assertEqual(result['status'], 'completed')
        final = self.batch.store.read('issue-95')
        self.assertEqual(self.commands.tests, 1)
        self.assertEqual(final['receipts']['local-0'], old['receipts']['local-0'])
        self.assertEqual(final['deadline'], old['deadline'])
        self.assertEqual(final['repairs'], 0)
        self.assertEqual(result['provider_calls'], 2)


class PullRequestValidationResumeTests(unittest.IsolatedAsyncioTestCase):
    setUp = pr_fixtures.PullRequestTests.setUp
    tearDown = pr_fixtures.PullRequestTests.tearDown
    git = pr_fixtures.PullRequestTests.git
    complete = pr_fixtures.PullRequestTests.complete

    async def test_existing_pr_resumes_passed_core_before_fresh_independent_review(self):
        extra = ('fixture', 'later-check')
        self.runner.validation_checks = AsyncMock(return_value=[
            ('local-0', ('make', 'verify'), None), ('local-1', extra, None)])
        original = self.commands.run
        failed = False

        async def boundary(args, **kwargs):
            nonlocal failed
            if tuple(args) == extra:
                if not failed:
                    failed = True
                    raise CommandError('connection refused')
                return 0, 'Later check passed'
            return await original(args, **kwargs)

        self.commands.run = boundary
        with self.assertRaisesRegex(GateError, 'environment'):
            await self.complete()
        old = self.runner.store.read('completion-pr-122')
        result = await self.complete()
        self.assertEqual(result['status'], 'merged')
        final = self.runner.store.read('completion-pr-122')
        self.assertEqual(self.commands.tests, 1)
        self.assertEqual(final['receipts']['local-0'], old['receipts']['local-0'])
        self.assertEqual(final['deadline'], old['deadline'])
        self.assertEqual(final['head'], final['tested_head'])
        self.assertEqual(final['head'], final['reviewed_head'])
