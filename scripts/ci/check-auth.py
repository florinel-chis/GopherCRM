#!/usr/bin/env python3
"""Build the API and run uncached auth/session regressions with race detection.

Select test roots by source file, so new auth tests do not need a naming regex
update. Missing files, empty selections and skipped roots/descendants fail closed.
"""

from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'automation/langflow'))
from gophercrm_runner.config import GateError
from gophercrm_runner.evidence import named_go_tests

SOURCES = {
    # logging_privacy_test.go asserts credentials, hashes and tokens never reach the
    # Login log output; it exercises auth_service.go directly.
    'internal/service': ('auth*_test.go', 'csrf_test.go', 'logging_privacy_test.go'),
    'internal/handler': ('auth*_test.go',),
    'internal/middleware': ('auth_test.go', 'registration_test.go'),
    'internal/repository': ('refresh_token_repository_test.go', 'password_reset_token_repository_test.go'),
    'internal/utils': ('password_test.go',),
    'tests': ('auth*_test.go', 'apikey_integration_test.go'),
    # erasure_test.go holds the ValidateAPIKey regressions for erased and
    # deactivated owners (FEATURES 1.6/8.4) on in-memory SQLite.
    'test/integration': ('erasure_test.go',),
}
TEST = re.compile(r'^func\s+(Test[A-Za-z0-9_]+)\s*\(\s*\w+\s+\*testing\.T\s*\)', re.MULTILINE)


def discover(root=ROOT):
    selected = {}
    for package, patterns in SOURCES.items():
        files = set()
        for pattern in patterns:
            matches = list((root / package).glob(pattern))
            if not matches or any(p.is_symlink() or not p.is_file() for p in matches):
                raise GateError('Missing or unsupported auth regression sources: ' + package + '/' + pattern)
            files.update(matches)
        names = [name for path in sorted(files) for name in TEST.findall(path.read_text())]
        if not names or len(names) != len(set(names)):
            raise GateError('Empty or duplicate auth test selection: ' + package)
        selected[package] = sorted(names)
    return selected


def run(root=ROOT):
    selected = discover(root)
    subprocess.run(['go', 'build', './cmd/...'], cwd=root, check=True)
    subprocess.run(['go', 'vet', *('./' + p for p in selected)], cwd=root, check=True)
    # Package-by-package execution keeps memory bounded on local machines.
    for package, names in selected.items():
        module = subprocess.check_output(['go', 'list', '-f', '{{.ImportPath}}', './' + package],
                                         cwd=root, text=True).strip()
        print(f'Auth checks: {package}: {len(names)} required roots', flush=True)
        command = ['go', 'test', '-race', '-json', '-count=1', './' + package,
                   '-run', '^(' + '|'.join(names) + ')$']
        result = subprocess.run(command, cwd=root, capture_output=True, text=True)
        print(result.stdout, end='', flush=True)
        print(result.stderr, end='', file=sys.stderr, flush=True)
        result.check_returncode()
        named_go_tests(result.stdout, [{'package': module, 'name': name} for name in names])


if __name__ == '__main__':
    run()
