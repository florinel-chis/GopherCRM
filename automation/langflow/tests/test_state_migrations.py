import json
from pathlib import Path
import tempfile
import unittest

from gophercrm_runner.config import GateError
from gophercrm_runner.state import Store


class StateMigrationTests(unittest.TestCase):
    def test_legacy_backup_is_exact_and_immutable_across_reads(self):
        with tempfile.TemporaryDirectory() as root:
            store = Store(Path(root))
            legacy = dict(issue=95, stage='ci', repairs=3, pr=124, branch='automation/remediate-95')
            store.write('issue-95', legacy)
            original = (Path(root) / 'issue-95.json').read_bytes()
            record = store.journal(95)
            self.assertTrue(record['legacy'])
            self.assertEqual(record['schema_version'], 2)
            self.assertEqual(record['repairs'], 3)
            backup = Path(record['legacy_backup'])
            self.assertEqual(backup.read_bytes(), original)
            self.assertEqual(backup.stat().st_mode & 0o777, 0o600)
            self.assertEqual(store.journal(95), record)
            self.assertEqual(backup.read_bytes(), original)

    def test_unknown_newer_schema_is_refused_without_overwrite(self):
        with tempfile.TemporaryDirectory() as root:
            store = Store(Path(root))
            record = dict(schema_version=999, issue=95, stage='ci')
            store.write('issue-95', record)
            with self.assertRaisesRegex(GateError, 'newer|matching runner'):
                store.journal(95)
            self.assertEqual(store.read('issue-95'), record)
