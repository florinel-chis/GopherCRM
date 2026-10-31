from dataclasses import asdict, replace
import hashlib
import json
import os
import unittest
from unittest.mock import patch

import test_runner as fixtures
from test_contracts import VITEST_CHECK, frontend_manifest
from gophercrm_runner.config import DATABASE_CHECKS, GateError
from gophercrm_runner.engine import Runner
from gophercrm_runner.pull_requests import PullRequestRunner
from gophercrm_runner.validation import SMOKE


class ValidationSelectionTests(fixtures.EngineTests):
    def selected_runner(self):
        config = replace(self.config, validation_mode='contextual',
                         database_test_commands=DATABASE_CHECKS)
        runner = Runner(config, self.commands)
        record = {'issue': 95, 'worktree': str(self.checkout)}
        for name in [SMOKE, 'e2e/tests/admin-tickets.spec.ts', 'e2e/tests/admin-entity-suite.spec.ts']:
            path = self.checkout / 'gocrm-ui' / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('// fixture spec\n')
        return runner, record

    async def test_named_acceptance_runs_before_contextual_database_checks(self):
        runner, record = self.selected_runner()
        with patch.dict(os.environ, TEST_MYSQL_DSN='test-dsn'):
            checks = await runner.validation_checks(record, ['internal/repository/ticket_repository.go'])
        ids = [c[0] for c in checks]
        self.assertLess(ids.index('acceptance-regression'), ids.index('database-0'))
        self.assertIn('SPECS=', checks[-3][1][-1])
        self.assertEqual(checks[-1][1], DATABASE_CHECKS[-1])
        self.assertEqual(checks[0][1], ('make', 'verify'))

    async def test_pr_and_ticket_use_identical_browser_selection(self):
        runner, record = self.selected_runner()
        pr = PullRequestRunner(runner.config, self.commands)
        record['criteria'] = []
        with patch.dict(os.environ, TEST_MYSQL_DSN='test-dsn'):
            ticket = await runner.validation_checks(record, ['internal/repository/ticket_repository.go'])
            external = await pr.validation_checks(record, ['internal/repository/ticket_repository.go'])
        self.assertEqual([c for c in ticket if c[0].startswith('database-')],
                         [c for c in external if c[0].startswith('database-')])

    async def test_runner_only_keeps_acceptance_and_runner_checks_without_full_matrix(self):
        runner, record = self.selected_runner()
        pr = PullRequestRunner(runner.config, self.commands)
        record['criteria'] = []
        paths = ['automation/langflow/gophercrm_runner/batches.py', '.github/workflows/ci.yml']
        with patch.dict(os.environ, {}, clear=True):
            checks = await pr.validation_checks(record, paths)
        self.assertEqual([c[1] for c in checks], [
            ('make', 'verify-hygiene'), ('make', 'verify-api-docs'), ('make', 'verify-runner'),
            ('npm', 'ci', '--prefix', 'gocrm-ui'),
            ('make', 'e2e', 'E2E_DB_DRIVER=sqlite', 'SPECS=' + SMOKE)])
        # Direct check assembly still retains every frozen acceptance root.
        with patch.dict(os.environ, {}, clear=True):
            checks = await runner.validation_checks(record, paths)
        self.assertIn('acceptance-regression', [c[0] for c in checks])
        self.assertFalse(any(c[0].startswith('database-') for c in checks))

    async def test_missing_selected_spec_requires_full_matrix(self):
        runner, record = self.selected_runner()
        (self.checkout / 'gocrm-ui' / SMOKE).unlink()
        with patch.dict(os.environ, TEST_MYSQL_DSN='test-dsn'):
            checks = await runner.validation_checks(record, ['internal/repository/ticket_repository.go'])
        self.assertEqual([c[1] for c in checks if c[0].startswith('database-')], list(DATABASE_CHECKS))
        with patch.dict(os.environ, {}, clear=True), self.assertRaises(GateError):
            await runner.validation_checks(record, ['unknown.file'])

    async def test_frontend_context_gets_smoke_even_without_database_changes(self):
        runner, record = self.selected_runner()
        checks = runner.browser_validation_checks(record, ['gocrm-ui/src/pages/tickets/TicketList.tsx'])
        self.assertEqual(checks[0][0], 'browser-0')
        self.assertIn('E2E_DB_DRIVER=sqlite', checks[0][1])
        self.assertIn(SMOKE, checks[0][1][-1])

    async def test_auth_uses_targeted_checks_and_preserves_explicit_database_contracts(self):
        runner, record = self.selected_runner()
        for name in ['login', 'registration']:
            (self.checkout / 'gocrm-ui/e2e/tests' / (name + '.spec.ts')).write_text('// fixture\n')
        paths = ['internal/service/auth_service.go', 'tests/auth_session_integration_test.go']
        with patch.dict(os.environ, {}, clear=True):
            checks = await runner.validation_checks(record, paths)
        self.assertIn(('python3', 'scripts/ci/check-auth.py'), [c[1] for c in checks])
        self.assertNotIn(('make', 'verify'), [c[1] for c in checks])
        self.assertIn('acceptance-regression', [c[0] for c in checks])
        self.assertEqual(checks[-1][0], 'browser-0')
        self.assertIn('e2e/tests/login.spec.ts', checks[-1][1][-1])
        with patch.dict(os.environ, TEST_MYSQL_DSN='test-dsn'):
            database = runner.browser_validation_checks(record, paths, database_profile=True)
        self.assertEqual(database[-1][1], DATABASE_CHECKS[-1])
        self.assertIn('E2E_DB_DRIVER=mysql', database[1][1])
        (self.checkout / 'gocrm-ui/e2e/tests/login.spec.ts').unlink()
        with patch.dict(os.environ, {}, clear=True), self.assertRaises(GateError):
            await runner.validation_checks(record, paths)

    async def test_explicit_full_runs_complete_matrix_for_ui_and_docs(self):
        selected, record = self.selected_runner()
        runner = Runner(replace(selected.config, validation_mode='full'), self.commands)
        for path in ('gocrm-ui/src/pages/tickets/TicketList.tsx', 'README.md'):
            with self.subTest(path=path), patch.dict(os.environ, TEST_MYSQL_DSN='test-dsn'):
                checks = await runner.validation_checks(record, [path])
                self.assertEqual([c[1] for c in checks if c[0].startswith('database-')],
                                 list(DATABASE_CHECKS))
                ids = [c[0] for c in checks]
                self.assertLess(ids.index('acceptance-regression'), ids.index('database-0'))
                commands = [c[1] for c in checks]
                self.assertIn(('python3', 'scripts/ci/check-go.py', 'transaction'), commands)
                self.assertIn(('python3', 'scripts/ci/check-auth.py', '--persistence'), commands)
        with patch.dict(os.environ, {}, clear=True), self.assertRaises(GateError):
            await runner.validation_checks(record, ['README.md'])

    async def test_auth_persistence_scope_proof_keeps_sql_and_named_acceptance_in_both_engines(self):
        runner, record = self.selected_runner()
        record.update(base='a' * 40, head='b' * 40, criteria=[])
        for name in ['login', 'registration']:
            (self.checkout / 'gocrm-ui/e2e/tests' / (name + '.spec.ts')).write_text('// fixture\n')
        paths = ['internal/service/auth_service.go', 'internal/repository/refresh_token_repository.go',
                 'internal/repository/interfaces.go']
        with patch('gophercrm_runner.validation.refresh_interface_only', return_value=True) as proof, \
             patch('gophercrm_runner.validation.rotation_roots_present', return_value=True), \
             patch.dict(os.environ, TEST_MYSQL_DSN='test-dsn'):
            checks = await runner.validation_checks(record, paths)
            external = await PullRequestRunner(runner.config, self.commands).validation_checks(record, paths)
        proof.assert_called_with(record['base'], record['head'], cwd=record['worktree'])
        self.assertIn(('python3', 'scripts/ci/check-auth.py', '--persistence'), [c[1] for c in checks])
        self.assertNotIn(('make', 'verify'), [c[1] for c in checks])
        ids = [c[0] for c in checks]
        self.assertLess(ids.index('acceptance-regression'), ids.index('database-0'))
        database = [c for c in checks if c[0].startswith('database-')]
        self.assertEqual(database, [c for c in external if c[0].startswith('database-')])
        self.assertEqual(database[-1][1], DATABASE_CHECKS[-1])
        for check in database[:2]:
            self.assertEqual(set(check[1][-1].removeprefix('SPECS=').split()),
                             {SMOKE, 'e2e/tests/login.spec.ts', 'e2e/tests/registration.spec.ts'})
        with patch('gophercrm_runner.validation.refresh_interface_only', return_value=False):
            self.assertEqual(runner.validation_selection(record, paths)['profile'], 'full')
        with patch('gophercrm_runner.validation.refresh_interface_only', return_value=True), \
             patch('gophercrm_runner.validation.rotation_roots_present', return_value=True), \
             patch.dict(os.environ, {}, clear=True), self.assertRaises(GateError):
            await runner.validation_checks(record, paths)

    async def test_transaction_profile_keeps_database_and_acceptance_without_full_browsers(self):
        runner, record = self.selected_runner()
        paths = ['internal/utils/transaction.go']
        with patch.dict(os.environ, TEST_MYSQL_DSN='test-dsn'):
            checks = await runner.validation_checks(record, paths)
            pr = PullRequestRunner(runner.config, self.commands)
            record['criteria'] = []
            external = await pr.validation_checks(record, paths)
        self.assertIn(('python3', 'scripts/ci/check-go.py', 'transaction'), [c[1] for c in checks])
        self.assertNotIn(('make', 'verify'), [c[1] for c in checks])
        self.assertIn('acceptance-regression', [c[0] for c in checks])
        database = [c for c in checks if c[0].startswith('database-')]
        self.assertEqual(database, [c for c in external if c[0].startswith('database-')])
        self.assertEqual(database[0][1][-1], 'SPECS=' + SMOKE)
        self.assertEqual(database[1][1][-1], 'SPECS=' + SMOKE)
        self.assertEqual(database[-1][1], DATABASE_CHECKS[-1])

    async def test_migration_workflow_uses_sql_checker_and_health_in_ticket_and_pr(self):
        runner, record = self.selected_runner()
        paths = ['.github/workflows/migrations.yml', 'scripts/release/check-sql-tests.py']
        ticket = await runner.validation_checks(record, paths)
        record['criteria'] = []
        pr = await PullRequestRunner(runner.config, self.commands).validation_checks(record, paths)
        for checks in (ticket, pr):
            self.assertIn(('python3', 'scripts/ci/check-go.py', 'migration'), [c[1] for c in checks])
            self.assertNotIn(('make', 'verify'), [c[1] for c in checks])
            self.assertEqual(checks[-1][1], ('make', 'e2e', 'E2E_DB_DRIVER=sqlite', 'SPECS=' + SMOKE))
        self.assertIn('acceptance-regression', [c[0] for c in ticket])

    async def test_full_mode_requires_database_matrix_and_pr_uses_it_too(self):
        with self.assertRaisesRegex(GateError, 'requires database_test_commands'):
            Runner(replace(self.config, validation_mode='full'), self.commands)
        selected, record = self.selected_runner()
        config = replace(selected.config, validation_mode='full')
        runner = PullRequestRunner(config, self.commands)
        record['criteria'] = []
        with patch.dict(os.environ, TEST_MYSQL_DSN='test-dsn'):
            checks = await runner.validation_checks(record, ['README.md'])
        self.assertEqual([c[1] for c in checks if c[0].startswith('database-')], list(DATABASE_CHECKS))

    async def frontend_runner(self, number=96):
        selected, record = self.selected_runner()
        config = replace(selected.config, acceptance_checks=dict(selected.config.acceptance_checks,
                                                                 **{'ui-deals': VITEST_CHECK}))
        runner = Runner(config, self.commands)
        raw = dict(frontend_manifest(), issue=number)
        path = self.root / f'frontend-{number}.json'
        path.write_text(json.dumps(raw))
        with patch.dict(os.environ, TEST_MYSQL_DSN='test-dsn'):
            await runner.freeze(number, path, refresh=True)
        return runner, dict(record, issue=number)

    async def test_frontend_contract_requires_selected_browser_matrix_and_vitest_evidence(self):
        runner, record = await self.frontend_runner()
        paths = ['gocrm-ui/src/pages/tickets/TicketList.tsx']
        with patch.dict(os.environ, TEST_MYSQL_DSN='test-dsn'):
            checks = await runner.validation_checks(record, paths)
        ids = [c[0] for c in checks]
        self.assertEqual(checks[0][1], ('make', 'verify'))
        self.assertLess(ids.index('acceptance-ui-deals'), ids.index('database-0'))
        acceptance = checks[ids.index('acceptance-ui-deals')]
        self.assertEqual(acceptance[1], tuple(VITEST_CHECK['argv']))
        self.assertEqual(acceptance[2], VITEST_CHECK['tests'])
        self.assertEqual(acceptance[2].kind, 'vitest')
        database = [c for c in checks if c[0].startswith('database-')]
        self.assertEqual(len(database), len(DATABASE_CHECKS))
        self.assertIn('e2e/tests/admin-tickets.spec.ts', database[0][1][-1])
        self.assertIn('E2E_DB_DRIVER=mysql', database[1][1])
        self.assertEqual(database[-1][1], DATABASE_CHECKS[-1])
        self.assertNotIn('browser-0', ids)
        # The same UI path under a backend contract keeps the SQLite smoke only.
        backend = await runner.validation_checks(dict(record, issue=95), paths)
        self.assertEqual([c[0] for c in backend if c[0].startswith(('browser-', 'database-'))], ['browser-0'])
        self.assertEqual(getattr(backend[[c[0] for c in backend].index('acceptance-regression')][2],
                                 'kind', 'go'), 'go')
        with patch.dict(os.environ, {}, clear=True), self.assertRaisesRegex(GateError, 'TEST_MYSQL_DSN'):
            await runner.validation_checks(record, paths)

    async def test_frontend_contract_without_a_selection_falls_back_to_the_full_matrix(self):
        runner, record = await self.frontend_runner()
        (self.checkout / 'gocrm-ui' / SMOKE).unlink()
        with patch.dict(os.environ, TEST_MYSQL_DSN='test-dsn'):
            checks = await runner.validation_checks(record, ['gocrm-ui/src/pages/tickets/TicketList.tsx'])
        self.assertEqual([c[1] for c in checks if c[0].startswith('database-')], list(DATABASE_CHECKS))

    async def test_frontend_freeze_requires_mysql_validation_environment(self):
        selected, _ = self.selected_runner()
        runner = Runner(replace(selected.config, acceptance_checks={'ui-deals': VITEST_CHECK}), self.commands)
        path = self.root / 'frontend.json'
        path.write_text(json.dumps(dict(frontend_manifest(), issue=96)))
        with patch.dict(os.environ, {}, clear=True), self.assertRaisesRegex(GateError, 'TEST_MYSQL_DSN'):
            await runner.freeze(96, path, refresh=True)
        bare = Runner(replace(self.config, acceptance_checks={'ui-deals': VITEST_CHECK}), self.commands)
        with patch.dict(os.environ, TEST_MYSQL_DSN='test-dsn'), \
                self.assertRaisesRegex(GateError, 'database validation'):
            await bare.freeze(96, path, refresh=True)

    async def test_pr_completion_of_a_frontend_ticket_keeps_its_database_and_vitest_checks(self):
        runner, record = await self.frontend_runner()
        pr = PullRequestRunner(runner.config, self.commands)
        frozen = json.loads((runner.config.state_dir / 'tasks/96/contract.json').read_text())
        record.update(criteria=frozen['acceptance'], database_profile=True)
        with patch.dict(os.environ, TEST_MYSQL_DSN='test-dsn'):
            checks = await pr.validation_checks(record, ['gocrm-ui/src/pages/tickets/TicketList.tsx'])
        acceptance = [c for c in checks if c[0] == 'acceptance-ui-deals']
        self.assertEqual(acceptance[0][2].kind, 'vitest')
        self.assertEqual(len([c for c in checks if c[0].startswith('database-')]), len(DATABASE_CHECKS))

    async def test_frontend_prompt_states_named_vitest_lint_build_and_dependency_rules(self):
        runner, record = await self.frontend_runner()
        issue = dict(number=96, title='Deal list', body='Acceptance')
        prompt = runner.prompt(issue, record)
        self.assertIn('FRONTEND PROFILE', prompt)
        self.assertIn('src/pages/deals/DealList.test.tsx', prompt)
        self.assertIn('exact full test name', prompt)
        self.assertIn('npm run lint', prompt)
        self.assertIn('zero warnings', prompt)
        self.assertIn('npm run build', prompt)
        self.assertIn('package.json', prompt)
        self.assertIn('package-lock.json', prompt)
        backend = runner.prompt(dict(issue, number=95), dict(record, issue=95))
        self.assertNotIn('FRONTEND PROFILE', backend)

    async def test_legacy_policy_hash_remains_compatible_and_mode_change_invalidates(self):
        policy = asdict(self.config)
        for key in ('issues', 'agent_timeout_seconds', 'test_timeout_seconds', 'ci_timeout_seconds',
                    'ci_poll_seconds', 'max_provider_calls', 'issue_timeout_seconds', 'validation_mode',
                    'documentation_mode', 'validation_review_required', 'analysis_output_dir',
                    'analysis_max_calls', 'analysis_context_refs', 'analysis_rules_dir'):
            policy.pop(key)
        old = hashlib.sha256(json.dumps(policy, default=str, sort_keys=True).encode()).hexdigest()
        self.assertEqual(self.runner.policy, old)
        runner, _ = self.selected_runner()
        self.assertNotEqual(runner.policy, old)
        full = Runner(replace(runner.config, validation_mode='full'), self.commands)
        self.assertNotEqual(full.policy, old)
        self.assertNotEqual(full.policy, runner.policy)


for _name in list(fixtures.EngineTests.__dict__):
    if _name.startswith('test_'):
        setattr(ValidationSelectionTests, _name, None)
