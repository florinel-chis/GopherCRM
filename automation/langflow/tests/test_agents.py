import json
from types import SimpleNamespace
import unittest

from gophercrm_runner.config import GateError


class AgentTests(unittest.TestCase):
    def test_explicit_provider_tools_and_structured_claims_do_not_grant_acceptance(self):
        from gophercrm_runner.agents import arguments, parse
        config = SimpleNamespace(agent_model='', documentation_mode='legacy')
        for review, allowed in [(False, 'Read,Glob,Grep,Edit,Write'), (True, 'Read,Glob,Grep')]:
            argv = arguments(config, review=review)
            self.assertEqual(argv[argv.index('--tools') + 1], allowed)
            self.assertEqual(argv[argv.index('--allowedTools') + 1], allowed)
            self.assertIn('--safe-mode', argv)
            self.assertIn('--json-schema', argv)
            self.assertNotIn('Bash', ','.join(argv))
        output = dict(is_error=False, structured_output=dict(summary='Implemented', addressed_criteria=['AC-1'], remaining_gaps=[]),
                      modelUsage={'observed-model': {'inputTokens': 100}}, usage={'input_tokens': 100}, total_cost_usd=1.25)
        claims, metadata = parse(json.dumps(output), review=False, criteria=[{'id': 'AC-1'}])
        self.assertEqual(claims['addressed_criteria'], ['AC-1'])
        self.assertNotIn('approved', claims)
        self.assertEqual(metadata['observed_models'], ['observed-model'])
        self.assertEqual(metadata['cost'], {'status': 'provider-reported', 'usd': 1.25})
        output.pop('modelUsage'); output.pop('usage'); output.pop('total_cost_usd')
        _, missing = parse(json.dumps(output), review=False, criteria=[{'id': 'AC-1'}])
        self.assertEqual(missing['cost'], {'status': 'unavailable', 'usd': None})
        self.assertIsNone(missing['observed_models'])
        output['permission_denials'] = ['Write']
        with self.assertRaises(GateError):
            parse(json.dumps(output), review=False, criteria=[{'id': 'AC-1'}])

    def test_nonfinite_provider_cost_is_unknown(self):
        from gophercrm_runner.agents import parse
        for cost in [float('nan'), float('inf')]:
            raw = dict(is_error=False, structured_output=dict(summary='done', addressed_criteria=['AC-1'], remaining_gaps=[]),
                       total_cost_usd=cost)
            _, metadata = parse(json.dumps(raw), review=False, criteria=[{'id': 'AC-1'}])
            self.assertEqual(metadata['cost'], {'status': 'unavailable', 'usd': None})

    def test_documentation_mode_changes_review_schema_without_expanding_tools(self):
        from gophercrm_runner.agents import arguments
        for mode in ('legacy', 'reviewed'):
            config = SimpleNamespace(agent_model='', documentation_mode=mode)
            argv = arguments(config, review=True)
            schema = json.loads(argv[argv.index('--json-schema') + 1])
            self.assertEqual('documentation' in schema['required'], mode == 'reviewed')
            self.assertEqual(argv[argv.index('--tools') + 1], 'Read,Glob,Grep')
            implementation = arguments(config)
            schema = json.loads(implementation[implementation.index('--json-schema') + 1])
            self.assertNotIn('documentation', schema['required'])
