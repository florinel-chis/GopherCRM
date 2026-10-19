"""Execute serialized workflows through the real, pinned Langflow graph engine."""

import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import AsyncMock, patch

from lfx.graph import Graph
from gophercrm_runner.flows import build_flow


class FlowTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        root = Path(self.temp.name)
        checkout = root / 'checkout'
        (checkout / '.git').mkdir(parents=True)
        self.policy = root / 'policy.json'
        self.policy.write_text(json.dumps(dict(repository='example/repo', checkout=str(checkout),
            state_dir=str(root / 'state'), issues=[95, 96])))
        self.queue = [dict(issue=95, title='First', url='https://example.test/95'),
                      dict(issue=96, title='Second', url='https://example.test/96')]

    def tearDown(self):
        self.temp.cleanup()

    async def run_graph(self):
        serialized = json.dumps(build_flow(str(self.policy), limit=2))
        graph = Graph.from_payload(json.loads(serialized))
        return await graph.arun(inputs=[{}], outputs=['RemediationSummary-main'])

    async def test_export_roundtrip_dispatches_each_ticket_once_in_order(self):
        issue = AsyncMock(side_effect=lambda number, execute=False: dict(issue=number, status='planned'))
        with patch('gophercrm_runner.engine.Runner.queue', AsyncMock(return_value=self.queue)), \
             patch('gophercrm_runner.engine.Runner.run_issue', issue):
            result = await self.run_graph()
        self.assertEqual([call.args[0] for call in issue.await_args_list], [95, 96])
        self.assertTrue(all(call.kwargs == {'execute': False} for call in issue.await_args_list))
        self.assertIn('planned', json.dumps([item.model_dump(mode='json') for item in result]))

    async def test_loop_stops_after_failure(self):
        issue = AsyncMock(side_effect=RuntimeError('Concrete failing gate'))
        with patch('gophercrm_runner.engine.Runner.queue', AsyncMock(return_value=self.queue)), \
             patch('gophercrm_runner.engine.Runner.run_issue', issue):
            with self.assertRaisesRegex(Exception, 'Concrete failing gate'):
                await self.run_graph()
        self.assertEqual([call.args[0] for call in issue.await_args_list], [95])

    async def test_empty_queue_does_not_run_an_issue(self):
        issue = AsyncMock()
        with patch('gophercrm_runner.engine.Runner.queue', AsyncMock(return_value=[])), \
             patch('gophercrm_runner.engine.Runner.run_issue', issue):
            await self.run_graph()
        issue.assert_not_awaited()


if __name__ == '__main__':
    unittest.main()
