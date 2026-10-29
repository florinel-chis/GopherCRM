import json
from pathlib import Path
import tempfile
import unittest

from gophercrm_runner.config import CommandError, GateError


class EvidenceTests(unittest.TestCase):
    def test_named_go_tests_require_exact_package_and_non_skipped_success(self):
        from gophercrm_runner.evidence import named_go_tests
        tests = [{'package': 'example/repo/internal/integration', 'name': 'TestRegression'}]
        event = dict(Package=tests[0]['package'], Test='TestRegression', Action='pass')
        named_go_tests(json.dumps(event), tests)
        for output in ['', 'All checks passed', json.dumps(dict(event, Action='skip')),
                       json.dumps(dict(event, Package='wrong/package')),
                       json.dumps(event) + '\n' + json.dumps(dict(event, Action='fail'))]:
            with self.subTest(output=output), self.assertRaises(GateError):
                named_go_tests(output, tests)

    def test_receipts_bind_all_inputs_and_reject_changed_output_and_symlinks(self):
        from gophercrm_runner.evidence import write_receipt, verify_receipt
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            record = dict(execution_id='a' * 32, head='b' * 40, base='c' * 40,
                          policy='policy', contract_hash='contract', context_hash='context',
                          validation_attempt='d' * 32)
            receipt = write_receipt(root, record, 'regression', ['go', 'test'], 'passed', 0)
            verify_receipt(root, record, receipt, 'regression', ['go', 'test'])
            for key in ['execution_id', 'head', 'base', 'policy', 'contract_hash', 'context_hash', 'validation_attempt']:
                with self.subTest(key=key), self.assertRaises(GateError):
                    verify_receipt(root, dict(record, **{key: 'stale'}), receipt, 'regression', ['go', 'test'])
            path = root / receipt['path']
            raw = json.loads(path.read_text())
            output = root / raw['output_path']
            output.write_text('tampered')
            with self.assertRaises(GateError):
                verify_receipt(root, record, receipt, 'regression', ['go', 'test'])
            receipt = write_receipt(root, record, 'regression', ['go', 'test'], 'passed', 0)
            output.unlink(); output.symlink_to(root / receipt['path'])
            with self.assertRaises(GateError):
                verify_receipt(root, record, receipt, 'regression', ['go', 'test'])

    def test_reviewer_requires_exact_criterion_coverage_and_matching_receipts(self):
        from gophercrm_runner.evidence import review_coverage
        criteria = [dict(id='AC-1', verification=dict(check_id='regression'))]
        verdict = dict(approved=True, blocking_findings=[], summary='Reviewed',
            criteria=[dict(id='AC-1', passed=True, evidence=['acceptance-regression'], finding='')])
        review_coverage(verdict, criteria)
        for wrong in [dict(verdict, criteria=[]), dict(verdict, criteria=verdict['criteria'] * 2),
                      dict(verdict, criteria=[dict(verdict['criteria'][0], id='unknown')]),
                      dict(verdict, criteria=[dict(verdict['criteria'][0], evidence=['generic'])]),
                      dict(verdict, criteria=[dict(verdict['criteria'][0], passed=False, finding='Bug')])]:
            with self.assertRaises(GateError):
                review_coverage(wrong, criteria)


REPORT = Path(__file__).with_name('vitest-report.json')
CHIP = 'src/pages/deals/DealStageChip.test.tsx'
COLORS = 'src/pages/deals/dealStageColors.test.ts'
QUALIFICATION = 'DealStageChip labels \'qualification\' as "\'Qualification\'"'
PALETTE = 'dealStagePaletteColor covers every stage in the one colour map'


