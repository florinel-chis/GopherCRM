import json
from pathlib import Path
import sys
import tempfile
import unittest

from gophercrm_runner.config import CommandError
from gophercrm_runner.diagnostics import failure_summary
from gophercrm_runner.process import Commands
from gophercrm_runner.readiness import environment_failure


ASSERTION = ('--- FAIL: TestResetOwner (0.8s)\n'
             '    reset_test.go:40:\n'
             '        Error: Should be false\n'
             '        Messages: reset must not reactivate the disabled owner\n')
MOCK_LOG = json.dumps(dict(level='error', msg='Expected mocked lookup failure',
                          service='CustomerService', method='GetByID', error='connection refused')) + '\n'


class FailureDiagnosticsTests(unittest.TestCase):
    def test_early_assertion_survives_large_later_passing_test_output(self):
        stdout = ASSERTION + 'passing test output\n' * 3000 + MOCK_LOG
        summary = failure_summary(stdout, 'make: *** [verify-backend] Error 1\n')
        self.assertLessEqual(len(summary), 12000)
        self.assertIn('TestResetOwner', summary)
        self.assertIn('must not reactivate', summary)
        error = CommandError(summary, stdout=stdout, stderr='')
        self.assertFalse(environment_failure(error))
        for limit in (1, 20, 100):
            self.assertLessEqual(len(failure_summary(stdout, '', limit)), limit)

    def test_go_json_preserves_assertions_and_ignores_service_log_records(self):
        lines = ASSERTION.splitlines(keepends=True)
        # Real go test -json emits the assertion before the final FAIL event.
        output = ''.join(json.dumps(dict(Action='output', Package='example/service',
            Test='TestResetOwner', Output=line)) + '\n'
            for line in lines[1:] + [MOCK_LOG, lines[0]])
        output += json.dumps(dict(Action='fail', Package='example/service', Test='TestResetOwner')) + '\n'
        summary = failure_summary(output, '')
        self.assertIn('reset must not reactivate', summary)
        self.assertFalse(environment_failure(CommandError(summary, stdout=output, stderr='')))

    def test_real_environment_diagnostics_are_not_masked_by_go_failures(self):
        for diagnostic in ('dial tcp: connection refused', 'ENOSPC: no space left on device',
                           'JWT_SECRET must be at least 32 characters',
                           'Error: http://localhost:15177 is already used'):
            with self.subTest(diagnostic=diagnostic):
                stdout = ASSERTION + MOCK_LOG + '    Error: ' + diagnostic + '\n'
                self.assertTrue(environment_failure(CommandError('failed', stdout=stdout, stderr='')))
                self.assertTrue(environment_failure(CommandError('failed', stdout=ASSERTION, stderr=diagnostic)))

    def test_embedded_json_fixture_without_output_cannot_hide_real_plain_assertions(self):
        fixture = json.dumps(dict(Action='fail', Package='example/pkg', Test='TestFixture')) + '\n'
        stdout = fixture + ASSERTION + 'later passing output\n' * 3000 + MOCK_LOG
        summary = failure_summary(stdout, '')
        self.assertIn('must not reactivate', summary)
        self.assertFalse(environment_failure(CommandError(summary, stdout=stdout, stderr='')))

    def test_startup_unknown_logs_and_legacy_errors_keep_environment_detection(self):
        self.assertTrue(environment_failure(CommandError('failed', stdout=MOCK_LOG, stderr='')))
        unknown = json.dumps(dict(level='error', msg='connection refused'))
        self.assertTrue(environment_failure(CommandError('failed', stdout=ASSERTION + unknown, stderr='')))
        self.assertTrue(environment_failure(CommandError('connection refused')))
        self.assertFalse(environment_failure(CommandError('assertion failed')))


class ProcessFailureDiagnosticsTests(unittest.IsolatedAsyncioTestCase):
    async def test_real_failed_command_retains_complete_diagnostics_and_bounded_summary(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            stdout = ASSERTION + 'passing output\n' * 3000 + MOCK_LOG
            code = 'import sys; sys.stdout.write(' + repr(stdout) + '); sys.exit(1)'
            with self.assertRaises(CommandError) as caught:
                await Commands(root / 'logs').run((sys.executable, '-c', code), cwd=root)
            error = caught.exception
            self.assertEqual(error.stdout, stdout)
            self.assertIn('must not reactivate', str(error))
            self.assertLess(len(str(error)), 12500)
            self.assertFalse(environment_failure(error))
            logs = [p for p in (root / 'logs').glob('*.log') if not p.name.endswith('.stderr.log')]
            self.assertEqual(logs[0].read_text(), stdout)
