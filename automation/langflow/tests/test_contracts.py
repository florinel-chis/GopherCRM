from copy import deepcopy
from dataclasses import replace
import os
import unittest
from unittest.mock import patch

import test_runner as fixtures
from gophercrm_runner.config import DATABASE_CHECKS, GateError


def manifest():
    return dict(schema_version=1, repository='example/repo', issue=95,
                objective='A scoped behavior is correct', non_goals=['Unrelated UI changes'],
                scope_paths=['internal/**', 'docs/**'], dependencies=[],
                validation_profile='backend', context_refs=['AGENTS.md'],
                acceptance=[dict(id='AC-1', requirement='Named regression passes',
                    verification=dict(kind='automated', check_id='regression'))])


class ContractTests(fixtures.EngineTests):
    async def test_contract_rejects_untrusted_commands_unknown_fields_and_unsupported_evidence(self):
        from gophercrm_runner.contracts import create_contract
        config = replace(self.config, acceptance_checks={'regression': {
            'argv': ['go', 'test', '-json', '-count=1', './internal/integration'],
            'tests': [{'package': 'example/repo/internal/integration', 'name': 'TestRegression'}]}})
        issue = dict(number=95, title='A task', body='Acceptance')
        docs = [dict(path='AGENTS.md', origin='git', content='Repository guidance')]
        for change in [lambda m: m.update(command=['sh', '-c', 'echo bypass']),
                       lambda m: m.update(validation_profile='ui'),
                       lambda m: m['acceptance'].append(deepcopy(m['acceptance'][0])),
                       lambda m: m['acceptance'][0]['verification'].update(kind='manual'),
                       lambda m: m['acceptance'][0]['verification'].update(check_id='unknown'),
                       lambda m: m.update(scope_paths=['../outside']),
                       lambda m: m.update(dependencies=[dict(issue=95, pr=1, merge_commit='a' * 40)])]:
            raw = manifest(); change(raw)
            with self.subTest(raw=raw), self.assertRaises(GateError):
                create_contract(config, raw, issue, self.commands.base, docs)

    async def test_frozen_inputs_are_deterministic_and_detect_issue_guidance_or_base_changes(self):
        from gophercrm_runner.contracts import create_contract, verify_current
        config = replace(self.config, acceptance_checks={'regression': {
            'argv': ['go', 'test', '-json', '-count=1', './internal/integration'],
            'tests': [{'package': 'example/repo/internal/integration', 'name': 'TestRegression'}]}})
        issue = dict(number=95, title='A task', body='Acceptance')
        docs = [dict(path='AGENTS.md', origin='git', content='Repository guidance')]
        frozen = create_contract(config, manifest(), issue, self.commands.base, docs)
        self.assertEqual(frozen, create_contract(config, manifest(), issue, self.commands.base, docs))
        verify_current(frozen, issue, self.commands.base, docs)
        for changed_issue, base, changed_docs in [
            (dict(issue, body='Changed acceptance'), self.commands.base, docs),
            (issue, 'b' * 40, docs),
            (issue, self.commands.base, [dict(docs[0], content='Changed guidance')]),
        ]:
            with self.assertRaisesRegex(GateError, 'changed|refresh'):
                verify_current(frozen, changed_issue, base, changed_docs)

    async def test_scope_never_permits_frontend_edits_in_backend_mvp(self):
        from gophercrm_runner.contracts import check_scope
        frozen = dict(scope_paths=['internal/**', 'gocrm-ui/**'], validation_profile='backend')
        check_scope(frozen, ['internal/handler/dashboard_handler.go'])
        for paths in [['other.txt'], ['gocrm-ui/src/pages/LeadList.tsx'], ['internal/handler/assets/form.js']]:
            with self.assertRaises(GateError):
                check_scope(frozen, paths)


VITEST_CHECK = {
    'kind': 'vitest',
    'argv': ['npm', '--prefix', 'gocrm-ui', 'test', '--silent', '--', '--run', '--reporter=json',
             'src/pages/deals/DealList.test.tsx'],
    'tests': [{'file': 'src/pages/deals/DealList.test.tsx', 'name': 'DealList renders the empty state'}]}
GO_CHECK = {'argv': ['go', 'test', '-json', '-count=1', './internal/integration'],
            'tests': [{'package': 'example/repo/internal/integration', 'name': 'TestRegression'}]}


def frontend_manifest():
    raw = manifest()
    raw.update(validation_profile='frontend', scope_paths=['gocrm-ui/src/pages/deals/**', 'docs/**'],
               acceptance=[dict(id='AC-1', requirement='The deal list renders its empty state',
                                verification=dict(kind='automated', check_id='ui-deals'))])
    return raw


