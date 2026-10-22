#!/usr/bin/env python3
"""Require non-skipped database coverage in PR and release Go JSON logs."""
import argparse
import json
from pathlib import Path
import re


def check(output, root, *, migrations_only=False):
    required = {
        ('github.com/florinel-chis/gophercrm/cmd/migrate', 'TestMigrationFresh_FullUpAndDown'),
        ('github.com/florinel-chis/gophercrm/cmd/migrate', 'TestMigrationUpgrade_FromMainAutoMigratedSchema'),
        ('github.com/florinel-chis/gophercrm/cmd/migrate', 'TestCustomerWebsiteMigration'),
    }
    if migrations_only:
        required.update(('github.com/florinel-chis/gophercrm/internal/models', name) for name in (
            'TestMigrateDatabase_SQLiteUpgradeFromV120',
            'TestMigrateDatabase_SQLiteUpgradeFromHalfAppliedV120',
            'TestMigrateDatabase_SQLiteEnforcesForeignKeysAfterUpgrade',
            'TestCustomerWebsiteUpgradeFromSQLiteFixture',
        ))
    else:
        for path in (root / 'internal/integration').glob('*_test.go'):
            required.update(('github.com/florinel-chis/gophercrm/internal/integration', name)
                            for name in re.findall(r'^func (Test\w+MySQL)\(t \*testing.T\)', path.read_text(), re.M))
        if len(required) <= 3:
            raise ValueError('No real SQL integration roots found')
    events = [json.loads(line) for line in output.splitlines() if line.startswith('{')]
    passes = [(event.get('Package'), event.get('Test')) for event in events if event.get('Action') == 'pass']
    passed = set(passes)
    bad = [event for event in events if event.get('Action') in {'skip', 'fail'}]
    if bad or not required.issubset(passed):
        raise ValueError(f'Required SQL coverage failed or skipped: missing {sorted(required - passed)}')
    if any(passes.count(identity) != 1 for identity in required):
        raise ValueError('Duplicate required SQL test result')
    if any((package, None) not in passed for package, _ in required):
        raise ValueError('Required SQL package did not finish successfully')
    return sorted(required)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('log', type=Path)
    parser.add_argument('--migrations-only', action='store_true',
                        help='Require SQL migrations and SQLite upgrades instead of release integration roots')
    args = parser.parse_args()
    for package, name in check(args.log.read_text(), Path.cwd(), migrations_only=args.migrations_only):
        print(f'PASS {package}: {name}')
