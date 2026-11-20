"""Tests for the documentation citation check (scripts/ci/check-doc-citations.py).

Each rule is shown to fail on a fixture repository (a real git repository, so
"tracked" means what `git ls-files` says) and to pass once the citation is
right; the converted guides must pass against the real repository, with the
Go symbol helper actually run.
"""

import contextlib
import importlib.util
import io
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]
spec_loader = importlib.util.spec_from_file_location('check_doc_citations', ROOT / 'scripts' / 'ci' / 'check-doc-citations.py')
gate = importlib.util.module_from_spec(spec_loader)
spec_loader.loader.exec_module(gate)

SAMPLE_GO = """package sample

type Widget struct{ Name string }

func (w *Widget) Run() {}

type Store interface{ Claim() error }

func Helper() {}

const (
	First  = 1
	Second = 2
)
"""

OTHER_GO = """package sample

func Elsewhere() {}
"""

# One valid citation of every kind; each failing test appends one bad line.
VALID_DOC = """# Fixture

Path first: `pkg/sample.go` `Widget.Run`. Symbol first: `Helper` (`pkg/sample.go`).
The receiver form `(*Widget).Run()` (`pkg/sample.go`), the package-qualified
`sample.Helper` (`pkg/sample.go`), the interface method `pkg/sample.go` `Store.Claim`,
the struct field `pkg/sample.go` `Widget.Name` and the grouped const `Second` (`pkg/sample.go`).
Paths: `./pkg/sample.go`, `pkg/*.go`, `pkg/<entity>.go` and the directory `pkg/`.
"""


def run(root, *files):
    out = io.StringIO()
    with contextlib.redirect_stdout(out):
        code = gate.main(['--repo-root', str(root), *files])
    return code, out.getvalue()


class DocCitationCheckTest(unittest.TestCase):
    def setUp(self):
        folder = tempfile.TemporaryDirectory()
        self.addCleanup(folder.cleanup)
        self.root = Path(folder.name).resolve()
        self.git('init', '-q')
        self.write('pkg/sample.go', SAMPLE_GO)
        self.write('pkg/other.go', OTHER_GO)

    def git(self, *args):
        subprocess.run(['git', *args], cwd=self.root, check=True, capture_output=True)

    def write(self, path, text, *, track=True):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text, encoding='utf-8')
        if track:
            self.git('add', '--', path)

    def write_doc(self, extra='', path='docs/FORKING.md'):
        self.write(path, VALID_DOC + extra)

    def assert_fails_naming(self, extra, span, kind):
        self.write_doc(extra)
        code, out = run(self.root, 'docs/FORKING.md')
        self.assertEqual(code, 1, out)
        self.assertIn('`%s`' % span, out)
        self.assertIn(kind, out)
        self.assertIn('docs/FORKING.md:', out)
        return out

    def test_valid_citation_passes(self):
        self.write_doc()
        code, out = run(self.root, 'docs/FORKING.md')
        self.assertEqual(code, 0, out)
        self.assertTrue(out.startswith('OK:'), out)

    def test_missing_file_fails(self):
        self.assert_fails_naming('See `pkg/absent.go`.\n', 'pkg/absent.go', 'missing path')

    def test_symbol_not_declared_in_cited_file_fails(self):
        self.assert_fails_naming('Also `pkg/sample.go` `Widget.Stop`.\n', 'Widget.Stop',
                                 'unknown symbol Widget.Stop in pkg/sample.go')

    def test_symbol_declared_only_in_another_file_fails(self):
        self.assert_fails_naming('Also `pkg/sample.go` `Elsewhere`.\n', 'Elsewhere',
                                 'unknown symbol Elsewhere in pkg/sample.go')

    def test_line_citation_in_converted_file_fails(self):
        self.assert_fails_naming('Old style: `pkg/sample.go:3`.\n', 'pkg/sample.go:3', 'line citation')

    def test_line_citation_in_a_fenced_block_fails(self):
        self.assert_fails_naming('```\npkg/sample.go:3\n```\n', 'pkg/sample.go:3', 'line citation')

    def test_untracked_present_file_fails(self):
        self.write('pkg/local.go', 'package sample\n', track=False)
        self.assert_fails_naming('See `pkg/local.go`.\n', 'pkg/local.go', 'missing path')

    def test_a_bare_file_name_paired_with_a_symbol_fails(self):
        self.assert_fails_naming('Also `Helper` (`sample.go`).\n', 'sample.go', 'missing path')

    def test_ignored_output_passes(self):
        # bin/ and gocrm-ui/ are tracked top-level segments here, so the spans
        # are recognised as paths and only the allowlist lets them pass.
        self.write('bin/README', 'build outputs\n')
        self.write('gocrm-ui/package.json', '{}\n')
        self.write_doc('Build `bin/gophercrm`; configure `gocrm-ui/.env`.\n')
        self.assertFalse((self.root / 'bin/gophercrm').exists())
        code, out = run(self.root, 'docs/FORKING.md')
        self.assertEqual(code, 0, out)
        self.assertIn('3 skipped', out)  # the two outputs and the <entity> placeholder
        # Without the allowlist the same citations are missing paths.
        saved = gate.IGNORED_OUTPUTS
        gate.IGNORED_OUTPUTS = ()
        try:
            code, out = run(self.root, 'docs/FORKING.md')
        finally:
            gate.IGNORED_OUTPUTS = saved
        self.assertEqual(code, 1, out)
        self.assertIn('`bin/gophercrm`: missing path', out)
        self.assertIn('`gocrm-ui/.env`: missing path', out)

    def test_a_non_converted_file_may_cite_lines_but_paths_and_symbols_are_checked(self):
        self.write('docs/OTHER.md', 'Lines: `pkg/sample.go:3`.\n')
        code, out = run(self.root, 'docs/OTHER.md')
        self.assertEqual(code, 0, out)
        self.write('docs/OTHER.md', 'Lines: `pkg/sample.go:3`, `pkg/absent.go`, `pkg/sample.go` `Nope`.\n')
        code, out = run(self.root, 'docs/OTHER.md')
        self.assertEqual(code, 1, out)
        self.assertNotIn('line citation', out)
        self.assertIn('`pkg/absent.go`: missing path', out)
        self.assertIn('unknown symbol Nope in pkg/sample.go', out)

    def test_a_converted_file_without_citations_fails(self):
        self.write('docs/FORKING.md', '# Nothing cited\n')
        code, out = run(self.root, 'docs/FORKING.md')
        self.assertEqual(code, 1, out)
        self.assertIn('no path citation found', out)
        self.assertIn('no symbol citation found', out)

    def test_real_tree_passes(self):
        code, out = run(ROOT)
        self.assertEqual(code, 0, out)
        self.assertRegex(out, r'^OK: [1-9][0-9]* path citations, [1-9][0-9]* symbol citations')


if __name__ == '__main__':
    unittest.main()
