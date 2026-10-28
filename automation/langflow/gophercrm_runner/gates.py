"""Pure, fail-closed predicates used immediately before publication and merge."""

import json
import re
from pathlib import PurePosixPath

from .config import GateError


REVIEW_SCHEMA = {
    'type': 'object', 'additionalProperties': False,
    'required': ['approved', 'blocking_findings', 'summary'],
    'properties': {
        'approved': {'type': 'boolean'},
        'blocking_findings': {'type': 'array', 'items': {'type': 'string'}},
        'summary': {'type': 'string'},
    },
}


def agent_result(output: str, *, review: bool = False) -> dict:
    try:
        result = json.loads(output)
    except (ValueError, TypeError) as exc:
        raise GateError('Claude did not return a JSON result') from exc
    if not isinstance(result, dict) or result.get('is_error') is not False:
        raise GateError('Claude failed or omitted its success status')
    if result.get('permission_denials'):
        raise GateError('Claude could not complete its work with the configured tools')
    if not review:
        return result
    verdict = result.get('structured_output')
    if (not isinstance(verdict, dict) or set(verdict) != set(REVIEW_SCHEMA['required'])
            or type(verdict['approved']) is not bool
            or not isinstance(verdict['summary'], str)
            or not isinstance(verdict['blocking_findings'], list)
            or any(not isinstance(f, str) or not f.strip() for f in verdict['blocking_findings'])):
        raise GateError('Claude review has no valid structured verdict')
    if verdict['approved'] and verdict['blocking_findings']:
        raise GateError('Contradictory review verdict')
    return verdict


def check_state(checks: list[dict], required: tuple[str, ...]) -> str:
    """Return pending, failed, or success; absent/duplicate checks never pass."""
    names = [c.get('name', c.get('context')) for c in checks]
    if len(names) != len(set(names)):
        return 'pending'
    states = {}
    for check, name in zip(checks, names):
        if check.get('__typename') == 'StatusContext':
            states[name] = check.get('state', 'PENDING')
        else:
            states[name] = (check.get('conclusion') if check.get('status') == 'COMPLETED'
                            else 'PENDING')
    if any(s in {'FAILURE', 'ERROR', 'CANCELLED', 'TIMED_OUT', 'ACTION_REQUIRED',
                 'STARTUP_FAILURE', 'STALE'} for s in states.values()):
        return 'failed'
    if not set(required).issubset(states) or any(states[n] != 'SUCCESS' for n in required):
        return 'pending'
    if any(s not in {'SUCCESS', 'NEUTRAL', 'SKIPPED'} for s in states.values()):
        return 'pending'
    return 'success'


def merge_gate(record: dict, pr: dict, required: tuple[str, ...], *, require_approval=True) -> None:
    head = record['head']
    if not head or any(record.get(key) != head for key in ('tested_head', 'reviewed_head')):
        raise GateError('Current commit has not passed both tests and review')
    if pr.get('headRefOid') != head or pr.get('baseRefOid') != record['base']:
        raise GateError('PR head or base changed after validation; revalidate before merging')
    if (pr.get('state') != 'OPEN' or pr.get('isDraft') is not False
            or pr.get('mergeable') != 'MERGEABLE'
            or pr.get('reviewDecision') == 'CHANGES_REQUESTED'
            or (require_approval and pr.get('reviewDecision') == 'REVIEW_REQUIRED')):
        raise GateError('GitHub PR is not ready to merge')
    if check_state(pr.get('statusCheckRollup', []), required) != 'success':
        raise GateError('Every required CI check must succeed')


# Frontend validation inputs: the Vitest and Playwright configuration, the Vitest
# setup, and the whole browser suite with its helpers, fixtures and page objects.
# Changing any of them could narrow or weaken the evidence that `make verify` and
# the browser matrix produce, so they are existing validation gates for every
# profile. A newly added top-level browser spec only adds coverage and is admitted;
# the health smoke, which every bounded profile runs, is never admitted as an addition.
FRONTEND_GATE_FILES = frozenset({'gocrm-ui/src/test/setup.ts', 'gocrm-ui/src/test/globals.d.ts',
                                 'gocrm-ui/e2e/tests/health-smoke.spec.ts'})
FRONTEND_GATE_PREFIXES = ('gocrm-ui/vitest.config', 'gocrm-ui/playwright.config', 'gocrm-ui/e2e/')
NEW_BROWSER_SPEC = re.compile(r'gocrm-ui/e2e/tests/[a-z0-9-]+\.spec\.ts')


def frontend_gate(name: str, added: set[str]) -> bool:
    if name in FRONTEND_GATE_FILES:
        return True
    if not name.startswith(FRONTEND_GATE_PREFIXES):
        return False
    return not (name in added and NEW_BROWSER_SPEC.fullmatch(name))


def validate_paths(paths: list[str], *, new_workflows: set[str] | None = None,
                   added: set[str] | None = None) -> None:
    """Refuse protected, private and validation-gate paths.

    `new_workflows` lists workflow files the local policy allows an issue to add;
    `added` lists paths absent from the base commit (a new browser spec is admitted
    only when it appears there)."""
    new_workflows = new_workflows or set()
    added = added or set()
    for name in paths:
        path = PurePosixPath(name)
        if (path.is_absolute() or '..' in path.parts or '.git' in path.parts
                or name.startswith('automation/langflow/')
                or (path.name.startswith('.env') and path.name != '.env.example')
                or path.suffix.lower() in {'.pem', '.key', '.sqlite', '.sqlite3', '.db'}):
            raise GateError(f'Agent changed a protected or private path: {name}')
        if (path.name == '.gitattributes'
                or name in {'Makefile', '.gitignore', '.github/release.yml', 'gocrm-ui/package.json', 'gocrm-ui/package-lock.json'}
                or name.startswith(('scripts/ci/', 'scripts/release/', 'scripts/deploy/', 'scripts/e2e/', 'gocrm-ui/tsconfig',
                                    'gocrm-ui/vite.config', 'gocrm-ui/eslint.config'))
                or (name.startswith('.github/workflows/') and name not in new_workflows)
                or frontend_gate(name, added)):
            raise GateError(f'Agent changed an existing validation gate: {name}')


def touches_database(paths: list[str]) -> bool:
    return any(p.startswith(('migrations/', 'cmd/migrate/', 'internal/database/', 'internal/models/',
                             'internal/repository/', 'internal/service/', 'internal/utils/transaction'))
               for p in paths)


def migration_coverage(output: str) -> None:
    required = {'TestMigrationFresh_FullUpAndDown', 'TestMigrationUpgrade_FromMainAutoMigratedSchema'}
    passed = set()
    for line in output.splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if not isinstance(event, dict):
            continue
        if event.get('Action') == 'skip':
            raise GateError('Migration coverage was skipped')
        if event.get('Action') == 'pass':
            passed.add(event.get('Test'))
    if not required.issubset(passed):
        raise GateError('Fresh and upgrade migration tests did not both report success')
