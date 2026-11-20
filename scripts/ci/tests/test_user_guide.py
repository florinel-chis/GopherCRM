"""Tests for the user-guide name check (scripts/ci/check-user-guide.py).

The real guide must pass against the real repository; each token class is shown
to report an unknown name in a small fixture guide, and to pass once the name
is right; a missing page and a class with no token fail.
"""

import contextlib
import importlib.util
import io
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]
spec_loader = importlib.util.spec_from_file_location('check_user_guide', ROOT / 'scripts' / 'ci' / 'check-user-guide.py')
gate = importlib.util.module_from_spec(spec_loader)
spec_loader.loader.exec_module(gate)

# One known token of every class; the fixtures below replace one of them.
BASE_PAGE = """# Fixture

Set `OIDC_CLIENT_SECRET` and `security.sso.allow_password`; read `GET /roles/audit`
and open `/settings/sso`. The redirect URI is `APP_BASE_URL/auth/callback`.

```
OIDC_ENABLED=true
```
"""


def run(guide_dir):
    out = io.StringIO()
    with contextlib.redirect_stdout(out):
        code = gate.main(['--guide-dir', str(guide_dir), '--repo-root', str(ROOT)])
    return code, out.getvalue()


class UserGuideCheckTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def write_guide(self, extra='', base=BASE_PAGE):
        for page in gate.PAGES:
            (self.dir / page).write_text('# %s\n' % page, encoding='utf-8')
        (self.dir / 'overview.md').write_text(base + extra, encoding='utf-8')

    def assert_fails_naming(self, extra, token, kind):
        self.write_guide(extra)
        code, out = run(self.dir)
        self.assertEqual(code, 1, out)
        self.assertIn('`%s`' % token, out)
        self.assertIn('unknown %s' % kind, out)
        self.assertIn('overview.md', out)

    def test_real_guide_passes(self):
        code, out = run(ROOT / gate.DEFAULT_GUIDE_DIR)
        self.assertEqual(code, 0, out)
        self.assertTrue(out.startswith('OK'), out)

    def test_fixture_with_known_names_passes(self):
        self.write_guide('\n`OIDC_GRAPH_MAX_PAGES`, `roles.audit_view`, `PUT /settings/sso`, '
                         '`POST /roles/{id}/clone`, `/settings/roles`.\n')
        code, out = run(self.dir)
        self.assertEqual(code, 0, out)
        self.assertIn('OK', out)

    def test_unknown_environment_variable(self):
        self.assert_fails_naming('\n`OIDC_NOT_A_SETTING`\n', 'OIDC_NOT_A_SETTING', 'constant')

    def test_unknown_environment_variable_in_fenced_block(self):
        self.assert_fails_naming('\n```\nOIDC_NOT_A_SETTING=1\n```\n', 'OIDC_NOT_A_SETTING', 'constant')

    def test_unknown_setting_key(self):
        self.assert_fails_naming('\n`security.sso.not_a_key`\n', 'security.sso.not_a_key', 'dotted key')

    def test_unknown_route(self):
        self.assert_fails_naming('\n`POST /roles/{id}/not-a-route`\n', 'POST /roles/{id}/not-a-route', 'route')

    def test_known_path_with_wrong_method(self):
        self.assert_fails_naming('\n`DELETE /settings/sso`\n', 'DELETE /settings/sso', 'route')

    def test_unknown_spa_path(self):
        self.assert_fails_naming('\n`/settings/not-a-page`\n', '/settings/not-a-page', 'path')

    def test_route_with_api_prefix_and_colon_parameter_resolves(self):
        self.write_guide('\n`POST /api/v1/users/:id/deactivate`\n')
        code, out = run(self.dir)
        self.assertEqual(code, 0, out)

    def test_file_names_are_not_keys(self):
        self.write_guide('\n`single.not_a_page.md`\n')
        code, out = run(self.dir)
        self.assertEqual(code, 0, out)

    def test_missing_page_fails(self):
        self.write_guide()
        (self.dir / 'faq.md').unlink()
        code, out = run(self.dir)
        self.assertEqual(code, 1, out)
        self.assertIn('faq.md: missing page', out)

    def test_empty_directory_reports_every_page(self):
        code, out = run(self.dir)
        self.assertEqual(code, 1, out)
        for page in gate.PAGES:
            self.assertIn('%s: missing page' % page, out)

    def test_class_with_no_tokens_fails(self):
        without_route = BASE_PAGE.replace('`GET /roles/audit`', 'the audit route')
        self.write_guide(base=without_route)
        code, out = run(self.dir)
        self.assertEqual(code, 1, out)
        self.assertIn('no route token found', out)


if __name__ == '__main__':
    unittest.main()
