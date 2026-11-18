#!/usr/bin/env python3
"""Check formatting, build and vet the module, then test the Go packages a change touches.

Outside a release, CI runs this instead of the whole suite: gofmt on every tracked
Go file (check-go-format.py, not with --tests-only), then the packages that own the
changed files, every package that imports them (transitively, test imports included)
and the few packages whose tests read the changed files from disk. A documentation-only
change runs no Go test. The race detector and the no-skip checkers belong to a full run
(`make verify-backend`, check-go.py, check-auth.py). The migrations matrix is a separate
job: a pull request that changes migration code runs its MySQL 8 leg, a full run both engines.

    python3 scripts/ci/check-changed-go.py --base origin/main          # local
    python3 scripts/ci/check-changed-go.py --base origin/main --list   # selection only
    python3 scripts/ci/check-changed-go.py --plan validation-plan.json # CI

--base compares REV with HEAD: uncommitted changes are not part of it.
"""

import argparse
import json
from pathlib import PurePosixPath
import subprocess

from go_checks import ROOT, verification_environment

EVERY = object()
E2E_TAG = 'gophercrm_e2e'
# The gofmt check over every tracked Go file, started from the repository root.
FORMAT_CHECK = ['python3', 'scripts/ci/check-go-format.py']
# Packages with files behind the e2e build tag; a second run compiles them in.
E2E_TAGGED = ('internal/aeo', 'internal/middleware')
# What a non-documentation change outside every package still has to prove:
# the configuration tests read .env.example, the compose files, the Dockerfile
# and the deploy scripts.
FALLBACK = 'internal/config'

# Packages whose tests read files from disk without importing them. A match
# adds the named packages as they are, without their importers. A new test that
# reads files outside its own package needs a row here; until it has one, only
# a full run exercises it.
EXTERNAL_READERS = (
    # cmd/migrate/*_test.go, the change-password regression and the
    # configuration documentation tests read the SQL files.
    (('migrations/',), ('cmd/migrate', 'internal/service', 'internal/config')),
    # internal/models/role_test.go and tests/users_me_password_test.go read the
    # generated reference; the route extractor is what the reference is checked with.
    (('api/',), ('internal/models', 'tests', 'scripts/ci/apiroutes')),
    # The extractor parses these sources (DefaultSources, InputSources).
    (('internal/handler/', 'internal/utils/', 'cmd/'), ('scripts/ci/apiroutes',)),
    # The gate that consumes the extractor.
    (('scripts/ci/check-api-docs.py',), ('scripts/ci/apiroutes',)),
    # cmd/lead_erasure_hooks_wiring_test.go walks both trees.
    (('internal/', 'cmd/'), ('cmd',)),
    # The configuration documentation tests quote these sources.
    (('internal/models/', 'internal/service/', 'internal/utils/', 'internal/repository/',
      'cmd/', 'scripts/release/'), ('internal/config',)),
    # scripts/release/dsn_test.go reads the release workflow.
    (('.github/workflows/release.yml',), ('scripts/release',)),
)


def documentation(path):
    return path.endswith('.md') or path.startswith('docs/') or path == 'LICENSE'


def owner(path, packages):
    """The nearest directory, the file's own included, that is a package."""
    for folder in PurePosixPath(path).parents:
        if str(folder) in packages:
            return str(folder)
    return None


def importers(roots, imports):
    """The roots plus every package that reaches one of them through its imports."""
    reverse = {}
    for package, imported in imports.items():
        for target in imported:
            reverse.setdefault(target, set()).add(package)
    found, queue = set(roots), list(roots)
    while queue:
        for package in reverse.get(queue.pop(), ()):
            if package not in found:
                found.add(package)
                queue.append(package)
    return found


def select(paths, imports, packages):
    """Changed paths, the import graph and the package directories in; selection out.

    `imports` maps a package directory to the module package directories its
    sources and tests import. Returns (directories, reason): the sorted selected
    directories, and the reason when that is every package (None otherwise).
    """
    everything = sorted(packages)
    if paths is None:
        return everything, 'the plan records no changed paths'
    paths = sorted(set(paths))
    if not paths:
        return everything, 'the change set is empty'
    for path in paths:
        if path in ('go.mod', 'go.sum'):
            return everything, f'{path} changed'
    code = [path for path in paths if not documentation(path)]
    for path in code:
        if path.endswith('.go') and str(PurePosixPath(path).parent) not in packages:
            return everything, f'{path} is not in a package at this head'
    owners, readers = set(), set()
    for path in code:
        package = owner(path, packages)
        matched = [names for prefixes, names in EXTERNAL_READERS if path.startswith(prefixes)]
        if package:
            owners.add(package)
        for names in matched:
            readers.update(names)
        if not package and not matched:
            readers.add(FALLBACK)
    return sorted((importers(owners, imports) | readers) & set(packages)), None


