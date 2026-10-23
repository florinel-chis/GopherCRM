"""Operational recovery must preserve evidence and refuse unsafe shortcuts."""

import asyncio
from dataclasses import replace
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

from gophercrm_runner import operations, retention
from gophercrm_runner.config import BudgetError, EnvironmentError, GateError
from gophercrm_runner.process import Commands, wait_command
from gophercrm_runner.resources import Samples, heavy_lease, liveness
from gophercrm_runner.state import Store
import test_batches as batch_fixtures
import test_runner


class BatchBudgetOperations(batch_fixtures.BatchTests):
    async def stopped(self):
        self.spec['entries'] = self.spec['entries'][:1]
        await self.prepare()
        with patch.object(self.batch.runner, 'validate', side_effect=RuntimeError('stop at validation')):
            with self.assertRaisesRegex(RuntimeError, 'stop at validation'):
                await self.batch.run('fixture', execute=True)
        child = self.batch.store.read('issue-95')
        child.update(stage='implement', repairs=1, receipts={'unchanged': {'sha256': 'fixture'}})
        self.batch.store.write('issue-95', child)
        return child

    async def test_extension_keeps_plan_evidence_spent_calls_and_repair_reservation(self):
        original = await self.stopped()
        plan = self.batch.store.read('batch-fixture-plan')
        event = self.fresh().extend_budget('fixture', calls=2, seconds=3600, reason='Inspected stalled validation')
        current = self.batch.store.read('issue-95')
        result = self.fresh().status('fixture')
        self.assertEqual(self.batch.store.read('batch-fixture-plan'), plan)
        self.assertEqual(current['provider_calls'], original['provider_calls'])
        self.assertEqual(current['repairs'], original['repairs'])
        self.assertEqual(current['receipts'], original['receipts'])
        self.assertEqual(current['deadline'], original['deadline'] + 3600)
        self.assertEqual(result['allocations'][0]['deadline'], current['deadline'])
        self.assertEqual(result['max_provider_calls'], plan['max_provider_calls'] + 2)
        self.assertTrue(event['resume_required'])

    async def test_interrupted_extension_recovers_before_resume_without_double_extension(self):
        original = await self.stopped()
        other = self.fresh()
        write = other.store.write
        def crash(name, data):
            if name == 'issue-95':
                raise RuntimeError('power loss between journal writes')
            write(name, data)
        with patch.object(other.store, 'write', crash), self.assertRaisesRegex(RuntimeError, 'power loss'):
            other.extend_budget('fixture', seconds=600, reason='Inspected outage')
        self.assertTrue(other.store.read('pending-update'))
        recovered = self.fresh()
        recovered.store.batch_id = 'fixture'
        with recovered.store.exclusive():
            pass
        current = recovered.store.read('issue-95')
        self.assertEqual(current['deadline'], original['deadline'] + 600)
        self.assertEqual(len(current['budget_extensions']), 1)
        self.assertEqual(recovered.store.read('pending-update'), {})

    async def test_extension_rejects_unresolved_invocation_and_wrong_owner(self):
        child = await self.stopped()
        child['provider_invocations'] = [{'status': 'running'}]
        self.batch.store.write('issue-95', child)
        with self.assertRaisesRegex(GateError, 'orphaned'):
            self.fresh().extend_budget('fixture', seconds=600, reason='not yet inspected')
        with self.assertRaisesRegex(GateError, 'batch owns'):
            with Store(self.config.state_dir).exclusive():
                pass


# Reuse setup helpers, not the entire inherited application acceptance suite.
for name in batch_fixtures.BatchTests.__dict__:
    if name.startswith('test_'):
        setattr(BatchBudgetOperations, name, None)


