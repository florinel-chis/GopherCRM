from dataclasses import replace
import asyncio
import json
from pathlib import Path
import subprocess
from unittest.mock import AsyncMock, patch

import test_runner as fixtures
from test_contracts import manifest
from gophercrm_runner.config import CommandError, GateError
from gophercrm_runner.engine import CIFailure, Runner


class ContractExecutionTests(fixtures.EngineTests):
    async def asyncSetUp(self):
        (self.checkout / 'AGENTS.md').write_text('Use meaningful regressions.\n')
        for args in [('add', '.'), ('commit', '-m', 'guidance'), ('update-ref', 'refs/remotes/origin/main', 'HEAD')]:
            subprocess.run(['git', *args], cwd=self.checkout, check=True, capture_output=True)
        self.commands.base = await self.runner.head(self.checkout)
        self.config = replace(self.config, acceptance_checks={'regression': {
            'argv': ['go', 'test', '-json', '-count=1', './feature'],
            'tests': [{'package': 'example/repo/feature', 'name': 'TestFeature'}]}})
        self.runner = Runner(self.config, self.commands)
        self.manifest = manifest(); self.manifest['scope_paths'] = ['feature.txt']
        self.manifest_path = self.root / 'manifest.json'
        self.manifest_path.write_text(json.dumps(self.manifest))
        await self.runner.freeze(95, self.manifest_path)
        self.commands.calls.clear()

    async def test_missing_contract_blocks_before_provider_or_worktree(self):
        config = replace(self.config, state_dir=self.root / 'unprepared-state')
        with self.assertRaisesRegex(GateError, 'contract'):
            await Runner(config, self.commands).run_issue(95, execute=True)
        self.assertEqual(self.commands.implementations, 0)
        self.assertFalse((config.state_dir / 'worktrees').exists())

    async def assert_repair_recovers_main_advance(self, trigger):
        self.config = replace(self.config, max_repair_rounds=1)
        self.runner = Runner(self.config, self.commands)
        original = getattr(self.runner, trigger)
        advanced = False
        if trigger == 'review':
            self.commands.reject_reviews = 1

        async def advance(*args):
            nonlocal advanced
            result = await original(*args)
            if not advanced:
                advanced = True
                (self.checkout / 'other.txt').write_text('Independent main change\n')
                for command in [('add', '.'), ('commit', '-m', 'advance main'),
                                ('update-ref', 'refs/remotes/origin/main', 'HEAD')]:
                    subprocess.run(['git', *command], cwd=self.checkout, check=True, capture_output=True)
                self.commands.base = await self.runner.head(self.checkout)
                if self.commands.pr_data:
                    self.commands.pr_data['baseRefOid'] = self.commands.base
                if trigger == 'validate':
                    raise CommandError('Regression failed while main advanced')
                if trigger == 'wait_ci':
                    raise CIFailure('CI regression failed while main advanced')
            return result

        setattr(self.runner, trigger, advance)
        with self.assertRaisesRegex(GateError, 'Live main advanced'):
            await self.runner.run_issue(95, execute=True)
        before = self.runner.store.read('issue-95')
        self.assertEqual((before['stage'], before['repairs']), ('implement', 1))
        self.assertEqual(self.commands.implementations, 1)
        await self.runner.revalidate(95)
        await self.runner.freeze(95, self.manifest_path, refresh=True)
        refreshed = self.runner.store.read('issue-95')
        self.assertEqual((refreshed['stage'], refreshed['repairs']), ('implement', 1))
        self.assertEqual(refreshed['provider_calls'], before['provider_calls'])
        self.assertEqual(refreshed['deadline'], before['deadline'])
        result = await self.runner.run_issue(95, execute=True)
        self.assertEqual(result['status'], 'merged')
        final = self.runner.store.read('issue-95')
        self.assertEqual((final['repairs'], self.commands.implementations), (1, 2))
        self.assertEqual(final['head'], final['tested_head'])
        self.assertEqual(final['head'], final['reviewed_head'])
        self.assertEqual(self.runner.store.read('active'), {})

    async def test_validation_repair_can_revalidate_after_main_advances(self):
        await self.assert_repair_recovers_main_advance('validate')

    async def test_rejected_review_repair_can_revalidate_after_main_advances(self):
        await self.assert_repair_recovers_main_advance('review')

    async def test_ci_repair_can_revalidate_after_main_advances(self):
        await self.assert_repair_recovers_main_advance('wait_ci')

    async def assert_pending_repair_conflict_keeps_its_budget(self, exhausted):
        self.commands.reject_reviews = 2 if exhausted else 1
        self.config = replace(self.config, max_repair_rounds=1)
        self.runner = Runner(self.config, self.commands)
        if exhausted:
            with self.assertRaisesRegex(GateError, 'Repair budget exhausted'):
                await self.runner.run_issue(95, execute=True)
        else:
            with patch.object(self.runner, 'repair', AsyncMock(side_effect=asyncio.CancelledError())):
                with self.assertRaises(asyncio.CancelledError):
                    await self.runner.run_issue(95, execute=True)
        before = self.runner.store.read('issue-95')
        self.assertEqual(before['stage'], 'repair_pending')
        (self.checkout / 'feature.txt').write_text('Conflicting change on main\n')
        for command in [('add', '.'), ('commit', '-m', 'conflicting main change'),
                        ('update-ref', 'refs/remotes/origin/main', 'HEAD')]:
            subprocess.run(['git', *command], cwd=self.checkout, check=True, capture_output=True)
        self.commands.base = await self.runner.head(self.checkout)
        with self.assertRaisesRegex(GateError, 'merge conflicts'):
            await self.runner.revalidate(95)
        await self.runner.freeze(95, self.manifest_path, refresh=True)
        refreshed = self.runner.store.read('issue-95')
        self.assertEqual(refreshed['stage'], 'repair_pending')
        self.assertNotIn('after_revalidation', refreshed)
        self.assertIn('Concrete missing edge case', refreshed['feedback'])
        self.assertIn('merge conflicts', refreshed['feedback'])
        if exhausted:
            with self.assertRaisesRegex(GateError, 'Repair budget exhausted'):
                await self.runner.run_issue(95, execute=True)
            final = self.runner.store.read('issue-95')
            self.assertEqual(final['provider_calls'], before['provider_calls'])
            self.assertEqual(final['repairs'], 1)
            self.assertEqual(self.commands.merges, 0)
        else:
            self.assertEqual((await self.runner.run_issue(95, execute=True))['status'], 'merged')
            self.assertEqual(self.runner.store.read('issue-95')['repairs'], 1)
            self.assertEqual(self.commands.implementations, 2)

    async def test_conflicting_main_cannot_bypass_exhausted_review_repair_budget(self):
        await self.assert_pending_repair_conflict_keeps_its_budget(True)

    async def test_conflicting_main_preserves_findings_and_charges_pending_repair(self):
        await self.assert_pending_repair_conflict_keeps_its_budget(False)

    async def test_changed_issue_cannot_publish_old_acceptance(self):
        original = self.runner.issue
        async def edited(number):
            issue = await original(number)
            if self.commands.implementations:
                issue['body'] = 'Changed requirements after admission'
            return issue
        self.runner.issue = edited
        with self.assertRaisesRegex(GateError, 'changed|refresh'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.creates, 0)

    async def test_receipt_tamper_blocks_publication_even_after_approval(self):
        original = self.runner.review
        async def tamper(issue, record):
            verdict = await original(issue, record)
            receipt = next(iter(record['receipts'].values()))
            (self.config.state_dir / receipt['path']).write_text('{}')
            return verdict
        self.runner.review = tamper
        with self.assertRaisesRegex(GateError, 'Evidence|evidence'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.creates, 0)

    async def test_explicit_refresh_invalidates_gates_without_resetting_spent_work(self):
        self.commands.fail_ci = True
        with self.assertRaises(GateError):
            await self.runner.run_issue(95, execute=True)
        record = self.runner.store.read('issue-95')
        record.update(provider_calls=4, deadline=12345)
        self.runner.save(record)
        self.manifest['objective'] = 'Refined objective'
        self.manifest_path.write_text(json.dumps(self.manifest))
        await self.runner.freeze(95, self.manifest_path, refresh=True)
        refreshed = self.runner.store.read('issue-95')
        self.assertEqual((refreshed['provider_calls'], refreshed['deadline'], refreshed['repairs']),
                         (4, 12345, record['repairs']))
        self.assertIsNone(refreshed['tested_head'])
        self.assertIsNone(refreshed['reviewed_head'])
        self.assertEqual(refreshed['receipts'], {})
        self.assertEqual(len(refreshed['contract_history']), 1)

    async def test_generic_pass_and_provider_claim_cannot_replace_named_regression(self):
        original = self.commands.run
        async def missing(args, **kwargs):
            if tuple(args) == ('go', 'test', '-json', '-count=1', './feature'):
                return 0, 'All tests passed; no named regression was executed'
            return await original(args, **kwargs)
        self.commands.run = missing
        with self.assertRaisesRegex(GateError, 'budget exhausted'):
            await self.runner.run_issue(95, execute=True)
        self.assertGreater(self.commands.tests, 0)
        self.assertEqual((self.commands.reviews, self.commands.creates), (0, 0))

    async def test_no_change_claim_never_creates_an_empty_commit_or_pr(self):
        original = self.commands.run
        async def no_change(args, **kwargs):
            result = await original(args, **kwargs)
            if args[0] == 'claude' and '-p' in args and 'Edit' in args[args.index('--tools') + 1]:
                (kwargs['cwd'] / 'feature.txt').unlink()
            return result
        self.commands.run = no_change
        with self.assertRaisesRegex(GateError, 'no changes'):
            await self.runner.run_issue(95, execute=True)
        record = self.runner.store.read('issue-95')
        self.assertEqual(record['base'], record['head'])
        self.assertEqual(self.commands.creates, 0)
        calls = self.commands.implementations
        with self.assertRaisesRegex(GateError, 'no changes|disposition'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.implementations, calls)

    async def test_corrupted_review_coverage_cannot_publish_with_valid_test_receipts(self):
        original = self.runner.review
        async def corrupt(issue, record):
            verdict = await original(issue, record)
            record['review']['criteria'] = []
            self.runner.save(record)
            return verdict
        self.runner.review = corrupt
        with self.assertRaisesRegex(GateError, 'Review|review'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.creates, 0)

    async def test_candidate_document_edits_do_not_change_frozen_input_guidance(self):
        self.manifest['context_refs'].append('README.md')
        self.manifest['scope_paths'].append('README.md')
        self.manifest_path.write_text(json.dumps(self.manifest))
        await self.runner.freeze(95, self.manifest_path, refresh=True)
        original = self.commands.run
        async def candidate_docs(args, **kwargs):
            result = await original(args, **kwargs)
            if args[0] == 'claude' and '-p' in args and 'Edit' in args[args.index('--tools') + 1]:
                (kwargs['cwd'] / 'README.md').write_text('Authorized documentation output\n')
            return result
        self.commands.run = candidate_docs
        self.assertEqual((await self.runner.run_issue(95, execute=True))['status'], 'merged')
        self.assertEqual((self.checkout / 'README.md').read_text(), 'Fixture\n')

    async def test_source_guidance_change_stops_before_validation_or_publication(self):
        original = self.commands.run
        async def change_source(args, **kwargs):
            result = await original(args, **kwargs)
            if args[0] == 'claude' and '-p' in args and 'Edit' in args[args.index('--tools') + 1]:
                (self.checkout / 'AGENTS.md').write_text('Changed operator guidance\n')
            return result
        self.commands.run = change_source
        with self.assertRaisesRegex(GateError, 'guidance|refresh'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual((self.commands.tests, self.commands.creates), (0, 0))

    async def test_dependency_requires_confirmed_merge_and_ancestry(self):
        self.manifest['dependencies'] = [dict(issue=96, pr=10, merge_commit='a' * 40)]
        self.manifest_path.write_text(json.dumps(self.manifest))
        for state, commit, message in [('OPEN', 'a' * 40, 'confirmed'), ('MERGED', 'a' * 40, 'not present')]:
            pr = dict(state=state, mergeCommit={'oid': commit}, closingIssuesReferences=[{'number': 96}])
            with patch.object(self.runner, 'pr', AsyncMock(return_value=pr)), self.assertRaisesRegex(GateError, message):
                await self.runner.freeze(95, self.manifest_path, refresh=True)
        self.manifest['dependencies'][0]['merge_commit'] = self.commands.base
        self.manifest_path.write_text(json.dumps(self.manifest))
        pr = dict(state='MERGED', mergeCommit={'oid': self.commands.base}, closingIssuesReferences=[{'number': 96}])
        with patch.object(self.runner, 'pr', AsyncMock(return_value=pr)):
            await self.runner.freeze(95, self.manifest_path, refresh=True)
        self.assertEqual(self.commands.implementations, 0)

    async def test_dependency_cycle_is_rejected_before_remote_proof(self):
        path = self.config.state_dir / 'tasks' / '96' / 'contract.json'
        path.parent.mkdir(parents=True)
        path.write_text(json.dumps({'dependencies': [{'issue': 95}]}))
        self.manifest['dependencies'] = [dict(issue=96, pr=10, merge_commit=self.commands.base)]
        self.manifest_path.write_text(json.dumps(self.manifest))
        with patch.object(self.runner, 'pr', AsyncMock(side_effect=AssertionError('should reject cycle first'))):
            with self.assertRaisesRegex(GateError, 'cycle'):
                await self.runner.freeze(95, self.manifest_path, refresh=True)


class ScopeViolationRecoveryTests(ContractExecutionTests):
    """An out-of-scope implementation is committed and stopped, never stranded or published."""

    def write_during_implementation(self, relative, content='Agent output\n'):
        original = self.commands.run

        async def implementation(args, **kwargs):
            result = await original(args, **kwargs)
            if args[0] == 'claude' and '-p' in args and 'Edit' in args[args.index('--tools') + 1]:
                target = kwargs['cwd'] / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text(content)
            return result

        self.commands.run = implementation

    def advance_main(self):
        (self.checkout / 'other.txt').write_text('Independent main change\n')
        for command in [('add', '.'), ('commit', '-m', 'advance main'),
                        ('update-ref', 'refs/remotes/origin/main', 'HEAD')]:
            subprocess.run(['git', *command], cwd=self.checkout, check=True, capture_output=True)

    def git(self, worktree, *args):
        return subprocess.check_output(['git', *args], cwd=worktree, text=True)

    async def stop_on_scope_violation(self):
        self.write_during_implementation('internal/handler/bulk_status_handler.go', 'package handler\n')
        with self.assertRaisesRegex(GateError, 'Change outside frozen task scope: '
                                               'internal/handler/bulk_status_handler.go'):
            await self.runner.run_issue(95, execute=True)
        return self.runner.store.read('issue-95')

    async def test_scope_violation_commits_the_implementation_and_stops_at_validation(self):
        record = await self.stop_on_scope_violation()
        worktree = Path(record['worktree'])
        self.assertEqual(record['stage'], 'validate')
        self.assertIn('Change outside frozen task scope', record['last_error'])
        self.assertNotEqual(record['head'], record['base'])
        self.assertEqual(await self.runner.head(worktree), record['head'])
        self.assertEqual(self.git(worktree, 'status', '--porcelain', '--untracked-files=all'), '')
        self.assertEqual(self.git(worktree, 'diff', '--name-only', record['base'], record['head']).split(),
                         ['feature.txt', 'internal/handler/bulk_status_handler.go'])
        self.assertEqual(len(record['implementation_claims']), 1)
        self.assertEqual(record['implementation_claims'][0]['head_before'], record['base'])
        self.assertIsNone(record['tested_head'])
        self.assertEqual((self.commands.implementations, self.commands.tests, self.commands.reviews,
                          self.commands.creates), (1, 0, 0, 0))

    async def test_unchanged_scope_keeps_failing_at_validation_without_another_provider_call(self):
        await self.stop_on_scope_violation()
        with self.assertRaisesRegex(GateError, 'outside frozen task scope'):
            await self.runner.run_issue(95, execute=True)
        await self.runner.freeze(95, self.manifest_path, refresh=True)
        self.assertEqual(self.runner.store.read('issue-95')['stage'], 'validate')
        with self.assertRaisesRegex(GateError, 'outside frozen task scope'):
            await self.runner.run_issue(95, execute=True)
        record = self.runner.store.read('issue-95')
        self.assertEqual(record['stage'], 'validate')
        self.assertIsNone(record['tested_head'])
        self.assertEqual((self.commands.implementations, self.commands.tests, self.commands.creates,
                          self.commands.merges), (1, 0, 0, 0))

    async def test_revalidate_and_widened_refresh_recover_the_committed_implementation(self):
        before = await self.stop_on_scope_violation()
        self.advance_main()
        self.commands.base = await self.runner.head(self.checkout)
        with self.assertRaisesRegex(GateError, 'Revalidate the active candidate'):
            await self.runner.freeze(95, self.manifest_path, refresh=True)
        await self.runner.revalidate(95)
        revalidated = self.runner.store.read('issue-95')
        self.assertEqual((revalidated['stage'], revalidated['base']), ('validate', self.commands.base))
        self.manifest['scope_paths'].append('internal/handler/**')
        self.manifest_path.write_text(json.dumps(self.manifest))
        await self.runner.freeze(95, self.manifest_path, refresh=True)
        refreshed = self.runner.store.read('issue-95')
        self.assertEqual(refreshed['stage'], 'validate')
        self.assertIsNone(refreshed['last_error'])
        self.assertEqual(refreshed['provider_calls'], before['provider_calls'])
        result = await self.runner.run_issue(95, execute=True)
        self.assertEqual(result['status'], 'merged')
        final = self.runner.store.read('issue-95')
        self.assertEqual(self.commands.implementations, 1)
        self.assertEqual(final['repairs'], 0)
        self.assertEqual(final['head'], final['tested_head'])
        self.assertEqual(subprocess.run(['git', 'merge-base', '--is-ancestor', before['head'], final['head']],
                                        cwd=Path(final['worktree'])).returncode, 0)

    async def test_protected_path_is_refused_before_any_commit(self):
        self.write_during_implementation('.env.local', 'SECRET=value\n')
        with self.assertRaisesRegex(GateError, 'protected or private path: .env.local'):
            await self.runner.run_issue(95, execute=True)
        record = self.runner.store.read('issue-95')
        worktree = Path(record['worktree'])
        self.assertEqual(record['stage'], 'finalize_implementation')
        self.assertEqual(record['head'], record['base'])
        self.assertEqual(await self.runner.head(worktree), record['base'])
        self.assertNotIn('intended_tree', record)
        status = self.git(worktree, 'status', '--porcelain', '--untracked-files=all')
        self.assertIn('.env.local', status)
        self.assertIn('feature.txt', status)
        self.assertEqual(self.commands.implementations, 1)


for _name in list(fixtures.EngineTests.__dict__):
    if _name.startswith('test_'):
        setattr(ContractExecutionTests, _name, None)
for _name in list(ContractExecutionTests.__dict__):
    if _name.startswith('test_'):
        setattr(ScopeViolationRecoveryTests, _name, None)
