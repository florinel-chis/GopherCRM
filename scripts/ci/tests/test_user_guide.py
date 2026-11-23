"""Tests for the user-guide name check (scripts/ci/check-user-guide.py).

The real guide must pass against the real repository; each token class is shown
to report an unknown name in a small fixture guide, and to pass once the name
is right; a missing page and a class with no token fail. Webhook event types
resolve from the event catalogue, and the sign-in audit page must name exactly
the methods and reason codes of the Go vocabulary.
"""

import contextlib
import importlib.util
import io
from pathlib import Path
import shutil
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


def run(guide_dir, repo_root=ROOT):
    out = io.StringIO()
    with contextlib.redirect_stdout(out):
        code = gate.main(['--guide-dir', str(guide_dir), '--repo-root', str(repo_root)])
    return code, out.getvalue()


SCIM_ROUTES = {
    ('GET', '/scim/v2/ServiceProviderConfig'),
    ('GET', '/scim/v2/Schemas'),
    ('GET', '/scim/v2/ResourceTypes'),
    ('GET', '/scim/v2/Users'),
    ('GET', '/scim/v2/Users/{id}'),
    ('POST', '/scim/v2/Users'),
    ('PUT', '/scim/v2/Users/{id}'),
    ('PATCH', '/scim/v2/Users/{id}'),
    ('DELETE', '/scim/v2/Users/{id}'),
    ('GET', '/scim/v2/Groups'),
    ('GET', '/scim/v2/Groups/{id}'),
    ('PATCH', '/scim/v2/Groups/{id}'),
    ('POST', '/scim/v2/Groups'),
    ('DELETE', '/scim/v2/Groups/{id}'),
}


# The twenty-one event types of the shipped webhook catalogue, written out so
# the parser is checked against a list it did not produce.
WEBHOOK_EVENT_TYPES = {
    '%s.%s' % (entity, action)
    for entity in ('lead', 'customer', 'ticket', 'deal', 'task', 'company', 'quote')
    for action in ('created', 'updated', 'deleted')
}


def audit_page(drop=(), extra=''):
    """A sign-in audit page whose section names every method and reason code except drop."""
    vocabulary, problems = gate.auth_audit_vocabulary(ROOT)
    assert not problems, problems
    codes = sorted((vocabulary['method'] | vocabulary['reason']) - set(drop))
    return '# Sign-in audit\n\n%s\n\n%s\n%s\n## Next\n\n`not_checked_here`\n' % (
        gate.AUDIT_SECTION, '\n'.join('- `%s`' % code for code in codes), extra)


class UserGuideCheckTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def write_guide(self, extra='', base=BASE_PAGE):
        for page in gate.PAGES:
            (self.dir / page).write_text('# %s\n' % page, encoding='utf-8')
        (self.dir / gate.AUDIT_PAGE).write_text(audit_page(), encoding='utf-8')
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

    def test_testing_page_is_checked(self):
        self.assertIn('testing-single-sign-on.md', gate.PAGES)

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

    def test_scim_page_is_checked(self):
        self.assertIn('scim-provisioning.md', gate.PAGES)

    def test_known_scim_names_pass(self):
        self.write_guide('\n`GET /scim/v2/Users`, `PATCH /scim/v2/Users/{id}`, `PUT /scim/v2/Users/:id`, '
                         '`DELETE /scim/v2/Users/{id}`, `GET /scim/v2/Groups`, `GET /scim/v2/Groups/{id}`, '
                         '`PATCH /scim/v2/Groups/:id`, `POST /scim/v2/Groups`, `DELETE /scim/v2/Groups/{id}`, '
                         '`/scim/v2/Groups`, '
                         '`GET /scim/v2/ServiceProviderConfig`, `PUBLIC_BASE_URL/scim/v2`, `/scim/v2/Users`.\n')
        code, out = run(self.dir)
        self.assertEqual(code, 0, out)

    def test_unserved_scim_collection_delete_is_unknown_route(self):
        # DELETE is served on a single user (#504), never on the collection.
        self.assert_fails_naming('\n`DELETE /scim/v2/Users`\n', 'DELETE /scim/v2/Users', 'route')

    def test_scim_groups_put_is_unknown_route(self):
        # #506 serves the Groups, but PUT /Groups/{id} is not routed.
        self.assert_fails_naming('\n`PUT /scim/v2/Groups/{id}`\n', 'PUT /scim/v2/Groups/{id}', 'route')

    def test_scim_bulk_is_unknown_path(self):
        self.assert_fails_naming('\n`/scim/v2/Bulk`\n', '/scim/v2/Bulk', 'path')

    def test_scim_token_routes_are_not_scim_routes(self):
        self.assert_fails_naming('\n`GET /scim/v2/settings/scim/tokens`\n',
                                 'GET /scim/v2/settings/scim/tokens', 'route')

    def test_scim_routes_under_api_prefix_do_not_resolve(self):
        self.assert_fails_naming('\n`GET /api/v1/scim/v2/Users`\n', 'GET /api/v1/scim/v2/Users', 'route')

    def test_parsed_scim_routes_are_exactly_setup_scim_routes(self):
        routes, problems = gate.scim_routes(ROOT)
        self.assertEqual(problems, [])
        self.assertEqual(routes, SCIM_ROUTES)

    def fake_root(self, scim_routes_go=None, main_go=None, catalog_go=None):
        root = self.dir / 'root'
        for rel in ('api/swagger.json', 'gocrm-ui/src/routes/index.tsx', gate.SCIM_ROUTES_FILE, gate.SCIM_PREFIX_FILE,
                    gate.WEBHOOK_CATALOGUE_FILE, gate.AUDIT_VOCABULARY_FILE):
            (root / rel).parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / rel, root / rel)
        if scim_routes_go is not None:
            (root / gate.SCIM_ROUTES_FILE).write_text(scim_routes_go, encoding='utf-8')
        if main_go is not None:
            (root / gate.SCIM_PREFIX_FILE).write_text(main_go, encoding='utf-8')
        if catalog_go is not None:
            (root / gate.WEBHOOK_CATALOGUE_FILE).write_text(catalog_go, encoding='utf-8')
        return root

    def test_root_without_setup_scim_routes_fails(self):
        root = self.fake_root(scim_routes_go='package handler\n\nfunc SetupSCIMTokenRoutes() {\n}\n')
        self.write_guide()
        code, out = run(self.dir, root)
        self.assertEqual(code, 1, out)
        self.assertIn('no func SetupSCIMRoutes', out)

    def test_setup_scim_routes_without_routes_fails(self):
        root = self.fake_root(scim_routes_go='package handler\n\nfunc SetupSCIMRoutes() {\n}\n')
        self.write_guide()
        code, out = run(self.dir, root)
        self.assertEqual(code, 1, out)
        self.assertIn('SetupSCIMRoutes registers no route', out)

    def test_root_without_scim_prefix_fails(self):
        root = self.fake_root(main_go='package main\n')
        self.write_guide()
        code, out = run(self.dir, root)
        self.assertEqual(code, 1, out)
        self.assertIn('no router.Group("/scim/v2")', out)

    def test_class_with_no_tokens_fails(self):
        without_route = BASE_PAGE.replace('`GET /roles/audit`', 'the audit route')
        self.write_guide(base=without_route)
        code, out = run(self.dir)
        self.assertEqual(code, 1, out)
        self.assertIn('no route token found', out)

    def test_new_pages_are_checked(self):
        for page in ('api-keys.md', 'sign-in-audit.md', 'webhooks.md'):
            self.assertIn(page, gate.PAGES)

    def test_parsed_event_types_are_the_catalogue(self):
        types, problems = gate.webhook_event_types(ROOT)
        self.assertEqual(problems, [])
        self.assertEqual(types, WEBHOOK_EVENT_TYPES)

    def test_known_event_types_pass(self):
        self.write_guide('\n`deal.created`, `quote.deleted`, `company.updated`.\n')
        code, out = run(self.dir)
        self.assertEqual(code, 0, out)

    def test_unknown_event_type(self):
        self.assert_fails_naming('\n`lead.archived`\n', 'lead.archived', 'dotted key')

    def test_catalogue_without_default_fails(self):
        catalog = (ROOT / gate.WEBHOOK_CATALOGUE_FILE).read_text(encoding='utf-8')
        without_default = catalog.replace('var defaultCatalogue = NewCatalogue(', 'var otherCatalogue = NewCatalogue(')
        self.assertNotEqual(catalog, without_default)
        root = self.fake_root(catalog_go=without_default)
        self.write_guide()
        code, out = run(self.dir, root)
        self.assertEqual(code, 1, out)
        self.assertIn('no var defaultCatalogue', out)

    def assert_audit_page_fails(self, page, *expected):
        self.write_guide()
        (self.dir / gate.AUDIT_PAGE).write_text(page, encoding='utf-8')
        code, out = run(self.dir)
        self.assertEqual(code, 1, out)
        for line in expected:
            self.assertIn(line, out)
        return out

    def test_unknown_sign_in_audit_code(self):
        self.assert_audit_page_fails(audit_page(extra='- `not_a_reason`\n'),
                                     'unknown sign-in audit code `not_a_reason`')

    def test_missing_sign_in_audit_code(self):
        out = self.assert_audit_page_fails(audit_page(drop=('token_reuse',)),
                                           'missing sign-in audit code `token_reuse`')
        self.assertEqual(out.count('sign-in audit code'), 1, out)

    def test_sign_in_audit_page_without_section_fails(self):
        page = audit_page().replace(gate.AUDIT_SECTION, '## Codes')
        self.assert_audit_page_fails(page, 'no "%s" section' % gate.AUDIT_SECTION)


if __name__ == '__main__':
    unittest.main()
