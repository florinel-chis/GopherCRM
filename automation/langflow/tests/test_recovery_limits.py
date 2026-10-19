import asyncio
from dataclasses import replace
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest

import test_runner as fixtures
from gophercrm_runner.config import BudgetError, CommandError, GateError
from gophercrm_runner.engine import Runner


class RecoveryLimitTests(fixtures.EngineTests):
    async def test_completed_implementation_resumes_without_another_provider_call(self):
        original = self.runner.finalize_implementation
        async def interrupted(record):
            raise asyncio.CancelledError()
        self.runner.finalize_implementation = interrupted
        with self.assertRaises(asyncio.CancelledError):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.runner.store.read('issue-95')['stage'], 'finalize_implementation')
        self.runner.finalize_implementation = original
        await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.implementations, 1)

    async def test_durable_rejected_review_is_not_rerolled_after_postreview_interruption(self):
        self.commands.reject_reviews = 1
        original = self.runner.clean
        async def interrupt(worktree):
            if self.commands.reviews == 1:
                raise asyncio.CancelledError()
            await original(worktree)
        self.runner.clean = interrupt
        with self.assertRaises(asyncio.CancelledError):
            await self.runner.run_issue(95, execute=True)
        self.runner.clean = original
        await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.commands.implementations, 2)
        self.assertEqual(self.runner.store.read('issue-95')['repairs'], 1)

    async def test_validation_timeout_uses_remaining_total_deadline(self):
        now = [1000.0]
        self.runner = Runner(replace(self.config, issue_timeout_seconds=60), self.commands, clock=lambda: now[0])
        original = self.commands.run
        async def expires(args, **kwargs):
            if tuple(args) == ('make', 'verify'):
                self.assertEqual(kwargs['timeout'], 60)
                now[0] = 1060
                raise asyncio.TimeoutError()
            return await original(args, **kwargs)
        self.commands.run = expires
        with self.assertRaisesRegex(BudgetError, 'deadline'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.runner.store.read('issue-95')['repairs'], 0)

    async def test_revalidate_cannot_mutate_after_total_deadline(self):
        self.commands.fail_ci = True
        with self.assertRaises(GateError):
            await self.runner.run_issue(95, execute=True)
        record = self.runner.store.read('issue-95')
        record['deadline'] = 0
        self.runner.save(record)
        before = len(self.commands.calls)
        with self.assertRaisesRegex(BudgetError, 'deadline'):
            await self.runner.revalidate(95)
        self.assertFalse(any(c[:2] == ('git', 'merge') for c in self.commands.calls[before:]))

    async def test_database_access_denied_is_an_environment_failure(self):
        original = self.commands.run
        async def denied(args, **kwargs):
            if tuple(args) == ('make', 'verify'):
                raise CommandError('Error 1044 (42000): Access denied for user to database test_migrations')
            return await original(args, **kwargs)
        self.commands.run = denied
        with self.assertRaisesRegex(GateError, 'environment'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual(self.runner.store.read('issue-95')['repairs'], 0)


for _name in list(fixtures.EngineTests.__dict__):
    if _name.startswith('test_'):
        setattr(RecoveryLimitTests, _name, None)


class SignalTests(unittest.TestCase):
    def test_cli_sigterm_cancels_real_command_process_group(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            pidfile = root / 'child.pid'
            code = '''import asyncio, sys
from pathlib import Path
from gophercrm_runner.cli import run_async
from gophercrm_runner.process import Commands
root = Path(sys.argv[1])
run_async(Commands(root / 'logs').run([sys.executable, '-c',
    "import os, time; from pathlib import Path; Path(" + repr(str(root / 'child.pid')) + ").write_text(str(os.getpid())); time.sleep(60)"], cwd=root))
'''
            proc = subprocess.Popen([sys.executable, '-c', code, directory], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            try:
                deadline = time.monotonic() + 10
                while not pidfile.exists() and proc.poll() is None and time.monotonic() < deadline:
                    time.sleep(0.02)
                self.assertTrue(pidfile.exists(), 'CLI exited before launching the child')
                child = int(pidfile.read_text())
                proc.terminate()
                proc.communicate(timeout=10)
                import os
                with self.assertRaises(ProcessLookupError):
                    os.kill(child, 0)
            finally:
                if proc.poll() is None:
                    proc.kill()
                proc.communicate(timeout=10)
