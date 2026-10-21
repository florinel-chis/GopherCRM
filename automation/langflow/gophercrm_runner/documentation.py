"""Documentation impact and review evidence for one immutable candidate.

The map suggests what to read; independent review decides whether prose is accurate.
It never grants write scope, executes generators, or edits documentation after merge.
"""

import argparse
import json
from pathlib import Path, PurePosixPath
import re
import subprocess

from .config import GateError
from .validation import changed_paths


POLICY_VERSION = 1
PRODUCT = ('README.md', 'CHANGELOG.md', 'docs/FEATURES.md', 'docs/ROADMAP.md')
WORKFLOW = (*PRODUCT, 'docs/DEVELOPMENT.md', 'docs/VALIDATION_RELEASE.md', 'docs/DOCUMENTATION_WORKFLOW.md',
            'automation/langflow/README.md', 'automation/langflow/BATCHES.md')
FEATURE_DOCS = {
    'auth': '01-authentication', 'dashboard': '02-dashboard', 'lead': '03-leads',
    'customer': '04-customers', 'ticket': '05-tickets', 'task': '06-tasks',
    'user': '07-users', 'configuration': '08-settings', 'label': '10-labels',
    'aeo': '11-aeo', 'form': '12-forms', 'compan': '13-companies', 'deal': '14-deals',
}
REVIEW_SCHEMA = {
    'type': 'array', 'items': {
        'type': 'object', 'additionalProperties': False,
        'required': ['path', 'disposition', 'reason'],
        'properties': {
            'path': {'type': 'string'},
            'disposition': {'type': 'string', 'enum': [
                'updated', 'already-accurate', 'no-impact', 'needs-update']},
            'reason': {'type': 'string'},
        },
    },
}


def safe_path(path):
    if (not isinstance(path, str) or not path or path.startswith('/')
            or '..' in PurePosixPath(path).parts or '.git' in PurePosixPath(path).parts
            or any(ord(c) < 32 for c in path) or '\\' in path):
        raise GateError('Invalid documentation path')
    return path


def is_document(path):
    return (path.endswith(('.md', '.mdx', '.rst')) or path in {
        '.env.example', 'gocrm-ui/.env.example', 'api/swagger.json', 'api/swagger.yaml', 'api/docs.go'})


def suggestions(paths):
    """Also accepts frozen scope patterns for preliminary implementation guidance."""
    selected = {}

    def add(names, reason):
        for name in names:
            selected.setdefault(name, set()).add(reason)

    for path in sorted(set(paths)):
        safe_path(path)
        if is_document(path):
            if not any(c in path for c in '*?['):
                add((path,), 'changed documentation')
            continue
        lower = path.lower()
        if path.startswith(('automation/langflow/', '.github/', 'scripts/ci/', 'scripts/release/',
                            'scripts/deploy/')) or path == 'Makefile':
            add(WORKFLOW, 'development or delivery workflow')
        elif path.startswith(('internal/', 'cmd/', 'gocrm-ui/')):
            add(PRODUCT, 'application behavior or coverage')
            for feature, name in FEATURE_DOCS.items():
                if feature in lower:
                    add((f'docs/testing/{name}.md',), 'affected feature guidance')
            if path.startswith(('internal/models/', 'internal/database/', 'cmd/migrate/')):
                add(('docs/datamodel.md', 'docs/SETUP.md', 'docs/DOCKER.md'), 'schema or upgrade behavior')
            if 'aeo' in lower:
                add(('docs/AEO_SECURITY.md',), 'AEO operations and security')
            if path.startswith('internal/middleware/') or 'auth' in lower:
                add(('docs/DEVELOPMENT.md', 'docs/testing/09-cross-cutting.md'), 'security or shared behavior')
            if path.startswith('internal/handler/'):
                add(('api/swagger.json', 'api/swagger.yaml', 'api/docs.go'), 'generated API contract; regenerate when annotations change')
            if path.startswith(('cmd/', 'internal/config/')):
                add(('docs/SETUP.md', '.env.example'), 'runtime configuration')
        elif path.startswith('migrations/'):
            add((*PRODUCT, 'docs/datamodel.md', 'docs/SETUP.md', 'docs/DOCKER.md'), 'schema or upgrade behavior')
        else:
            add((*PRODUCT, 'docs/DEVELOPMENT.md'), 'unmapped change; assess conservatively')
    return [{'path': p, 'reasons': sorted(reasons)} for p, reasons in sorted(selected.items())]


