import asyncio
from dataclasses import replace
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import AsyncMock, patch

from gophercrm_runner.config import CommandError, Config, EnvironmentError, GateError, REQUIRED_CHECKS
from gophercrm_runner.engine import CIFailure, Runner
from gophercrm_runner.gates import agent_result, check_state, merge_gate, migration_coverage, validate_paths
from gophercrm_runner.process import Commands
from gophercrm_runner.state import Store


def green_checks():
    return [{'__typename': 'CheckRun', 'name': name, 'status': 'COMPLETED',
             'conclusion': 'SUCCESS'} for name in REQUIRED_CHECKS]


def ready_pr():
    return dict(number=122, url='https://github.com/example/repo/pull/122', state='OPEN',
                isDraft=False, headRefOid='head', baseRefOid='base', mergeable='MERGEABLE',
                reviewDecision='', statusCheckRollup=green_checks(), mergeCommit=None,
                headRefName='automation/remediate-95', baseRefName='main')


GH_TRANSPORT_FAILURE = 'Post "https://api.github.com/graphql": net/http: TLS handshake timeout\n'


def fixture_manifest(number):
    return dict(schema_version=1, repository='example/repo', issue=number,
                objective='Fix the scoped behavior', non_goals=['Unrelated changes'],
                scope_paths=['feature.txt', 'internal/**', 'migrations/**', 'Makefile', '.env'],
                dependencies=[], validation_profile='backend', context_refs=['AGENTS.md'],
                acceptance=[dict(id='AC-1', requirement='Feature regression passes',
                                 verification=dict(kind='automated', check_id='regression'))])


