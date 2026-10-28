"""Runner-observed, candidate-bound evidence; model claims cannot replace it."""

import hashlib
import json
import os
from pathlib import Path
import re

from .config import CommandError, GateError, check_kind
from .process import redact
from . import documentation


BINDING_KEYS = ('execution_id', 'head', 'base', 'policy', 'contract_hash', 'context_hash', 'validation_attempt')


def sha(data):
    return hashlib.sha256(data).hexdigest()


def named_go_tests(output, tests):
    required = {(t['package'], t['name']) for t in tests}
    passed = set()
    for line in output.splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if not isinstance(event, dict):
            continue
        identity = (event.get('Package'), event.get('Test'))
        required_descendant = (isinstance(identity[1], str) and any(
            identity[0] == package and identity[1].startswith(name + '/') for package, name in required))
        if (identity in required or required_descendant) and event.get('Action') in {'skip', 'fail'}:
            raise CommandError('Required acceptance regression or descendant failed or was skipped')
        if identity in required:
            if event.get('Action') == 'pass':
                if identity in passed:
                    raise CommandError('Duplicate required acceptance result')
                passed.add(identity)
    if passed != required:
        raise CommandError('Named acceptance regressions did not all report exact package/test passes')


class VitestIdentities(list):
    """Named Vitest identities. Serializes exactly like the registry list it copies."""
    kind = 'vitest'


def vitest_identities(tests):
    return VitestIdentities(tests)


def acceptance_tests(check):
    """Named tests of a registry entry, carrying its kind into the (id, argv, tests) check tuple.
    A Go entry yields its registry list unchanged, so existing plan identities are preserved."""
    return vitest_identities(check['tests']) if check_kind(check) == 'vitest' else check['tests']


IDENTITY_FIELDS = {'go': {'package', 'name'}, 'vitest': {'file', 'name'}}


def named_tests(output, tests, *, root=None):
    """Dispatch to the parser of the check's declared kind; never infer it from the output.
    `root` is the validation checkout; Vitest results must then name its exact file paths."""
    kind = getattr(tests, 'kind', 'go')
    if kind not in IDENTITY_FIELDS or any(
            not isinstance(t, dict) or set(t) != IDENTITY_FIELDS[kind] for t in tests):
        raise GateError('Named-test identities do not match their declared check kind')
    if kind == 'vitest':
        named_vitest_tests(output, tests, root=root)
    else:
        named_go_tests(output, tests)


def vitest_report(output):
    """Return the one JSON report on stdout, after any leading non-JSON lines."""
    decoder = json.JSONDecoder()
    offset = 0
    for line in output.splitlines(keepends=True):
        start, offset = offset, offset + len(line)
        if not line.startswith('{'):
            continue
        try:
            report, end = decoder.raw_decode(output, start)
        except ValueError:
            continue
        if output[end:].strip():
            raise CommandError('Vitest output continues after its JSON report')
        if (not isinstance(report, dict) or not isinstance(report.get('testResults'), list)
                or any(not isinstance(r, dict) or not isinstance(r.get('name'), str)
                       or not isinstance(r.get('assertionResults'), list)
                       or any(not isinstance(a, dict) for a in r['assertionResults'])
                       for r in report['testResults'])):
            raise CommandError('Malformed Vitest JSON report')
        return report
    raise CommandError('Acceptance check printed no Vitest JSON report')


def named_vitest_tests(output, tests, *, root=None):
    """Require each named file/test identity to pass exactly once.

    With `root` (the validation checkout) a result matches a file only when its path
    equals `<root>/gocrm-ui/<file>`; without it, a path ending in `/gocrm-ui/<file>` matches."""
    required = {(t['file'], t['name']) for t in tests}
    report = vitest_report(output)
    prefix = None if root is None else str(root).replace('\\', '/').rstrip('/') + '/gocrm-ui/'
    passed = set()
    for file in sorted({file for file, _ in required}):
        names = [(r, r['name'].replace('\\', '/')) for r in report['testResults']]
        results = [r for r, name in names
                   if (name == prefix + file if prefix else name.endswith('/gocrm-ui/' + file))]
        if len(results) > 1:
            raise CommandError('Duplicate required acceptance result')
        if not results or not results[0]['assertionResults']:
            raise CommandError('Required Vitest file reported no results')
        if results[0].get('status') != 'passed':
            raise CommandError('Required acceptance test file failed or was skipped')
        for assertion in results[0]['assertionResults']:
            identity = (file, assertion.get('fullName'))
            if identity not in required:
                continue
            if assertion.get('status') != 'passed':
                raise CommandError('Required acceptance test failed or was skipped')
            if identity in passed:
                raise CommandError('Duplicate required acceptance result')
            passed.add(identity)
    if passed != required:
        raise CommandError('Named acceptance tests did not all report exact file/test passes')


