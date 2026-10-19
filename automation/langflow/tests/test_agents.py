import json
from types import SimpleNamespace
import unittest

from gophercrm_runner.config import GateError


class AgentTests(unittest.TestCase):
    def test_explicit_provider_tools_and_structured_claims_do_not_grant_acceptance(self):
        from gophercrm_runner.agents import arguments, parse
        config = SimpleNamespace(agent_model='')
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
