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


if __name__ == '__main__':
    unittest.main()