class GateTests(unittest.TestCase):
    def test_ci_requires_all_checks_and_current_success(self):
        checks = green_checks()
        self.assertEqual(check_state(checks, REQUIRED_CHECKS), 'success')
        self.assertEqual(check_state(checks[:-1], REQUIRED_CHECKS), 'pending')
        self.assertEqual(check_state(checks + [checks[0]], REQUIRED_CHECKS), 'pending')
        for state in ['SKIPPED', 'NEUTRAL', '', 'PENDING']:
            checks[0]['conclusion'] = state
            self.assertEqual(check_state(checks, REQUIRED_CHECKS), 'pending')
        for state in ['FAILURE', 'CANCELLED', 'TIMED_OUT']:
            checks[0]['conclusion'] = state
            self.assertEqual(check_state(checks, REQUIRED_CHECKS), 'failed')
        checks = green_checks() + [dict(name='Extra check', status='COMPLETED', conclusion='FAILURE')]
        self.assertEqual(check_state(checks, REQUIRED_CHECKS), 'failed')

    def test_merge_rejects_stale_review_head_base_and_github_blocks(self):
        record = dict(head='head', tested_head='head', reviewed_head='head', base='base')
        merge_gate(record, ready_pr(), REQUIRED_CHECKS)
        for field in ['tested_head', 'reviewed_head']:
            with self.assertRaises(GateError):
                merge_gate(dict(record, **{field: 'old'}), ready_pr(), REQUIRED_CHECKS)
        for field, value in [('headRefOid', 'new'), ('baseRefOid', 'new'), ('isDraft', True),
                             ('mergeable', 'UNKNOWN'), ('reviewDecision', 'CHANGES_REQUESTED'),
                             ('reviewDecision', 'REVIEW_REQUIRED'), ('state', 'CLOSED')]:
            with self.subTest(field=field, value=value), self.assertRaises(GateError):
                merge_gate(record, dict(ready_pr(), **{field: value}), REQUIRED_CHECKS)

    def test_review_requires_explicit_consistent_structured_success(self):
        verdict = dict(approved=True, blocking_findings=[], summary='Reviewed the diff')
        good = dict(is_error=False, structured_output=verdict)
        self.assertEqual(agent_result(json.dumps(good), review=True), verdict)
        for result in [{}, dict(good, is_error=True), dict(good, permission_denials=['Read']),
                       dict(good, structured_output={'approved': True}),
                       dict(good, structured_output=dict(verdict, blocking_findings=['bug']))]:
            with self.assertRaises(GateError):
                agent_result(json.dumps(result), review=True)

    def test_private_and_runner_paths_are_rejected(self):
        validate_paths(['internal/service/test.go', '.env.example'])
        for name in ['../outside', '/outside', '.git/config', '.env', 'sub/.env.production',
                     'local.db', 'key.pem', 'automation/langflow/gophercrm_runner/engine.py',
                     '.github/release.yml', 'scripts/release/release.py', 'scripts/deploy/bootstrap.sh']:
            with self.subTest(name=name), self.assertRaises(GateError):
                validate_paths([name])

    def test_frontend_validation_files_are_protected_gates(self):
        for name in ['gocrm-ui/vitest.config.ts', 'gocrm-ui/vitest.config.mts',
                     'gocrm-ui/playwright.config.ts', 'gocrm-ui/playwright.config.screenshots.ts',
                     'gocrm-ui/src/test/setup.ts', 'gocrm-ui/src/test/globals.d.ts',
                     'gocrm-ui/e2e/global-setup.ts', 'gocrm-ui/e2e/global-teardown.ts',
                     'gocrm-ui/e2e/helpers/auth.ts', 'gocrm-ui/e2e/fixtures/data.ts',
                     'gocrm-ui/e2e/pages/LoginPage.ts', 'gocrm-ui/e2e/scripts/seed.ts',
                     'gocrm-ui/e2e/screenshots/capture.spec.ts', 'gocrm-ui/e2e/README.md',
                     'gocrm-ui/e2e/tests/health-smoke.spec.ts']:
            with self.subTest(name=name), self.assertRaisesRegex(GateError, 'existing validation gate'):
                validate_paths([name])
            with self.subTest(name=name, added=True), \
                    self.assertRaisesRegex(GateError, 'existing validation gate'):
                validate_paths([name], added={name})

    def test_every_vitest_root_configuration_is_a_protected_gate(self):
        # Vitest discovers workspace and project files beside its configuration.
        for name in ['gocrm-ui/vitest.workspace.ts', 'gocrm-ui/vitest.projects.ts',
                     'gocrm-ui/vitest.workspace.json', 'gocrm-ui/vitest.setup.ts']:
            for added in (set(), {name}):
                with self.subTest(name=name, added=bool(added)), \
                        self.assertRaisesRegex(GateError, 'existing validation gate'):
                    validate_paths([name], added=added)
        validate_paths(['gocrm-ui/src/vitest.helpers.ts', 'gocrm-ui/src/pages/Deals.test.tsx'])

    def test_only_a_newly_added_browser_spec_is_admitted(self):
        spec = 'gocrm-ui/e2e/tests/deal-board.spec.ts'
        validate_paths([spec, 'gocrm-ui/src/pages/Deals.tsx'], added={spec})
        with self.assertRaisesRegex(GateError, 'existing validation gate'):
            validate_paths([spec])
        for name in ['gocrm-ui/e2e/tests/Deal-Board.spec.ts', 'gocrm-ui/e2e/tests/nested/x.spec.ts',
                     'gocrm-ui/e2e/tests/deal-board.ts', 'gocrm-ui/e2e/helpers/new-helper.ts']:
            with self.subTest(name=name), self.assertRaisesRegex(GateError, 'existing validation gate'):
                validate_paths([name], added={name})
        validate_paths(['gocrm-ui/src/test/utils.tsx', 'gocrm-ui/src/pages/Deals.test.tsx'])

    def test_migration_success_cannot_be_a_skip_or_empty_output(self):
        for output in ['', '{}', '{"Action":"skip","Test":"TestMigrationFresh_FullUpAndDown"}']:
            with self.assertRaises(GateError):
                migration_coverage(output)
        migration_coverage('\n'.join(json.dumps({'Action': 'pass', 'Test': test}) for test in
            ['TestMigrationFresh_FullUpAndDown', 'TestMigrationUpgrade_FromMainAutoMigratedSchema']))


