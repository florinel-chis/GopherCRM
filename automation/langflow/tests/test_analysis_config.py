"""Analysis configuration fields stay outside the execution policy hash."""

from dataclasses import replace
import json
from pathlib import Path
import tempfile
import unittest

from gophercrm_runner.config import Config, GateError, validate_acceptance_checks
from gophercrm_runner.engine import Runner


def policy(root, **extra):
    checkout = root / 'checkout'
    (checkout / '.git').mkdir(parents=True, exist_ok=True)
    raw = dict(repository='example/repo', checkout=str(checkout), state_dir=str(root / 'state'), issues=[95])
    raw.update(extra)
    path = root / f'policy-{len(extra)}.json'
    path.write_text(json.dumps(raw))
    return path


class AnalysisConfigTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)

    def tearDown(self):
        self.temp.cleanup()

    def test_analysis_fields_have_defaults_and_are_loaded_as_tuples(self):
        config = Config.load(policy(self.root))
        self.assertEqual(config.analysis_output_dir, 'docs/specs')
        self.assertEqual(config.analysis_max_calls, 8)
        self.assertEqual(config.analysis_context_refs, ('AGENTS.md', 'docs/DEVELOPMENT.md'))
        self.assertEqual(config.analysis_rules_dir, '')
        loaded = Config.load(policy(self.root, analysis_context_refs=['AGENTS.md', 'README.md'],
                                    analysis_max_calls=3, analysis_output_dir='docs/plans',
                                    analysis_rules_dir=str(self.root / 'rules')))
        self.assertEqual(loaded.analysis_context_refs, ('AGENTS.md', 'README.md'))
        self.assertEqual((loaded.analysis_max_calls, loaded.analysis_output_dir), (3, 'docs/plans'))

    def test_policy_hash_ignores_analysis_fields(self):
        plain = Config.load(policy(self.root))
        tuned = replace(plain, analysis_output_dir='docs/plans', analysis_max_calls=2,
                        analysis_context_refs=('AGENTS.md',), analysis_rules_dir=str(self.root))
        self.assertEqual(Runner(plain).policy, Runner(tuned).policy)
        from gophercrm_runner.batches import policy_hash
        self.assertEqual(policy_hash(plain), policy_hash(tuned))

    def test_analysis_validation(self):
        plain = Config.load(policy(self.root))
        for field, value in [('analysis_output_dir', '/abs/specs'), ('analysis_output_dir', 'docs/../x'),
                             ('analysis_output_dir', ''), ('analysis_max_calls', 0),
                             ('analysis_context_refs', ('docs/DEVELOPMENT.md',)),
                             ('analysis_context_refs', ('AGENTS.md', 'notes.txt')),
                             ('analysis_context_refs', ()), ('analysis_rules_dir', 'relative/rules')]:
            with self.subTest(field=field, value=value):
                with self.assertRaises(GateError):
                    replace(plain, **{field: value}).validate()

    def test_validate_acceptance_checks_is_shared(self):
        registry = {'dash': {'argv': ['go', 'test', '-count=1', './internal'],
                             'tests': [{'package': 'example/repo/internal', 'name': 'TestDash'}]}}
        with self.assertRaisesRegex(GateError, 'uncached go test -json'):
            validate_acceptance_checks(registry)
        with self.assertRaisesRegex(GateError, 'uncached go test -json'):
            replace(Config.load(policy(self.root)), acceptance_checks=registry).validate()
        registry['dash']['argv'].insert(2, '-json')
        validate_acceptance_checks(registry)

    def test_check_kind_is_optional_and_go_by_default(self):
        go = {'argv': ['go', 'test', '-json', '-count=1', './internal'],
              'tests': [{'package': 'example/repo/internal', 'name': 'TestDash'}]}
        validate_acceptance_checks({'dash': go})
        validate_acceptance_checks({'dash': dict(go, kind='go')})
        for kind in ['vitest', 'jest', '', None]:
            with self.subTest(kind=kind), self.assertRaises(GateError):
                validate_acceptance_checks({'dash': dict(go, kind=kind)})

    def test_vitest_checks_require_the_exact_json_command_and_file_identities(self):
        prefix = ['npm', '--prefix', 'gocrm-ui', 'test', '--silent', '--', '--run', '--reporter=json']
        files = ['src/pages/deals/DealList.test.tsx', 'src/pages/deals/dealFormat.test.ts']
        check = {'kind': 'vitest', 'argv': prefix + files,
                 'tests': [{'file': files[0], 'name': 'DealList renders the empty state'},
                           {'file': files[1], 'name': 'formatAmount formats minor units'}]}
        validate_acceptance_checks({'ui-deals': check})
        invalid_argv = [
            prefix,  # No test file.
            prefix[:4] + prefix[5:] + files,  # npm banner lines are not suppressed.
            ['npx', 'vitest', '--run', '--reporter=json'] + files,
            prefix[:-1] + ['--reporter=verbose'] + files,
            prefix + files + ['--passWithNoTests'],
            prefix + ['src/pages/deals/DealList.tsx'],
            prefix + ['e2e/tests/login.spec.ts'],
            prefix + ['src/../../etc/passwd.test.ts'],
            prefix + ['/abs/src/a.test.ts'],
            prefix + [files[0], files[0]],
            ['go', 'test', '-json', '-count=1', './internal'],
        ]
        for argv in invalid_argv:
            with self.subTest(argv=argv), self.assertRaisesRegex(GateError, 'Vitest'):
                validate_acceptance_checks({'ui-deals': dict(check, argv=argv)})
        invalid_tests = [
            [],
            [{'file': 'src/pages/deals/Other.test.tsx', 'name': 'Other renders'}],
            [{'file': files[0], 'name': ''}],
            [{'file': files[0], 'name': '   '}],
            [{'file': files[0], 'name': 'a\x00b'}],
            [{'package': 'example/repo/internal', 'name': 'TestDash'}],
            [{'file': files[0], 'name': 'Same'}, {'file': files[0], 'name': 'Same'}],
        ]
        for tests in invalid_tests:
            with self.subTest(tests=tests), self.assertRaises(GateError):
                validate_acceptance_checks({'ui-deals': dict(check, tests=tests)})
        # A Go registry entry still refuses Vitest identities and the reverse.
        with self.assertRaises(GateError):
            validate_acceptance_checks({'ui-deals': dict(check, kind='go')})


if __name__ == '__main__':
    unittest.main()
