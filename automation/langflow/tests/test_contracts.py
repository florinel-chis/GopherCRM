from copy import deepcopy
from dataclasses import replace
import unittest

import test_runner as fixtures
from gophercrm_runner.config import GateError


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


for _name in list(fixtures.EngineTests.__dict__):
    if _name.startswith('test_'):
        setattr(ContractTests, _name, None)
