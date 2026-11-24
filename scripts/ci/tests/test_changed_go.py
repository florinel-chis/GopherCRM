import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[3]
SCRIPT = ROOT / 'scripts/ci/check-changed-go.py'
sys.path.insert(0, str(ROOT / 'scripts/ci'))
spec = importlib.util.spec_from_file_location('check_changed_go', SCRIPT)
changed = importlib.util.module_from_spec(spec)
spec.loader.exec_module(changed)

# A reduced copy of the module: package directory -> the module packages its
# sources and tests import. internal/integration reaches the handlers only
# through a test import; nothing imports scripts/ci/apiroutes, internal/config,
# scripts/release or the MySQL probe.
IMPORTS = {
    'automation/langflow/probes/mysql': set(),
    'cmd': {'internal/handler', 'internal/service', 'internal/aeo', 'internal/models'},
    'cmd/migrate': {'internal/models'},
    'internal/aeo': set(),
    'internal/config': set(),
    'internal/handler': {'internal/service', 'internal/utils', 'internal/middleware'},
    'internal/integration': {'internal/handler'},
    'internal/mailer': set(),
    'internal/middleware': {'internal/service'},
    'internal/models': set(),
    'internal/repository': {'internal/models'},
    'internal/service': {'internal/mailer', 'internal/repository', 'internal/aeo', 'internal/utils'},
    'internal/utils': set(),
    'scripts/ci/apiroutes': set(),
    'scripts/release': set(),
    'tests': {'internal/handler'},
}
PACKAGES = set(IMPORTS)
FORMAT = ['python3', 'scripts/ci/check-go-format.py']
MAILER = ['cmd', 'internal/handler', 'internal/integration', 'internal/mailer', 'internal/middleware',
          'internal/service', 'tests']


def select(*paths):
    return changed.select(list(paths), IMPORTS, PACKAGES)


