from dataclasses import asdict, replace
import hashlib
import json
import os
import unittest
from unittest.mock import patch

import test_runner as fixtures
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
        with patch.dict(os.environ, {}, clear=True), self.assertRaises(GateError):
            await runner.validation_checks(record, ['README.md'])

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

    async def test_legacy_policy_hash_remains_compatible_and_mode_change_invalidates(self):
        policy = asdict(self.config)
        for key in ('issues', 'agent_timeout_seconds', 'test_timeout_seconds', 'ci_timeout_seconds',
                    'ci_poll_seconds', 'max_provider_calls', 'issue_timeout_seconds', 'validation_mode'):
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