class FakeCommands:
    """Real Git worktrees/commits; deterministic agent, GitHub, and validation boundary."""
    def __init__(self, root):
        self.real = Commands(root / 'command-logs')
        self.calls = []
        self.implementations = self.reviews = self.tests = self.merges = self.creates = 0
        self.reject_reviews = 0
        self.fail_tests = 0
        self.fail_ci = False
        self.ci_repair = False
        self.pr_data = None
        self.pushed_head = None
        # GitHub state reads that fail at the transport layer, as `gh` reports them.
        self.gh_transport_failures = 0

    async def run(self, args, *, cwd, **kwargs):
        args = tuple(args)
        self.calls.append(args)
        if (self.gh_transport_failures and args[0] == 'gh'
                and args[1:2] in {('issue',), ('pr',), ('run',)}):
            self.gh_transport_failures -= 1
            raise CommandError('gh exited 1; log gh.log', stdout='', stderr=GH_TRANSPORT_FAILURE)
        versions = {('git', '--version'): 'git version 2.54.0',
                    ('gh', '--version'): 'gh version 2.92.0',
                    ('claude', '--version'): '2.1.303',
                    ('go', 'version'): 'go version go1.26.3 darwin/arm64',
                    ('node', '--version'): 'v26.12.0', ('npm', '--version'): '11.20.0',
                    ('make', '--version'): 'GNU Make 3.81',
                    ('mysql', '--version'): 'mysql Ver 8.0.46'}
        if args in versions:
            return 0, versions[args]
        if args == ('claude', 'auth', 'status', '--json'):
            return 0, '{"loggedIn": true}'
        if args == ('gh', 'auth', 'status'):
            return 0, 'Authenticated'
        if args[:2] == ('gh', 'api'):
            return 0, '{}'
        if args == ('go', 'run', './automation/langflow/probes/mysql'):
            return 0, '{"migration":"ok","e2e":"ok"}'
        if args == ('go', 'test', './cmd/migrate', '-list', '^TestMigration'):
            return 0, 'TestMigrationFresh_FullUpAndDown\nTestMigrationUpgrade_FromMainAutoMigratedSchema\n'
        if args[:4] == ('git', 'remote', 'get-url', 'origin'):
            return 0, 'https://github.com/example/repo.git'
        if args[:2] == ('git', 'fetch'):
            return 0, ''
        if args[:3] == ('git', 'ls-remote', 'origin'):
            head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=cwd, text=True).strip()
            return 0, f'{head}\trefs/heads/main\n'
        if args[:2] == ('git', 'push'):
            self.pushed_head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=cwd, text=True).strip()
            if self.pr_data:
                self.pr_data['headRefOid'] = self.pushed_head
            return 0, ''
        if args[0] == 'claude':
            assert 'Bash' not in args[args.index('--tools') + 1]
            if 'Edit' not in args[args.index('--tools') + 1]:
                self.reviews += 1
                rejected = self.reviews <= self.reject_reviews
                result = dict(is_error=False, structured_output=dict(approved=not rejected,
                    blocking_findings=['Concrete missing edge case'] if rejected else [], summary='Reviewed',
                    criteria=[dict(id='AC-1', passed=not rejected, evidence=['acceptance-regression'],
                                   finding='Missing edge case' if rejected else '')]))
            else:
                self.implementations += 1
                (cwd / 'feature.txt').write_text(f'Implementation {self.implementations}\n')
                result = dict(is_error=False, structured_output=dict(summary='Implemented',
                    addressed_criteria=['AC-1'], remaining_gaps=[]))
            return 0, json.dumps(result)
        if args == ('go', 'test', '-json', '-count=1', './feature'):
            return 0, json.dumps(dict(Package='example/repo/feature', Test='TestFeature', Action='pass'))
        if args == ('make', 'verify'):
            self.tests += 1
            if self.tests <= self.fail_tests:
                raise CommandError('Regression test failed')
            return 0, 'All checks passed'
        if args[:3] == ('gh', 'issue', 'view'):
            number = int(args[3])
            return 0, json.dumps(dict(number=number, title='Fix issue', body='Acceptance criteria',
                                      state='OPEN', url=f'https://github.com/example/repo/issues/{number}'))
        if args[:3] == ('gh', 'pr', 'list'):
            return 0, json.dumps([self.pr_data] if self.pr_data else [])
        if args[:3] == ('gh', 'pr', 'create'):
            self.creates += 1
            self.pr_data = ready_pr()
            self.pr_data.update(headRefOid=self.pushed_head, baseRefOid=self.base)
            return 0, self.pr_data['url']
        if args[:3] == ('gh', 'pr', 'view'):
            result = dict(self.pr_data)
            if self.fail_ci:
                result['statusCheckRollup'] = [dict(green_checks()[0], conclusion='FAILURE')]
            elif self.ci_repair and self.implementations == 1:
                result['statusCheckRollup'] = [dict(green_checks()[0], conclusion='FAILURE',
                    detailsUrl='https://github.com/example/repo/actions/runs/123/job/456')]
            return 0, json.dumps(result)
        if args[:3] == ('gh', 'pr', 'edit'):
            return 0, ''
        if args[:3] == ('gh', 'run', 'view'):
            if '--json' in args:
                return 0, json.dumps(dict(status='completed', conclusion='failure'))
            return 0, 'A regression assertion failed on CI'
        if args[:3] == ('gh', 'pr', 'merge'):
            assert args[args.index('--match-head-commit') + 1] == self.pushed_head
            assert '--admin' not in args
            self.merges += 1
            self.pr_data.update(state='MERGED', mergeCommit={'oid': 'merge-sha'})
            return 0, ''
        return await self.real.run(args, cwd=cwd, **kwargs)


class EngineTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.checkout = self.root / 'checkout'
        self.checkout.mkdir()
        for args in [('init', '-b', 'main'), ('config', 'user.email', 'runner@example.test'),
                     ('config', 'user.name', 'Runner Test')]:
            subprocess.run(['git', *args], cwd=self.checkout, check=True, capture_output=True)
        (self.checkout / 'README.md').write_text('Fixture\n')
        (self.checkout / 'AGENTS.md').write_text('Fixture repository guidance\n')
        subprocess.run(['git', 'add', '.'], cwd=self.checkout, check=True, capture_output=True)
        subprocess.run(['git', 'commit', '-m', 'fixture'], cwd=self.checkout, check=True, capture_output=True)
        subprocess.run(['git', 'update-ref', 'refs/remotes/origin/main', 'HEAD'],
                       cwd=self.checkout, check=True, capture_output=True)
        self.config = Config(repository='example/repo', checkout=self.checkout,
                             state_dir=self.root / 'state', issues=(95, 96), max_repair_rounds=2,
                             min_free_disk_mb=1, acceptance_checks={'regression': {
                                 'argv': ['go', 'test', '-json', '-count=1', './feature'],
                                 'tests': [{'package': 'example/repo/feature', 'name': 'TestFeature'}]}})
        self.commands = FakeCommands(self.root)
        self.commands.base = subprocess.check_output(['git', 'rev-parse', 'HEAD'],
                                                     cwd=self.checkout, text=True).strip()
        self.runner = Runner(self.config, self.commands)

    async def asyncSetUp(self):
        for number in self.config.issues:
            path = self.root / f'manifest-{number}.json'
            path.write_text(json.dumps(fixture_manifest(number)))
            await self.runner.freeze(number, path)
        self.commands.calls.clear()

    def tearDown(self):
        self.temp.cleanup()

    async def test_zero_repair_budget_stops_before_reserving_a_round_or_provider_call(self):
        config = replace(self.config, max_repair_rounds=0)
        config.validate()
        runner = Runner(config, self.commands)
        record = {'repairs': 0, 'provider_calls': 1, 'stage': 'repair_pending'}
        before = dict(record)
        with patch.object(runner, 'implement', new_callable=AsyncMock) as implement:
            with self.assertRaisesRegex(GateError, 'Repair budget exhausted'):
                await runner.repair({'number': 95}, record, 'Failed required check')
            implement.assert_not_awaited()
        self.assertEqual(record, before)
        self.assertEqual(self.commands.calls, [])
        for invalid in (-1, True, False, 0.5, '0', None):
            with self.subTest(value=invalid), self.assertRaisesRegex(GateError, 'max_repair_rounds'):
                replace(config, max_repair_rounds=invalid).validate()

    def failed_check_runner(self, statuses, config=None):
        """Script one failed required check on run 123 whose run reports `statuses` in order."""
        runner = Runner(config or self.config, self.commands)
        pr = ready_pr()
        pr['statusCheckRollup'][0].update(conclusion='FAILURE',
            detailsUrl='https://github.com/example/repo/actions/runs/123/job/456')
        calls = []
        pending = list(statuses)

        async def gh(*args):
            calls.append(args)
            if args == ('run', 'view', '123', '--json', 'status,conclusion'):
                status = pending.pop(0) if len(pending) > 1 else pending[0]
                return json.dumps(dict(status=status, conclusion='failure' if status == 'completed' else ''))
            if args == ('run', 'view', '123', '--log-failed'):
                return 'backend job: assertion failed'
            raise AssertionError(f'unexpected gh call {args}')

        async def view(number):
            calls.append(('pr', number))
            return json.loads(json.dumps(pr))

        runner.gh = gh
        runner.pr = view
        return runner, calls

    async def test_ci_failure_waits_for_in_progress_run_before_fetching_logs(self):
        runner, calls = self.failed_check_runner(['in_progress', 'in_progress', 'completed'])
        record = dict(pr=122, head='head', base='base')
        with patch.object(runner, 'ci_pause', new_callable=AsyncMock) as pause:
            with self.assertRaisesRegex(CIFailure, 'backend job: assertion failed'):
                await runner.wait_ci(record)
        self.assertEqual(pause.await_count, 2)
        log_calls = [c for c in calls if c[-1] == '--log-failed']
        self.assertEqual(log_calls, [('run', 'view', '123', '--log-failed')])
        self.assertEqual(calls.index(log_calls[0]), len(calls) - 1)
        self.assertEqual(calls[-2], ('run', 'view', '123', '--json', 'status,conclusion'))
        # The PR head and base are re-read on every iteration while waiting.
        self.assertEqual(sum(1 for c in calls if c[0] == 'pr'), 3)

    async def test_ci_failure_on_completed_run_raises_without_waiting(self):
        runner, calls = self.failed_check_runner(['completed'])
        record = dict(pr=122, head='head', base='base')
        with patch.object(runner, 'ci_pause', new_callable=AsyncMock) as pause:
            with self.assertRaisesRegex(CIFailure, 'backend job: assertion failed'):
                await runner.wait_ci(record)
        pause.assert_not_awaited()
        self.assertEqual(sum(1 for c in calls if c[-1] == '--log-failed'), 1)

    async def test_ci_deadline_applies_while_failed_run_is_in_progress(self):
        config = replace(self.config, ci_timeout_seconds=60)
        runner, calls = self.failed_check_runner(['in_progress'], config)
        record = dict(pr=122, head='head', base='base')
        clock = [1000.0]

        async def advance(_record):
            clock[0] += 30

        with patch('gophercrm_runner.engine.time.monotonic', side_effect=lambda: clock[0]), \
                patch.object(runner, 'ci_pause', side_effect=advance):
            with self.assertRaisesRegex(GateError, 'CI deadline exceeded'):
                await runner.wait_ci(record)
        self.assertFalse(any(c[-1] == '--log-failed' for c in calls))

    async def test_preview_has_no_mutations_or_agent_calls(self):
        before = {str(p): p.read_bytes() for p in self.config.state_dir.rglob('*') if p.is_file()}
        result = await self.runner.run_issue(95)
        self.assertEqual(result['status'], 'planned')
        self.assertEqual(before, {str(p): p.read_bytes() for p in self.config.state_dir.rglob('*') if p.is_file()})
        self.assertEqual(self.commands.calls, [('gh', 'issue', 'view', '95', '--json',
            'number,title,body,state,url', '--repo', 'example/repo')])

    async def test_review_repair_retests_exact_commit_then_merges_once(self):
        self.commands.reject_reviews = 1
        result = await self.runner.run_issue(95, execute=True)
        self.assertEqual(result['status'], 'merged')
        self.assertEqual((self.commands.implementations, self.commands.tests, self.commands.reviews), (2, 2, 2))
        self.assertEqual((self.commands.creates, self.commands.merges), (1, 1))
        for argv in self.commands.calls:
            if argv[0] == 'claude' and '--json-schema' in argv:
                schema = json.loads(argv[argv.index('--json-schema') + 1])
                if 'addressed_criteria' in schema['properties']:
                    self.assertEqual(schema['properties']['addressed_criteria']['items']['enum'], ['AC-1'])
        record = self.runner.store.read('issue-95')
        self.assertEqual(record['head'], record['tested_head'])
        self.assertEqual(record['head'], record['reviewed_head'])
        self.assertEqual(self.runner.store.read('active'), {})
        self.assertEqual(await self.runner.head(self.checkout), self.commands.base)
        # A repeated request reconciles the existing merge without another agent or PR.
        await self.runner.run_issue(95, execute=True)
        self.assertEqual((self.commands.implementations, self.commands.merges), (2, 1))

    async def test_github_transport_failure_during_validation_is_resumable_without_repair(self):
        original = self.runner.validate

        async def validate(record):
            # The first GitHub read inside validation fails at the transport layer.
            self.commands.gh_transport_failures = 1
            return await original(record)

        with patch.object(self.runner, 'validate', side_effect=validate):
            with self.assertRaisesRegex(EnvironmentError, 'GitHub command failed: .*TLS handshake timeout') as caught:
                await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.gh_transport_failures, 0)
        self.assertIsInstance(caught.exception.__cause__, CommandError)
        record = self.runner.store.read('issue-95')
        self.assertEqual(record['stage'], 'validate')
        self.assertEqual((record['attempts'][-1]['stage'], record['attempts'][-1]['outcome'],
                          record['attempts'][-1]['category']), ('validation', 'failed', 'environment'))
        self.assertEqual(record['last_failure']['category'], 'environment')
        self.assertEqual((record['provider_calls'], record['repairs']), (1, 0))
        self.assertEqual((self.commands.implementations, self.commands.tests, self.commands.reviews), (1, 0, 0))
        self.assertEqual(self.runner.store.read('active'), {'issue': 95})
        # A plain resume validates the same commit once GitHub answers again.
        result = await self.runner.run_issue(95, execute=True)
        self.assertEqual(result['status'], 'merged')
        self.assertEqual((self.commands.implementations, self.commands.tests, self.commands.reviews), (1, 1, 1))
        record = self.runner.store.read('issue-95')
        self.assertEqual((record['provider_calls'], record['repairs']), (2, 0))

    async def test_failed_log_retrieval_stops_ci_wait_without_a_repair(self):
        self.commands.pr_data = dict(ready_pr(), statusCheckRollup=[dict(green_checks()[0], conclusion='FAILURE',
            detailsUrl='https://github.com/example/repo/actions/runs/123/job/456')])
        original = self.commands.run

        async def boundary(args, **kwargs):
            if tuple(args[:3]) == ('gh', 'run', 'view') and '--log-failed' in args:
                self.commands.calls.append(tuple(args))
                raise CommandError('gh exited 1; log gh.log', stdout='', stderr=GH_TRANSPORT_FAILURE)
            return await original(args, **kwargs)

        self.commands.run = boundary
        record = dict(pr=122, head='head', base='base')
        with patch.object(self.runner, 'ci_pause', new_callable=AsyncMock) as pause:
            with self.assertRaisesRegex(EnvironmentError, 'GitHub command failed') as caught:
                await self.runner.wait_ci(record)
        self.assertNotIsInstance(caught.exception, CIFailure)
        self.assertIsInstance(caught.exception.__cause__, CommandError)
        pause.assert_not_awaited()
        gh = [c[1:5] for c in self.commands.calls if c[0] == 'gh']
        self.assertEqual(gh, [('pr', 'view', '122', '--json'), ('run', 'view', '123', '--json'),
                              ('run', 'view', '123', '--log-failed')])

    async def test_failed_tests_are_repaired_before_review(self):
        self.commands.fail_tests = 1
        await self.runner.run_issue(95, execute=True)
        self.assertEqual((self.commands.tests, self.commands.reviews), (2, 1))

    async def test_exhausted_reviews_never_publish_and_block_next_issue(self):
        self.commands.reject_reviews = 100
        with self.assertRaisesRegex(GateError, 'budget exhausted'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual((self.commands.creates, self.commands.merges), (0, 0))
        with self.assertRaisesRegex(GateError, '#95 must finish'):
            await self.runner.run_issue(96, execute=True)

    async def test_ci_failure_never_merges_and_resume_does_not_duplicate_pr(self):
        self.commands.fail_ci = True
        with self.assertRaisesRegex(GateError, 'CI failed'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.merges, 0)
        self.commands.fail_ci = False
        await self.runner.run_issue(95, execute=True)
        self.assertEqual((self.commands.creates, self.commands.merges), (1, 1))

    async def test_unexpected_local_commit_blocks_resume(self):
        self.commands.fail_ci = True
        with self.assertRaises(GateError):
            await self.runner.run_issue(95, execute=True)
        worktree = Path(self.runner.store.read('issue-95')['worktree'])
        subprocess.run(['git', 'commit', '--allow-empty', '-m', 'unexpected'],
                       cwd=worktree, check=True, capture_output=True)
        self.commands.fail_ci = False
        with self.assertRaisesRegex(GateError, 'Local commit changed'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.merges, 0)

    async def test_ci_regression_enters_repair_retest_review_before_reusing_pr(self):
        self.commands.ci_repair = True
        await self.runner.run_issue(95, execute=True)
        self.assertEqual((self.commands.implementations, self.commands.tests, self.commands.reviews), (2, 2, 2))
        self.assertEqual((self.commands.creates, self.commands.merges), (1, 1))

    async def test_base_advance_revalidation_repeats_tests_and_review(self):
        self.commands.fail_ci = True
        with self.assertRaises(GateError):
            await self.runner.run_issue(95, execute=True)
        (self.checkout / 'other.txt').write_text('Another change on main\n')
        for args in [('add', '.'), ('commit', '-m', 'advance main'),
                     ('update-ref', 'refs/remotes/origin/main', 'HEAD')]:
            subprocess.run(['git', *args], cwd=self.checkout, check=True, capture_output=True)
        base = await self.runner.head(self.checkout)
        self.commands.pr_data['baseRefOid'] = base
        self.commands.fail_ci = False
        with self.assertRaisesRegex(GateError, 'PR changed'):
            await self.runner.run_issue(95, execute=True)
        await self.runner.revalidate(95)
        record = self.runner.store.read('issue-95')
        self.assertIsNone(record['tested_head'])
        self.assertIsNone(record['reviewed_head'])
        await self.runner.freeze(95, self.root / 'manifest-95.json', refresh=True)
        await self.runner.run_issue(95, execute=True)
        self.assertEqual((self.commands.tests, self.commands.reviews, self.commands.merges), (2, 2, 1))

    async def test_interrupted_revalidation_cannot_validate_the_old_base_as_new(self):
        self.commands.fail_ci = True
        with self.assertRaises(GateError):
            await self.runner.run_issue(95, execute=True)
        (self.checkout / 'other.txt').write_text('New main\n')
        for args in [('add', '.'), ('commit', '-m', 'new main'),
                     ('update-ref', 'refs/remotes/origin/main', 'HEAD')]:
            subprocess.run(['git', *args], cwd=self.checkout, check=True, capture_output=True)
        target = await self.runner.head(self.checkout)
        self.commands.pr_data['baseRefOid'] = target
        original = self.commands.run

        async def cancel_merge(args, **kwargs):
            if tuple(args[:2]) == ('git', 'merge'):
                raise asyncio.CancelledError()
            return await original(args, **kwargs)

        self.commands.run = cancel_merge
        with self.assertRaises(asyncio.CancelledError):
            await self.runner.revalidate(95)
        record = self.runner.store.read('issue-95')
        self.assertEqual(record['stage'], 'revalidating')
        self.assertNotEqual(record['base'], target)
        self.assertIsNone(record['tested_head'])
        self.commands.run = original
        self.commands.fail_ci = False
        await self.runner.revalidate(95)
        await self.runner.freeze(95, self.root / 'manifest-95.json', refresh=True)
        await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.runner.store.read('issue-95')['base'], target)
        self.assertEqual((self.commands.tests, self.commands.reviews), (2, 2))

    async def test_aborted_conflict_cannot_merge_without_containing_main(self):
        self.commands.fail_ci = True
        with self.assertRaises(GateError):
            await self.runner.run_issue(95, execute=True)
        (self.checkout / 'feature.txt').write_text('Conflicting main implementation\n')
        for args in [('add', '.'), ('commit', '-m', 'conflicting main'),
                     ('update-ref', 'refs/remotes/origin/main', 'HEAD')]:
            subprocess.run(['git', *args], cwd=self.checkout, check=True, capture_output=True)
        self.commands.pr_data['baseRefOid'] = await self.runner.head(self.checkout)
        with self.assertRaisesRegex(GateError, 'merge conflicts'):
            await self.runner.revalidate(95)
        worktree = Path(self.runner.store.read('issue-95')['worktree'])
        subprocess.run(['git', 'merge', '--abort'], cwd=worktree, check=True, capture_output=True)
        self.commands.fail_ci = False
        with self.assertRaisesRegex(GateError, 'does not contain|changed|refresh'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.merges, 0)

    async def test_database_changes_require_real_database_validation(self):
        original = self.commands.run

        async def db_agent(args, **kwargs):
            result = await original(args, **kwargs)
            if args[0] == 'claude' and '-p' in args and 'Edit' in args[args.index('--tools') + 1]:
                path = kwargs['cwd'] / 'migrations/change.sql'
                path.parent.mkdir()
                path.write_text('SELECT 1;\n')
            return result

        self.commands.run = db_agent
        with patch.dict(os.environ, {}, clear=True), self.assertRaisesRegex(GateError, 'Database changes'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.merges, 0)

    async def test_agent_cannot_weaken_existing_validation_commands(self):
        original = self.commands.run

        async def bad_agent(args, **kwargs):
            result = await original(args, **kwargs)
            if args[0] == 'claude' and '-p' in args and 'Edit' in args[args.index('--tools') + 1]:
                (kwargs['cwd'] / 'Makefile').write_text('verify:\n\ttrue\n')
            return result

        self.commands.run = bad_agent
        with self.assertRaisesRegex(GateError, 'existing validation gate'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.tests, 0)

    async def agent_writes(self, files):
        original = self.commands.run

        async def writing_agent(args, **kwargs):
            result = await original(args, **kwargs)
            if args[0] == 'claude' and '-p' in args and 'Edit' in args[args.index('--tools') + 1]:
                for name, content in files.items():
                    path = kwargs['cwd'] / name
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_text(content)
            return result

        self.commands.run = writing_agent

    def commit_fixture(self, files):
        for name, content in files.items():
            path = self.checkout / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content)
        subprocess.run(['git', 'add', '.'], cwd=self.checkout, check=True, capture_output=True)
        subprocess.run(['git', 'commit', '-m', 'browser suite'], cwd=self.checkout, check=True,
                       capture_output=True)
        return subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=self.checkout, text=True).strip()

    async def test_added_paths_lists_only_paths_absent_from_the_base(self):
        existing = 'gocrm-ui/e2e/tests/login.spec.ts'
        base = self.commit_fixture({existing: 'old\n'})
        (self.checkout / existing).write_text('weakened\n')
        spec = self.checkout / 'gocrm-ui/e2e/tests/deal-board.spec.ts'
        spec.write_text('new\n')
        paths = await self.runner.changed_paths(self.checkout, base)
        self.assertEqual(await self.runner.added_paths(self.checkout, base, paths),
                         {'gocrm-ui/e2e/tests/deal-board.spec.ts'})

    async def test_agent_cannot_edit_the_browser_suite_or_vitest_setup(self):
        for name in ['gocrm-ui/e2e/helpers/auth.ts', 'gocrm-ui/playwright.config.ts',
                     'gocrm-ui/vitest.config.ts', 'gocrm-ui/src/test/setup.ts']:
            with self.subTest(name=name):
                self.tearDown()  # Each path starts from a fresh fixture repository.
                self.setUp()
                await self.asyncSetUp()
                await self.agent_writes({name: 'export {}\n'})
                with self.assertRaisesRegex(GateError, 'existing validation gate'):
                    await self.runner.run_issue(95, execute=True)
                self.assertEqual(self.commands.tests, 0)

    async def test_new_browser_spec_passes_the_gate_and_reaches_the_scope_check(self):
        await self.agent_writes({'gocrm-ui/e2e/tests/deal-board.spec.ts': 'export {}\n'})
        with self.assertRaisesRegex(GateError, 'Frontend edits require the frontend profile'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.tests, 0)

    async def test_validation_runs_in_fresh_committed_worktree(self):
        await self.runner.run_issue(95, execute=True)
        record = self.runner.store.read('issue-95')
        self.assertNotEqual(record['validation_worktree'], record['worktree'])
        self.assertEqual(await self.runner.head(Path(record['validation_worktree'])), record['head'])

    async def test_commit_response_loss_recovers_without_reimplementing(self):
        original = self.commands.run
        interrupted = False

        async def interrupt_commit(args, **kwargs):
            nonlocal interrupted
            result = await original(args, **kwargs)
            if tuple(args[:2]) == ('git', 'commit') and not interrupted:
                interrupted = True
                raise asyncio.CancelledError()
            return result

        self.commands.run = interrupt_commit
        with self.assertRaises(asyncio.CancelledError):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.runner.store.read('issue-95')['stage'], 'committing')
        await self.runner.run_issue(95, execute=True)
        self.assertEqual((self.commands.implementations, self.commands.merges), (1, 1))

    async def test_prepare_failure_does_not_claim_active_issue(self):
        with patch.object(self.runner, 'check_remote', AsyncMock(side_effect=GateError('Wrong remote'))):
            with self.assertRaises(GateError):
                await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.runner.store.read('active'), {})
        self.assertEqual(self.runner.store.read('last_error')['issue'], 95)

    async def test_ignored_agent_input_cannot_influence_validation(self):
        (self.checkout / '.gitignore').write_text('.env\n')
        for args in [('add', '.'), ('commit', '-m', 'ignore local environment'),
                     ('update-ref', 'refs/remotes/origin/main', 'HEAD')]:
            subprocess.run(['git', *args], cwd=self.checkout, check=True, capture_output=True)
        original = self.commands.run

        async def ignored_agent(args, **kwargs):
            result = await original(args, **kwargs)
            if args[0] == 'claude' and '-p' in args and 'Edit' in args[args.index('--tools') + 1]:
                (kwargs['cwd'] / '.env').write_text('TEST_OVERRIDE=true\n')
            return result

        self.commands.run = ignored_agent
        await self.runner.freeze(95, self.root / 'manifest-95.json', refresh=True)
        with self.assertRaisesRegex(GateError, 'ignored files'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.tests, 0)

    async def test_issue_allowlist_and_lock(self):
        with self.assertRaises(GateError):
            await self.runner.run_issue(1000, execute=True)
        with self.runner.store.exclusive():
            with self.assertRaisesRegex(GateError, 'Another runner stage'):
                with Store(self.config.state_dir).exclusive():
                    pass


