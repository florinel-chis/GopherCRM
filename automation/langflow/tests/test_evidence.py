import json
from pathlib import Path
import tempfile
import unittest

from gophercrm_runner.config import GateError


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
