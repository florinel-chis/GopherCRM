#!/usr/bin/env python3
"""Check that the permissions-and-access user guide names only things that exist.

The administrator guide under docs/user-guide/permissions-access quotes names
an administrator types or looks for: environment variables, error codes,
setting and permission keys, API routes and SPA paths. Prose drifts silently
when the code renames one of them, so this check resolves every such name
against the code and fails when one no longer exists.

Tokens are taken from backtick spans outside fenced code blocks, and from
`NAME=` at the start of a line inside fenced blocks. Four classes are checked
on every page:

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
   must exist with that method in api/swagger.json paths, or, with no prefix
   stripped, be one of the SCIM routes (below).
d. Paths (`/settings/sso`): a token starting with / and the path after a
   leading constant name (`/auth/callback`) must be an api/swagger.json path,
   a path: entry of gocrm-ui/src/routes/index.tsx (leading / ignored), /scim/v2
   itself or the path of a SCIM route.

The SCIM 2.0 routes are served outside the API prefix and are not in
api/swagger.json, so they are read from the code: the router.METHOD("<path>"
calls in the body of func SetupSCIMRoutes in internal/handler/scim_routes.go
(SetupSCIMTokenRoutes is not read: its routes sit under the API prefix and are
in swagger), with :x normalised to {x} and the /scim/v2 prefix in front. The
prefix is hard-coded and must appear as router.Group("/scim/v2") in
cmd/main.go. A missing prefix, a missing SetupSCIMRoutes or a body with no
route is reported as a problem, never treated as an empty set.

A dotted key also resolves when it is an event type of the shipped webhook
catalogue (`deal.created`), which the Go code builds at run time and never
writes as a literal. The types are read from internal/webhook/catalog.go: the
identifiers in var defaultCatalogue = NewCatalogue(...), each mapped through
its `<Ident> Entity = "<value>"` constant, crossed with the values of the
identifiers in var actions = []Action{...} (`<Ident> Action = "<value>"`). A
missing file, no defaultCatalogue, an identifier without a constant or no
action is reported as a problem, never treated as an empty set.

The sign-in audit page (sign-in-audit.md) has one more rule. Inside its
section headed `## Methods and reason codes` (up to the next `## ` heading),
every backtick span matching ^[a-z][a-z_]*$ must be the value of an
AuthAuditMethod*, AuthAuditOutcome* or AuthAuditReason* constant in
internal/service/auth_audit_vocabulary.go ("unknown sign-in audit code"), and
every method and reason value must appear in that section ("missing sign-in
audit code"). A missing section, or a vocabulary file that yields no method or
no reason, is a problem.

Tokens that match no class (`openid profile email`, `CRM.Admin`, lower-case
reason codes outside that section) are not checked. The check fails when any of
the ten pages is missing and when any class resolved zero tokens, so an
extractor broken by a rewrite of the guide cannot pass empty. It uses the
standard library only.

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
    'scim-provisioning.md',
    'api-keys.md',
    'sign-in-audit.md',
    'webhooks.md',
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
SCIM_PREFIX = '/scim/v2'
SCIM_ROUTES_FILE = 'internal/handler/scim_routes.go'
SCIM_PREFIX_FILE = 'cmd/main.go'
SCIM_FUNC = re.compile(r'^func SetupSCIMRoutes\(.*?\{\n(.*?)^\}', re.M | re.S)
SCIM_ROUTE_CALL = re.compile(r'\brouter\.(GET|POST|PUT|PATCH|DELETE)\("([^"]*)"')
WALK_SKIP = {'docs', 'node_modules', 'gocrm-ui', '.git'}
WEBHOOK_CATALOGUE_FILE = 'internal/webhook/catalog.go'
WEBHOOK_DEFAULT = re.compile(r'^var defaultCatalogue = NewCatalogue\(([^)]*)\)', re.M)
WEBHOOK_ACTIONS = re.compile(r'^var actions = \[\]Action\{([^}]*)\}', re.M)
WEBHOOK_ENTITY_CONST = re.compile(r'^\s*([A-Za-z_]\w*)\s+Entity\s*=\s*"([^"]*)"', re.M)
WEBHOOK_ACTION_CONST = re.compile(r'^\s*([A-Za-z_]\w*)\s+Action\s*=\s*"([^"]*)"', re.M)
GO_IDENT = re.compile(r'[A-Za-z_]\w*')
AUDIT_PAGE = 'sign-in-audit.md'
AUDIT_SECTION = '## Methods and reason codes'
AUDIT_VOCABULARY_FILE = 'internal/service/auth_audit_vocabulary.go'
AUDIT_CONST = re.compile(r'^\s*AuthAudit(Method|Outcome|Reason)\w*\s*=\s*"([^"]*)"', re.M)
AUDIT_CODE = re.compile(r'^[a-z][a-z_]*$')


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


def scim_routes(repo_root):
    """(routes, problems): the (METHOD, path) pairs of SetupSCIMRoutes under /scim/v2.

    Every way the routes cannot be read is a problem line; routes is then empty.
    """
    repo_root = Path(repo_root)
    problems = []
    try:
        main_text = (repo_root / SCIM_PREFIX_FILE).read_text(encoding='utf-8')
    except OSError:
        main_text = ''
    if 'router.Group("%s")' % SCIM_PREFIX not in main_text:
        problems.append('%s: no router.Group("%s"); the SCIM prefix moved' % (SCIM_PREFIX_FILE, SCIM_PREFIX))
    try:
        routes_text = (repo_root / SCIM_ROUTES_FILE).read_text(encoding='utf-8')
    except OSError:
        routes_text = ''
    body = SCIM_FUNC.search(routes_text)
    routes = set()
    if not body:
        problems.append('%s: no func SetupSCIMRoutes; the SCIM routes cannot be read' % SCIM_ROUTES_FILE)
    else:
        for method, path in SCIM_ROUTE_CALL.findall(body.group(1)):
            routes.add((method, SCIM_PREFIX + normalise_params(path)))
        if not routes:
            problems.append('%s: SetupSCIMRoutes registers no route the check can read' % SCIM_ROUTES_FILE)
    if problems:
        return set(), problems
    return routes, problems


def webhook_event_types(repo_root):
    """(types, problems): the event types of the shipped webhook catalogue.

    Every way the catalogue cannot be read is a problem line; types is then empty.
    """
    path = Path(repo_root) / WEBHOOK_CATALOGUE_FILE
    try:
        text = path.read_text(encoding='utf-8')
    except OSError:
        return set(), ['%s: missing; the webhook event types cannot be read' % WEBHOOK_CATALOGUE_FILE]
    problems = []
    entities = dict(WEBHOOK_ENTITY_CONST.findall(text))
    action_values = dict(WEBHOOK_ACTION_CONST.findall(text))
    default = WEBHOOK_DEFAULT.search(text)
    prefixes = []
    if not default:
        problems.append('%s: no var defaultCatalogue = NewCatalogue(...); the webhook event types cannot be read'
                        % WEBHOOK_CATALOGUE_FILE)
    else:
        idents = GO_IDENT.findall(default.group(1))
        if not idents:
            problems.append('%s: defaultCatalogue registers no entity' % WEBHOOK_CATALOGUE_FILE)
        for ident in idents:
            if ident not in entities:
                problems.append('%s: entity %s has no Entity constant' % (WEBHOOK_CATALOGUE_FILE, ident))
            else:
                prefixes.append(entities[ident])
    actions = WEBHOOK_ACTIONS.search(text)
    suffixes = []
    if not actions:
        problems.append('%s: no var actions = []Action{...}; the webhook event types cannot be read'
                        % WEBHOOK_CATALOGUE_FILE)
    else:
        idents = GO_IDENT.findall(actions.group(1))
        if not idents:
            problems.append('%s: actions lists no action' % WEBHOOK_CATALOGUE_FILE)
        for ident in idents:
            if ident not in action_values:
                problems.append('%s: action %s has no Action constant' % (WEBHOOK_CATALOGUE_FILE, ident))
            else:
                suffixes.append(action_values[ident])
    if problems:
        return set(), problems
    return {'%s.%s' % (prefix, suffix) for prefix in prefixes for suffix in suffixes}, problems


def auth_audit_vocabulary(repo_root):
    """(vocabulary, problems): {'method', 'outcome', 'reason'} -> set of values of the sign-in audit constants."""
    vocabulary = {'method': set(), 'outcome': set(), 'reason': set()}
    try:
        text = (Path(repo_root) / AUDIT_VOCABULARY_FILE).read_text(encoding='utf-8')
    except OSError:
        text = ''
    for kind, value in AUDIT_CONST.findall(text):
        vocabulary[kind.lower()].add(value)
    problems = []
    for kind in ('method', 'reason'):
        if not vocabulary[kind]:
            problems.append('%s: no sign-in audit %s constant found' % (AUDIT_VOCABULARY_FILE, kind))
    return vocabulary, problems


def audit_section_codes(text):
    """The backtick spans matching AUDIT_CODE inside the AUDIT_SECTION section, or None without the section."""
    lines = text.splitlines()
    try:
        start = next(i for i, line in enumerate(lines) if line.rstrip() == AUDIT_SECTION)
    except StopIteration:
        return None
    codes = []
    in_fence = False
    for line in lines[start + 1:]:
        if line.lstrip().startswith('```'):
            in_fence = not in_fence
            continue
        if in_fence:
            continue
        if line.startswith('## '):
            break
        for span in BACKTICK_SPAN.findall(line):
            token = span.strip()
            if AUDIT_CODE.match(token):
                codes.append(token)
    return codes


def check_audit_page(path, text, vocabulary):
    """Problem lines of the sign-in audit vocabulary rule, and the number of codes checked."""
    codes = audit_section_codes(text)
    if codes is None:
        return ['%s: no "%s" section' % (path, AUDIT_SECTION)], 0
    known = vocabulary['method'] | vocabulary['outcome'] | vocabulary['reason']
    problems = []
    for code in dict.fromkeys(codes):
        if code not in known:
            problems.append('%s: unknown sign-in audit code `%s`' % (path, code))
    present = set(codes)
    for kind in ('method', 'reason'):
        for value in sorted(vocabulary[kind] - present):
            problems.append('%s: missing sign-in audit code `%s` (a %s)' % (path, value, kind))
    return problems, len(codes)


def normalise_params(path):
    """A :x path parameter becomes {x}."""
    return re.sub(r':([A-Za-z_][A-Za-z0-9_]*)', r'{\1}', path)


def normalise_api_path(path):
    if path.startswith(API_PREFIX + '/'):
        path = path[len(API_PREFIX):]
    return normalise_params(path)


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
        self.scim, self.problems = scim_routes(repo_root)
        self.event_types, problems = webhook_event_types(repo_root)
        self.problems = self.problems + problems
        self.audit_vocabulary, problems = auth_audit_vocabulary(repo_root)
        self.problems = self.problems + problems
        self.scim_paths = {path for _, path in self.scim}
        if self.scim:
            self.scim_paths.add(SCIM_PREFIX)

    def resolves(self, kind, token):
        if kind == 'dotted key' and token in self.event_types:
            return True
        if kind in ('constant', 'dotted key'):
            return '"%s"' % token in self.literals
        if kind == 'route':
            method, path = ROUTE.match(token).groups()
            if (method, normalise_params(path)) in self.scim:
                return True
            return method.lower() in self.swagger.get(normalise_api_path(path), set())
        if kind == 'path':
            if normalise_params(token) in self.scim_paths or normalise_api_path(token) in self.swagger:
                return True
            return token.lstrip('/') in self.spa
        raise ValueError(kind)


def check(guide_dir, repo_root):
    """Return (problems, counts): problem lines and the number of tokens checked per class.

    counts also holds 'sign-in audit code', the codes checked in the sign-in audit section.
    """
    guide_dir = Path(guide_dir)
    problems = []
    counts = {kind: 0 for kind in CLASSES}
    counts['sign-in audit code'] = 0
    resolver = Resolver(repo_root)
    problems.extend(resolver.problems)
    for page in PAGES:
        path = guide_dir / page
        if not path.is_file():
            problems.append('%s: missing page' % path)
            continue
        text = path.read_text(encoding='utf-8')
        if page == AUDIT_PAGE:
            audit_problems, audit_count = check_audit_page(path, text, resolver.audit_vocabulary)
            problems.extend(audit_problems)
            counts['sign-in audit code'] += audit_count
        for kind, token, display in extract_tokens(text):
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
    print('OK: ' + ', '.join('%d %s tokens' % (counts[kind], kind) for kind in CLASSES + ('sign-in audit code',)))
    return 0


if __name__ == '__main__':
    sys.exit(main())
