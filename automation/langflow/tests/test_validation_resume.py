from pathlib import Path
import json
import shutil
import unittest
from unittest.mock import AsyncMock, patch

import test_runner as fixtures
import test_batches as batch_fixtures
import test_pull_requests as pr_fixtures
from gophercrm_runner import evidence
from gophercrm_runner.config import BudgetError, CommandError, GateError


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
            if args[:3] in (SQLITE, MYSQL):
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

    async def assert_fresh_validation(self, old, reason):
        result = await self.runner.run_issue(95, execute=True)
        self.assertEqual(result['status'], 'merged')
        final = self.runner.store.read('issue-95')
        self.assertEqual(self.commands.tests, 2)
        self.assertEqual(self.commands.implementations, 1)
        self.assertEqual(final['repairs'], 0)
        self.assertEqual(final['deadline'], old['deadline'])
        self.assertNotEqual(final['validation_attempt'], old['validation_attempt'])
        self.assertNotEqual(final['receipts']['local-0'], old['receipts']['local-0'])
        restart = final['validation_restarts'][-1]
        self.assertEqual(restart['receipts'], old['receipts'])
        self.assertEqual(restart['previous_validation_attempt'], old['validation_attempt'])
        self.assertIn(reason, restart['reason'])
        self.assertFalse(final.get('validation_resumptions'))
        self.assertEqual(len(self.browser_calls), 4)
        self.assertEqual(final['head'], final['tested_head'])
        self.assertEqual(final['head'], final['reviewed_head'])
        return final

    async def test_tampered_receipt_requires_fresh_full_validation(self):
        record = await self.failed()
        receipt = self.config.state_dir / record['receipts']['local-0']['path']
        receipt.write_text('{}')
        await self.assert_fresh_validation(record, 'digest')
        self.assertEqual(receipt.read_text(), '{}', 'Do not overwrite the damaged historical artifact')

    async def test_dirty_prior_validation_checkout_cannot_supply_reused_evidence(self):
        record = await self.failed()
        (Path(record['validation_worktree']) / 'untracked-change.txt').write_text('tampered')
        await self.assert_fresh_validation(record, 'Worktree changed')

    async def test_missing_prior_checkout_restarts_without_uncaught_oserror(self):
        record = await self.failed()
        shutil.rmtree(record['validation_worktree'])
        await self.assert_fresh_validation(record, 'No such file')

    async def test_missing_output_restarts_without_reusing_any_pass(self):
        record = await self.failed()
        receipt = self.config.state_dir / record['receipts']['local-0']['path']
        output = self.config.state_dir / json.loads(receipt.read_text())['output_path']
        output.unlink()
        await self.assert_fresh_validation(record, 'Missing or invalid evidence')

    async def test_budget_expiry_during_checkpoint_verification_does_not_discard_passes(self):
        record = await self.failed()
        original = self.runner.clean

        async def boundary(path):
            if path == Path(record['validation_worktree']):
                raise BudgetError('Execution deadline exhausted')
            await original(path)

        self.runner.clean = boundary
        with self.assertRaises(BudgetError):
            await self.runner.validate(record)
        self.assertFalse(record.get('validation_restarts'))
        self.assertEqual(record['receipts'], self.runner.store.read('issue-95')['receipts'])
        self.assertEqual(self.commands.tests, 1)

    async def test_changed_check_command_cannot_reuse_previous_success(self):
        old = await self.failed()
        self.checks[2] = ('database-0', SQLITE + ('SPECS=different.spec.ts',), None)
        await self.assert_fresh_validation(old, 'trusted successful check')
        self.assertEqual(self.browser_calls[2][0], self.checks[2][1])

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

    async def failed_pr(self):
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
        return self.runner.store.read('completion-pr-122')

    async def test_existing_pr_resumes_passed_core_before_fresh_independent_review(self):
        old = await self.failed_pr()
        result = await self.complete()
        self.assertEqual(result['status'], 'merged')
        final = self.runner.store.read('completion-pr-122')
        self.assertEqual(self.commands.tests, 1)
        self.assertEqual(final['receipts']['local-0'], old['receipts']['local-0'])
        self.assertEqual(final['deadline'], old['deadline'])
        self.assertEqual(final['head'], final['tested_head'])
        self.assertEqual(final['head'], final['reviewed_head'])

    async def test_existing_pr_recovers_missing_checkout_without_head_or_budget_change(self):
        old = await self.failed_pr()
        shutil.rmtree(old['validation_worktree'])
        result = await self.complete()
        self.assertEqual(result['status'], 'merged')
        final = self.runner.store.read('completion-pr-122')
        self.assertEqual(self.commands.tests, 2)
        self.assertEqual(final['head'], old['head'])
        self.assertEqual(final['deadline'], old['deadline'])
        self.assertEqual(final['repairs'], 0)
        self.assertNotEqual(final['validation_attempt'], old['validation_attempt'])
        self.assertEqual(final['validation_restarts'][0]['receipts'], old['receipts'])
        self.assertEqual(final['head'], final['tested_head'])
        self.assertEqual(final['head'], final['reviewed_head'])


