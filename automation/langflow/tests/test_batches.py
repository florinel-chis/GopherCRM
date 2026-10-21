"""Exercise the supervisor through real ticket gates and real Git squash merges."""
import asyncio
from dataclasses import asdict, replace
import json
import os
from pathlib import Path
import subprocess
import sys
import unittest
from unittest.mock import patch

from gophercrm_runner.batches import BatchRunner
from gophercrm_runner.config import BudgetError, GateError
from gophercrm_runner.state import Store
import test_runner
from test_runner import FakeCommands, fixture_manifest, ready_pr


class BatchCommands(FakeCommands):
    def __init__(self, root, checkout):
        super().__init__(root)
        self.checkout = checkout
        self.prs = {}
        self.after_merge = None
        self.no_change = False

    def git(self, *args):
        return subprocess.check_output(['git', *args], cwd=self.checkout, text=True).strip()

    async def run(self, args, *, cwd, **kwargs):
        args = tuple(args)
        if args[:2] == ('git', 'fetch') and args[-1].startswith('+refs/pull/'):
            source, target = args[-1].split(':')
            self.git('update-ref', target, self.prs[int(source.split('/')[2])]['headRefOid'])
            return 0, ''
        if args[:3] == ('git', 'ls-remote', 'origin'):
            self.calls.append(args)
            return 0, self.git('rev-parse', 'origin/main') + '\trefs/heads/main\n'
        if args[:3] == ('gh', 'pr', 'list'):
            self.calls.append(args)
            branch = args[args.index('--head') + 1]
            return 0, json.dumps([p for p in self.prs.values() if p['headRefName'] == branch])
        if args[:3] == ('gh', 'pr', 'create'):
            self.calls.append(args)
            self.creates += 1
            number = 121 + self.creates
            self.pr_data = dict(ready_pr(), number=number, headRefOid=self.pushed_head,
                headRefName=args[args.index('--head') + 1],
                baseRefOid=self.git('rev-parse', 'origin/main'), url=f'https://github.com/example/repo/pull/{number}')
            self.prs[number] = self.pr_data
            return 0, self.pr_data['url']
        if args[:3] == ('gh', 'pr', 'view'):
            self.pr_data = self.prs[int(args[3])]
        if args[:3] == ('gh', 'pr', 'merge'):
            self.calls.append(args)
            pr = self.prs[int(args[3])]
            assert args[args.index('--match-head-commit') + 1] == pr['headRefOid']
            assert '--admin' not in args
            self.merges += 1
            merge = self.git('commit-tree', self.git('rev-parse', pr['headRefOid'] + '^{tree}'),
                             '-p', self.git('rev-parse', 'origin/main'), '-m', f'Merge {pr["number"]}')
            self.git('update-ref', 'refs/remotes/origin/main', merge)
            pr.update(state='MERGED', mergeCommit={'oid': merge})
            if self.after_merge:
                self.after_merge()
            return 0, ''
        if args[:2] == ('git', 'push'):
            # An earlier merged PR must retain its original head.
            self.pr_data = None
        if args[0] == 'claude' and '--tools' in args and 'Edit' in args[args.index('--tools') + 1] and self.no_change:
            self.implementations += 1
            return 0, json.dumps(dict(is_error=False, structured_output=dict(
                summary='No changes', addressed_criteria=['AC-1'], remaining_gaps=[])))
        result = await super().run(args, cwd=cwd, **kwargs)
        if args[0] == 'claude' and '--tools' in args and 'pr-worktrees' in str(cwd):
            code, text = result
            parsed = json.loads(text)
            verdict = parsed['structured_output']
            verdict['criteria' if 'criteria' in verdict else 'addressed_criteria'] = []
            return code, json.dumps(parsed)
        return result


