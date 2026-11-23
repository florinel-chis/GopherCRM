#!/usr/bin/env python3
"""Check that the code the tracked docs cite exists in the tracked tree.

Citations by line number drift silently whenever the cited file changes, so
the docs cite code by file plus symbol or anchor, and this check resolves
those citations against the tracked tree. Without FILE arguments it checks
every tracked docs/**/*.md and README.md (from `git ls-files`, never a
directory walk), plus the files in EXTRA_DEFAULT_FILES, except the files in
EXEMPT_FILES; a file passed explicitly is checked in full, exempt or not. Spans are backtick spans outside fenced code
blocks. Every checked file goes through all three rules.

a. Paths. A span, without a leading ./ and a trailing :N, :N-M or :N,M, is a
   path when it has no whitespace, contains /, does not start with /, and its
   first segment is the first segment of a tracked file or the span is listed
   in IGNORED_OUTPUTS. A span with <, >, {, } or $ is a placeholder and is
   skipped; a span in IGNORED_OUTPUTS, or below one, is skipped; a missing
   Playwright spec the test catalog plans (PLANNED_SPEC in a PLANNED_SPEC_DOCS
   file) is skipped; a span with * must match at least one tracked file
   (fnmatch); any other path, without a trailing /, must be a tracked file or
   have a tracked file under it.
b. Symbols. A Go symbol is cited as `P.go` `S` (path span, one space, symbol
   span) or `S` (`P.go` (symbol span, a space, an opening parenthesis, path
   span). S is Name, Type.Name or pkg.Name, optionally followed by (), or the
   receiver form (*Type).Name / (*pkg.Type).Name. P, relative to the repo
   root, must be a tracked .go file, and S must be declared in it: a func, a
   method (Recv.Method), a type, a var or const, an interface method
   (Iface.Method) or a struct field (Type.Field), optionally qualified by the
   file's package name. The declared names come from the go/parser helper
   scripts/ci/docsymbols, run once per check.
c. Line citations. A checked file may not contain a file.go:N, .ts, .tsx,
   .py, .sh or .sql line citation anywhere, fenced blocks included.

Existence is judged against `git ls-files` only, never the working tree, so
an untracked or gitignored local file does not count and a local run agrees
with the CI checkout. A file in CONVERTED_FILES that yields no checked path
span or no symbol pair fails, so a broken extractor cannot pass empty.
Standard library only.

Usage: python3 scripts/ci/check-doc-citations.py [--repo-root DIR] [FILE ...]
"""

import argparse
import fnmatch
import json
import re
import subprocess
import sys
from pathlib import Path

# Docs that are not checked by default, each with the test that pins it.
# internal/config/concurrency_doc_test.go pins every citation of the auth
# service files (internal/service/auth_service.go and the per-flow files split
# from it) in docs/CONCURRENCY.md to its file, its enclosing function and a
# text fragment of the cited line; citations of other files
# are only checked to lie inside their file. This is the only full
# exemption. An exemption applies to discovered files only.
EXEMPT_FILES = {
    'docs/CONCURRENCY.md': 'internal/config/concurrency_doc_test.go',
}

# Tracked docs outside docs/ that are checked by default as well. Discovery
# does not widen to every README.md: only the files listed here are added,
# and only while `git ls-files` lists them. CHANGELOG.md is deliberately not
# checked: its released entries are history and name files that were later
# removed or renamed (and migration stems without the .up/.down suffix), and
# rewriting them would change what a release said.
EXTRA_DEFAULT_FILES = (
    'internal/idkit/README.md',
)

# The guides whose line-number citations were converted to symbol citations:
# the fork and extension guides (#487), then the use-case, provisioning and
# smaller guides and the README (#488). Each must keep at least one checked
# path and one symbol pair, so a broken extractor cannot pass them empty.
# docs/DOCKER.md keeps its Compose line citations (docker-compose*.yml:N),
# which a Go test recomputes; they are not .go/.ts/.tsx/.py/.sh/.sql
# citations, so rule c does not apply to them.
CONVERTED_FILES = (
    'docs/FORKING.md',
    'docs/EXTENDING.md',
    'docs/USE_CASES.md',
    'docs/SERVER_PROVISIONING.md',
    'README.md',
    'docs/REFACTORING_GUIDE.md',
    'docs/FEATURES.md',
    'docs/DOCKER.md',
    'docs/datamodel.md',
    'docs/SETUP.md',
)

# Gitignored build outputs and local configuration the guides legitimately
# cite. They exist only after a build or a local setup, never in a checkout,
# so they are skipped rather than checked, and so is any path below one.
# `git check-ignore` is not used: .git/info/exclude and the global excludes
# differ per machine.
IGNORED_OUTPUTS = (
    'bin/gophercrm', 'gocrm-ui/.env', 'gocrm-ui/dist',
    # gitignored, local only
    'docs/specs', 'docs/plans', 'gocrm-ui/node_modules',
)