class OperationalReports(unittest.TestCase):
    setUp = test_runner.EngineTests.setUp
    tearDown = test_runner.EngineTests.tearDown

    def test_report_is_read_only_and_distinguishes_historical_from_active_failure(self):
        store = Store(self.config.state_dir)
        record = dict(issue=95, stage='merged', head='abc', provider_calls=2, max_provider_calls=8,
                      deadline=1, worktree=str(self.checkout), last_error='old assertion',
                      last_failure={'category': 'implementation/test'}, attempts=[])
        store.write('issue-95', record)
        before = (self.config.state_dir / 'issue-95.json').read_bytes()
        result = operations.handoff(self.config, issue=95)
        self.assertIsNone(result['active_stop_reason'])
        self.assertIn('terminal', result['next_action'])
        self.assertEqual((self.config.state_dir / 'issue-95.json').read_bytes(), before)
        self.assertIn('workflow report', operations.markdown(result))

    def test_history_ignores_backups_and_does_not_treat_cancelled_as_completed(self):
        store = Store(self.config.state_dir)
        record = dict(issue=95, stage='merged', execution_id='one', policy='same',
                      validation_plan={'profile': 'full'}, attempts=[
                          dict(stage='validation', duration_seconds=60, outcome='success')])
        store.write('issue-95', record)
        store.write('issue-95.legacy-backup', dict(record, execution_id='fake'))
        store.write('issue-96', dict(record, execution_id='two', stage='validate'))
        result = operations.measurements(self.config.state_dir)
        self.assertEqual(len(result['runs']), 2)
        self.assertEqual(result['groups'][0]['sample_count'], 1)
        self.assertEqual(result['groups'][0]['median_validation_seconds'], 60)

    def test_preview_does_not_narrow_shared_auth_persistence(self):
        config = replace(self.config, validation_mode='contextual')
        result = operations.validation_preview(config, ['internal/service/auth_service.go',
                                                       'internal/repository/interfaces.go'])
        self.assertEqual(result['selection']['profile'], 'full')
        self.assertTrue(result['missing_database_policy'])
        self.assertEqual(result['estimate']['status'], 'unknown')

    def test_process_identity_mismatch_is_not_liveness(self):
        with patch('gophercrm_runner.resources.process_identity', return_value='new-process'):
            self.assertEqual(liveness({'pid': 123, 'process_identity': 'old-process'})['status'], 'identity-mismatch')