class SelectionTests(unittest.TestCase):
    def test_a_package_selects_itself_and_its_transitive_importers(self):
        selected, reason = select('internal/mailer/smtp.go')
        self.assertIsNone(reason)
        self.assertEqual(selected, MAILER)
        # No table row applies to the mailer, and nothing it does not reach is added.
        for absent in ('scripts/ci/apiroutes', 'internal/config', 'internal/models', 'internal/repository',
                       'internal/aeo'):
            self.assertNotIn(absent, selected)
        # A leaf has no importers; the result does not depend on the order of the paths.
        self.assertEqual(select('automation/langflow/probes/mysql/main.go'),
                         (['automation/langflow/probes/mysql'], None))
        self.assertEqual(select('README.md', 'internal/mailer/smtp.go', 'internal/mailer/smtp.go'),
                         (MAILER, None))

    def test_a_test_only_import_edge_counts(self):
        # internal/integration imports the handlers only from its tests.
        self.assertIn('internal/integration', select('internal/middleware/auth.go')[0])
        without = dict(IMPORTS, **{'internal/integration': set()})
        self.assertNotIn('internal/integration',
                         changed.select(['internal/middleware/auth.go'], without, PACKAGES)[0])

    def test_documentation_selects_nothing(self):
        for paths in (['docs/DEVELOPMENT.md', 'CHANGELOG.md'], ['LICENSE'], ['docs/img/logo.png'],
                      ['internal/service/README.md'], ['automation/langflow/OPERATIONS.md']):
            with self.subTest(paths=paths):
                self.assertEqual(changed.select(paths, IMPORTS, PACKAGES), ([], None))

    def test_changes_that_cannot_be_narrowed_select_every_package(self):
        everything = sorted(PACKAGES)
        cases = {
            'go.mod changed': ['go.mod', 'docs/SETUP.md'],
            'go.sum changed': ['go.sum'],
            'the change set is empty': [],
            'the plan records no changed paths': None,
            # A package deleted by the change is not a package at this head.
            'internal/gone/gone.go is not in a package at this head': ['internal/gone/gone.go', 'README.md'],
        }
        for reason, paths in cases.items():
            with self.subTest(reason=reason):
                self.assertEqual(changed.select(paths, IMPORTS, PACKAGES), (everything, reason))

    def test_files_resolve_to_their_package_by_directory(self):
        # Invisible to an untagged `go list`, but the directory is a package.
        self.assertIn('internal/aeo', select('internal/aeo/tls_roots_e2e.go')[0])
        self.assertIn('internal/handler', select('internal/handler/assets/form_embed.js')[0])
        self.assertEqual(select('scripts/ci/apiroutes/testdata/routes.txt'), (['scripts/ci/apiroutes'], None))

    def test_packages_that_read_the_changed_files_are_selected(self):
        exact = {
            'migrations/20261120120000_x.up.sql': ['cmd/migrate', 'internal/config', 'internal/service'],
            'api/swagger.json': ['internal/models', 'scripts/ci/apiroutes', 'tests'],
            'api/index.html': ['internal/models', 'scripts/ci/apiroutes', 'tests'],
            'scripts/ci/check-api-docs.py': ['scripts/ci/apiroutes'],
            '.github/workflows/release.yml': ['scripts/release'],
        }
        for path, expected in exact.items():
            with self.subTest(path=path):
                self.assertEqual(select(path), (expected, None))
        contains = {
            'internal/handler/x.go': ('scripts/ci/apiroutes', 'cmd'),
            'internal/utils/x.go': ('scripts/ci/apiroutes', 'cmd', 'internal/config'),
            'cmd/main.go': ('scripts/ci/apiroutes', 'cmd', 'internal/config'),
            'cmd/migrate/main.go': ('scripts/ci/apiroutes', 'cmd', 'cmd/migrate'),
            'internal/service/x.go': ('internal/config', 'cmd'),
            'internal/models/x.go': ('internal/config', 'cmd'),
            'internal/repository/x.go': ('internal/config', 'cmd'),
            'internal/middleware/rate_limit.go': ('internal/middleware', 'internal/config'),
            # Its own package, and the configuration tests read the release scripts.
            'scripts/release/build.sh': ('scripts/release', 'internal/config'),
        }
        for path, expected in contains.items():
            selected, reason = select(path)
            with self.subTest(path=path):
                self.assertIsNone(reason)
                for package in expected:
                    self.assertIn(package, selected)
        # A reader is added as it is: its importers are not.
        self.assertNotIn('internal/handler', select('migrations/20261120120000_x.up.sql')[0])
        # The extractor tests run only for the sources and artifacts the reference is built from.
        for path in ('internal/service/x.go', 'internal/models/x.go', 'internal/mailer/smtp.go',
                     'internal/middleware/auth.go', '.env.example', 'migrations/x.up.sql'):
            with self.subTest(path=path):
                self.assertNotIn('scripts/ci/apiroutes', select(path)[0])
        self.assertNotIn('internal/config', select('internal/handler/x.go')[0])

    def test_a_change_outside_every_package_runs_the_configuration_tests(self):
        for path in ('.env.example', 'docker-compose.yml', 'Dockerfile', 'Makefile',
                     'gocrm-ui/src/App.tsx', 'scripts/deploy/receive-release.sh',
                     'scripts/ci/plan-validation.py', '.github/workflows/ci.yml',
                     'automation/langflow/gophercrm_runner/validation.py'):
            with self.subTest(path=path):
                self.assertEqual(select(path), (['internal/config'], None))
        # Next to a change that selects packages it is still added; documentation is not a reason for it.
        self.assertIn('internal/config', select('internal/mailer/smtp.go', '.env.example')[0])
        self.assertEqual(select('internal/mailer/smtp.go', 'docs/SETUP.md'), (MAILER, None))


