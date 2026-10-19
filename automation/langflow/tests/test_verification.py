from pathlib import Path
import subprocess
import sys
import unittest


class VerificationTests(unittest.TestCase):
    def test_local_and_ci_use_the_same_runner_target_without_rewriting_export(self):
        root = Path(__file__).resolve().parents[3]
        result = subprocess.run(['make', '-n', 'verify-runner', f'RUNNER_PYTHON={sys.executable}'],
                                cwd=root, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('unittest discover -s automation/langflow/tests -v', result.stdout)
        self.assertIn('gophercrm_runner.verify_export', result.stdout)
        self.assertIn('go test ./automation/langflow/probes/mysql', result.stdout)
        workflow = (root / '.github/workflows/remediation-runner.yml').read_text()
        self.assertIn('run: make verify-runner', workflow)
        self.assertIn('merge_group:', workflow)
        self.assertIn("- 'Makefile'", workflow)
        self.assertNotIn('run: python -m unittest', workflow)
        frontend = subprocess.run(['make', '-n', 'verify-frontend'], cwd=root, capture_output=True, text=True, check=True)
        self.assertEqual(frontend.stdout.count('npm ci'), 1)
        self.assertLess(frontend.stdout.index('npm ci'), frontend.stdout.index('npm run build'))
