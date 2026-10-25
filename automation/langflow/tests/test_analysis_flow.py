"""The serialized analysis workflow executes through the pinned LFX engine and matches its export."""

import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import AsyncMock, patch

from lfx.graph import Graph

from gophercrm_runner.analysis import Analysis, RunRef, StageOutput
from gophercrm_runner.flows import build_analysis_flow

NODES = ['AnalysisIntake-main', 'AnalysisResearch-main', 'AnalysisAnalysis-main', 'AnalysisSpecification-main',
         'AnalysisReview-main', 'AnalysisRevision-main', 'AnalysisDocument-main']


class AnalysisFlowTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        root = Path(self.temp.name)
        checkout = root / 'checkout'
        (checkout / '.git').mkdir(parents=True)
        self.policy = root / 'policy.json'
        self.policy.write_text(json.dumps(dict(repository='example/repo', checkout=str(checkout),
                                               state_dir=str(root / 'state'), issues=[95])))
        self.run = RunRef('a' * 32, checkout, root / 'state', 'b' * 40, slug='follow-up-automation')

    def tearDown(self):
        self.temp.cleanup()

    def test_export_matches_components(self):
        expected = json.dumps(build_analysis_flow(), indent=2) + '\n'
        path = Path(__file__).resolve().parents[1] / 'flows' / 'analysis.json'
        self.assertEqual(path.read_text(), expected)

    def test_export_edges_follow_editor_encoding(self):
        payload = build_analysis_flow()
        nodes = {node['id']: node for node in payload['data']['nodes']}
        self.assertEqual(sorted(nodes), sorted(NODES))
        self.assertTrue(all(node['type'] == 'genericNode' for node in nodes.values()))
        self.assertEqual(len(payload['data']['edges']), 6)
        for edge in payload['data']['edges']:
            source = json.loads(edge['sourceHandle'].replace('œ', '"'))
            target = json.loads(edge['targetHandle'].replace('œ', '"'))
            self.assertEqual(source, edge['data']['sourceHandle'])
            self.assertEqual(target, edge['data']['targetHandle'])
            output = next(o for o in nodes[edge['source']]['data']['node']['outputs'] if o['name'] == source['name'])
            self.assertEqual(source['output_types'], [output['selected']])
            field = nodes[edge['target']]['data']['node']['template'][target['fieldName']]
            self.assertEqual(target['type'], field['type'])
        intake = nodes['AnalysisIntake-main']['data']['node']['template']
        self.assertEqual(intake['refine_from']['value'], 'research')
        self.assertEqual(intake['refine_from']['options'], ['intake', 'research', 'analysis', 'specification'])
        self.assertEqual(intake['wish']['value'], '')

    def stage(self, name):
        document = name == 'document'
        return AsyncMock(return_value=StageOutput(name, self.run, {'path': '/tmp/x.md'} if document else {name: 1},
                                                  'Document written to /tmp/x.md\n' if document else f'# {name}\n', ('note',)))

    async def test_roundtrip_passes_run_id_through_every_stage(self):
        mocks = {name: self.stage(name) for name in ('intake', 'research', 'analysis', 'specification', 'review',
                                                      'revision', 'document')}
        payload = json.loads(json.dumps(build_analysis_flow(str(self.policy), wish='Add follow-up automation',
                                                            links='https://x.test/a')))
        with patch.multiple(Analysis, **mocks):
            graph = Graph.from_payload(payload)
            result = await graph.arun(inputs=[{}], outputs=['AnalysisDocument-main'])
        mocks['intake'].assert_awaited_once_with(wish='Add follow-up automation', links='https://x.test/a', comments='',
                                                  previous_run_id='', refine_from='research')
        for name in ('research', 'analysis', 'specification', 'review', 'revision', 'document'):
            mocks[name].assert_awaited_once_with('a' * 32)
        text = json.dumps([item.model_dump(mode='json') for item in result])
        self.assertIn('/tmp/x.md', text)

    async def test_document_message_contains_path(self):
        from gophercrm_runner.analysis_document_component import AnalysisDocument
        from gophercrm_runner.analysis_support import as_data
        with patch.object(Analysis, 'document', self.stage('document')):
            component = AnalysisDocument(from_revision=as_data(self.stage('revision').return_value),
                                         policy_path=str(self.policy))
            message = await component.publish()
        self.assertIn('/tmp/x.md', message.text)


if __name__ == '__main__':
    unittest.main()
