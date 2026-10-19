import asyncio
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import AsyncMock, patch

from gophercrm_runner.config import CommandError, Config, GateError, REQUIRED_CHECKS
from gophercrm_runner.engine import Runner
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
                     'local.db', 'key.pem', 'automation/langflow/gophercrm_runner/engine.py']:
            with self.subTest(name=name), self.assertRaises(GateError):
                validate_paths([name])

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

    async def run(self, args, *, cwd, **kwargs):
        args = tuple(args)
        self.calls.append(args)
        versions = {('git', '--version'): 'git version 2.54.0',
                    ('gh', '--version'): 'gh version 2.92.0',
                    ('claude', '--version'): '2.1.303 (Claude Code)',
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
            if '--json-schema' in args:
                self.reviews += 1
                rejected = self.reviews <= self.reject_reviews
                result = dict(is_error=False, structured_output=dict(approved=not rejected,
                    blocking_findings=['Concrete missing edge case'] if rejected else [], summary='Reviewed'))
            else:
                self.implementations += 1
                (cwd / 'feature.txt').write_text(f'Implementation {self.implementations}\n')
                result = dict(is_error=False, result='Implemented')
            return 0, json.dumps(result)
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
        subprocess.run(['git', 'add', '.'], cwd=self.checkout, check=True, capture_output=True)
        subprocess.run(['git', 'commit', '-m', 'fixture'], cwd=self.checkout, check=True, capture_output=True)
        subprocess.run(['git', 'update-ref', 'refs/remotes/origin/main', 'HEAD'],
                       cwd=self.checkout, check=True, capture_output=True)
        self.config = Config(repository='example/repo', checkout=self.checkout,
                             state_dir=self.root / 'state', issues=(95, 96), max_repair_rounds=2,
                             min_free_disk_mb=1)
        self.commands = FakeCommands(self.root)
        self.commands.base = subprocess.check_output(['git', 'rev-parse', 'HEAD'],
                                                     cwd=self.checkout, text=True).strip()
        self.runner = Runner(self.config, self.commands)

    def tearDown(self):
        self.temp.cleanup()

    async def test_preview_has_no_mutations_or_agent_calls(self):
        result = await self.runner.run_issue(95)
        self.assertEqual(result['status'], 'planned')
        self.assertFalse(self.config.state_dir.exists())
        self.assertEqual(self.commands.calls, [('gh', 'issue', 'view', '95', '--json',
            'number,title,body,state,url', '--repo', 'example/repo')])

    async def test_review_repair_retests_exact_commit_then_merges_once(self):
        self.commands.reject_reviews = 1
        result = await self.runner.run_issue(95, execute=True)
        self.assertEqual(result['status'], 'merged')
        self.assertEqual((self.commands.implementations, self.commands.tests, self.commands.reviews), (2, 2, 2))
        self.assertEqual((self.commands.creates, self.commands.merges), (1, 1))
        record = self.runner.store.read('issue-95')
        self.assertEqual(record['head'], record['tested_head'])
        self.assertEqual(record['head'], record['reviewed_head'])
        self.assertEqual(self.runner.store.read('active'), {})
        self.assertEqual(await self.runner.head(self.checkout), self.commands.base)
        # A repeated request reconciles the existing merge without another agent or PR.
        await self.runner.run_issue(95, execute=True)
        self.assertEqual((self.commands.implementations, self.commands.merges), (2, 1))

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
        with self.assertRaisesRegex(GateError, 'does not contain'):
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
