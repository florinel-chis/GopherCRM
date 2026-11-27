"""Tests for the docs folder rules of the pre-commit hook (scripts/git-hooks/pre-commit).

Each case builds a throw-away git repository holding a copy of the hook and of
the size check it calls, stages exactly one file and runs the hook. A file in
one of the documentation folders, or docs/README.md itself, is accepted; a file
in any other docs/ folder is refused, and docs/plans and docs/specs are refused
even when force-added past .gitignore.
"""

from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]
HOOK = ROOT / 'scripts' / 'git-hooks' / 'pre-commit'
SIZE_CHECK = ROOT / 'scripts' / 'ci' / 'check-file-sizes.sh'

ALLOWED_FOLDERS = (
    'administration', 'assets', 'development', 'integrations', 'maintainers',
    'operations', 'reference', 'testing', 'user-guide',
)


class PreCommitDocsFoldersTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.repo = Path(self.tmp.name)
        subprocess.run(['git', 'init', '-q', str(self.repo)], check=True)
        for source, rel in ((HOOK, 'scripts/git-hooks/pre-commit'), (SIZE_CHECK, 'scripts/ci/check-file-sizes.sh')):
            target = self.repo / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, target)
            target.chmod(0o755)
        (self.repo / '.gitignore').write_text('docs/plans/\ndocs/specs/\n', encoding='utf-8')

    def tearDown(self):
        self.tmp.cleanup()

    def run_hook(self, rel, force=False):
        # Start every case from an empty index, so one case cannot leak into the next.
        subprocess.run(['git', 'rm', '-r', '-q', '--cached', '--ignore-unmatch', '.'], cwd=self.repo, check=True)
        path = self.repo / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text('# %s\n' % rel, encoding='utf-8')
        add = ['git', 'add'] + (['-f'] if force else []) + ['--', rel]
        subprocess.run(add, cwd=self.repo, check=True)
        staged = subprocess.run(['git', 'diff', '--cached', '--name-only'], cwd=self.repo, check=True,
                                capture_output=True, text=True).stdout.split()
        self.assertEqual(staged, [rel], 'exactly the one file is staged')
        return subprocess.run(['sh', 'scripts/git-hooks/pre-commit'], cwd=self.repo,
                              capture_output=True, text=True)

    def test_allowed_folders_are_the_documentation_folders(self):
        line = next(l for l in HOOK.read_text(encoding='utf-8').splitlines() if l.startswith('ALLOWED_DOCS_DIRS='))
        self.assertEqual(sorted(line.split('=', 1)[1].strip('"').split()), sorted(ALLOWED_FOLDERS))

    def test_accepts_a_file_in_each_documentation_folder(self):
        for folder in ALLOWED_FOLDERS:
            with self.subTest(folder=folder):
                result = self.run_hook('docs/%s/x.md' % folder)
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_accepts_the_docs_index(self):
        result = self.run_hook('docs/README.md')
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_refuses_a_new_folder(self):
        for rel in ('docs/newarea/x.md', 'docs/img/x.png', 'docs/screenshots/a/x.png'):
            with self.subTest(path=rel):
                result = self.run_hook(rel)
                self.assertNotEqual(result.returncode, 0, result.stderr)
                self.assertIn("refusing '%s'" % rel, result.stderr)

    def test_refuses_force_added_working_papers(self):
        for rel in ('docs/plans/x.md', 'docs/specs/x.md'):
            with self.subTest(path=rel):
                result = self.run_hook(rel, force=True)
                self.assertNotEqual(result.returncode, 0, result.stderr)
                self.assertIn('docs/specs and docs/plans are local working papers', result.stderr)


if __name__ == '__main__':
    unittest.main()