class CommandTests(unittest.TestCase):
    UNIVERSE = sorted(PACKAGES)
    BUILDABLE = [name for name in sorted(PACKAGES) if name != 'tests']

    def planned(self, selected, **options):
        return changed.commands(selected, self.UNIVERSE, self.BUILDABLE, **options)

    def test_tests_are_uncached_bounded_and_never_use_the_race_detector(self):
        planned = self.planned(MAILER)
        # The formatting check comes first, before anything is built.
        self.assertEqual(planned[0], FORMAT)
        self.assertEqual(planned[1], ['go', 'build', *('./' + name for name in self.BUILDABLE)])
        self.assertEqual(planned[2], ['go', 'vet', *('./' + name for name in self.UNIVERSE)])
        self.assertEqual(planned[3], ['go', 'test', '-count=1', '-timeout', '10m', *('./' + name for name in MAILER)])
        self.assertEqual(planned[4], ['go', 'test', '-count=1', '-timeout', '10m', '-tags=gophercrm_e2e',
                                      './internal/middleware'])
        self.assertEqual(len(planned), 5)
        # The Database gates job (--tests-only) starts tests and nothing else.
        self.assertNotIn(FORMAT, self.planned(MAILER, tests_only=True))
        self.assertEqual({command[1] for command in self.planned(MAILER, tests_only=True)}, {'test'})
        for selected in (MAILER, self.UNIVERSE, ['internal/config'], []):
            for tests_only in (False, True):
                for command in self.planned(selected, tests_only=tests_only):
                    self.assertFalse([word for word in command if 'race' in word], command)
                    if command[1] == 'test':
                        self.assertEqual(command[2:5], ['-count=1', '-timeout', '10m'])

    def test_nothing_selected_starts_no_go_test(self):
        self.assertEqual(self.planned([])[0], FORMAT)
        self.assertEqual([command[1] for command in self.planned([])[1:]], ['build', 'vet'])
        self.assertEqual(self.planned([], tests_only=True), [])
        self.assertEqual(self.planned(['internal/config'], tests_only=True),
                         [['go', 'test', '-count=1', '-timeout', '10m', './internal/config']])

    def test_the_e2e_tag_run_covers_only_the_tagged_packages_that_were_selected(self):
        def tagged(selected):
            return [command for command in self.planned(selected, tests_only=True)
                    if '-tags=gophercrm_e2e' in command]
        self.assertEqual(tagged(['internal/config', 'internal/mailer']), [])
        self.assertEqual([command[6:] for command in tagged(['internal/aeo', 'internal/config'])],
                         [['./internal/aeo']])
        self.assertEqual([command[6:] for command in tagged(self.UNIVERSE)],
                         [['./internal/aeo', './internal/middleware']])
        # From a changed path to the tagged command.
        selected, _ = select('internal/aeo/tls_roots_e2e.go')
        self.assertEqual([command[6:] for command in tagged(selected)],
                         [['./internal/aeo', './internal/middleware']])

    def test_the_log_names_the_selection(self):
        self.assertEqual(changed.report(MAILER, None, 25), ['Go tests: 7 of 25 packages selected', *MAILER])
        self.assertEqual(changed.report([], None, 25), ['Go tests: none selected (documentation-only change)'])
        self.assertEqual(changed.report(self.UNIVERSE, 'go.mod changed', 25),
                         ['Go tests: every package (go.mod changed)'])


