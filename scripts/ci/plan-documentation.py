#!/usr/bin/env python3
"""Publish documentation impact; this report never substitutes for semantic review."""

import json
import os
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'automation/langflow'))
from gophercrm_runner import documentation
from gophercrm_runner.config import GateError


def main():
    directory = Path(os.environ['RUNNER_TEMP'])
    selection = json.loads((directory / 'validation-plan.json').read_text())
    try:
        if not selection.get('base') or selection.get('base') == '0' * 40:
            raise GateError('No comparison base is available')
        result = documentation.plan(Path.cwd(), selection['base'], selection['head'])
        result['status'] = 'assessment-required'
    except (GateError, OSError) as exc:
        result = dict(status='comparison-unavailable', base=selection.get('base'),
                      head=selection.get('head'), documents=[], reason=str(exc)[:1000])
    result['semantic_approval'] = False
    result['notice'] = ('Impact report only. Update affected docs in the PR or explain why they '
                        'remain accurate, and have the reviewer assess that decision.')
    raw = json.dumps(result, indent=2) + '\n'
    (directory / 'documentation-impact.json').write_text(raw)
    if os.environ.get('GITHUB_STEP_SUMMARY'):
        with Path(os.environ['GITHUB_STEP_SUMMARY']).open('a') as stream:
            stream.write('### Documentation impact\n\n' + result['notice'] + '\n\n```json\n' + raw + '```\n')
    print(raw, end='')


if __name__ == '__main__':
    main()
