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
UNFORMATTED = 'Go files that are not gofmt-formatted (run gofmt -w on them):'


def gofmt_binary(root=ROOT):
    """gofmt from PATH, or the one that ships with the `go` on PATH."""
    import shutil
    found = shutil.which('gofmt')
    if found:
        return found
    goroot = subprocess.check_output(['go', 'env', 'GOROOT'], cwd=root, text=True).strip()
    return str(Path(goroot) / 'bin' / 'gofmt')


def unformatted(root=ROOT):
    """The tracked Go files gofmt would rewrite, as repository-relative paths.

    Tracked files only: untracked sources (a local snapshot under docs/, a
    scratch file) are never read. A tracked path that is gone from the working
    tree is skipped. A file gofmt cannot parse raises GateError with its report.
    """
    root = Path(root)
    listed = subprocess.check_output(['git', 'ls-files', '-z', '--', '*.go'], cwd=root, text=True)
    files = [path for path in listed.split('\0') if path and (root / path).is_file()]
    gofmt, found = gofmt_binary(root), []
    # Bounded argument lists: the command line has a size limit.
    for start in range(0, len(files), 200):
        result = subprocess.run([gofmt, '-l', '--', *files[start:start + 200]], cwd=root,
                                capture_output=True, text=True)
        if result.returncode != 0:
            raise GateError('gofmt failed:\n' + (result.stderr.strip() or f'exit status {result.returncode}'))
        found.extend(line for line in result.stdout.splitlines() if line)
    return sorted(found)


def check_format(root=ROOT):
    """Fail, naming them one per line, when a tracked Go file is not gofmt-formatted."""
    found = unformatted(root)
    if found:
        raise GateError('\n'.join([UNFORMATTED, *found]))


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
