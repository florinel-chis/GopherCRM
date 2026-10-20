"""Existing-PR completion uses real Git and deterministic external boundaries."""

import asyncio
from dataclasses import asdict, replace
import json
from pathlib import Path
import shutil
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

    def test_setup_defaults_to_full_cycle_without_overwriting_existing_pilot_policy(self):
        from gophercrm_runner.local import setup
        directory = self.root / 'installation'
        setup(self.checkout, directory)
        policy = directory / 'config.json'
        value = json.loads(policy.read_text())
        self.assertEqual(value['completion_target'], 'merge')
        value['completion_target'] = 'pr'
        policy.write_text(json.dumps(value))
        before = policy.read_bytes()
        setup(self.checkout, directory)
        self.assertEqual(policy.read_bytes(), before)

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
        self.assertEqual(len(after['history'][0]['provider_invocations']), before['provider_calls'])

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
        rejected = self.runner.store.read('completion-pr-122')
        original_path = Path(rejected['worktree'])
        await self.runner.restart_pr_repair(122, reason='Retain and reject the unauthorized Makefile change')
        restarted = self.runner.store.read('completion-pr-122')
        self.assertNotEqual(restarted['worktree'], rejected['worktree'])
        self.assertEqual((original_path / 'Makefile').read_text(), 'verify:\n\ttrue\n')
        self.assertFalse((Path(restarted['worktree']) / 'Makefile').exists())
        await self.complete()
        final = self.runner.store.read('completion-pr-122')
        self.assertEqual(final['repairs'], 1)
        self.assertEqual(final['provider_calls'], 4)
        self.assertEqual(self.commands.merges, 1)

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

    async def test_dirty_cancelled_repair_can_refresh_after_main_advance_without_losing_work(self):
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
        before = self.runner.store.read('completion-pr-122')
        old_file = Path(before['worktree']) / 'feature.txt'
        old_bytes = old_file.read_bytes()
        (self.checkout / 'advanced.txt').write_text('new base\n')
        self.git('add', '.')
        self.git('commit', '-m', 'advance main')
        base = self.git('rev-parse', 'HEAD')
        self.git('update-ref', 'refs/remotes/origin/main', base)
        self.git('merge', '--no-edit', base, cwd=self.source)
        self.commands.pr_data.update(baseRefOid=base, headRefOid=self.git('rev-parse', 'HEAD', cwd=self.source))
        self.commands.pushed_head = self.commands.pr_data['headRefOid']
        await self.complete(refresh=True)
        after = self.runner.store.read('completion-pr-122')
        self.assertEqual(old_file.read_bytes(), old_bytes)
        self.assertEqual(after['deadline'], before['deadline'])
        self.assertEqual(after['repairs'], before['repairs'])
        self.assertEqual(after['history'][-1]['worktree'], before['worktree'])
        self.assertEqual(self.commands.merges, 1)

    async def test_restart_worktree_response_loss_resumes_without_losing_repair_round(self):
        self.commands.reject_reviews = 1
        original = self.commands.run

        async def cancel_agent(args, **kwargs):
            result = await original(args, **kwargs)
            if args[0] == 'claude' and '-p' in args and 'Edit' in args[args.index('--tools') + 1]:
                raise asyncio.CancelledError()
            return result

        with patch.object(self.commands, 'run', side_effect=cancel_agent):
            with self.assertRaises(asyncio.CancelledError):
                await self.complete()

        async def lose_worktree_response(args, **kwargs):
            result = await original(args, **kwargs)
            if tuple(args[:3]) == ('git', 'worktree', 'add'):
                raise asyncio.CancelledError()
            return result

        with patch.object(self.commands, 'run', side_effect=lose_worktree_response):
            with self.assertRaises(asyncio.CancelledError):
                await self.runner.restart_pr_repair(122, reason='Retain interrupted repair and restart cleanly')
        self.assertEqual(self.runner.store.read('completion-pr-122')['stage'], 'preparing_repair')
        await self.complete()
        self.assertEqual(self.runner.store.read('completion-pr-122')['repairs'], 1)
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

    async def test_abandon_cannot_downgrade_a_completed_merge_when_receipts_become_unavailable(self):
        await self.complete()
        record = self.runner.store.read('completion-pr-122')
        (self.config.state_dir / record['review_receipt']['path']).unlink()
        result = await self.runner.abandon_pr(122, reason='Already completed; release any remaining admission')
        self.assertTrue(result['previously_completed'])
        self.assertEqual(self.runner.store.read('completion-pr-122')['stage'], 'merged')
        self.assertNotIn('abandon_reason', self.runner.store.read('completion-pr-122'))

    async def test_external_merge_does_not_release_an_unresolved_orphaned_provider(self):
        self.commands.fail_ci = True
        with self.assertRaises(GateError):
            await self.complete()
        record = self.runner.store.read('completion-pr-122')
        record['provider_invocations'][0]['status'] = 'running'
        self.runner.save(record)
        self.commands.pr_data.update(state='MERGED', mergeCommit={'oid': 'external-merge'})
        with self.assertRaisesRegex(GateError, 'orphaned provider'):
            await self.complete()
        self.assertEqual(self.runner.store.read('active'), {'pr': 122})
        self.assertEqual(self.runner.store.read('completion-pr-122')['stage'], record['stage'])
        await self.runner.recover_pr(122, reason='Inspected and stopped the orphan', processes_stopped=True)
        self.assertEqual((await self.complete())['status'], 'external_merge')
        self.assertEqual(self.runner.store.read('active'), {})

    async def test_external_merge_at_another_head_can_release_slot_without_claiming_validation(self):
        self.commands.fail_ci = True
        with self.assertRaises(GateError):
            await self.complete()
        before = self.runner.store.read('completion-pr-122')
        self.commands.pr_data.update(state='MERGED', headRefOid='f' * 40, mergeCommit={'oid': 'external-merge'})
        self.assertEqual((await self.complete())['status'], 'external_merge')
        result = await self.runner.abandon_pr(122, reason='Inspected maintainer merge at a different head')
        self.assertEqual(result['status'], 'abandoned')
        after = self.runner.store.read('completion-pr-122')
        self.assertEqual(after['head'], before['head'])
        self.assertEqual(after['receipts'], before['receipts'])
        self.assertFalse(after['remote_disposition']['validated_by_this_completion'])
        self.assertEqual(after['remote_disposition']['merge_commit'], 'external-merge')
        self.assertNotIn('merge_commit', after)
        self.assertEqual(self.runner.store.read('active'), {})

    async def test_external_merge_of_matching_unvalidated_head_does_not_complete_source_ticket(self):
        from unittest.mock import AsyncMock
        ticket, source = await self.prepare_ticket()
        worktree = Path(source['worktree'])
        (worktree / 'feature.txt').write_text('Untested external change\n')
        self.git('add', '.', cwd=worktree)
        self.git('commit', '-m', 'unvalidated', cwd=worktree)
        self.commands.pr_data['headRefOid'] = self.git('rev-parse', 'HEAD', cwd=worktree)
        self.commands.pushed_head = self.commands.pr_data['headRefOid']
        with patch.object(self.runner, 'validate', AsyncMock(side_effect=GateError('Validation blocked'))):
            with self.assertRaisesRegex(GateError, 'Validation blocked'):
                await self.complete()
        self.commands.pr_data.update(state='MERGED', mergeCommit={'oid': 'external-merge'})
        result = await self.complete()
        self.assertEqual(result['status'], 'external_merge')
        self.assertFalse(result['validated_by_this_completion'])
        self.assertNotIn('completion_result', ticket.store.read('issue-95'))
        self.assertEqual(ticket.store.read('issue-95')['stage'], 'pr_ready')
        self.assertEqual(self.runner.store.read('active'), {})
        self.assertNotIn('merge_commit', self.runner.store.read('completion-pr-122'))

    async def test_matching_remote_merge_rechecks_approval_receipts(self):
        self.commands.fail_ci = True
        with self.assertRaises(GateError):
            await self.complete()
        record = self.runner.store.read('completion-pr-122')
        record['stage'] = 'merging'
        self.runner.save(record)
        (self.config.state_dir / record['review_receipt']['path']).write_text('{}')
        self.commands.pr_data.update(state='MERGED', mergeCommit={'oid': 'external-merge'})
        with self.assertRaisesRegex(GateError, 'digest'):
            await self.complete()
        self.assertEqual(self.runner.store.read('completion-pr-122')['stage'], 'merging')
        result = await self.runner.abandon_pr(122, reason='Merged externally; retained approval proof is corrupt')
        self.assertEqual(result['status'], 'abandoned')
        self.assertFalse(self.runner.store.read('completion-pr-122')['remote_disposition']['validated_by_this_completion'])
        self.assertEqual(self.runner.store.read('active'), {})

    async def lost_merge_response(self):
        original = self.commands.run

        async def lost(args, **kwargs):
            result = await original(args, **kwargs)
            if tuple(args[:3]) == ('gh', 'pr', 'merge'):
                raise asyncio.CancelledError()
            return result

        with patch.object(self.commands, 'run', side_effect=lost):
            with self.assertRaises(asyncio.CancelledError):
                await self.complete()
        return self.runner.store.read('completion-pr-122')

    async def test_missing_merged_candidate_worktree_can_be_disposed_without_claiming_validation(self):
        record = await self.lost_merge_response()
        shutil.rmtree(record['worktree'])
        with self.assertRaisesRegex(GateError, 'evidence is unavailable'):
            await self.complete()
        result = await self.runner.abandon_pr(122, reason='Missing candidate directory prevents proof verification')
        self.assertEqual(result['status'], 'abandoned')
        self.assertEqual(self.runner.store.read('active'), {})
        self.assertFalse(self.runner.store.read('completion-pr-122')['remote_disposition']['validated_by_this_completion'])

    async def test_missing_reused_ticket_worktree_does_not_hold_pr_slot_forever(self):
        ticket, source = await self.prepare_ticket()
        await self.lost_merge_response()
        shutil.rmtree(source['worktree'])
        with self.assertRaisesRegex(GateError, 'evidence is unavailable'):
            await self.complete()
        await self.runner.abandon_pr(122, reason='Source ticket checkout was removed; retained proof is unverified')
        self.assertEqual(self.runner.store.read('active'), {})
        self.assertEqual(ticket.store.read('issue-95')['stage'], 'pr_ready')
        self.assertNotIn('completion_result', ticket.store.read('issue-95'))

    async def test_remote_merge_cannot_verify_changed_frozen_approval_inputs(self):
        record = await self.lost_merge_response()
        record['pr_input']['body'] = 'Changed acceptance objective'
        self.runner.save(record)
        with self.assertRaisesRegex(GateError, 'Approval contract'):
            await self.complete()
        self.assertEqual(self.runner.store.read('completion-pr-122')['stage'], 'merging')

    async def test_orphaned_provider_requires_explicit_inspected_process_acknowledgement(self):
        self.commands.fail_ci = True
        with self.assertRaises(GateError):
            await self.complete()
        record = self.runner.store.read('completion-pr-122')
        record['provider_invocations'][0]['status'] = 'running'
        self.runner.save(record)
        with self.assertRaisesRegex(GateError, 'processes-stopped'):
            await self.runner.abandon_pr(122, reason='Not yet inspected')
        self.assertEqual(self.runner.store.read('active'), {'pr': 122})
        await self.runner.abandon_pr(122, reason='Confirmed orphaned reviewer has stopped', processes_stopped=True)
        after = self.runner.store.read('completion-pr-122')
        self.assertEqual(after['provider_calls'], record['provider_calls'])
        self.assertEqual(after['provider_invocations'][0]['status'], 'operator-resolved')
        self.assertIsNone(after['provider_invocations'][0]['ended_at'])
        self.assertTrue(after['provider_dispositions'][0]['operator_confirmed_processes_stopped'])
        self.assertEqual(self.runner.store.read('active'), {})

    async def test_orphaned_review_can_recover_and_resume_without_refunding_spent_call(self):
        self.commands.fail_ci = True
        with self.assertRaises(GateError):
            await self.complete()
        record = self.runner.store.read('completion-pr-122')
        record.update(stage='review', reviewed_head=None, review=None, review_receipt=None)
        record['provider_invocations'][0]['status'] = 'running'
        self.runner.save(record)
        with self.assertRaisesRegex(GateError, 'processes-stopped'):
            await self.runner.recover_pr(122, reason='No acknowledgement yet')
        await self.runner.recover_pr(122, reason='Verified crashed reviewer stopped', processes_stopped=True)
        recovered = self.runner.store.read('completion-pr-122')
        self.assertEqual(recovered['provider_calls'], record['provider_calls'])
        self.assertEqual(recovered['deadline'], record['deadline'])
        self.assertEqual(recovered['receipts'], record['receipts'])
        self.assertEqual(self.runner.store.read('active'), {'pr': 122})
        self.commands.fail_ci = False
        await self.complete()
        final = self.runner.store.read('completion-pr-122')
        self.assertEqual(final['provider_calls'], 2)
        self.assertEqual(final['provider_invocations'][0]['status'], 'operator-resolved')
        self.assertEqual(len(final['operator_recoveries']), 1)
        self.assertEqual(self.commands.merges, 1)

    async def test_journalless_admission_can_recover_then_complete_same_pr(self):
        self.runner.store.admit_pr(122)
        await self.runner.recover_pr(122, reason='Inspected stopped worker before candidate admission',
                                     processes_stopped=True)
        self.assertEqual(self.runner.store.read('active'), {})
        self.assertFalse(self.runner.store.read('completion-pr-122'))
        self.assertEqual(len(self.runner.store.read('pr-admission-recovery-122')['entries']), 1)
        await self.complete()
        self.assertEqual(self.commands.merges, 1)

    async def test_recovery_cannot_refund_or_reroll_a_recorded_rejection(self):
        self.commands.reject_reviews = 100
        with self.assertRaises(GateError):
            await self.complete()
        before = self.runner.store.read('completion-pr-122')
        with self.assertRaisesRegex(GateError, 'No unresolved'):
            await self.runner.recover_pr(122, reason='Attempt to reroll rejection', processes_stopped=True)
        after = self.runner.store.read('completion-pr-122')
        self.assertEqual(after, before)

    async def test_journalless_admission_after_hard_crash_can_be_abandoned_when_pr_closed(self):
        self.runner.store.admit_pr(122)
        self.commands.pr_data['state'] = 'CLOSED'
        await self.runner.abandon_pr(122, reason='Recovered interrupted admission after worker crash')
        record = self.runner.store.read('completion-pr-122')
        self.assertTrue(record['interrupted_admission'])
        self.assertNotIn('head', record)
        self.assertNotIn('tested_head', record)
        self.assertEqual(self.runner.store.read('active'), {})
        self.assertEqual(self.runner.report_pr(122)['status'], 'abandoned')

    async def test_journalless_admission_external_merge_is_not_claimed_as_validated(self):
        self.runner.store.admit_pr(122)
        self.commands.pr_data.update(state='MERGED', mergeCommit={'oid': 'external-merge'})
        await self.runner.abandon_pr(122, reason='External merge after interrupted admission')
        record = self.runner.store.read('completion-pr-122')
        self.assertFalse(record['remote_disposition']['validated_by_this_completion'])
        self.assertEqual(self.runner.store.read('active'), {})
        with self.assertRaisesRegex(GateError, 'explicitly abandoned'):
            await self.complete()

    async def test_journalless_abandonment_cannot_release_another_pr_slot(self):
        self.runner.store.admit_pr(123)
        with self.assertRaisesRegex(GateError, 'No matching'):
            await self.runner.abandon_pr(122, reason='Wrong PR number')
        self.assertEqual(self.runner.store.read('active'), {'pr': 123})

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
        self.assertEqual((await self.complete())['status'], 'merged')
        self.assertEqual((self.commands.tests, self.commands.reviews), counts)

    async def test_explicit_promotion_allows_only_the_ticket_stop_target_to_change(self):
        ticket, source = await self.prepare_ticket()
        counts = self.commands.tests, self.commands.reviews
        self.runner = PullRequestRunner(replace(self.config, completion_target='merge'), self.commands)
        await self.complete()
        self.assertEqual((self.commands.tests, self.commands.reviews), counts)
        self.assertEqual(ticket.store.read('issue-95')['completion_target'], 'pr')
        self.assertEqual(ticket.store.read('issue-95')['policy'], source['policy'])
        self.assertEqual(self.commands.merges, 1)

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

    async def test_queue_edit_cannot_drop_linked_ticket_acceptance_criteria(self):
        await self.prepare_ticket()
        counts = self.commands.tests, self.commands.reviews
        self.runner = PullRequestRunner(replace(self.config, issues=(96,)), self.commands)
        with self.assertRaisesRegex(GateError, 'Restore the linked ticket'):
            await self.complete()
        self.assertEqual((self.commands.tests, self.commands.reviews), counts)
        self.assertEqual(self.commands.merges, 0)
        self.assertEqual(self.runner.store.read('active'), {})

    async def test_source_ticket_update_failure_does_not_hold_merged_pr_slot(self):
        from unittest.mock import AsyncMock
        ticket, _ = await self.prepare_ticket()
        with patch('gophercrm_runner.engine.Runner.finish', AsyncMock(side_effect=GateError('Source journal changed'))):
            result = await self.complete()
        self.assertEqual(result['status'], 'merged')
        self.assertIn('Source journal changed', result['source_reconciliation_error'])
        self.assertEqual(self.runner.store.read('active'), {})
        result = await self.complete()
        self.assertIsNone(result['source_reconciliation_error'])
        self.assertEqual(ticket.store.read('issue-95')['stage'], 'merged')
        self.assertEqual(self.commands.merges, 1)

    async def test_serialized_pr_graph_dispatches_explicit_parameters(self):
        from gophercrm_runner.flows import run_pr_flow
        policy = self.root / 'policy.json'
        policy.write_text(json.dumps(asdict(self.config), default=str))
        with patch('gophercrm_runner.engine.Commands', return_value=self.commands):
            result = await run_pr_flow(policy, number=122, head=self.head, execute=True)
        self.assertIn('merged', json.dumps([r.model_dump(mode='json') for r in result]))
        self.assertEqual(self.commands.merges, 1)
