"""Audited adoption of a maintainer commit into an unfinished ticket execution."""

from contextlib import redirect_stdout
from dataclasses import replace
import io
import json
from pathlib import Path
import subprocess
import sys
import unittest
from unittest.mock import AsyncMock, patch

import test_runner as fixtures
from gophercrm_runner.config import EnvironmentError, GateError
from gophercrm_runner.engine import Runner
from gophercrm_runner.validation_review import confirm


def git(cwd, *args):
    return subprocess.check_output(['git', *args], cwd=cwd, stderr=subprocess.DEVNULL, text=True).strip()


class CommitAdoptionTests(unittest.IsolatedAsyncioTestCase):
    setUp = fixtures.EngineTests.setUp
    tearDown = fixtures.EngineTests.tearDown

    async def asyncSetUp(self):
        await fixtures.EngineTests.asyncSetUp(self)

    async def exhausted(self):
        """Every review rejects until the repair budget is spent; the ticket stays admitted."""
        self.commands.reject_reviews = 100
        with self.assertRaisesRegex(GateError, 'Repair budget exhausted'):
            await self.runner.run_issue(95, execute=True)
        record = self.runner.store.journal(95)
        self.assertEqual((record['stage'], record['repairs']), ('repair_pending', 2))
        self.assertEqual(self.commands.implementations, 3)
        return record

    def maintainer_commit(self, record, files, message='Maintainer correction'):
        worktree = Path(record['worktree'])
        for name, content in files.items():
            path = worktree / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content)
        git(worktree, 'add', '--all')
        git(worktree, 'commit', '-m', message)
        return git(worktree, 'rev-parse', 'HEAD')

    async def assert_refused(self, pattern, head, before, reason='Maintainer fixed the review finding.'):
        with self.assertRaisesRegex(GateError, pattern):
            await self.runner.adopt_commit(95, head=head, reason=reason)
        self.assertEqual(self.runner.store.journal(95), before)

    async def test_adopts_maintainer_commit_without_spending_budgets(self):
        before = await self.exhausted()
        head = self.maintainer_commit(before, {'feature.txt': 'Maintainer implementation\n'})
        result = await self.runner.adopt_commit(95, head=head, reason='Corrected the rejected edge case by hand.')
        record = self.runner.store.journal(95)
        self.assertEqual(result['issue'], 95)
        self.assertEqual((result['head'], result['stage']), (head, 'validate'))
        self.assertEqual((record['head'], record['stage']), (head, 'validate'))
        for key in ('tested_head', 'reviewed_head', 'review', 'review_receipt', 'pending_review', 'last_error'):
            self.assertIsNone(record.get(key), key)
        self.assertEqual(record['receipts'], {})
        for key in ('provider_calls', 'max_provider_calls', 'repairs', 'deadline', 'base', 'execution_id',
                    'provider_invocations', 'attempts'):
            self.assertEqual(record[key], before[key], key)
        entry = record['adoptions'][-1]
        self.assertEqual({k: entry[k] for k in ('from_head', 'to_head', 'reason')},
                         dict(from_head=before['head'], to_head=head,
                              reason='Corrected the rejected edge case by hand.'))
        self.assertIsInstance(entry['at'], float)
        self.assertEqual(result['adoption'], entry)
        self.assertEqual(self.commands.implementations, 3)

    async def test_run_after_adoption_validates_reviews_and_merges_without_a_repair(self):
        before = await self.exhausted()
        head = self.maintainer_commit(before, {'feature.txt': 'Maintainer implementation\n'})
        await self.runner.adopt_commit(95, head=head, reason='Corrected the rejected edge case by hand.')
        tests, reviews = self.commands.tests, self.commands.reviews
        self.commands.reject_reviews = reviews  # The next independent review approves.
        result = await self.runner.run_issue(95, execute=True)
        self.assertEqual(result['status'], 'merged')
        final = self.runner.store.journal(95)
        self.assertEqual(self.commands.implementations, 3)
        self.assertEqual((self.commands.tests, self.commands.reviews), (tests + 1, reviews + 1))
        self.assertEqual(final['repairs'], before['repairs'])
        self.assertEqual(final['provider_calls'], before['provider_calls'] + 1)
        self.assertEqual((final['head'], final['tested_head'], final['reviewed_head']), (head, head, head))
        self.assertEqual(self.commands.pushed_head, head)
        self.assertEqual((self.commands.creates, self.commands.merges), (1, 1))

    async def test_adopted_commit_still_requires_validation_plan_confirmation(self):
        self.config = replace(self.config, validation_review_required=True)
        self.runner = Runner(self.config, self.commands)
        record = await self.runner.prepare(await self.runner.issue(95))
        commit = self.maintainer_commit(record, {'feature.txt': 'Implementation\n'}, 'Implementation')
        record.update(head=commit, stage='validate')
        self.runner.save(record)
        head = self.maintainer_commit(record, {'feature.txt': 'Maintainer implementation\n'})
        await self.runner.adopt_commit(95, head=head, reason='Corrected by hand.')
        with self.assertRaisesRegex(EnvironmentError, 'operator inspection'):
            await self.runner.run_issue(95, execute=True)
        held = self.runner.store.journal(95)
        self.assertEqual((held['stage'], held['validation_inspection']['head'], self.commands.tests),
                         ('validate', head, 0))
        with patch('gophercrm_runner.engine.Runner', return_value=self.runner):
            await confirm(self.config, 95, head=head, plan_hash=held['validation_inspection']['plan_hash'],
                          reason='Inspected the adopted diff and the complete command plan')
        result = await self.runner.run_issue(95, execute=True)
        self.assertEqual(result['status'], 'merged')
        self.assertEqual((self.commands.implementations, self.commands.tests), (0, 1))

    async def advance_main(self, files):
        for name, content in files.items():
            (self.checkout / name).write_text(content)
        git(self.checkout, 'add', '--all')
        git(self.checkout, 'commit', '-m', 'Advance main')
        git(self.checkout, 'update-ref', 'refs/remotes/origin/main', 'HEAD')
        return git(self.checkout, 'rev-parse', 'HEAD')

    async def test_resolved_main_merge_after_exhausted_budget_is_adopted_against_the_new_base(self):
        before = await self.exhausted()
        target = await self.advance_main({'feature.txt': 'Conflicting main change\n', 'other.txt': 'Main only\n'})
        with self.assertRaisesRegex(GateError, 'merge conflicts'):
            await self.runner.revalidate(95)
        conflicted = self.runner.store.journal(95)
        self.assertEqual((conflicted['stage'], conflicted['base'], conflicted['head']),
                         ('repair_pending', target, before['head']))
        worktree = Path(before['worktree'])
        (worktree / 'feature.txt').write_text('Maintainer resolution\n')
        git(worktree, 'add', 'feature.txt')
        git(worktree, 'commit', '--no-edit')
        head = git(worktree, 'rev-parse', 'HEAD')
        self.assertEqual(git(worktree, 'show', '-s', '--format=%P', 'HEAD').split(), [before['head'], target])
        await self.runner.adopt_commit(95, head=head, reason='Resolved the main merge conflict by hand.')
        await self.runner.freeze(95, self.root / 'manifest-95.json', refresh=True)
        self.commands.reject_reviews = self.commands.reviews
        self.commands.base = target  # GitHub reports the advanced main as the PR base.
        result = await self.runner.run_issue(95, execute=True)
        self.assertEqual(result['status'], 'merged')
        final = self.runner.store.journal(95)
        self.assertEqual((final['base'], final['head'], final['repairs']), (target, head, 2))
        self.assertEqual(self.commands.implementations, 3)

    async def test_hand_merge_of_main_is_refused_against_the_recorded_base(self):
        # Main's own changes appear in the diff from the old base; revalidate records the new base.
        before = await self.exhausted()
        target = await self.advance_main({'other.txt': 'Main only\n'})
        worktree = Path(before['worktree'])
        git(worktree, 'merge', '--no-edit', target)
        head = git(worktree, 'rev-parse', 'HEAD')
        await self.assert_refused(r'outside frozen task scope: other\.txt', head, before)

    async def test_refuses_dirty_worktree_and_head_mismatch(self):
        before = await self.exhausted()
        head = self.maintainer_commit(before, {'feature.txt': 'Maintainer implementation\n'})
        (Path(before['worktree']) / 'feature.txt').write_text('Uncommitted\n')
        await self.assert_refused('Worktree changed', head, before)
        git(before['worktree'], 'checkout', '--', 'feature.txt')
        await self.assert_refused('HEAD differs', before['head'], before)
        await self.assert_refused('full commit', head[:12], before)
        await self.assert_refused('short reason', head, before, reason='  ')

    async def test_refuses_a_head_that_does_not_descend_from_the_recorded_head(self):
        before = await self.exhausted()
        git(before['worktree'], 'checkout', '--detach', before['base'])
        head = self.maintainer_commit(before, {'feature.txt': 'Unrelated history\n'})
        await self.assert_refused('descendant', head, before)

    async def test_refuses_protected_paths(self):
        for name in ('automation/langflow/gophercrm_runner/gates.py', 'Makefile', '.github/workflows/ci.yml'):
            with self.subTest(path=name):
                await self.asyncTearDownAndSetUp()
                before = await self.exhausted()
                head = self.maintainer_commit(before, {name: 'weakened\n'})
                await self.assert_refused('protected or private path|existing validation gate', head, before)

    async def asyncTearDownAndSetUp(self):
        self.tearDown()
        self.setUp()
        await self.asyncSetUp()

    async def test_refuses_out_of_scope_paths_and_points_at_refresh(self):
        before = await self.exhausted()
        head = self.maintainer_commit(before, {'outside.txt': 'Not in the frozen scope\n'})
        await self.assert_refused(r'outside frozen task scope: outside\.txt.*freeze --refresh', head, before)

    async def test_refuses_finished_legacy_mismatched_and_uncommitted_executions(self):
        before = await self.exhausted()
        head = self.maintainer_commit(before, {'feature.txt': 'Maintainer implementation\n'})
        for stage in ('merged', 'pr_ready', 'abandoned', 'retired', 'no_change', 'preparing',
                      'committing', 'publishing', 'merging', 'revalidating'):
            changed = dict(before, stage=stage)
            self.runner.save(changed)
            with self.subTest(stage=stage):
                await self.assert_refused('unfinished execution', head, changed)
        uncommitted = dict(before, stage='implement', head=before['base'])
        self.runner.save(uncommitted)
        await self.assert_refused('unfinished execution', head, uncommitted)
        mismatched = dict(before, policy='another-policy')
        self.runner.save(mismatched)
        await self.assert_refused('unfinished execution', head, mismatched)
        running = dict(before, provider_invocations=before['provider_invocations'] + [
            dict(id='orphan', status='running', pid=123)])
        self.runner.save(running)
        await self.assert_refused('provider', head, running)
        with self.assertRaisesRegex(GateError, 'unfinished execution'):
            await self.runner.adopt_commit(96, head=head, reason='No journal exists.')

    async def test_recorded_pr_must_already_carry_the_adopted_head(self):
        self.commands.fail_ci = True
        with self.assertRaisesRegex(GateError, 'CI failed'):
            await self.runner.run_issue(95, execute=True)
        before = self.runner.store.journal(95)
        self.assertEqual((before['stage'], before['pr']), ('ci', 122))
        head = self.maintainer_commit(before, {'feature.txt': 'Maintainer CI correction\n'})
        await self.assert_refused('pull request.*head', head, before)
        self.commands.pr_data['headRefOid'] = head  # The maintainer pushed the correction.
        await self.runner.adopt_commit(95, head=head, reason='Fixed the CI regression by hand.')
        self.commands.fail_ci = False
        result = await self.runner.run_issue(95, execute=True)
        self.assertEqual(result['status'], 'merged')
        final = self.runner.store.journal(95)
        self.assertEqual((final['head'], final['reviewed_head'], final['repairs']), (head, head, 0))
        self.assertEqual((self.commands.implementations, self.commands.creates, self.commands.merges), (1, 1, 1))

    async def test_repeated_adoption_of_the_same_head_is_idempotent(self):
        before = await self.exhausted()
        head = self.maintainer_commit(before, {'feature.txt': 'Maintainer implementation\n'})
        first = await self.runner.adopt_commit(95, head=head, reason='Corrected by hand.')
        after = self.runner.store.journal(95)
        repeat = await self.runner.adopt_commit(95, head=head, reason='Repeat after a lost response.')
        self.assertEqual(repeat, first)
        self.assertEqual(self.runner.store.journal(95), after)

    def test_adopt_commit_cli_passes_head_and_reason(self):
        from gophercrm_runner import cli
        head = 'a' * 40
        argv = ['runner', 'adopt-commit', '--config', 'unused', '--issue', '95', '--head', head,
                '--reason', 'Corrected by hand.']
        output = io.StringIO()
        with patch('gophercrm_runner.config.Config.load', return_value=self.config), \
                patch.object(Runner, 'adopt_commit', new_callable=AsyncMock,
                             return_value={'issue': 95, 'stage': 'validate'}) as adopt, \
                patch.object(sys, 'argv', argv), redirect_stdout(output):
            cli.main()
        adopt.assert_awaited_once_with(95, head=head, reason='Corrected by hand.')
        self.assertEqual(json.loads(output.getvalue())['stage'], 'validate')
