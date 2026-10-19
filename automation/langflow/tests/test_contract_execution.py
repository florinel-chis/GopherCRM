from dataclasses import replace
import json
from pathlib import Path
import subprocess
from unittest.mock import AsyncMock, patch

import test_runner as fixtures
from test_contracts import manifest
from gophercrm_runner.config import GateError
from gophercrm_runner.engine import Runner


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


for _name in list(fixtures.EngineTests.__dict__):
    if _name.startswith('test_'):
        setattr(ContractExecutionTests, _name, None)
