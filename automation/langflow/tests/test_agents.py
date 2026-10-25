import json
from types import SimpleNamespace
import unittest

from gophercrm_runner.config import GateError


class AgentTests(unittest.TestCase):
    def test_implementation_schema_rejects_descriptions_unknown_and_duplicate_ids(self):
        from jsonschema import Draft202012Validator
        from gophercrm_runner.agents import arguments, parse
        config = SimpleNamespace(agent_model='', documentation_mode='reviewed')
        criteria = [{'id': 'AC-1'}, {'id': 'AC-2'}]
        argv = arguments(config, criteria=criteria)
        schema = json.loads(argv[argv.index('--json-schema') + 1])
        validator = Draft202012Validator(schema)
        for addressed in [[], ['AC-1'], ['AC-2', 'AC-1']]:
            report = dict(summary='Implementation claim, not test evidence',
                          addressed_criteria=addressed, remaining_gaps=[])
            validator.validate(report)
            parse(json.dumps(dict(is_error=False, structured_output=report)),
                  review=False, criteria=criteria)
        for addressed in [['AC-1: implemented the claim'], ['Documentation: updated'],
                          ['AC-99'], ['AC-1', 'AC-1']]:
            with self.subTest(addressed=addressed):
                report = dict(summary='done', addressed_criteria=addressed, remaining_gaps=[])
                self.assertFalse(validator.is_valid(report))
                with self.assertRaises(GateError):
                    parse(json.dumps(dict(is_error=False, structured_output=report)),
                          review=False, criteria=criteria)
        # Existing PRs without frozen criteria can only claim an empty list.
        argv = arguments(config, criteria=[])
        empty = Draft202012Validator(json.loads(argv[argv.index('--json-schema') + 1]))
        self.assertTrue(empty.is_valid(dict(summary='done', addressed_criteria=[], remaining_gaps=[])))
        self.assertFalse(empty.is_valid(dict(summary='done', addressed_criteria=['AC-1'], remaining_gaps=[])))
        # One invocation must not mutate the shared schema for later tasks.
        argv = arguments(config, criteria=[{'id': 'AC-3'}])
        next_task = Draft202012Validator(json.loads(argv[argv.index('--json-schema') + 1]))
        self.assertTrue(next_task.is_valid(dict(summary='done', addressed_criteria=['AC-3'], remaining_gaps=[])))
        self.assertFalse(next_task.is_valid(dict(summary='done', addressed_criteria=['AC-1'], remaining_gaps=[])))

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


class ReaderAgentTests(unittest.TestCase):
    def test_reader_arguments_are_restricted_read_only_with_web(self):
        from gophercrm_runner.agents import READER_TOOLS, reader_arguments
        schema = {'type': 'object', 'additionalProperties': False, 'required': ['summary'],
                  'properties': {'summary': {'type': 'string'}}}
        self.assertEqual(READER_TOOLS, 'Read,Glob,Grep,WebFetch,WebSearch')
        argv = reader_arguments(SimpleNamespace(agent_model=''), schema=schema)
        self.assertEqual(argv[0], 'claude')
        for flag in ('--safe-mode', '--restricted', '-p', '--no-session-persistence'):
            self.assertIn(flag, argv)
        self.assertEqual(argv[argv.index('--permission-mode') + 1], 'dontAsk')
        self.assertEqual(argv[argv.index('--output-format') + 1], 'json')
        self.assertEqual(argv[argv.index('--tools') + 1], READER_TOOLS)
        self.assertEqual(argv[argv.index('--allowedTools') + 1], READER_TOOLS)
        self.assertEqual(json.loads(argv[argv.index('--json-schema') + 1]), schema)
        joined = ','.join(argv)
        for forbidden in ('Edit', 'Write', 'Bash'):
            self.assertNotIn(forbidden, joined)
        self.assertNotIn('--model', argv)
        with_model = reader_arguments(SimpleNamespace(agent_model='claude-sonnet-5-5'), schema=schema)
        self.assertEqual(with_model[with_model.index('--model') + 1], 'claude-sonnet-5-5')

    def test_metadata_matches_parse(self):
        from gophercrm_runner.agents import metadata, parse
        raw = dict(is_error=False, structured_output=dict(summary='done', addressed_criteria=['AC-1'], remaining_gaps=[]),
                   modelUsage={'observed-model': {'inputTokens': 1}, 'helper': {}},
                   usage={'input_tokens': 10, 'output_tokens': 2, 'bogus': 'x', 'cache_read_input_tokens': -1},
                   total_cost_usd=0.5)
        _, parsed = parse(json.dumps(raw), review=False, criteria=[{'id': 'AC-1'}])
        self.assertEqual(metadata(raw), parsed)
        self.assertEqual(metadata(raw)['usage'], {'input_tokens': 10, 'output_tokens': 2})
        self.assertEqual(metadata({})['cost'], {'status': 'unavailable', 'usd': None})
