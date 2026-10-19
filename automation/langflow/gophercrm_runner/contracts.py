"""Strict operator-authored task contracts. No executable commands come from issues."""

import fnmatch
import hashlib
import json
from pathlib import PurePosixPath
import re

from .config import GateError
from .evidence import safe_artifact


MANIFEST_FIELDS = {'schema_version', 'repository', 'issue', 'objective', 'non_goals',
                   'scope_paths', 'dependencies', 'validation_profile', 'context_refs', 'acceptance'}
CONTRACT_FIELDS = MANIFEST_FIELDS | {'source', 'source_hash', 'base', 'documents', 'context_hash',
                                    'checks_hash', 'contract_hash'}
MAX_CONTEXT_BYTES = 128 * 1024


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':'),
                                    ensure_ascii=False).encode()).hexdigest()


def source(issue):
    return {key: issue[key] for key in ('number', 'title', 'body')}


def safe_path(value):
    if not isinstance(value, str) or not value or '\\' in value or '\x00' in value:
        raise GateError('Invalid contract path')
    path = PurePosixPath(value)
    if path.is_absolute() or '..' in path.parts or '.git' in path.parts:
        raise GateError('Contract path escapes the repository')


def validate_manifest(config, raw, number):
    if not isinstance(raw, dict) or set(raw) != MANIFEST_FIELDS:
        raise GateError('Task manifest has missing or unknown fields')
    if (type(raw['schema_version']) is not int or raw['schema_version'] != 1
            or raw['repository'] != config.repository or type(raw['issue']) is not int or raw['issue'] != number):
        raise GateError('Task manifest version or identity does not match')
    if not isinstance(raw['objective'], str) or not raw['objective'].strip():
        raise GateError('Task objective is required')
    for key in ['non_goals', 'scope_paths', 'context_refs']:
        values = raw[key]
        if (not isinstance(values, list) or not values or any(not isinstance(v, str) or not v.strip() for v in values)
                or len(set(values)) != len(values)):
            raise GateError(f'{key} must be a nonempty unique string list')
    for value in raw['scope_paths'] + raw['context_refs']:
        safe_path(value)
    if 'AGENTS.md' not in raw['context_refs'] or any(not p.endswith('.md') for p in raw['context_refs']):
        raise GateError('Context must include AGENTS.md and explicit Markdown guidance paths')
    if raw['validation_profile'] not in {'backend', 'backend-database'}:
        raise GateError('Unsupported MVP profile; manual/UI evidence is not implemented')
    if raw['validation_profile'] == 'backend-database' and not config.database_test_commands:
        raise GateError('Database profile requires configured database validation')
    criteria = raw['acceptance']
    if not isinstance(criteria, list) or not criteria:
        raise GateError('Acceptance criteria are required')
    seen = set()
    for criterion in criteria:
        if (not isinstance(criterion, dict) or set(criterion) != {'id', 'requirement', 'verification'}
                or not isinstance(criterion['id'], str) or not re.fullmatch(r'AC-[1-9][0-9]*', criterion['id'])
                or criterion['id'] in seen or not isinstance(criterion['requirement'], str)
                or not criterion['requirement'].strip()):
            raise GateError('Acceptance criteria need unique IDs and observable requirements')
        seen.add(criterion['id'])
        verification = criterion['verification']
        if (not isinstance(verification, dict) or set(verification) != {'kind', 'check_id'}
                or verification['kind'] != 'automated' or not isinstance(verification['check_id'], str)
                or verification['check_id'] not in config.acceptance_checks):
            raise GateError('Unsupported or unregistered acceptance evidence')
    deps = raw['dependencies']
    if not isinstance(deps, list):
        raise GateError('dependencies must be a list')
    seen = {number}
    for dep in deps:
        if (not isinstance(dep, dict) or set(dep) != {'issue', 'pr', 'merge_commit'}
                or type(dep['issue']) is not int or dep['issue'] <= 0 or dep['issue'] in seen
                or type(dep['pr']) is not int or dep['pr'] <= 0
                or not isinstance(dep['merge_commit'], str) or not re.fullmatch('[0-9a-f]{40}', dep['merge_commit'])):
            raise GateError('Dependencies require unique other issues and pinned merged PR identities')
        seen.add(dep['issue'])


