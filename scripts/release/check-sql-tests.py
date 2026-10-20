#!/usr/bin/env python3
"""Fail a release when required real-database test roots did not execute."""
import json
from pathlib import Path
import re
import sys


def check(output, root):
    required = {
        ('github.com/florinel-chis/gophercrm/cmd/migrate', 'TestMigrationFresh_FullUpAndDown'),
        ('github.com/florinel-chis/gophercrm/cmd/migrate', 'TestMigrationUpgrade_FromMainAutoMigratedSchema'),
    }
    for path in (root / 'internal/integration').glob('*_test.go'):
        required.update(('github.com/florinel-chis/gophercrm/internal/integration', name)
                        for name in re.findall(r'^func (Test\w+MySQL)\(t \*testing.T\)', path.read_text(), re.M))
    if len(required) <= 2:
        raise ValueError('No real SQL integration roots found')
    events = [json.loads(line) for line in output.splitlines() if line.startswith('{')]
    passed = {(event.get('Package'), event.get('Test')) for event in events if event.get('Action') == 'pass'}
    bad = [event for event in events if event.get('Action') in {'skip', 'fail'}]
    if bad or not required.issubset(passed):
        raise ValueError(f'Required SQL coverage failed or skipped: missing {sorted(required - passed)}')


if __name__ == '__main__':
    check(Path(sys.argv[1]).read_text(), Path.cwd())
