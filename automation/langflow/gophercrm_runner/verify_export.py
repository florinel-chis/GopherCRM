"""Compare the generated workflow without modifying the checked-in artifact."""

import json
from pathlib import Path

from .flows import build_flow


def main():
    root = Path(__file__).resolve().parents[1]
    expected = json.dumps(build_flow(), indent=2) + '\n'
    if (root / 'flows/remediation.json').read_text() != expected:
        raise SystemExit('Workflow export differs; regenerate with gophercrm-runner export automation/langflow/flows/remediation.json')
    print('Workflow export matches its components')


if __name__ == '__main__':
    main()
