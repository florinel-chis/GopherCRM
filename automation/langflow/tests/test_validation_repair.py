import copy
from pathlib import Path
import subprocess
import unittest

import test_runner as fixtures
import test_pull_requests as pr_fixtures
import test_batches as batch_fixtures
from gophercrm_runner.config import CommandError, GateError
from gophercrm_runner.engine import Runner


async def fail_validation_once(test, execute, key):
    original = test.commands.run
    failed = False

    async def boundary(args, **kwargs):
        nonlocal failed
        if tuple(args) == ('make', 'verify') and not failed:
            failed = True
            # A historical, environment-classified failure remains historical.
            raise CommandError('connection refused in unrelated test logging')
        return await original(args, **kwargs)

    test.commands.run = boundary
    with test.assertRaisesRegex(GateError, 'environment'):
        await execute()
    record = test.runner.store.read(key)
    test.assertEqual(record['stage'], 'validate')
    test.assertEqual(record['attempts'][-1]['category'], 'environment')
    return record


class ValidationRepairTests(unittest.IsolatedAsyncioTestCase):
    setUp = fixtures.EngineTests.setUp
    asyncSetUp = fixtures.EngineTests.asyncSetUp
    tearDown = fixtures.EngineTests.tearDown

    async def failed(self):
        return await fail_validation_once(self, lambda: self.runner.run_issue(95, execute=True), 'issue-95')

    async def test_inspected_failure_enters_bounded_repair_without_repeating_failed_validation(self):
        old = await self.failed()
        result = await self.runner.request_validation_repair(95, head=old['head'], reason='Inspected concrete assertion failure')
        self.assertEqual(result['status'], 'repair_pending')
        pending = self.runner.store.read('issue-95')
        for field in ('attempts', 'provider_calls', 'repairs', 'deadline', 'receipts', 'contract_hash', 'policy'):
            self.assertEqual(pending[field], old[field])
        self.assertEqual(pending['validation_repair_requests'][0]['failed_attempt'], old['attempts'][-1]['id'])
        self.assertEqual(self.runner.report(95)['validation_repair_requests'], pending['validation_repair_requests'])
        with self.assertRaises(GateError):
            await self.runner.request_validation_repair(95, head=old['head'], reason='Duplicate request')
        result = await self.runner.run_issue(95, execute=True)
        self.assertEqual(result['status'], 'merged')
        final = self.runner.store.read('issue-95')
        self.assertEqual(final['repairs'], 1)
        self.assertEqual(self.commands.implementations, 2)
        self.assertNotEqual(final['head'], old['head'])
        self.assertEqual(final['head'], final['tested_head'])
        self.assertEqual(final['head'], final['reviewed_head'])
        self.assertEqual([a['stage'] for a in final['attempts']][2], 'repair')
        self.assertEqual(final['attempts'][1], old['attempts'][1])

    async def test_wrong_head_dirty_source_and_inactive_execution_are_rejected(self):
        record = await self.failed()
        with self.assertRaises(GateError):
            await self.runner.request_validation_repair(95, head='0' * 40, reason='Wrong commit')
        dirty = Path(record['worktree']) / 'uncommitted.txt'
        dirty.write_text('operator work')
        with self.assertRaises(GateError):
            await self.runner.request_validation_repair(95, head=record['head'], reason='Dirty source')
        dirty.unlink()
        self.runner.store.write('active', {})
        with self.assertRaises(GateError):
            await self.runner.request_validation_repair(95, head=record['head'], reason='No active execution')
        self.assertEqual(self.runner.store.read('issue-95'), record)

    async def test_review_outcomes_or_exhausted_limits_cannot_be_overridden(self):
        original = await self.failed()
        cases = [dict(stage='review'), dict(deadline=0),
                 dict(provider_calls=original['max_provider_calls']),
                 dict(repairs=self.config.max_repair_rounds),
                 dict(provider_invocations=[dict(id='orphan', status='running')]),
                 dict(policy='changed')]
        for values in cases:
            record = dict(copy.deepcopy(original), **values)
            self.runner.store.write('issue-95', record)
            with self.subTest(values=values), self.assertRaises(GateError):
                await self.runner.request_validation_repair(95, head=record['head'], reason='Inspected failure')
            self.assertEqual(self.runner.store.read('issue-95'), record)

    async def test_actual_source_commit_must_match_the_pinned_failure(self):
        record = await self.failed()
        source = Path(record['worktree'])
        (source / 'other.txt').write_text('new candidate')
        for args in [('add', '.'), ('commit', '-m', 'new candidate')]:
            subprocess.run(['git', *args], cwd=source, check=True, capture_output=True)
        with self.assertRaises(GateError):
            await self.runner.request_validation_repair(95, head=record['head'], reason='Old candidate')
        self.assertEqual(self.runner.store.read('issue-95'), record)


