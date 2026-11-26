"""Tests for the documentation citation check (scripts/ci/check-doc-citations.py).

Each rule is shown to fail on a fixture repository (a real git repository, so
"tracked" means what `git ls-files` says) and to pass once the citation is
right; every tracked doc the check discovers must pass against the real
repository, with the Go symbol helper actually run.
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

    def test_symbol_first_pair_after_another_span_is_checked(self):
        # `x` (`Widget.Stop` ... must not swallow the real pair `Widget.Stop` (`pkg/sample.go`).
        self.assert_fails_naming('A `x` (`Widget.Stop` (`pkg/sample.go`) case.\n', 'Widget.Stop',
                                 'unknown symbol Widget.Stop in pkg/sample.go')

    def test_path_first_pair_after_another_span_is_checked(self):
        self.assert_fails_naming('A `x` `pkg/sample.go` `Widget.Stop` case.\n', 'Widget.Stop',
                                 'unknown symbol Widget.Stop in pkg/sample.go')

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

    def test_a_non_converted_file_is_checked_by_every_rule(self):
        self.write('docs/OTHER.md', 'Lines: `pkg/sample.go:3`, `pkg/absent.go`, `pkg/sample.go` `Nope`.\n')
        code, out = run(self.root, 'docs/OTHER.md')
        self.assertEqual(code, 1, out)
        self.assertIn('docs/OTHER.md:1: `pkg/sample.go:3`: line citation', out)
        self.assertIn('`pkg/absent.go`: missing path', out)
        self.assertIn('unknown symbol Nope in pkg/sample.go', out)

    def test_line_citation_in_an_unlisted_doc_fails_by_default(self):
        self.write('docs/NEW.md', 'See `pkg/sample.go:3`.\n')
        code, out = run(self.root)
        self.assertEqual(code, 1, out)
        self.assertIn('docs/NEW.md:', out)
        self.assertIn('line citation', out)

    def test_exempt_doc_may_cite_lines(self):
        # #743 converted docs/CONCURRENCY.md, the last exempt doc: a line
        # citation in it now fails the default run as in any other doc.
        self.write('docs/CONCURRENCY.md', 'See `pkg/sample.go:3` and `pkg/sample.go` `Nope`.\n')
        code, out = run(self.root)
        self.assertEqual(code, 1, out)
        self.assertIn('docs/CONCURRENCY.md:1: `pkg/sample.go:3`: line citation', out)
        self.assertIn('unknown symbol Nope', out)
        code, out = run(self.root, 'docs/CONCURRENCY.md')
        self.assertEqual(code, 1, out)
        self.assertIn('line citation', out)

    def test_default_files_are_every_tracked_doc(self):
        for path in ('README.md', 'docs/A.md', 'docs/testing/B.md', 'docs/CONCURRENCY.md',
                     'other/README.md', 'docs/notes.txt', 'internal/idkit/README.md', 'CHANGELOG.md'):
            self.write(path, '# doc\n')
        self.write('docs/X.md', '# untracked\n', track=False)
        self.assertEqual(sorted(gate.default_files(self.root)),
                         ['README.md', 'docs/A.md', 'docs/CONCURRENCY.md', 'docs/testing/B.md',
                          'internal/idkit/README.md'])
        self.assertEqual(gate.EXEMPT_FILES, {})

    def test_untracked_extra_default_file_is_not_checked(self):
        self.write('internal/idkit/README.md', 'See `pkg/absent.go`.\n', track=False)
        self.assertNotIn('internal/idkit/README.md', gate.default_files(self.root))

    def test_missing_path_in_idkit_readme_fails_default_run(self):
        self.write('docs/A.md', 'See `pkg/sample.go`.\n')
        self.write('internal/idkit/README.md', 'See `pkg/absent.go`.\n')
        code, out = run(self.root)
        self.assertEqual(code, 1, out)
        self.assertIn('internal/idkit/README.md:1: `pkg/absent.go`: missing path', out)
        self.write('internal/idkit/README.md', 'See `pkg/sample.go`.\n')
        code, out = run(self.root)
        self.assertEqual(code, 0, out)
        self.assertIn('in 2 files', out)

    def test_extra_default_files_are_tracked(self):
        tracked = set(gate.tracked_files(ROOT))
        self.assertTrue(gate.EXTRA_DEFAULT_FILES)
        for doc in gate.EXTRA_DEFAULT_FILES:
            self.assertIn(doc, tracked, doc)
            self.assertNotIn(doc, gate.EXEMPT_FILES, doc)
        self.assertNotIn('CHANGELOG.md', gate.EXTRA_DEFAULT_FILES)

    def test_exemptions_name_a_test_that_reads_the_doc(self):
        tracked = set(gate.tracked_files(ROOT))
        for doc, test in gate.EXEMPT_FILES.items():
            self.assertIn(doc, tracked, doc)
            self.assertIn(test, tracked, test)
            self.assertIn(doc, (ROOT / test).read_text(encoding='utf-8'), test)

    def test_catalog_planned_spec_is_skipped_but_pairs_are_checked(self):
        self.write('gocrm-ui/package.json', '{}\n')
        self.write('docs/testing/X.md', 'Planned: `gocrm-ui/e2e/tests/planned.spec.ts`.\n')
        code, out = run(self.root)
        self.assertEqual(code, 0, out)
        self.assertIn('1 skipped', out)
        self.write('docs/testing/X.md', 'Planned: `gocrm-ui/e2e/tests/planned.spec.ts`; `pkg/sample.go` `Nope`.\n')
        code, out = run(self.root)
        self.assertEqual(code, 1, out)
        self.assertNotIn('planned.spec.ts', out)
        self.assertIn('docs/testing/X.md:1: `Nope`: unknown symbol Nope in pkg/sample.go', out)

    def test_missing_non_spec_path_in_catalog_fails(self):
        self.write('docs/testing/X.md', 'See `pkg/absent.go`.\n')
        code, out = run(self.root)
        self.assertEqual(code, 1, out)
        self.assertIn('docs/testing/X.md:1: `pkg/absent.go`: missing path', out)

    def test_planned_spec_outside_catalog_fails(self):
        self.write('gocrm-ui/package.json', '{}\n')
        self.write('docs/X.md', 'Planned: `gocrm-ui/e2e/tests/planned.spec.ts`.\n')
        code, out = run(self.root)
        self.assertEqual(code, 1, out)
        self.assertIn('docs/X.md:1: `gocrm-ui/e2e/tests/planned.spec.ts`: missing path', out)

    def test_missing_path_in_development_guide_fails(self):
        self.write('docs/DEVELOPMENT.md', 'See `pkg/absent.go`.\n')
        code, out = run(self.root)
        self.assertEqual(code, 1, out)
        self.assertIn('docs/DEVELOPMENT.md:1: `pkg/absent.go`: missing path', out)

    def test_local_directories_are_ignored(self):
        # docs/ and gocrm-ui/ are tracked top-level segments, so the spans are
        # path candidates and only IGNORED_OUTPUTS lets them pass.
        self.write('gocrm-ui/package.json', '{}\n')
        self.write('docs/LOCAL.md', 'Local: `docs/specs/`, `docs/plans/x.md`, `gocrm-ui/node_modules` '
                                    'and `gocrm-ui/node_modules/x`.\n')
        code, out = run(self.root)
        self.assertEqual(code, 0, out)
        self.assertIn('4 skipped', out)
        # The match is by whole path segments, not a string prefix.
        self.write('docs/LOCAL.md', 'Not local: `gocrm-ui/node_modulesX` and `docs/specsfoo.md`.\n')
        code, out = run(self.root)
        self.assertEqual(code, 1, out)
        self.assertIn('`gocrm-ui/node_modulesX`: missing path', out)
        self.assertIn('`docs/specsfoo.md`: missing path', out)

    def test_a_converted_file_without_citations_fails(self):
        self.write('docs/FORKING.md', '# Nothing cited\n')
        code, out = run(self.root, 'docs/FORKING.md')
        self.assertEqual(code, 1, out)
        self.assertIn('no path citation found', out)
        self.assertIn('no symbol citation found', out)

    def test_converted_files_cover_every_converted_guide(self):
        # #487 converted the fork and extension guides, #488 the rest of the
        # guides that cited code by line and #743 docs/CONCURRENCY.md; #786
        # moved a README symbol citation to docs/KNOWN_LIMITATIONS.md. Each
        # must stay under rule c.
        expected = {
            'docs/FORKING.md', 'docs/EXTENDING.md', 'docs/USE_CASES.md',
            'docs/SERVER_PROVISIONING.md', 'README.md',
            'docs/FEATURES.md', 'docs/DOCKER.md', 'docs/datamodel.md', 'docs/SETUP.md',
            'docs/CONCURRENCY.md', 'docs/KNOWN_LIMITATIONS.md',
        }
        self.assertEqual(expected - set(gate.CONVERTED_FILES), set())
        for name in expected:
            self.assertTrue((ROOT / name).is_file(), name)

    # Rule d: every relative .md link and #anchor resolves. Each test fails on a
    # broken fixture and passes once the fixture is fixed.

    def assert_link_rule(self, broken, fixed, target, kind, links=1):
        out = self.assert_fails_naming(broken, target, kind)
        self.assertIn('docs/FORKING.md:%d:' % (VALID_DOC.count('\n') + 1 + broken.count('\n', 0, broken.index(target))), out)
        self.write_doc(fixed)
        code, out = run(self.root, 'docs/FORKING.md')
        self.assertEqual(code, 0, out)
        self.assertIn(', %d links,' % links, out)

    def test_link_to_a_missing_anchor_fails(self):
        self.write('docs/OTHER.md', '# Other\n\n## Present section\n')
        self.assert_link_rule('See [it](OTHER.md#absent-section).\n', 'See [it](OTHER.md#present-section).\n',
                              'OTHER.md#absent-section', 'missing anchor')

    def test_link_to_a_missing_file_fails(self):
        self.write('docs/OTHER.md', '# Other\n')
        self.assert_link_rule('See [it](ABSENT.md).\n', 'See [it](OTHER.md).\n', 'ABSENT.md', 'missing link target')

    def test_link_to_an_untracked_file_fails(self):
        self.write('docs/LOCAL.md', '# Local\n', track=False)
        self.write('README.md', '# Readme\n')
        self.assert_link_rule('See [it](LOCAL.md).\n', 'See [it](../README.md).\n', 'LOCAL.md', 'missing link target')

    def test_same_file_anchor_is_checked(self):
        self.assert_link_rule('See [above](#fixtures).\n', 'See [above](#fixture).\n', '#fixtures', 'missing anchor')

    def test_heading_punctuation_is_dropped_from_the_slug(self):
        self.write('docs/OTHER.md', '# Other\n\n### Outbound webhooks (#345)\n')
        self.assert_link_rule('See [it](OTHER.md#outbound-webhooks-(345)).\n',
                              'See [it](OTHER.md#outbound-webhooks-345).\n',
                              'OTHER.md#outbound-webhooks-(345', 'missing anchor')

    def test_a_repeated_heading_gets_a_numbered_slug(self):
        self.write('docs/OTHER.md', '# Other\n\n## Roots\n\n## Roots\n')
        self.assert_link_rule('See [it](OTHER.md#roots-2).\n', 'See [it](OTHER.md#roots-1).\n',
                              'OTHER.md#roots-2', 'missing anchor')

    def test_a_heading_inside_a_fenced_block_does_not_count(self):
        self.write('docs/OTHER.md', '# Other\n\n```sh\n# fenced\n```\n')
        self.assert_link_rule('See [it](OTHER.md#fenced).\n', 'See [it](OTHER.md#other).\n',
                              'OTHER.md#fenced', 'missing anchor')

    def test_a_link_inside_a_fenced_block_is_not_checked(self):
        self.assert_link_rule('See [it](ABSENT.md).\n',
                              '```markdown\nSee [it](ABSENT.md).\n```\n', 'ABSENT.md', 'missing link target',
                              links=0)

    def test_a_link_inside_a_heading_keeps_its_text_in_the_slug(self):
        self.write('docs/OTHER.md', '# Other\n\n### Audit *(details in the [API reference](api/index.html))*\n')
        self.assert_link_rule('See [it](OTHER.md#audit-details-in-the-api-referenceapiindexhtml).\n',
                              'See [it](OTHER.md#audit-details-in-the-api-reference).\n',
                              'OTHER.md#audit-details-in-the-api-referenceapiindexhtml', 'missing anchor')

    def test_a_titled_link_is_checked(self):
        self.write('docs/x.md', '# X\n\n## A\n')
        self.assert_link_rule('See [it](x.md#b "t").\n', 'See [it](x.md#a "t").\n', 'x.md#b', 'missing anchor')

    def test_real_tree_passes(self):
        code, out = run(ROOT)
        self.assertEqual(code, 0, out)
        self.assertRegex(out, r'^OK: [1-9][0-9]* path citations, [1-9][0-9]* symbol citations, [1-9][0-9]* links')


if __name__ == '__main__':
    unittest.main()
