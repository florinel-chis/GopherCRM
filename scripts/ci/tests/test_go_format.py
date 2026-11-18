import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]
SCRIPT = ROOT / 'scripts/ci/check-go-format.py'
sys.path.insert(0, str(ROOT / 'scripts/ci'))
import go_checks

FORMATTED = 'package x\n\nfunc F() {}\n'
# Wrong under every gofmt version: doubled space, no blank line, no space before the body.
UNFORMATTED = 'package x\nfunc  F(){}\n'
# No package clause: gofmt cannot parse it.
UNPARSABLE = 'func F() {}\n'
HEADING = 'Go files that are not gofmt-formatted (run gofmt -w on them):'


class FormatCheckTests(unittest.TestCase):
    """The script against a real git repository and the real gofmt."""

    def setUp(self):
        folder = tempfile.TemporaryDirectory()
        self.addCleanup(folder.cleanup)
        self.root = Path(folder.name).resolve()
        self.git('init', '-q')

    def git(self, *args):
        subprocess.run(['git', *args], cwd=self.root, check=True, capture_output=True)

    def write(self, path, text, *, track=True):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text)
        if track:
            self.git('add', '--', path)

    def check(self):
        result = subprocess.run([sys.executable, str(SCRIPT), '--root', str(self.root)], cwd=ROOT,
                                capture_output=True, text=True)
        return result.returncode, (result.stdout + result.stderr).splitlines()

    def test_a_tracked_unformatted_file_is_named_and_fails_until_it_is_formatted(self):
        self.write('good/good.go', FORMATTED)
        self.write('bad/bad.go', UNFORMATTED)
        self.write('with space/also bad.go', UNFORMATTED)
        self.write('docs/local/untracked.go', UNFORMATTED, track=False)
        self.write('notes.txt', 'not Go\n')
        status, lines = self.check()
        self.assertEqual(status, 1)
        # The heading, then one repository-relative path per line, and nothing else.
        self.assertEqual(lines, [HEADING, 'bad/bad.go', 'with space/also bad.go'])
        subprocess.run([go_checks.gofmt_binary(), '-w', 'bad/bad.go', 'with space/also bad.go'], cwd=self.root,
                       check=True)
        self.assertEqual((self.root / 'bad/bad.go').read_text(), FORMATTED)
        self.assertEqual(self.check(), (0, []))
        # Still untracked and still unformatted: it was never read.
        self.assertEqual((self.root / 'docs/local/untracked.go').read_text(), UNFORMATTED)

    def test_a_clean_or_empty_tree_passes_without_output(self):
        self.assertEqual(self.check(), (0, []))
        self.write('good.go', FORMATTED)
        self.assertEqual(self.check(), (0, []))

    def test_a_tracked_file_deleted_from_the_working_tree_is_skipped(self):
        self.write('good.go', FORMATTED)
        self.write('gone.go', UNFORMATTED)
        (self.root / 'gone.go').unlink()
        self.assertEqual(self.check(), (0, []))

    def test_a_file_gofmt_cannot_parse_fails_with_its_report(self):
        self.write('good.go', FORMATTED)
        self.write('broken.go', UNPARSABLE)
        status, lines = self.check()
        self.assertEqual(status, 1)
        self.assertEqual(lines[0], 'gofmt failed:')
        self.assertTrue([line for line in lines[1:] if line.startswith('broken.go:')], lines)
        self.assertNotIn(HEADING, lines)

    def test_gofmt_is_found_next_to_go_when_it_is_not_on_the_path(self):
        go = shutil.which('go')
        with tempfile.TemporaryDirectory() as folder:
            # A PATH that holds `go` (and git) but no gofmt.
            for name, target in (('go', go), ('git', shutil.which('git'))):
                os.symlink(target, Path(folder) / name)
            environment = dict(os.environ, PATH=folder)
            self.write('bad.go', UNFORMATTED)
            result = subprocess.run([sys.executable, str(SCRIPT), '--root', str(self.root)], cwd=ROOT,
                                    capture_output=True, text=True, env=environment)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(result.stderr.splitlines(), [HEADING, 'bad.go'])


if __name__ == '__main__':
    unittest.main()