class RunTests(unittest.TestCase):
    """The script end to end, with the package graph and the Go processes replaced."""

    def run_with(self, paths, **options):
        graph = (sorted(PACKAGES), [name for name in sorted(PACKAGES) if name != 'tests'], IMPORTS)
        started, output = [], io.StringIO()

        def record(command, **settings):
            # Each command line is already in the log when its process starts.
            self.assertIn('+ ' + ' '.join(command) + '\n', output.getvalue())
            started.append((command, settings))
            return subprocess.CompletedProcess(command, 0)

        with patch.object(changed, 'module_graph', return_value=graph), \
                patch.object(changed.subprocess, 'run', side_effect=record), \
                contextlib.redirect_stdout(output):
            changed.run(paths, **options)
        return started, output.getvalue().splitlines()

    def test_every_command_is_printed_before_it_runs(self):
        started, lines = self.run_with(['.env.example'], tests_only=True)
        self.assertEqual([command for command, _ in started],
                         [['go', 'test', '-count=1', '-timeout', '10m', './internal/config']])
        self.assertEqual(lines, ['Go tests: 1 of 16 packages selected', 'internal/config',
                                 '+ go test -count=1 -timeout 10m ./internal/config'])
        started, lines = self.run_with(['internal/aeo/provider.go'])
        self.assertEqual(started[0][0], FORMAT)
        self.assertEqual([command[1] for command, _ in started[1:]], ['build', 'vet', 'test', 'test'])
        self.assertEqual([line.split()[:3] for line in lines if line.startswith('+ ')],
                         [['+', 'python3', 'scripts/ci/check-go-format.py'], ['+', 'go', 'build'], ['+', 'go', 'vet'],
                          ['+', 'go', 'test'], ['+', 'go', 'test']])
        # The check is started from the repository root, where its relative path resolves.
        self.assertEqual(started[0][1]['cwd'], ROOT)
        self.assertTrue((ROOT / FORMAT[1]).is_file())
        self.assertTrue(started[0][1]['check'])
        self.assertIn('-tags=gophercrm_e2e', lines[-1])

    def test_a_documentation_only_change_starts_no_go_test(self):
        started, lines = self.run_with(['docs/DEVELOPMENT.md', 'CHANGELOG.md'], tests_only=True)
        self.assertEqual(started, [])
        self.assertEqual(lines, ['Go tests: none selected (documentation-only change)'])
        started, lines = self.run_with(['docs/DEVELOPMENT.md', 'CHANGELOG.md'])
        # Formatting is checked on every run that builds, a documentation-only one included.
        self.assertEqual([command for command, _ in started][0], FORMAT)
        self.assertEqual([command[1] for command, _ in started[1:]], ['build', 'vet'])

    def test_a_formatting_failure_stops_the_run_before_the_build(self):
        graph = (sorted(PACKAGES), [name for name in sorted(PACKAGES) if name != 'tests'], IMPORTS)
        started = []

        def fail(command, **settings):
            started.append(command)
            raise subprocess.CalledProcessError(1, command)

        with patch.object(changed, 'module_graph', return_value=graph), \
                patch.object(changed.subprocess, 'run', side_effect=fail), \
                contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaises(subprocess.CalledProcessError):
                changed.run(['internal/aeo/provider.go'])
        self.assertEqual(started, [FORMAT])

    def test_a_plan_without_changed_paths_tests_every_package(self):
        started, lines = self.run_with(None, tests_only=True)
        self.assertEqual(lines[0], 'Go tests: every package (the plan records no changed paths)')
        self.assertEqual(started[0][0][5:], ['./' + name for name in sorted(PACKAGES)])

    def test_listing_runs_nothing(self):
        started, lines = self.run_with(['internal/mailer/smtp.go'], list_only=True)
        self.assertEqual(started, [])
        self.assertEqual(lines, ['Go tests: 7 of 16 packages selected', *MAILER])

    def test_go_processes_keep_the_fixture_dsn_and_drop_application_database_settings(self):
        environment = {'PATH': '/usr/bin', 'TEST_MYSQL_DSN': 'disposable-fixture', 'DB_HOST': 'application',
                       'DB_NAME': 'application'}
        with patch.dict(os.environ, environment, clear=True):
            started, _ = self.run_with(['.env.example'], tests_only=True)
        env = started[0][1]['env']
        self.assertEqual(env['TEST_MYSQL_DSN'], 'disposable-fixture')
        self.assertFalse([key for key in env if key.startswith('DB_')])
        self.assertTrue(started[0][1]['check'])


class CommandLineTests(unittest.TestCase):
    def script(self, *args):
        return subprocess.run([sys.executable, str(SCRIPT), *args], cwd=ROOT, capture_output=True, text=True)

    def test_exactly_one_source_of_changed_paths_is_required(self):
        self.assertNotEqual(self.script('--list').returncode, 0)
        self.assertNotEqual(self.script('--plan', 'x.json', '--base', 'HEAD', '--list').returncode, 0)

    def test_the_real_module_resolves_a_plan_file(self):
        with tempfile.TemporaryDirectory() as folder:
            plan = Path(folder) / 'validation-plan.json'
            plan.write_text(json.dumps({'profile': 'contextual', 'changed_paths': ['internal/mailer/smtp.go']}))
            result = self.script('--plan', str(plan), '--list')
            self.assertEqual(result.returncode, 0, result.stderr)
            lines = result.stdout.splitlines()
            self.assertRegex(lines[0], r'^Go tests: \d+ of \d+ packages selected$')
            self.assertEqual(lines[1:], sorted(lines[1:]))
            for package in ('cmd', 'internal/mailer', 'internal/service', 'tests'):
                self.assertIn(package, lines)
            for package in ('internal/config', 'internal/models', 'scripts/ci/apiroutes'):
                self.assertNotIn(package, lines)
            # Untracked Go sources under docs/ and the frontend tree are not part of the universe.
            self.assertFalse([line for line in lines[1:] if line.startswith(('docs/', 'gocrm-ui/'))])
            plan.write_text(json.dumps({'profile': 'bounded'}))
            result = self.script('--plan', str(plan), '--list')
            self.assertEqual(result.stdout, 'Go tests: every package (the plan records no changed paths)\n')

    def test_the_script_never_names_the_race_detector(self):
        self.assertNotIn('-race', SCRIPT.read_text())


if __name__ == '__main__':
    unittest.main()
