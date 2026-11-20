#!/usr/bin/env python3
"""Check that the permissions-and-access user guide names only things that exist.

The administrator guide under docs/user-guide/permissions-access quotes names
an administrator types or looks for: environment variables, error codes,
setting and permission keys, API routes and SPA paths. Prose drifts silently
when the code renames one of them, so this check resolves every such name
against the code and fails when one no longer exists.

Tokens are taken from backtick spans outside fenced code blocks, and from
`NAME=` at the start of a line inside fenced blocks. Four classes are checked:

a. Constant names (`OIDC_CLIENT_SECRET`, `LAST_ADMIN`): a token matching
   ^[A-Z][A-Z0-9]*(_[A-Z0-9]+)+$, the leading such segment of a token like
   `APP_BASE_URL/auth/callback`, and `NAME=` in a fenced block must appear as
   the literal "NAME" in a tracked Go file that is not a _test.go file.
b. Dotted keys (`security.sso.allow_password`, `roles.audit_view`): a token
   matching ^[a-z][a-z0-9_]*(\\.[a-z][a-z0-9_]*)+$ must appear as the literal
   "token" in the same Go files. File names ending in .md, .go, .json, .yaml
   or .tsx are skipped. Host names match this pattern too, so the guide writes
   them without backticks rather than the check guessing at them.
c. Routes (`GET /roles/audit`): METHOD /path, METHOD one of GET, POST, PUT,
   PATCH, DELETE, with any /api/v1 prefix stripped and :x normalised to {x},
   must exist with that method in api/swagger.json paths.
d. Paths (`/settings/sso`): a token starting with / and the path after a
   leading constant name (`/auth/callback`) must be an api/swagger.json path
   or a path: entry of gocrm-ui/src/routes/index.tsx (leading / ignored).

Tokens that match no class (`openid profile email`, `CRM.Admin`, lower-case
reason codes) are not checked. The check fails when any of the six pages is
missing and when any class resolved zero tokens, so an extractor broken by a
rewrite of the guide cannot pass empty. It uses the standard library only.

Usage: python3 scripts/ci/check-user-guide.py [--guide-dir DIR] [--repo-root DIR]
"""

import argparse
import json
import os
import re
import subprocess
import sys
from pathlib import Path

PAGES = (
    'overview.md',
    'roles-and-permissions.md',
    'single-sign-on.md',
    'how-to-microsoft-entra-id.md',
    'testing-single-sign-on.md',
    'faq.md',
)
DEFAULT_GUIDE_DIR = 'docs/user-guide/permissions-access'
CLASSES = ('constant', 'dotted key', 'route', 'path')

CONSTANT = re.compile(r'^[A-Z][A-Z0-9]*(_[A-Z0-9]+)+$')
CONSTANT_PREFIX = re.compile(r'^([A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+)(/.*)$')
DOTTED = re.compile(r'^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$')
FILE_SUFFIXES = ('.md', '.go', '.json', '.yaml', '.tsx')
ROUTE = re.compile(r'^(GET|POST|PUT|PATCH|DELETE) (/\S*)$')
FENCED_ASSIGNMENT = re.compile(r'^([A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+)=')
BACKTICK_SPAN = re.compile(r'`([^`\n]+)`')
SPA_PATH = re.compile(r"""\bpath:\s*['"]([^'"]+)['"]""")
API_PREFIX = '/api/v1'
WALK_SKIP = {'docs', 'node_modules', 'gocrm-ui', '.git'}


def default_repo_root():
    return Path(__file__).resolve().parents[2]


def go_files(repo_root):
    """Tracked non-test Go files (git ls-files), or a walk when git is unavailable."""
    try:
        out = subprocess.run(['git', 'ls-files', '-z', '--', '*.go'], cwd=repo_root,
                             capture_output=True, check=True).stdout.decode()
        names = [name for name in out.split('\0') if name]
    except (OSError, subprocess.CalledProcessError):
        names = None
    if not names:
        names = []
        for dirpath, dirnames, filenames in os.walk(repo_root):
            dirnames[:] = [d for d in dirnames if d not in WALK_SKIP]
            for filename in filenames:
                if filename.endswith('.go'):
                    names.append(os.path.relpath(os.path.join(dirpath, filename), repo_root))
    keep = []
    for name in names:
        parts = Path(name).parts
        if name.endswith('_test.go') or parts[0] == 'docs' or 'node_modules' in parts or parts[0] == 'gocrm-ui':
            continue
        keep.append(Path(repo_root) / name)
    return keep


