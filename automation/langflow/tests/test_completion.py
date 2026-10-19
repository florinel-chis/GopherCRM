import asyncio
from dataclasses import asdict, replace
import json
from unittest.mock import AsyncMock, patch

import test_runner as fixtures
from gophercrm_runner.config import GateError
from gophercrm_runner.engine import Runner


class CompletionTests(fixtures.EngineTests):
    # Reuse the real Git fixture, without collecting the baseline tests twice.
    async def test_pr_target_waits_for_ci_and_never_merges_on_repeat_or_policy_edit(self):
        self.runner = Runner(replace(self.config, completion_target='pr'), self.commands)
        self.commands.fail_ci = True
        with self.assertRaisesRegex(GateError, 'CI failed'):
            await self.runner.run_issue(95, execute=True)
        self.assertNotEqual(self.runner.store.read('issue-95')['stage'], 'pr_ready')
        self.commands.fail_ci = False
        result = await self.runner.run_issue(95, execute=True)
        self.assertEqual(result['status'], 'pr_ready')
        self.assertEqual(self.runner.store.read('active'), {})
        self.runner = Runner(self.config, self.commands)  # legacy merge default
        repeated = await self.runner.run_issue(95, execute=True)
        self.assertEqual(repeated['status'], 'pr_ready')
        self.assertEqual((self.commands.creates, self.commands.merges, self.commands.implementations), (1, 0, 1))
        self.assertEqual([i['issue'] for i in await self.runner.queue(2)], [96])

    async def test_stop_after_current_blocks_a_preloaded_next_ticket(self):
        original = self.commands.run

        async def stop_during_implementation(args, **kwargs):
            if args[0] == 'claude' and '-p' in args:
                self.runner.store.stop_after_current()
            return await original(args, **kwargs)

        self.commands.run = stop_during_implementation
        self.assertEqual((await self.runner.run_issue(95, execute=True))['status'], 'merged')
        before = len(self.commands.calls)
        self.assertEqual((await self.runner.run_issue(96, execute=True))['status'], 'stopped')
        self.assertEqual(len(self.commands.calls), before)
        self.assertFalse(self.runner.store.read('issue-96'))
        self.assertEqual(await self.runner.queue(2), [])
        self.runner.store.resume_queue()
        self.assertEqual([i['issue'] for i in await self.runner.queue(2)], [96])

    async def test_stop_without_active_issue_allows_no_admission(self):
        self.runner.store.stop_after_current()
        result = await self.runner.run_issue(95, execute=True)
        self.assertEqual(result['status'], 'stopped')
        self.assertEqual(self.commands.calls, [])
        self.assertFalse((self.config.state_dir / 'worktrees').exists())

    async def test_lost_publication_response_is_reconciled_without_republishing(self):
        original = self.commands.run

        async def lose_create_response(args, **kwargs):
            result = await original(args, **kwargs)
            if tuple(args[:3]) == ('gh', 'pr', 'create'):
                raise asyncio.CancelledError()
            return result

        self.commands.run = lose_create_response
        with self.assertRaises(asyncio.CancelledError):
            await self.runner.run_issue(95, execute=True)
        self.commands.run = original
        self.assertEqual((await self.runner.run_issue(95, execute=True))['status'], 'merged')
        self.assertEqual((self.commands.creates, self.commands.implementations), (1, 1))
        self.assertEqual(sum(c[:2] == ('git', 'push') for c in self.commands.calls), 1)

    async def test_completed_merge_recovery_precedes_issue_policy_and_other_active_checks(self):
        await self.runner.run_issue(95, execute=True)
        self.runner.store.write('active', {'issue': 96})
        changed = Runner(replace(self.config, completion_target='pr'), self.commands)
        with patch.object(changed, 'issue', AsyncMock(side_effect=GateError('Changed issue/unavailable'))):
            result = await changed.run_issue(95, execute=True)
        self.assertEqual(result['status'], 'merged')
        self.assertEqual(changed.store.read('active'), {'issue': 96})
        self.assertEqual(self.commands.merges, 1)

    async def test_rejected_review_cannot_be_rerolled_after_budget_exhaustion(self):
        self.commands.reject_reviews = 100
        with self.assertRaises(GateError):
            await self.runner.run_issue(95, execute=True)
        counts = (self.commands.implementations, self.commands.tests, self.commands.reviews)
        self.commands.reject_reviews = 0
        with self.assertRaisesRegex(GateError, 'budget exhausted'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual((self.commands.implementations, self.commands.tests, self.commands.reviews), counts)
        self.assertEqual(self.commands.creates, 0)

    async def test_unfinished_legacy_record_is_backed_up_and_never_adopted(self):
        self.commands.fail_ci = True
        with self.assertRaises(GateError):
            await self.runner.run_issue(95, execute=True)
        record = self.runner.store.read('issue-95')
        record.pop('schema_version')
        self.runner.store.write('issue-95', record)
        counts = (self.commands.implementations, self.commands.creates, self.commands.merges)
        with self.assertRaisesRegex(GateError, 'matching runner'):
            await self.runner.run_issue(95, execute=True)
        self.assertEqual((self.commands.implementations, self.commands.creates, self.commands.merges), counts)
        self.assertTrue(self.runner.store.read('issue-95')['legacy'])

    async def test_terminal_pr_branch_change_blocks_recovery(self):
        self.runner = Runner(replace(self.config, completion_target='pr'), self.commands)
        await self.runner.run_issue(95, execute=True)
        self.commands.pr_data['headRefName'] = 'unrelated'
        with self.assertRaisesRegex(GateError, 'branch'):
            await self.runner.run_issue(95, execute=True)

    async def test_real_serialized_graph_stops_after_one_pr_ready_ticket(self):
        from gophercrm_runner.flows import run_flow
        config = replace(self.config, completion_target='pr')
        path = self.root / 'policy.json'
        path.write_text(json.dumps(asdict(config), default=str))
        original = self.commands.run

        async def drain(args, **kwargs):
            if args[0] == 'claude' and '-p' in args:
                self.runner.store.stop_after_current()
            return await original(args, **kwargs)

        self.commands.run = drain
        with patch('gophercrm_runner.engine.Commands', return_value=self.commands):
            results = await run_flow(path, execute=True, limit=2)
        rendered = json.dumps([r.model_dump(mode='json') for r in results])
        self.assertIn('pr_ready', rendered)
        self.assertIn('stopped', rendered)
        self.assertEqual((self.commands.implementations, self.commands.merges), (1, 0))
        self.assertFalse(self.runner.store.read('issue-96'))


# unittest otherwise collects inherited baseline tests again.
for _name in list(fixtures.EngineTests.__dict__):
    if _name.startswith('test_'):
        setattr(CompletionTests, _name, None)