class VitestEvidenceTests(unittest.TestCase):
    """The parser is exercised against a captured `--reporter=json` report."""

    def report(self):
        return json.loads(REPORT.read_text())

    def identities(self):
        return [{'file': CHIP, 'name': QUALIFICATION}, {'file': COLORS, 'name': PALETTE}]

    def assertion(self, report, file, name):
        for result in report['testResults']:
            if result['name'].endswith('/gocrm-ui/' + file):
                for item in result['assertionResults']:
                    if item['fullName'] == name:
                        return result, item
        raise AssertionError('fixture lacks ' + name)

    def test_captured_report_passes_named_identities(self):
        from gophercrm_runner.evidence import named_vitest_tests
        named_vitest_tests(REPORT.read_text(), self.identities())

    def test_leading_banner_lines_are_tolerated(self):
        from gophercrm_runner.evidence import named_vitest_tests
        banner = '\n> gophercrm-ui@1.2.0 test\n> vitest --run --reporter=json src/x.test.ts\n\n'
        named_vitest_tests(banner + json.dumps(self.report()), self.identities())

    def test_failed_skipped_pending_or_todo_identity_fails(self):
        from gophercrm_runner.evidence import named_vitest_tests
        for status in ['failed', 'skipped', 'pending', 'todo', 'disabled']:
            report = self.report()
            self.assertion(report, CHIP, QUALIFICATION)[1]['status'] = status
            with self.subTest(status=status), self.assertRaisesRegex(CommandError, 'failed or was skipped'):
                named_vitest_tests(json.dumps(report), self.identities())

    def test_failed_required_file_fails_even_when_its_named_case_passed(self):
        from gophercrm_runner.evidence import named_vitest_tests
        report = self.report()
        self.assertion(report, CHIP, QUALIFICATION)[0]['status'] = 'failed'
        with self.assertRaisesRegex(CommandError, 'failed or was skipped'):
            named_vitest_tests(json.dumps(report), self.identities())

    def test_missing_file_or_case_fails(self):
        from gophercrm_runner.evidence import named_vitest_tests
        report = self.report()
        report['testResults'] = [r for r in report['testResults'] if not r['name'].endswith(COLORS)]
        with self.assertRaisesRegex(CommandError, 'reported no results'):
            named_vitest_tests(json.dumps(report), self.identities())
        report = self.report()
        self.assertion(report, COLORS, PALETTE)[0]['assertionResults'] = []
        with self.assertRaisesRegex(CommandError, 'reported no results'):
            named_vitest_tests(json.dumps(report), self.identities())
        report = self.report()
        self.assertion(report, COLORS, PALETTE)[1]['fullName'] = PALETTE + ' (renamed)'
        with self.assertRaisesRegex(CommandError, 'exact file/test passes'):
            named_vitest_tests(json.dumps(report), self.identities())
        # A same-named case in another file is not the required identity.
        with self.assertRaisesRegex(CommandError, 'exact file/test passes'):
            named_vitest_tests(REPORT.read_text(), [{'file': COLORS, 'name': QUALIFICATION}])

    def test_duplicate_results_fail(self):
        from gophercrm_runner.evidence import named_vitest_tests
        report = self.report()
        result, item = self.assertion(report, CHIP, QUALIFICATION)
        result['assertionResults'].append(dict(item))
        with self.assertRaisesRegex(CommandError, 'Duplicate'):
            named_vitest_tests(json.dumps(report), self.identities())
        report = self.report()
        report['testResults'].append(self.assertion(report, CHIP, QUALIFICATION)[0])
        with self.assertRaisesRegex(CommandError, 'Duplicate'):
            named_vitest_tests(json.dumps(report), self.identities())

    def test_absent_or_malformed_report_fails(self):
        from gophercrm_runner.evidence import named_vitest_tests
        for output in ['', 'All checks passed', '{"testResults": "none"}', '[]',
                       REPORT.read_text()[:200], json.dumps({'success': True}),
                       REPORT.read_text() + '\n' + REPORT.read_text()]:
            with self.subTest(output=output[:40]), self.assertRaises(CommandError):
                named_vitest_tests(output, self.identities())

    def test_checkout_root_requires_the_exact_result_path(self):
        from gophercrm_runner.evidence import named_tests, named_vitest_tests, vitest_identities
        root = '/runner-state/validation/96-0123456789ab'
        named_vitest_tests(REPORT.read_text(), self.identities(), root=root)
        named_vitest_tests(REPORT.read_text(), self.identities(), root=Path(root + '/'))
        named_tests(REPORT.read_text(), vitest_identities(self.identities()), root=root)
        with self.assertRaisesRegex(CommandError, 'reported no results'):
            named_vitest_tests(REPORT.read_text(), self.identities(), root='/runner-state/validation/other')
        # A nested copy whose path merely ends in gocrm-ui/<file> is not the checkout's file.
        report = self.report()
        for result in report['testResults']:
            result['name'] = result['name'].replace(root, root + '/gocrm-ui/vendor')
        named_vitest_tests(json.dumps(report), self.identities())
        with self.assertRaisesRegex(CommandError, 'reported no results'):
            named_vitest_tests(json.dumps(report), self.identities(), root=root)

    def test_named_tests_dispatches_by_declared_kind(self):
        from gophercrm_runner.evidence import named_tests, vitest_identities
        named_tests(REPORT.read_text(), vitest_identities(self.identities()))
        go = [{'package': 'example/repo/feature', 'name': 'TestFeature'}]
        named_tests(json.dumps(dict(Package='example/repo/feature', Test='TestFeature', Action='pass')), go)
        with self.assertRaises(CommandError):
            named_tests(REPORT.read_text(), go)
        # The declared kind, not the identity shape, selects the parser; a mismatch is refused.
        with self.assertRaisesRegex(GateError, 'identities'):
            named_tests(REPORT.read_text(), self.identities())
        with self.assertRaisesRegex(GateError, 'identities'):
            named_tests(REPORT.read_text(), vitest_identities(go))