class ProcessTests(unittest.IsolatedAsyncioTestCase):
    async def test_first_readonly_command_creates_a_private_state_parent(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            await Commands(root / 'state' / 'logs').run([sys.executable, '-c', 'print("ok")'], cwd=root)
            self.assertEqual((root / 'state').stat().st_mode & 0o777, 0o700)

    async def test_stderr_warning_does_not_corrupt_structured_stdout(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            code = 'import sys; print("warning", file=sys.stderr); print("{\\"is_error\\": false}")'
            _, output = await Commands(root / 'logs').run([sys.executable, '-c', code], cwd=root)
            self.assertEqual(agent_result(output), {'is_error': False})

    async def test_cancel_terminates_child_and_keeps_log_private(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            marker = root / 'pid'
            code = 'import os,time,pathlib; pathlib.Path("pid").write_text(str(os.getpid())); time.sleep(60)'
            task = asyncio.create_task(Commands(root / 'logs').run([sys.executable, '-c', code], cwd=root))
            for _ in range(100):
                if marker.exists():
                    break
                await asyncio.sleep(0.02)
            self.assertTrue(marker.exists())
            task.cancel()
            with self.assertRaises(asyncio.CancelledError):
                await task
            with self.assertRaises(ProcessLookupError):
                os.kill(int(marker.read_text()), 0)
            self.assertEqual(next((root / 'logs').iterdir()).stat().st_mode & 0o777, 0o600)


if __name__ == '__main__':
    unittest.main()
