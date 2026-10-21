import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).resolve().parents[1] / 'check-auth.py'
spec = importlib.util.spec_from_file_location('auth_checks', SCRIPT)
checks = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checks)


class AuthChecksTests(unittest.TestCase):
    def test_discovery_includes_new_roots_and_rejects_missing_sources(self):
        with tempfile.TemporaryDirectory() as folder, patch.dict(checks.SOURCES, {'pkg': ('auth*_test.go',)}, clear=True):
            root = Path(folder)
            (root / 'pkg').mkdir()
            first = root / 'pkg/auth_test.go'
            first.write_text('func TestExisting(t *testing.T) {}\n')
            second = root / 'pkg/auth_new_test.go'
            second.write_text('func TestNew(t *testing.T) {}\n')
            self.assertEqual(checks.discover(root), {'pkg': ['TestExisting', 'TestNew']})
            second.write_text('func TestExisting(t *testing.T) {}\n')
            with self.assertRaises(checks.GateError):
                checks.discover(root)
            second.unlink()
            second.symlink_to(first)
            with self.assertRaises(checks.GateError):
                checks.discover(root)
            second.unlink()
            first.unlink()
            with self.assertRaises(checks.GateError):
                checks.discover(root)
            first.write_text('package pkg\n')
            with self.assertRaises(checks.GateError):
                checks.discover(root)

    def test_runtime_rejects_missing_skipped_failed_and_duplicate_results(self):
        def event(action, name='TestLogin', package='example/pkg'):
            return json.dumps({'Package': package, 'Test': name, 'Action': action}) + '\n'
        passed = event('pass')
        for output, code, success in [
            (passed, 0, True), ('', 0, False), (event('skip'), 0, False),
            (event('skip', 'TestLogin/disabled') + passed, 0, False),
            (event('fail') + passed, 0, False), (passed + passed, 0, False),
            (event('pass', package='wrong/pkg'), 0, False), (passed, 1, False),
        ]:
            with self.subTest(output=output, code=code), \
                 patch.object(checks, 'discover', return_value={'pkg': ['TestLogin']}), \
                 patch.object(checks.subprocess, 'check_output', return_value='example/pkg\n'), \
                 patch.object(checks.subprocess, 'run', side_effect=[
                     subprocess.CompletedProcess([], 0), subprocess.CompletedProcess([], 0),
                     subprocess.CompletedProcess([], code, output, '')]):
                if success:
                    checks.run()
                else:
                    with self.assertRaises((checks.GateError, subprocess.CalledProcessError)):
                        checks.run()

    def test_real_selection_covers_auth_and_session_boundaries(self):
        selected = checks.discover()
        self.assertIn('TestAuthLockoutExpiryBoundary', selected['internal/service'])
        self.assertIn('TestAuthLoggingPrivacy_CredentialsAndTokens', selected['internal/service'])
        self.assertIn('TestAuthHandlerTestSuite', selected['internal/handler'])
        self.assertIn('TestAuthSessionIntegrationTestSuite', selected['tests'])
        self.assertIn('TestAuth_ExpiredBearerToken', selected['internal/middleware'])
        self.assertIn('TestAPIKeyOfAnErasedUserCannotAuthenticate', selected['test/integration'])
        self.assertIn('TestAPIKeyOfADeactivatedUserCannotAuthenticate', selected['test/integration'])
