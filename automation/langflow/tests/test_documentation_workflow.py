"""Real Git candidates and merges, deterministic external agent/CI boundaries."""

from dataclasses import replace
import json
import subprocess

import test_runner as fixtures
import test_pull_requests as pr_fixtures
from gophercrm_runner import documentation as docs
from gophercrm_runner.config import GateError
from gophercrm_runner.engine import Runner
from gophercrm_runner.pull_requests import PullRequestRunner


def git(test, *args, cwd=None):
    return subprocess.check_output(['git', *args], cwd=cwd or test.checkout,
        stderr=subprocess.DEVNULL, text=True).strip()


def install_boundary(test):
    original = test.commands.run
    test.missing_docs = test.gap_once = test.write_document = test.lose_merge_response = test.drift_merge = False

    async def boundary(args, **kwargs):
        if tuple(args[:2]) == ('git', 'fetch') and any('refs/pull/' in a for a in args):
            git(test, 'update-ref', 'refs/remotes/origin/completion-pr-122', test.commands.pr_data['headRefOid'])
            return 0, ''
        code, output = await original(args, **kwargs)
        if args[0] == 'claude' and '-p' in args:
            cwd = kwargs['cwd']
            result = json.loads(output)
            report = result['structured_output']
            if 'approved' in report and not test.missing_docs:
                selected = docs.plan(cwd, test.commands.base, git(test, 'rev-parse', 'HEAD', cwd=cwd))
                report['documentation'] = [dict(path=d['path'],
                    disposition='updated' if d['changed'] else 'no-impact',
                    reason='Fixture change does not alter this documented interface.') for d in selected['documents']]
                if test.gap_once and test.commands.reviews == 1:
                    report.update(approved=False, blocking_findings=['README still describes old behavior.'])
                    next(d for d in report['documentation'] if d['path'] == 'README.md').update(
                        disposition='needs-update', reason='Update the behavior description.')
                    for criterion in report['criteria']:
                        criterion.update(passed=False, finding='Documentation remains incomplete.')
            elif 'addressed_criteria' in report and (test.write_document or (
                    test.gap_once and test.commands.implementations > 1)):
                (cwd / 'README.md').write_text('Documented implemented behavior.\n')
            output = json.dumps(result)
        if tuple(args[:3]) == ('gh', 'pr', 'merge'):
            tree = git(test, 'rev-parse', test.commands.pushed_head + '^{tree}')
            if test.drift_merge:
                changed_blob = subprocess.check_output(['git', 'hash-object', '-w', '--stdin'],
                    cwd=test.checkout, input='Unexpected documentation after review\n', text=True).strip()
                rows = subprocess.check_output(['git', 'ls-tree', '-z', tree], cwd=test.checkout, text=True).split('\0')
                rows = [f'100644 blob {changed_blob}\tREADME.md' if row.endswith('\tREADME.md') else row
                        for row in rows]
                tree = subprocess.check_output(['git', 'mktree', '-z'], cwd=test.checkout,
                    input='\0'.join(rows), text=True).strip()
            merge = git(test, 'commit-tree', tree, '-p', test.commands.base, '-m', 'fixture squash')
            test.commands.pr_data['mergeCommit'] = {'oid': merge}
            if test.lose_merge_response:
                test.lose_merge_response = False
                raise GateError('Simulated lost merge response')
        return code, output

    test.commands.run = boundary