def context_hash(documents):
    if (not isinstance(documents, list) or not documents
            or any(not isinstance(d, dict) or set(d) != {'path', 'origin', 'content'}
                   or d['origin'] not in {'git', 'operator-local'} or not isinstance(d['content'], str)
                   for d in documents)):
        raise GateError('Invalid explicit guidance snapshot')
    if len(json.dumps(documents).encode()) > MAX_CONTEXT_BYTES:
        raise GateError('Explicit guidance exceeds 128 KiB; select less context rather than truncate it')
    return digest({'rendering_version': 1, 'documents': documents})


def check_registry_hash(config, raw):
    ids = sorted({c['verification']['check_id'] for c in raw['acceptance']})
    return digest({key: config.acceptance_checks[key] for key in ids})


def create_contract(config, raw, issue, base, documents):
    validate_manifest(config, raw, issue['number'])
    if [d['path'] for d in documents] != raw['context_refs']:
        raise GateError('Context does not exactly cover the selected guidance')
    result = dict(raw, source=source(issue), source_hash=digest(source(issue)), base=base,
                  documents=documents, context_hash=context_hash(documents),
                  checks_hash=check_registry_hash(config, raw))
    return dict(result, contract_hash=digest(result))


def load_contract(config, number):
    path = config.state_dir / 'tasks' / str(number) / 'contract.json'
    if not path.is_file() or path.is_symlink():
        raise GateError('Missing frozen task contract; use freeze with an operator-authored manifest')
    if path.stat().st_size > 512 * 1024:
        raise GateError('Task contract exceeds its size bound')
    raw = json.loads(path.read_text())
    if not isinstance(raw, dict) or set(raw) != CONTRACT_FIELDS:
        raise GateError('Invalid frozen contract fields')
    validate_manifest(config, {k: raw[k] for k in MANIFEST_FIELDS}, number)
    if (digest({k: v for k, v in raw.items() if k != 'contract_hash'}) != raw['contract_hash']
            or context_hash(raw['documents']) != raw['context_hash']
            or check_registry_hash(config, raw) != raw['checks_hash']):
        raise GateError('Frozen contract or check registry changed; explicit refresh required')
    return raw


def verify_current(contract, issue, base, documents):
    if (digest(source(issue)) != contract['source_hash'] or base != contract['base']
            or context_hash(documents) != contract['context_hash']):
        raise GateError('Task source, base or guidance changed; explicit contract refresh required')


def check_scope(contract, paths):
    for path in paths:
        if path.startswith(('gocrm-ui/', 'internal/handler/assets/')):
            raise GateError('Frontend edits require unsupported UI evidence; outside this backend MVP')
        if not any(fnmatch.fnmatchcase(path, scope) for scope in contract['scope_paths']):
            raise GateError(f'Change outside frozen task scope: {path}')


def check_dependency_cycles(root, number, dependencies):
    visited, active = set(), set()

    def visit(issue, deps=None):
        if issue in active:
            raise GateError('Task dependency cycle requires operator correction')
        if issue in visited:
            return
        if len(visited) + len(active) >= 128:
            raise GateError('Dependency graph exceeds the MVP bound')
        active.add(issue)
        if deps is None:
            path = safe_artifact(root, f'tasks/{issue}/contract.json')
            if path.exists():
                if path.stat().st_size > 512 * 1024:
                    raise GateError('Oversized dependency contract')
                raw = json.loads(path.read_text())
                deps = raw.get('dependencies') if isinstance(raw, dict) else None
            else:
                deps = []  # A dependency need not have run in this state directory.
        if not isinstance(deps, list):
            raise GateError('Invalid dependency contract')
        for dep in deps:
            if not isinstance(dep, dict) or type(dep.get('issue')) is not int or dep['issue'] <= 0:
                raise GateError('Invalid dependency identity')
            visit(dep['issue'])
        active.remove(issue)
        visited.add(issue)

    visit(number, dependencies)