class ResourceTests(unittest.IsolatedAsyncioTestCase):
    async def test_backward_clock_jump_cannot_extend_command_timeout(self):
        class Process:
            async def communicate(self, value):
                await asyncio.Event().wait()
        clock, monotonic = iter([100, 100, 0]), iter([0, 0, 1001])
        with self.assertRaises(asyncio.TimeoutError):
            await wait_command(Process(), None, 1000, 200, clock=lambda: next(clock),
                               monotonic=lambda: next(monotonic), interval=0.001)

    async def test_wall_clock_jump_interrupts_inflight_command(self):
        class Process:
            async def communicate(self, value):
                await asyncio.Event().wait()
        clock = iter([100, 100, 500])
        with self.assertRaisesRegex(BudgetError, 'wall-clock'):
            await wait_command(Process(), None, 1000, 200, clock=lambda: next(clock), interval=0.001)

    async def test_nonzero_and_zero_commands_do_not_pass_after_deadline(self):
        class Process:
            async def communicate(self, value):
                return b'', b''
        clock = iter([100, 100, 201])
        with self.assertRaisesRegex(BudgetError, 'finished after'):
            await wait_command(Process(), None, 1000, 200, clock=lambda: next(clock))

    async def test_command_cancellation_terminates_process_group(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            pidfile = root / 'pid'
            code = 'import os,time; from pathlib import Path; Path(' + repr(str(pidfile)) + ').write_text(str(os.getpid())); time.sleep(60)'
            task = asyncio.create_task(Commands(root / 'logs').run([sys.executable, '-c', code], cwd=root))
            for _ in range(100):
                if pidfile.exists():
                    break
                await asyncio.sleep(0.01)
            self.assertTrue(pidfile.exists())
            pid = int(pidfile.read_text())
            task.cancel()
            with self.assertRaises(asyncio.CancelledError):
                await task
            with self.assertRaises(ProcessLookupError):
                os.kill(pid, 0)

    async def test_machine_lease_rejects_second_state_and_releases_on_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / 'lease'
            with heavy_lease(root):
                with self.assertRaisesRegex(EnvironmentError, 'Another heavy'):
                    with heavy_lease(root):
                        pass
            with heavy_lease(root):
                pass

    async def test_resource_samples_are_bounded_and_disk_floor_stops(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            sample = Samples(root / 'resources.json', root, os.getpid(), 10)
            observation = {'at': 1, 'free_disk_bytes': 100, 'owned_processes': [], 'memory': {'status': 'unavailable'}}
            with patch('gophercrm_runner.resources.snapshot', return_value=observation):
                for _ in range(65):
                    sample.sample()
            self.assertEqual(len(sample.value['samples']), 60)
            self.assertEqual(sample.value['count'], 65)
            with patch('gophercrm_runner.resources.snapshot', return_value=dict(observation, free_disk_bytes=5)):
                with self.assertRaises(EnvironmentError):
                    sample.sample()
            self.assertEqual(json.loads(sample.path.read_text())['latest']['free_disk_bytes'], 5)


class RetentionTests(unittest.TestCase):
    def setUp(self):
        test_runner.EngineTests.setUp(self)
        self.lease = patch('gophercrm_runner.retention.heavy_lease', lambda: heavy_lease(self.root / 'lease'))
        self.lease.start()

    def tearDown(self):
        self.lease.stop()
        test_runner.EngineTests.tearDown(self)

    def tree(self):
        self.config = replace(self.config, state_dir=self.config.state_dir.resolve())
        tree = self.config.state_dir / 'validation/95-abcdef123456'
        tree.parent.mkdir(parents=True)
        (self.checkout / '.gitignore').write_text('gocrm-ui/node_modules/\n')
        subprocess.run(['git', 'add', '.gitignore'], cwd=self.checkout, check=True, capture_output=True)
        subprocess.run(['git', 'commit', '-m', 'ignore dependencies'], cwd=self.checkout, check=True, capture_output=True)
        subprocess.run(['git', 'worktree', 'add', '--detach', str(tree)], cwd=self.checkout, check=True, capture_output=True)
        target = tree / 'gocrm-ui/node_modules'
        target.mkdir(parents=True)
        (target / 'dependency').write_bytes(b'123456')
        head = retention.git(tree, 'rev-parse', 'HEAD')
        Store(self.config.state_dir).write('issue-95', dict(issue=95, stage='merged', merge_commit='confirmed', head=head))
        return tree, target

    def test_preview_apply_preserves_source_evidence_and_refuses_stale_hash(self):
        tree, target = self.tree()
        original = (tree / 'README.md').read_bytes()
        first = retention.reclaim(self.config)
        self.assertEqual(first['reclaimable_bytes'], 6)
        self.assertTrue(target.exists())
        (target / 'extra').write_text('changed')
        with self.assertRaisesRegex(GateError, 'selection changed'):
            retention.reclaim(self.config, apply=True, plan_hash=first['plan_hash'])
        current = retention.reclaim(self.config)
        result = retention.reclaim(self.config, apply=True, plan_hash=current['plan_hash'])
        self.assertEqual(result['removed'], [str(target)])
        self.assertFalse(target.exists())
        self.assertEqual((tree / 'README.md').read_bytes(), original)
        self.assertTrue((self.config.state_dir / 'issue-95.json').exists())

    def test_active_marker_dirty_source_and_backup_journal_never_authorize_removal(self):
        tree, target = self.tree()
        store = Store(self.config.state_dir)
        record = store.read('issue-95')
        store.write('issue-95.legacy-abc', record)
        store.write('issue-95', dict(record, stage='validate'))
        self.assertEqual(retention.preview(self.config)['candidates'], [])
        store.write('issue-95', record)
        store.write('active', {'issue': 95})
        self.assertEqual(retention.preview(self.config)['candidates'], [])
        store.write('active', {})
        (tree / 'README.md').write_text('uncommitted work')
        self.assertEqual(retention.preview(self.config)['candidates'], [])
        self.assertTrue(target.exists())

    def test_symlinked_dependencies_are_never_followed(self):
        tree, target = self.tree()
        target.rename(target.with_name('kept'))
        target.symlink_to(target.with_name('kept'), target_is_directory=True)
        self.assertEqual(retention.preview(self.config)['candidates'], [])
        self.assertTrue((target / 'dependency').exists())
