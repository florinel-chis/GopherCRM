"""The existing Claude CLI adapter, with explicit permissions and observed metadata."""

import json
import math
from copy import deepcopy

from .config import GateError
from . import documentation
from .evidence import review_coverage
from .gates import agent_result


CLAIM_SCHEMA = {
    'type': 'object', 'additionalProperties': False,
    'required': ['summary', 'addressed_criteria', 'remaining_gaps'],
    'properties': {'summary': {'type': 'string'},
                   'addressed_criteria': {
                       'type': 'array', 'uniqueItems': True,
                       'description': 'Exact acceptance IDs only, such as AC-1. Put explanations in summary.',
                       'items': {'type': 'string', 'pattern': '^AC-[1-9][0-9]*$'}},
                   'remaining_gaps': {'type': 'array', 'items': {'type': 'string'}}},
}
ACCEPTANCE_REVIEW_SCHEMA = {
    'type': 'object', 'additionalProperties': False,
    'required': ['approved', 'blocking_findings', 'summary', 'criteria'],
    'properties': {
        'approved': {'type': 'boolean'}, 'summary': {'type': 'string'},
        'blocking_findings': {'type': 'array', 'items': {'type': 'string'}},
        'criteria': {'type': 'array', 'items': {
            'type': 'object', 'additionalProperties': False,
            'required': ['id', 'passed', 'evidence', 'finding'],
            'properties': {'id': {'type': 'string'}, 'passed': {'type': 'boolean'},
                           'evidence': {'type': 'array', 'items': {'type': 'string'}},
                           'finding': {'type': 'string'}},
        }},
    },
}


def arguments(config, *, review=False, criteria=None):
    tools = 'Read,Glob,Grep' if review else 'Read,Glob,Grep,Edit,Write'
    schema = deepcopy(ACCEPTANCE_REVIEW_SCHEMA if review else CLAIM_SCHEMA)
    if not review and criteria is not None:
        claims = schema['properties']['addressed_criteria']
        ids = [criterion['id'] for criterion in criteria]
        if ids:
            claims['items']['enum'] = ids
        else:
            claims['maxItems'] = 0
    if review and config.documentation_mode == 'reviewed':
        schema['required'].append('documentation')
        schema['properties']['documentation'] = documentation.REVIEW_SCHEMA
    args = ['claude', '--safe-mode', '-p', '--permission-mode', 'dontAsk',
            '--tools', tools, '--allowedTools', tools, '--no-session-persistence',
            '--output-format', 'json', '--json-schema',
            json.dumps(schema)]
    if config.agent_model:
        args += ['--model', config.agent_model]
    return args


def parse(output, *, review, criteria, documentation_plan=None):
    raw = agent_result(output)
    report = raw.get('structured_output')
    if review:
        review_coverage(report, criteria, documentation_plan=documentation_plan)
    else:
        if (not isinstance(report, dict) or set(report) != set(CLAIM_SCHEMA['required'])
                or not isinstance(report['summary'], str) or not isinstance(report['addressed_criteria'], list)
                or not isinstance(report['remaining_gaps'], list)
                or any(not isinstance(v, str) for v in report['addressed_criteria'] + report['remaining_gaps'])
                or len(set(report['addressed_criteria'])) != len(report['addressed_criteria'])
                or not set(report['addressed_criteria']).issubset(c['id'] for c in criteria)):
            raise GateError('Invalid structured implementation claims')
    usage = raw.get('usage')
    usage = {k: v for k, v in usage.items() if k in {
        'input_tokens', 'output_tokens', 'cache_creation_input_tokens', 'cache_read_input_tokens'}
        and type(v) is int and v >= 0} if isinstance(usage, dict) else None
    observed = raw.get('modelUsage')
    cost = raw.get('total_cost_usd')
    metadata = {'observed_models': sorted(observed) if isinstance(observed, dict) and observed else None,
                'usage': usage or None, 'cost': {'status': 'unavailable', 'usd': None}}
    if type(cost) in {int, float} and math.isfinite(cost) and cost >= 0:
        metadata['cost'] = {'status': 'provider-reported', 'usd': cost}
    return report, metadata
