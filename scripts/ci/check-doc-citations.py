#!/usr/bin/env python3
"""Check that the code the fork and extension guides cite exists in the tracked tree.

Citations by line number drift silently whenever the cited file changes, so
the converted guides cite code by file plus symbol or anchor, and this check
resolves those citations against the tracked tree. Spans are backtick spans
outside fenced code blocks.

a. Paths. A span, without a leading ./ and a trailing :N, :N-M or :N,M, is a
   path when it has no whitespace, contains /, does not start with /, and its
   first segment is the first segment of a tracked file or the span is listed
   in IGNORED_OUTPUTS. A span with <, >, {, } or $ is a placeholder and is
   skipped; a span in IGNORED_OUTPUTS is skipped; a span with * must match at
   least one tracked file (fnmatch); any other path, without a trailing /,
   must be a tracked file or have a tracked file under it.
b. Symbols. A Go symbol is cited as `P.go` `S` (path span, one space, symbol
   span) or `S` (`P.go` (symbol span, a space, an opening parenthesis, path
   span). S is Name, Type.Name or pkg.Name, optionally followed by (), or the
   receiver form (*Type).Name / (*pkg.Type).Name. P, relative to the repo
   root, must be a tracked .go file, and S must be declared in it: a func, a
   method (Recv.Method), a type, a var or const, an interface method
   (Iface.Method) or a struct field (Type.Field), optionally qualified by the
   file's package name. The declared names come from the go/parser helper
   scripts/ci/docsymbols, run once per check.
c. Line citations. A file in CONVERTED_FILES may not contain a file.go:N,
   .ts, .tsx, .py, .sh or .sql line citation anywhere, fenced blocks
   included.

Existence is judged against `git ls-files` only, never the working tree, so
an untracked or gitignored local file does not count and a local run agrees
with the CI checkout. A converted file that yields no checked path span or no
symbol pair fails, so a broken extractor cannot pass empty. Standard library
only.

Usage: python3 scripts/ci/check-doc-citations.py [--repo-root DIR] [FILE ...]
"""

import argparse
import fnmatch
import json
import re
import subprocess
import sys
from pathlib import Path

# The guides whose line-number citations were converted to symbol citations.
CONVERTED_FILES = ('docs/FORKING.md', 'docs/EXTENDING.md')

# Gitignored build outputs and local configuration the guides legitimately
# cite. They exist only after a build or a local setup, never in a checkout,
# so they are skipped rather than checked. `git check-ignore` is not used:
# .git/info/exclude and the global excludes differ per machine.
IGNORED_OUTPUTS = ('bin/gophercrm', 'gocrm-ui/.env')

BACKTICK_SPAN = re.compile(r'`([^`\n]+)`')
LINE_SUFFIX = re.compile(r':[0-9][0-9,-]*$')
PLACEHOLDER = re.compile(r'[<>{}$]')
IDENT = r'[A-Za-z_][A-Za-z0-9_]*'
SYMBOL = re.compile(r'^%s(\.%s)?(\(\))?$' % (IDENT, IDENT))
RECEIVER = re.compile(r'^\(\*?((?:%s\.)?%s)\)\.(%s)(\(\))?$' % (IDENT, IDENT, IDENT))
FILE_EXTENSION = re.compile(r'\.(go|ts|tsx|js|jsx|py|sh|sql|md|yml|yaml|json|mod|sum|txt|html|css|conf|toml|env|example)$')
LINE_CITATION = re.compile(r'\.(go|ts|tsx|py|sh|sql):[0-9]+')
PAIR_PATH_FIRST = re.compile(r'`([^`\n]+)` `([^`\n]+)`')
PAIR_SYMBOL_FIRST = re.compile(r'`([^`\n]+)` \(`([^`\n]+)`')


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
        return path.split('/', 1)[0] in self.first_segments or path in IGNORED_OUTPUTS

    def exists(self, path):
        if '*' in path:
            return any(fnmatch.fnmatch(name, path) for name in self.files)
        path = path.rstrip('/')
        return path in self.files or path in self.dirs


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
        for span in BACKTICK_SPAN.findall(line):
            paths.append((number, span))
        for pattern, path_first in ((PAIR_PATH_FIRST, True), (PAIR_SYMBOL_FIRST, False)):
            for match in pattern.finditer(line):
                path_span, symbol_span = match.groups() if path_first else match.groups()[::-1]
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
            if PLACEHOLDER.search(path) or path in IGNORED_OUTPUTS:
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
        if name in CONVERTED_FILES:
            for number, line in enumerate(text.splitlines(), 1):
                for match in LINE_CITATION.finditer(line):
                    problems.append('%s:%d: `%s`: line citation' % (name, number, line_context(line, match)))
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
    parser.add_argument('files', nargs='*', help='repo-relative doc files; default: %s' % ', '.join(CONVERTED_FILES))
    args = parser.parse_args(argv)
    repo_root = Path(args.repo_root) if args.repo_root else default_repo_root()
    problems, counts = check(repo_root, args.files or list(CONVERTED_FILES))
    if problems:
        for line in problems:
            print(line)
        return 1
    print('OK: %d path citations, %d symbol citations, %d skipped placeholders or ignored outputs'
          % (counts['paths'], counts['symbols'], counts['skipped']))
    return 0


if __name__ == '__main__':
    sys.exit(main())
