import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]


class DocumentationCI(unittest.TestCase):
    def test_ci_retains_impact_and_does_not_claim_semantic_approval(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            repo = root / 'repository'
            repo.mkdir()
            def git(*args):
                return subprocess.check_output(['git', *args], cwd=repo,
                    stderr=subprocess.DEVNULL, text=True).strip()
            git('init', '-b', 'main')
            git('config', 'user.email', 'docs@example.test')
            git('config', 'user.name', 'Docs CI')
            (repo / 'README.md').write_text('Before\n')
            git('add', '.')
            git('commit', '-m', 'base')
            base = git('rev-parse', 'HEAD')
            (repo / 'README.md').write_text('After\n')
            git('commit', '-am', 'document behavior')
            head = git('rev-parse', 'HEAD')
            environment = dict(os.environ, RUNNER_TEMP=folder, GITHUB_STEP_SUMMARY=str(root / 'summary'))
            for compared_base, status in [(base, 'assessment-required'), (None, 'comparison-unavailable')]:
                (root / 'validation-plan.json').write_text(json.dumps(dict(base=compared_base, head=head)))
                subprocess.run([sys.executable, str(ROOT / 'scripts/ci/plan-documentation.py')],
                    cwd=repo, env=environment, check=True, capture_output=True)
                report = json.loads((root / 'documentation-impact.json').read_text())
                self.assertEqual(report['status'], status)
                self.assertFalse(report['semantic_approval'])
                self.assertEqual(report['head'], head)
                if compared_base:
                    self.assertEqual(report['documents'][0]['path'], 'README.md')
                    self.assertTrue(report['documents'][0]['changed'])
                else:
                    self.assertEqual(report['documents'], [])
            self.assertIn('Documentation impact', (root / 'summary').read_text())
            # Unassessable input must not discard validation or pretend docs passed.
            (repo / 'README.md').unlink()
            (repo / 'README.md').symlink_to('/etc/passwd')
            git('add', '.')
            git('commit', '-m', 'unassessable document')
            (root / 'validation-plan.json').write_text(json.dumps(dict(base=base, head=git('rev-parse', 'HEAD'))))
            subprocess.run([sys.executable, str(ROOT / 'scripts/ci/plan-documentation.py')],
                cwd=repo, env=environment, check=True, capture_output=True)
            report = json.loads((root / 'documentation-impact.json').read_text())
            self.assertEqual(report['status'], 'comparison-unavailable')
            self.assertIn('symlink', report['reason'])
            self.assertFalse(report['semantic_approval'])
            self.assertEqual(git('status', '--porcelain'), '')