# The one path-check skip. The test catalog names the Playwright specs it
# plans (docs/testing/README.md says so); dropping those names would change
# its Automation lines and case counts. A planned spec is skipped only in a
# catalog file and only while it is missing; the same path in any other doc
# is a missing path.
PLANNED_SPEC_DOCS = 'docs/testing/*.md'
PLANNED_SPEC = 'gocrm-ui/e2e/tests/*.spec.ts'

BACKTICK_SPAN = re.compile(r'`([^`\n]+)`')
LINE_SUFFIX = re.compile(r':[0-9][0-9,-]*$')
PLACEHOLDER = re.compile(r'[<>{}$]')
IDENT = r'[A-Za-z_][A-Za-z0-9_]*'
SYMBOL = re.compile(r'^%s(\.%s)?(\(\))?$' % (IDENT, IDENT))
RECEIVER = re.compile(r'^\(\*?((?:%s\.)?%s)\)\.(%s)(\(\))?$' % (IDENT, IDENT, IDENT))
FILE_EXTENSION = re.compile(r'\.(go|ts|tsx|js|jsx|py|sh|sql|md|yml|yaml|json|mod|sum|txt|html|css|conf|toml|env|example)$')
LINE_CITATION = re.compile(r'\.(go|ts|tsx|py|sh|sql):[0-9]+')
# The text between two neighbouring backtick spans that makes them a pair:
# `P.go` `S` (path first) or `S` (`P.go` (symbol first). Every neighbouring
# pair of spans is tested, so a span that also pairs with its other neighbour
# (`x` (`S` (`P.go`) cannot hide a citation.
GAP_PATH_FIRST = ' '
GAP_SYMBOL_FIRST = ' ('


def default_repo_root():
    return Path(__file__).resolve().parents[2]


def tracked_files(repo_root):
    """The tracked paths of the repository; raises when git fails."""
    out = subprocess.run(['git', '-C', str(repo_root), 'ls-files', '-z'],
                         capture_output=True, check=True).stdout.decode('utf-8')
    return [name for name in out.split('\0') if name]


def normalize_path(span):
    span = span.strip()
    if span.startswith('./'):
        span = span[2:]
    return LINE_SUFFIX.sub('', span)


def normalize_symbol(span):
    """The symbol a span cites, without () and receiver parentheses, or None."""
    span = span.strip()
    receiver = RECEIVER.match(span)
    if receiver:
        return '%s.%s' % (receiver.group(1), receiver.group(2))
    if SYMBOL.match(span) and not FILE_EXTENSION.search(span.replace('()', '')):
        return span[:-2] if span.endswith('()') else span
    return None


def outside_fences(text):
    """(line number, line) pairs of the lines outside fenced code blocks."""
    in_fence = False
    for number, line in enumerate(text.splitlines(), 1):
        if line.lstrip().startswith('```'):
            in_fence = not in_fence
            continue
        if not in_fence:
            yield number, line


class Tree:
    def __init__(self, repo_root):
        self.files = set(tracked_files(repo_root))
        self.first_segments = {name.split('/', 1)[0] for name in self.files}
        self.dirs = set()
        for name in self.files:
            parts = name.split('/')
            for i in range(1, len(parts)):
                self.dirs.add('/'.join(parts[:i]))

    def is_path(self, path):
        if not path or any(c.isspace() for c in path) or '/' not in path or path.startswith('/'):
            return False
        return path.split('/', 1)[0] in self.first_segments or is_ignored_output(path)

    def exists(self, path):
        if '*' in path:
            return any(fnmatch.fnmatch(name, path) for name in self.files)
        path = path.rstrip('/')
        return path in self.files or path in self.dirs


def is_ignored_output(path):
    """Whether path is an IGNORED_OUTPUTS entry or lies below one (segment boundaries)."""
    path = path.rstrip('/')
    return any(path == entry or path.startswith(entry + '/') for entry in IGNORED_OUTPUTS)


def is_planned_spec(doc, path):
    """Whether path names a Playwright spec the test catalog plans."""
    return fnmatch.fnmatchcase(doc, PLANNED_SPEC_DOCS) and fnmatch.fnmatchcase(path, PLANNED_SPEC)


def default_files(repo_root):
    """Every tracked docs/**/*.md, README.md and EXTRA_DEFAULT_FILES entry, minus EXEMPT_FILES."""
    return [name for name in tracked_files(repo_root)
            if ((name.startswith('docs/') and name.endswith('.md')) or name == 'README.md'
                or name in EXTRA_DEFAULT_FILES)
            and name not in EXEMPT_FILES]


def go_symbols(repo_root, go_paths):
    """Map of repo-relative .go path -> (package, set of symbols), via the docsymbols helper."""
    if not go_paths:
        return {}
    repo_root = Path(repo_root).resolve()
    absolute = {str(repo_root / path): path for path in sorted(go_paths)}
    result = subprocess.run(['go', 'run', './scripts/ci/docsymbols', *absolute],
                            cwd=default_repo_root(), capture_output=True, text=True)
    if result.returncode != 0:
        raise RuntimeError('docsymbols failed: %s' % (result.stderr.strip() or result.returncode))
    data = json.loads(result.stdout)
    return {absolute[key]: (value['package'], set(value['symbols'] or ())) for key, value in data.items()}