class DocumentationTicketTests(fixtures.EngineTests):
    def setUp(self):
        super().setUp()
        self.config = replace(self.config, documentation_mode='reviewed')
        self.runner = Runner(self.config, self.commands)
        install_boundary(self)

    async def asyncSetUp(self):
        for number in self.config.issues:
            manifest = fixtures.fixture_manifest(number)
            manifest['scope_paths'].append('README.md')
            path = self.root / f'manifest-{number}.json'
            path.write_text(json.dumps(manifest))
            await self.runner.freeze(number, path)
        self.commands.calls.clear()

    async def test_documentation_uses_existing_calls_and_verifies_merge(self):
        result = await self.runner.run_issue(95, execute=True)
        self.assertEqual(result['status'], 'merged')
        self.assertEqual((self.commands.implementations, self.commands.reviews, self.commands.tests), (1, 1, 1))
        self.assertEqual(result['documentation_merge']['merge_commit'], result['merge_commit'])
        record = self.runner.store.journal(95)
        self.assertIn('documentation_receipt', record)
        self.assertIn('Documentation reviewed', (self.config.state_dir / 'pr-95.md').read_text())

    async def test_documentation_gap_repairs_retests_and_reviews_new_head(self):
        self.gap_once = True
        result = await self.runner.run_issue(95, execute=True)
        self.assertEqual(result['status'], 'merged')
        self.assertEqual((self.commands.implementations, self.commands.reviews, self.commands.tests), (2, 2, 2))
        readme = next(d for d in result['documentation'] if d['path'] == 'README.md')
        self.assertEqual(readme['disposition'], 'updated')

    async def test_missing_documentation_coverage_blocks_merge(self):
        self.missing_docs = True
        with self.assertRaisesRegex(GateError, 'structured acceptance review'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.merges, 0)
        self.assertEqual(self.runner.store.journal(95)['provider_calls'], 2)

    async def test_impact_receipt_cannot_be_changed_after_review(self):
        self.commands.fail_ci = True
        with self.assertRaises(GateError):
            await self.runner.run_issue(95, execute=True)
        record = self.runner.store.journal(95)
        record['documentation_plan']['documents'] = []
        self.runner.save(record)
        self.commands.fail_ci = False
        with self.assertRaisesRegex(GateError, 'Documentation impact'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.merges, 0)

    async def test_documentation_cannot_widen_frozen_write_scope(self):
        path = self.root / 'manifest-95.json'
        path.write_text(json.dumps(fixtures.fixture_manifest(95)))
        await self.runner.freeze(95, path, refresh=True)
        self.write_document = True
        with self.assertRaisesRegex(GateError, 'outside frozen task scope'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.merges, 0)

    async def test_lost_merge_response_reconciles_documentation_after_expiry(self):
        self.lose_merge_response = True
        with self.assertRaisesRegex(GateError, 'lost merge'):
            await self.runner.run_issue(95, execute=True)
        record = self.runner.store.journal(95)
        record['deadline'] = self.runner.clock() - 1
        self.runner.save(record)
        result = await self.runner.run_issue(95, execute=True)
        self.assertEqual(result['status'], 'merged')
        self.assertEqual(result['documentation_merge']['merge_commit'], result['merge_commit'])
        self.assertEqual((self.commands.reviews, self.commands.merges), (1, 1))

    async def test_drift_at_merge_blocks_completion_and_the_next_ticket(self):
        self.drift_merge = True
        with self.assertRaisesRegex(GateError, 'Merged documentation differs'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.runner.store.journal(95)['stage'], 'merging')
        self.assertEqual(self.runner.store.read('active'), {'issue': 95})
        with self.assertRaises(GateError):
            await self.runner.run_issue(96, execute=True)
        self.assertEqual(self.commands.implementations, 1)

    async def test_failed_merge_has_explicit_idempotent_disposition_without_claiming_success(self):
        self.drift_merge = True
        with self.assertRaises(GateError):
            await self.runner.run_issue(95, execute=True)
        before = self.runner.store.journal(95)
        result = await self.runner.abandon_issue(95, reason='Inspected mismatch; prepare a separate correction.')
        self.assertEqual(result['status'], 'abandoned')
        self.assertFalse(result['validated_by_this_execution'])
        after = self.runner.store.journal(95)
        self.assertEqual(after['provider_calls'], before['provider_calls'])
        self.assertEqual(after['documentation_merge_failure'], before['documentation_merge_failure'])
        self.assertEqual(after['receipts'], before['receipts'])
        self.assertEqual(self.runner.store.read('active'), {})
        self.assertTrue(self.runner.store.read('control')['stopped'])
        self.assertEqual((await self.runner.run_issue(95, execute=True))['status'], 'abandoned')
        self.assertEqual((await self.runner.run_issue(96, execute=True))['status'], 'stopped')
        await self.runner.abandon_issue(95, reason='Repeat after response loss.')
        self.assertEqual(self.runner.store.journal(95)['completed_at'], after['completed_at'])
        self.assertEqual(self.runner.store.journal(95)['abandonments'], after['abandonments'])

    async def test_unverified_external_merge_requires_orphan_acknowledgement(self):
        self.commands.fail_ci = True
        with self.assertRaises(GateError):
            await self.runner.run_issue(95, execute=True)
        record = self.runner.store.journal(95)
        record.update(stage='implement', tested_head=None, reviewed_head=None)
        record['provider_calls'] += 1
        record['provider_invocations'].append(dict(id='orphan', status='running', pid=123))
        self.runner.save(record)
        tree = git(self, 'rev-parse', record['head'] + '^{tree}')
        merge = git(self, 'commit-tree', tree, '-p', record['base'], '-m', 'external merge during repair')
        self.commands.pr_data.update(state='MERGED', mergeCommit={'oid': merge})
        with self.assertRaisesRegex(GateError, 'lacks approval'):
            await self.runner.run_issue(95, execute=True)
        with self.assertRaisesRegex(GateError, 'processes-stopped'):
            await self.runner.abandon_issue(95, reason='Inspected external merge.')
        result = await self.runner.abandon_issue(95, reason='Inspected and stopped orphan.', processes_stopped=True)
        self.assertEqual(result['status'], 'abandoned')
        final = self.runner.store.journal(95)
        self.assertEqual(final['provider_calls'], record['provider_calls'])
        self.assertEqual(final['provider_invocations'][-1]['status'], 'operator-resolved')
        self.assertFalse(final['remote_disposition']['validated_by_this_execution'])

    async def test_ordinary_success_cannot_be_downgraded_to_an_abandonment(self):
        await self.runner.run_issue(95, execute=True)
        with self.assertRaises(GateError):
            await self.runner.abandon_issue(95, reason='Not a failed documentation merge.')
        self.assertEqual(self.runner.store.journal(95)['stage'], 'merged')

    async def test_pr_ready_promotion_keeps_original_documentation_evidence(self):
        self.runner = Runner(replace(self.config, completion_target='pr'), self.commands)
        result = await self.runner.run_issue(95, execute=True)
        self.assertEqual(result['status'], 'pr_ready')
        source = self.runner.store.journal(95)
        self.commands.pr_data.update(title='Existing ticket', body='Scoped behavior', isCrossRepository=False)
        completion = PullRequestRunner(self.config, self.commands)
        result = await completion.complete_pr(122, self.commands.pushed_head, execute=True)
        self.assertEqual(result['status'], 'merged')
        self.assertEqual(self.commands.reviews, 1)
        after = self.runner.store.journal(95)
        self.assertEqual(after['documentation_receipt'], source['documentation_receipt'])
        self.assertEqual(after['review_receipt'], source['review_receipt'])
        self.assertEqual(result['documentation_merge']['merge_commit'], result['merge_commit'])


