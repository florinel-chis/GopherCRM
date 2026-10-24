"""Shared source-based Go selection and exact, uncached race-test evidence."""

import json
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'automation/langflow'))
from gophercrm_runner.config import GateError
from gophercrm_runner.evidence import named_go_tests
from gophercrm_runner.readiness import verification_environment

TEST = re.compile(r'^func\s+(Test[A-Za-z0-9_]+)\s*\(\s*\w+\s+\*testing\.T\s*\)', re.MULTILINE)


def discover(sources, *, root=ROOT, label='Go'):
    selected = {}
    for package, patterns in sources.items():
        files = set()
        for pattern in patterns:
            matches = list((root / package).glob(pattern))
            if not matches or any(p.is_symlink() or not p.is_file() for p in matches):
                raise GateError(f'Missing or unsupported {label} sources: {package}/{pattern}')
            files.update(matches)
        names = [name for path in sorted(files) for name in TEST.findall(path.read_text())]
        if not names or len(names) != len(set(names)):
            raise GateError(f'Empty or duplicate {label} selection: {package}')
        selected[package] = sorted(names)
    return selected


def verify(output, module, names, known_skips):
    events = [json.loads(line) for line in output.splitlines() if line.strip()]
    # Retain every event in the command output. Only this explicitly documented
    # pre-existing placeholder is exempt from the caller coverage assertion.
    checked = []
    for event in events:
        key = (event.get('Package'), event.get('Test'))
        if event.get('Action') == 'skip' and key in known_skips:
            print('Known coverage gap: ' + key[1] + ': ' + known_skips[key], flush=True)
        else:
            checked.append(json.dumps(event))
    if sum(e.get('Package') == module and not e.get('Test') and e.get('Action') == 'pass'
           for e in events) != 1:
        raise GateError('Missing or duplicate package completion: ' + module)
    named_go_tests('\n'.join(checked), [{'package': module, 'name': name} for name in names])


def run(selected, *, root=ROOT, label='Go', known_skips=None):
    # Application connection settings belong to browser/server processes. Go
    # fixtures use TEST_MYSQL_DSN and configuration tests assert unset defaults.
    # Keep the same isolation locally and in CI (including the MariaDB job).
    env = verification_environment()
    subprocess.run(['go', 'build', './cmd/...'], cwd=root, env=env, check=True)
    subprocess.run(['go', 'vet', *('./' + p for p in selected)], cwd=root, env=env, check=True)
    for package, names in selected.items():
        module = subprocess.check_output(['go', 'list', '-f', '{{.ImportPath}}', './' + package],
                                         cwd=root, env=env, text=True).strip()
        print(f'{label} checks: {package}: {len(names)} required roots', flush=True)
        command = ['go', 'test', '-race', '-json', '-count=1', '-timeout', '10m', './' + package,
                   '-run', '^(' + '|'.join(names) + ')$']
        result = subprocess.run(command, cwd=root, env=env, capture_output=True, text=True)
        print(result.stdout, end='', flush=True)
        print(result.stderr, end='', file=sys.stderr, flush=True)
        result.check_returncode()
        verify(result.stdout, module, names, known_skips or {})