def citations(text):
    """(paths, pairs): (line, span) path candidates and (line, path span, symbol span) pairs."""
    paths, pairs = [], []
    for number, line in outside_fences(text):
        spans = list(BACKTICK_SPAN.finditer(line))
        for match in spans:
            paths.append((number, match.group(1)))
        for left, right in zip(spans, spans[1:]):
            gap = line[left.end():right.start()]
            if gap == GAP_PATH_FIRST:
                path_span, symbol_span = left.group(1), right.group(1)
            elif gap == GAP_SYMBOL_FIRST:
                path_span, symbol_span = right.group(1), left.group(1)
            else:
                continue
            if normalize_path(path_span).endswith('.go') and normalize_symbol(symbol_span):
                pairs.append((number, path_span, symbol_span))
    return paths, pairs


def check(repo_root, files):
    """Return (problems, counts) for the given repo-relative doc files."""
    repo_root = Path(repo_root)
    problems = []
    counts = {'paths': 0, 'skipped': 0, 'symbols': 0}
    try:
        tree = Tree(repo_root)
    except (OSError, subprocess.CalledProcessError) as exc:
        return ['git ls-files failed in %s: %s' % (repo_root, exc)], counts
    parsed = {}
    for name in files:
        doc = repo_root / name
        try:
            text = doc.read_text(encoding='utf-8')
        except OSError as exc:
            problems.append('%s: cannot read: %s' % (name, exc))
            continue
        parsed[name] = (text, *citations(text))

    go_paths = {normalize_path(span) for _, _, pairs in parsed.values() for _, span, _ in pairs}
    go_paths = {path for path in go_paths if path in tree.files}
    try:
        declared = go_symbols(repo_root, go_paths)
    except (OSError, RuntimeError, ValueError) as exc:
        return problems + ['go symbol helper failed: %s' % exc], counts

    for name, (text, paths, pairs) in parsed.items():
        file_paths = file_pairs = 0
        for number, span in paths:
            path = normalize_path(span)
            if not tree.is_path(path):
                continue
            if PLACEHOLDER.search(path) or is_ignored_output(path):
                counts['skipped'] += 1
                continue
            if is_planned_spec(name, path) and not tree.exists(path):
                counts['skipped'] += 1
                continue
            counts['paths'] += 1
            file_paths += 1
            if not tree.exists(path):
                problems.append('%s:%d: `%s`: missing path' % (name, number, span))
        for number, path_span, symbol_span in pairs:
            path = normalize_path(path_span)
            symbol = normalize_symbol(symbol_span)
            counts['symbols'] += 1
            file_pairs += 1
            if path not in declared:
                problems.append('%s:%d: `%s`: missing path' % (name, number, path_span))
                continue
            package, symbols = declared[path]
            local = symbol[len(package) + 1:] if symbol.startswith(package + '.') else None
            if symbol not in symbols and local not in symbols:
                problems.append('%s:%d: `%s`: unknown symbol %s in %s' % (name, number, symbol_span, symbol, path))
        for number, line in enumerate(text.splitlines(), 1):
            for match in LINE_CITATION.finditer(line):
                problems.append('%s:%d: `%s`: line citation' % (name, number, line_context(line, match)))
        if name in CONVERTED_FILES:
            if file_paths == 0:
                problems.append('%s: no path citation found; the extractor or the guide is broken' % name)
            if file_pairs == 0:
                problems.append('%s: no symbol citation found; the extractor or the guide is broken' % name)
    return problems, counts


def line_context(line, match):
    """The non-space run of text around a line-citation match."""
    start = match.start()
    while start > 0 and not line[start - 1].isspace() and line[start - 1] not in '`(':
        start -= 1
    end = match.end()
    while end < len(line) and not line[end].isspace() and line[end] not in '`)':
        end += 1
    return line[start:end]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split('\n\n')[0])
    parser.add_argument('--repo-root', default=None, help='default: the repository holding this script')
    parser.add_argument('files', nargs='*',
                        help='repo-relative doc files, checked in full; default: every tracked docs/**/*.md, '
                             'README.md and %s except %s' % (', '.join(EXTRA_DEFAULT_FILES),
                                                            ', '.join(sorted(EXEMPT_FILES))))
    args = parser.parse_args(argv)
    repo_root = Path(args.repo_root) if args.repo_root else default_repo_root()
    files = args.files
    if not files:
        try:
            files = default_files(repo_root)
        except (OSError, subprocess.CalledProcessError) as exc:
            print('git ls-files failed in %s: %s' % (repo_root, exc))
            return 1
    problems, counts = check(repo_root, files)
    if problems:
        for line in problems:
            print(line)
        return 1
    print('OK: %d path citations, %d symbol citations, %d skipped placeholders, ignored outputs '
          'or planned specs, in %d files' % (counts['paths'], counts['symbols'], counts['skipped'], len(files)))
    return 0


if __name__ == '__main__':
    sys.exit(main())
