from dataclasses import replace
import unittest
from unittest.mock import patch

import test_runner as fixtures
from gophercrm_runner.config import EnvironmentError, GateError
from gophercrm_runner.engine import Runner
from gophercrm_runner.state import Store
from gophercrm_runner.validation_review import confirm, plan_identity


class ValidationReviewTests(unittest.IsolatedAsyncioTestCase):
    setUp = fixtures.EngineTests.setUp
    tearDown = fixtures.EngineTests.tearDown

    async def asyncSetUp(self):
        await fixtures.EngineTests.asyncSetUp(self)
        self.config = replace(self.config, validation_review_required=True)
        self.runner = Runner(self.config, self.commands)

    async def held(self):
        with self.assertRaisesRegex(EnvironmentError, 'operator inspection'):
            await self.runner.run_issue(95, execute=True)
        record = self.runner.store.read('issue-95')
        self.assertEqual(record['stage'], 'validate')
        self.assertEqual(self.commands.tests, 0)
        self.assertFalse(record.get('validation_worktree'))
        self.assertEqual(record['repairs'], 0)
        return record

    async def acknowledge(self, record, **overrides):
        args = dict(head=record['head'], plan_hash=record['validation_inspection']['plan_hash'],
                    reason='Inspected committed diff and complete command plan')
        args.update(overrides)
        with patch('gophercrm_runner.engine.Runner', return_value=self.runner):
            return await confirm(self.config, 95, **args)

    async def test_hold_confirm_resume_preserves_budget_and_runs_all_gates(self):
        record = await self.held()
        await self.acknowledge(record)
        result = await self.runner.run_issue(95, execute=True)
        final = self.runner.store.read('issue-95')
        self.assertEqual(result['status'], 'merged')
        self.assertEqual(final['deadline'], record['deadline'])
        self.assertEqual(self.commands.implementations, 1)
        self.assertEqual(self.commands.tests, 1)
        self.assertEqual(final['repairs'], 0)

    async def test_wrong_candidate_plan_or_active_provider_cannot_confirm(self):
        record = await self.held()
        for changes in ({'head': 'wrong'}, {'plan_hash': 'wrong'}, {'reason': ''}):
            with self.subTest(changes=changes), self.assertRaises(GateError):
                await self.acknowledge(record, **changes)
        record['provider_invocations'].append({'status': 'running'})
        self.runner.store.write('issue-95', record)
        with self.assertRaises(GateError):
            await self.acknowledge(record)

    async def test_confirmation_cannot_run_while_lock_or_batch_is_owned(self):
        record = await self.held()
        with Store(self.config.state_dir).exclusive(), self.assertRaises(GateError):
            await self.acknowledge(record)
        self.runner.store.write('batch-owner', {'id': 'other', 'plan_hash': 'hash'})
        with self.assertRaises(GateError):
            await self.acknowledge(record)

    def test_hash_covers_candidate_policy_scope_commands_and_named_test_identity(self):
        record = dict(execution_id='x', head='a', base='b', policy='p', contract_hash='c',
                      context_hash='d', validation_plan={'profile': 'aeo'})
        checks = [('local-0', ['go', 'test'], [{'package': 'p', 'name': 'TestOne'}])]
        digest = plan_identity(record, checks)
        for key in record:
            self.assertNotEqual(digest, plan_identity(dict(record, **{key: 'changed'}), checks))
        self.assertNotEqual(digest, plan_identity(record, [('local-0', ['go', 'test'],
                            [{'package': 'p', 'name': 'TestTwo'}])]))


import test_batches as batch_fixtures
import test_pull_requests as pr_fixtures
from gophercrm_runner.batches import BatchRunner
from gophercrm_runner.pull_requests import PullRequestRunner


class BatchValidationReviewTests(unittest.IsolatedAsyncioTestCase):
    setUp = fixtures.EngineTests.setUp
    tearDown = fixtures.EngineTests.tearDown
    prepare = batch_fixtures.BatchTests.prepare

    async def test_single_ticket_batch_holds_and_resumes_original_allocation(self):
        await batch_fixtures.BatchTests.asyncSetUp(self)
        self.config = replace(self.config, validation_review_required=True)
        self.batch = BatchRunner(self.config, self.commands)
        self.spec['entries'] = self.spec['entries'][:1]
        await self.prepare()
        with self.assertRaises(EnvironmentError):
            await self.batch.run('fixture', execute=True)
        record = self.batch.store.read('issue-95')
        before = self.batch.store.read('batch-fixture')
        self.assertEqual(self.commands.tests, 0)
        with patch('gophercrm_runner.engine.Runner', return_value=self.batch.runner):
            await confirm(self.config, 95, head=record['head'],
                          plan_hash=record['validation_inspection']['plan_hash'],
                          reason='Inspected candidate', batch='fixture')
        result = await self.batch.run('fixture', execute=True, resume=True)
        final = self.batch.store.read('issue-95')
        self.assertEqual(result['status'], 'completed')
        self.assertEqual(final['execution_id'], record['execution_id'])
        self.assertEqual(final['deadline'], record['deadline'])
        self.assertEqual(self.batch.store.read('batch-fixture')['deadline'], before['deadline'])
        self.assertEqual((self.commands.implementations, self.commands.reviews, self.commands.tests), (1, 1, 1))


class PRValidationReviewTests(unittest.IsolatedAsyncioTestCase):
    setUp = pr_fixtures.PullRequestTests.setUp
    tearDown = pr_fixtures.PullRequestTests.tearDown
    git = pr_fixtures.PullRequestTests.git
    complete = pr_fixtures.PullRequestTests.complete

    async def test_existing_pr_holds_before_checks_then_requires_normal_review_and_ci(self):
        self.config = replace(self.config, validation_review_required=True)
        self.runner = PullRequestRunner(self.config, self.commands)
        with self.assertRaises(EnvironmentError):
            await self.complete()
        record = self.runner.store.read('completion-pr-122')
        self.assertEqual(self.commands.tests, 0)
        with patch('gophercrm_runner.pull_requests.PullRequestRunner', return_value=self.runner):
            await confirm(self.config, 122, head=record['head'],
                          plan_hash=record['validation_inspection']['plan_hash'],
                          reason='Inspected candidate', pull_request=True)
        result = await self.complete()
        self.assertEqual(result['status'], 'merged')
        self.assertEqual((self.commands.tests, self.commands.reviews, self.commands.merges), (1, 1, 1))