class BatchTests(unittest.IsolatedAsyncioTestCase):
    setUp = test_runner.EngineTests.setUp
    tearDown = test_runner.EngineTests.tearDown

    async def asyncSetUp(self):
        self.root = self.root.resolve()
        self.checkout = self.checkout.resolve()
        self.config = replace(self.config, checkout=self.checkout, state_dir=self.config.state_dir.resolve())
        self.commands = BatchCommands(self.root, self.checkout)
        self.commands.base = self.commands.git('rev-parse', 'HEAD')
        self.batch = BatchRunner(self.config, self.commands)
        self.spec = dict(schema_version=1, id='fixture', initial_base=self.commands.base,
                         max_items=2, max_provider_calls=8, timeout_seconds=3600, entries=[])
        for number in (95, 96):
            path = self.root / f'manifest-{number}.json'
            path.write_text(json.dumps(fixture_manifest(number)))
            self.spec['entries'].append(dict(kind='issue', issue=number, manifest=str(path), source_paths=['README.md']))
        self.path = self.root / 'batch.json'

    async def prepare(self):
        self.path.write_text(json.dumps(self.spec))
        return await self.batch.prepare(self.path)

    def fresh(self, **kwargs):
        return BatchRunner(self.config, self.commands, **kwargs)

    async def test_two_tickets_freeze_at_admission_and_pass_real_pipeline(self):
        await self.prepare()
        before = self.commands.implementations
        preview = await self.batch.run('fixture')
        self.assertEqual(preview['status'], 'preview')
        self.assertEqual(self.commands.implementations, before)
        self.assertFalse((self.config.state_dir / 'tasks/95/contract.json').exists())
        result = await self.batch.run('fixture', execute=True)
        self.assertEqual(result['status'], 'completed')
        self.assertEqual((self.commands.implementations, self.commands.reviews, self.commands.merges), (2, 2, 2))
        self.assertEqual(result['provider_calls'], 4)
        first, second = [self.batch.store.read(f'issue-{n}') for n in (95, 96)]
        self.assertEqual(second['base'], first['merge_commit'])
        for record in [first, second]:
            self.assertEqual(record['head'], record['tested_head'])
            self.assertEqual(record['head'], record['reviewed_head'])
        self.assertTrue(self.batch.store.stopped())
        self.assertEqual(self.batch.store.read('batch-owner'), {})
        self.assertEqual((await self.fresh().run('fixture', execute=True))['status'], 'completed')
        self.assertEqual(self.commands.merges, 2)

    async def test_stop_after_current_prevents_preloaded_next_ticket(self):
        await self.prepare()
        self.commands.after_merge = lambda: self.batch.stop('fixture')
        result = await self.batch.run('fixture', execute=True)
        self.assertEqual((result['status'], result['index']), ('stopped', 1))
        self.assertFalse(self.batch.store.read('issue-96'))
        with self.assertRaisesRegex(GateError, 'batch owns'):
            with Store(self.config.state_dir).exclusive():
                pass
        self.commands.after_merge = None
        result = await self.fresh().run('fixture', execute=True, resume=True)
        self.assertEqual(result['status'], 'completed')
        self.assertEqual(self.commands.merges, 2)

    async def test_lost_merge_response_reconciles_after_expiry_without_calls(self):
        self.spec['entries'] = self.spec['entries'][:1]
        await self.prepare()
        def lost():
            raise RuntimeError('lost merge response')
        self.commands.after_merge = lost
        with self.assertRaisesRegex(RuntimeError, 'lost merge'):
            await self.batch.run('fixture', execute=True)
        deadline = self.batch.record['deadline']
        calls = self.commands.implementations + self.commands.reviews
        result = await self.fresh(clock=lambda: deadline + 100).run('fixture', execute=True)
        self.assertEqual(result['status'], 'completed')
        self.assertEqual(self.commands.implementations + self.commands.reviews, calls)
        self.assertEqual(self.commands.merges, 1)

    async def test_total_call_limit_blocks_next_ticket_without_refund(self):
        self.spec['max_provider_calls'] = 2
        await self.prepare()
        with self.assertRaises(BudgetError):
            await self.batch.run('fixture', execute=True)
        self.assertEqual(self.batch.status('fixture')['provider_calls'], 2)
        with self.assertRaises(BudgetError):
            await self.fresh().run('fixture', execute=True, resume=True)
        self.assertFalse(self.batch.store.read('issue-96'))

    async def test_changed_manifest_source_policy_or_main_fails_before_model(self):
        await self.prepare()
        path = Path(self.spec['entries'][1]['manifest'])
        original = path.read_text()
        path.write_text(original.replace('Feature regression passes', 'Changed requirement'))
        with self.assertRaisesRegex(GateError, 'manifest changed'):
            await self.batch.run('fixture', execute=True)
        path.write_text(original)
        other = BatchRunner(replace(self.config, max_provider_calls=99), self.commands)
        with self.assertRaisesRegex(GateError, 'policy'):
            await other.run('fixture', execute=True)
        self.commands.git('update-ref', 'refs/remotes/origin/main',
            self.commands.git('commit-tree', 'HEAD^{tree}', '-p', 'HEAD', '-m', 'external'))
        with self.assertRaisesRegex(GateError, 'Main differs'):
            await self.batch.run('fixture', execute=True)
        self.assertEqual(self.commands.implementations, 0)

    async def test_prior_merge_invalidates_guard_before_next_model(self):
        self.spec['entries'][1]['source_paths'] = ['feature.txt']
        await self.prepare()
        with self.assertRaisesRegex(GateError, 'source assumptions'):
            await self.batch.run('fixture', execute=True)
        self.assertEqual(self.commands.implementations, 1)
        self.assertEqual(self.batch.record['index'], 1)

    async def test_missing_acceptance_and_ui_profiles_rejected(self):
        path = Path(self.spec['entries'][0]['manifest'])
        for field, value in [('acceptance', []), ('validation_profile', 'frontend')]:
            manifest = fixture_manifest(95)
            manifest[field] = value
            path.write_text(json.dumps(manifest))
            with self.assertRaises(GateError):
                await self.prepare()
        self.assertEqual(self.commands.implementations, 0)

    async def test_no_change_and_rejected_review_never_merge(self):
        await self.prepare()
        self.commands.no_change = True
        with self.assertRaises(GateError):
            await self.batch.run('fixture', execute=True)
        self.assertEqual(self.commands.merges, 0)
        self.assertFalse(self.batch.store.read('issue-96'))

    async def test_execution_lock_is_reentrant_only_for_same_task(self):
        store = self.batch.store
        async def competitor():
            with self.assertRaisesRegex(GateError, 'Another runner'):
                with store.exclusive():
                    pass
        with store.exclusive():
            await asyncio.create_task(competitor())
            with store.exclusive():
                with self.assertRaises(GateError):
                    with Store(self.config.state_dir).exclusive():
                        pass

    async def test_existing_pinned_pr_uses_completion_before_next_ticket(self):
        source = self.root / 'external'
        self.commands.git('worktree', 'add', '-b', 'external', str(source))
        (source / 'feature.txt').write_text('External fix\n')
        for args in [('add', '.'), ('commit', '-m', 'external')]:
            subprocess.run(['git', *args], cwd=source, check=True, capture_output=True)
        head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=source, text=True).strip()
        self.commands.prs[120] = dict(ready_pr(), number=120, headRefOid=head,
            baseRefOid=self.commands.base, headRefName='external', title='Fix external feature',
            body='Expected behavior', isCrossRepository=False)
        self.spec['entries'][0] = dict(kind='pr', pr=120, head=head)
        await self.prepare()
        result = await self.batch.run('fixture', execute=True)
        self.assertEqual(result['status'], 'completed')
        self.assertEqual((self.commands.reviews, self.commands.implementations, self.commands.merges), (2, 1, 2))
        self.assertEqual(self.batch.store.read('issue-96')['base'],
                         self.batch.store.read('completion-pr-120')['merge_commit'])

    async def test_crash_after_allocation_restores_active_and_spent_deadline(self):
        await self.prepare()
        original = self.batch.store.write
        def crash(name, value):
            if name == 'active':
                raise RuntimeError('interrupted active write')
            return original(name, value)
        with patch.object(self.batch.store, 'write', crash), self.assertRaisesRegex(RuntimeError, 'active write'):
            await self.batch.run('fixture', execute=True)
        allocation = self.batch.record['allocations'][0].copy()
        self.batch.stop('fixture')
        # Admission already happened; finish it, then honor stop before #96.
        record = self.batch.store.read('batch-fixture')
        record['status'] = 'running'
        self.batch.store.write('batch-fixture', record)
        result = await self.fresh().run('fixture', execute=True)
        self.assertEqual((result['index'], result['status']), (1, 'stopped'))
        child = self.batch.store.read('issue-95')
        self.assertEqual(child['deadline'], allocation['deadline'])
        self.assertEqual(child['execution_id'], allocation['execution_id'])
        self.assertEqual(self.commands.merges, 1)

    async def test_crash_after_terminal_save_before_advancement_and_cleanup(self):
        self.spec['entries'] = self.spec['entries'][:1]
        await self.prepare()
        finish = self.batch.runner.finish
        async def crash(*args):
            await finish(*args)
            raise RuntimeError('interrupted after terminal save')
        with patch.object(self.batch.runner, 'finish', crash), self.assertRaisesRegex(RuntimeError, 'terminal save'):
            await self.batch.run('fixture', execute=True)
        self.assertEqual(self.batch.store.read('issue-95')['stage'], 'merged')
        self.assertEqual(self.batch.record['index'], 0)
        original = self.batch.store.write
        def crash_cleanup(name, value):
            if name == 'batch-owner' and not value:
                raise RuntimeError('interrupted cleanup')
            return original(name, value)
        other = self.fresh()
        with patch.object(other.store, 'write', crash_cleanup), self.assertRaisesRegex(RuntimeError, 'cleanup'):
            await other.run('fixture', execute=True)
        result = await self.fresh().run('fixture', execute=True)
        self.assertEqual(result['status'], 'completed')
        self.assertEqual(result['provider_calls'], 2)
        self.assertEqual(self.commands.merges, 1)
        self.assertFalse(self.batch.store.read('batch-owner'))

    async def test_changed_issue_fails_before_model(self):
        await self.prepare()
        original = self.commands.run
        async def changed(args, **kwargs):
            code, text = await original(args, **kwargs)
            if tuple(args[:3]) == ('gh', 'issue', 'view') and args[3] == '96':
                text = json.dumps(dict(json.loads(text), body='New acceptance criteria'))
            return code, text
        with patch.object(self.commands, 'run', changed), self.assertRaisesRegex(GateError, 'source changed'):
            await self.batch.run('fixture', execute=True)
        self.assertEqual(self.commands.implementations, 0)

    async def test_unrelated_main_after_merge_stops_next_admission(self):
        await self.prepare()
        def external():
            self.commands.git('update-ref', 'refs/remotes/origin/main',
                self.commands.git('commit-tree', 'origin/main^{tree}', '-p', 'origin/main', '-m', 'external'))
        self.commands.after_merge = external
        with self.assertRaisesRegex(GateError, 'Unrelated main'):
            await self.batch.run('fixture', execute=True)
        self.assertEqual((self.commands.merges, self.commands.implementations), (1, 1))

    async def test_rejected_reviews_exhaust_repairs_without_next_admission(self):
        await self.prepare()
        self.commands.reject_reviews = 100
        with self.assertRaisesRegex(GateError, 'budget exhausted'):
            await self.batch.run('fixture', execute=True)
        self.assertEqual(self.commands.merges, 0)
        calls = self.batch.status('fixture')['provider_calls']
        with self.assertRaises(GateError):
            await self.fresh().run('fixture', execute=True, resume=True)
        self.assertEqual(self.batch.status('fixture')['provider_calls'], calls)
        self.assertFalse(self.batch.store.read('issue-96'))

    async def test_missing_dependency_or_registered_test_rejected_at_prepare(self):
        path = Path(self.spec['entries'][0]['manifest'])
        manifest = fixture_manifest(95)
        manifest['acceptance'][0]['verification']['check_id'] = 'missing'
        path.write_text(json.dumps(manifest))
        with self.assertRaisesRegex(GateError, 'unregistered'):
            await self.prepare()
        manifest = fixture_manifest(95)
        manifest['dependencies'] = [dict(issue=94, pr=122, merge_commit=self.commands.base)]
        path.write_text(json.dumps(manifest))
        self.commands.prs[122] = dict(ready_pr(), state='OPEN')
        with self.assertRaisesRegex(GateError, 'Dependency'):
            await self.prepare()
        self.assertEqual(self.commands.implementations, 0)

    async def test_orphan_acknowledgement_preserves_spent_calls_and_limits(self):
        await self.prepare()
        self.commands.no_change = True
        with self.assertRaises(GateError):
            await self.batch.run('fixture', execute=True)
        child = self.batch.store.read('issue-95')
        child['provider_invocations'][0]['status'] = 'running'
        self.batch.store.write('issue-95', child)
        with self.assertRaises(GateError):
            self.fresh().abandon('fixture', 'Inspect orphan first')
        with self.assertRaises(GateError):
            self.fresh().recover('fixture', 'Inspected')
        self.fresh().recover('fixture', 'Inspected processes', processes_stopped=True)
        after = self.batch.store.read('issue-95')
        for key in ('provider_calls', 'deadline', 'max_provider_calls', 'head', 'stage'):
            self.assertEqual(after[key], child[key])
        self.assertEqual(after['provider_invocations'][0]['status'], 'operator-resolved')
        self.fresh().abandon('fixture', 'Retain unfinished work')
        self.assertFalse(self.batch.store.read('batch-owner'))
        self.assertEqual(self.batch.store.read('active'), {'issue': 95})

    async def test_malformed_review_stops_before_publication(self):
        await self.prepare()
        original = self.commands.run
        async def malformed(args, **kwargs):
            code, text = await original(args, **kwargs)
            if args[0] == 'claude' and '--tools' in args and 'Edit' not in args[args.index('--tools') + 1]:
                return 0, '{"is_error":false,"structured_output":{"approved":true}}'
            return code, text
        with patch.object(self.commands, 'run', malformed), self.assertRaises(GateError):
            await self.batch.run('fixture', execute=True)
        self.assertEqual(self.commands.creates, 0)
        self.assertFalse(self.batch.store.read('issue-96'))

    async def test_out_of_scope_implementation_never_publishes(self):
        await self.prepare()
        original = self.commands.run
        async def outside(args, **kwargs):
            result = await original(args, **kwargs)
            if args[0] == 'claude' and '--tools' in args and 'Edit' in args[args.index('--tools') + 1]:
                (kwargs['cwd'] / 'README.md').write_text('Unrelated change\n')
            return result
        with patch.object(self.commands, 'run', outside), self.assertRaisesRegex(GateError, 'scope'):
            await self.batch.run('fixture', execute=True)
        self.assertEqual(self.commands.creates, 0)
        self.assertFalse(self.batch.store.read('issue-96'))

    async def test_crash_before_remote_merge_reuses_gates_without_duplicate_pr(self):
        self.spec['entries'] = self.spec['entries'][:1]
        await self.prepare()
        original = self.commands.run
        async def before_merge(args, **kwargs):
            if tuple(args[:3]) == ('gh', 'pr', 'merge'):
                raise RuntimeError('interrupted before merge')
            return await original(args, **kwargs)
        with patch.object(self.commands, 'run', before_merge), self.assertRaisesRegex(RuntimeError, 'before merge'):
            await self.batch.run('fixture', execute=True)
        calls = self.batch.status('fixture')['provider_calls']
        result = await self.fresh().run('fixture', execute=True, resume=True)
        self.assertEqual((result['status'], result['provider_calls']), ('completed', calls))
        self.assertEqual((self.commands.creates, self.commands.merges), (1, 1))

    async def test_crash_before_admission_keeps_deadline_and_external_owner_blocked(self):
        await self.prepare()
        with patch.object(self.batch.runner, 'doctor', side_effect=RuntimeError('preflight interrupted')):
            with self.assertRaisesRegex(RuntimeError, 'preflight'):
                await self.batch.run('fixture', execute=True)
        deadline = self.batch.record['deadline']
        self.assertFalse(self.batch.record['allocations'])
        with self.assertRaises(BudgetError):
            await self.fresh(clock=lambda: deadline + 1).run('fixture', execute=True, resume=True)
        self.assertEqual(self.batch.store.read('batch-fixture')['deadline'], deadline)
        self.assertEqual(self.commands.implementations, 0)

    async def test_stop_during_initial_preview_is_not_cleared_by_admission(self):
        await self.prepare()
        preview = self.batch.preview
        async def stop_after_preview(name):
            result = await preview(name)
            self.batch.store.stop_after_current()
            return result
        with patch.object(self.batch, 'preview', stop_after_preview):
            result = await self.batch.run('fixture', execute=True)
        self.assertEqual((result['status'], result['index']), ('stopped', 0))
        self.assertEqual(self.commands.implementations, 0)

    async def test_stop_during_resume_reconciliation_wins_over_resume(self):
        await self.prepare()
        self.commands.after_merge = lambda: self.batch.stop('fixture')
        await self.batch.run('fixture', execute=True)
        self.commands.after_merge = None
        other = self.fresh()
        reconcile = other.reconcile
        async def stop_during_reconcile():
            result = await reconcile()
            other.stop('fixture')
            return result
        with patch.object(other, 'reconcile', stop_during_reconcile):
            result = await other.run('fixture', execute=True, resume=True)
        self.assertEqual((result['status'], result['index']), ('stopped', 1))
        self.assertFalse(self.batch.store.read('issue-96'))

    async def test_stop_after_launch_snapshot_prevents_first_admission(self):
        await self.prepare()
        snapshot = self.batch.control_snapshot()
        self.batch.store.stop_after_current()
        result = await self.fresh().run('fixture', execute=True, control_snapshot=snapshot)
        self.assertEqual((result['status'], result['index']), ('stopped', 0))
        self.assertEqual(self.commands.implementations, 0)

    async def test_detached_worker_uses_real_graph_survives_launcher_and_honors_stop(self):
        from gophercrm_runner.batch_worker import start
        await self.prepare()
        policy = self.root / 'policy.json'
        policy.write_text(json.dumps(asdict(self.config), default=str))
        bootstrap = self.root / 'bootstrap'
        bootstrap.mkdir()
        release = self.root / 'release-provider'
        # The subprocess runs the production worker, graph, supervisor and
        # ticket pipeline. Only provider/GitHub/test command boundaries are fake.
        (bootstrap / 'sitecustomize.py').write_text(
            'import asyncio\nfrom pathlib import Path\n'
            'from test_batches import BatchCommands\n'
            'from gophercrm_runner.batches import BatchRunner\n'
            '_init = BatchRunner.__init__\n'
            'def init(self, config, commands=None, **kwargs):\n'
            f'    commands = BatchCommands(Path({str(self.root)!r}), config.checkout)\n'
            '    commands.base = commands.git("rev-parse", "HEAD")\n'
            '    original = commands.run\n'
            '    async def run(args, **kw):\n'
            '        if args[0] == "claude" and "--tools" in args:\n'
            f'            while not Path({str(release)!r}).exists():\n'
            '                await asyncio.sleep(0.05)\n'
            '        return await original(args, **kw)\n'
            '    commands.run = run\n'
            '    _init(self, config, commands, **kwargs)\n'
            'BatchRunner.__init__ = init\n')
        source = Path(__file__).resolve().parents[1]
        pythonpath = os.pathsep.join([str(bootstrap), str(source / 'tests'), str(source)])
        launch = ('from gophercrm_runner.batch_worker import start; import json; '
                  f'print(json.dumps(start({str(policy)!r}, "fixture")))')
        env = dict(os.environ, PYTHONPATH=pythonpath)
        process = await asyncio.create_subprocess_exec(sys.executable, '-c', launch, env=env,
            stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
        stdout, stderr = await asyncio.wait_for(process.communicate(), 20)
        self.assertEqual(process.returncode, 0, stderr.decode())
        worker = json.loads(stdout)
        pid = worker['pid']
        try:
            for _ in range(400):
                child = self.batch.store.read('issue-95')
                if child.get('provider_calls'):
                    break
                await asyncio.sleep(0.05)
            self.assertEqual(child.get('provider_calls'), 1, Path(worker['log']).read_text())
            self.assertEqual(os.getsid(pid), pid)
            with self.assertRaises(GateError):
                start(policy, 'fixture')
            self.batch.stop('fixture')
        finally:
            release.touch()
        for _ in range(400):
            status = self.batch.store.read('batch-fixture-worker')
            if status.get('status') == 'exited':
                break
            await asyncio.sleep(0.05)
        self.assertEqual(status.get('exit_code'), 0, Path(worker['log']).read_text())
        self.assertEqual(self.batch.status('fixture')['status'], 'stopped')
        self.assertEqual(self.batch.status('fixture')['index'], 1)
        self.assertFalse(self.batch.store.read('issue-96'))
        self.assertEqual(Path(worker['log']).stat().st_mode & 0o777, 0o600)
        # Explicit restart without --resume observes the stop; no automatic work.
        with patch.dict(os.environ, {'PYTHONPATH': pythonpath}):
            second = start(policy, 'fixture')
        for _ in range(400):
            status = self.batch.store.read('batch-fixture-worker')
            if status.get('token') == second['token'] and status.get('status') == 'exited':
                break
            await asyncio.sleep(0.05)
        self.assertEqual(status.get('exit_code'), 0, Path(second['log']).read_text())
        self.assertFalse(self.batch.store.read('issue-96'))