def git(repo, *args):
    try:
        return subprocess.check_output(['git', '--literal-pathspecs', *args], cwd=repo,
            stderr=subprocess.PIPE, timeout=15).decode()
    except (subprocess.SubprocessError, UnicodeError) as exc:
        raise GateError('Cannot inspect committed documentation') from exc


def commit(value):
    if not isinstance(value, str) or not re.fullmatch('[0-9a-f]{40}', value):
        raise GateError('Documentation requires full commit SHAs')
    return value


def blob(repo, revision, path):
    commit(revision)
    safe_path(path)
    rows = git(repo, 'ls-tree', '-z', revision, '--', path).split('\0')
    if rows == ['']:
        return None
    if len(rows) != 2:
        raise GateError('Ambiguous documentation object')
    metadata, name = rows[0].split('\t', 1)
    mode, kind, oid = metadata.split()
    if name != path or kind != 'blob' or mode not in {'100644', '100755'}:
        raise GateError('Documentation must be a regular committed file, never a symlink')
    return oid


def plan(repo, base, head):
    commit(base)
    commit(head)
    try:
        paths = changed_paths(base, head, cwd=repo)
    except (ValueError, subprocess.SubprocessError) as exc:
        raise GateError('Cannot determine documentation change scope') from exc
    documents = suggestions(paths)
    if len(documents) > 80:
        raise GateError('Documentation assessment exceeds the bounded MVP; split the PR')
    for document in documents:
        before = blob(repo, base, document['path'])
        after = blob(repo, head, document['path'])
        document.update(base_blob=before, head_blob=after, changed=before != after)
    return dict(policy_version=POLICY_VERSION, base=base, head=head,
                changed_paths=paths, documents=documents)


def review_coverage(report, selected, approved):
    expected = {d['path']: d for d in selected['documents']}
    if not isinstance(report, list):
        raise GateError('Documentation review is missing')
    seen = set()
    for item in report:
        if (not isinstance(item, dict) or set(item) != {'path', 'disposition', 'reason'}
                or not isinstance(item['path'], str) or item['path'] not in expected
                or item['path'] in seen or not isinstance(item['disposition'], str) or item['disposition'] not in {
                    'updated', 'already-accurate', 'no-impact', 'needs-update'}
                or not isinstance(item['reason'], str) or not item['reason'].strip()
                or len(item['reason']) > 4000):
            raise GateError('Invalid, duplicate or unexplained documentation disposition')
        document = expected[item['path']]
        disposition = item['disposition']
        if ((disposition == 'updated' and not document['changed'])
                or (disposition in {'already-accurate', 'no-impact'} and document['changed'])
                or (disposition == 'already-accurate' and document['head_blob'] is None)
                or (approved and disposition == 'needs-update')):
            raise GateError('Documentation approval contradicts the committed evidence')
        seen.add(item['path'])
    if seen != set(expected):
        raise GateError('Documentation review does not cover every affected document')


def verify_merge(repo, selected, merge_commit):
    commit(merge_commit)
    for document in selected['documents']:
        if blob(repo, merge_commit, document['path']) != document['head_blob']:
            raise GateError('Merged documentation differs from the reviewed candidate: ' + document['path'])
    return dict(merge_commit=merge_commit, reviewed_head=selected['head'],
                documents={d['path']: d['head_blob'] for d in selected['documents']})


def summary(report):
    return '\n'.join(f'- `{d["path"]}` — {d["disposition"]}: {d["reason"]}' for d in report)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base', required=True)
    parser.add_argument('--head', required=True)
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    result = json.dumps(plan(Path.cwd(), args.base, args.head), indent=2) + '\n'
    if args.output:
        args.output.write_text(result)
    print(result, end='')


if __name__ == '__main__':
    main()