def go_literals(repo_root):
    """The text of every tracked non-test Go file, joined; a token resolves when '"token"' is in it."""
    chunks = []
    for path in go_files(repo_root):
        try:
            chunks.append(path.read_text(encoding='utf-8', errors='replace'))
        except OSError:
            continue
    return '\n'.join(chunks)


def swagger_paths(repo_root):
    """Map of swagger path -> set of lower-case methods."""
    data = json.loads((Path(repo_root) / 'api' / 'swagger.json').read_text(encoding='utf-8'))
    return {path: {method.lower() for method in ops} for path, ops in data.get('paths', {}).items()}


def spa_paths(repo_root):
    """The path: entries of the SPA route table, without a leading /."""
    text = (Path(repo_root) / 'gocrm-ui' / 'src' / 'routes' / 'index.tsx').read_text(encoding='utf-8')
    return {match.lstrip('/') for match in SPA_PATH.findall(text)}


def normalise_api_path(path):
    if path.startswith(API_PREFIX + '/'):
        path = path[len(API_PREFIX):]
    return re.sub(r':([A-Za-z_][A-Za-z0-9_]*)', r'{\1}', path)


def extract_tokens(text):
    """(class, token, display) triples of one page, in order of appearance."""
    tokens = []
    in_fence = False
    for line in text.splitlines():
        if line.lstrip().startswith('```'):
            in_fence = not in_fence
            continue
        if in_fence:
            match = FENCED_ASSIGNMENT.match(line)
            if match:
                tokens.append(('constant', match.group(1), match.group(1)))
            continue
        for span in BACKTICK_SPAN.findall(line):
            token = span.strip()
            route = ROUTE.match(token)
            prefixed = CONSTANT_PREFIX.match(token)
            if CONSTANT.match(token):
                tokens.append(('constant', token, token))
            elif prefixed:
                tokens.append(('constant', prefixed.group(1), token))
                tokens.append(('path', prefixed.group(2), token))
            elif DOTTED.match(token):
                if not token.endswith(FILE_SUFFIXES):
                    tokens.append(('dotted key', token, token))
            elif route:
                tokens.append(('route', token, token))
            elif token.startswith('/'):
                tokens.append(('path', token, token))
    return tokens


class Resolver:
    def __init__(self, repo_root):
        self.literals = go_literals(repo_root)
        self.swagger = swagger_paths(repo_root)
        self.spa = spa_paths(repo_root)

    def resolves(self, kind, token):
        if kind in ('constant', 'dotted key'):
            return '"%s"' % token in self.literals
        if kind == 'route':
            method, path = ROUTE.match(token).groups()
            return method.lower() in self.swagger.get(normalise_api_path(path), set())
        if kind == 'path':
            if normalise_api_path(token) in self.swagger:
                return True
            return token.lstrip('/') in self.spa
        raise ValueError(kind)


def check(guide_dir, repo_root):
    """Return (problems, counts): problem lines and the number of tokens checked per class."""
    guide_dir = Path(guide_dir)
    problems = []
    counts = {kind: 0 for kind in CLASSES}
    resolver = Resolver(repo_root)
    for page in PAGES:
        path = guide_dir / page
        if not path.is_file():
            problems.append('%s: missing page' % path)
            continue
        for kind, token, display in extract_tokens(path.read_text(encoding='utf-8')):
            counts[kind] += 1
            if not resolver.resolves(kind, token):
                if display == token:
                    problems.append('%s: unknown %s `%s`' % (path, kind, token))
                else:
                    problems.append('%s: unknown %s `%s` (in `%s`)' % (path, kind, token, display))
    for kind in CLASSES:
        if counts[kind] == 0:
            problems.append('%s: no %s token found; the extractor or the guide is broken' % (guide_dir, kind))
    return problems, counts


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split('\n\n')[0])
    parser.add_argument('--guide-dir', default=None, help='default: %s' % DEFAULT_GUIDE_DIR)
    parser.add_argument('--repo-root', default=None, help='default: the repository holding this script')
    args = parser.parse_args(argv)
    repo_root = Path(args.repo_root) if args.repo_root else default_repo_root()
    guide_dir = Path(args.guide_dir) if args.guide_dir else repo_root / DEFAULT_GUIDE_DIR
    problems, counts = check(guide_dir, repo_root)
    if problems:
        for line in problems:
            print(line)
        return 1
    print('OK: ' + ', '.join('%d %s tokens' % (counts[kind], kind) for kind in CLASSES))
    return 0


if __name__ == '__main__':
    sys.exit(main())