def safe_artifact(root, relative):
    path = Path(relative)
    if path.is_absolute() or '..' in path.parts:
        raise GateError('Evidence path escapes private artifacts')
    current = root
    for part in path.parts:
        current = current / part
        if current.is_symlink():
            raise GateError('Evidence cannot use symlinks')
    if not current.resolve().is_relative_to(root.resolve()):
        raise GateError('Evidence escapes private state')
    return current


def private_bytes(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    temp = path.with_suffix('.tmp')
    fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, 'wb') as out:
        out.write(value); out.flush(); os.fsync(out.fileno())
    os.replace(temp, path)


def artifact_prefix(record):
    for key, length in [('execution_id', 32), ('head', 40), ('validation_attempt', 32)]:
        if not re.fullmatch('[0-9a-f]{' + str(length) + '}', record[key]):
            raise GateError('Invalid evidence identity')
    return Path('artifacts') / record['execution_id'] / record['head'] / record['validation_attempt']


def write_receipt(root, record, check_id, argv, output, exit_code, *, checkout=None):
    """Write a candidate-bound receipt. `checkout` records the validation checkout that
    produced a Vitest report, whose result paths name it; other receipts omit the field."""
    if not re.fullmatch(r'[a-z][a-z0-9_-]{0,80}', check_id) or exit_code != 0:
        raise GateError('Only successful trusted checks produce acceptance receipts')
    prefix = artifact_prefix(record)
    output_path = prefix / f'{check_id}.output'
    output_bytes = redact(output).encode()
    private_bytes(safe_artifact(root, output_path), output_bytes)
    receipt = {k: record[k] for k in BINDING_KEYS}
    receipt.update(check_id=check_id, argv=list(argv), exit_code=exit_code,
                   output_path=str(output_path), output_sha256=sha(output_bytes))
    if checkout is not None:
        receipt['root'] = str(checkout)
    raw = json.dumps(receipt, sort_keys=True).encode()
    path = prefix / f'{check_id}.json'
    private_bytes(safe_artifact(root, path), raw)
    return {'path': str(path), 'sha256': sha(raw)}


def verify_receipt(root, record, reference, check_id, argv):
    expected_path = artifact_prefix(record) / f'{check_id}.json'
    if not isinstance(reference, dict) or set(reference) != {'path', 'sha256'} or reference['path'] != str(expected_path):
        raise GateError('Missing or stale acceptance receipt')
    try:
        path = safe_artifact(root, reference['path'])
        if path.stat().st_size > 128 * 1024:
            raise GateError('Oversized receipt')
        raw = path.read_bytes()
        receipt = json.loads(raw)
        if sha(raw) != reference['sha256'] or any(receipt.get(k) != record[k] for k in BINDING_KEYS):
            raise GateError('Evidence identity or digest changed')
        if receipt.get('check_id') != check_id or receipt.get('argv') != list(argv) or receipt.get('exit_code') != 0:
            raise GateError('Evidence does not match the trusted successful check')
        if receipt.get('output_path') != str(expected_path.with_suffix('.output')):
            raise GateError('Unexpected check output path')
        output = safe_artifact(root, receipt['output_path'])
        if output.stat().st_size > 20 * 1024 * 1024 or sha(output.read_bytes()) != receipt['output_sha256']:
            raise GateError('Evidence output changed or exceeded its bound')
        return receipt
    except (OSError, ValueError, KeyError, TypeError) as exc:
        raise GateError('Missing or invalid evidence artifact') from exc


def review_coverage(verdict, criteria, *, documentation_plan=None):
    fields = {'approved', 'blocking_findings', 'summary', 'criteria'}
    if documentation_plan is not None:
        fields.add('documentation')
    if (not isinstance(verdict, dict) or set(verdict) != fields
            or type(verdict['approved']) is not bool or not isinstance(verdict['summary'], str)
            or not isinstance(verdict['blocking_findings'], list)
            or any(not isinstance(f, str) or not f.strip() for f in verdict['blocking_findings'])
            or not isinstance(verdict['criteria'], list)):
        raise GateError('Invalid structured acceptance review')
    expected = {c['id']: 'acceptance-' + c['verification']['check_id'] for c in criteria}
    seen = set()
    for item in verdict['criteria']:
        if (not isinstance(item, dict) or set(item) != {'id', 'passed', 'evidence', 'finding'}
                or not isinstance(item['id'], str) or item['id'] not in expected or item['id'] in seen
                or type(item['passed']) is not bool or item['evidence'] != [expected[item['id']]]
                or not isinstance(item['finding'], str) or (not item['passed'] and not item['finding'].strip())):
            raise GateError('Review has missing, duplicate, unknown or inconsistent criterion evidence')
        if verdict['approved'] and (not item['passed'] or item['finding']):
            raise GateError('Approval contradicts a criterion finding')
        seen.add(item['id'])
    if seen != set(expected) or verdict['approved'] == bool(verdict['blocking_findings']):
        raise GateError('Review coverage or approval contradicts its findings')
    if documentation_plan is not None:
        documentation.review_coverage(verdict['documentation'], documentation_plan, verdict['approved'])
    return verdict
