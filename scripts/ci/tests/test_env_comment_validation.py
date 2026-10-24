from pathlib import Path
import os
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'automation/langflow'))
from gophercrm_runner import validation as v


class EnvCommentSelectionTests(unittest.TestCase):
    def test_committed_comments_preserve_profiles_but_values_and_ambiguous_syntax_do_not(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            def git(*args):
                return subprocess.check_output(['git', *args], cwd=root, text=True,
                                               stderr=subprocess.DEVNULL).strip()
            git('init'); git('config', 'user.email', 'test@example.test'); git('config', 'user.name', 'Test')
            file = root / '.env.example'
            original = '# Schedule documentation\nDB_HOST=localhost\nJWT_SECRET=example-only\n# AEO_SCHEDULE_ENABLED=true\n'
            file.write_text(original)
            git('add', '.'); git('commit', '-m', 'base'); base = git('rev-parse', 'HEAD')
            def candidate(body):
                if isinstance(body, bytes):
                    file.write_bytes(body)
                else:
                    file.write_text(body)
                git('add', '.'); git('commit', '--allow-empty', '-m', 'candidate')
                return git('rev-parse', 'HEAD')
            changed = '\n# Updated scheduling explanation\nDB_HOST=localhost\n\nJWT_SECRET=example-only\n# AEO_SCHEDULE_ENABLED=true\n'
            head = candidate(changed)
            proof = v.candidate_scope(['.env.example'], base, head, cwd=root)
            self.assertTrue(proof['env_comments'])
            paths = ['.env.example', 'internal/aeo/engine.go']
            self.assertEqual(v.plan(paths, aeo_roots=True)['profile'], 'full')
            self.assertEqual(v.plan(paths, aeo_roots=True, env_comments=True)['profile'], 'aeo')
            self.assertEqual(v.plan(paths, full=True, aeo_roots=True, env_comments=True)['profile'], 'full')
            self.assertEqual(v.plan(['.env.example', 'scripts/ci/check-go.py'], **proof)['profile'], 'runner')
            self.assertEqual(v.plan(paths + ['internal/middleware/auth.go'], aeo_roots=True,
                                    env_comments=True)['profile'], 'full')
            self.assertEqual(v.plan(['gocrm-ui/.env.example'], env_comments=True)['profile'], 'full')
            for body in [changed.replace('localhost', 'remote'),
                         changed.replace('# AEO_SCHEDULE_ENABLED', 'AEO_SCHEDULE_ENABLED'),
                         changed.replace('DB_HOST=localhost\n', ''),
                         changed + 'NEW_SETTING=value\n',
                         changed + 'export NEW_SETTING=value\n',
                         changed + 'QUOTED="first\n# part of the value\nlast"\n',
                         changed + 'CONTINUED=first\\\n# still ambiguous\n',
                         changed + 'EXPANDED=$DB_HOST\n',
                         changed + '# comment\rDB_HOST=remote\n',
                         changed + '# comment\vDB_HOST=remote\n',
                         changed + '# comment\u2028DB_HOST=remote\n',
                         changed.replace('DB_HOST=localhost\n\nJWT_SECRET=example-only',
                                         'JWT_SECRET=example-only\n\nDB_HOST=localhost'),
                         changed.replace('DB_HOST=localhost', ' DB_HOST=localhost'),
                         changed.encode() + b'# invalid UTF-8: \xff\n']:
                with self.subTest(body=body):
                    rejected = candidate(body)
                    self.assertFalse(v.env_comments_only(base, rejected, cwd=root))
            # A comment-shaped line inside an unchanged multiline value is not
            # documentation; even changes to that line must retain full gates.
            multiline = 'VALUE="first\n# original value\nlast"\n'
            quoted_base = candidate(multiline)
            quoted_head = candidate(multiline.replace('# original', '# changed'))
            self.assertFalse(v.env_comments_only(quoted_base, quoted_head, cwd=root))
            head = candidate(changed)
            os.chmod(file, 0o755); git('add', '.'); git('commit', '-m', 'executable')
            self.assertFalse(v.env_comments_only(base, git('rev-parse', 'HEAD'), cwd=root))
            file.unlink(); file.symlink_to('DB_HOST=localhost')
            git('add', '.'); git('commit', '-m', 'symlink')
            self.assertFalse(v.env_comments_only(base, git('rev-parse', 'HEAD'), cwd=root))
            file.unlink(); git('add', '.'); git('commit', '-m', 'deleted')
            deleted = git('rev-parse', 'HEAD')
            self.assertFalse(v.env_comments_only(base, deleted, cwd=root))
            self.assertFalse(v.env_comments_only(deleted, head, cwd=root))
            self.assertFalse(v.env_comments_only('HEAD', head, cwd=root))