class PullRequestValidationRepairTests(unittest.IsolatedAsyncioTestCase):
    setUp = pr_fixtures.PullRequestTests.setUp
    tearDown = pr_fixtures.PullRequestTests.tearDown
    git = pr_fixtures.PullRequestTests.git
    complete = pr_fixtures.PullRequestTests.complete

    async def test_existing_pr_uses_same_repair_guards_and_preserves_failed_evidence(self):
        old = await fail_validation_once(self, self.complete, 'completion-pr-122')
        await self.runner.request_validation_repair(122, head=old['head'], reason='Inspected assertion', pull_request=True)
        pending = self.runner.store.read('completion-pr-122')
        self.assertEqual(pending['attempts'], old['attempts'])
        self.assertEqual(pending['deadline'], old['deadline'])
        self.assertEqual(pending['provider_calls'], old['provider_calls'])
        result = await self.complete()
        self.assertEqual(result['status'], 'merged')
        final = self.runner.store.read('completion-pr-122')
        self.assertEqual(final['repairs'], 1)
        self.assertEqual(final['head'], final['tested_head'])
        self.assertEqual(final['head'], final['reviewed_head'])
        self.assertEqual(final['attempts'][0], old['attempts'][0])


class BatchValidationRepairTests(unittest.IsolatedAsyncioTestCase):
    setUp = batch_fixtures.BatchTests.setUp
    asyncSetUp = batch_fixtures.BatchTests.asyncSetUp
    tearDown = batch_fixtures.BatchTests.tearDown
    prepare = batch_fixtures.BatchTests.prepare
    fresh = batch_fixtures.BatchTests.fresh

    async def failed(self):
        self.spec['entries'] = self.spec['entries'][:1]
        await self.prepare()
        self.runner = self.batch.runner
        return await fail_validation_once(self,
            lambda: self.batch.run('fixture', execute=True), 'issue-95')

    async def test_batch_repair_preserves_owner_plan_allocation_and_spent_limits(self):
        old = await self.failed()
        store = self.batch.store
        before = store.read('batch-fixture')
        owner, plan = store.read('batch-owner'), store.read('batch-fixture-plan')
        with self.assertRaisesRegex(GateError, 'batch owns'):
            await Runner(self.config, self.commands).request_validation_repair(
                95, head=old['head'], reason='Missing batch ownership')
        result = await self.fresh().request_validation_repair(
            'fixture', 95, head=old['head'], reason='Inspected source assertion')
        self.assertTrue(result['resume_required'])
        self.assertEqual(result['batch'], 'fixture')
        pending = store.read('issue-95')
        after = store.read('batch-fixture')
        for field in ('allocations', 'deadline', 'provider_calls', 'plan_hash', 'index', 'status'):
            self.assertEqual(before[field], after[field])
        for field in ('attempts', 'provider_calls', 'repairs', 'deadline', 'receipts', 'contract_hash', 'policy'):
            self.assertEqual(old[field], pending[field])
        self.assertEqual(store.read('batch-owner'), owner)
        self.assertEqual(store.read('batch-fixture-plan'), plan)
        self.assertEqual(after['current_stage'], 'repair_pending')
        result = await self.fresh().run('fixture', execute=True, resume=True)
        self.assertEqual(result['status'], 'completed')
        final = store.read('issue-95')
        self.assertEqual(final['repairs'], 1)
        self.assertEqual(final['attempts'][2]['stage'], 'repair')
        self.assertNotEqual(final['head'], old['head'])
        self.assertEqual(final['head'], final['tested_head'])
        self.assertEqual(final['head'], final['reviewed_head'])
        self.assertEqual(result['provider_calls'], 3)

    async def test_batch_repair_rejects_wrong_entry_changed_owner_plan_allocation_or_deadline(self):
        old = await self.failed()
        store = self.batch.store
        with self.assertRaisesRegex(GateError, 'current batch entry'):
            await self.fresh().request_validation_repair('fixture', 96, head=old['head'], reason='Wrong child')
        originals = {key: store.read(key) for key in ('batch-owner', 'batch-fixture', 'issue-95')}
        cases = [('batch-owner', dict(originals['batch-owner'], plan_hash='changed')),
                 ('batch-fixture', dict(originals['batch-fixture'], plan_hash='changed')),
                 ('batch-fixture', dict(originals['batch-fixture'], deadline=0)),
                 ('issue-95', dict(old, max_provider_calls=old['max_provider_calls'] + 1))]
        for key, value in cases:
            with self.subTest(key=key, value=value):
                store.write(key, value)
                with self.assertRaises(GateError):
                    await self.fresh().request_validation_repair('fixture', 95, head=old['head'], reason='Inspected assertion')
                self.assertEqual(store.read(key), value)
                store.write(key, originals[key])
        self.assertEqual(store.read('issue-95'), old)