VITEST = ('npm', '--prefix', 'gocrm-ui', 'test', '--silent', '--', '--run', '--reporter=json',
          'src/pages/deals/DealStageChip.test.tsx')
VITEST_TESTS = [{'file': 'src/pages/deals/DealStageChip.test.tsx',
                 'name': 'DealStageChip labels \'qualification\' as "\'Qualification\'"'}]
REPORT = Path(__file__).with_name('vitest-report.json').read_text()


class VitestValidationTests(unittest.IsolatedAsyncioTestCase):
    """A Vitest acceptance check is parsed from its JSON report and installs locked dependencies."""
    setUp = fixtures.EngineTests.setUp
    tearDown = fixtures.EngineTests.tearDown

    async def asyncSetUp(self):
        await fixtures.EngineTests.asyncSetUp(self)
        self.checks = [('local-0', ('make', 'verify'), None),
                       ('acceptance-ui-deals', VITEST, evidence.vitest_identities(VITEST_TESTS))]
        self.runner.validation_checks = AsyncMock(side_effect=lambda *args: self.checks)
        self.installs, self.vitest_calls, self.reports = [], [], []
        original = self.commands.run

        async def boundary(args, *, cwd, **kwargs):
            args = tuple(args)
            if args == INSTALL:
                self.installs.append(cwd)
                return 0, 'Locked frontend dependencies installed'
            if args == VITEST:
                self.vitest_calls.append(cwd)
                report = self.reports.pop(0)
                if isinstance(report, Exception):
                    raise report
                return 0, report
            return await original(args, cwd=cwd, **kwargs)

        self.commands.run = boundary

    async def test_resumed_vitest_check_installs_dependencies_and_parses_its_report(self):
        self.reports = [CommandError('connection refused: temporary test environment failure'), REPORT]
        with self.assertRaisesRegex(GateError, 'environment'):
            await self.runner.run_issue(95, execute=True)
        old = self.runner.store.read('issue-95')
        self.assertEqual(list(old['receipts']), ['local-0'])
        with patch('gophercrm_runner.evidence.named_vitest_tests',
                   wraps=evidence.named_vitest_tests) as vitest, \
                patch('gophercrm_runner.evidence.named_go_tests', wraps=evidence.named_go_tests) as go:
            result = await self.runner.run_issue(95, execute=True)
        self.assertEqual(result['status'], 'merged')
        final = self.runner.store.read('issue-95')
        self.assertEqual(final['receipts']['local-0'], old['receipts']['local-0'])
        self.assertIn('acceptance-ui-deals', final['receipts'])
        # The reused make verify receipt did not install this fresh checkout's dependencies.
        self.assertEqual(self.installs, [Path(final['validation_worktree'])])
        self.assertEqual(self.vitest_calls[-1], Path(final['validation_worktree']))
        # The runner passes the command's stdout without its trailing newline.
        self.assertEqual([call.args for call in vitest.call_args_list], [(REPORT.rstrip('\n'), VITEST_TESTS)])
        go.assert_not_called()

    async def test_vitest_check_without_a_named_pass_produces_no_receipt(self):
        skipped = json.loads(REPORT)
        skipped['testResults'][0]['assertionResults'][0]['status'] = 'skipped'
        self.reports = [json.dumps(skipped)] * 3
        with self.assertRaisesRegex(GateError, 'Repair budget exhausted') as raised:
            await self.runner.run_issue(95, execute=True)
        self.assertIn('failed or was skipped', str(raised.exception.__context__))
        self.assertEqual(len(self.vitest_calls), 3)
        record = self.runner.store.read('issue-95')
        self.assertNotIn('acceptance-ui-deals', record.get('receipts', {}))
        self.assertIsNone(record.get('tested_head'))
