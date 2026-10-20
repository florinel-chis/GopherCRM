"""Existing-PR completion uses real Git and deterministic external boundaries."""

import asyncio
from dataclasses import asdict, replace
import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

import test_runner as fixtures
from gophercrm_runner.config import GateError
from gophercrm_runner.engine import Runner
from gophercrm_runner.pull_requests import PullRequestRunner


class PullRequestTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        fixtures.EngineTests.setUp(self)
        self.source = self.root / 'external'
        self.git('worktree', 'add', '-b', 'external', str(self.source))
        (self.source / 'feature.txt').write_text('External contribution\n')
        self.git('add', '.', cwd=self.source)
        self.git('commit', '-m', 'external', cwd=self.source)
        self.head = self.git('rev-parse', 'HEAD', cwd=self.source)
        self.commands.pushed_head = self.head
        self.commands.pr_data = dict(fixtures.ready_pr(), headRefOid=self.head,
            baseRefOid=self.commands.base, headRefName='external', title='Fix external feature',
            body='Preserve expected behavior', isCrossRepository=False)
        original = self.commands.run

        async def boundary(args, **kwargs):
            if tuple(args[:2]) == ('git', 'fetch') and len(args) > 4:
                self.git('update-ref', 'refs/remotes/origin/completion-pr-122',
                         self.commands.pr_data['headRefOid'])
                return 0, ''
            code, output = await original(args, **kwargs)
            if args[0] == 'claude' and '-p' in args and not self.with_criteria:
                result = json.loads(output)
                report = result['structured_output']
                if 'criteria' in report:
                    report['criteria'] = []
                else:
                    report['addressed_criteria'] = []
                output = json.dumps(result)
            return code, output

        self.with_criteria = False
        self.commands.run = boundary
        self.runner = PullRequestRunner(self.config, self.commands)

    def git(self, *args, cwd=None):
        return subprocess.check_output(['git', *args], cwd=cwd or self.checkout,
                                       stderr=subprocess.DEVNULL, text=True).strip()

    def tearDown(self):
        self.temp.cleanup()

    async def complete(self, **kwargs):
        return await self.runner.complete_pr(122, self.commands.pr_data['headRefOid'],
                                             execute=True, **kwargs)

    async def test_preview_is_read_only_and_does_not_admit_queue(self):
        result = await self.runner.complete_pr(122, self.head)
        self.assertEqual(result['completion_target'], 'merge')
        self.assertEqual(result['status'], 'planned')
        self.assertFalse(self.runner.store.read('active'))
        self.assertEqual(self.commands.reviews, 0)
        self.assertFalse(self.runner.store.read('completion-pr-122'))

    async def test_existing_pr_tests_reviews_merges_and_reconciles_once(self):
        result = await self.complete()
        self.assertEqual(result['status'], 'merged')
        self.assertEqual((self.commands.tests, self.commands.reviews, self.commands.merges), (1, 1, 1))
        self.assertEqual(self.commands.creates, 0)
        self.assertEqual(self.runner.store.read('active'), {})
        self.assertEqual((await self.complete())['status'], 'merged')
        self.assertEqual((self.commands.tests, self.commands.reviews, self.commands.merges), (1, 1, 1))

    async def test_rejection_repairs_then_retests_and_reviews_new_head(self):
        self.commands.reject_reviews = 1
        await self.complete()
        self.assertEqual((self.commands.tests, self.commands.reviews, self.commands.implementations), (2, 2, 1))
        self.assertEqual(self.commands.merges, 1)
        record = self.runner.store.read('completion-pr-122')
        self.assertNotEqual(record['head'], self.head)
        self.assertEqual(record['tested_head'], record['reviewed_head'])

    async def test_exhausted_rejection_cannot_be_rerolled_by_resume_or_refresh(self):
        self.commands.reject_reviews = 100
        with self.assertRaisesRegex(GateError, 'budget exhausted'):
            await self.complete()
        counts = self.commands.tests, self.commands.reviews
        self.commands.reject_reviews = 0
        with self.assertRaisesRegex(GateError, 'budget exhausted'):
            await self.complete()
        with self.assertRaisesRegex(GateError, 'Refresh requires'):
            await self.complete(refresh=True)
        self.assertEqual((self.commands.tests, self.commands.reviews), counts)
        self.assertEqual(self.commands.merges, 0)

    async def test_explicit_pr_completion_preserves_stopped_issue_queue(self):
        self.runner.store.stop_after_current()
        control = self.runner.store.read('control')
        await self.complete()
        self.assertEqual(self.runner.store.read('control'), control)
        self.assertEqual(await self.runner.queue(2), [])

    async def test_active_issue_or_other_pr_blocks_admission(self):
        for active in ({'issue': 95}, {'pr': 123}):
            self.runner.store.write('active', active)
            with self.assertRaisesRegex(GateError, 'must finish'):
                await self.complete()
            self.assertEqual(self.runner.store.read('active'), active)
        self.assertEqual(self.commands.tests, 0)

    async def test_ticket_admission_is_blocked_while_pr_active(self):
        self.runner.store.write('active', {'pr': 122})
        with self.assertRaisesRegex(GateError, 'blocks the issue queue'):
            await self.runner.queue(2)
        with self.assertRaisesRegex(GateError, 'PR completion'):
            self.runner.store.admit(95)

    async def test_fork_draft_wrong_head_base_or_closed_pr_never_runs(self):
        for field, value in (('isCrossRepository', True), ('isDraft', True),
                             ('baseRefName', 'other'), ('state', 'CLOSED'),
                             ('headRefOid', 'f' * 40)):
            old = self.commands.pr_data[field]
            self.commands.pr_data[field] = value
            with self.assertRaises(GateError):
                await self.runner.complete_pr(122, self.head, execute=True)
            self.commands.pr_data[field] = old
        self.assertEqual((self.commands.tests, self.commands.reviews, self.commands.merges), (0, 0, 0))

    async def test_ci_failure_and_github_review_requirement_block_merge(self):
        self.commands.fail_ci = True
        with self.assertRaisesRegex(GateError, 'CI failed'):
            await self.complete()
        self.commands.fail_ci = False
        self.commands.pr_data['reviewDecision'] = 'REVIEW_REQUIRED'
        with self.assertRaisesRegex(GateError, 'not ready'):
            await self.complete()
        self.assertEqual(self.commands.merges, 0)
        self.assertEqual((self.commands.tests, self.commands.reviews), (1, 1))

    async def test_live_base_change_blocks_merge_and_requires_fresh_evidence(self):
        original = self.runner.review

        async def advance(issue, record):
            verdict = await original(issue, record)
            (self.checkout / 'new-main.txt').write_text('new main\n')
            self.git('add', '.')
            self.git('commit', '-m', 'advance main')
            new_base = self.git('rev-parse', 'HEAD')
            self.git('update-ref', 'refs/remotes/origin/main', new_base)
            self.commands.pr_data['baseRefOid'] = new_base
            return verdict

        with patch.object(self.runner, 'review', side_effect=advance):
            with self.assertRaisesRegex(GateError, 'Main advanced'):
                await self.complete()
        before = self.runner.store.read('completion-pr-122')
        self.git('merge', '--no-edit', self.git('rev-parse', 'HEAD'), cwd=self.source)
        self.commands.pr_data['headRefOid'] = self.git('rev-parse', 'HEAD', cwd=self.source)
        self.commands.pushed_head = self.commands.pr_data['headRefOid']
        await self.complete(refresh=True)
        after = self.runner.store.read('completion-pr-122')
        self.assertEqual((self.commands.tests, self.commands.reviews), (2, 2))
        self.assertEqual(after['deadline'], before['deadline'])
        self.assertEqual(after['provider_calls'], before['provider_calls'] + 1)
        self.assertEqual(len(after['history']), 1)

    async def test_review_receipt_tampering_blocks_merge(self):
        original = self.runner.review

        async def tamper(issue, record):
            result = await original(issue, record)
            (self.config.state_dir / record['review_receipt']['path']).write_text('{}')
            return result

        with patch.object(self.runner, 'review', side_effect=tamper):
            with self.assertRaisesRegex(GateError, 'digest'):
                await self.complete()
        self.assertEqual(self.commands.merges, 0)

    async def test_external_head_change_after_review_is_not_adopted(self):
        original = self.runner.review

        async def change(issue, record):
            result = await original(issue, record)
            self.commands.pr_data['headRefOid'] = 'f' * 40
            return result

        with patch.object(self.runner, 'review', side_effect=change):
            with self.assertRaisesRegex(GateError, 'explicit head'):
                await self.complete()
        self.assertEqual(self.commands.merges, 0)

    async def test_repair_cannot_modify_validation_gates(self):
        self.commands.reject_reviews = 1
        original = self.commands.run

        async def change(args, **kwargs):
            result = await original(args, **kwargs)
            if args[0] == 'claude' and '-p' in args and 'Edit' in args[args.index('--tools') + 1]:
                (kwargs['cwd'] / 'Makefile').write_text('verify:\n\ttrue\n')
            return result

        with patch.object(self.commands, 'run', side_effect=change):
            with self.assertRaisesRegex(GateError, 'validation gate'):
                await self.complete()
        self.assertEqual((self.commands.tests, self.commands.merges), (1, 0))

    async def test_runner_changes_require_local_and_remote_runner_checks(self):
        path = self.source / '.github/workflows/remediation-runner.yml'
        path.parent.mkdir(parents=True)
        path.write_text('name: fixture\n')
        self.git('add', '.', cwd=self.source)
        self.git('commit', '-m', 'workflow', cwd=self.source)
        self.commands.pr_data['headRefOid'] = self.git('rev-parse', 'HEAD', cwd=self.source)
        self.commands.pushed_head = self.commands.pr_data['headRefOid']
        self.config = replace(self.config, ci_timeout_seconds=1, ci_poll_seconds=1)
        self.runner = PullRequestRunner(self.config, self.commands)
        original = self.commands.run
        local_calls = []

        async def runner_check(args, **kwargs):
            if tuple(args) == ('make', 'verify-runner'):
                local_calls.append(args)
                return 0, 'Runner checks passed'
            return await original(args, **kwargs)

        with patch.object(self.commands, 'run', side_effect=runner_check):
            with self.assertRaisesRegex(GateError, 'CI deadline'):
                await self.complete()
            self.assertEqual(self.commands.merges, 0)
            self.commands.pr_data['statusCheckRollup'].append(dict(name='Langflow runner tests',
                status='COMPLETED', conclusion='SUCCESS'))
            await self.complete()
        self.assertEqual(len(local_calls), 1)
        self.assertEqual(self.commands.merges, 1)

    async def test_budget_extension_retains_spent_calls_and_history(self):
        self.commands.fail_ci = True
        with self.assertRaises(GateError):
            await self.complete()
        before = self.runner.store.read('completion-pr-122')
        self.runner.extend_pr_budget(122, calls=1, seconds=60, reason='Inspected failure')
        after = self.runner.store.read('completion-pr-122')
        self.assertEqual(after['provider_calls'], before['provider_calls'])
        self.assertEqual(after['deadline'], before['deadline'] + 60)
        self.assertEqual(after['max_provider_calls'], before['max_provider_calls'] + 1)
        self.assertEqual(len(after['budget_extensions']), 1)
        self.assertEqual(self.runner.report_pr(122)['pr'], 122)

    async def test_budget_exhausted_repair_resumes_after_extension_without_extra_round(self):
        self.runner = PullRequestRunner(replace(self.config, max_provider_calls=1), self.commands)
        self.commands.reject_reviews = 1
        with self.assertRaisesRegex(GateError, 'call budget exhausted'):
            await self.complete()
        before = self.runner.store.read('completion-pr-122')
        self.assertEqual(before['stage'], 'implement')
        self.runner.extend_pr_budget(122, calls=2, reason='Reviewed rejection; authorize repair and review')
        await self.complete()
        after = self.runner.store.read('completion-pr-122')
        self.assertEqual(after['repairs'], 1)
        self.assertEqual(after['provider_calls'], 3)
        self.assertEqual(self.commands.merges, 1)

    async def test_cancelled_repair_resumes_with_spent_invocation_retained(self):
        self.commands.reject_reviews = 1
        original = self.commands.run

        async def cancel(args, **kwargs):
            result = await original(args, **kwargs)
            if args[0] == 'claude' and '-p' in args and 'Edit' in args[args.index('--tools') + 1]:
                raise asyncio.CancelledError()
            return result

        with patch.object(self.commands, 'run', side_effect=cancel):
            with self.assertRaises(asyncio.CancelledError):
                await self.complete()
        await self.complete()
        record = self.runner.store.read('completion-pr-122')
        self.assertEqual(record['repairs'], 1)
        self.assertEqual(record['provider_calls'], 4)
        self.assertEqual(record['provider_invocations'][1]['status'], 'cancelled')
        self.assertEqual(self.commands.merges, 1)

    async def test_no_change_repair_requires_audited_retry_and_spends_new_round(self):
        self.commands.reject_reviews = 1
        original = self.commands.run

        async def no_change(args, **kwargs):
            result = await original(args, **kwargs)
            if args[0] == 'claude' and '-p' in args and 'Edit' in args[args.index('--tools') + 1]:
                (kwargs['cwd'] / 'feature.txt').write_text('External contribution\n')
            return result

        with patch.object(self.commands, 'run', side_effect=no_change):
            with self.assertRaisesRegex(GateError, 'no changes'):
                await self.complete()
        with self.assertRaisesRegex(GateError, 'retry-pr-repair'):
            await self.complete()
        self.runner.retry_pr_repair(122, reason='Reviewed missing edge case; try a concrete fix')
        await self.complete()
        record = self.runner.store.read('completion-pr-122')
        self.assertEqual(record['repairs'], 2)
        self.assertEqual(len(record['repair_retry_history']), 1)

    async def test_preflight_failure_releases_slot_without_a_completion_journal(self):
        from unittest.mock import AsyncMock
        with patch.object(self.runner, 'doctor', AsyncMock(side_effect=GateError('Unavailable tool'))):
            with self.assertRaisesRegex(GateError, 'Unavailable'):
                await self.complete()
        self.assertEqual(self.runner.store.read('active'), {})
        self.assertFalse(self.runner.store.read('completion-pr-122'))

    async def test_closed_pr_can_be_abandoned_without_modifying_github_or_queue_control(self):
        self.commands.fail_ci = True
        with self.assertRaises(GateError):
            await self.complete()
        self.runner.store.stop_after_current()
        control = self.runner.store.read('control')
        self.commands.pr_data['state'] = 'CLOSED'
        result = await self.runner.abandon_pr(122, reason='Maintainer closed the superseded PR')
        self.assertEqual(result['status'], 'abandoned')
        self.assertEqual(self.runner.store.read('active'), {})
        self.assertEqual(self.runner.store.read('control'), control)
        record = self.runner.store.read('completion-pr-122')
        self.assertEqual(record['provider_calls'], 1)
        self.assertTrue(record['receipts'])
        self.commands.pr_data['state'] = 'OPEN'
        with self.assertRaisesRegex(GateError, 'explicitly abandoned'):
            await self.complete()
        self.assertEqual(self.commands.merges, 0)

    async def test_abandon_reconciles_a_confirmed_merge_instead(self):
        await self.complete()
        result = await self.runner.abandon_pr(122, reason='Inspect completed work')
        self.assertEqual(result['status'], 'merged')
        self.assertNotIn('abandon_reason', self.runner.store.read('completion-pr-122'))

    async def test_contract_and_guidance_tampering_block_resume(self):
        self.commands.fail_ci = True
        with self.assertRaises(GateError):
            await self.complete()
        record = self.runner.store.read('completion-pr-122')
        record['documents'][0]['content'] = 'Changed instructions'
        self.runner.save(record)
        self.commands.fail_ci = False
        with self.assertRaisesRegex(GateError, 'guidance changed'):
            await self.complete()
        self.assertEqual(self.commands.merges, 0)

    async def test_lost_merge_response_reconciles_after_deadline_without_another_merge(self):
        original = self.commands.run

        async def lost(args, **kwargs):
            result = await original(args, **kwargs)
            if tuple(args[:3]) == ('gh', 'pr', 'merge'):
                raise asyncio.CancelledError()
            return result

        with patch.object(self.commands, 'run', side_effect=lost):
            with self.assertRaises(asyncio.CancelledError):
                await self.complete()
        record = self.runner.store.read('completion-pr-122')
        record['deadline'] = 0
        self.runner.save(record)
        self.assertEqual((await self.complete())['status'], 'merged')
        self.assertEqual(self.commands.merges, 1)

    async def test_lost_push_response_reuses_review_without_provider_call(self):
        self.commands.reject_reviews = 1
        original = self.commands.run

        async def lost(args, **kwargs):
            result = await original(args, **kwargs)
            if tuple(args[:2]) == ('git', 'push'):
                raise asyncio.CancelledError()
            return result

        with patch.object(self.commands, 'run', side_effect=lost):
            with self.assertRaises(asyncio.CancelledError):
                await self.complete()
        counts = self.commands.tests, self.commands.reviews
        await self.complete()
        self.assertEqual((self.commands.tests, self.commands.reviews), counts)
        self.assertEqual(self.commands.merges, 1)

    async def prepare_ticket(self):
        # Build a genuine ticket execution, including frozen acceptance receipts.
        self.config = replace(self.config, completion_target='pr')
        ticket = Runner(self.config, self.commands)
        manifest = self.root / 'manifest.json'
        manifest.write_text(json.dumps(fixtures.fixture_manifest(95)))
        await ticket.freeze(95, manifest)
        self.commands.pr_data = None
        self.with_criteria = True
        await ticket.run_issue(95, execute=True)
        self.commands.pr_data.update(isCrossRepository=False, title='Ticket fix', body='Fixes #95')
        self.commands.pr_data['headRefName'] = 'automation/remediate-95'
        source = ticket.store.read('issue-95')
        self.runner = PullRequestRunner(self.config, self.commands)
        ticket.store.stop_after_current()
        return ticket, source

    async def test_exact_pr_ready_promotion_reuses_verified_evidence_and_keeps_history(self):
        ticket, source = await self.prepare_ticket()
        counts = self.commands.tests, self.commands.reviews
        await self.complete()
        self.assertEqual((self.commands.tests, self.commands.reviews), counts)
        self.assertEqual(ticket.store.read('issue-95')['stage'], 'merged')
        self.assertEqual(ticket.store.read('issue-95')['provider_calls'], source['provider_calls'])
        self.assertTrue(ticket.store.read('control')['stopped'])
        self.assertTrue(self.runner.store.read('completion-pr-122')['reuse_ticket'])

    async def test_changed_pr_ready_candidate_revalidates_and_reconciles_original_ticket(self):
        ticket, source = await self.prepare_ticket()
        worktree = Path(source['worktree'])
        (worktree / 'feature.txt').write_text('Inspected external improvement\n')
        self.git('add', '.', cwd=worktree)
        self.git('commit', '-m', 'external improvement', cwd=worktree)
        self.commands.pr_data['headRefOid'] = self.git('rev-parse', 'HEAD', cwd=worktree)
        self.commands.pushed_head = self.commands.pr_data['headRefOid']
        await self.complete()
        original = ticket.store.read('issue-95')
        self.assertEqual(original['head'], source['head'])
        self.assertEqual(original['receipts'], source['receipts'])
        self.assertNotEqual(original['completion_result']['head'], source['head'])
        self.assertEqual((await ticket.run_issue(95, execute=True))['status'], 'merged')
        self.assertEqual(ticket.report(95)['status'], 'merged')
        self.assertEqual((self.commands.tests, self.commands.reviews, self.commands.merges), (2, 2, 1))

    async def test_serialized_pr_graph_dispatches_explicit_parameters(self):
        from gophercrm_runner.flows import run_pr_flow
        policy = self.root / 'policy.json'
        policy.write_text(json.dumps(asdict(self.config), default=str))
        with patch('gophercrm_runner.engine.Commands', return_value=self.commands):
            result = await run_pr_flow(policy, number=122, head=self.head, execute=True)
        self.assertIn('merged', json.dumps([r.model_dump(mode='json') for r in result]))
        self.assertEqual(self.commands.merges, 1)
