"""Compare the generated workflow without modifying the checked-in artifact."""

import json
from pathlib import Path

from .flows import build_flow, build_pr_flow, build_batch_flow


def main():
    root = Path(__file__).resolve().parents[1]
    expected = json.dumps(build_flow(), indent=2) + '\n'
    if (root / 'flows/remediation.json').read_text() != expected:
        raise SystemExit('Workflow export differs; regenerate with gophercrm-runner export automation/langflow/flows/remediation.json')
    if (root / 'flows/pr-completion.json').read_text() != json.dumps(build_pr_flow(), indent=2) + '\n':
        raise SystemExit('PR workflow export differs; regenerate with gophercrm-runner export --pr automation/langflow/flows/pr-completion.json')
    if (root / 'flows/batch.json').read_text() != json.dumps(build_batch_flow(), indent=2) + '\n':
        raise SystemExit('Batch workflow export differs; regenerate with gophercrm-runner export --batch automation/langflow/flows/batch.json')
    print('Workflow export matches its components')


if __name__ == '__main__':
    main()