class DocumentationPRTests(pr_fixtures.PullRequestTests):
    def setUp(self):
        super().setUp()
        self.config = replace(self.config, documentation_mode='reviewed')
        self.runner = PullRequestRunner(self.config, self.commands)
        install_boundary(self)

    async def test_existing_pr_requires_doc_review_and_verified_handoff(self):
        result = await self.complete()
        self.assertEqual(result['status'], 'merged')
        self.assertTrue(result['documentation'])
        self.assertEqual(result['documentation_merge']['merge_commit'], result['merge_commit'])
        self.assertEqual(self.commands.reviews, 1)
        self.assertEqual((await self.complete())['status'], 'merged')
        self.assertEqual(self.commands.merges, 1)

    async def test_existing_pr_missing_doc_review_does_not_merge(self):
        self.missing_docs = True
        with self.assertRaises(GateError):
            await self.complete()
        self.assertEqual(self.commands.merges, 0)

    async def test_existing_pr_drift_retains_active_completion_for_recovery(self):
        self.drift_merge = True
        with self.assertRaisesRegex(GateError, 'Merged documentation differs'):
            await self.complete()
        self.assertEqual(self.runner.store.read('completion-pr-122')['stage'], 'merging')
        self.assertEqual(self.runner.store.read('active'), {'pr': 122})
        result = await self.runner.abandon_pr(122, reason='Inspected mismatch; preserve evidence for correction.')
        self.assertEqual(result['status'], 'abandoned')
        self.assertEqual(self.runner.store.read('active'), {})


for _child, _parent in [(DocumentationTicketTests, fixtures.EngineTests),
                         (DocumentationPRTests, pr_fixtures.PullRequestTests)]:
    for _name in _parent.__dict__:
        if _name.startswith('test_') and _name not in _child.__dict__:
            setattr(_child, _name, None)
del _child, _parent, _name