class FrontendContractTests(fixtures.EngineTests):
    def frontend_config(self, **changes):
        return replace(self.config, database_test_commands=DATABASE_CHECKS,
                       acceptance_checks={'ui-deals': VITEST_CHECK, 'regression': GO_CHECK}, **changes)

    async def test_frontend_profile_requires_vitest_checks_and_database_validation(self):
        from gophercrm_runner.contracts import create_contract, validate_manifest
        config = self.frontend_config()
        issue = dict(number=95, title='A task', body='Acceptance')
        docs = [dict(path='AGENTS.md', origin='git', content='Repository guidance')]
        with patch.dict(os.environ, TEST_MYSQL_DSN='test-dsn'):
            frozen = create_contract(config, frontend_manifest(), issue, self.commands.base, docs)
        self.assertEqual(frozen['validation_profile'], 'frontend')
        with self.assertRaisesRegex(GateError, 'Frontend profile requires configured database validation'):
            validate_manifest(replace(config, database_test_commands=()), frontend_manifest(), 95)
        with patch.dict(os.environ, {}, clear=True), self.assertRaisesRegex(GateError, 'TEST_MYSQL_DSN'):
            create_contract(config, frontend_manifest(), issue, self.commands.base, docs)
        mixed = frontend_manifest()
        mixed['acceptance'].append(dict(id='AC-2', requirement='Go regression',
                                        verification=dict(kind='automated', check_id='regression')))
        with self.assertRaisesRegex(GateError, 'Frontend profile requires Vitest acceptance checks'):
            validate_manifest(config, mixed, 95)
        for profile in ['backend', 'backend-database']:
            backend = manifest()
            backend.update(validation_profile=profile, acceptance=frontend_manifest()['acceptance'])
            with self.subTest(profile=profile), \
                    self.assertRaisesRegex(GateError, 'Backend profiles require Go acceptance checks'):
                validate_manifest(config, backend, 95)
        for profile in ['ui', 'manual', 'Frontend']:
            with self.subTest(profile=profile), self.assertRaisesRegex(GateError, 'Unsupported profile'):
                validate_manifest(config, dict(frontend_manifest(), validation_profile=profile), 95)

    async def test_existing_backend_contract_hashes_are_unchanged_by_an_optional_kind(self):
        from gophercrm_runner.contracts import check_registry_hash, digest
        config = self.frontend_config()
        config.validate()
        self.assertNotIn('kind', config.acceptance_checks['regression'])
        raw = dict(manifest(), acceptance=[dict(id='AC-1', requirement='r',
                                                verification=dict(kind='automated', check_id='regression'))])
        # The digest pinned before the kind field existed; frozen contracts keep their checks_hash.
        self.assertEqual(check_registry_hash(config, raw),
                         '52c4e698dd2092e64e0489fa9c2f81b86b2881441ad301af5df36279c3a45f3f')
        self.assertEqual(digest({'regression': GO_CHECK}), check_registry_hash(config, raw))

    async def test_frontend_scope_admits_only_the_ui_and_documentation(self):
        from gophercrm_runner.contracts import check_scope
        frozen = dict(validation_profile='frontend',
                      scope_paths=['gocrm-ui/src/**', 'docs/**', 'README.md', 'CHANGELOG.md',
                                   'internal/**', 'internal/handler/assets/*', '*'])
        check_scope(frozen, ['gocrm-ui/src/pages/deals/DealList.tsx', 'gocrm-ui/src/pages/deals/DealList.test.tsx',
                             'docs/FEATURES.md', 'README.md', 'CHANGELOG.md'])
        for path in ['internal/handler/deal_handler.go', 'internal/handler/assets/form_embed.js',
                     'cmd/main.go', 'go.mod', 'Makefile', 'AGENTS.md', 'gocrm-ui/../internal/x.go']:
            with self.subTest(path=path), self.assertRaises(GateError):
                check_scope(frozen, [path])
        with self.assertRaisesRegex(GateError, 'outside frozen task scope'):
            check_scope(dict(frozen, scope_paths=['gocrm-ui/src/pages/deals/**']),
                        ['gocrm-ui/src/pages/leads/LeadList.tsx'])
        with self.assertRaisesRegex(GateError, 'Frontend edits require the frontend profile'):
            check_scope(dict(frozen, validation_profile='backend-database'),
                        ['gocrm-ui/src/pages/deals/DealList.tsx'])


for _name in list(fixtures.EngineTests.__dict__):
    if _name.startswith('test_'):
        setattr(ContractTests, _name, None)
        setattr(FrontendContractTests, _name, None)
