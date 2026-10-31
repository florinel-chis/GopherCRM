"""The database `make create-db` creates must be the one the server connects to by default (#226)."""

import os
from pathlib import Path
import re
import stat
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]
CLEANUP = ROOT / 'gocrm-ui' / 'cleanup-test-users.sh'
# A whole identifier: bare, or backtick-quoted with both quotes, followed by whitespace or end of line.
IDENT = r'(?:`(\w+)`|(\w+))(?=\s|;|$)'


def single(pattern, path):
    matches = re.findall(pattern, (ROOT / path).read_text(), re.MULTILINE)
    if len(matches) != 1:
        raise AssertionError(f'{path}: expected one match for {pattern!r}, found {matches}')
    match = matches[0]
    return ''.join(match) if isinstance(match, tuple) else match


def config_default():
    return single(r'getEnv\("DB_NAME", "([^"]+)"\)', 'internal/config/config.go')


class DatabaseNameTests(unittest.TestCase):
    def test_every_creation_and_connection_name_matches_the_config_default(self):
        config = config_default()
        names = {
            'scripts/create_database.sql': single(r'^CREATE DATABASE IF NOT EXISTS ' + IDENT, 'scripts/create_database.sql'),
            '.env.example': single(r'^DB_NAME=(\S+)$', '.env.example'),
            'docker-compose.yml MYSQL_DATABASE': single(r'^\s+MYSQL_DATABASE:\s*(\S+)$', 'docker-compose.yml'),
            'docker-compose.yml DB_NAME': single(r'^\s+DB_NAME:\s*(\S+)$', 'docker-compose.yml'),
        }
        for source, name in names.items():
            with self.subTest(source=source):
                self.assertEqual(name, config, f'{source} names {name!r}, internal/config/config.go defaults to {config!r}')

    def test_identifier_pattern_rejects_a_longer_name(self):
        for line in ('CREATE DATABASE IF NOT EXISTS `gocrm-fork` CHARACTER SET utf8mb4;',
                     'CREATE DATABASE IF NOT EXISTS gocrm-fork CHARACTER SET utf8mb4;',
                     'CREATE DATABASE IF NOT EXISTS `gocrm CHARACTER SET utf8mb4;'):
            with self.subTest(line=line):
                self.assertIsNone(re.match(r'^CREATE DATABASE IF NOT EXISTS ' + IDENT, line))


class CleanupScriptTests(unittest.TestCase):
    def run_cleanup(self, env_name):
        with tempfile.TemporaryDirectory() as folder:
            folder = Path(folder)
            calls = folder / 'calls'
            stub = folder / 'mysql'
            # One record per call, one argument per line, so argument boundaries survive.
            stub.write_text(f'#!/bin/sh\nprintf "%s\\n" "$@" --- >> "{calls}"\necho 0\n')
            stub.chmod(stub.stat().st_mode | stat.S_IEXEC)
            env = {**os.environ, 'PATH': f'{folder}{os.pathsep}{os.environ["PATH"]}'}
            env.pop('DB_NAME', None)
            if env_name is not None:
                env['DB_NAME'] = env_name
            subprocess.run(['bash', str(CLEANUP)], env=env, check=True, capture_output=True, text=True)
            records = calls.read_text().split('\n---\n')
            return [record.split('\n') for record in records if record]

    def assert_database(self, calls, expected):
        self.assertEqual(len(calls), 2)
        for call in calls:
            self.assertEqual(call[:3], ['-u', 'root', expected], call)

    def test_both_statements_use_the_config_default(self):
        self.assert_database(self.run_cleanup(None), config_default())

    def test_both_statements_follow_db_name(self):
        self.assert_database(self.run_cleanup('crm_fork'), 'crm_fork')


if __name__ == '__main__':
    unittest.main()
