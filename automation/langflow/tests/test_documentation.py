from copy import deepcopy
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

from gophercrm_runner import documentation as docs
from gophercrm_runner.config import GateError
from gophercrm_runner.evidence import review_coverage


class DocumentationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.git('init', '-b', 'main')
        self.git('config', 'user.email', 'docs@example.test')
        self.git('config', 'user.name', 'Docs Test')
        (self.root / 'README.md').write_text('Current documentation\n')
        self.base = self.save('base')

    def tearDown(self):
        self.temp.cleanup()

    def git(self, *args):
        return subprocess.check_output(['git', *args], cwd=self.root,
            stderr=subprocess.DEVNULL, text=True).strip()

    def save(self, message):
        self.git('add', '.')
        self.git('commit', '-m', message)
        return self.git('rev-parse', 'HEAD')

    def candidate(self):
        path = self.root / 'internal/service/customer_service.go'
        path.parent.mkdir(parents=True)
        path.write_text('package service\n')
        (self.root / 'README.md').write_text('Documented changed behavior\n')
        return docs.plan(self.root, self.base, self.save('candidate'))

    @staticmethod
    def report(selected):
        return [dict(path=d['path'], disposition='updated' if d['changed'] else 'no-impact',
                     reason='Assessed this document against the scoped change.') for d in selected['documents']]

    def test_impact_unions_features_workflow_and_changed_documents(self):
        selected = docs.suggestions(['internal/service/customer_service.go', 'migrations/new.sql',
            '.github/workflows/ci.yml', 'docs/NEW.md'])
        paths = {d['path'] for d in selected}
        self.assertTrue({'docs/FEATURES.md', 'docs/ROADMAP.md', 'docs/testing/04-customers.md',
            'docs/datamodel.md', 'docs/SETUP.md', 'docs/NEW.md', 'docs/DEVELOPMENT.md',
            'automation/langflow/README.md', 'docs/VALIDATION_RELEASE.md'}.issubset(paths))
        self.assertEqual(selected, docs.suggestions(list(reversed([
            'internal/service/customer_service.go', 'migrations/new.sql', '.github/workflows/ci.yml', 'docs/NEW.md']))))
        self.assertIn('docs/DEVELOPMENT.md', {d['path'] for d in docs.suggestions(['unknown.bin'])})

    def test_actual_updates_and_specific_no_impact_are_accepted(self):
        selected = self.candidate()
        docs.review_coverage(self.report(selected), selected, True)
        readme = next(d for d in selected['documents'] if d['path'] == 'README.md')
        self.assertTrue(readme['changed'])
        self.assertNotEqual(readme['base_blob'], readme['head_blob'])

    def test_missing_duplicate_unknown_and_unexplained_claims_fail(self):
        selected = self.candidate()
        report = self.report(selected)
        for wrong in [None, report[:-1], report + [report[0]],
                      [dict(report[0], path='docs/unknown.md')] + report[1:],
                      [dict(report[0], reason=' ')] + report[1:],
                      [dict(report[0], disposition=[])] + report[1:]]:
            with self.subTest(wrong=wrong), self.assertRaises(GateError):
                docs.review_coverage(wrong, selected, True)

    def test_claims_cannot_invent_an_update_or_ignore_a_real_change(self):
        selected = self.candidate()
        for document in selected['documents']:
            report = self.report(selected)
            report[next(i for i, d in enumerate(report) if d['path'] == document['path'])]['disposition'] = (
                'no-impact' if document['changed'] else 'updated')
            with self.assertRaises(GateError):
                docs.review_coverage(report, selected, True)
        report = self.report(selected)
        missing = next(i for i, d in enumerate(selected['documents']) if d['head_blob'] is None)
        report[missing]['disposition'] = 'already-accurate'
        with self.assertRaises(GateError):
            docs.review_coverage(report, selected, True)

    def test_gap_is_a_repairable_rejection_never_an_approval(self):
        selected = self.candidate()
        report = self.report(selected)
        report[0].update(disposition='needs-update', reason='Description still states the old behavior.')
        verdict = dict(approved=False, blocking_findings=['Update stale description.'], summary='Reviewed',
                       criteria=[], documentation=report)
        review_coverage(verdict, [], documentation_plan=selected)
        with self.assertRaises(GateError):
            review_coverage(dict(verdict, approved=True, blocking_findings=[]), [], documentation_plan=selected)
        without_docs = deepcopy(verdict)
        del without_docs['documentation']
        with self.assertRaises(GateError):
            review_coverage(without_docs, [], documentation_plan=selected)

    def test_committed_removal_can_be_reviewed_as_an_update(self):
        (self.root / 'README.md').unlink()
        selected = docs.plan(self.root, self.base, self.save('remove obsolete document'))
        self.assertEqual(selected['documents'][0]['head_blob'], None)
        docs.review_coverage(self.report(selected), selected, True)

    def test_paths_and_symlink_documents_are_rejected(self):
        for path in ['../secret', '/secret', 'docs/a\nb.md', '.git/config', 'docs\\secret']:
            with self.assertRaises(GateError):
                docs.safe_path(path)
        (self.root / 'README.md').unlink()
        (self.root / 'README.md').symlink_to('/etc/passwd')
        with self.assertRaisesRegex(GateError, 'symlink'):
            docs.plan(self.root, self.base, self.save('unsafe link'))

    def test_merge_compares_reviewed_blobs_at_exact_merge_commit(self):
        selected = self.candidate()
        merge = self.git('commit-tree', self.git('rev-parse', 'HEAD^{tree}'), '-p', self.base, '-m', 'squash')
        receipt = docs.verify_merge(self.root, selected, merge)
        self.assertEqual(receipt['reviewed_head'], selected['head'])
        (self.root / 'README.md').write_text('Unexpected post-review edit\n')
        drift = self.save('drift')
        with self.assertRaisesRegex(GateError, 'differs'):
            docs.verify_merge(self.root, selected, drift)
        # Moving main never changes what the original merge proves.
        self.assertEqual(docs.verify_merge(self.root, selected, merge), receipt)


if __name__ == '__main__':
    unittest.main()