def report(selected, reason, total):
    """The lines that say what will be tested."""
    if reason:
        return [f'Go tests: every package ({reason})']
    if not selected:
        return ['Go tests: none selected (documentation-only change)']
    return [f'Go tests: {len(selected)} of {total} packages selected', *selected]


def commands(selected, universe, buildable, *, tests_only=False):
    """Every command of the run, in order. Nothing selected starts no test."""
    def targets(folders):
        return ['./' + folder for folder in folders]
    planned = []
    if not tests_only:
        planned.append(list(FORMAT_CHECK))
        planned.append(['go', 'build', *targets(buildable)])
        planned.append(['go', 'vet', *targets(universe)])
    if selected:
        test = ['go', 'test', '-count=1', '-timeout', '10m']
        planned.append([*test, *targets(selected)])
        tagged = [folder for folder in selected if folder in E2E_TAGGED]
        if tagged:
            planned.append([*test, '-tags=' + E2E_TAG, *targets(tagged)])
    return planned


def module_graph(root=ROOT, env=None):
    """(universe, buildable, imports) as repository-relative package directories."""
    output = subprocess.check_output(['go', 'list', '-e', '-json', './...'], cwd=root, env=env, text=True)
    decoder, position, listed = json.JSONDecoder(), 0, []
    while position < len(output):
        if output[position].isspace():
            position += 1
            continue
        package, position = decoder.raw_decode(output, position)
        listed.append(package)
    # The Makefile's GO_PKGS exclusion, plus Go sources kept under docs/.
    listed = [package for package in listed
              if not any(part in package['ImportPath'] + '/' for part in ('/gocrm-ui/', '/docs/'))]
    base = subprocess.check_output(['go', 'list', '-m', '-f', '{{.Dir}}'], cwd=root, env=env,
                                   text=True).strip()
    folders = {package['ImportPath']: PurePosixPath(package['Dir']).relative_to(base).as_posix()
               for package in listed}
    imports = {}
    for package in listed:
        named = (package.get('Imports', []) + package.get('TestImports', [])
                 + package.get('XTestImports', []))
        imports[folders[package['ImportPath']]] = {folders[name] for name in named if name in folders}
    universe = sorted(folders.values())
    buildable = sorted(folders[package['ImportPath']] for package in listed if package.get('GoFiles'))
    return universe, buildable, imports


def changed_since(revision, root=ROOT):
    output = subprocess.check_output(
        ['git', 'diff', '--no-renames', '--name-only', '-z', revision, 'HEAD'], cwd=root, text=True)
    return [path for path in output.split('\0') if path]


def run(paths, *, list_only=False, tests_only=False, root=ROOT):
    # Application connection settings belong to server processes; the Go
    # fixtures use TEST_MYSQL_DSN (see go_checks.run).
    env = verification_environment()
    universe, buildable, imports = module_graph(root, env)
    selected, reason = select(paths, imports, set(universe))
    for line in report(selected, reason, len(universe)):
        print(line, flush=True)
    if list_only:
        return
    for command in commands(selected, universe, buildable, tests_only=tests_only):
        print('+ ' + ' '.join(command), flush=True)
        subprocess.run(command, cwd=root, env=env, check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument('--plan', help="the plan job's validation-plan.json (reads changed_paths)")
    source.add_argument('--base', help='compare this revision with HEAD (committed changes only)')
    parser.add_argument('--list', action='store_true', help='print the selection and run nothing')
    parser.add_argument('--tests-only', action='store_true', help='skip the formatting check, the build and vet')
    args = parser.parse_args()
    if args.plan:
        with open(args.plan) as stream:
            paths = json.load(stream).get('changed_paths')
    else:
        paths = changed_since(args.base)
    try:
        run(paths, list_only=args.list, tests_only=args.tests_only)
    except subprocess.CalledProcessError as error:
        raise SystemExit(error.returncode or 1)


if __name__ == '__main__':
    main()
