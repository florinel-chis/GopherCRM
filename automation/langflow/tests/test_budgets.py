import asyncio
from dataclasses import replace
from unittest.mock import AsyncMock, patch

import test_runner as fixtures
from gophercrm_runner.config import GateError
from gophercrm_runner.engine import Runner


class BudgetTests(fixtures.EngineTests):
    async def test_call_limit_survives_restart_and_extension_reuses_validated_candidate(self):
        config = replace(self.config, max_provider_calls=1)
        self.runner = Runner(config, self.commands)
        with self.assertRaisesRegex(GateError, 'call budget'):
            await self.runner.run_issue(95, execute=True)
        record = self.runner.store.read('issue-95')
        self.assertEqual(record['provider_calls'], 1)
        self.assertEqual(record['stage'], 'review')
        self.runner = Runner(config, self.commands)
        with self.assertRaisesRegex(GateError, 'call budget'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual((self.commands.implementations, self.commands.tests, self.commands.reviews), (1, 1, 0))
        await self.runner.extend_budget(95, calls=1, seconds=0, reason='Explicit test operator extension')
        self.assertEqual((await self.runner.run_issue(95, execute=True))['status'], 'merged')
        final = self.runner.store.read('issue-95')
        self.assertEqual(final['provider_calls'], 2)
        self.assertEqual(final['repairs'], 0)
        self.assertEqual(len(final['budget_extensions']), 1)
        self.assertEqual((self.commands.implementations, self.commands.tests, self.commands.reviews), (1, 1, 1))

    async def test_cancellation_keeps_the_call_reservation(self):
        config = replace(self.config, max_provider_calls=1)
        self.runner = Runner(config, self.commands)
        original = self.commands.run
        async def cancel(args, **kwargs):
            if args[0] == 'claude' and '-p' in args:
                raise asyncio.CancelledError()
            return await original(args, **kwargs)
        self.commands.run = cancel
        with self.assertRaises(asyncio.CancelledError):
            await self.runner.run_issue(95, execute=True)
        record = self.runner.store.read('issue-95')
        self.assertEqual(record['provider_calls'], 1)
        self.assertEqual(record['provider_invocations'][0]['status'], 'cancelled')
        self.commands.run = original
        with self.assertRaisesRegex(GateError, 'call budget'):
            await Runner(config, self.commands).run_issue(95, execute=True)
        self.assertEqual(self.commands.implementations, 0)

    async def test_deadline_includes_downtime_and_stops_mutation_on_resume(self):
        now = [1000.0]
        config = replace(self.config, issue_timeout_seconds=60)
        self.runner = Runner(config, self.commands, clock=lambda: now[0])
        self.commands.fail_ci = True
        with self.assertRaisesRegex(GateError, 'CI failed'):
            await self.runner.run_issue(95, execute=True)
        record = self.runner.store.read('issue-95')
        self.assertEqual(record['deadline'], 1060)
        before = (self.commands.implementations, self.commands.reviews, self.commands.creates)
        now[0] = 1061
        self.commands.fail_ci = False
        with self.assertRaisesRegex(GateError, 'deadline'):
            await Runner(config, self.commands, clock=lambda: now[0]).run_issue(95, execute=True)
        self.assertEqual((self.commands.implementations, self.commands.reviews, self.commands.creates), before)
        self.assertEqual(self.commands.merges, 0)
        self.assertEqual(self.runner.store.read('issue-95')['deadline'], 1060)

    async def test_unresolved_crash_call_blocks_even_when_ci_would_allow_merge(self):
        self.commands.fail_ci = True
        with self.assertRaises(GateError):
            await self.runner.run_issue(95, execute=True)
        record = self.runner.store.read('issue-95')
        record['provider_invocations'][-1]['status'] = 'running'
        self.runner.save(record)
        self.commands.fail_ci = False
        with self.assertRaisesRegex(GateError, 'Unresolved provider'):
            await Runner(self.config, self.commands).run_issue(95, execute=True)
        self.assertEqual(self.commands.merges, 0)
        self.assertEqual(self.runner.store.read('issue-95')['provider_calls'], 2)

    async def test_confirmed_merge_recovers_after_deadline_without_provider_readiness(self):
        await self.runner.run_issue(95, execute=True)
        record = self.runner.store.read('issue-95')
        record['deadline'] = 0
        self.runner.save(record)
        with patch.object(self.runner, 'doctor', AsyncMock(side_effect=AssertionError('must reconcile first'))):
            self.assertEqual((await self.runner.run_issue(95, execute=True))['status'], 'merged')
        self.assertEqual(self.commands.merges, 1)


for _name in list(fixtures.EngineTests.__dict__):
    if _name.startswith('test_'):
        setattr(BudgetTests, _name, None)
